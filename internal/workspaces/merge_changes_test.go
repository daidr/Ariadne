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
	remoteDir := t.TempDir()
	git := local.NewGit(remoteDir, "file")
	run := func(args ...string) {
		t.Helper()
		res := git(ctx, args, local.GitOptions{})
		if res.ExitCode != 0 {
			t.Fatalf("git %v failed: %s", args, res.Stderr)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.name", "Test")
	run("config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(remoteDir, "README.md"), []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-q", "-m", "initial")

	dataDir := t.TempDir()
	repo, err := core.Open(ctx, core.Options{Path: filepath.Join(t.TempDir(), "repo.sqlite")})
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
		Connect: func(context.Context, map[string]any) (sdk.Capabilities, error) {
			hosted, err := local.NewHosted(local.HostedOptions{
				DataDir:   dataDir,
				Path:      "owner/repo",
				Namespace: []string{"github"},
				RemoteURL: "file://" + filepath.ToSlash(remoteDir),
				DefaultBranch: func(context.Context) (string, error) {
					return "main", nil
				},
			})
			if err != nil {
				return sdk.Capabilities{}, err
			}
			return sdk.Capabilities{
				Workspace: hosted.Capability(),
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
				Close: hosted.Close,
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

func commitInWorkspace(t *testing.T, ws sdk.Workspace, file, content, message string) {
	t.Helper()
	ctx := t.Context()
	if err := ws.WriteFile(ctx, file, content); err != nil {
		t.Fatal(err)
	}
	for _, argv := range [][]string{
		{"git", "config", "user.name", "Test"},
		{"git", "config", "user.email", "test@example.invalid"},
		{"git", "add", file},
		{"git", "commit", "-q", "-m", message},
	} {
		res, err := ws.ExecFile(ctx, argv, sdk.ExecOptions{})
		if err != nil || res.ExitCode != 0 {
			t.Fatalf("exec %v: err=%v exit=%d stderr=%s", argv, err, res.ExitCode, res.Stderr)
		}
	}
}

func TestThreadChangesAndSyncPullRequestBackfillTotals(t *testing.T) {
	ctx := t.Context()
	repo, manager, project, prInfo := initHostedChangesRepo(t)

	// 1. ThreadChanges backfills NULL thread.Changes and is idempotent on repeat reads.
	thread, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "Backfill via ThreadChanges"})
	if err != nil {
		t.Fatal(err)
	}
	ws, err := manager.ForThread(ctx, project.ID, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	commitInWorkspace(t, ws, "README.md", "hello\nsecond\nthird\n", "add lines")

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
	var syncedCount int
	if err := repo.Store.DB().QueryRowContext(ctx, "SELECT count(*) FROM events WHERE type = 'branch.changes_synced'").Scan(&syncedCount); err != nil {
		t.Fatal(err)
	}
	if syncedCount != 1 {
		t.Fatalf("expected 1 branch.changes_synced event, got %d", syncedCount)
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
	thread2, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "Backfill via SyncPullRequest"})
	if err != nil {
		t.Fatal(err)
	}
	ws2, err := manager.ForThread(ctx, project.ID, thread2.ID)
	if err != nil {
		t.Fatal(err)
	}
	commitInWorkspace(t, ws2, "README.md", "updated\n", "replace line")

	if _, err := manager.SyncPullRequest(ctx, thread2.ID, prInfo, nil); err != nil {
		t.Fatal(err)
	}
	synced2, err := repo.GetThread(ctx, thread2.ID)
	if err != nil {
		t.Fatal(err)
	}
	totals2, ok := synced2.Changes.(map[string]any)
	if !ok || totals2["files"] != float64(1) || totals2["added"] != float64(1) || totals2["removed"] != float64(1) {
		t.Fatalf("expected SyncPullRequest to backfill totals, got %#v", synced2.Changes)
	}

	// Subsequent commit + new PR headSha refreshes totals.
	commitInWorkspace(t, ws2, "extra.txt", "a\nb\nc\n", "add extra")
	prInfo.HeadSHA = "2222222222222222222222222222222222222222"
	if _, err := manager.SyncPullRequest(ctx, thread2.ID, prInfo, nil); err != nil {
		t.Fatal(err)
	}
	synced3, err := repo.GetThread(ctx, thread2.ID)
	if err != nil {
		t.Fatal(err)
	}
	totals3, ok := synced3.Changes.(map[string]any)
	if !ok || totals3["files"] != float64(2) || totals3["added"] != float64(4) || totals3["removed"] != float64(1) {
		t.Fatalf("expected SyncPullRequest head change to refresh totals, got %#v", synced3.Changes)
	}
}

func TestSyncBranchChangesSkipsZeroFallbackWhenUncommittedSnapshotFails(t *testing.T) {
	ctx := t.Context()
	repo, manager, project, _ := initHostedChangesRepo(t)
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
