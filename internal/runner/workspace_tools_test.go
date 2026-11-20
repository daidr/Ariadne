package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/background"
	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/local"
	"github.com/daidr/Ariadne/internal/process"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/internal/sources"
	"github.com/daidr/Ariadne/internal/workspaces"
	"github.com/daidr/Ariadne/sdk"
)

func TestCommandExcerptPreservesDiagnosticAfterLargeOutput(t *testing.T) {
	result, err := formatCommand(background.Execution{Result: sdk.ExecResult{
		ExitCode: 1,
		Stdout:   "build starting\n" + strings.Repeat("verbose output\n", 5000) + "build finished",
		Stderr:   strings.Repeat("warning\n", 5000) + "fatal: build output missing",
	}}, 2000, "")
	if err == nil || len(result) > 2020 {
		t.Fatalf("expected a bounded failure, got %d bytes, %v", len(result), err)
	}
	for _, marker := range []string{"Exit code 1", "build starting", "output truncated", "build finished", "fatal: build output missing"} {
		if !strings.Contains(result, marker) {
			t.Errorf("result omitted %q", marker)
		}
	}
}

func TestStalledCommandChild(t *testing.T) {
	if os.Args[len(os.Args)-1] == "--stalled-workspace-command" {
		time.Sleep(time.Minute)
	}
}

func TestCommandTimeoutPersistsAfterYield(t *testing.T) {
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	commands := background.New(background.Options{})
	t.Cleanup(func() { _ = commands.Close(context.Background()) })
	active := &turn{Runner: &Runner{Workspaces: &workspaces.Manager{Background: commands}}}
	cwd := t.TempDir()
	thread := core.ThreadView{ID: "thread"}
	run := func(ctx context.Context, options sdk.ExecOptions) (sdk.ExecResult, error) {
		return process.Run(ctx, []string{program, "-test.run=^TestStalledCommandChild$", "--", "--stalled-workspace-command"}, process.RunOptions{
			Directory: cwd, Timeout: options.Timeout, OnOutput: options.OnOutput,
		}), nil
	}
	result, err := active.foreground(ctx, thread, "stalled command", ".", "", 20*time.Millisecond,
		500*time.Millisecond, 2000, true, run, "source")
	if err != nil || !strings.Contains(result, "cmd-1") {
		t.Fatalf("expected a background handle, got %q, %v", result, err)
	}
	job, err := commands.Wait(ctx, thread.ID, "cmd-1")
	if err != nil || job == nil || job.Result == nil || job.Result.ExitCode != 124 {
		t.Fatalf("background command did not honour its execution deadline: %+v, %v", job, err)
	}
}

func TestCommandApprovalBindsToOneExecutionInItsWorkspace(t *testing.T) {
	home, folder := t.TempDir(), t.TempDir()
	const command = "echo approved > result.txt"
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"commands":{"rules":[{"pattern":"echo*","action":"ask"}]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := settings.Open(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(home, "ariadne.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	registry := sources.New()
	if _, err := registry.Register(local.DirectoryProvider(filepath.Join(home, "local"), nil, true)); err != nil {
		t.Fatal(err)
	}
	work, err := workspaces.New(workspaces.Options{Repository: repo, Sources: registry})
	if err != nil {
		t.Fatal(err)
	}
	defer work.Close(context.Background())
	project, err := repo.CreateProject(t.Context(), new("Fixture"), core.ProjectStart{Source: args{"kind": "local-directory", "settings": args{"path": folder}}})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := repo.CreateThread(t.Context(), project.ID, core.NewThread{Title: "Test commands"})
	if err != nil {
		t.Fatal(err)
	}
	turn := &turn{Runner: &Runner{Repository: repo, Settings: store, Workspaces: work}, conversation: core.ConversationView{ID: thread.ID, ProjectID: project.ID, Role: "thread"}}
	call := func(ctx context.Context, command, folder string) string {
		t.Helper()
		result, err := turn.runCommand(ctx, args{"command": command, "cwd": folder, "timeoutSeconds": float64(15)})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	file := filepath.Join(folder, "result.txt")
	waiting := func(command, cwd string) {
		t.Helper()
		if result := call(t.Context(), command, cwd); !strings.Contains(result, "Waiting") {
			t.Fatalf("expected approval, got %q", result)
		}
		if _, err := os.Stat(file); !os.IsNotExist(err) {
			t.Fatalf("command touched the filesystem before approval: %v", err)
		}
	}
	waiting(command, ".")
	cards, err := repo.ListOpenAttentions(t.Context(), project.ID)
	if err != nil || len(cards) != 1 {
		t.Fatal(cards, err)
	}
	if err := repo.AnswerAttention(t.Context(), cards[0].ID, args{"type": "confirm", "approved": true}); err != nil {
		t.Fatal(err)
	}
	waiting(command, "sub")
	waiting("echo changed > result.txt", ".")
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	if result := call(ctx, command, "."); !strings.Contains(result, "Exit code 0") {
		t.Fatalf("approved command result: %q", result)
	}
	if data, err := os.ReadFile(file); err != nil || strings.TrimSpace(string(data)) != "approved" {
		t.Fatalf("approved command did not run once: %q %v", data, err)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	waiting(command, ".")

	// Compare the stored approval with a command at the input size limit.
	longCommand := "echo long\n# "
	longCommand += strings.Repeat("x", (32<<10)-len(longCommand)-len("\n# end  ")) + "\n# end  "
	waiting(longCommand, "sub")
	cards, err = repo.ListOpenAttentions(t.Context(), project.ID)
	if err != nil {
		t.Fatal(err)
	}
	var longApprovalID string
	for _, card := range cards {
		block, _ := card.Block.(map[string]any)
		if block["detail"] == "cd sub\n"+longCommand {
			longApprovalID = card.ID
			break
		}
	}
	if longApprovalID == "" {
		t.Fatal("confirmation must show the full command and its working directory")
	}
	if err := repo.AnswerAttention(t.Context(), longApprovalID, args{"type": "confirm", "approved": false}); err != nil {
		t.Fatal(err)
	}
	if _, err := turn.runCommand(t.Context(), args{"command": longCommand, "cwd": "sub"}); err == nil || !strings.Contains(err.Error(), "declined") {
		t.Fatalf("expected the long command to remain denied, got %v", err)
	}
}
