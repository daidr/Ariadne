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

	outer, outerGit := initGit(t)
	writeGit(t, outer, ".gitignore", "subrepos/\nnested-worktrees/\n")
	gitCommit(t, outerGit, "ignore subrepos and nested worktrees")

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

	// Clone pkg repo inside workDir/subrepos/pkg-ui and create nested linked worktree with initialized orphan submodule.
	pkgClone := filepath.Join(workDir, "subrepos", "pkg-ui")
	if err := os.MkdirAll(filepath.Dir(pkgClone), 0o755); err != nil {
		t.Fatal(err)
	}
	gitOK(t, NewGit(filepath.Dir(pkgClone), "file"), "clone", "-q", remoteURL, pkgClone)
	pkgCloneGit := NewGit(pkgClone, "file")
	gitOK(t, pkgCloneGit, "config", "user.name", "Ariadne Test")
	gitOK(t, pkgCloneGit, "config", "user.email", "test@example.invalid")

	nestedWT := filepath.Join(workDir, "nested-worktrees", "feat-split", "subrepos", "pkg-ui")
	if err := os.MkdirAll(filepath.Dir(nestedWT), 0o755); err != nil {
		t.Fatal(err)
	}
	gitOK(t, pkgCloneGit, "worktree", "add", "-qb", "feat/entry-split", nestedWT)
	nestedGit := NewGit(nestedWT, "file")
	gitOK(t, nestedGit, "-c", "protocol.file.allow=always", "submodule", "update", "--init", "-q", "vendor-bundle")

	subRelPath := "nested-worktrees/feat-split/subrepos/pkg-ui/vendor-bundle"
	subDir := filepath.Join(nestedWT, "vendor-bundle")
	subGit := NewGit(subDir, "file")
	gitOK(t, subGit, "config", "user.name", "Ariadne Test")
	gitOK(t, subGit, "config", "user.email", "test@example.invalid")

	// Sanity check: submodule origin/HEAD points to origin/main which has NO common ancestor with HEAD (vendor-orphan).
	if mb := mergeBaseSHA(ctx, subGit, "refs/remotes/origin/main", orphanC1); mb != "" {
		t.Fatalf("expected no merge-base between origin/main and orphan commit, got %s", mb)
	}

	// Verify resolveEmbeddedRepoPath also identifies the nested submodule and its parent superproject.
	resolvedSub, err := resolveEmbeddedRepoPath(ctx, workDir, subRelPath)
	if err != nil {
		t.Fatalf("resolveEmbeddedRepoPath(%q): %v", subRelPath, err)
	}
	if !resolvedSub.IsSubmodule() || resolvedSub.SubmodulePath != "vendor-bundle" || worktreePathKey(resolvedSub.SuperprojectPath) != worktreePathKey(nestedWT) {
		t.Fatalf("unexpected resolved submodule metadata: %+v", resolvedSub)
	}

	findRepo := func(items []sdk.RepoChanges, rel string) *sdk.RepoChanges {
		for i := range items {
			if items[i].RepoPath == rel {
				return &items[i]
			}
		}
		return nil
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
	writeGit(t, subDir, "bundle.js", "export const version = 1.5;\n")
	changes, err = factory.EmbeddedChanges(ctx, threadID, nil)
	if err != nil {
		t.Fatal(err)
	}
	subChanges := findRepo(changes, subRelPath)
	if subChanges == nil || subChanges.Changes == nil {
		t.Fatalf("expected uncommitted changes for %s, got %+v", subRelPath, changes)
	}
	if len(subChanges.Changes.Files) != 0 || len(subChanges.Changes.Commits) != 0 || len(subChanges.Changes.Uncommitted) != 1 || subChanges.Changes.Uncommitted[0] != "bundle.js" {
		t.Fatalf("expected only uncommitted bundle.js without full-tree diff, got %+v", subChanges.Changes)
	}
	gitOK(t, subGit, "checkout", "--", "bundle.js")

	// 3. New local commit C1 -> C2 inside submodule (parent gitlink still at C1) -> exact C1..C2 diff.
	writeGit(t, subDir, "bundle.js", "export const version = 2;\n")
	gitCommit(t, subGit, "bump bundle to v2")
	orphanC2 := commitSHA(ctx, subGit, "HEAD", "")
	if orphanC2 == "" || orphanC2 == orphanC1 {
		t.Fatalf("expected new submodule commit C2, got %s", orphanC2)
	}
	changes, err = factory.EmbeddedChanges(ctx, threadID, nil)
	if err != nil {
		t.Fatal(err)
	}
	subChanges = findRepo(changes, subRelPath)
	if subChanges == nil || subChanges.Changes == nil {
		t.Fatalf("expected C1..C2 changes for %s, got %+v", subRelPath, changes)
	}
	if subChanges.Changes.From != orphanC1 || subChanges.Changes.Head != orphanC2 ||
		len(subChanges.Changes.Commits) != 1 || subChanges.Changes.Commits[0].SHA != orphanC2 ||
		len(subChanges.Changes.Files) != 1 || subChanges.Changes.Files[0].Path != "bundle.js" {
		t.Fatalf("unexpected submodule C1..C2 diff: %+v", subChanges.Changes)
	}

	// 4. Anti-regression: parent commits updated gitlink C2 locally, but submodule C2 is NOT pushed to remote yet.
	// With baseline BaseHead=C1, C1..C2 diff must still be preserved (not swallowed by parent gitlink C2).
	gitOK(t, nestedGit, "add", "vendor-bundle")
	gitCommit(t, nestedGit, "bump vendor-bundle gitlink")

	baselines := map[string]sdk.RepoBaseline{
		subRelPath: {RepoPath: subRelPath, BaseHead: orphanC1, BaseBranch: "HEAD"},
	}
	changes, err = factory.EmbeddedChanges(ctx, threadID, baselines)
	if err != nil {
		t.Fatal(err)
	}
	subChanges = findRepo(changes, subRelPath)
	if subChanges == nil || subChanges.Changes == nil {
		t.Fatalf("expected C1..C2 diff to remain visible after local parent gitlink commit, got %+v", changes)
	}
	if subChanges.Changes.From != orphanC1 || subChanges.Changes.Head != orphanC2 ||
		len(subChanges.Changes.Files) != 1 || subChanges.Changes.Files[0].Path != "bundle.js" {
		t.Fatalf("parent gitlink update must not swallow unpushed submodule diff: %+v", subChanges.Changes)
	}

	// 5. Push C2 to origin/vendor-orphan and fetch -> baseline upgrades automatically and submodule diff clears.
	gitOK(t, subGit, "push", "-q", "origin", "HEAD:refs/heads/vendor-orphan")
	gitOK(t, subGit, "fetch", "-q", "origin")
	changes, err = factory.EmbeddedChanges(ctx, threadID, baselines)
	if err != nil {
		t.Fatal(err)
	}
	if subChanges := findRepo(changes, subRelPath); subChanges != nil {
		t.Fatalf("expected submodule diff to clear once C2 lands on origin/vendor-orphan, got %+v", subChanges.Changes)
	}
}

func TestEmbeddedRepoUnrelatedOrphanAndShallowNoFallbackFullTreeDiff(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	outer, outerGit := initGit(t)
	writeGit(t, outer, ".gitignore", "subrepos/\n")
	gitCommit(t, outerGit, "ignore subrepos")

	factory := newFactory(t, FactoryOptions{Path: outer, DataDir: shortTempDir(t), DisableClone: true})
	const threadID = "thr_orphan_shallow"
	ws, err := factory.Open(ctx, OpenRequest{ThreadID: threadID})
	if err != nil {
		t.Fatal(err)
	}
	workDir := ws.Info().Label
	if err := ws.Close(); err != nil {
		t.Fatal(err)
	}

	// Case 1: Standalone embedded repo on an orphan branch while origin/HEAD -> origin/main exists.
	orphanDir, _, mainBaseSHA := initEmbeddedSubRepo(t, workDir, "subrepos/orphan-repo")
	orphanGit := NewGit(orphanDir, "file")
	gitOK(t, orphanGit, "checkout", "-q", "--orphan", "orphan-docs")
	gitOK(t, orphanGit, "rm", "-rfq", ".")
	writeGit(t, orphanDir, "docs.md", "v1\n")
	gitCommit(t, orphanGit, "orphan docs init")

	// Clean orphan branch with no upstream -> 0 changes (no fallback diff against unrelated origin/main).
	changes, err := factory.EmbeddedChanges(ctx, threadID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("expected 0 changes for clean orphan branch, got %+v", changes)
	}

	// Dirty orphan branch -> only Uncommitted reported, Files empty.
	writeGit(t, orphanDir, "docs.md", "v1\ndraft\n")
	changes, err = factory.EmbeddedChanges(ctx, threadID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].RepoPath != "subrepos/orphan-repo" ||
		len(changes[0].Changes.Files) != 0 || len(changes[0].Changes.Uncommitted) != 1 || changes[0].Changes.Uncommitted[0] != "docs.md" {
		t.Fatalf("expected only uncommitted docs.md on orphan branch, got %+v", changes)
	}
	gitOK(t, orphanGit, "checkout", "--", "docs.md")

	// Configure matching upstream origin/orphan-docs and add 1 commit ahead -> skips unrelated origin/main and diffs against origin/orphan-docs.
	gitOK(t, orphanGit, "push", "-q", "-u", "origin", "orphan-docs")
	orphanUpstreamSHA := commitSHA(ctx, orphanGit, "HEAD", "")
	writeGit(t, orphanDir, "docs.md", "v1\nv2\n")
	gitCommit(t, orphanGit, "update orphan docs")
	orphanHeadSHA := commitSHA(ctx, orphanGit, "HEAD", "")

	changes, err = factory.EmbeddedChanges(ctx, threadID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].RepoPath != "subrepos/orphan-repo" ||
		changes[0].Changes.Base != "origin/orphan-docs" ||
		changes[0].Changes.From != orphanUpstreamSHA ||
		changes[0].Changes.Head != orphanHeadSHA ||
		len(changes[0].Changes.Commits) != 1 ||
		len(changes[0].Changes.Files) != 1 || changes[0].Changes.Files[0].Path != "docs.md" {
		t.Fatalf("expected 1-commit diff relative to origin/orphan-docs, got %+v", changes)
	}

	// Stale baseline from unrelated main history must not trigger full-tree diff; falls back to connected origin/orphan-docs.
	changes, err = factory.EmbeddedChanges(ctx, threadID, map[string]sdk.RepoBaseline{
		"subrepos/orphan-repo": {RepoPath: "subrepos/orphan-repo", BaseHead: mainBaseSHA, BaseBranch: "main"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Changes.From != orphanUpstreamSHA || len(changes[0].Changes.Files) != 1 || changes[0].Changes.Files[0].Path != "docs.md" {
		t.Fatalf("stale unrelated baseline must not diff unrelated trees, got %+v", changes)
	}

	// Reset orphan-repo clean at upstream so Case 2 assertions are isolated.
	gitOK(t, orphanGit, "reset", "--hard", "-q", orphanUpstreamSHA)

	// Case 2: Shallow clone (--depth=1) whose origin/main advances and breaks merge-base.
	seedDir, remoteBare, _ := initEmbeddedSubRepo(t, t.TempDir(), "seed")
	seedGit := NewGit(seedDir, "file")
	shallowDir := filepath.Join(workDir, "subrepos", "shallow-repo")
	gitOK(t, NewGit(filepath.Dir(shallowDir), "file"), "clone", "-q", "--depth=1", fileURL(remoteBare), shallowDir)
	shallowGit := NewGit(shallowDir, "file")
	gitOK(t, shallowGit, "config", "user.name", "Ariadne Test")
	gitOK(t, shallowGit, "config", "user.email", "test@example.invalid")

	// Advance remote main by 2 commits and shallow-fetch so origin/main and local HEAD share no ancestor.
	writeGit(t, seedDir, "remote1.txt", "r1\n")
	gitCommit(t, seedGit, "remote 1")
	writeGit(t, seedDir, "remote2.txt", "r2\n")
	gitCommit(t, seedGit, "remote 2")
	gitOK(t, seedGit, "push", "-q", "origin", "main")
	gitOK(t, shallowGit, "fetch", "-q", "--depth=1", "origin", "main")

	shallowHead := commitSHA(ctx, shallowGit, "HEAD", "")
	if mb := mergeBaseSHA(ctx, shallowGit, "refs/remotes/origin/main", shallowHead); mb != "" {
		t.Fatalf("expected disconnected shallow history with no merge-base, got %s", mb)
	}

	// Clean shallow clone with disconnected origin/main -> 0 changes (no reverse full-tree diff).
	changes, err = factory.EmbeddedChanges(ctx, threadID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("expected 0 changes for clean disconnected shallow clone, got %+v", changes)
	}

	// Dirty shallow clone -> only Uncommitted reported, Files empty.
	writeGit(t, shallowDir, "hello.txt", "local edit\n")
	changes, err = factory.EmbeddedChanges(ctx, threadID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].RepoPath != "subrepos/shallow-repo" ||
		len(changes[0].Changes.Files) != 0 || len(changes[0].Changes.Uncommitted) != 1 || changes[0].Changes.Uncommitted[0] != "hello.txt" {
		t.Fatalf("expected only uncommitted hello.txt for dirty disconnected shallow clone, got %+v", changes)
	}
}
