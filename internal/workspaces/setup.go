package workspaces

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/daidr/Ariadne/internal/background"
	"github.com/daidr/Ariadne/internal/commandpolicy"
	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/sdk"
)

const checkHint = "[Ariadne: this project has no check after edits yet. Once you know how the repository checks its code, note one with note_check.]"
const setupHint = "[Ariadne: nothing was installed in your new workspace: this project has no setup for new workspaces yet. When the work needs its dependencies, install them with note_setup, the way the repository says to: later threads' new workspaces then run the same first.]"

type setupNote struct {
	project, job, text string
	check, unset       bool
}

func (manager *Manager) changeNote(thread string, change func(*setupNote)) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closed {
		return
	}
	note, _ := manager.notes.Get(thread)
	change(&note)
	if note.job == "" && note.text == "" && !note.check && !note.unset {
		note = setupNote{}
	}
	if note == (setupNote{}) {
		manager.notes.Remove(thread)
		return
	}
	manager.notes.Put(thread, note, int64(len(thread)+len(note.project)+len(note.job)+len(note.text)+64))
}
func detectedCommand(project core.ProjectView, kind string) string {
	detected, _ := project.Detected.(map[string]any)
	value, _ := detected[kind].(map[string]any)
	command, _ := value["command"].(string)
	return command
}
func setupEndedNote(id string, result *background.Result) string {
	if result == nil || result.ExitCode == 0 && !result.Stopped {
		return ""
	}
	ending := fmt.Sprintf("failed (exit code %d)", result.ExitCode)
	if result.Stopped {
		ending = "was stopped before it finished"
	}
	return fmt.Sprintf("[Ariadne: %s, the setup of your new workspace, %s; what the work needs may be missing. It does not run again by itself: read_command_output %s shows what it printed, and run_command what the work needs.]", id, ending, id)
}
func (manager *Manager) prepareSetup(ctx context.Context, project core.ProjectView, thread core.ThreadView, value *workspaceConnection, key string, isolated bool) {
	own := !project.Settings.Workspace.Auto.Setup
	command := project.Settings.Workspace.Setup
	if !own {
		command = detectedCommand(project, "setup")
		if !isolated {
			command = ""
		}
	}
	note := setupNote{project: project.ID, check: true, unset: !own && isolated && detectedCommand(project, "setup") == ""}
	if command != "" {
		var rules []commandpolicy.Rule
		if manager.options.CommandRules != nil {
			rules = manager.options.CommandRules(project.ID)
		}
		if !own && commandpolicy.Decide(command, rules).Action != "allow" {
			line, _, _ := strings.Cut(command, "\n")
			note.text = fmt.Sprintf("[Ariadne: your workspace is new, and its setup did not run: the user's command rules do not let `%s` run unasked. When the work needs it, run it with run_command, which asks the user where the rules say so.]", line)
		} else {
			shell, err := manager.Shell(ctx, project.ID)
			if err != nil {
				note.text = "[Ariadne: workspace setup could not start: " + err.Error() + "]"
			} else {
				env := manager.Env(project.ID)
				timeout := 10 * time.Minute
				if manager.options.SetupTimeout != nil {
					if chosen := manager.options.SetupTimeout(project.ID); chosen > 0 {
						timeout = chosen
					}
				}
				view := &workspaceView{manager: manager, project: project.ID, thread: thread.ID, key: key, info: copyInfo(value.workspace.Info())}
				job, err := manager.Background.Start(thread.ID, command, func(ctx context.Context, options sdk.ExecOptions) (sdk.ExecResult, error) {
					options.Shell = shell.Argv
					options.Env = env
					options.Timeout = timeout
					return view.Exec(ctx, command, options)
				}, background.StartOptions{CWD: "."})
				if err != nil {
					note.text = "[Ariadne: workspace setup could not start: " + err.Error() + "]"
				} else {
					note.job = job.ID
					what := "the setup noted for this project"
					if own {
						what = "the project's setup script, as the user set it,"
					}
					note.text = fmt.Sprintf("[Ariadne: your workspace is new, so %s runs in it now, in the background as %s. run_command waits for it to end before running anything; read files and edit meanwhile, or read_command_output %s to look at it.]", what, job.ID, job.ID)
				}
			}
		}
	}
	manager.changeNote(thread.ID, func(value *setupNote) { *value = note })
}
func (manager *Manager) setupJob(thread, id string) *background.Command {
	for _, job := range manager.Background.List(thread) {
		if job.ID == id {
			return &job
		}
	}
	return nil
}
func (manager *Manager) SetupRunning(thread string) *string {
	note, _ := manager.notes.Get(thread)
	if note.job == "" {
		return nil
	}
	job := manager.setupJob(thread, note.job)
	if job != nil && job.Result == nil {
		return &note.job
	}
	return nil
}
func (manager *Manager) SetupDone(ctx context.Context, thread string) (string, error) {
	ctx, done, err := manager.begin(ctx)
	if err != nil {
		return "", err
	}
	defer done()
	note, _ := manager.notes.Get(thread)
	if note.job == "" {
		return "", nil
	}
	job, err := manager.Background.Wait(ctx, thread, note.job)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if job != nil && job.Result == nil {
		return "", nil
	}
	message := ""
	manager.changeNote(thread, func(current *setupNote) {
		if current.job == note.job {
			current.job = ""
			if job != nil {
				message = setupEndedNote(note.job, job.Result)
			}
		}
	})
	return message, nil
}
func keptNote(commands []background.Command) string {
	lines := []string{}
	for _, command := range commands {
		id := command.ID
		if command.URL != nil {
			id += " at " + *command.URL
		}
		lines = append(lines, id+": "+textutil.HeadUnits(command.Command, 120))
	}
	return "Still running in the background from before your last report, kept because the user wanted to try them:\n" + strings.Join(lines, "\n") + "\nUse them as they are (read_command_output), or stop_command one that needs a restart; do not start a second copy."
}
func (manager *Manager) TakeSetup(ctx context.Context, thread string) (string, error) {
	ctx, done, err := manager.begin(ctx)
	if err != nil {
		return "", err
	}
	defer done()
	var note setupNote
	parts := []string{}
	manager.changeNote(thread, func(current *setupNote) {
		note = *current
		if current.text != "" {
			parts = append(parts, current.text)
			current.text = ""
		}
		if current.job != "" {
			job := manager.setupJob(thread, current.job)
			if job == nil || job.Result != nil {
				if job != nil {
					if message := setupEndedNote(current.job, job.Result); message != "" {
						parts = append(parts, message)
					}
				}
				current.job = ""
			}
		}
		current.check, current.unset = false, false
	})
	if note.project != "" {
		project, err := manager.options.Repository.GetProject(ctx, note.project)
		if err == nil {
			if note.check {
				allowed, _ := manager.options.Repository.CanNoteCheck(ctx, thread)
				if allowed && detectedCommand(project, "check") == "" {
					parts = append(parts, checkHint)
				}
			}
			if note.unset {
				allowed, _ := manager.options.Repository.CanNoteSetup(ctx, thread)
				if allowed && detectedCommand(project, "setup") == "" {
					parts = append(parts, setupHint)
				}
			}
		}
	}
	if kept := manager.Background.TakeKept(thread); len(kept) > 0 {
		parts = append(parts, keptNote(kept))
	}
	return strings.Join(parts, "\n\n"), nil
}
func (manager *Manager) noteSetupGone(thread string) {
	manager.changeNote(thread, func(note *setupNote) {
		if note.job == "" {
			return
		}
		if job := manager.setupJob(thread, note.job); job != nil && job.Result == nil {
			note.text = setupEndedNote(note.job, &background.Result{ExitCode: -1, Stopped: true})
		}
		note.job = ""
	})
}
func (manager *Manager) StopThread(ctx context.Context, thread string) error {
	ctx, done, err := manager.begin(ctx)
	if err != nil {
		return err
	}
	defer done()
	manager.noteSetupGone(thread)
	return manager.Background.StopThread(ctx, thread, false)
}
