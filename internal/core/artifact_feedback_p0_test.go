package core

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func feedbackRoundOf(t *testing.T, fb object) int64 {
	t.Helper()
	if fb == nil {
		return 0
	}
	switch v := fb["round"].(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	default:
		t.Fatalf("unexpected round type %T in %+v", fb["round"], fb)
		return 0
	}
}

func feedbackItemsOf(t *testing.T, fb object) []object {
	t.Helper()
	if fb == nil {
		return nil
	}
	if items, ok := fb["items"].([]object); ok {
		return items
	}
	if raw, ok := fb["items"].([]any); ok {
		out := make([]object, 0, len(raw))
		for _, item := range raw {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			} else if m, ok := item.(object); ok {
				out = append(out, m)
			}
		}
		return out
	}
	t.Fatalf("unexpected items type %T in %+v", fb["items"], fb)
	return nil
}

func TestArtifactFeedbackCommentFingerprints(t *testing.T) {
	ctx := t.Context()
	clock := time.UnixMilli(1900000000000)
	repo, err := Open(ctx, Options{Path: filepath.Join(t.TempDir(), "test.sqlite"), Now: func() time.Time { return clock }})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	project, err := repo.CreateProject(ctx, new("Fixture"), ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := repo.CreateArtifact(ctx, project.ID, NewArtifact{
		Kind:    "document",
		Title:   "Spec",
		Author:  project.CoordinatorID,
		Content: "Line one\nLine two",
	})
	if err != nil {
		t.Fatal(err)
	}

	// 1. Create a comment -> triggers feedback with action "create".
	clock = clock.Add(time.Second)
	if _, err := repo.CommentOnArtifact(ctx, artifact.ID, "user", map[string]any{
		"type":      "create",
		"threadId":  "thr-c1",
		"commentId": "cmt-1",
		"quote":     "Line one",
		"body":      "Initial comment",
	}); err != nil {
		t.Fatal(err)
	}
	fb, err := repo.SendPendingArtifactFeedback(ctx, artifact.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	items := feedbackItemsOf(t, fb)
	if fb == nil || feedbackRoundOf(t, fb) != 1 || len(items) != 1 || items[0]["action"] != "create" {
		t.Fatalf("expected create feedback round 1, got %+v", fb)
	}

	// 2. Resolving a thread stays silent (does not emit feedback).
	clock = clock.Add(time.Second)
	if _, err := repo.CommentOnArtifact(ctx, artifact.ID, "user", map[string]any{
		"type":     "resolve",
		"threadId": "thr-c1",
	}); err != nil {
		t.Fatal(err)
	}
	fb, err = repo.SendPendingArtifactFeedback(ctx, artifact.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if fb != nil {
		t.Fatalf("resolve should remain silent, got %+v", fb)
	}

	// 3. Reopening a resolved thread triggers feedback with action "reopen".
	clock = clock.Add(time.Second)
	if _, err := repo.CommentOnArtifact(ctx, artifact.ID, "user", map[string]any{
		"type":     "reopen",
		"threadId": "thr-c1",
	}); err != nil {
		t.Fatal(err)
	}
	fb, err = repo.SendPendingArtifactFeedback(ctx, artifact.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	items = feedbackItemsOf(t, fb)
	if fb == nil || feedbackRoundOf(t, fb) != 2 || len(items) != 1 || items[0]["action"] != "reopen" {
		t.Fatalf("expected reopen feedback round 2, got %+v", fb)
	}

	// 4. Editing an existing comment body triggers feedback with action "edit".
	clock = clock.Add(time.Second)
	if _, err := repo.CommentOnArtifact(ctx, artifact.ID, "user", map[string]any{
		"type":      "edit",
		"threadId":  "thr-c1",
		"commentId": "cmt-1",
		"body":      "Updated comment body",
	}); err != nil {
		t.Fatal(err)
	}
	fb, err = repo.SendPendingArtifactFeedback(ctx, artifact.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	items = feedbackItemsOf(t, fb)
	if fb == nil || feedbackRoundOf(t, fb) != 3 || len(items) != 1 || items[0]["action"] != "edit" || items[0]["body"] != "Updated comment body" {
		t.Fatalf("expected edit feedback round 3, got %+v", fb)
	}

	// 5. Re-sending with no changes produces nothing.
	fb, err = repo.SendPendingArtifactFeedback(ctx, artifact.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if fb != nil {
		t.Fatalf("expected nil feedback when unchanged, got %+v", fb)
	}
}

func TestArtifactFeedbackWithdrawBeforeAndAfterConsumption(t *testing.T) {
	ctx := t.Context()
	clock := time.UnixMilli(1900000000000)
	repo, err := Open(ctx, Options{Path: filepath.Join(t.TempDir(), "test.sqlite"), Now: func() time.Time { return clock }})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	project, err := repo.CreateProject(ctx, new("Fixture"), ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := repo.CreateArtifact(ctx, project.ID, NewArtifact{
		Kind:    "document",
		Title:   "Draft",
		Author:  project.CoordinatorID,
		Content: "Original",
	})
	if err != nil {
		t.Fatal(err)
	}

	clock = clock.Add(time.Second)
	if _, err := repo.ReviseArtifact(ctx, artifact.ID, ArtifactRevision{
		Author:      "user",
		BaseVersion: &artifact.Version,
		Content:     "Updated by user",
		Shared:      true,
	}); err != nil {
		t.Fatal(err)
	}

	fb, err := repo.SendPendingArtifactFeedback(ctx, artifact.ID, nil)
	if err != nil || fb == nil || feedbackRoundOf(t, fb) != 1 {
		t.Fatalf("expected round 1 feedback, got %+v, err=%v", fb, err)
	}

	// Withdraw before the coordinator starts a turn -> succeeds.
	clock = clock.Add(time.Second)
	withdrawn, err := repo.WithdrawArtifactFeedback(ctx, artifact.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !withdrawn {
		t.Fatal("expected withdraw to succeed before consumption")
	}

	// Pending wake-up should be cleared on the coordinator.
	var pendingSeq int64
	if err := repo.DB.QueryRowContext(ctx, "SELECT pending_seq FROM conversations WHERE id = ?", project.CoordinatorID).Scan(&pendingSeq); err != nil {
		t.Fatal(err)
	}
	if pendingSeq != 0 {
		t.Fatalf("expected coordinator pending_seq cleared to 0 after withdraw, got %d", pendingSeq)
	}

	// Sending again re-captures the same edit cleanly as round 2.
	clock = clock.Add(time.Second)
	fb2, err := repo.SendPendingArtifactFeedback(ctx, artifact.ID, nil)
	if err != nil || fb2 == nil || feedbackRoundOf(t, fb2) != 2 {
		t.Fatalf("expected round 2 feedback after withdraw, got %+v, err=%v", fb2, err)
	}

	// Once the coordinator starts a turn, withdrawing round 2 must fail cleanly.
	clock = clock.Add(time.Second)
	if _, err := repo.StartTurn(ctx, project.CoordinatorID, "test-model"); err != nil {
		t.Fatal(err)
	}
	withdrawn2, err := repo.WithdrawArtifactFeedback(ctx, artifact.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if withdrawn2 {
		t.Fatal("expected withdraw to return false after turn started consuming feedback")
	}
}

func TestArtifactFeedbackDecisionGroupsAndChoice(t *testing.T) {
	ctx := t.Context()
	clock := time.UnixMilli(1900000000000)
	repo, err := Open(ctx, Options{Path: filepath.Join(t.TempDir(), "test.sqlite"), Now: func() time.Time { return clock }})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	project, err := repo.CreateProject(ctx, new("Fixture"), ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}

	initialBoard := map[string]any{
		"version": 1,
		"nodes": []any{
			map[string]any{"id": "grp-arch", "type": "group", "x": 0, "y": 0, "width": 800, "height": 340, "data": map[string]any{"label": "Architecture", "round": 1, "pick": "one", "mode": "choose"}},
			map[string]any{"id": "opt-a", "type": "option", "parent": "grp-arch", "x": 20, "y": 50, "width": 320, "height": 240, "data": map[string]any{"title": "Monolith", "group": "grp-arch", "recommended": true}},
			map[string]any{"id": "opt-b", "type": "option", "parent": "grp-arch", "x": 380, "y": 50, "width": 320, "height": 240, "data": map[string]any{"title": "Modular", "group": "grp-arch"}},
		},
		"edges": []any{},
	}
	rawInitial, _ := json.Marshal(initialBoard)
	if !HasPendingDecisionGroups(string(rawInitial)) {
		t.Fatal("expected HasPendingDecisionGroups to be true for active decision group")
	}

	artifact, err := repo.CreateArtifact(ctx, project.ID, NewArtifact{
		Kind:    "whiteboard",
		Title:   "Decisions",
		Author:  project.CoordinatorID,
		Content: string(rawInitial),
	})
	if err != nil {
		t.Fatal(err)
	}

	// User picks opt-b in grp-arch.
	updatedBoard := map[string]any{
		"version": 1,
		"nodes": []any{
			map[string]any{"id": "grp-arch", "type": "group", "x": 0, "y": 0, "width": 800, "height": 340, "data": map[string]any{"label": "Architecture", "round": 1, "pick": "one", "mode": "choose", "confirmed": true}},
			map[string]any{"id": "opt-a", "type": "option", "parent": "grp-arch", "x": 20, "y": 50, "width": 320, "height": 240, "data": map[string]any{"title": "Monolith", "group": "grp-arch", "recommended": true}},
			map[string]any{"id": "opt-b", "type": "option", "parent": "grp-arch", "x": 380, "y": 50, "width": 320, "height": 240, "data": map[string]any{"title": "Modular", "group": "grp-arch", "chosen": true}},
		},
		"edges": []any{},
	}
	rawUpdated, _ := json.Marshal(updatedBoard)
	if HasPendingDecisionGroups(string(rawUpdated)) {
		t.Fatal("expected HasPendingDecisionGroups to be false when all decision groups are confirmed")
	}

	clock = clock.Add(time.Second)
	if _, err := repo.ReviseArtifact(ctx, artifact.ID, ArtifactRevision{
		Author:      "user",
		BaseVersion: &artifact.Version,
		Content:     string(rawUpdated),
		Shared:      true,
	}); err != nil {
		t.Fatal(err)
	}

	fb, err := repo.SendPendingArtifactFeedback(ctx, artifact.ID, nil)
	if err != nil || fb == nil {
		t.Fatalf("expected choice feedback, got %+v, err=%v", fb, err)
	}
	foundChoice := false
	for _, item := range feedbackItemsOf(t, fb) {
		if item["kind"] != "choice" {
			continue
		}
		foundChoice = true
		var entry object
		switch chosen := item["chosen"].(type) {
		case []object:
			if len(chosen) != 1 {
				t.Fatalf("expected 1 chosen option, got %+v", chosen)
			}
			entry = chosen[0]
		case []any:
			if len(chosen) != 1 {
				t.Fatalf("expected 1 chosen option, got %+v", chosen)
			}
			entry, _ = chosen[0].(map[string]any)
		default:
			t.Fatalf("unexpected chosen type %T", item["chosen"])
		}
		if entry["id"] != "opt-b" || entry["group"] != "grp-arch" || entry["groupTitle"] != "Architecture" {
			t.Fatalf("expected group metadata on choice entry, got %+v", entry)
		}
	}
	if !foundChoice {
		t.Fatalf("expected choice item in feedback, got %+v", fb["items"])
	}
}
