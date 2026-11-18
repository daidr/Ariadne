package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/daidr/Ariadne/internal/commandpolicy"
	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/projectsettings"
	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/sdk"
)

func (turn *turn) workspaceNoteTools(ctx context.Context, id string) []tool {
	var tools []tool
	if allowed, _ := turn.Repository.CanNoteCheck(ctx, id); allowed {
		tools = append(tools, specification("note_check", "Try and record a quick lint or type check for this project's future edits. Use {file} for changed files. Give an empty command when there is no suitable check.",
			args{"command": field("string"), "files": arrayOf(field("string"))}, []string{"command"},
			func(ctx context.Context, input args) (string, error) {
				return turn.noteWorkspaceCommand(ctx, input, "check")
			}))
	}
	if allowed, _ := turn.Repository.CanNoteSetup(ctx, id); allowed {
		tools = append(tools, specification("note_setup", "Run the repository's documented workspace setup and record it for future workspaces after it succeeds. Give an empty command when no setup is needed.",
			args{"command": field("string")}, []string{"command"},
			func(ctx context.Context, input args) (string, error) {
				return turn.noteWorkspaceCommand(ctx, input, "setup")
			}))
	}
	return tools
}

func (turn *turn) noteWorkspaceCommand(ctx context.Context, input args, kind string) (string, error) {
	thread, err := turn.editable(ctx)
	if err != nil {
		return "", err
	}
	note := turn.Repository.NoteCheck
	canNote := turn.Repository.CanNoteCheck
	limit := projectsettings.MaxCheck
	if kind == "setup" {
		note, canNote, limit = turn.Repository.NoteSetup, turn.Repository.CanNoteSetup, projectsettings.MaxSetup
	}
	value, ok := input["command"].(string)
	command := textutil.Trim(value)
	if !ok || textutil.HeadUnits(command, limit) != command || strings.ContainsRune(command, 0) {
		return "", fmt.Errorf("command must be text of at most %d characters", limit)
	}
	allowed, err := canNote(ctx, thread.ID)
	if err != nil || !allowed {
		return "", errors.New("this conversation cannot set the project's " + kind)
	}
	if command == "" {
		_, err := note(ctx, thread.ID, "")
		return "Recorded that this project does not need a " + kind + " command.", err
	}
	files, err := stringsOf(input["files"])
	if err != nil {
		return "", err
	}
	workspace, err := turn.Workspaces.ForThread(ctx, thread.ProjectID, thread.ID)
	if err != nil {
		return "", err
	}
	turn.instructionsDirty = true
	if _, err := turn.Workspaces.SetupDone(ctx, thread.ID); err != nil {
		return "", err
	}
	result, err := turn.tryWorkspaceCommand(ctx, thread, workspace, command, files, kind)
	if err != nil {
		return "", err
	}
	report := workspaceCommandOutput(result)
	if result.ExitCode != 0 && kind == "setup" {
		return report, errors.New("setup failed")
	}
	if _, err := note(ctx, thread.ID, command); err != nil {
		return "", err
	}
	return "Saved the project's " + kind + " command.\n" + report, nil
}

func (turn *turn) checkAfterEdit(ctx context.Context, thread core.ThreadView, workspace sdk.Workspace, paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	project, err := turn.Repository.GetProject(ctx, thread.ProjectID)
	if err != nil || slices.Contains(project.Settings.Tools, "commands") {
		return ""
	}
	command := project.Settings.Workspace.Check
	if project.Settings.Workspace.Auto.Check {
		detected, _ := project.Detected.(args)
		check, _ := detected["check"].(args)
		command = text(check, "command")
	}
	if command == "" {
		return ""
	}
	if job := turn.Workspaces.SetupRunning(thread.ID); job != nil {
		return "\nWorkspace setup is still running (" + *job + ")."
	}
	result, err := turn.tryWorkspaceCommand(ctx, thread, workspace, command, paths, "check")
	if err != nil {
		return "\nWorkspace check: " + err.Error()
	}
	return "\nWorkspace check: " + workspaceCommandOutput(result)
}

func (turn *turn) tryWorkspaceCommand(ctx context.Context, thread core.ThreadView, workspace sdk.Workspace, command string, paths []string, kind string) (sdk.ExecResult, error) {
	var result sdk.ExecResult
	project, err := turn.Repository.GetProject(ctx, thread.ProjectID)
	if err != nil {
		return result, err
	}
	if slices.Contains(project.Settings.Tools, "commands") {
		return result, errors.New("commands are disabled for this project")
	}
	config := turn.Settings.TurnSettings(thread.ProjectID)
	raw, err := json.Marshal(config["commandRules"])
	if err != nil {
		return result, err
	}
	var rules []commandpolicy.Rule
	if err := json.Unmarshal(raw, &rules); err != nil {
		return result, err
	}
	shell, err := turn.Workspaces.Shell(ctx, thread.ProjectID)
	if err != nil {
		return result, err
	}
	if kind == "check" && strings.Contains(command, "{file}") {
		if len(paths) == 0 {
			return result, errors.New("files is required when the check contains {file}")
		}
		quoted := make([]string, 0, len(paths))
		for _, raw := range paths {
			file, err := editPath(raw)
			if err != nil {
				return result, err
			}
			word, err := checkFileArgument(shell.Name, file)
			if err != nil {
				return result, err
			}
			quoted = append(quoted, word)
		}
		command = strings.ReplaceAll(command, "{file}", strings.Join(quoted, " "))
	}
	if len(command) > 32<<10 || commandpolicy.Decide(command, rules).Action != "allow" {
		return result, errors.New("the command rules require approval. Run it explicitly with run_command")
	}
	limits, _ := config["limits"].(args)
	seconds := asNumber(limits["commandTimeout"])
	if kind == "setup" {
		seconds = asNumber(limits["setupTimeout"])
		if seconds <= 0 {
			seconds = 600
		}
	}
	return workspace.Exec(ctx, command, sdk.ExecOptions{Shell: shell.Argv, Env: turn.Workspaces.Env(thread.ProjectID),
		Timeout: time.Duration(max(1, min(seconds, 7200))) * time.Second})
}

func checkFileArgument(shell, file string) (string, error) {
	switch shell {
	case "pwsh", "powershell", "pwsh.exe", "powershell.exe":
		return "'" + strings.ReplaceAll(file, "'", "''") + "'", nil
	case "cmd", "cmd.exe":
		if strings.ContainsAny(file, "\"%!^") {
			return "", errors.New("this path cannot be passed to the configured cmd check")
		}
		return "\"" + file + "\"", nil
	default:
		return "'" + strings.ReplaceAll(file, "'", "'\"'\"'") + "'", nil
	}
}

func workspaceCommandOutput(result sdk.ExecResult) string {
	if result.ExitCode == 0 {
		return "Passed."
	}
	output := strings.TrimSpace(textutil.ScreenText(result.Stdout + "\n" + result.Stderr))
	return fmt.Sprintf("Failed (exit %d).\n%s", result.ExitCode, textutil.TailUnits(output, 6000))
}
