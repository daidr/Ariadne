package core

import (
	"path/filepath"
	"testing"
)

func TestToolApprovalsAppearInConversationPages(t *testing.T) {
	ctx := t.Context()
	repo, err := Open(ctx, Options{Path: filepath.Join(t.TempDir(), "approval.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	project, err := repo.CreateProject(ctx, new("Approval project"), ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	thread := func(title string) string {
		t.Helper()
		view, err := repo.CreateThread(ctx, project.ID, NewThread{Title: title})
		if err != nil {
			t.Fatal(err)
		}
		return view.ID
	}
	source, waiter, other := thread("Push"), thread("Waiting"), thread("Unrelated")
	card := func(title string) ApprovalCard {
		return func() map[string]any {
			return object{"type": "confirm", "title": title, "detail": "git push origin test"}
		}
	}
	find := func(id string, cursor MessageCursor) map[string]object {
		t.Helper()
		page, err := repo.PageMessages(ctx, id, cursor)
		if err != nil {
			t.Fatal(err)
		}
		approvals := map[string]object{}
		for _, notice := range page.Notices {
			if notice["kind"] == "approval" {
				id := notice["id"].(string)
				if approvals[id] != nil {
					t.Fatalf("duplicate approval %s", id)
				}
				approvals[id] = notice
			}
		}
		return approvals
	}

	if result, err := repo.CallApproval(ctx, project.ID, source, "command:test", card("Allow git push?"), CallApprovalOptions{}); err != nil || result.State != "asked" {
		t.Fatal(result, err)
	}
	if state, err := repo.RequestApproval(ctx, project.ID, source, "push-public:owner/repo", card("Allow public push?")); err != nil || state != "asked" {
		t.Fatal(state, err)
	}
	if result, err := repo.CallApproval(ctx, project.ID, project.CoordinatorID, "mcp:write:test", card("Allow external tool?"), CallApprovalOptions{}); err != nil || result.State != "asked" {
		t.Fatal(result, err)
	}
	if result, err := repo.CallApproval(ctx, project.ID, source, "reference:inline", card("Allow reference?"), CallApprovalOptions{InChat: true}); err != nil || result.State != "asked" {
		t.Fatal(result, err)
	}
	if _, err := repo.OpenAttention(ctx, project.ID, AttentionInput{Purpose: new(milestoneGate + "test"), Block: card("Proceed past milestone?")()}); err != nil {
		t.Fatal(err)
	}
	cards, err := repo.ListOpenAttentions(ctx, project.ID)
	if err != nil || len(cards) != 5 {
		t.Fatal(cards, err)
	}
	var commandID, pushID string
	for _, item := range cards {
		switch asObject(item.Block).str("title") {
		case "Allow git push?":
			commandID = item.ID
		case "Allow public push?":
			pushID = item.ID
		}
	}
	if found := find(source, MessageCursor{}); len(found) != 2 || found[commandID] == nil || found[pushID] == nil {
		t.Fatalf("expected both git approvals in origin, got %v", found)
	}
	if found := find(project.CoordinatorID, MessageCursor{}); len(found) != 1 {
		t.Fatalf("expected only the coordinator's tool approval, got %v", found)
	}
	if found := find(other, MessageCursor{}); len(found) != 0 {
		t.Fatalf("unexpected approvals in an unrelated thread: %v", found)
	}

	beforeJoin, err := repo.Store.LastSeq(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := repo.RequestApproval(ctx, project.ID, waiter, "push-public:owner/repo", card("Allow public push?")); err != nil || state != "pending" {
		t.Fatal(state, err)
	}
	if found := find(waiter, MessageCursor{Since: &beforeJoin}); len(found) != 1 || found[pushID] == nil {
		t.Fatalf("expected the same pending approval in the joining thread, got %v", found)
	}
	beforeAnswer, err := repo.Store.LastSeq(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.AnswerAttention(ctx, pushID, object{"type": "confirm", "approved": false}); err != nil {
		t.Fatal(err)
	}
	for _, conversationID := range []string{source, waiter} {
		found := find(conversationID, MessageCursor{Since: &beforeAnswer})
		answer := asObject(found[pushID]["answer"])
		if len(found) != 1 || answer.str("type") != "confirm" || answer.yes("approved") {
			t.Fatalf("expected the answered approval in %s, got %v", conversationID, found)
		}
	}
}
