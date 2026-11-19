package core

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/internal/eventstore"
)

func TestLargeToolOutputStoredOnceAndReadAfterMigration(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "tool-output.db")
	repo, err := Open(ctx, Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repo.Close() })
	project, err := repo.CreateProject(ctx, new("Storage"), ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := repo.CreateThread(ctx, project.ID, NewThread{Title: "Older discussion"})
	if err != nil {
		t.Fatal(err)
	}
	content := strings.Repeat("构建输出\n", 4096)
	event := eventstore.NewEvent{Type: "tool.finished", ProjectID: project.ID, Data: json.RawMessage(encoded(object{
		"conversationId": thread.ID, "toolCallId": "call", "name": "run_command", "content": content,
		"files": []any{object{"fileId": "file_a"}}, "isError": false,
	}))}
	stored, err := repo.Store.Append(ctx, []eventstore.NewEvent{event})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.FinishThread(ctx, thread.ID, "user"); err != nil {
		t.Fatal(err)
	}
	seq := stored[0].Seq
	check := func(repo *Repository) {
		t.Helper()
		var size int
		if err := repo.Store.DB().QueryRowContext(ctx, "SELECT length(payload) FROM transcript WHERE seq = ?", seq).Scan(&size); err != nil || size > 300 {
			t.Fatalf("tool output duplicated in transcript: %d, %v", size, err)
		}
		for _, options := range []TranscriptOptions{{}, {ModelContext: true, UpTo: &seq}} {
			entries, err := repo.ListTranscript(ctx, thread.ID, options)
			if err != nil {
				t.Fatal(err)
			}
			var tool object
			for _, entry := range entries {
				if entry["kind"] == "tool" {
					tool = entry
					break
				}
			}
			if tool["content"] != content || tool["toolCallId"] != "call" || encoded(tool["files"]) != `[{"fileId":"file_a"}]` {
				t.Fatal("tool output or attachment metadata missing after replay")
			}
		}
	}
	check(repo)
	if _, err := repo.Store.DB().ExecContext(ctx, "UPDATE transcript SET payload = json_set(payload, '$.content', ?) WHERE seq = ?", content, seq); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Store.DB().ExecContext(ctx, "PRAGMA user_version = 67"); err != nil {
		t.Fatal(err)
	}
	repo.Close()
	reopened, err := Open(ctx, Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	check(reopened)
}
