package local

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/sdk"
)

func initEmbeddedSubRepo(t *testing.T, parentDir, relPath string) (repoDir, remoteDir, initialSHA string) {
	t.Helper()
	remoteDir = filepath.Join(t.TempDir(), "remote.git")
	runGit := func(dir string, args ...string) string {
		t.Helper()
		res := NewGit(dir, "file")(t.Context(), args, GitOptions{})
		if res.ExitCode != 0 {
			t.Fatalf("git %v in %s failed: %s", args, dir, res.Stderr)
		}
		return res.Stdout
	}
	if err := os.MkdirAll(remoteDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(remoteDir, "init", "--bare", "-q", "-b", "main")

	repoDir = filepath.Join(parentDir, filepath.FromSlash(relPath))
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(repoDir, "init", "-q", "-b", "main")
	runGit(repoDir, "config", "user.name", "Test")
	runGit(repoDir, "config", "user.email", "test@example.invalid")
	runGit(repoDir, "remote", "add", "origin", fileURL(remoteDir))
	if err := os.WriteFile(filepath.Join(repoDir, "hello.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(repoDir, "add", "hello.txt")
	runGit(repoDir, "commit", "-q", "-m", "initial")
	runGit(repoDir, "push", "-q", "-u", "origin", "main")
	runGit(repoDir, "remote", "set-head", "origin", "main")
	initialSHA = commitSHA(t.Context(), NewGit(repoDir, "file"), "HEAD", "")
	if initialSHA == "" {
		t.Fatal("failed to resolve HEAD sha")
	}
	return repoDir, remoteDir, initialSHA
}

func TestEmbeddedChangesBaselinesFallbackSnapshotsAndFileVersions(t *testing.T) {
	ctx := t.Context()
	outerDir := t.TempDir()
	outerGit := NewGit(outerDir, "file")
	runOuter := func(args ...string) {
		t.Helper()
		res := outerGit(ctx, args, GitOptions{})
		if res.ExitCode != 0 {
			t.Fatalf("outer git %v failed: %s", args, res.Stderr)
		}
	}
	runOuter("init", "-q", "-b", "main")
	runOuter("config", "user.name", "Test")
	runOuter("config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(outerDir, "README.md"), []byte("outer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runOuter("add", "README.md")
	runOuter("commit", "-q", "-m", "outer initial")

	dataDir := t.TempDir()
	factory := newFactory(t, FactoryOptions{Path: outerDir, DataDir: dataDir, DisableClone: true})

	ws, err := factory.Open(ctx, OpenRequest{ThreadID: "thr_emb"})
	if err != nil {
		t.Fatal(err)
	}
	workDir := ws.root.Name()

	// Create two embedded repos inside the thread worktree:
	// 1) sub/active: will receive commits and uncommitted edits
	// 2) sub/clean: remains untouched and must not appear in EmbeddedChanges
	activeDir, _, baseSHA := initEmbeddedSubRepo(t, workDir, "sub/active")
	_, _, _ = initEmbeddedSubRepo(t, workDir, "sub/clean")

	// Before any changes, EmbeddedChanges returns empty.
	initial, err := factory.EmbeddedChanges(ctx, "thr_emb", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(initial) != 0 {
		t.Fatalf("expected 0 active embedded repos initially, got %+v", initial)
	}

	// Capture before RepoSnapshots.
	beforeSnaps, err := factory.RepoSnapshots(ctx, "thr_emb")
	if err != nil {
		t.Fatal(err)
	}
	var beforeActive sdk.RepoSnapshot
	for _, s := range beforeSnaps {
		if s.RepoPath == "sub/active" {
			beforeActive = s
		}
	}
	if beforeActive.Tree == "" || beforeActive.Head != baseSHA {
		t.Fatalf("expected before snapshot for sub/active at %s, got %+v", baseSHA, beforeActive)
	}

	// Make a commit inside sub/active, then an uncommitted edit.
	activeGit := NewGit(activeDir, "file")
	if err := os.WriteFile(filepath.Join(activeDir, "hello.txt"), []byte("v1\nv2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if res := activeGit(ctx, []string{"add", "hello.txt"}, GitOptions{}); res.ExitCode != 0 {
		t.Fatalf("git add failed: %s", res.Stderr)
	}
	if res := activeGit(ctx, []string{"commit", "-q", "-m", "update hello"}, GitOptions{}); res.ExitCode != 0 {
		t.Fatalf("git commit failed: %s", res.Stderr)
	}
	commit2SHA := commitSHA(ctx, activeGit, "HEAD", "")
	if commit2SHA == "" {
		t.Fatal("failed to resolve commit2 HEAD sha")
	}
	if err := os.WriteFile(filepath.Join(activeDir, "draft.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 1. EmbeddedChanges with recorded baseline uses the baseline HEAD.
	withBaseline, err := factory.EmbeddedChanges(ctx, "thr_emb", map[string]sdk.RepoBaseline{
		"sub/active": {RepoPath: "sub/active", BaseHead: baseSHA, BaseBranch: "main"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(withBaseline) != 1 || withBaseline[0].RepoPath != "sub/active" || !withBaseline[0].Embedded || withBaseline[0].Mergeable {
		t.Fatalf("unexpected EmbeddedChanges with baseline: %+v", withBaseline)
	}
	c := withBaseline[0].Changes
	if c.From != baseSHA || c.Head != commit2SHA || len(c.Commits) != 1 || len(c.Files) != 1 || c.Files[0].Path != "hello.txt" || len(c.Uncommitted) != 1 {
		t.Fatalf("unexpected embedded branch changes: %+v", c)
	}

	// 2. EmbeddedChanges WITHOUT recorded baseline falls back to merge-base against origin/HEAD.
	fallback, err := factory.EmbeddedChanges(ctx, "thr_emb", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(fallback) != 1 || fallback[0].Changes.From != baseSHA || len(fallback[0].Changes.Files) != 1 {
		t.Fatalf("expected fallback merge-base to resolve %s, got %+v", baseSHA, fallback)
	}

	// 3. RepoSnapshots + RepoDiff captures both committed and uncommitted changes.
	afterSnaps, err := factory.RepoSnapshots(ctx, "thr_emb")
	if err != nil {
		t.Fatal(err)
	}
	var afterActive sdk.RepoSnapshot
	for _, s := range afterSnaps {
		if s.RepoPath == "sub/active" {
			afterActive = s
		}
	}
	if afterActive.Tree == "" || afterActive.Tree == beforeActive.Tree {
		t.Fatalf("expected changed after snapshot tree, got %+v", afterActive)
	}
	diffFiles, err := factory.RepoDiff(ctx, "thr_emb", "sub/active", beforeActive.Tree, afterActive.Tree)
	if err != nil {
		t.Fatal(err)
	}
	if len(diffFiles) != 2 {
		t.Fatalf("expected 2 changed files (hello.txt + draft.txt), got %+v", diffFiles)
	}

	// 4. FileVersions with RepoPath reads from the embedded repo.
	versions, err := factory.FileVersions(ctx, []sdk.FileVersionRequest{{
		ThreadID: "thr_emb",
		RepoPath: "sub/active",
		From:     baseSHA,
		Head:     commit2SHA,
		Path:     "hello.txt",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 1 || versions[0].Before == nil || *versions[0].Before != "v1\n" || versions[0].After == nil || *versions[0].After != "v1\nv2\n" {
		t.Fatalf("unexpected embedded file versions: %+v", versions)
	}

	// 5. Reject path traversal in RepoPath.
	if _, err := factory.FileVersions(ctx, []sdk.FileVersionRequest{{
		ThreadID: "thr_emb",
		RepoPath: "../outside",
		From:     baseSHA,
		Head:     commit2SHA,
		Path:     "hello.txt",
	}}); err == nil {
		t.Fatal("expected error for escaping RepoPath")
	}
}

func TestRemoveWorktreeDropsExternalEmbeddedSnapshotsOnlyAfterRemoval(t *testing.T) {
	ctx := t.Context()
	runGit := func(dir string, args ...string) string {
		t.Helper()
		res := NewGit(dir, "file")(ctx, args, GitOptions{})
		if res.ExitCode != 0 {
			t.Fatalf("git %v in %s failed: %s", args, dir, res.Stderr)
		}
		return strings.TrimSpace(res.Stdout)
	}

	outerRepo := t.TempDir()
	runGit(outerRepo, "init", "-q", "-b", "main")
	runGit(outerRepo, "config", "user.name", "Test")
	runGit(outerRepo, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(outerRepo, "root.txt"), []byte("root\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(outerRepo, "add", "root.txt")
	runGit(outerRepo, "commit", "-q", "-m", "init outer")

	// External repository whose worktree will be nested inside the thread worktree.
	externalRepo := t.TempDir()
	runGit(externalRepo, "init", "-q", "-b", "main")
	runGit(externalRepo, "config", "user.name", "Test")
	runGit(externalRepo, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(externalRepo, "ext.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(externalRepo, "add", "ext.txt")
	runGit(externalRepo, "commit", "-q", "-m", "init external")

	factory := newFactory(t, FactoryOptions{Path: outerRepo, DataDir: t.TempDir(), DisableClone: true})

	const threadID = "thr_ext_snap"
	ws, err := factory.Open(ctx, OpenRequest{ThreadID: threadID})
	if err != nil {
		t.Fatal(err)
	}
	wtDir := ws.root.Name()
	if err := ws.Close(); err != nil {
		t.Fatal(err)
	}

	nestedWT := filepath.Join(wtDir, "packages", "ext-wt")
	if err := os.MkdirAll(filepath.Dir(nestedWT), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(externalRepo, "worktree", "add", "-q", "-b", "feat/ext", nestedWT)
	if err := os.WriteFile(filepath.Join(nestedWT, "ext.txt"), []byte("v1\nv2\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Capture snapshot so externalRepo gets a snapshot ref for threadID.
	if _, err := factory.RepoSnapshots(ctx, threadID); err != nil {
		t.Fatal(err)
	}
	snapshotPrefix := SnapshotRefs(threadID)
	refsBefore := runGit(externalRepo, "for-each-ref", "--format=%(refname)", snapshotPrefix)
	if refsBefore == "" {
		t.Fatalf("expected snapshot refs under %s in external repo before remove", snapshotPrefix)
	}

	// Simulate git worktree remove failing (e.g. file handle held open).
	origGit := factory.git
	failRemove := true
	factory.git = func(ctx context.Context, args []string, options GitOptions) sdk.ExecResult {
		if failRemove && len(args) >= 2 && args[0] == "worktree" && args[1] == "remove" {
			return sdk.ExecResult{ExitCode: 1, Stderr: "simulated worktree remove failure"}
		}
		return origGit(ctx, args, options)
	}
	if removed, err := factory.Remove(ctx, threadID, RemoveOptions{Discard: true}); err == nil || removed {
		t.Fatalf("expected Remove to fail when git worktree remove fails, removed=%v err=%v", removed, err)
	}
	refsAfterFailedRemove := runGit(externalRepo, "for-each-ref", "--format=%(refname)", snapshotPrefix)
	if refsAfterFailedRemove == "" {
		t.Fatal("expected external embedded snapshot refs to remain intact when RemoveWorktree fails")
	}

	// Re-add nested worktree if force-discard removed it before outer removal failed,
	// then allow removal to succeed and verify external snapshot refs are dropped.
	if _, err := os.Stat(nestedWT); os.IsNotExist(err) {
		runGit(externalRepo, "worktree", "add", "-q", nestedWT, "feat/ext")
	}
	failRemove = false
	removed, err := factory.Remove(ctx, threadID, RemoveOptions{Discard: true})
	if err != nil || !removed {
		t.Fatalf("expected Remove to succeed after unlock, removed=%v err=%v", removed, err)
	}
	refsAfterSuccess := runGit(externalRepo, "for-each-ref", "--format=%(refname)", snapshotPrefix)
	if refsAfterSuccess != "" {
		t.Fatalf("expected external embedded snapshot refs to be dropped after RemoveWorktree succeeds, got %q", refsAfterSuccess)
	}
}
