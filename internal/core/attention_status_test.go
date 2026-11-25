package core

import (
	"testing"
)

func TestWorkStatusDoesNotCreateUserAttention(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	repo, err := Open(ctx, Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	project, err := repo.CreateProject(ctx, new("External delivery"), ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SetProjectDelivery(ctx, project.ID, object{"mode": "external", "instructions": "Push to staging and update the ticket"}); err != nil {
		t.Fatal(err)
	}
	thread, err := repo.CreateThread(ctx, project.ID, NewThread{Title: "Staging fix"})
	if err != nil {
		t.Fatal(err)
	}
	report := func(kind string) {
		t.Helper()
		if _, err := repo.ReportThread(ctx, thread.ID, ThreadReport{
			Kind: kind, Headline: "Pushed to staging", Summary: "The target branch has the fix. Unrelated Stylelint checks failed.",
		}, nil, ReviewOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	check := func(stage string, count int) {
		t.Helper()
		detail, err := repo.GetProjectDetail(ctx, project.ID)
		if err != nil || len(detail.Threads) != 1 || detail.Threads[0].Stage != stage || len(detail.NeedsMe) != count || detail.Project.NeedsMe != count {
			t.Fatalf("want stage %s and %d items, got %+v, %v", stage, count, detail, err)
		}
		projects, err := repo.ListProjects(ctx)
		if err != nil || len(projects) != 1 || projects[0].NeedsMe != count {
			t.Fatalf("sidebar count differs: %+v, %v", projects, err)
		}
	}
	report("blocked")
	check("blocked", 0)
	report("failed")
	check("blocked", 0)
	ask, err := repo.PostMessage(ctx, project.ID, MessageInput{ConversationID: thread.ID, Author: "thread", Blocks: []any{object{"type": "confirm", "title": "Use the other staging branch?"}}})
	if err != nil {
		t.Fatal(err)
	}
	check("answer", 1)
	if err := repo.WithdrawAttention(ctx, thread.ID, ask.AttentionID(), "Already resolved"); err != nil {
		t.Fatal(err)
	}
	check("blocked", 0)
	turn, err := repo.StartTurn(ctx, thread.ID, "mock")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.FinishTurn(ctx, thread.ID, turn, "failed", nil); err != nil {
		t.Fatal(err)
	}
	check("blocked", 1)
	turn, err = repo.StartTurn(ctx, thread.ID, "mock")
	if err != nil {
		t.Fatal(err)
	}
	report("ready")
	if err := repo.FinishTurn(ctx, thread.ID, turn, "completed", nil); err != nil {
		t.Fatal(err)
	}
	check("done", 0)
	facts := StageFacts{Started: true, LastReport: "done", ChecksGaveUp: true}
	if stage := ThreadStage(facts); stage != "done" {
		t.Fatalf("CI retry limit overwrote completed delivery: %s", stage)
	}
}

func TestPullRequestDeliveryFollowsTheHost(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	repo, err := Open(ctx, Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	project, err := repo.CreateProject(ctx, new("Hosted delivery"), ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := repo.CreateThread(ctx, project.ID, NewThread{Title: "Fix"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ReportThread(ctx, thread.ID, ThreadReport{
		Kind: "ready", Headline: "Ready", Summary: "Ready for review",
	}, nil, ReviewOptions{}); err != nil {
		t.Fatal(err)
	}
	assertStatus := func(stage string, count int) {
		t.Helper()
		detail, err := repo.GetProjectDetail(ctx, project.ID)
		if err != nil || len(detail.Threads) != 1 || detail.Threads[0].Stage != stage || len(detail.NeedsMe) != count {
			t.Fatalf("want %s and %d items, got %+v, %v", stage, count, detail, err)
		}
		projects, err := repo.ListProjects(ctx)
		if err != nil || len(projects) != 1 || projects[0].NeedsMe != count {
			t.Fatalf("want sidebar count %d, got %+v, %v", count, projects, err)
		}
	}
	assertStatus("review", 1)
	for _, stage := range []string{"open", "closed", "merged"} {
		if err := repo.SyncPullRequest(ctx, thread.ID, object{
			"number": 1, "state": stage, "headSha": "abc",
			"checks": object{"state": "pending"},
		}, nil); err != nil {
			t.Fatal(err)
		}
		if stage == "merged" {
			assertStatus("done", 0)
		} else {
			assertStatus("review", 0)
		}
	}
}
