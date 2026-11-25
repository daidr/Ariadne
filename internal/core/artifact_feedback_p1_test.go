package core

import (
	"testing"
	"time"
)

func TestDocumentInlineAskFeedbackAndAutoPause(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	clock := time.UnixMilli(1921000000000)
	repo, err := Open(ctx, Options{Path: ":memory:", Now: func() time.Time { return clock }})
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

func TestDocumentInlineAskTagAndShorthandNormalization(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	clock := time.UnixMilli(1922000000000)
	repo, err := Open(ctx, Options{Path: ":memory:", Now: func() time.Time { return clock }})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	project, err := repo.CreateProject(ctx, new("Fixture"), ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}

	// Author creates a document using shorthand <ask> HTML tag and shorthand ```ask fence
	// (omitting id, pick, and using string array options).
	q1 := "登录态采用哪种方案？"
	q2 := "缓存方案选哪种？"
	initialDoc := "# 架构评审\n\n" +
		`<ask question="` + q1 + `" options='["JWT","Session"]'></ask>` + "\n\n" +
		"```ask\n{\"question\":\"" + q2 + "\",\"options\":[\"Redis\",\"本地内存\"]}\n```\n"
	artifact, err := repo.CreateArtifact(ctx, project.ID, NewArtifact{
		Kind:    "document",
		Title:   "架构评审",
		Author:  project.CoordinatorID,
		Content: initialDoc,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !HasPendingDocumentAsks(artifact.Content) {
		t.Fatal("expected HasPendingDocumentAsks to be true for <ask> tag document")
	}

	// Simulate ReadDocument writing back canonical ```ask fences after the user answers.
	id1 := FallbackAskID(q1)
	id2 := FallbackAskID(q2)
	answeredDoc := "# 架构评审\n\n" +
		"```ask\n{\"id\":\"" + id1 + "\",\"options\":[{\"id\":\"opt-1\",\"label\":\"JWT\"},{\"id\":\"opt-2\",\"label\":\"Session\"}],\"pick\":\"one\",\"question\":\"" + q1 + "\",\"selected\":[\"opt-1\"]}\n```\n\n" +
		"```ask\n{\"id\":\"" + id2 + "\",\"options\":[{\"id\":\"opt-1\",\"label\":\"Redis\"},{\"id\":\"opt-2\",\"label\":\"本地内存\"}],\"pick\":\"one\",\"question\":\"" + q2 + "\",\"selected\":[\"opt-2\"],\"note\":\"单机部署\"}\n```\n"
	clock = clock.Add(time.Second)
	if _, err := repo.ReviseArtifact(ctx, artifact.ID, ArtifactRevision{
		Content:     answeredDoc,
		BaseVersion: new(int64(1)),
		Author:      "user",
		Reason:      "answer asks",
	}); err != nil {
		t.Fatal(err)
	}

	// Idle auto-send must stay paused while asks are unconfirmed.
	if autoFB, err := repo.SendAutoPendingArtifactFeedback(ctx, artifact.ID, nil); err != nil {
		t.Fatal(err)
	} else if autoFB != nil {
		t.Fatalf("expected auto feedback to pause, got %+v", autoFB)
	}

	// Explicit submit must emit only a single structured answer item and NO noisy raw edit diff.
	fb, err := repo.SendPendingArtifactFeedback(ctx, artifact.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	items := feedbackItemsOf(t, fb)
	if len(items) != 1 || items[0].str("kind") != "answer" {
		t.Fatalf("expected only 1 answer item without raw edit diff, got %+v", items)
	}
	answers, _ := items[0]["answers"].([]any)
	if len(answers) != 2 {
		t.Fatalf("expected 2 answers, got %+v", answers)
	}
	ans1 := object(answers[0].(map[string]any))
	if ans1.str("id") != id1 {
		t.Fatalf("expected answer 1 id %q, got %q", id1, ans1.str("id"))
	}
	ans2 := object(answers[1].(map[string]any))
	if ans2.str("id") != id2 || ans2.str("note") != "单机部署" {
		t.Fatalf("unexpected answer 2: %+v", ans2)
	}
}

func TestFallbackAskIDDeterministicUTF8FNV1a(t *testing.T) {
	t.Parallel()
	// Cross-checked against frontend fallbackAskId in ask-ref-blocks.test.ts.
	cases := []struct {
		question string
		want     string
	}{
		{"Choose auth strategy", "ask-b7d87cc3"},
		{"登录态采用哪种方案？", "ask-689d2ac1"},
		{"发布目标 🚀", "ask-02560760"},
	}
	for _, tc := range cases {
		if got := FallbackAskID(tc.question); got != tc.want {
			t.Errorf("FallbackAskID(%q) = %q, want %q", tc.question, got, tc.want)
		}
	}
}
