package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/local"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/internal/sources"
	"github.com/daidr/Ariadne/internal/workspaces"
)

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
}
