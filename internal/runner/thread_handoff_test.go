package runner

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/internal/core"
)

func TestCoordinatorUsesReturnedThreadIDsForMessagesAndCards(t *testing.T) {
	ctx := t.Context()
	repo, err := core.Open(ctx, core.Options{Path: filepath.Join(t.TempDir(), "handoff.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	home, err := repo.CreateProject(ctx, new("Handoff"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	agent := &turn{Runner: &Runner{Repository: repo}, conversation: core.ConversationView{
		ID: home.CoordinatorID, ProjectID: home.ID, Role: "coordinator",
	}}
	create := func(title string) string {
		t.Helper()
		result, err := agent.createThread(ctx, args{"title": title, "brief": "Initial brief"})
		var response args
		if err != nil || json.Unmarshal([]byte(result), &response) != nil {
			t.Fatalf("create_thread returned %s, %v", result, err)
		}
		id := text(response, "threadId")
		if id == "" || id == "undefined" {
			t.Fatalf("create_thread did not return a usable threadId: %s", result)
		}
		return id
	}
	first, second := create("Check first path"), create("Check second path")
	if first == second {
		t.Fatal("duplicate thread IDs")
	}
	if _, err := agent.messageThread(ctx, args{"threadId": "undefined", "content": "wrong target"}); err == nil || !strings.Contains(err.Error(), "list_threads") {
		t.Fatalf("invalid relay cannot recover the ID: %v", err)
	}
	if _, err := agent.messageThread(ctx, args{"threadId": second, "content": "Follow-up"}); err != nil {
		t.Fatal(err)
	}
	for id, count := range map[string]int{first: 1, second: 2} {
		messages, err := repo.ListMessages(ctx, id)
		if err != nil || len(messages) != count {
			t.Fatalf("relay targeted the wrong thread: %s has %d messages, %v", id, len(messages), err)
		}
	}
	if _, err := agent.postUI(ctx, args{"text": "Invalid", "blocks": []any{args{"type": "thread"}}}); err == nil || !strings.Contains(err.Error(), "blocks[0].threadId") {
		t.Fatalf("missing card ID lacked a useful error: %v", err)
	}
	if _, err := agent.postUI(ctx, args{"text": "Invalid", "threadIds": []any{"undefined"}}); err == nil || !strings.Contains(err.Error(), "list_threads") {
		t.Fatalf("invalid card ID lacked a useful error: %v", err)
	}
	other, err := repo.CreateProject(ctx, new("Different project"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := repo.CreateThread(ctx, other.ID, core.NewThread{Title: "Private"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agent.postUI(ctx, args{"threadIds": []any{foreign.ID}}); err == nil {
		t.Fatal("post_ui accepted a cross-project ID")
	}
	if _, err := agent.postUI(ctx, args{"text": "Assigned", "threadIds": []any{second, first}}); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.postUI(ctx, args{"blocks": []any{args{"type": "thread", "threadId": first}}}); err != nil {
		t.Fatal(err)
	}
	messages, err := repo.ListMessages(ctx, home.CoordinatorID)
	if err != nil || len(messages) != 2 {
		t.Fatalf("invalid cards may have created a message: %d, %v", len(messages), err)
	}
	blocks, _ := messages[0].Blocks.([]any)
	if len(blocks) != 2 || text(objectField(args{"block": blocks[0]}, "block"), "threadId") != second ||
		text(objectField(args{"block": blocks[1]}, "block"), "threadId") != first {
		t.Fatalf("wrong card targets or order: %v", blocks)
	}
}
