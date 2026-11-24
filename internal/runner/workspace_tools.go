package runner

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/daidr/Ariadne/internal/background"
	"github.com/daidr/Ariadne/internal/commandpolicy"
	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/jsonutil"
	"github.com/daidr/Ariadne/internal/process"
	"github.com/daidr/Ariadne/internal/realtime"
	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/sdk"
)

func (turn *turn) editable(ctx context.Context) (core.ThreadView, error) {
	if turn.Workspaces == nil || turn.conversation.Role != "thread" {
		return core.ThreadView{}, errors.New("this conversation cannot edit a workspace")
	}
	thread, err := turn.Repository.GetThread(ctx, turn.conversation.ID)
	if err == nil && (thread.ReviewOf != nil || thread.Stopped != nil) {
		err = errors.New("a review or stopped thread cannot edit a workspace")
	}
	return thread, err
}

func (turn *turn) canEditThread(ctx context.Context) bool {
	_, err := turn.editable(ctx)
	return err == nil
}

func (turn *turn) editWorkspace(ctx context.Context, thread core.ThreadView, input args) (sdk.Workspace, error) {
	name := text(input, "source")
	if name == "" {
		workspace, err := turn.Workspaces.ForThread(ctx, thread.ProjectID, thread.ID)
		if err == nil {
			turn.changes.begin(ctx, thread)
		}
		return workspace, err
	}
	id, err := turn.Workspaces.ResolveFileSource(ctx, thread.ProjectID, name, thread.ID)
	if err != nil {
		return nil, err
	}
	if id == "" {
		return nil, fmt.Errorf("no writable file source named %q", name)
	}
	workspace, err := turn.Workspaces.ForThreadSource(ctx, thread.ProjectID, thread.ID, id)
	if err == nil {
		turn.changes.begin(ctx, thread)
	}
	return workspace, err
}

func (turn *turn) writeFile(ctx context.Context, input args) (string, error) {
	thread, err := turn.editable(ctx)
	if err != nil {
		return "", err
	}
	path, err := editPath(text(input, "path"))
	if err != nil {
		return "", err
	}
	content, ok := input["content"].(string)
	if !ok || len(content) > 1<<20 {
		return "", errors.New("content must be a text string of at most 1 MiB")
	}
	workspace, err := turn.editWorkspace(ctx, thread, input)
	if err != nil {
		return "", err
	}
	sourceID := fileSourceIdentity(workspace, input)
	if turn.fileTracker().hasSeen(turn.conversation.ID, sourceID, path) {
		before, exists, err := readExistingBytes(ctx, workspace, path)
		if err != nil {
			var tooLarge *sdk.FileTooLargeError
			if errors.As(err, &tooLarge) {
				return "", changedSinceError(path)
			}
			return "", err
		}
		if exists && turn.fileTracker().changedSince(turn.conversation.ID, sourceID, path, before) {
			return "", changedSinceError(path)
		}
	}
	turn.instructionsDirty.Store(true)
	if err := workspace.WriteFile(ctx, path, content); err != nil {
		return "", err
	}
	turn.fileTracker().noteFile(turn.conversation.ID, sourceID, path, []byte(content))
	return "Wrote " + path + "." + turn.checkAfterEdit(ctx, thread, workspace, []string{path}), nil
}

func runCommandDescription(shell process.Shell) string {
	intro := "Run a shell command in the workspace."
	if name := strings.TrimSpace(shell.Name); name != "" {
		switch name {
		case "powershell":
			intro = "Run a powershell command in the workspace (Windows PowerShell syntax; use ; instead of && and avoid bash heredocs)."
		case "pwsh":
			intro = "Run a pwsh command in the workspace (PowerShell syntax; avoid bash heredocs and POSIX-only syntax)."
		case "cmd":
			intro = "Run a cmd command in the workspace (Windows cmd.exe syntax)."
		default:
			intro = fmt.Sprintf("Run a %s command in the workspace.", name)
		}
	}
	return intro + " Pass source to select a writable source. Commands covered by an approval rule require the user's answer. Long commands may continue in the background. For large logs, traces, source maps or generated bundles, use a script that prints a small targeted summary."
}

func (turn *turn) commandTools(shell process.Shell) []tool {
	readOutput := specification("read_command_output", "Read the status and next page of output of one of your background commands. If more is available, use the same handle to continue.",
		args{"handle": field("string"), "waitSeconds": args{"type": "integer", "description": "Seconds to wait for new output or completion while the command is running, up to 60."}}, []string{"handle"}, nil)
	readOutput.poll = turn.readCommand
	return []tool{
		specification("run_command", runCommandDescription(shell),
			args{"command": field("string"), "cwd": field("string"), "source": field("string"),
				"timeoutSeconds": args{"type": "integer", "description": "Maximum execution time, even after the command moves to the background."},
				"yieldSeconds":   args{"type": "integer", "description": "How long to wait for a foreground command before returning a background handle."},
				"background":     field("boolean"), "keepForUser": field("boolean")}, []string{"command"}, turn.runCommand),
		readOutput,
		specification("stop_command", "Stop one of your background commands.",
			args{"handle": field("string")}, []string{"handle"}, turn.stopCommand),
	}
}

// commandGate binds approval to this conversation, exact command and directory.
// The approval is recorded as used before the command can start.
func (turn *turn) commandGate(ctx context.Context, command, cwd, source, identity string, rules []commandpolicy.Rule) (bool, string, error) {
	verdict := commandpolicy.Decide(command, rules)
	switch verdict.Action {
	case "allow":
		return true, "", nil
	case "deny":
		return false, "", errors.New("the user's command rules deny this command")
	}
	detail := command
	if cwd != "." {
		detail = "cd " + cwd + "\n" + detail
	}
	if source != "" {
		detail = "[" + source + "]\n" + detail
	}
	binding := []string{cwd, command}
	if identity != "" {
		binding = append(binding, identity)
	}
	key, err := jsonutil.Encode(binding)
	if err != nil {
		return false, "", err
	}
	purpose := fmt.Sprintf("command:%x", sha256.Sum256(key))
	conversation := turn.conversation
	approval, err := turn.Repository.CallApproval(ctx, conversation.ProjectID, conversation.ID, purpose, func() map[string]any {
		title, yes, no := "Allow this command once?", "Run", "Don't run"
		if turn.Settings.Locale() != "en" {
			title, yes, no = "允许运行这条命令一次？", "运行", "不要"
		}
		card := map[string]any{"type": "confirm", "title": title, "tone": "neutral", "approveText": yes, "denyText": no}
		if verdict.Pattern != nil {
			card["description"] = textutil.HeadUnits(*verdict.Pattern, 500)
		}
		return card
	}, core.CallApprovalOptions{ConfirmDetail: detail})
	if err != nil {
		return false, "", err
	}
	switch approval.State {
	case "approved":
		err = turn.Repository.UseApproval(ctx, conversation.ProjectID, conversation.ID, approval.AttentionID, "command")
		return err == nil, "", err
	case "declined":
		return false, "", errors.New("the user declined this command")
	default:
		return false, "Waiting for the user's approval. End your turn until their answer arrives.", nil
	}
}

func (turn *turn) runCommand(ctx context.Context, input args) (string, error) {
	thread, err := turn.editable(ctx)
	if err != nil {
		return "", err
	}
	project, err := turn.Repository.GetProject(ctx, thread.ProjectID)
	if err != nil {
		return "", err
	}
	if slices.Contains(project.Settings.Tools, "commands") {
		return "", errors.New("commands are disabled for this project")
	}
	command, ok := input["command"].(string)
	if !ok || strings.TrimSpace(command) == "" || len(command) > 32<<10 || strings.ContainsRune(command, 0) {
		return "", errors.New("command must be nonempty shell text within 32 KiB")
	}
	cwd, err := sdk.ViewPath(text(input, "cwd"))
	if err != nil {
		return "", err
	}
	if len(cwd) > 4096 {
		return "", errors.New("cwd must be a relative workspace directory")
	}
	cwd = path.Clean(strings.ReplaceAll(cwd, "\\", "/"))
	config := turn.Settings.TurnSettings(thread.ProjectID)
	var rules []commandpolicy.Rule
	data, err := json.Marshal(config["commandRules"])
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(data, &rules); err != nil {
		return "", err
	}
	workspace, err := turn.editWorkspace(ctx, thread, input)
	if err != nil {
		return "", err
	}
	source := text(input, "source")
	identity := workspace.(interface{ SourceIdentity() string }).SourceIdentity()
	binding := identity
	if source == "" && project.FileSources == nil {
		binding = ""
	}
	if source == "" && project.FileSources != nil {
		source = workspace.Info().Label
	}
	allowed, note, err := turn.commandGate(ctx, command, cwd, source, binding, rules)
	if err != nil || !allowed {
		return note, err
	}
	turn.instructionsDirty.Store(true)
	setup, err := turn.Workspaces.SetupDone(ctx, thread.ID)
	if err != nil {
		return "", err
	}
	shell, err := turn.Workspaces.Shell(ctx, thread.ProjectID)
	if err != nil {
		return "", err
	}
	env := turn.Workspaces.Env(thread.ProjectID)
	if _, configured := env["GIT_EDITOR"]; !configured {
		env["GIT_EDITOR"] = "true"
	}
	limits, _ := config["limits"].(map[string]any)
	maximum := max(1, asNumber(limits["commandTimeoutMax"]))
	wait := asNumber(limits["commandTimeout"])
	if n, present := input["yieldSeconds"]; present {
		wait = asNumber(n)
	}
	yield := time.Duration(max(1, min(maximum, wait))) * time.Second
	var timeout time.Duration
	if n, present := input["timeoutSeconds"]; present {
		timeout = time.Duration(max(1, min(maximum, asNumber(n)))) * time.Second
	}
	// Leave space for the status, setup note and continuation message.
	limit := min(max(2000, int(asNumber(limits["commandOutput"]))), maxToolResultChars-512)
	run := func(ctx context.Context, options sdk.ExecOptions) (sdk.ExecResult, error) {
		options.Directory, options.Env, options.Shell = cwd, env, shell.Argv
		return workspace.Exec(ctx, command, options)
	}
	commands := turn.Workspaces.Background
	local := workspace.Info().Local
	if bg, _ := input["background"].(bool); bg {
		keep, _ := input["keepForUser"].(bool)
		for _, previous := range commands.List(thread.ID) {
			if previous.Result == nil && previous.Command == command && previous.CWD == cwd && previous.SourceIdentity == identity {
				stopped, err := commands.Stop(ctx, thread.ID, previous.ID)
				if err != nil || stopped == nil || stopped.Result == nil {
					return "", errors.New("the previous copy of this command has not stopped")
				}
				keep = keep || previous.Keep
			}
		}
		job, err := commands.Start(thread.ID, command, run, background.StartOptions{CWD: cwd, Keep: keep, Local: local, SourceIdentity: identity, Timeout: timeout})
		if err != nil {
			return "", err
		}
		first, err := commands.Read(ctx, thread.ID, job.ID, 2*time.Second, limit)
		return formatOutput(first, setup, err)
	}
	return turn.foreground(ctx, thread, command, cwd, setup, yield, timeout, limit, local, run, identity)
}

func (turn *turn) foreground(ctx context.Context, thread core.ThreadView, command, cwd, setup string, wait, timeout time.Duration, limit int, local bool, run background.Executor, identity string) (string, error) {
	live := realtime.LiveHandle{Output: func(string) {}, End: func() {}}
	if turn.Live != nil {
		if handle, err := turn.Live.Start(realtime.LiveCommand{ProjectID: thread.ProjectID, ConversationID: thread.ID, Command: command, CWD: cwd}); err == nil {
			live = handle
		}
	}
	defer live.End()
	commands := turn.Workspaces.Background
	move := (timeout == 0 || wait < timeout) && commands.HasRoom(thread.ID)
	if timeout == 0 {
		timeout = wait
		if move {
			timeout = background.Timeout
		}
	}
	active, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	adopted := false
	defer func() {
		if !adopted {
			cancel()
		}
	}()
	ended := make(chan background.Execution, 1)
	var mu sync.Mutex
	var appendToJob func(string)
	var output string
	go func() {
		completion := background.Execution{}
		defer func() {
			if cause := recover(); cause != nil {
				completion.Err = fmt.Errorf("workspace command failed: %v", cause)
			}
			ended <- completion
		}()
		completion.Result, completion.Err = run(active, sdk.ExecOptions{Timeout: timeout, OnOutput: func(text, _ string) {
			mu.Lock()
			defer mu.Unlock()
			if appendToJob != nil {
				appendToJob(text)
			} else {
				live.Output(text)
				if move {
					output += text
					if len(output) > 2<<20 {
						output = strings.Clone(output[len(output)-(1<<20):])
					}
				}
			}
		}})
	}()
	var yield <-chan time.Time
	if move {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		yield = timer.C
	}
	select {
	case result := <-ended:
		return turn.formatCommandResult(result, limit, setup)
	case <-ctx.Done():
		cancel()
		<-ended
		return "", ctx.Err()
	case <-yield:
		if stop() && ctx.Err() == nil {
			mu.Lock()
			job, appendOutput, err := commands.Adopt(thread.ID, command, active, cancel, ended, output, background.StartOptions{CWD: cwd, Local: local, SourceIdentity: identity})
			if err == nil {
				adopted, appendToJob, output = true, appendOutput, ""
			}
			mu.Unlock()
			if err == nil {
				first, err := commands.Read(ctx, thread.ID, job.ID, 0, limit)
				return formatOutput(first, setup, err)
			}
		}
		cancel()
		return turn.formatCommandResult(<-ended, limit, setup)
	}
}

func (turn *turn) formatCommandResult(result background.Execution, limit int, setup string) (string, error) {
	if result.Err != nil {
		return "", result.Err
	}
	savedName := ""
	if len(result.Result.Stdout)+len(result.Result.Stderr)+1 > limit {
		savedName = turn.saveCommandOutput(result.Result)
	}
	return formatCommandOutput(result, limit, setup, savedName)
}

func formatCommand(result background.Execution, limit int, setup string) (string, error) {
	return formatCommandOutput(result, limit, setup, "")
}

func formatCommandOutput(result background.Execution, limit int, setup, savedName string) (string, error) {
	if result.Err != nil {
		return "", result.Err
	}
	stdout, stderr := result.Result.Stdout, result.Result.Stderr
	footer := ""
	if len(stdout)+len(stderr)+1 > limit {
		totalLines := countOutputLines(stdout) + countOutputLines(stderr)
		totalBytes := len(stdout) + len(stderr)
		if savedName != "" {
			footer = fmt.Sprintf("\n[Output truncated: total %d lines, %d bytes. Full output saved: use read_file(source=%q, path=%q, offset=1) or search(source=%q, path=%q, pattern=\"...\").]",
				totalLines, totalBytes, toolOutputSourceName, savedName, toolOutputSourceName, savedName)
		} else {
			footer = fmt.Sprintf("\n[Output truncated: total %d lines, %d bytes.]", totalLines, totalBytes)
		}
		if len(footer) > limit/2 {
			footer = ""
		}
		budget := max(0, limit-1-len(footer))
		errorBudget := max(budget/2, budget-len(stdout))
		stderr = commandExcerpt(stderr, errorBudget)
		stdout = commandExcerpt(stdout, max(budget-errorBudget, budget-len(stderr)))
	}
	text := fmt.Sprintf("Exit code %d\n%s\n%s%s", result.Result.ExitCode, stdout, stderr, footer)
	if setup != "" {
		text = setup + "\n\n" + text
	}
	if result.Result.ExitCode != 0 {
		return text, errors.New("command finished with a nonzero exit code")
	}
	return text, nil
}

func countOutputLines(output string) int {
	if output == "" {
		return 0
	}
	n := strings.Count(output, "\n")
	if !strings.HasSuffix(output, "\n") {
		n++
	}
	return n
}

func commandExcerpt(output string, limit int) string {
	head := textutil.HeadUnits(output, limit)
	if len(head) == len(output) {
		return output
	}
	const fallbackNotice = "\n[… output truncated …]\n"
	if limit <= len(fallbackNotice) {
		return head
	}
	const maxDetailedNotice = 80
	if limit <= maxDetailedNotice+32 {
		budget := limit - len(fallbackNotice)
		return textutil.HeadUnits(output, budget/3) + fallbackNotice + textutil.TailUnits(output, budget-budget/3)
	}
	budget := limit - maxDetailedNotice
	headPart := textutil.HeadUnits(output, budget/3)
	tailPart := textutil.TailUnits(output, budget-budget/3)
	omittedBytes := len(output) - len(headPart) - len(tailPart)
	if omittedBytes <= 0 {
		return output
	}
	omittedLines := strings.Count(output[len(headPart):len(output)-len(tailPart)], "\n")
	notice := fmt.Sprintf("\n[… output truncated: %d lines / %d bytes omitted …]\n", omittedLines, omittedBytes)
	if len(notice) > maxDetailedNotice {
		notice = fallbackNotice
	}
	return headPart + notice + tailPart
}

func formatOutput(output *background.Output, setup string, err error) (string, error) {
	if err != nil {
		return "", err
	}
	if output == nil {
		return "", errors.New("unknown background command")
	}
	state := output.Command.ID + " is running"
	if result := output.Command.Result; result != nil {
		state = fmt.Sprintf("%s ended with exit code %d", output.Command.ID, result.ExitCode)
	}
	if output.Missed > 0 {
		state += fmt.Sprintf(" (%d characters of earlier output expired)", output.Missed)
	}
	if setup != "" {
		state = setup + "\n\n" + state
	}
	text := state + "\n" + output.Text
	if output.More {
		text += "\n[More output is available. Call read_command_output again with this handle.]"
	}
	return text, nil
}

func (turn *turn) readCommand(ctx context.Context, input args) (string, repeatEffect, error) {
	thread, err := turn.editable(ctx)
	if err != nil {
		return "", repeatTrack, err
	}
	wait := time.Duration(min(background.MaxReadWait.Seconds(), max(0, asNumber(input["waitSeconds"]))) * float64(time.Second))
	output, err := turn.Workspaces.Background.Read(ctx, thread.ID, text(input, "handle"), wait, 30000)
	content, err := formatOutput(output, "", err)
	if err != nil || output == nil {
		return content, repeatTrack, err
	}
	if output.Text != "" || output.More || output.Missed > 0 {
		return content, repeatProgress, nil
	}
	if output.Command.Result == nil && wait > 0 {
		return content, repeatWait, nil
	}
	return content, repeatTrack, nil
}

func (turn *turn) stopCommand(ctx context.Context, input args) (string, error) {
	thread, err := turn.editable(ctx)
	if err != nil {
		return "", err
	}
	job, err := turn.Workspaces.Background.Stop(ctx, thread.ID, text(input, "handle"))
	if err != nil || job == nil {
		return "", errors.Join(err, errors.New("command could not be stopped"))
	}
	return reply(job, nil)
}
