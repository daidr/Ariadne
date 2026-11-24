package core

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRepoBaselinesFirstTouchAndReplayCompatibility(t *testing.T) {
	ctx := t.Context()
	dbPath := filepath.Join(t.TempDir(), "data.sqlite")
	repo, err := Open(ctx, Options{Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	project, err := repo.CreateProject(ctx, new("Multi"), ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := repo.CreateThread(ctx, project.ID, NewThread{Title: "Embedded baseline test"})
	if err != nil {
		t.Fatal(err)
	}

	head1 := strings.Repeat("1", 40)
	head2 := strings.Repeat("2", 40)
	head3 := strings.Repeat("3", 40)

	// First touch records the baseline HEAD.
	if err := repo.RecordRepoBaselines(ctx, thread.ID, []RepoBaseline{
		{SourceID: "src_main", RepoPath: "packages/sub", BaseHead: head1, BaseBranch: "main"},
	}); err != nil {
		t.Fatal(err)
	}

	// Second call for the same (sourceId, repoPath) must NOT overwrite the first-touch HEAD,
	// while a new repo path in the same batch is still recorded.
	if err := repo.RecordRepoBaselines(ctx, thread.ID, []RepoBaseline{
		{SourceID: "src_main", RepoPath: "packages/sub", BaseHead: head2, BaseBranch: "feat/x"},
		{SourceID: "src_main", RepoPath: "vendor/lib", BaseHead: head3, BaseBranch: "develop"},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := repo.RepoBaselines(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 baselines, got %+v", got)
	}
	if got[0].RepoPath != "packages/sub" || got[0].BaseHead != head1 || got[0].BaseBranch != "main" {
		t.Fatalf("first baseline overwritten or unexpected: %+v", got[0])
	}
	if got[1].RepoPath != "vendor/lib" || got[1].BaseHead != head3 || got[1].BaseBranch != "develop" {
		t.Fatalf("second baseline unexpected: %+v", got[1])
	}

	// Verify multi-repo turn notice and legacy single-repo turn notice both project cleanly.
	if _, err := repo.PostMessage(ctx, project.ID, MessageInput{ConversationID: thread.ID, Author: "user", Content: "go"}); err != nil {
		t.Fatal(err)
	}
	turn1, err := repo.StartScheduledTurn(ctx, thread.ID, "mock")
	if err != nil {
		t.Fatal(err)
	}
	added1, removed1 := int64(5), int64(1)
	added2, removed2 := int64(3), int64(2)
	if err := repo.RecordTurnRepoChanges(ctx, thread.ID, turn1, "", "", nil, []TurnRepoChanges{
		{
			SourceID: "src_main",
			RepoPath: "",
			Branch:   "ariadne/t",
			From:     head1,
			To:       head2,
			Files:    []TurnFile{{Path: "root.go", Status: "modified", Added: &added1, Removed: &removed1}},
		},
		{
			SourceID: "src_main",
			RepoPath: "packages/sub",
			Branch:   "feat/sub",
			From:     head2,
			To:       head3,
			Files:    []TurnFile{{Path: "sub.go", Status: "added", Added: &added2, Removed: &removed2}},
		},
	}, &ChangeTotals{Files: 2, Added: 8, Removed: 3}); err != nil {
		t.Fatal(err)
	}
	if err := repo.FinishTurn(ctx, thread.ID, turn1, "completed", nil); err != nil {
		t.Fatal(err)
	}

	notices, err := repo.ListNotices(ctx, thread.ID, NoticeRange{})
	if err != nil {
		t.Fatal(err)
	}
	var turnNotice object
	for _, n := range notices {
		if n["kind"] == "turn-changes" {
			turnNotice = n
		}
	}
	if turnNotice == nil {
		t.Fatalf("expected turn-changes notice, got %+v", notices)
	}
	reposRaw, ok := turnNotice["repos"].([]any)
	if !ok || len(reposRaw) != 2 {
		t.Fatalf("expected 2 repos in turn-changes notice, got %#v", turnNotice["repos"])
	}
	totalMap, ok := turnNotice["total"].(map[string]any)
	if !ok || totalMap["files"] != float64(2) || totalMap["added"] != float64(8) || totalMap["removed"] != float64(3) {
		t.Fatalf("unexpected total in turn-changes notice: %#v", turnNotice["total"])
	}

	// Rebuild projection from scratch and verify replay restores baselines and notices.
	if err := repo.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}

	replayed, err := repo.RepoBaselines(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed) != 2 || replayed[0].BaseHead != head1 || replayed[1].BaseHead != head3 {
		t.Fatalf("expected replayed baselines to match, got %+v", replayed)
	}
}
