package core

import (
	"path/filepath"
	"testing"
)

func TestPullRequestConflictInThreadAndPlanViews(t *testing.T) {
	ctx := t.Context()
	repo, err := Open(ctx, Options{Path: filepath.Join(t.TempDir(), "ariadne.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	project, err := repo.CreateProject(ctx, new("Fixture"), ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.RevisePlan(ctx, project.ID, PlanRevision{
		Author: "user",
		Reason: "add task",
		Ops:    []map[string]any{planAdd("node_conflict", "Check conflict")},
	}); err != nil {
		t.Fatal(err)
	}
	thread, err := repo.CreateThread(ctx, project.ID, NewThread{Title: "Conflict PR", NodeID: new("node_conflict")})
	if err != nil {
		t.Fatal(err)
	}

	readPlanPR := func() map[string]any {
		t.Helper()
		plan, err := repo.GetPlan(ctx, project.ID)
		if err != nil {
			t.Fatal(err)
		}
		nodes, _ := plan["nodes"].([]object)
		if len(nodes) != 1 {
			t.Fatalf("expected 1 node, got %d (%T)", len(nodes), plan["nodes"])
		}
		threads, _ := nodes[0]["threads"].([]object)
		if len(threads) != 1 {
			t.Fatalf("expected 1 thread, got %d (%T)", len(threads), nodes[0]["threads"])
		}
		return asObject(threads[0]["pullRequest"])
	}

	openConflicted := object{
		"number":     157,
		"url":        "https://github.com/daidr/Ariadne/pull/157",
		"title":      "PR with conflicts",
		"state":      "open",
		"conflicted": true,
		"headSha":    "abc123",
		"checks":     object{"state": "success", "failed": []any{}},
	}
	if err := repo.SyncPullRequest(ctx, thread.ID, openConflicted, nil); err != nil {
		t.Fatal(err)
	}

	gotThread, err := repo.GetThread(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pr := asObject(gotThread.PullRequest); pr["conflicted"] != true {
		t.Fatalf("thread pullRequest conflicted = %#v, want true", pr["conflicted"])
	}
	if pr := readPlanPR(); pr["conflicted"] != true {
		t.Fatalf("plan pullRequest conflicted = %#v, want true", pr["conflicted"])
	}

	merged := object{
		"number":     157,
		"url":        "https://github.com/daidr/Ariadne/pull/157",
		"title":      "PR with conflicts",
		"state":      "merged",
		"conflicted": true,
		"headSha":    "abc123",
		"checks":     object{"state": "success", "failed": []any{}},
	}
	if err := repo.SyncPullRequest(ctx, thread.ID, merged, nil); err != nil {
		t.Fatal(err)
	}
	if pr := readPlanPR(); pr["conflicted"] != nil {
		t.Fatalf("merged plan pullRequest should omit conflicted, got %#v", pr)
	}
}
