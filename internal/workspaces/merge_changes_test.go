package workspaces

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/local"
	"github.com/daidr/Ariadne/internal/sources"
	"github.com/daidr/Ariadne/sdk"
)

func initHostedChangesRepo(t *testing.T) (*core.Repository, *Manager, core.ProjectView, *sdk.PullRequestInfo) {
	t.Helper()
	ctx := t.Context()
	repoDir := t.TempDir()
	initTemplateRepo(t, repoDir)

	dataDir := t.TempDir()
	repo, err := core.Open(ctx, core.Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repo.Close() })

	prInfo := &sdk.PullRequestInfo{
		Number:  180,
		URL:     "https://github.com/owner/repo/pull/180",
		Title:   "Backfill totals",
		State:   "open",
		HeadSHA: "1111111111111111111111111111111111111111",
	}

	registry := sources.New()
	if _, err := registry.Register(sdk.ContextProvider{
		Info: sdk.ContextProviderInfo{Kind: "github", Provides: []string{"workspace", "repo"}},
		Connect: func(connectCtx context.Context, _ map[string]any) (sdk.Capabilities, error) {
			factory, err := local.NewWorkspaces(connectCtx, local.FactoryOptions{
				Path:         repoDir,
				DataDir:      dataDir,
				DisableClone: true,
			})
			if err != nil {
				return sdk.Capabilities{}, err
			}
			return sdk.Capabilities{
				Workspace: factory.Capability(),
				Repo: &sdk.RepoCapability{
					Remote: "owner/repo",
					DefaultBranch: func(context.Context) (string, error) {
						return "main", nil
					},
					PullRequest: func(context.Context, int) (sdk.PullRequestInfo, error) {
						return *prInfo, nil
					},
					PullRequestFor: func(context.Context, string) (*sdk.PullRequestInfo, error) {
						copyPR := *prInfo
						return &copyPR, nil
					},
					Checks: func(context.Context, string) ([]sdk.CheckInfo, error) {
						return []sdk.CheckInfo{}, nil
					},
				},
				Close: factory.Close,
			}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	manager, err := New(Options{Repository: repo, Sources: registry})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeManager(t, manager) })

	project, err := repo.CreateProject(ctx, new("Hosted"), core.ProjectStart{
		Source: core.Source{Kind: "github", Settings: map[string]any{"repository": "owner/repo"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return repo, manager, project, prInfo
}

func commitInWorkspace(t *testing.T, ws sdk.Workspace, file, content, message string, tracked bool) {
	t.Helper()
	ctx := t.Context()
	if err := ws.WriteFile(ctx, file, content); err != nil {
		t.Fatal(err)
	}
	git := local.NewGit(ws.Info().Label, "file")
	if tracked {
		res := git(ctx, []string{"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qam", message}, local.GitOptions{})
		if res.ExitCode != 0 {
			t.Fatalf("git commit -qam: exit=%d stderr=%s", res.ExitCode, res.Stderr)
		}
		return
	}
	for _, argv := range [][]string{
		{"add", file},
		{"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-q", "-m", message},
	} {
		res := git(ctx, argv, local.GitOptions{})
		if res.ExitCode != 0 {
			t.Fatalf("git %v: exit=%d stderr=%s", argv, res.ExitCode, res.Stderr)
		}
	}
}

func TestThreadChangesAndSyncPullRequestBackfillTotals(t *testing.T) {
	if testing.Short() {
		t.Skip("PR totals backfill matrix covered by full suite")
	}
	t.Parallel()
	ctx := t.Context()
	repo, manager, project, prInfo := initHostedChangesRepo(t)

	thread, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "Backfill and self-heal"})
	if err != nil {
		t.Fatal(err)
	}
	ws, err := manager.ForThread(ctx, project.ID, thread.ID)
	if err != nil {
		t.Fatal(err)
	}

	// 0. Clean outer repo: ThreadChangesSince self-heals stale non-zero totals, then takes fast path.
	initial, err := manager.ThreadChanges(ctx, thread.ID)
	if err != nil || initial.Changes == nil || initial.Changes.Head == "" || initial.Changes.Head != initial.Changes.BaseHead {
		t.Fatalf("expected clean outer repo with Head==BaseHead, got %+v err=%v", initial, err)
	}
	known := &sdk.BranchRevisions{Head: initial.Changes.Head, BaseHead: initial.Changes.BaseHead}
	if err := repo.SyncBranchChanges(ctx, thread.ID, core.ChangeTotals{Files: 500, Added: 10000, Removed: 2000}); err != nil {
		t.Fatal(err)
	}
	healed, err := manager.ThreadChangesSince(ctx, thread.ID, known)
	if err != nil || healed.Unchanged {
		t.Fatalf("expected stale non-zero totals on empty outer repo to bypass Unchanged fast path, got %+v err=%v", healed, err)
	}
	if afterHeal, err := repo.GetThread(ctx, thread.ID); err != nil || !core.SameChangeTotals(afterHeal.Changes, core.ChangeTotals{}) {
		t.Fatalf("expected thread.Changes to be reconciled to zero, got %#v err=%v", afterHeal.Changes, err)
	}
	if nextPoll, err := manager.ThreadChangesSince(ctx, thread.ID, known); err != nil || !nextPoll.Unchanged {
		t.Fatalf("expected Unchanged=true once stale totals are cleared, got %+v err=%v", nextPoll, err)
	}

	// 1. ThreadChanges backfills NULL thread.Changes and is idempotent on repeat reads.
	if _, err := repo.Store.DB().ExecContext(ctx, "UPDATE threads SET changes = NULL WHERE id = ?", thread.ID); err != nil {
		t.Fatal(err)
	}
	var syncedBefore int
	if err := repo.Store.DB().QueryRowContext(ctx, "SELECT count(*) FROM events WHERE type = 'branch.changes_synced'").Scan(&syncedBefore); err != nil {
		t.Fatal(err)
	}
	commitInWorkspace(t, ws, "README.md", "hello\nsecond\nthird\n", "add lines", true)

	result, err := manager.ThreadChanges(ctx, thread.ID)
	if err != nil || result.Changes == nil {
		t.Fatalf("ThreadChanges: result=%+v err=%v", result, err)
	}
	updated, err := repo.GetThread(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	totals, ok := updated.Changes.(map[string]any)
	if !ok || totals["files"] != float64(1) || totals["added"] != float64(2) || totals["removed"] != float64(0) {
		t.Fatalf("unexpected backfilled totals: %#v", updated.Changes)
	}

	// Second call with identical state must not append another branch.changes_synced event.
	if _, err := manager.ThreadChanges(ctx, thread.ID); err != nil {
		t.Fatal(err)
	}
	var syncedAfter int
	if err := repo.Store.DB().QueryRowContext(ctx, "SELECT count(*) FROM events WHERE type = 'branch.changes_synced'").Scan(&syncedAfter); err != nil {
		t.Fatal(err)
	}
	if syncedAfter-syncedBefore != 1 {
		t.Fatalf("expected 1 new branch.changes_synced event, got %d", syncedAfter-syncedBefore)
	}

	// When uncommitted changes exist and thread.Changes is already recorded (e.g. from turnChanges),
	// ThreadChanges must not downgrade thread.Changes to commit-only totals.
	if err := repo.SyncBranchChanges(ctx, thread.ID, core.ChangeTotals{Files: 2, Added: 10, Removed: 1}); err != nil {
		t.Fatal(err)
	}
	if err := ws.WriteFile(ctx, "draft.txt", "wip\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ThreadChanges(ctx, thread.ID); err != nil {
		t.Fatal(err)
	}
	preserved, err := repo.GetThread(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	preservedTotals, _ := preserved.Changes.(map[string]any)
	if preservedTotals["files"] != float64(2) || preservedTotals["added"] != float64(10) {
		t.Fatalf("expected uncommitted totals to be preserved, got %#v", preserved.Changes)
	}

	// 2. SyncPullRequest backfills NULL thread.Changes and refreshes when headSha changes.
	if err := os.Remove(filepath.Join(ws.Info().Label, "draft.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Store.DB().ExecContext(ctx, "UPDATE threads SET changes = NULL WHERE id = ?", thread.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SyncPullRequest(ctx, thread.ID, prInfo, nil); err != nil {
		t.Fatal(err)
	}
	synced2, err := repo.GetThread(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	totals2, ok := synced2.Changes.(map[string]any)
	if !ok || totals2["files"] != float64(1) || totals2["added"] != float64(2) || totals2["removed"] != float64(0) {
		t.Fatalf("expected SyncPullRequest to backfill totals, got %#v", synced2.Changes)
	}

	// Subsequent commit + new PR headSha refreshes totals.
	commitInWorkspace(t, ws, "extra.txt", "a\nb\nc\n", "add extra", false)
	prInfo.HeadSHA = "2222222222222222222222222222222222222222"
	if _, err := manager.SyncPullRequest(ctx, thread.ID, prInfo, nil); err != nil {
		t.Fatal(err)
	}
	synced3, err := repo.GetThread(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	totals3, ok := synced3.Changes.(map[string]any)
	if !ok || totals3["files"] != float64(2) || totals3["added"] != float64(5) || totals3["removed"] != float64(0) {
		t.Fatalf("expected SyncPullRequest head change to refresh totals, got %#v", synced3.Changes)
	}
}

func TestSyncBranchChangesSkipsZeroFallbackWhenUncommittedSnapshotFails(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	repo, err := core.Open(ctx, core.Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repo.Close() })
	manager, err := New(Options{Repository: repo, Sources: sources.New()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeManager(t, manager) })
	project, err := repo.CreateProject(ctx, new("Hosted"), core.ProjectStart{
		Source: core.Source{Kind: "github", Settings: map[string]any{"repository": "owner/repo"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "Uncommitted only"})
	if err != nil {
		t.Fatal(err)
	}

	changes := &sdk.BranchChanges{
		Branch:      "ariadne/wip",
		Base:        "main",
		From:        "0000000000000000000000000000000000000001",
		Head:        "0000000000000000000000000000000000000001",
		BaseHead:    "0000000000000000000000000000000000000001",
		Files:       nil,
		Uncommitted: []string{"draft.txt"},
	}

	failingBranches := &sdk.BranchCapability{
		Snapshot: func(context.Context, string) (*string, error) {
			return nil, errors.New("snapshot unavailable")
		},
		Diff: func(context.Context, string, string) ([]sdk.ChangedFile, error) {
			return nil, errors.New("diff unavailable")
		},
	}
	manager.syncBranchChanges(ctx, thread, failingBranches, changes)

	afterFailure, err := repo.GetThread(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterFailure.Changes != nil {
		t.Fatalf("expected nil thread.Changes after snapshot failure with only uncommitted files, got %#v", afterFailure.Changes)
	}
	var syncedCount int
	if err := repo.Store.DB().QueryRowContext(ctx, "SELECT count(*) FROM events WHERE type = 'branch.changes_synced'").Scan(&syncedCount); err != nil {
		t.Fatal(err)
	}
	if syncedCount != 0 {
		t.Fatalf("expected 0 branch.changes_synced events after snapshot failure, got %d", syncedCount)
	}

	// Once snapshot and diff succeed on a later attempt, thread.Changes is populated.
	snap := "0000000000000000000000000000000000000002"
	added := int64(3)
	removed := int64(0)
	workingBranches := &sdk.BranchCapability{
		Snapshot: func(context.Context, string) (*string, error) {
			return &snap, nil
		},
		Diff: func(context.Context, string, string) ([]sdk.ChangedFile, error) {
			return []sdk.ChangedFile{{Path: "draft.txt", Status: "added", Added: &added, Removed: &removed}}, nil
		},
	}
	manager.syncBranchChanges(ctx, afterFailure, workingBranches, changes)

	afterRetry, err := repo.GetThread(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	totals, ok := afterRetry.Changes.(map[string]any)
	if !ok || totals["files"] != float64(1) || totals["added"] != float64(3) || totals["removed"] != float64(0) {
		t.Fatalf("expected uncommitted snapshot retry to populate totals, got %#v", afterRetry.Changes)
	}
}
