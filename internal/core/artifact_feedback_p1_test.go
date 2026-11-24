package core

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestWhiteboardVerifyModeFeedbackAndAutoPause(t *testing.T) {
	ctx := t.Context()
	clock := time.UnixMilli(1920000000000)
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
			map[string]any{
				"id": "grp-v1", "type": "group", "x": 0, "y": 0, "width": 700, "height": 320,
				"data": map[string]any{"label": "首页验收", "round": 1, "mode": "verify"},
			},
			map[string]any{
				"id": "item-hero", "type": "option", "parent": "grp-v1", "x": 20, "y": 50, "width": 300, "height": 220,
				"data": map[string]any{"title": "Hero 区域", "group": "grp-v1"},
			},
			map[string]any{
				"id": "item-footer", "type": "option", "parent": "grp-v1", "x": 350, "y": 50, "width": 300, "height": 220,
				"data": map[string]any{"title": "Footer 对齐", "group": "grp-v1"},
			},
		},
		"edges": []any{},
	}
	rawInitial, _ := json.Marshal(initialBoard)
	artifact, err := repo.CreateArtifact(ctx, project.ID, NewArtifact{
		Kind:    "whiteboard",
		Title:   "视觉验收",
		Author:  project.CoordinatorID,
		Content: string(rawInitial),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !HasPendingDecisionGroups(artifact.Content) {
		t.Fatal("expected HasPendingDecisionGroups to be true for active verify group")
	}

	// User records verification verdicts.
	reviewedBoard := map[string]any{
		"version": 1,
		"nodes": []any{
			map[string]any{
				"id": "grp-v1", "type": "group", "x": 0, "y": 0, "width": 700, "height": 320,
				"data": map[string]any{"label": "首页验收", "round": 1, "mode": "verify"},
			},
			map[string]any{
				"id": "item-hero", "type": "option", "parent": "grp-v1", "x": 20, "y": 50, "width": 300, "height": 220,
				"data": map[string]any{"title": "Hero 区域", "group": "grp-v1", "verdict": "approved"},
			},
			map[string]any{
				"id": "item-footer", "type": "option", "parent": "grp-v1", "x": 350, "y": 50, "width": 300, "height": 220,
				"data": map[string]any{"title": "Footer 对齐", "group": "grp-v1", "verdict": "changes_requested"},
			},
		},
		"edges": []any{},
	}
	rawReviewed, _ := json.Marshal(reviewedBoard)
	clock = clock.Add(time.Second)
	if _, err := repo.ReviseArtifact(ctx, artifact.ID, ArtifactRevision{
		Content:     string(rawReviewed),
		BaseVersion: new(int64(1)),
		Author:      "user",
		Reason:      "review",
	}); err != nil {
		t.Fatal(err)
	}

	// Idle auto-send must stay paused while verify group is unconfirmed.
	autoFB, err := repo.SendAutoPendingArtifactFeedback(ctx, artifact.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if autoFB != nil {
		t.Fatalf("expected auto feedback to pause for active verify group, got %+v", autoFB)
	}

	// Explicit submit emits a structured verify feedback item without noisy board edit items.
	fb, err := repo.SendPendingArtifactFeedback(ctx, artifact.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	items := feedbackItemsOf(t, fb)
	if len(items) != 1 || items[0].str("kind") != "verify" {
		t.Fatalf("expected 1 verify feedback item, got %+v", items)
	}
	verdicts, _ := items[0]["verdicts"].([]any)
	if len(verdicts) != 2 {
		t.Fatalf("expected 2 verdicts, got %+v", verdicts)
	}
}

func TestDocumentInlineAskFeedbackAndAutoPause(t *testing.T) {
	ctx := t.Context()
	clock := time.UnixMilli(1921000000000)
	repo, err := Open(ctx, Options{Path: filepath.Join(t.TempDir(), "test.sqlite"), Now: func() time.Time { return clock }})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	project, err := repo.CreateProject(ctx, new("Fixture"), ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}

	initialDoc := "# 接口设计\n\n```ask\n{\"id\":\"ask-cache\",\"question\":\"缓存方案选哪种？\",\"pick\":\"one\",\"allowNote\":true,\"options\":[{\"id\":\"redis\",\"label\":\"Redis\",\"recommended\":true},{\"id\":\"local\",\"label\":\"本地内存\"}]}\n```\n"
	artifact, err := repo.CreateArtifact(ctx, project.ID, NewArtifact{
		Kind:    "document",
		Title:   "接口设计",
		Author:  project.CoordinatorID,
		Content: initialDoc,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !HasPendingDocumentAsks(artifact.Content) {
		t.Fatal("expected HasPendingDocumentAsks to be true")
	}

	answeredDoc := "# 接口设计\n\n```ask\n{\"id\":\"ask-cache\",\"question\":\"缓存方案选哪种？\",\"pick\":\"one\",\"options\":[{\"id\":\"redis\",\"label\":\"Redis\",\"recommended\":true},{\"id\":\"local\",\"label\":\"本地内存\"}],\"selected\":[\"redis\"],\"note\":\"支持多实例共享\"}\n```\n"
	clock = clock.Add(time.Second)
	if _, err := repo.ReviseArtifact(ctx, artifact.ID, ArtifactRevision{
		Content:     answeredDoc,
		BaseVersion: new(int64(1)),
		Author:      "user",
		Reason:      "answer ask",
	}); err != nil {
		t.Fatal(err)
	}

	// Background idle auto-send pauses while an inline ask is active.
	autoFB, err := repo.SendAutoPendingArtifactFeedback(ctx, artifact.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if autoFB != nil {
		t.Fatalf("expected auto feedback to pause for active ask block, got %+v", autoFB)
	}

	// Explicit submit emits structured answer item (and no raw JSON edit diff).
	fb, err := repo.SendPendingArtifactFeedback(ctx, artifact.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	items := feedbackItemsOf(t, fb)
	if len(items) != 1 || items[0].str("kind") != "answer" {
		t.Fatalf("expected 1 answer item without raw edit diff, got %+v", items)
	}
	answers, _ := items[0]["answers"].([]any)
	if len(answers) != 1 {
		t.Fatalf("expected 1 answer entry, got %+v", answers)
	}
	ans := object(answers[0].(map[string]any))
	if ans.str("id") != "ask-cache" || ans.str("note") != "支持多实例共享" {
		t.Fatalf("unexpected answer payload: %+v", ans)
	}

	// Once AI confirms the ask block, HasPendingDocumentAsks becomes false.
	confirmedDoc := "# 接口设计\n\n```ask\n{\"id\":\"ask-cache\",\"question\":\"缓存方案选哪种？\",\"pick\":\"one\",\"options\":[{\"id\":\"redis\",\"label\":\"Redis\",\"recommended\":true},{\"id\":\"local\",\"label\":\"本地内存\"}],\"selected\":[\"redis\"],\"note\":\"支持多实例共享\",\"confirmed\":true}\n```\n"
	if HasPendingDocumentAsks(confirmedDoc) {
		t.Fatal("expected HasPendingDocumentAsks to be false when confirmed")
	}
}
