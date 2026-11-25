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

func TestGetArtifactDetailDocumentAskBaselineLifecycle(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	clock := time.UnixMilli(1923000000000)
	repo, err := Open(ctx, Options{Path: ":memory:", Now: func() time.Time { return clock }})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	project, err := repo.CreateProject(ctx, new("Fixture"), ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}

	v1Doc := "# 方案评审\n\n" +
		"```ask\n{\"id\":\"ask-1\",\"question\":\"缓存选型？\",\"pick\":\"one\",\"options\":[{\"id\":\"redis\",\"label\":\"Redis\",\"recommended\":true},{\"id\":\"mem\",\"label\":\"内存\"}]}\n```\n\n" +
		"```ask\n{\"id\":\"ask-2\",\"question\":\"需要哪些能力？\",\"pick\":\"many\",\"options\":[{\"id\":\"b\",\"label\":\"B\"},{\"id\":\"a\",\"label\":\"A\"}]}\n```\n"
	artifact, err := repo.CreateArtifact(ctx, project.ID, NewArtifact{
		Kind:    "document",
		Title:   "方案评审",
		Author:  project.CoordinatorID,
		Content: v1Doc,
	})
	if err != nil {
		t.Fatal(err)
	}

	// 1. Initial AI document -> baseline is v1 with empty answers.
	detail, err := repo.GetArtifactDetail(ctx, artifact.ID, nil, false, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if detail.AskBaseline == nil || detail.AskBaseline.Version != 1 || len(detail.AskBaseline.Asks) != 2 {
		t.Fatalf("expected v1 baseline with 2 asks, got %+v", detail.AskBaseline)
	}
	if len(detail.AskBaseline.Asks[0].Selected) != 0 || detail.AskBaseline.Asks[0].Note != "" {
		t.Fatalf("expected empty v1 baseline answers, got %+v", detail.AskBaseline.Asks)
	}

	// 2. User answers both asks without submitting -> baseline stays at v1 (empty).
	v2Doc := "# 方案评审\n\n" +
		"```ask\n{\"id\":\"ask-1\",\"question\":\"缓存选型？\",\"pick\":\"one\",\"options\":[{\"id\":\"redis\",\"label\":\"Redis\",\"recommended\":true},{\"id\":\"mem\",\"label\":\"内存\"}],\"selected\":[\"redis\"],\"note\":\"  优先多实例  \"}\n```\n\n" +
		"```ask\n{\"id\":\"ask-2\",\"question\":\"需要哪些能力？\",\"pick\":\"many\",\"options\":[{\"id\":\"b\",\"label\":\"B\"},{\"id\":\"a\",\"label\":\"A\"}],\"selected\":[\"b\",\"a\"]}\n```\n"
	clock = clock.Add(time.Second)
	if _, err := repo.ReviseArtifact(ctx, artifact.ID, ArtifactRevision{
		Content:     v2Doc,
		BaseVersion: new(int64(1)),
		Author:      "user",
		Reason:      "answer asks",
	}); err != nil {
		t.Fatal(err)
	}
	detail, err = repo.GetArtifactDetail(ctx, artifact.ID, nil, false, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if detail.AskBaseline == nil || detail.AskBaseline.Version != 1 || len(detail.AskBaseline.Asks[0].Selected) != 0 {
		t.Fatalf("expected unsubmitted answers to keep v1 baseline, got %+v", detail.AskBaseline)
	}

	// 3. User submits feedback round 1 -> baseline advances to v2 with normalized answers.
	clock = clock.Add(time.Second)
	fb, err := repo.SendPendingArtifactFeedback(ctx, artifact.ID, nil)
	if err != nil || fb == nil {
		t.Fatalf("SendPendingArtifactFeedback failed: fb=%+v err=%v", fb, err)
	}
	detail, err = repo.GetArtifactDetail(ctx, artifact.ID, nil, false, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if detail.AskBaseline == nil || detail.AskBaseline.Version != 2 || len(detail.AskBaseline.Asks) != 2 {
		t.Fatalf("expected v2 baseline after submit, got %+v", detail.AskBaseline)
	}
	if got := detail.AskBaseline.Asks[0]; got.ID != "ask-1" || len(got.Selected) != 1 || got.Selected[0] != "redis" || got.Note != "优先多实例" {
		t.Fatalf("unexpected ask-1 baseline after submit: %+v", got)
	}
	if got := detail.AskBaseline.Asks[1]; got.ID != "ask-2" || len(got.Selected) != 2 || got.Selected[0] != "a" || got.Selected[1] != "b" {
		t.Fatalf("expected sorted selected IDs for ask-2, got %+v", got)
	}

	// 4. Withdrawing round 1 reverts baseline back to v1.
	clock = clock.Add(time.Second)
	withdrawn, err := repo.WithdrawArtifactFeedback(ctx, artifact.ID, 1)
	if err != nil || !withdrawn {
		t.Fatalf("expected withdraw to succeed, withdrawn=%v err=%v", withdrawn, err)
	}
	detail, err = repo.GetArtifactDetail(ctx, artifact.ID, nil, false, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if detail.AskBaseline == nil || detail.AskBaseline.Version != 1 || len(detail.AskBaseline.Asks[0].Selected) != 0 {
		t.Fatalf("expected baseline to revert to v1 after withdraw, got %+v", detail.AskBaseline)
	}

	// 5. Re-submit round 2, then user edits ask-1 without submitting -> baseline stays at v2.
	clock = clock.Add(time.Second)
	if fb2, err := repo.SendPendingArtifactFeedback(ctx, artifact.ID, nil); err != nil || fb2 == nil {
		t.Fatalf("expected round 2 feedback, fb=%+v err=%v", fb2, err)
	}
	v3Doc := "# 方案评审\n\n" +
		"```ask\n{\"id\":\"ask-1\",\"question\":\"缓存选型？\",\"pick\":\"one\",\"options\":[{\"id\":\"redis\",\"label\":\"Redis\",\"recommended\":true},{\"id\":\"mem\",\"label\":\"内存\"}],\"selected\":[\"mem\"]}\n```\n\n" +
		"```ask\n{\"id\":\"ask-2\",\"question\":\"需要哪些能力？\",\"pick\":\"many\",\"options\":[{\"id\":\"b\",\"label\":\"B\"},{\"id\":\"a\",\"label\":\"A\"}],\"selected\":[\"b\",\"a\"]}\n```\n"
	clock = clock.Add(time.Second)
	if _, err := repo.ReviseArtifact(ctx, artifact.ID, ArtifactRevision{
		Content:     v3Doc,
		BaseVersion: new(int64(2)),
		Author:      "user",
		Reason:      "change ask-1",
	}); err != nil {
		t.Fatal(err)
	}
	detail, err = repo.GetArtifactDetail(ctx, artifact.ID, nil, false, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if detail.AskBaseline == nil || detail.AskBaseline.Version != 2 || detail.AskBaseline.Asks[0].Selected[0] != "redis" {
		t.Fatalf("expected baseline to stay at v2 before submitting v3, got %+v", detail.AskBaseline)
	}

	// 6. Agent revises doc to v4 keeping answered asks unconfirmed -> baseline advances to v4.
	clock = clock.Add(time.Second)
	if _, err := repo.ReviseArtifact(ctx, artifact.ID, ArtifactRevision{
		Content:     v3Doc + "\n补充结论。\n",
		BaseVersion: new(int64(3)),
		Author:      project.CoordinatorID,
		Reason:      "agent update",
	}); err != nil {
		t.Fatal(err)
	}
	detail, err = repo.GetArtifactDetail(ctx, artifact.ID, nil, false, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if detail.AskBaseline == nil || detail.AskBaseline.Version != 4 || detail.AskBaseline.Asks[0].Selected[0] != "mem" {
		t.Fatalf("expected baseline to advance to agent v4, got %+v", detail.AskBaseline)
	}

	// 7. Historical version queries omit AskBaseline.
	hist, err := repo.GetArtifactDetail(ctx, artifact.ID, new(int64(1)), false, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if hist.AskBaseline != nil {
		t.Fatalf("expected nil AskBaseline for historical version, got %+v", hist.AskBaseline)
	}
}
