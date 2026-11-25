package local

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/daidr/Ariadne/sdk"
)

func initEmbeddedSubRepo(t *testing.T, parentDir, relPath string) (repoDir, initialSHA string) {
	t.Helper()
	_, tplWork, sha := ensureEmbeddedSubRepoTemplate(t)
	repoDir = filepath.Join(parentDir, filepath.FromSlash(relPath))
	copyTree(t, tplWork, repoDir)
	return repoDir, sha
}

func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ad-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestEmbeddedChangesBaselinesFallbackSnapshotsAndFileVersions(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	outerDir, _ := initGit(t)

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
	activeDir, baseSHA := initEmbeddedSubRepo(t, workDir, "sub/active")
	_, _ = initEmbeddedSubRepo(t, workDir, "sub/clean")

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
	if res := activeGit(ctx, []string{"commit", "-qam", "update hello"}, GitOptions{}); res.ExitCode != 0 {
		t.Fatalf("git commit failed: %s", res.Stderr)
	}
	commit2SHA := readHeadSHA(t, activeDir)
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

func TestEmbeddedRepoUnrelatedOrphanAndShallowNoFallbackFullTreeDiff(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	workDir := shortTempDir(t)

	// 1. Orphan branch repo (no merge-base with origin/main).
	orphanDir, mainBaseSHA := initEmbeddedSubRepo(t, workDir, "subrepos/orphan-repo")
	orphanGit := NewGit(orphanDir, "file")
	orphanGitDir := filepath.Join(orphanDir, ".git")
	if err := os.Remove(filepath.Join(orphanDir, "hello.txt")); err != nil {
		t.Fatal(err)
	}
	writeGit(t, orphanDir, "docs.md", "v1\n")
	writeGitRef(t, orphanGitDir, "HEAD", "ref: refs/heads/orphan-docs")
	_ = os.Remove(filepath.Join(orphanGitDir, "index"))
	gitCommitOnly(t, orphanGit, "orphan docs init")

	// 2. Shallow clone repo with disconnected origin/main.
	shallowDir, shallowHead := initEmbeddedSubRepo(t, workDir, "subrepos/shallow-repo")
	shallowGit := NewGit(shallowDir, "file")
	shallowGitDir := filepath.Join(shallowDir, ".git")
	writeGitRef(t, shallowGitDir, "shallow", shallowHead)
	unrelatedRemote := gitOK(t, shallowGit, "commit-tree", shallowHead+"^{tree}", "-m", "remote shallow tip")
	writeGitRef(t, shallowGitDir, "refs/remotes/origin/main", unrelatedRemote)

	repos, err := DiscoverEmbeddedRepos(ctx, workDir)
	if err != nil || len(repos) != 2 {
		t.Fatalf("DiscoverEmbeddedRepos = %+v, %v", repos, err)
	}
	byRel := map[string]EmbeddedRepo{}
	for _, r := range repos {
		byRel[r.RelPath] = r
	}
	orphanRepo := byRel["subrepos/orphan-repo"]
	shallowRepo := byRel["subrepos/shallow-repo"]

	// Orphan: clean -> nil; dirty -> uncommitted only; with origin/orphan-docs + commit -> 1-file diff even with stale unrelated baseline.
	if ch, err := EmbeddedRepoChanges(ctx, orphanRepo, nil); err != nil || ch != nil {
		t.Fatalf("expected nil changes for clean orphan branch, got %+v, %v", ch, err)
	}
	writeGit(t, orphanDir, "docs.md", "v1\ndraft\n")
	if ch, err := EmbeddedRepoChanges(ctx, orphanRepo, nil); err != nil || ch == nil || len(ch.Files) != 0 || len(ch.Uncommitted) != 1 || ch.Uncommitted[0] != "docs.md" {
		t.Fatalf("expected only uncommitted docs.md on orphan branch, got %+v, %v", ch, err)
	}
	orphanUpstreamSHA := readHeadSHA(t, orphanDir)
	writeGitRef(t, orphanGitDir, "refs/remotes/origin/orphan-docs", orphanUpstreamSHA)
	appendGitConfig(t, orphanGitDir, "\n[branch \"orphan-docs\"]\n\tremote = origin\n\tmerge = refs/heads/orphan-docs\n")
	gitCommitTracked(t, orphanGit, "update orphan docs")
	orphanHeadSHA := readHeadSHA(t, orphanDir)

	if ch, err := EmbeddedRepoChanges(ctx, orphanRepo, nil); err != nil || ch == nil || ch.Base != "origin/orphan-docs" || ch.From != orphanUpstreamSHA || ch.Head != orphanHeadSHA ||
		len(ch.Commits) != 1 || len(ch.Files) != 1 || ch.Files[0].Path != "docs.md" {
		t.Fatalf("expected 1-commit diff relative to origin/orphan-docs, got %+v, %v", ch, err)
	}
	if ch, err := EmbeddedRepoChanges(ctx, orphanRepo, &sdk.RepoBaseline{RepoPath: orphanRepo.RelPath, BaseHead: mainBaseSHA, BaseBranch: "main"}); err != nil || ch == nil || ch.From != orphanUpstreamSHA || len(ch.Files) != 1 || ch.Files[0].Path != "docs.md" {
		t.Fatalf("stale unrelated baseline must not diff unrelated trees, got %+v, %v", ch, err)
	}

	// Shallow: clean disconnected -> nil; dirty -> uncommitted only.
	if ch, err := EmbeddedRepoChanges(ctx, shallowRepo, nil); err != nil || ch != nil {
		t.Fatalf("expected nil changes for clean disconnected shallow clone, got %+v, %v", ch, err)
	}
	writeGit(t, shallowDir, "hello.txt", "local edit\n")
	if ch, err := EmbeddedRepoChanges(ctx, shallowRepo, nil); err != nil || ch == nil || len(ch.Files) != 0 || len(ch.Uncommitted) != 1 || ch.Uncommitted[0] != "hello.txt" {
		t.Fatalf("expected only uncommitted hello.txt for dirty disconnected shallow clone, got %+v, %v", ch, err)
	}
}
