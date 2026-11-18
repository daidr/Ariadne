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
	workspace, err := turn.Workspaces.ForThread(ctx, thread.ProjectID, thread.ID)
	if err != nil {
		return "", err
	}
	turn.instructionsDirty = true
	if err := workspace.WriteFile(ctx, path, content); err != nil {
		return "", err
	}
	return "Wrote " + path + "." + turn.checkAfterEdit(ctx, thread, workspace, []string{path}), nil
}

func (turn *turn) commandTools() []tool {
	return []tool{
		specification("run_command", "Run a shell command in the workspace. Commands covered by an approval rule require the user's answer. Long commands may continue in the background.",
			args{"command": field("string"), "cwd": field("string"), "timeoutSeconds": field("integer"), "background": field("boolean"), "keepForUser": field("boolean")}, []string{"command"}, turn.runCommand),
		specification("read_command_output", "Read the status and new output of one of your background commands.",
			args{"handle": field("string"), "waitSeconds": field("integer")}, []string{"handle"}, turn.readCommand),
		specification("stop_command", "Stop one of your background commands.",
			args{"handle": field("string")}, []string{"handle"}, turn.stopCommand),
	}
}

// commandGate binds approval to this conversation, exact command and directory.
// The approval is recorded as used before the command can start.
func (turn *turn) commandGate(ctx context.Context, command, cwd string, rules []commandpolicy.Rule) (bool, string, error) {
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
	if len(detail) > 10000 {
		return false, "", errors.New("this command is too long for approval")
	}
	key, err := jsonutil.Encode([]string{cwd, command})
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
		card := map[string]any{"type": "confirm", "title": title, "detail": detail, "tone": "neutral", "approveText": yes, "denyText": no}
		if verdict.Pattern != nil {
			card["description"] = textutil.HeadUnits(*verdict.Pattern, 500)
		}
		return card
	}, core.CallApprovalOptions{})
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
	if err != nil || len(cwd) > 4096 {
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
	allowed, note, err := turn.commandGate(ctx, command, cwd, rules)
	if err != nil || !allowed {
		return note, err
	}
	workspace, err := turn.Workspaces.ForThread(ctx, thread.ProjectID, thread.ID)
	if err != nil {
		return "", err
	}
	turn.instructionsDirty = true
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
	wait := max(1, int(asNumber(limits["commandTimeout"])))
	if n, present := input["timeoutSeconds"]; present {
		wait = int(max(1, min(7200, asNumber(n))))
	}
	wait = min(wait, max(1, int(asNumber(limits["commandTimeoutMax"]))))
	limit := max(2000, int(asNumber(limits["commandOutput"])))
	run := func(ctx context.Context, options sdk.ExecOptions) (sdk.ExecResult, error) {
		options.Directory, options.Env, options.Shell = cwd, env, shell.Argv
		return workspace.Exec(ctx, command, options)
	}
	commands := turn.Workspaces.Background
	local := workspace.Info().Local
	if bg, _ := input["background"].(bool); bg {
		keep, _ := input["keepForUser"].(bool)
		for _, previous := range commands.List(thread.ID) {
			if previous.Result == nil && previous.Command == command && previous.CWD == cwd {
				stopped, err := commands.Stop(ctx, thread.ID, previous.ID)
				if err != nil || stopped == nil || stopped.Result == nil {
					return "", errors.New("the previous copy of this command has not stopped")
				}
				keep = keep || previous.Keep
			}
		}
		job, err := commands.Start(thread.ID, command, run, background.StartOptions{CWD: cwd, Keep: keep, Local: local})
		if err != nil {
			return "", err
		}
		first, err := commands.Read(ctx, thread.ID, job.ID, 2*time.Second)
		return formatOutput(first, limit, setup, err)
	}
	return turn.foreground(ctx, thread, command, cwd, setup, time.Duration(wait)*time.Second, limit, local, run)
}

func (turn *turn) foreground(ctx context.Context, thread core.ThreadView, command, cwd, setup string, wait time.Duration, limit int, local bool, run background.Executor) (string, error) {
	live := realtime.LiveHandle{Output: func(string) {}, End: func() {}}
	if turn.Live != nil {
		if handle, err := turn.Live.Start(realtime.LiveCommand{ProjectID: thread.ProjectID, ConversationID: thread.ID, Command: command, CWD: cwd}); err == nil {
			live = handle
		}
	}
	defer live.End()
	commands := turn.Workspaces.Background
	move := commands.HasRoom(thread.ID)
	timeout := wait
	if move {
		timeout = background.Timeout
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
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case result := <-ended:
		return formatCommand(result, limit, setup)
	case <-ctx.Done():
		cancel()
		<-ended
		return "", ctx.Err()
	case <-timer.C:
		if move && stop() && ctx.Err() == nil {
			mu.Lock()
			job, appendOutput, err := commands.Adopt(thread.ID, command, active, cancel, ended, output, background.StartOptions{CWD: cwd, Local: local})
			if err == nil {
				adopted, appendToJob, output = true, appendOutput, ""
			}
			mu.Unlock()
			if err == nil {
				first, err := commands.Read(ctx, thread.ID, job.ID, 0)
				return formatOutput(first, limit, setup, err)
			}
		}
		cancel()
		return formatCommand(<-ended, limit, setup)
	}
}

func formatCommand(result background.Execution, limit int, setup string) (string, error) {
	if result.Err != nil {
		return "", result.Err
	}
	output := textutil.HeadUnits(result.Result.Stdout+"\n"+result.Result.Stderr, limit)
	text := fmt.Sprintf("Exit code %d\n%s", result.Result.ExitCode, output)
	if setup != "" {
		text = setup + "\n\n" + text
	}
	if result.Result.ExitCode != 0 {
		return text, errors.New("command finished with a nonzero exit code")
	}
	return text, nil
}

func formatOutput(output *background.Output, limit int, setup string, err error) (string, error) {
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
	return state + "\n" + textutil.HeadUnits(output.Text, limit), nil
}

func (turn *turn) readCommand(ctx context.Context, input args) (string, error) {
	thread, err := turn.editable(ctx)
	if err != nil {
		return "", err
	}
	wait := time.Duration(max(0, min(60, asNumber(input["waitSeconds"]))) * float64(time.Second))
	output, err := turn.Workspaces.Background.Read(ctx, thread.ID, text(input, "handle"), wait)
	return formatOutput(output, 30000, "", err)
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
