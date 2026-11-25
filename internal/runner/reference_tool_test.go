package runner

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/jsonutil"
	"github.com/daidr/Ariadne/internal/local"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/internal/sources"
	"github.com/daidr/Ariadne/internal/workspaces"
	"github.com/daidr/Ariadne/sdk"
)

func TestOutsideWorkspaceReadsSuggestRequestingOnlyNeededAccess(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	home, folder, outside := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"locale":"zh-CN"}`), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := settings.Open(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	repo, err := core.Open(ctx, core.Options{Path: ":memory:"})
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
	project, err := repo.CreateProject(ctx, new("Fixture"), core.ProjectStart{Source: args{"kind": "local-directory", "settings": args{"path": folder}}})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "Read a reference"})
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(outside, "report.txt")
	if err := os.WriteFile(file, []byte("example report"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PostMessage(ctx, project.ID, core.MessageInput{ConversationID: thread.ID, Author: "user", Content: "Summarize the report."}); err != nil {
		t.Fatal(err)
	}
	turnID, err := repo.StartScheduledTurn(ctx, thread.ID, "mock")
	if err != nil || turnID == "" {
		t.Fatal(turnID, err)
	}
	agent := &turn{Runner: &Runner{Repository: repo, Settings: store, Workspaces: work, Sources: registry, Home: home}, id: turnID,
		conversation: core.ConversationView{ID: thread.ID, ProjectID: project.ID, Role: "thread"}}
	call := func(id, name string, input args) sdk.ToolCall {
		t.Helper()
		encoded, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		return sdk.ToolCall{ID: id, Name: name, Arguments: string(encoded)}
	}
	results := []sdk.ToolCall{
		call("read", "read_file", args{"path": file}),
		call("list", "list_dir", args{"path": outside}),
		call("search", "search", args{"path": outside, "pattern": "report"}),
		call("find", "find_files", args{"path": outside, "pattern": "*.txt"}),
		call("missing", "read_file", args{"path": "missing.txt"}),
	}
	if _, err := agent.execute(ctx, agent.tools(ctx, project), results); err != nil {
		t.Fatal(err)
	}
	entries, err := repo.ListTranscript(ctx, thread.ID, core.TranscriptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, entry := range entries {
		if entry["kind"] != "tool" {
			continue
		}
		seen++
		content := text(entry, "content")
		wantHint := entry["toolCallId"] != "missing"
		if entry["isError"] != true || strings.Contains(content, "add_source") != wantHint ||
			wantHint && !strings.Contains(content, "is outside the workspace") {
			t.Errorf("unexpected result for %v: %q", entry["toolCallId"], content)
		}
	}
	if seen != len(results) {
		t.Fatalf("found %d tool results, want %d", seen, len(results))
	}
	if cards, err := repo.ListOpenAttentions(ctx, project.ID); err != nil || len(cards) != 0 {
		t.Fatalf("a file error opened an approval card: %+v, %v", cards, err)
	}

	request := args{"kind": "local-directory", "settings": args{"path": outside}, "reason": "Read the requested report.", "keep": "project"}
	if _, err := agent.addReference(ctx, request); err != nil {
		t.Fatal(err)
	}
	cards, err := repo.ListOpenAttentions(ctx, project.ID)
	if err != nil || len(cards) != 1 {
		t.Fatal(cards, err)
	}
	card, _ := cards[0].Block.(map[string]any)
	if card["type"] != "confirm" || card["allowProjectReference"] != true {
		t.Fatalf("expected a reference confirmation with scope selection: %+v", card)
	}
	if err := repo.AnswerAttention(ctx, cards[0].ID, args{"type": "confirm", "approved": true}); err != nil {
		t.Fatal(err)
	}
	result, err := agent.addReference(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	var approved struct{ Source string }
	if err := json.Unmarshal([]byte(result), &approved); err != nil || approved.Source == "" {
		t.Fatal(result, err)
	}
	content, _, err := agent.readFile(ctx, args{"path": "report.txt", "source": approved.Source})
	if err != nil || !strings.Contains(content, "example report") {
		t.Fatal(content, err)
	}
	current, err := repo.GetProject(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if refs, _ := current.References.([]any); len(refs) != 0 {
		t.Fatalf("thread approval was shared across the project: %+v", refs)
	}

	hasReference := func(raw any, path string) bool {
		refs, _ := raw.([]any)
		for _, rawRef := range refs {
			ref, _ := rawRef.(map[string]any)
			settings, _ := ref["settings"].(map[string]any)
			if settings["path"] == path {
				return true
			}
		}
		return false
	}
	t.Run("can select the project before approving", func(t *testing.T) {
		path := filepath.Join(outside, "shared-report.txt")
		if err := os.WriteFile(path, []byte("shared report"), 0600); err != nil {
			t.Fatal(err)
		}
		request := args{"kind": "local-directory", "settings": args{"path": path}, "reason": "Share the report."}
		if _, err := agent.addReference(ctx, request); err != nil {
			t.Fatal(err)
		}
		cards, err := repo.ListOpenAttentions(ctx, project.ID)
		if err != nil || len(cards) != 1 {
			t.Fatal(cards, err)
		}
		if err := repo.AnswerAttention(ctx, cards[0].ID, args{"type": "confirm", "approved": true, "scope": "invalid"}); err == nil {
			t.Fatal("an invalid scope was accepted")
		}
		if err := repo.AnswerAttention(ctx, cards[0].ID, args{"type": "confirm", "approved": true, "scope": "project"}); err != nil {
			t.Fatal(err)
		}
		if _, err := agent.addReference(ctx, request); err != nil {
			t.Fatal(err)
		}
		shared, err := repo.GetProject(ctx, project.ID)
		if err != nil {
			t.Fatal(err)
		}
		currentThread, err := repo.GetThread(ctx, thread.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !hasReference(shared.References, path) || hasReference(currentThread.References, path) {
			t.Fatal("the approval did not grant project-wide read access")
		}
	})

	t.Run("legacy answers retain their scope", func(t *testing.T) {
		for index, scope := range []string{"project", "thread", "denied"} {
			t.Run(scope, func(t *testing.T) {
				path := filepath.Join(outside, scope+".txt")
				if err := os.WriteFile(path, []byte(scope), 0600); err != nil {
					t.Fatal(err)
				}
				const kind = "local-directory"
				fingerprint, err := jsonutil.Encode([]any{kind, [][2]any{{"path", path}}})
				if err != nil {
					t.Fatal(err)
				}
				purpose := fmt.Sprintf("reference:%x", sha256.Sum256(fingerprint))
				_, err = repo.CallApproval(ctx, project.ID, thread.ID, purpose, func() args {
					return args{"type": "choice", "question": "Allow this reference?", "options": []any{
						args{"label": "Project", "consequence": "Share with the project"},
						args{"label": "Thread", "consequence": "Only for this thread"},
						args{"label": "Decline", "consequence": "No access"},
					}}
				}, core.CallApprovalOptions{InChat: true, No: new(2)})
				if err != nil {
					t.Fatal(err)
				}
				cards, err := repo.ListOpenAttentions(ctx, project.ID)
				if err != nil || len(cards) != 1 {
					t.Fatal(cards, err)
				}
				if err := repo.AnswerAttention(ctx, cards[0].ID, args{"type": "choice", "choice": index}); err != nil {
					t.Fatal(err)
				}
				_, err = agent.addReference(ctx, args{"kind": kind, "settings": args{"path": path}, "reason": "Read this file."})
				if scope == "denied" {
					if err == nil || !strings.Contains(err.Error(), "declined") {
						t.Fatalf("expected a declined approval, got %v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				currentProject, err := repo.GetProject(ctx, project.ID)
				if err != nil {
					t.Fatal(err)
				}
				currentThread, err := repo.GetThread(ctx, thread.ID)
				if err != nil {
					t.Fatal(err)
				}
				if hasReference(currentProject.References, path) != (scope == "project") || hasReference(currentThread.References, path) != (scope == "thread") {
					t.Fatalf("legacy approval did not honor the chosen scope %q", scope)
				}
			})
		}
	})

	t.Run("writable source follows the confirmed scope", func(t *testing.T) {
		for _, scope := range []string{"thread", "project"} {
			t.Run(scope, func(t *testing.T) {
				path := filepath.Join(outside, "writable-"+scope)
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
				request := args{"kind": "local-directory", "settings": args{"path": path}, "reason": "Update the requested files."}
				if _, err := agent.addFileSource(ctx, request); err != nil {
					t.Fatal(err)
				}
				cards, err := repo.ListOpenAttentions(ctx, project.ID)
				if err != nil || len(cards) != 1 {
					t.Fatal(cards, err)
				}
				card, _ := cards[0].Block.(map[string]any)
				if !strings.Contains(text(card, "title"), "读写") || card["allowProjectReference"] != true {
					t.Fatal(card)
				}
				if err := repo.AnswerAttention(ctx, cards[0].ID, args{"type": "confirm", "approved": true, "scope": scope}); err != nil {
					t.Fatal(err)
				}
				result, err := agent.addFileSource(ctx, request)
				if err != nil {
					t.Fatal(err)
				}
				var approved struct{ Source string }
				if err := json.Unmarshal([]byte(result), &approved); err != nil || approved.Source == "" {
					t.Fatal(result, err)
				}
				if _, err := agent.writeFile(ctx, args{"source": approved.Source, "path": "grant.txt", "content": "approved"}); err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(filepath.Join(path, "grant.txt"))
				if err != nil || string(data) != "approved" {
					t.Fatal(string(data), err)
				}
				id, err := work.ResolveFileSource(ctx, project.ID, approved.Source, "")
				if err != nil || (id != "") != (scope == "project") {
					t.Fatalf("grant visible beyond its scope: %s, %v", id, err)
				}
			})
		}
	})
}
