package local

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/sdk"
)

func initRemoteAndClone(t *testing.T, cloneDir string) (string, GitRun, GitRun) {
	t.Helper()
	tplBare, tplWork := ensureRemoteAndCloneTemplate(t)
	bare := filepath.Join(t.TempDir(), "origin.git")
	copyTree(t, tplBare, bare)
	copyTree(t, tplWork, cloneDir)
	cfgPath := filepath.Join(cloneDir, ".git", "config")
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.ReplaceAll(string(raw), filepath.ToSlash(tplBare), filepath.ToSlash(bare))
	if err := os.WriteFile(cfgPath, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}
	return bare, NewGit(bare, "file"), NewGit(cloneDir, "file")
}

func TestDiscoverEmbeddedReposKindsPruningForgedAndCorrupted(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	outer, outerGit := initGit(t)
	writeGitExclude(t, outer, "subrepos/\nnested/\nforged/\nbroken/\nnode_modules/\nvendor/\n.pnpm-store/\n.cache/\n")

	wt := filepath.Join(t.TempDir(), "wt")
	gitOK(t, outerGit, "worktree", "add", "-qb", "ariadne/discover", wt)

	// 1. Standalone .git directory.
	standaloneDir := filepath.Join(wt, "subrepos", "app")
	initRemoteAndClone(t, standaloneDir)

	// 2. Nested linked worktree + external outside worktree on the same host for forged gitfile test.
	host1, host1Git := initGit(t)
	nested1 := filepath.Join(wt, "nested", "svc")
	gitOK(t, host1Git, "worktree", "add", "-qb", "feat/svc", nested1)

	outsideWT := filepath.Join(t.TempDir(), "important-outside")
	gitOK(t, host1Git, "worktree", "add", "-qb", "feat/important", outsideWT)
	outsideGitDir := resolveWorktreeGitDir(t, outsideWT)

	// 3. Submodule-style gitfile (points to modules/, not worktrees/).
	modDir := filepath.Join(t.TempDir(), "modules", "sub")
	if err := os.MkdirAll(modDir, 0o755); err != nil {
		t.Fatal(err)
	}
	subDir := filepath.Join(wt, "subrepos", "submod")
	writeGit(t, subDir, ".git", "gitdir: "+filepath.ToSlash(modDir)+"\n")

	// 4. Malformed gitfile (missing target) and forged gitfile pointing to outsideWT.
	writeGit(t, filepath.Join(wt, "subrepos", "broken"), ".git", "gitdir: ./does-not-exist\n")
	writeGit(t, filepath.Join(wt, "forged", "trap"), ".git", "gitdir: "+filepath.ToSlash(outsideGitDir)+"\n")

	// 5. Corrupted .git directories (missing HEAD or missing objects/) must fail closed.
	missingHead := filepath.Join(wt, "broken", "missing-head", ".git")
	for _, sub := range []string{"objects", "refs"} {
		if err := os.MkdirAll(filepath.Join(missingHead, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	missingObjects := filepath.Join(wt, "broken", "missing-objects", ".git")
	if err := os.MkdirAll(filepath.Join(missingObjects, "refs"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeGit(t, filepath.Dir(missingObjects), filepath.Join(".git", "HEAD"), "ref: refs/heads/main\n")

	// 6. Pruned dependency/cache directories and over-depth directories must be skipped.
	for _, pruned := range []string{
		filepath.Join(wt, "node_modules", "pkg", ".git"),
		filepath.Join(wt, "vendor", "lib", ".git"),
		filepath.Join(wt, ".pnpm-store", "item", ".git"),
		filepath.Join(wt, ".cache", "repo", ".git"),
	} {
		if err := os.MkdirAll(pruned, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	deepPath := wt
	for range maxEmbeddedGitDepth + 1 {
		deepPath = filepath.Join(deepPath, "d")
	}
	if err := os.MkdirAll(filepath.Join(deepPath, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	// 7. Symlink to external repository must not be followed.
	linkPath := filepath.Join(wt, "subrepos", "linked-external")
	if err := os.Symlink(host1, linkPath); err != nil && runtime.GOOS != "windows" {
		t.Fatal(err)
	}

	repos, err := DiscoverEmbeddedRepos(ctx, wt)
	if err != nil {
		t.Fatal(err)
	}
	byRel := map[string]EmbeddedRepo{}
	for _, repo := range repos {
		byRel[repo.RelPath] = repo
	}
	if len(repos) != 7 {
		t.Fatalf("expected 7 embedded repos, got %d: %+v", len(repos), repos)
	}
	if r := byRel["subrepos/app"]; r.Kind != EmbeddedStandalone || !r.ObjectStoreInsideWorktree || r.Depth != 2 {
		t.Fatalf("unexpected standalone repo: %+v", r)
	}
	if r := byRel["nested/svc"]; r.Kind != EmbeddedWorktree || r.ObjectStoreInsideWorktree || r.RegisteredWorktreePath == "" || r.Depth != 2 {
		t.Fatalf("unexpected nested1 worktree: %+v", r)
	}
	if r := byRel["subrepos/submod"]; r.Kind != EmbeddedGitFile || r.GitDir == "" {
		t.Fatalf("unexpected submodule gitfile: %+v", r)
	}
	if r := byRel["subrepos/broken"]; r.Kind != EmbeddedGitFile || r.GitDir != "" {
		t.Fatalf("unexpected broken gitfile: %+v", r)
	}
	if r := byRel["forged/trap"]; r.RegisteredWorktreePath != "" {
		t.Fatalf("forged gitfile matched external worktree: %+v", r)
	}
	for _, rel := range []string{"subrepos/broken", "forged/trap", "broken/missing-head", "broken/missing-objects"} {
		st, err := InspectEmbeddedRepo(ctx, byRel[rel])
		if err != nil || !st.Dirty || !st.Unmerged {
			t.Fatalf("%s must fail closed: %+v, %v", rel, st, err)
		}
		if strings.HasPrefix(rel, "broken/") && !st.Unpushed {
			t.Fatalf("corrupted %s must mark Unpushed=true: %+v", rel, st)
		}
	}

	// Discard remove cleans wt without touching outsideWT via forged gitfile.
	if removed, err := RemoveWorktree(ctx, outerGit, wt, RemoveOptions{Discard: true}); err != nil || !removed {
		t.Fatalf("discard remove failed: %t, %v", removed, err)
	}
	if _, err := os.Stat(filepath.Join(outsideWT, "file.txt")); err != nil {
		t.Fatalf("outside worktree was deleted via forged gitfile: %v", err)
	}
}

func TestInspectEmbeddedRepoStates(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	t.Run("standalone ref and remote states", func(t *testing.T) {
		t.Parallel()
		wt := t.TempDir()
		repoDir := filepath.Join(wt, "subrepos", "app")
		_, _, repoGit := initRemoteAndClone(t, repoDir)
		gitDir := filepath.Join(repoDir, ".git")
		baseConfig, err := os.ReadFile(filepath.Join(gitDir, "config"))
		if err != nil {
			t.Fatal(err)
		}

		repos, err := DiscoverEmbeddedRepos(ctx, wt)
		if err != nil || len(repos) != 1 {
			t.Fatalf("discover: %+v, %v", repos, err)
		}
		embedded := repos[0]

		// 1. In-progress operation marker -> dirty.
		rebaseDir := filepath.Join(gitDir, "rebase-merge")
		if err := os.Mkdir(rebaseDir, 0o700); err != nil {
			t.Fatal(err)
		}
		st, err := InspectEmbeddedRepo(ctx, embedded)
		if err != nil || !st.Dirty {
			t.Fatalf("rebase-merge should be dirty: %+v, %v", st, err)
		}
		_ = os.Remove(rebaseDir)

		// 2. Detached HEAD -> dirty and unmerged.
		baseSHA := readHeadSHA(t, repoDir)
		writeGitRef(t, gitDir, "HEAD", baseSHA)
		st, err = InspectEmbeddedRepo(ctx, embedded)
		if err != nil || !st.Dirty || !st.Unmerged {
			t.Fatalf("detached HEAD should be dirty+unmerged: %+v, %v", st, err)
		}

		// 3. Local unpushed commit merged locally into main without pushing -> still unpushed + unmerged.
		writeGitRef(t, gitDir, "refs/heads/feat/work", baseSHA)
		writeGitRef(t, gitDir, "HEAD", "ref: refs/heads/feat/work")
		writeGit(t, repoDir, "feature.txt", "new feature\n")
		gitCommitOnly(t, repoGit, "feat commit")
		featSHA := readHeadSHA(t, repoDir)
		writeGitRef(t, gitDir, "refs/heads/main", featSHA)
		writeGitRef(t, gitDir, "HEAD", "ref: refs/heads/main")
		st, err = InspectEmbeddedRepo(ctx, embedded)
		if err != nil || st.Dirty || !st.Unpushed || !st.Unmerged {
			t.Fatalf("merged locally without push must still be unpushed: %+v, %v", st, err)
		}

		// 4. Pushed feature branch (PR open, not merged to origin/main) -> Unpushed=false, Unmerged=true.
		writeGitRef(t, gitDir, "refs/heads/main", baseSHA)
		writeGitRef(t, gitDir, "HEAD", "ref: refs/heads/feat/work")
		writeGitRef(t, gitDir, "refs/remotes/origin/feat/work", featSHA)
		appendGitConfig(t, gitDir, "\n[branch \"feat/work\"]\n\tremote = origin\n\tmerge = refs/heads/feat/work\n")
		st, err = InspectEmbeddedRepo(ctx, embedded)
		if err != nil || st.Dirty || st.Unpushed || !st.Unmerged {
			t.Fatalf("pushed but unmerged feat should be Unpushed=false Unmerged=true: %+v, %v", st, err)
		}

		// 5. Advance origin/main to feat/work -> landed.
		writeGitRef(t, gitDir, "refs/remotes/origin/main", featSHA)
		st, err = InspectEmbeddedRepo(ctx, embedded)
		if err != nil || st.Dirty || st.Unpushed || st.Unmerged {
			t.Fatalf("merged into origin/main should be landed: %+v, %v", st, err)
		}

		// 6. Non-origin remote "upstream" checks upstream/main without HEAD.
		_ = os.Remove(filepath.Join(gitDir, "packed-refs"))
		removeGitRef(t, gitDir, "refs/remotes/origin/HEAD")
		removeGitRef(t, gitDir, "refs/remotes/origin/main")
		removeGitRef(t, gitDir, "refs/heads/main")
		removeGitRef(t, gitDir, "refs/remotes/origin/feat/work")
		writeGitRef(t, gitDir, "refs/remotes/upstream/main", baseSHA)
		writeGitRef(t, gitDir, "refs/remotes/upstream/feat/work", featSHA)
		if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte("[core]\n\trepositoryformatversion = 0\n\tfilemode = false\n\tbare = false\n[remote \"upstream\"]\n\turl = file:///nonexistent.git\n\tfetch = +refs/heads/*:refs/remotes/upstream/*\n[branch \"feat/work\"]\n\tremote = upstream\n\tmerge = refs/heads/feat/work\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		st, err = InspectEmbeddedRepo(ctx, embedded)
		if err != nil || !st.Unmerged {
			t.Fatalf("unmerged feat/work on upstream remote must be Unmerged: %+v, %v", st, err)
		}
		writeGitRef(t, gitDir, "refs/remotes/upstream/main", featSHA)
		st, err = InspectEmbeddedRepo(ctx, embedded)
		if err != nil || st.Dirty || st.Unpushed || st.Unmerged {
			t.Fatalf("merged feat/work on upstream/main should be landed: %+v, %v", st, err)
		}

		// 7. Pure single-branch readonly clone without origin/HEAD is landed.
		removeGitRef(t, gitDir, "refs/remotes/upstream/main")
		removeGitRef(t, gitDir, "refs/remotes/upstream/feat/work")
		removeGitRef(t, gitDir, "refs/heads/feat/work")
		writeGitRef(t, gitDir, "refs/heads/trunk", featSHA)
		writeGitRef(t, gitDir, "HEAD", "ref: refs/heads/trunk")
		writeGitRef(t, gitDir, "refs/remotes/origin/trunk", featSHA)
		if err := os.WriteFile(filepath.Join(gitDir, "config"), append(baseConfig, []byte("\n[branch \"trunk\"]\n\tremote = origin\n\tmerge = refs/heads/trunk\n")...), 0o644); err != nil {
			t.Fatal(err)
		}
		st, err = InspectEmbeddedRepo(ctx, embedded)
		if err != nil || st.Dirty || st.Unpushed || st.Unmerged {
			t.Fatalf("clean single-branch clone should be landed: %+v, %v", st, err)
		}

		// 8. Local-only repo without remotes -> unpushed + unmerged.
		removeGitRef(t, gitDir, "refs/remotes/origin/trunk")
		if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte("[core]\n\trepositoryformatversion = 0\n\tfilemode = false\n\tbare = false\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		st, err = InspectEmbeddedRepo(ctx, embedded)
		if err != nil || st.Dirty || !st.Unpushed || !st.Unmerged {
			t.Fatalf("local-only repo without remotes must be unpushed+unmerged: %+v, %v", st, err)
		}
	})

	t.Run("nested worktree custom default branch and remote", func(t *testing.T) {
		t.Parallel()
		host, hostGit := initGit(t)
		hostGitDir := filepath.Join(host, ".git")
		baseSHA := readHeadSHA(t, host)
		writeGitRef(t, hostGitDir, "refs/heads/develop", baseSHA)
		writeGitRef(t, hostGitDir, "HEAD", "ref: refs/heads/develop")
		removeGitRef(t, hostGitDir, "refs/heads/main")
		wt := t.TempDir()
		nested := filepath.Join(wt, "nested", "svc")
		gitOK(t, hostGit, "worktree", "add", "-qb", "wt/task", nested)
		nestedGit := NewGit(nested, "file")

		repos, err := DiscoverEmbeddedRepos(ctx, wt)
		if err != nil || len(repos) != 1 {
			t.Fatalf("discover: %+v, %v", repos, err)
		}
		// 1. Freshly branched from local host develop -> landed.
		st, err := InspectEmbeddedRepo(ctx, repos[0])
		if err != nil || st.Dirty || st.Unmerged || st.Unpushed {
			t.Fatalf("fresh nested worktree should be landed: %+v, %v", st, err)
		}

		// 2. Unmerged commit against local host develop -> Unmerged=true, Unpushed=false.
		writeGit(t, nested, "svc.txt", "change\n")
		gitCommitOnly(t, nestedGit, "svc change")
		featSHA := readHeadSHA(t, nested)
		st, err = InspectEmbeddedRepo(ctx, repos[0])
		if err != nil || st.Dirty || !st.Unmerged || st.Unpushed {
			t.Fatalf("unmerged nested worktree commit: %+v, %v", st, err)
		}

		// 3. Configure origin/develop without origin/HEAD and push wt/task -> still Unmerged until merged into origin/develop.
		writeGitRef(t, hostGitDir, "refs/remotes/origin/develop", baseSHA)
		writeGitRef(t, hostGitDir, "refs/remotes/origin/wt/task", featSHA)
		appendGitConfig(t, hostGitDir, "\n[remote \"origin\"]\n\turl = file:///nonexistent.git\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n[branch \"develop\"]\n\tremote = origin\n\tmerge = refs/heads/develop\n[branch \"wt/task\"]\n\tremote = origin\n\tmerge = refs/heads/wt/task\n")
		st, err = InspectEmbeddedRepo(ctx, repos[0])
		if err != nil || !st.Unmerged {
			t.Fatalf("pushed unmerged wt/task must be Unmerged: %+v, %v", st, err)
		}

		// 4. Advance origin/develop (without fast-forwarding local host develop) -> landed.
		writeGitRef(t, hostGitDir, "refs/remotes/origin/develop", featSHA)
		st, err = InspectEmbeddedRepo(ctx, repos[0])
		if err != nil || st.Dirty || st.Unmerged || st.Unpushed {
			t.Fatalf("merged wt/task on origin/develop should be landed: %+v, %v", st, err)
		}
	})
}

func TestRemoveWorktreeProtectsStandaloneEmbeddedRepoAndAllowsIgnoredDist(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	outer, _ := initGit(t)
	writeGitExclude(t, outer, "subrepos/\ndist/\n.env\n")

	factory := newFactory(t, FactoryOptions{Path: outer, DataDir: t.TempDir(), DisableClone: true})
	ws, err := factory.Open(ctx, OpenRequest{ThreadID: "thr_standalone"})
	if err != nil {
		t.Fatal(err)
	}
	info := ws.Info()
	dir := info.Label
	ws.Close()

	// Build artifacts left in ignored dist/ must not block merged cleanup.
	writeGit(t, dir, "dist/bundle.js", "console.log('built')\n")

	subDir := filepath.Join(dir, "subrepos", "service")
	_, _, subGit := initRemoteAndClone(t, subDir)
	subGitDir := filepath.Join(subDir, ".git")

	// 1. Uncommitted change -> protected in Merged.
	writeGit(t, subDir, "dirty.txt", "wip\n")
	if removed, err := factory.Remove(ctx, "thr_standalone", RemoveOptions{Merged: true}); err != nil || removed {
		t.Fatalf("dirty embedded repo removed on merged: %t, %v", removed, err)
	}

	// 2. Committed locally without push -> still protected in Spare mode.
	baseSHA := readHeadSHA(t, subDir)
	writeGitRef(t, subGitDir, "refs/heads/feat/local", baseSHA)
	writeGitRef(t, subGitDir, "HEAD", "ref: refs/heads/feat/local")
	gitCommitOnly(t, subGit, "unpushed work")
	featSHA := readHeadSHA(t, subDir)
	if removed, err := factory.Remove(ctx, "thr_standalone", RemoveOptions{Spare: true}); err != nil || removed {
		t.Fatalf("unpushed standalone repo removed on idle: %t, %v", removed, err)
	}

	// 3. Merged into origin/main -> removed cleanly (along with dist/).
	writeGitRef(t, subGitDir, "refs/remotes/origin/main", featSHA)
	if removed, err := factory.Remove(ctx, "thr_standalone", RemoveOptions{Merged: true}); err != nil || !removed {
		t.Fatalf("landed standalone repo should be removed: %t, %v", removed, err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("worktree directory still exists: %v", err)
	}
}

func TestRemoveWorktreeGracefullyRemovesMultiLevelNestedWorktreesAndSnapshots(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	outer, _ := initGit(t)
	writeGitExclude(t, outer, "nested/\n")

	host1, host1Git := initGit(t)
	writeGitExclude(t, host1, "inner/\n")
	host2, host2Git := initGit(t)

	factory := newFactory(t, FactoryOptions{Path: outer, DataDir: t.TempDir(), DisableClone: true})
	const threadID = "thr_nested"
	ws, err := factory.Open(ctx, OpenRequest{ThreadID: threadID})
	if err != nil {
		t.Fatal(err)
	}
	info := ws.Info()
	dir := info.Label
	ws.Close()

	nested1 := filepath.Join(dir, "nested", "level1")
	gitOK(t, host1Git, "worktree", "add", "-qb", "feat/level1", nested1)
	nested2 := filepath.Join(nested1, "inner", "level2")
	gitOK(t, host2Git, "worktree", "add", "-qb", "feat/level2", nested2)

	// Add unmerged commit in deepest nested worktree.
	writeGit(t, nested2, "deep.txt", "deep change\n")
	gitCommitOnly(t, NewGit(nested2, "file"), "deep commit")

	if merged, err := factory.Merged(ctx, sdk.ThreadBranch{ThreadID: threadID, Branch: *info.Branch}, *info.Base); err != nil || merged {
		t.Fatalf("unmerged nested worktree must not be merged: %t, %v", merged, err)
	}
	if removed, err := factory.Remove(ctx, threadID, RemoveOptions{Merged: true}); err != nil || removed {
		t.Fatalf("unmerged nested worktree was removed: %t, %v", removed, err)
	}

	// Merge deepest branch into host2 main, BUT make outer worktree dirty ->
	// removal must refuse WITHOUT partially deleting nested worktrees.
	writeGitRef(t, filepath.Join(host2, ".git"), "refs/heads/main", readHeadSHA(t, nested2))
	writeGit(t, dir, "outer-dirty.txt", "keep me\n")
	if removed, err := factory.Remove(ctx, threadID, RemoveOptions{Merged: true}); err != nil || removed {
		t.Fatalf("dirty outer worktree was removed: %t, %v", removed, err)
	}
	if _, err := os.Stat(nested2); err != nil {
		t.Fatalf("nested2 was partially deleted despite dirty outer worktree: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "outer-dirty.txt")); err != nil {
		t.Fatal(err)
	}

	// Lock the shallower nested worktree while the deeper one is clean -> blocks without partial deletion.
	nested1GitDir := resolveWorktreeGitDir(t, nested1)
	writeGitRef(t, nested1GitDir, "locked", "")
	if removed, err := factory.Remove(ctx, threadID, RemoveOptions{Merged: true}); err != nil || removed {
		t.Fatalf("locked nested worktree must block Remove: %t, %v", removed, err)
	}
	if _, err := os.Stat(nested2); err != nil {
		t.Fatalf("deeper unlocked nested worktree was partially deleted: %v", err)
	}
	removeGitRef(t, nested1GitDir, "locked")

	// Now outer and both nested worktrees succeed -> deepest-first removal leaves no dangling entries.
	if removed, err := factory.Remove(ctx, threadID, RemoveOptions{Merged: true}); err != nil || !removed {
		t.Fatalf("clean merged nested worktrees should be removed: %t, %v", removed, err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("outer worktree still exists: %v", err)
	}
	assertSingleWorktreeFast(t, host1)
	assertSingleWorktreeFast(t, host2)
}

func TestRemoveWorktreeDiscardDropsExternalEmbeddedSnapshots(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	outer, _ := initGit(t)
	writeGitExclude(t, outer, "nested/\n")
	host1, host1Git := initGit(t)
	factory := newFactory(t, FactoryOptions{Path: outer, DataDir: t.TempDir(), DisableClone: true})

	// Discard cleans a dirty nested worktree, preserves host branch,
	// and drops external embedded snapshot refs only after removal succeeds.
	const discardID = "thr_discard"
	ws2, err := factory.Open(ctx, OpenRequest{ThreadID: discardID})
	if err != nil {
		t.Fatal(err)
	}
	dir2 := ws2.Info().Label
	ws2.Close()
	dirtyNested := filepath.Join(dir2, "nested", "dirty-wt")
	gitOK(t, host1Git, "worktree", "add", "-qb", "feat/dirty", dirtyNested)
	writeGit(t, dirtyNested, "uncommitted.txt", "dirty\n")
	if _, err := factory.RepoSnapshots(ctx, discardID); err != nil {
		t.Fatal(err)
	}
	host1CommonDir := filepath.Join(host1, ".git")
	if snapshotRefsDefinitelyEmpty(host1CommonDir, discardID) {
		t.Fatal("expected external embedded snapshot refs in host1 before discard")
	}

	origGit := factory.git
	failRemove := true
	factory.git = func(ctx context.Context, args []string, options GitOptions) sdk.ExecResult {
		if failRemove && len(args) >= 2 && args[0] == "worktree" && args[1] == "remove" {
			return sdk.ExecResult{ExitCode: 1, Stderr: "simulated worktree remove failure"}
		}
		return origGit(ctx, args, options)
	}
	if removed, err := factory.Remove(ctx, discardID, RemoveOptions{Discard: true}); err == nil || removed {
		t.Fatalf("expected Remove to fail when outer worktree remove fails, removed=%v err=%v", removed, err)
	}
	if snapshotRefsDefinitelyEmpty(host1CommonDir, discardID) {
		t.Fatal("expected external embedded snapshot refs to remain intact when RemoveWorktree fails")
	}
	if _, err := os.Stat(dirtyNested); os.IsNotExist(err) {
		gitOK(t, host1Git, "worktree", "add", "-q", dirtyNested, "feat/dirty")
	}
	failRemove = false
	if removed, err := factory.Remove(ctx, discardID, RemoveOptions{Discard: true}); err != nil || !removed {
		t.Fatalf("discard remove failed: %t, %v", removed, err)
	}
	if _, err := os.Stat(dir2); !os.IsNotExist(err) {
		t.Fatalf("outer worktree still exists after discard: %v", err)
	}
	assertSingleWorktreeFast(t, host1)
	if !snapshotRefsDefinitelyEmpty(host1CommonDir, discardID) {
		t.Fatal("expected external embedded snapshot refs to be dropped after RemoveWorktree succeeds")
	}
	if sha := readRefSHA(t, host1CommonDir, "refs/heads/feat/dirty"); sha == "" {
		t.Fatal("host branch lost")
	}
}

// initOrphanSubmoduleRemote creates a bare remote with:
//   - an orphan branch "vendor-orphan" (independent root commit, no ancestor with main)
//   - a "main" branch that registers the same remote URL as submodule "vendor-bundle"
//     tracking branch "vendor-orphan"
func initOrphanSubmoduleRemote(t *testing.T) (remoteBare, orphanSHA string) {
	t.Helper()
	ensureOrphanSubmoduleTemplate(t)
	return orphanSubBareDir, orphanSubSHA
}

func TestEmbeddedSubmoduleDiscoverInspectChangesAndRemove(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	remoteBare, orphanC1 := initOrphanSubmoduleRemote(t)
	remoteURL := fileURL(remoteBare)

	// Outer umbrella repo cloned from remoteBare with a direct submodule plus ignored nested worktree & fake gitfile.
	outer := filepath.Join(shortTempDir(t), "outer-submod")
	cloneOrphanSubmoduleRepo(t, remoteBare, outer)
	writeGitExclude(t, outer, "subrepos/\nnested-worktrees/\n")

	// External host repo with initialized submodule config that must not be mutated during worktree removal.
	hostDir := filepath.Join(t.TempDir(), "host-pkg")
	hostGit := cloneOrphanSubmoduleRepo(t, remoteBare, hostDir)
	hostConfigPath := filepath.Join(hostDir, ".git", "config")
	hostConfigBefore, err := os.ReadFile(hostConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	hostConfigBefore = append(hostConfigBefore, []byte("[submodule \"vendor-bundle\"]\n\tactive = true\n\turl = "+remoteURL+"\n")...)
	if err := os.WriteFile(hostConfigPath, hostConfigBefore, 0o644); err != nil {
		t.Fatal(err)
	}

	factory := newFactory(t, FactoryOptions{Path: outer, DataDir: shortTempDir(t), DisableClone: true})
	const threadID = "thr_submod"
	ws, err := factory.Open(ctx, OpenRequest{ThreadID: threadID})
	if err != nil {
		t.Fatal(err)
	}
	wt := ws.Info().Label
	ws.Close()

	// 1. Direct initialized submodule inside outer worktree.
	initOrphanSubmoduleFast(t, wt, "vendor-bundle", remoteURL)

	// 2. Nested linked worktree inside outer worktree with its own initialized orphan submodule.
	nestedWT := filepath.Join(wt, "nested-worktrees", "feat-split", "pkg")
	if err := os.MkdirAll(filepath.Dir(nestedWT), 0o755); err != nil {
		t.Fatal(err)
	}
	gitOK(t, hostGit, "worktree", "add", "-qb", "feat/split", nestedWT)
	nestedGit := NewGit(nestedWT, "file")
	nestedSubGit := initOrphanSubmoduleFast(t, nestedWT, "vendor-bundle", remoteURL)

	// 3. Fake gitfile not registered as a parent gitlink must stay IsSubmodule()==false.
	fakeModDir := filepath.Join(t.TempDir(), "modules", "fake")
	if err := os.MkdirAll(fakeModDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeSub := filepath.Join(wt, "subrepos", "fake-gitfile")
	writeGit(t, fakeSub, ".git", "gitdir: "+filepath.ToSlash(fakeModDir)+"\n")

	// Discover + resolveEmbeddedRepoPath metadata verification.
	repos, err := DiscoverEmbeddedRepos(ctx, wt)
	if err != nil {
		t.Fatal(err)
	}
	byRel := map[string]EmbeddedRepo{}
	for _, r := range repos {
		byRel[r.RelPath] = r
	}
	directSub, ok := byRel["vendor-bundle"]
	if !ok || !directSub.IsSubmodule() || directSub.Kind != EmbeddedGitFile || !directSub.ObjectStoreInsideWorktree ||
		directSub.SubmodulePath != "vendor-bundle" || worktreePathKey(directSub.SuperprojectPath) != worktreePathKey(wt) {
		t.Fatalf("unexpected direct-sub metadata: %+v", directSub)
	}
	const nestedRel = "nested-worktrees/feat-split/pkg"
	const nestedSubRel = "nested-worktrees/feat-split/pkg/vendor-bundle"
	if r := byRel[nestedRel]; r.Kind != EmbeddedWorktree || r.ObjectStoreInsideWorktree {
		t.Fatalf("external-host nested worktree must keep ObjectStoreInsideWorktree=false: %+v", r)
	}
	nestedSub, ok := byRel[nestedSubRel]
	if !ok || !nestedSub.IsSubmodule() || nestedSub.Kind != EmbeddedGitFile || !nestedSub.ObjectStoreInsideWorktree ||
		nestedSub.SubmodulePath != "vendor-bundle" || worktreePathKey(nestedSub.SuperprojectPath) != worktreePathKey(nestedWT) {
		t.Fatalf("unexpected nested submodule metadata: %+v", nestedSub)
	}
	if resolved, err := resolveEmbeddedRepoPath(ctx, wt, nestedSubRel); err != nil || !resolved.IsSubmodule() || resolved.SubmodulePath != "vendor-bundle" || worktreePathKey(resolved.SuperprojectPath) != worktreePathKey(nestedWT) {
		t.Fatalf("resolveEmbeddedRepoPath(%q): %+v, err=%v", nestedSubRel, resolved, err)
	}
	if fakeRepo, ok := byRel["subrepos/fake-gitfile"]; !ok || fakeRepo.Kind != EmbeddedGitFile || fakeRepo.IsSubmodule() {
		t.Fatalf("fake gitfile must stay non-submodule EmbeddedGitFile: %+v", fakeRepo)
	}
	// Remove fake gitfile now so subsequent EmbeddedChanges/Remove assertions focus on valid submodules.
	if err := os.RemoveAll(filepath.Join(wt, "subrepos")); err != nil {
		t.Fatal(err)
	}

	// Clean aligned detached orphan submodule (even without remote tracking ref) -> 0 EmbeddedChanges.
	removeGitRef(t, nestedSub.GitDir, "refs/remotes/origin/vendor-orphan")
	if changes, err := factory.EmbeddedChanges(ctx, threadID, nil); err != nil || len(changes) != 0 {
		t.Fatalf("expected 0 active embedded repos for clean orphan submodules, got %+v, err=%v", changes, err)
	}

	// Dirty working tree inside nested submodule -> Uncommitted only (Files empty).
	writeGit(t, nestedSub.Path, "bundle.js", "export const version = 2;\n")
	if subChanges, err := EmbeddedRepoChanges(ctx, nestedSub, nil); err != nil || subChanges == nil || len(subChanges.Files) != 0 || len(subChanges.Uncommitted) != 1 || subChanges.Uncommitted[0] != "bundle.js" {
		t.Fatalf("expected only uncommitted bundle.js for dirty orphan submodule, got %+v, err=%v", subChanges, err)
	}

	// Diverged detached commit C2 inside nested submodule -> Dirty+Unmerged, C1..C2 diff reported.
	gitCommitTracked(t, nestedSubGit, "diverged detached commit")
	orphanC2 := readHeadSHA(t, nestedSub.Path)
	if orphanC2 == "" || orphanC2 == orphanC1 {
		t.Fatalf("expected new diverged commit, got %s", orphanC2)
	}
	st, err := InspectEmbeddedRepo(ctx, nestedSub)
	if err != nil || !st.Dirty || !st.Unmerged {
		t.Fatalf("diverged detached submodule should be Dirty+Unmerged: %+v, err=%v", st, err)
	}
	if subChanges, err := EmbeddedRepoChanges(ctx, nestedSub, nil); err != nil || subChanges == nil || subChanges.From != orphanC1 || subChanges.Head != orphanC2 ||
		len(subChanges.Commits) != 1 || len(subChanges.Files) != 1 || subChanges.Files[0].Path != "bundle.js" {
		t.Fatalf("unexpected submodule C1..C2 diff: %+v, err=%v", subChanges, err)
	}

	// Named branch ahead of origin/vendor-orphan (even with local-only merge) -> Unmerged; parent gitlink bump does not swallow unpushed diff or bypass Remove guard.
	writeGitRef(t, nestedSub.GitDir, "refs/heads/feat/sub-branch", orphanC2)
	writeGitRef(t, nestedSub.GitDir, "HEAD", "ref: refs/heads/feat/sub-branch")
	writeGitRef(t, nestedSub.GitDir, "refs/remotes/origin/feat/sub-branch", orphanC2)
	writeGitRef(t, nestedSub.GitDir, "refs/remotes/origin/vendor-orphan", orphanC1)
	writeGitRef(t, nestedSub.GitDir, "refs/heads/vendor-orphan", orphanC2)
	appendGitConfig(t, nestedSub.GitDir, "\n[branch \"feat/sub-branch\"]\n\tremote = origin\n\tmerge = refs/heads/feat/sub-branch\n")
	st, err = InspectEmbeddedRepo(ctx, nestedSub)
	if err != nil || st.Dirty || st.Unpushed || !st.Unmerged {
		t.Fatalf("pushed unmerged submodule branch must remain Unmerged even with local-only merge: %+v, err=%v", st, err)
	}
	removeGitRef(t, nestedSub.GitDir, "refs/heads/vendor-orphan")

	gitCommitTracked(t, nestedGit, "bump vendor-bundle gitlink")
	baseline := &sdk.RepoBaseline{RepoPath: nestedSubRel, BaseHead: orphanC1, BaseBranch: "HEAD"}
	if subChanges, err := EmbeddedRepoChanges(ctx, nestedSub, baseline); err != nil || subChanges == nil || subChanges.From != orphanC1 || subChanges.Head != orphanC2 ||
		len(subChanges.Files) != 1 || subChanges.Files[0].Path != "bundle.js" {
		t.Fatalf("parent gitlink update must not swallow unpushed submodule diff: %+v, err=%v", subChanges, err)
	}
	if removed, err := factory.Remove(ctx, threadID, RemoveOptions{Merged: true}); err != nil || removed {
		t.Fatalf("unpushed aligned submodule commit must block merged Remove: removed=%t err=%v", removed, err)
	}

	// Advance origin/vendor-orphan to C2 -> diff clears; stash still protects.
	writeGitRef(t, nestedSub.GitDir, "refs/remotes/origin/vendor-orphan", orphanC2)
	if subChanges, err := EmbeddedRepoChanges(ctx, nestedSub, baseline); err != nil || subChanges != nil {
		t.Fatalf("expected submodule diff to clear once C2 lands on origin/vendor-orphan, got %+v, err=%v", subChanges, err)
	}
	writeGitRef(t, nestedSub.GitDir, "refs/stash", orphanC2)
	if st, err := InspectEmbeddedRepo(ctx, nestedSub); err != nil || st.Dirty || !st.Unpushed || !st.Unmerged {
		t.Fatalf("stash in submodule must be protected: %+v, err=%v", st, err)
	}
	removeGitRef(t, nestedSub.GitDir, "refs/stash")

	// Land nested worktree on host main -> clean merged remove succeeds without mutating external host config or leaving dangling worktrees.
	writeGitRef(t, filepath.Join(hostDir, ".git"), "refs/heads/main", readHeadSHA(t, nestedWT))
	if removed, err := factory.Remove(ctx, threadID, RemoveOptions{Merged: true}); err != nil || !removed {
		t.Fatalf("clean aligned submodules should be removed on merged: %t, %v", removed, err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("outer worktree directory still exists: %v", err)
	}
	assertSingleWorktreeFast(t, hostDir)
	hostConfigAfter, err := os.ReadFile(hostConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(hostConfigAfter) != string(hostConfigBefore) {
		t.Fatalf("host .git/config was mutated during nested worktree removal:\nbefore:\n%s\nafter:\n%s", hostConfigBefore, hostConfigAfter)
	}
}

func TestSubmoduleGitlinkParsingAndGitmodulesBranch(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	sha1 := strings.Repeat("a", 40)
	sha2 := strings.Repeat("b", 40)
	sha3 := strings.Repeat("c", 64)

	// parseLsTreeGitlink requires exact path match and 160000 commit mode.
	lsTreeOut := strings.Join([]string{
		"160000 commit " + sha1 + "\tvendor-bundle-extra",
		"040000 tree " + sha2 + "\tvendor-bundle",
		"160000 commit " + sha3 + "\tvendor-bundle",
	}, "\x00") + "\x00"
	if got := parseLsTreeGitlink(lsTreeOut, "vendor-bundle"); got != sha3 {
		t.Fatalf("parseLsTreeGitlink exact match = %q, want %q", got, sha3)
	}
	if got := parseLsTreeGitlink("160000 commit "+sha1+"\tvendor-bundle-extra\x00", "vendor-bundle"); got != "" {
		t.Fatalf("parseLsTreeGitlink must not prefix-match: got %q", got)
	}

	// parseLsFilesGitlink prefers stage 0, falls back to stage 2 during merge conflicts.
	lsFilesConflict := strings.Join([]string{
		"160000 " + sha1 + " 1\tlibs/sub",
		"160000 " + sha2 + " 2\tlibs/sub",
		"160000 " + sha3 + " 3\tlibs/sub",
	}, "\x00") + "\x00"
	if got := parseLsFilesGitlink(lsFilesConflict, "libs/sub"); got != sha2 {
		t.Fatalf("parseLsFilesGitlink conflict stage = %q, want %q", got, sha2)
	}
	if got := parseLsFilesGitlink("100644 "+sha1+" 0\tlibs/sub\x00", "libs/sub"); got != "" {
		t.Fatalf("parseLsFilesGitlink must reject non-160000 mode, got %q", got)
	}

	// readSubmoduleConfiguredBranch handles explicit branch, path normalization, ".", and invalid values.
	repoDir, _ := initGit(t)
	repoGitDir := filepath.Join(repoDir, ".git")
	repoBaseSHA := readHeadSHA(t, repoDir)
	writeGitRef(t, repoGitDir, "refs/heads/release/v2", repoBaseSHA)
	writeGitRef(t, repoGitDir, "HEAD", "ref: refs/heads/release/v2")
	writeGit(t, repoDir, ".gitmodules", strings.Join([]string{
		`[submodule "explicit"]`,
		"\tpath = ./libs/explicit/",
		"\turl = https://example.invalid/repo.git",
		"\tbranch = vendor-orphan",
		`[submodule "follow-parent"]`,
		"\tpath = libs/follow",
		"\turl = https://example.invalid/repo.git",
		"\tbranch = .",
		`[submodule "bad-flag"]`,
		"\tpath = libs/bad-flag",
		"\turl = https://example.invalid/repo.git",
		"\tbranch = --upload-pack=evil",
		`[submodule "bad-space"]`,
		"\tpath = libs/bad-space",
		"\turl = https://example.invalid/repo.git",
		"\tbranch = has space",
	}, "\n")+"\n")

	if got := readSubmoduleConfiguredBranch(ctx, repoDir, "libs/explicit"); got != "vendor-orphan" {
		t.Fatalf("explicit branch = %q, want vendor-orphan", got)
	}
	if got := readSubmoduleConfiguredBranch(ctx, repoDir, "libs/follow"); got != "release/v2" {
		t.Fatalf("dot branch = %q, want release/v2", got)
	}
	if got := readSubmoduleConfiguredBranch(ctx, repoDir, "libs/bad-flag"); got != "" {
		t.Fatalf("leading-dash branch must be rejected, got %q", got)
	}
	if got := readSubmoduleConfiguredBranch(ctx, repoDir, "libs/bad-space"); got != "" {
		t.Fatalf("branch with whitespace must be rejected, got %q", got)
	}

	// When superproject HEAD is detached, branch = "." resolves to "".
	writeGitRef(t, repoGitDir, "HEAD", repoBaseSHA)
	if got := readSubmoduleConfiguredBranch(ctx, repoDir, "libs/follow"); got != "" {
		t.Fatalf("dot branch on detached superproject must be empty, got %q", got)
	}
}

func TestSubmoduleGitlinkLiteralPathspecs(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	outer, outerGit := initGit(t)
	subSHA := readHeadSHA(t, outer)
	const bracketPath = "packages/[pkg]"
	writeGit(t, outer, ".gitmodules", "[submodule \""+bracketPath+"\"]\n\tpath = "+bracketPath+"\n\turl = https://example.invalid/pkg.git\n\tbranch = main\n")
	gitOK(t, outerGit, "add", ".gitmodules")
	for _, subPath := range []string{bracketPath, ":colon-sub", ":(bad)sub"} {
		gitOK(t, outerGit, "-c", "core.protectNTFS=false", "update-index", "--add", "--cacheinfo", "160000,"+subSHA+","+subPath)
	}
	gitOK(t, outerGit, "-c", "core.protectNTFS=false", "-c", "user.name=Ariadne Test", "-c", "user.email=test@example.invalid", "commit", "-q", "-m", "add literal pathspec gitlinks")

	// Materialize the bracket submodule module gitdir inside outer/.git/modules/<bracketPath>.
	modDir := filepath.Join(outer, ".git", "modules", filepath.FromSlash(bracketPath))
	subDir := filepath.Join(outer, filepath.FromSlash(bracketPath))
	copyTree(t, filepath.Join(ensureBaseGitTemplate(t), ".git"), modDir)
	writeGit(t, subDir, "file.txt", "base\n")
	writeGit(t, subDir, ".git", "gitdir: "+filepath.ToSlash(modDir)+"\n")

	if got := readSubmoduleConfiguredBranch(ctx, outer, bracketPath); got != "main" {
		t.Fatalf("readSubmoduleConfiguredBranch(%q) = %q, want main", bracketPath, got)
	}
	for _, subPath := range []string{bracketPath, ":colon-sub", ":(bad)sub"} {
		if got := readSubmoduleIndexGitlink(ctx, outer, subPath); got != subSHA {
			t.Fatalf("readSubmoduleIndexGitlink(%q) = %q, want %q", subPath, got, subSHA)
		}
		if got := readSubmoduleHeadGitlink(ctx, outer, subPath); got != subSHA {
			t.Fatalf("readSubmoduleHeadGitlink(%q) = %q, want %q", subPath, got, subSHA)
		}
	}
	repos, err := DiscoverEmbeddedRepos(ctx, outer)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || !repos[0].IsSubmodule() || repos[0].SubmodulePath != bracketPath || !repos[0].ObjectStoreInsideWorktree {
		t.Fatalf("expected bracket submodule to be discovered as submodule with ObjectStoreInsideWorktree=true, got %+v", repos)
	}
}
