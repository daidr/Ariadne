package local

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

func TestRemoveWorktreeDropsExternalEmbeddedSnapshotsOnlyAfterRemoval(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	runGit := func(dir string, args ...string) string {
		t.Helper()
		res := NewGit(dir, "file")(ctx, args, GitOptions{})
		if res.ExitCode != 0 {
			t.Fatalf("git %v in %s failed: %s", args, dir, res.Stderr)
		}
		return strings.TrimSpace(res.Stdout)
	}

	outerRepo, _ := initGit(t)
	// External repository whose worktree will be nested inside the thread worktree.
	externalRepo, _ := initGit(t)

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
	externalCommonDir := filepath.Join(externalRepo, ".git")
	if snapshotRefsDefinitelyEmpty(externalCommonDir, threadID) {
		t.Fatalf("expected snapshot refs under %s in external repo before remove", SnapshotRefs(threadID))
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
	if snapshotRefsDefinitelyEmpty(externalCommonDir, threadID) {
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
	if !snapshotRefsDefinitelyEmpty(externalCommonDir, threadID) {
		t.Fatalf("expected external embedded snapshot refs to be dropped after RemoveWorktree succeeds")
	}
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

func TestEmbeddedSubmoduleOrphanBranchAlignedCleanHasNoChanges(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	remoteBare, orphanC1 := initOrphanSubmoduleRemote(t)
	remoteURL := fileURL(remoteBare)

	outer, _ := initGit(t)
	writeGitExclude(t, outer, "subrepos/\nnested-worktrees/\n")

	factory := newFactory(t, FactoryOptions{Path: outer, DataDir: shortTempDir(t), DisableClone: true})
	const threadID = "thr_orphan_submod"
	ws, err := factory.Open(ctx, OpenRequest{ThreadID: threadID})
	if err != nil {
		t.Fatal(err)
	}
	workDir := ws.Info().Label
	if err := ws.Close(); err != nil {
		t.Fatal(err)
	}

	// Host pkg repo outside workDir + nested linked worktree inside workDir with initialized orphan submodule.
	pkgHost := filepath.Join(t.TempDir(), "pkg-ui-host")
	pkgHostGit := cloneOrphanSubmoduleRepo(t, remoteBare, pkgHost)

	nestedWT := filepath.Join(workDir, "nested-worktrees", "feat-split", "subrepos", "pkg-ui")
	if err := os.MkdirAll(filepath.Dir(nestedWT), 0o755); err != nil {
		t.Fatal(err)
	}
	gitOK(t, pkgHostGit, "worktree", "add", "-qb", "feat/entry-split", nestedWT)
	nestedGit := NewGit(nestedWT, "file")
	subRelPath := "nested-worktrees/feat-split/subrepos/pkg-ui/vendor-bundle"
	subDir := filepath.Join(nestedWT, "vendor-bundle")
	subGit := initOrphanSubmoduleFast(t, nestedWT, "vendor-bundle", remoteURL)

	// Verify resolveEmbeddedRepoPath also identifies the nested submodule and its parent superproject.
	resolvedSub, err := resolveEmbeddedRepoPath(ctx, workDir, subRelPath)
	if err != nil {
		t.Fatalf("resolveEmbeddedRepoPath(%q): %v", subRelPath, err)
	}
	if !resolvedSub.IsSubmodule() || resolvedSub.SubmodulePath != "vendor-bundle" || worktreePathKey(resolvedSub.SuperprojectPath) != worktreePathKey(nestedWT) {
		t.Fatalf("unexpected resolved submodule metadata: %+v", resolvedSub)
	}

	// 1. Clean detached orphan submodule aligned with parent gitlink -> 0 active repos, no fake full-tree diff.
	changes, err := factory.EmbeddedChanges(ctx, threadID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("expected 0 active embedded repos for clean orphan submodule, got %+v", changes)
	}

	// 2. Dirty working tree inside orphan submodule -> only Uncommitted reported, Files stays empty.
	writeGit(t, subDir, "bundle.js", "export const version = 2;\n")
	subChanges, err := EmbeddedRepoChanges(ctx, resolvedSub, nil)
	if err != nil {
		t.Fatal(err)
	}
	if subChanges == nil || len(subChanges.Files) != 0 || len(subChanges.Commits) != 0 || len(subChanges.Uncommitted) != 1 || subChanges.Uncommitted[0] != "bundle.js" {
		t.Fatalf("expected only uncommitted bundle.js without full-tree diff, got %+v", subChanges)
	}

	// 3. New local commit C1 -> C2 inside submodule (parent gitlink still at C1) -> exact C1..C2 diff.
	gitCommitTracked(t, subGit, "bump bundle to v2")
	orphanC2 := readHeadSHA(t, subDir)
	if orphanC2 == "" || orphanC2 == orphanC1 {
		t.Fatalf("expected new submodule commit C2, got %s", orphanC2)
	}
	subChanges, err = EmbeddedRepoChanges(ctx, resolvedSub, nil)
	if err != nil {
		t.Fatal(err)
	}
	if subChanges == nil || subChanges.From != orphanC1 || subChanges.Head != orphanC2 ||
		len(subChanges.Commits) != 1 || subChanges.Commits[0].SHA != orphanC2 ||
		len(subChanges.Files) != 1 || subChanges.Files[0].Path != "bundle.js" {
		t.Fatalf("unexpected submodule C1..C2 diff: %+v", subChanges)
	}

	// 4. Anti-regression: parent commits updated gitlink C2 locally, but submodule C2 is NOT pushed to remote yet.
	// With baseline BaseHead=C1, C1..C2 diff must still be preserved (not swallowed by parent gitlink C2).
	gitCommitTracked(t, nestedGit, "bump vendor-bundle gitlink")

	baseline := &sdk.RepoBaseline{RepoPath: subRelPath, BaseHead: orphanC1, BaseBranch: "HEAD"}
	subChanges, err = EmbeddedRepoChanges(ctx, resolvedSub, baseline)
	if err != nil {
		t.Fatal(err)
	}
	if subChanges == nil || subChanges.From != orphanC1 || subChanges.Head != orphanC2 ||
		len(subChanges.Files) != 1 || subChanges.Files[0].Path != "bundle.js" {
		t.Fatalf("parent gitlink update must not swallow unpushed submodule diff: %+v", subChanges)
	}

	// 5. Advance origin/vendor-orphan to C2 -> baseline upgrades automatically and submodule diff clears.
	writeGitRef(t, resolvedSub.GitDir, "refs/remotes/origin/vendor-orphan", orphanC2)
	subChanges, err = EmbeddedRepoChanges(ctx, resolvedSub, baseline)
	if err != nil {
		t.Fatal(err)
	}
	if subChanges != nil {
		t.Fatalf("expected submodule diff to clear once C2 lands on origin/vendor-orphan, got %+v", subChanges)
	}
}

func TestEmbeddedRepoUnrelatedOrphanAndShallowNoFallbackFullTreeDiff(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	t.Run("orphan branch", func(t *testing.T) {
		t.Parallel()
		workDir := shortTempDir(t)
		orphanDir, mainBaseSHA := initEmbeddedSubRepo(t, workDir, "subrepos/orphan-repo")
		orphanGit := NewGit(orphanDir, "file")
		orphanGitDir := filepath.Join(orphanDir, ".git")
		// Build an orphan root commit with docs.md without checkout --orphan + rm -rf.
		if err := os.Remove(filepath.Join(orphanDir, "hello.txt")); err != nil {
			t.Fatal(err)
		}
		writeGit(t, orphanDir, "docs.md", "v1\n")
		writeGitRef(t, orphanGitDir, "HEAD", "ref: refs/heads/orphan-docs")
		_ = os.Remove(filepath.Join(orphanGitDir, "index"))
		gitCommitOnly(t, orphanGit, "orphan docs init")

		repos, err := DiscoverEmbeddedRepos(ctx, workDir)
		if err != nil || len(repos) != 1 {
			t.Fatalf("DiscoverEmbeddedRepos = %+v, %v", repos, err)
		}
		repo := repos[0]

		// Clean orphan branch with no upstream -> 0 changes (no fallback diff against unrelated origin/main).
		ch, err := EmbeddedRepoChanges(ctx, repo, nil)
		if err != nil {
			t.Fatal(err)
		}
		if ch != nil {
			t.Fatalf("expected nil changes for clean orphan branch, got %+v", ch)
		}

		// Dirty orphan branch -> only Uncommitted reported, Files empty.
		writeGit(t, orphanDir, "docs.md", "v1\ndraft\n")
		ch, err = EmbeddedRepoChanges(ctx, repo, nil)
		if err != nil {
			t.Fatal(err)
		}
		if ch == nil || len(ch.Files) != 0 || len(ch.Uncommitted) != 1 || ch.Uncommitted[0] != "docs.md" {
			t.Fatalf("expected only uncommitted docs.md on orphan branch, got %+v", ch)
		}
		writeGit(t, orphanDir, "docs.md", "v1\n")

		// Configure matching upstream origin/orphan-docs and add 1 commit ahead -> skips unrelated origin/main.
		orphanUpstreamSHA := readHeadSHA(t, orphanDir)
		writeGitRef(t, orphanGitDir, "refs/remotes/origin/orphan-docs", orphanUpstreamSHA)
		appendGitConfig(t, orphanGitDir, "\n[branch \"orphan-docs\"]\n\tremote = origin\n\tmerge = refs/heads/orphan-docs\n")
		writeGit(t, orphanDir, "docs.md", "v1\nv2\n")
		gitCommitTracked(t, orphanGit, "update orphan docs")
		orphanHeadSHA := readHeadSHA(t, orphanDir)

		ch, err = EmbeddedRepoChanges(ctx, repo, nil)
		if err != nil {
			t.Fatal(err)
		}
		if ch == nil || ch.Base != "origin/orphan-docs" || ch.From != orphanUpstreamSHA || ch.Head != orphanHeadSHA ||
			len(ch.Commits) != 1 || len(ch.Files) != 1 || ch.Files[0].Path != "docs.md" {
			t.Fatalf("expected 1-commit diff relative to origin/orphan-docs, got %+v", ch)
		}

		// Stale baseline from unrelated main history must not trigger full-tree diff; falls back to connected origin/orphan-docs.
		ch, err = EmbeddedRepoChanges(ctx, repo, &sdk.RepoBaseline{RepoPath: repo.RelPath, BaseHead: mainBaseSHA, BaseBranch: "main"})
		if err != nil {
			t.Fatal(err)
		}
		if ch == nil || ch.From != orphanUpstreamSHA || len(ch.Files) != 1 || ch.Files[0].Path != "docs.md" {
			t.Fatalf("stale unrelated baseline must not diff unrelated trees, got %+v", ch)
		}
	})

	t.Run("shallow clone", func(t *testing.T) {
		t.Parallel()
		workDir := shortTempDir(t)
		shallowDir, shallowHead := initEmbeddedSubRepo(t, workDir, "subrepos/shallow-repo")
		shallowGit := NewGit(shallowDir, "file")
		shallowGitDir := filepath.Join(shallowDir, ".git")

		// Mark HEAD as a shallow boundary and point origin/main at an unrelated root commit
		// (matching a --depth=1 fetch where origin/main and local HEAD share no merge-base).
		writeGitRef(t, shallowGitDir, "shallow", shallowHead)
		unrelatedRemote := gitOK(t, shallowGit, "commit-tree", shallowHead+"^{tree}", "-m", "remote shallow tip")
		writeGitRef(t, shallowGitDir, "refs/remotes/origin/main", unrelatedRemote)

		if mb := mergeBaseSHA(ctx, shallowGit, "refs/remotes/origin/main", shallowHead); mb != "" {
			t.Fatalf("expected disconnected shallow history with no merge-base, got %s", mb)
		}

		repos, err := DiscoverEmbeddedRepos(ctx, workDir)
		if err != nil || len(repos) != 1 {
			t.Fatalf("DiscoverEmbeddedRepos = %+v, %v", repos, err)
		}
		repo := repos[0]

		// Clean shallow clone with disconnected origin/main -> nil changes (no reverse full-tree diff).
		ch, err := EmbeddedRepoChanges(ctx, repo, nil)
		if err != nil {
			t.Fatal(err)
		}
		if ch != nil {
			t.Fatalf("expected nil changes for clean disconnected shallow clone, got %+v", ch)
		}

		// Dirty shallow clone -> only Uncommitted reported, Files empty.
		writeGit(t, shallowDir, "hello.txt", "local edit\n")
		ch, err = EmbeddedRepoChanges(ctx, repo, nil)
		if err != nil {
			t.Fatal(err)
		}
		if ch == nil || len(ch.Files) != 0 || len(ch.Uncommitted) != 1 || ch.Uncommitted[0] != "hello.txt" {
			t.Fatalf("expected only uncommitted hello.txt for dirty disconnected shallow clone, got %+v", ch)
		}
	})
}
