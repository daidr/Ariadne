package local

import (
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

func TestDiscoverEmbeddedReposKindsPruningAndSymlinks(t *testing.T) {
	t.Parallel()
	outer, outerGit := initGit(t)
	writeGitExclude(t, outer, "subrepos/\nnested/\nnode_modules/\nvendor/\n.pnpm-store/\n.cache/\n")

	wt := filepath.Join(t.TempDir(), "wt")
	gitOK(t, outerGit, "worktree", "add", "-qb", "ariadne/discover", wt)

	// 1. Standalone .git directory.
	standaloneDir := filepath.Join(wt, "subrepos", "app")
	initRemoteAndClone(t, standaloneDir)

	// 2. Nested linked worktree + deeper nested linked worktree inside it.
	host1, host1Git := initGit(t)
	writeGitExclude(t, host1, "deep/\n")
	nested1 := filepath.Join(wt, "nested", "svc")
	gitOK(t, host1Git, "worktree", "add", "-qb", "feat/svc", nested1)

	_, host2Git := initGit(t)
	nested2 := filepath.Join(nested1, "deep", "inner")
	gitOK(t, host2Git, "worktree", "add", "-qb", "feat/inner", nested2)

	// 3. Submodule-style gitfile (points to modules/, not worktrees/).
	modDir := filepath.Join(t.TempDir(), "modules", "sub")
	if err := os.MkdirAll(modDir, 0755); err != nil {
		t.Fatal(err)
	}
	subDir := filepath.Join(wt, "subrepos", "submod")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeGit(t, subDir, ".git", "gitdir: "+filepath.ToSlash(modDir)+"\n")

	// 4. Malformed gitfile (missing target).
	brokenDir := filepath.Join(wt, "subrepos", "broken")
	writeGit(t, brokenDir, ".git", "gitdir: ./does-not-exist\n")

	// 5. Pruned dependency/cache directories must be skipped.
	for _, pruned := range []string{
		filepath.Join(wt, "node_modules", "pkg", ".git"),
		filepath.Join(wt, "vendor", "lib", ".git"),
		filepath.Join(wt, ".pnpm-store", "item", ".git"),
		filepath.Join(wt, ".cache", "repo", ".git"),
	} {
		if err := os.MkdirAll(pruned, 0755); err != nil {
			t.Fatal(err)
		}
	}

	// 6. Depth limit: depth 9 is pruned.
	deepPath := wt
	for range maxEmbeddedGitDepth + 1 {
		deepPath = filepath.Join(deepPath, "d")
	}
	if err := os.MkdirAll(filepath.Join(deepPath, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	// 7. Symlink to external repository must not be followed.
	externalRepo, _ := initGit(t)
	linkPath := filepath.Join(wt, "subrepos", "linked-external")
	if err := os.Symlink(externalRepo, linkPath); err != nil && runtime.GOOS != "windows" {
		t.Fatal(err)
	}

	repos, err := DiscoverEmbeddedRepos(t.Context(), wt)
	if err != nil {
		t.Fatal(err)
	}
	byRel := map[string]EmbeddedRepo{}
	for _, repo := range repos {
		byRel[repo.RelPath] = repo
	}
	if len(repos) != 5 {
		t.Fatalf("expected 5 embedded repos, got %d: %+v", len(repos), repos)
	}
	if r := byRel["subrepos/app"]; r.Kind != EmbeddedStandalone || !r.ObjectStoreInsideWorktree || r.Depth != 2 {
		t.Fatalf("unexpected standalone repo: %+v", r)
	}
	if r := byRel["nested/svc"]; r.Kind != EmbeddedWorktree || r.ObjectStoreInsideWorktree || r.RegisteredWorktreePath == "" || r.Depth != 2 {
		t.Fatalf("unexpected nested1 worktree: %+v", r)
	}
	if r := byRel["nested/svc/deep/inner"]; r.Kind != EmbeddedWorktree || r.ObjectStoreInsideWorktree || r.RegisteredWorktreePath == "" || r.Depth != 4 {
		t.Fatalf("unexpected nested2 worktree: %+v", r)
	}
	if r := byRel["subrepos/submod"]; r.Kind != EmbeddedGitFile || r.GitDir == "" {
		t.Fatalf("unexpected submodule gitfile: %+v", r)
	}
	if r := byRel["subrepos/broken"]; r.Kind != EmbeddedGitFile || r.GitDir != "" {
		t.Fatalf("unexpected broken gitfile: %+v", r)
	}
	brokenStatus, err := InspectEmbeddedRepo(t.Context(), byRel["subrepos/broken"])
	if err != nil || !brokenStatus.Dirty || !brokenStatus.Unmerged {
		t.Fatalf("broken gitfile must fail closed: %+v, %v", brokenStatus, err)
	}
}

func TestDiscoverEmbeddedReposRejectsForgedWorktreeGitFile(t *testing.T) {
	t.Parallel()
	outer, outerGit := initGit(t)
	writeGitExclude(t, outer, "forged/\n")
	wt := filepath.Join(t.TempDir(), "wt")
	gitOK(t, outerGit, "worktree", "add", "-qb", "ariadne/forged", wt)

	// External host and a real worktree outside wt.
	_, hostGit := initGit(t)
	outsideWT := filepath.Join(t.TempDir(), "important-outside")
	gitOK(t, hostGit, "worktree", "add", "-qb", "feat/important", outsideWT)
	outsideGitDir := resolveWorktreeGitDir(t, outsideWT)

	// Craft a malicious .git file inside wt pointing at outsideWT's gitdir.
	forgedDir := filepath.Join(wt, "forged", "trap")
	writeGit(t, forgedDir, ".git", "gitdir: "+filepath.ToSlash(outsideGitDir)+"\n")

	repos, err := DiscoverEmbeddedRepos(t.Context(), wt)
	if err != nil || len(repos) != 1 {
		t.Fatalf("expected 1 repo, got %+v, %v", repos, err)
	}
	if repos[0].RegisteredWorktreePath != "" {
		t.Fatalf("forged gitfile matched external worktree: %+v", repos[0])
	}
	status, err := InspectEmbeddedRepo(t.Context(), repos[0])
	if err != nil || !status.Dirty {
		t.Fatalf("forged gitfile must be treated as dirty: %+v, %v", status, err)
	}
	// Non-discard remove refuses; discard remove must NOT touch outsideWT.
	if removed, err := RemoveWorktree(t.Context(), outerGit, wt, RemoveOptions{Merged: true}); err != nil || removed {
		t.Fatalf("forged gitfile was not protected: %t, %v", removed, err)
	}
	if removed, err := RemoveWorktree(t.Context(), outerGit, wt, RemoveOptions{Discard: true}); err != nil || !removed {
		t.Fatalf("discard remove failed: %t, %v", removed, err)
	}
	if _, err := os.Stat(filepath.Join(outsideWT, "file.txt")); err != nil {
		t.Fatalf("outside worktree was deleted via forged gitfile: %v", err)
	}
}

func TestInspectEmbeddedRepoStates(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	t.Run("standalone clone lifecycle", func(t *testing.T) {
		t.Parallel()
		wt := t.TempDir()
		repoDir := filepath.Join(wt, "subrepos", "app")
		_, _, repoGit := initRemoteAndClone(t, repoDir)

		repos, err := DiscoverEmbeddedRepos(ctx, wt)
		if err != nil || len(repos) != 1 {
			t.Fatalf("discover: %+v, %v", repos, err)
		}
		embedded := repos[0]

		// Clean read-only clone at origin/main.
		st, err := InspectEmbeddedRepo(ctx, embedded)
		if err != nil || st.Dirty || st.Unmerged || st.Unpushed {
			t.Fatalf("clean clone: %+v, %v", st, err)
		}

		// In-progress operation marker -> dirty.
		rebaseDir := filepath.Join(repoDir, ".git", "rebase-merge")
		if err := os.Mkdir(rebaseDir, 0700); err != nil {
			t.Fatal(err)
		}
		st, err = InspectEmbeddedRepo(ctx, embedded)
		if err != nil || !st.Dirty {
			t.Fatalf("rebase-merge should be dirty: %+v, %v", st, err)
		}
		_ = os.Remove(rebaseDir)

		// Detached HEAD -> dirty and unmerged.
		gitDir := filepath.Join(repoDir, ".git")
		baseSHA := readHeadSHA(t, repoDir)
		writeGitRef(t, gitDir, "HEAD", baseSHA)
		st, err = InspectEmbeddedRepo(ctx, embedded)
		if err != nil || !st.Dirty || !st.Unmerged {
			t.Fatalf("detached HEAD should be dirty+unmerged: %+v, %v", st, err)
		}
		writeGitRef(t, gitDir, "HEAD", "ref: refs/heads/main")

		// Local unpushed commit on feature branch -> unpushed + unmerged.
		writeGitRef(t, gitDir, "refs/heads/feat/work", baseSHA)
		writeGitRef(t, gitDir, "HEAD", "ref: refs/heads/feat/work")
		writeGit(t, repoDir, "feature.txt", "new feature\n")
		gitCommitOnly(t, repoGit, "feat commit")
		featSHA := readHeadSHA(t, repoDir)
		st, err = InspectEmbeddedRepo(ctx, embedded)
		if err != nil || st.Dirty || !st.Unpushed || !st.Unmerged {
			t.Fatalf("unpushed feat commit: %+v, %v", st, err)
		}

		// Merging feat/work into local main without pushing (and switching to main) -> STILL unpushed!
		writeGitRef(t, gitDir, "refs/heads/main", featSHA)
		writeGitRef(t, gitDir, "HEAD", "ref: refs/heads/main")
		st, err = InspectEmbeddedRepo(ctx, embedded)
		if err != nil || st.Dirty || !st.Unpushed || !st.Unmerged {
			t.Fatalf("merged locally without push must still be unpushed: %+v, %v", st, err)
		}

		// Reset local main back to origin/main, switch to feat/work, publish origin/feat/work (PR open, not merged to origin/main).
		writeGitRef(t, gitDir, "refs/heads/main", baseSHA)
		writeGitRef(t, gitDir, "HEAD", "ref: refs/heads/feat/work")
		writeGitRef(t, gitDir, "refs/remotes/origin/feat/work", featSHA)
		appendGitConfig(t, gitDir, "\n[branch \"feat/work\"]\n\tremote = origin\n\tmerge = refs/heads/feat/work\n")
		st, err = InspectEmbeddedRepo(ctx, embedded)
		if err != nil || st.Dirty || st.Unpushed || !st.Unmerged {
			t.Fatalf("pushed but unmerged feat should be Unpushed=false Unmerged=true: %+v, %v", st, err)
		}

		// Advance origin/main to feat/work (landed) -> clean, pushed, merged.
		writeGitRef(t, gitDir, "refs/remotes/origin/main", featSHA)
		st, err = InspectEmbeddedRepo(ctx, embedded)
		if err != nil || st.Dirty || st.Unpushed || st.Unmerged {
			t.Fatalf("merged into origin/main should be landed: %+v, %v", st, err)
		}
	})

	t.Run("standalone local-only repo without remotes", func(t *testing.T) {
		t.Parallel()
		wt := t.TempDir()
		repoDir := filepath.Join(wt, "local-repo")
		copyTree(t, ensureBaseGitTemplate(t), repoDir)

		repos, err := DiscoverEmbeddedRepos(ctx, wt)
		if err != nil || len(repos) != 1 {
			t.Fatalf("discover: %+v, %v", repos, err)
		}
		st, err := InspectEmbeddedRepo(ctx, repos[0])
		if err != nil || st.Dirty || !st.Unpushed || !st.Unmerged {
			t.Fatalf("local-only repo with commit must be unpushed+unmerged: %+v, %v", st, err)
		}
	})

	t.Run("nested worktree against local host non-main default branch", func(t *testing.T) {
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
		// Freshly branched from develop -> 0 new commits -> landed.
		st, err := InspectEmbeddedRepo(ctx, repos[0])
		if err != nil || st.Dirty || st.Unmerged || st.Unpushed {
			t.Fatalf("fresh nested worktree should be landed: %+v, %v", st, err)
		}

		// Add unmerged commit -> unmerged (object store is in external host, so Unpushed=false).
		writeGit(t, nested, "svc.txt", "change\n")
		gitCommitOnly(t, nestedGit, "svc change")
		st, err = InspectEmbeddedRepo(ctx, repos[0])
		if err != nil || st.Dirty || !st.Unmerged || st.Unpushed {
			t.Fatalf("unmerged nested worktree: %+v, %v", st, err)
		}

		// Merge into host's develop -> landed!
		writeGitRef(t, hostGitDir, "refs/heads/develop", readHeadSHA(t, nested))
		st, err = InspectEmbeddedRepo(ctx, repos[0])
		if err != nil || st.Dirty || st.Unmerged || st.Unpushed {
			t.Fatalf("merged nested worktree should be landed: %+v, %v", st, err)
		}
	})
}

func TestRemoveWorktreeSingleRepoWithIgnoredDistStillCleansOnMerged(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	root, _ := initGit(t)
	writeGitExclude(t, root, "dist/\n.env\n")

	factory := newFactory(t, FactoryOptions{Path: root, DataDir: t.TempDir(), DisableClone: true})
	ws, err := factory.Open(ctx, OpenRequest{ThreadID: "thr_single"})
	if err != nil {
		t.Fatal(err)
	}
	info := ws.Info()
	dir := filepath.Dir(filepath.Join(info.Label, "x"))
	ws.Close()

	// Build artifacts left in dist/ must NOT block merged cleanup.
	writeGit(t, dir, "dist/bundle.js", "console.log('built')\n")

	removed, err := factory.Remove(ctx, "thr_single", RemoveOptions{Merged: true})
	if err != nil || !removed {
		t.Fatalf("single repo with dist/ should be removed on merged: %t, %v", removed, err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("single repo worktree still exists: %v", err)
	}
}

func TestRemoveWorktreeProtectsStandaloneEmbeddedRepoAndAllowsCleanReadOnly(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	outer, _ := initGit(t)
	writeGitExclude(t, outer, "subrepos/\n")

	factory := newFactory(t, FactoryOptions{Path: outer, DataDir: t.TempDir(), DisableClone: true})
	ws, err := factory.Open(ctx, OpenRequest{ThreadID: "thr_standalone"})
	if err != nil {
		t.Fatal(err)
	}
	info := ws.Info()
	dir := info.Label
	ws.Close()

	subDir := filepath.Join(dir, "subrepos", "service")
	_, _, subGit := initRemoteAndClone(t, subDir)
	subGitDir := filepath.Join(subDir, ".git")

	// 1. Uncommitted change -> protected in Merged and Remove.
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

	// 3. Merged into origin/main -> Merged=true and removed cleanly!
	writeGitRef(t, subGitDir, "refs/remotes/origin/main", featSHA)
	if merged, err := factory.Merged(ctx, sdk.ThreadBranch{ThreadID: "thr_standalone", Branch: *info.Branch}, *info.Base); err != nil || !merged {
		t.Fatalf("landed standalone repo should be merged: %t, %v", merged, err)
	}
	if removed, err := factory.Remove(ctx, "thr_standalone", RemoveOptions{Merged: true}); err != nil || !removed {
		t.Fatalf("landed standalone repo should be removed: %t, %v", removed, err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("worktree directory still exists: %v", err)
	}
}

func TestRemoveWorktreeGracefullyRemovesMultiLevelNestedWorktrees(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	outer, _ := initGit(t)
	writeGitExclude(t, outer, "nested/\n")

	host1, host1Git := initGit(t)
	writeGitExclude(t, host1, "inner/\n")
	host2, host2Git := initGit(t)

	factory := newFactory(t, FactoryOptions{Path: outer, DataDir: t.TempDir(), DisableClone: true})
	ws, err := factory.Open(ctx, OpenRequest{ThreadID: "thr_nested"})
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

	if merged, err := factory.Merged(ctx, sdk.ThreadBranch{ThreadID: "thr_nested", Branch: *info.Branch}, *info.Base); err != nil || merged {
		t.Fatalf("unmerged nested worktree must not be merged: %t, %v", merged, err)
	}
	if removed, err := factory.Remove(ctx, "thr_nested", RemoveOptions{Merged: true}); err != nil || removed {
		t.Fatalf("unmerged nested worktree was removed: %t, %v", removed, err)
	}

	// Merge deepest branch into host2 main, BUT make outer worktree dirty ->
	// removal must refuse WITHOUT partially deleting nested worktrees!
	writeGitRef(t, filepath.Join(host2, ".git"), "refs/heads/main", readHeadSHA(t, nested2))
	writeGit(t, dir, "outer-dirty.txt", "keep me\n")
	if removed, err := factory.Remove(ctx, "thr_nested", RemoveOptions{Merged: true}); err != nil || removed {
		t.Fatalf("dirty outer worktree was removed: %t, %v", removed, err)
	}
	if _, err := os.Stat(nested2); err != nil {
		t.Fatalf("nested2 was partially deleted despite dirty outer worktree: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "outer-dirty.txt")); err != nil {
		t.Fatal(err)
	}

	// Lock the shallower nested worktree while the deeper one is clean, merged, and unlocked ->
	// locked nested worktree must block removal without deleting nested2.
	nested1GitDir := resolveWorktreeGitDir(t, nested1)
	writeGitRef(t, nested1GitDir, "locked", "")
	if removed, err := factory.Remove(ctx, "thr_nested", RemoveOptions{Merged: true}); err != nil || removed {
		t.Fatalf("locked nested worktree must block Remove: %t, %v", removed, err)
	}
	if _, err := os.Stat(nested2); err != nil {
		t.Fatalf("deeper unlocked nested worktree was partially deleted: %v", err)
	}
	removeGitRef(t, nested1GitDir, "locked")

	// Now outer and both nested worktrees are clean, unlocked, and merged -> deepest-first removal succeeds with no dangling registrations!
	if removed, err := factory.Remove(ctx, "thr_nested", RemoveOptions{Merged: true}); err != nil || !removed {
		t.Fatalf("clean merged nested worktrees should be removed: %t, %v", removed, err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("outer worktree still exists: %v", err)
	}
	assertSingleWorktreeFast(t, host1)
	assertSingleWorktreeFast(t, host2)
}

func TestRemoveWorktreeDiscardCleansDirtyNestedWorktreeWithoutDangling(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	outer, _ := initGit(t)
	writeGitExclude(t, outer, "nested/\n")
	hostDir, hostGit := initGit(t)

	factory := newFactory(t, FactoryOptions{Path: outer, DataDir: t.TempDir(), DisableClone: true})
	ws, err := factory.Open(ctx, OpenRequest{ThreadID: "thr_discard"})
	if err != nil {
		t.Fatal(err)
	}
	dir := ws.Info().Label
	ws.Close()

	nested := filepath.Join(dir, "nested", "dirty-wt")
	gitOK(t, hostGit, "worktree", "add", "-qb", "feat/dirty", nested)
	writeGit(t, nested, "uncommitted.txt", "dirty\n")

	if removed, err := factory.Remove(ctx, "thr_discard", RemoveOptions{Discard: true}); err != nil || !removed {
		t.Fatalf("discard remove failed: %t, %v", removed, err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("outer worktree still exists after discard: %v", err)
	}
	assertSingleWorktreeFast(t, hostDir)
	// Branch in host repo stays preserved.
	if sha := readRefSHA(t, filepath.Join(hostDir, ".git"), "refs/heads/feat/dirty"); sha == "" {
		t.Fatal("host branch lost")
	}
}

func TestInspectEmbeddedRepoCustomDefaultBranchAndRemote(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	t.Run("linked worktree on develop remote without origin HEAD protects pushed feature branch", func(t *testing.T) {
		t.Parallel()
		hostDir, hostGit := initGit(t)
		hostGitDir := filepath.Join(hostDir, ".git")
		baseSHA := readHeadSHA(t, hostDir)
		writeGitRef(t, hostGitDir, "refs/heads/develop", baseSHA)
		writeGitRef(t, hostGitDir, "HEAD", "ref: refs/heads/develop")
		removeGitRef(t, hostGitDir, "refs/heads/main")
		writeGitRef(t, hostGitDir, "refs/remotes/origin/develop", baseSHA)
		appendGitConfig(t, hostGitDir, "\n[remote \"origin\"]\n\turl = file:///nonexistent.git\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n[branch \"develop\"]\n\tremote = origin\n\tmerge = refs/heads/develop\n")

		workspace := t.TempDir()
		nested := filepath.Join(workspace, "sub-wt")
		gitOK(t, hostGit, "worktree", "add", "-qb", "feat/work", nested)
		nestedGit := NewGit(nested, "file")
		writeGit(t, nested, "feat.txt", "feature work\n")
		gitCommitOnly(t, nestedGit, "feature commit")
		featSHA := readHeadSHA(t, nested)
		writeGitRef(t, hostGitDir, "refs/remotes/origin/feat/work", featSHA)
		appendGitConfig(t, hostGitDir, "\n[branch \"feat/work\"]\n\tremote = origin\n\tmerge = refs/heads/feat/work\n")

		repos, err := DiscoverEmbeddedRepos(ctx, workspace)
		if err != nil || len(repos) != 1 {
			t.Fatalf("DiscoverEmbeddedRepos = %+v, %v", repos, err)
		}
		status, err := InspectEmbeddedRepo(ctx, repos[0])
		if err != nil {
			t.Fatal(err)
		}
		if !status.Unmerged {
			t.Fatalf("pushed unmerged feat/work must be Unmerged: %+v", status)
		}

		// Merge feat/work on remote develop without fast-forwarding local host develop.
		writeGitRef(t, hostGitDir, "refs/remotes/origin/develop", featSHA)
		status, err = InspectEmbeddedRepo(ctx, repos[0])
		if err != nil {
			t.Fatal(err)
		}
		if status.Dirty || status.Unmerged || status.Unpushed {
			t.Fatalf("merged feat/work on origin/develop should be clean & merged: %+v", status)
		}
	})

	t.Run("standalone clone without origin HEAD protects pushed feature branch on develop", func(t *testing.T) {
		t.Parallel()
		workspace := t.TempDir()
		repoDir := filepath.Join(workspace, "sub-standalone")
		copyTree(t, ensureBaseGitTemplate(t), repoDir)
		repoGit := NewGit(repoDir, "file")
		gitDir := filepath.Join(repoDir, ".git")
		baseSHA := readHeadSHA(t, repoDir)

		// Only refs/remotes/origin/develop exists (no local develop, no origin/HEAD).
		removeGitRef(t, gitDir, "refs/heads/main")
		writeGitRef(t, gitDir, "refs/heads/feat/work", baseSHA)
		writeGitRef(t, gitDir, "HEAD", "ref: refs/heads/feat/work")
		writeGitRef(t, gitDir, "refs/remotes/origin/develop", baseSHA)
		writeGit(t, repoDir, "feat.txt", "feature\n")
		gitCommitOnly(t, repoGit, "feature commit")
		featSHA := readHeadSHA(t, repoDir)
		writeGitRef(t, gitDir, "refs/remotes/origin/feat/work", featSHA)
		appendGitConfig(t, gitDir, "\n[remote \"origin\"]\n\turl = file:///nonexistent.git\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n[branch \"feat/work\"]\n\tremote = origin\n\tmerge = refs/heads/feat/work\n")

		repos, err := DiscoverEmbeddedRepos(ctx, workspace)
		if err != nil || len(repos) != 1 {
			t.Fatalf("DiscoverEmbeddedRepos = %+v, %v", repos, err)
		}
		status, err := InspectEmbeddedRepo(ctx, repos[0])
		if err != nil {
			t.Fatal(err)
		}
		if status.Unpushed || !status.Unmerged {
			t.Fatalf("expected pushed but unmerged feat/work when only origin/develop exists, got %+v", status)
		}
	})

	t.Run("non-origin remote upstream checks upstream/main without HEAD", func(t *testing.T) {
		t.Parallel()
		workspace := t.TempDir()
		repoDir := filepath.Join(workspace, "sub-upstream")
		copyTree(t, ensureBaseGitTemplate(t), repoDir)
		repoGit := NewGit(repoDir, "file")
		gitDir := filepath.Join(repoDir, ".git")
		baseSHA := readHeadSHA(t, repoDir)

		writeGitRef(t, gitDir, "refs/remotes/upstream/main", baseSHA)
		writeGitRef(t, gitDir, "refs/heads/feat/pr", baseSHA)
		writeGitRef(t, gitDir, "HEAD", "ref: refs/heads/feat/pr")
		writeGit(t, repoDir, "pr.txt", "pr\n")
		gitCommitOnly(t, repoGit, "pr commit")
		prSHA := readHeadSHA(t, repoDir)
		writeGitRef(t, gitDir, "refs/remotes/upstream/feat/pr", prSHA)
		appendGitConfig(t, gitDir, "\n[remote \"upstream\"]\n\turl = file:///nonexistent.git\n\tfetch = +refs/heads/*:refs/remotes/upstream/*\n[branch \"feat/pr\"]\n\tremote = upstream\n\tmerge = refs/heads/feat/pr\n")

		repos, err := DiscoverEmbeddedRepos(ctx, workspace)
		if err != nil || len(repos) != 1 {
			t.Fatalf("DiscoverEmbeddedRepos = %+v, %v", repos, err)
		}
		status, err := InspectEmbeddedRepo(ctx, repos[0])
		if err != nil {
			t.Fatal(err)
		}
		if !status.Unmerged {
			t.Fatalf("unmerged feat/pr on upstream remote must be Unmerged: %+v", status)
		}

		// Merge feat/pr into upstream/main.
		writeGitRef(t, gitDir, "refs/remotes/upstream/main", prSHA)
		status, err = InspectEmbeddedRepo(ctx, repos[0])
		if err != nil {
			t.Fatal(err)
		}
		if status.Dirty || status.Unpushed || status.Unmerged {
			t.Fatalf("merged feat/pr on upstream/main should be merged: %+v", status)
		}
	})

	t.Run("pure single-branch readonly clone without origin HEAD is considered merged", func(t *testing.T) {
		t.Parallel()
		workspace := t.TempDir()
		cloneDir := filepath.Join(workspace, "readonly-trunk")
		copyTree(t, ensureBaseGitTemplate(t), cloneDir)
		gitDir := filepath.Join(cloneDir, ".git")
		baseSHA := readHeadSHA(t, cloneDir)
		removeGitRef(t, gitDir, "refs/heads/main")
		writeGitRef(t, gitDir, "refs/heads/trunk", baseSHA)
		writeGitRef(t, gitDir, "HEAD", "ref: refs/heads/trunk")
		writeGitRef(t, gitDir, "refs/remotes/origin/trunk", baseSHA)
		appendGitConfig(t, gitDir, "\n[remote \"origin\"]\n\turl = file:///nonexistent.git\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n[branch \"trunk\"]\n\tremote = origin\n\tmerge = refs/heads/trunk\n")

		repos, err := DiscoverEmbeddedRepos(ctx, workspace)
		if err != nil || len(repos) != 1 {
			t.Fatalf("DiscoverEmbeddedRepos = %+v, %v", repos, err)
		}
		status, err := InspectEmbeddedRepo(ctx, repos[0])
		if err != nil {
			t.Fatal(err)
		}
		if status.Dirty || status.Unpushed || status.Unmerged {
			t.Fatalf("clean single-branch clone should be landed, got %+v", status)
		}
	})
}

func TestInspectEmbeddedRepoCorruptedGitDirFailsClosed(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	outer, _ := initGit(t)
	writeGitExclude(t, outer, "broken/\n")

	factory := newFactory(t, FactoryOptions{Path: outer, DataDir: t.TempDir(), DisableClone: true})
	ws, err := factory.Open(ctx, OpenRequest{ThreadID: "thr_corrupt_gitdir"})
	if err != nil {
		t.Fatal(err)
	}
	info := ws.Info()
	dir := info.Label
	ws.Close()

	// Corrupted .git directory missing HEAD (git would walk up to outer worktree's .git).
	missingHead := filepath.Join(dir, "broken", "missing-head", ".git")
	if err := os.MkdirAll(filepath.Join(missingHead, "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(missingHead, "refs"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Corrupted .git directory missing objects/.
	missingObjects := filepath.Join(dir, "broken", "missing-objects", ".git")
	if err := os.MkdirAll(filepath.Join(missingObjects, "refs"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeGit(t, filepath.Dir(missingObjects), filepath.Join(".git", "HEAD"), "ref: refs/heads/main\n")

	repos, err := DiscoverEmbeddedRepos(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 2 {
		t.Fatalf("expected 2 broken repos, got %+v", repos)
	}
	for _, repo := range repos {
		status, err := InspectEmbeddedRepo(ctx, repo)
		if err != nil {
			t.Fatal(err)
		}
		if !status.Dirty || !status.Unmerged || !status.Unpushed {
			t.Fatalf("corrupted embedded .git dir %s must fail closed, got %+v", repo.RelPath, status)
		}
	}

	if merged, err := factory.Merged(ctx, sdk.ThreadBranch{ThreadID: "thr_corrupt_gitdir", Branch: *info.Branch}, *info.Base); err != nil || merged {
		t.Fatalf("corrupted embedded .git dir must block Merged: %t, %v", merged, err)
	}
	if removed, err := factory.Remove(ctx, "thr_corrupt_gitdir", RemoveOptions{Merged: true}); err != nil || removed {
		t.Fatalf("corrupted embedded .git dir must block Remove: %t, %v", removed, err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("outer worktree was deleted despite corrupted embedded .git dir: %v", err)
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

func TestDiscoverAndInspectEmbeddedSubmodule(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	remoteBare, orphanSHA := initOrphanSubmoduleRemote(t)
	remoteURL := fileURL(remoteBare)

	// Outer umbrella repo with a direct initialized submodule and an ignored nested worktree containing a submodule.
	wt := filepath.Join(shortTempDir(t), "wt")
	cloneOrphanSubmoduleRepo(t, remoteBare, wt)
	writeGitExclude(t, wt, "subrepos/\nnested-worktrees/\n")
	initOrphanSubmoduleFast(t, wt, "vendor-bundle", remoteURL)

	// External host clone + nested linked worktree whose submodule object store lies inside wt.
	pkgHost := filepath.Join(t.TempDir(), "pkg-ui-host")
	pkgHostGit := cloneOrphanSubmoduleRepo(t, remoteBare, pkgHost)

	nestedWT := filepath.Join(wt, "nested-worktrees", "feat-split", "subrepos", "pkg-ui")
	if err := os.MkdirAll(filepath.Dir(nestedWT), 0o755); err != nil {
		t.Fatal(err)
	}
	gitOK(t, pkgHostGit, "worktree", "add", "-qb", "feat/split", nestedWT)
	initOrphanSubmoduleFast(t, nestedWT, "vendor-bundle", remoteURL)

	// Fake gitfile not registered as a parent gitlink must stay IsSubmodule()==false.
	fakeModDir := filepath.Join(t.TempDir(), "modules", "fake")
	if err := os.MkdirAll(fakeModDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeSub := filepath.Join(wt, "subrepos", "fake-gitfile")
	writeGit(t, fakeSub, ".git", "gitdir: "+filepath.ToSlash(fakeModDir)+"\n")

	repos, err := DiscoverEmbeddedRepos(ctx, wt)
	if err != nil {
		t.Fatal(err)
	}
	byRel := map[string]EmbeddedRepo{}
	for _, r := range repos {
		byRel[r.RelPath] = r
	}

	directSub, ok := byRel["vendor-bundle"]
	if !ok {
		t.Fatalf("missing vendor-bundle in %+v", repos)
	}
	if !directSub.IsSubmodule() || directSub.Kind != EmbeddedGitFile || !directSub.ObjectStoreInsideWorktree ||
		directSub.SubmodulePath != "vendor-bundle" || worktreePathKey(directSub.SuperprojectPath) != worktreePathKey(wt) {
		t.Fatalf("unexpected direct-sub metadata: %+v", directSub)
	}
	resolvedDirectSub, err := resolveEmbeddedRepoPath(ctx, wt, "vendor-bundle")
	if err != nil {
		t.Fatalf("resolveEmbeddedRepoPath(vendor-bundle): %v", err)
	}
	if !resolvedDirectSub.IsSubmodule() || !resolvedDirectSub.ObjectStoreInsideWorktree {
		t.Fatalf("resolveEmbeddedRepoPath must mark direct submodule ObjectStoreInsideWorktree=true: %+v", resolvedDirectSub)
	}

	nestedSubRel := "nested-worktrees/feat-split/subrepos/pkg-ui/vendor-bundle"
	nestedSub, ok := byRel[nestedSubRel]
	if !ok {
		t.Fatalf("missing %s in %+v", nestedSubRel, repos)
	}
	if !nestedSub.IsSubmodule() || nestedSub.Kind != EmbeddedGitFile || !nestedSub.ObjectStoreInsideWorktree ||
		nestedSub.SubmodulePath != "vendor-bundle" || worktreePathKey(nestedSub.SuperprojectPath) != worktreePathKey(nestedWT) {
		t.Fatalf("unexpected nested submodule metadata: %+v", nestedSub)
	}

	fakeRepo, ok := byRel["subrepos/fake-gitfile"]
	if !ok || fakeRepo.Kind != EmbeddedGitFile || fakeRepo.IsSubmodule() {
		t.Fatalf("fake gitfile must stay non-submodule EmbeddedGitFile: %+v", fakeRepo)
	}

	nestedSubGit := NewGit(nestedSub.Path, "file")

	// 1. Clean detached HEAD aligned with parent gitlink -> landed, even if remote tracking ref for orphan branch is removed.
	removeGitRef(t, nestedSub.GitDir, "refs/remotes/origin/vendor-orphan")
	st, err := InspectEmbeddedRepo(ctx, nestedSub)
	if err != nil || st.Dirty || st.Unmerged || st.Unpushed {
		t.Fatalf("clean aligned detached submodule should be landed, got %+v, err=%v", st, err)
	}

	// 2. Diverged detached commit inside submodule -> Dirty + Unmerged.
	writeGit(t, nestedSub.Path, "bundle.js", "export const version = 3;\n")
	gitCommitTracked(t, nestedSubGit, "diverged detached commit")
	divergedSHA := readHeadSHA(t, nestedSub.Path)
	if divergedSHA == "" || divergedSHA == orphanSHA {
		t.Fatalf("expected new diverged commit, got %s", divergedSHA)
	}
	st, err = InspectEmbeddedRepo(ctx, nestedSub)
	if err != nil || !st.Dirty || !st.Unmerged {
		t.Fatalf("diverged detached submodule should be Dirty+Unmerged: %+v, err=%v", st, err)
	}

	// 3. Named branch in submodule ahead of .gitmodules branch (including local-only merge into configured branch) -> Unmerged until merged into origin/vendor-orphan.
	writeGitRef(t, nestedSub.GitDir, "refs/heads/feat/sub-branch", divergedSHA)
	writeGitRef(t, nestedSub.GitDir, "HEAD", "ref: refs/heads/feat/sub-branch")
	writeGitRef(t, nestedSub.GitDir, "refs/remotes/origin/feat/sub-branch", divergedSHA)
	writeGitRef(t, nestedSub.GitDir, "refs/remotes/origin/vendor-orphan", orphanSHA)
	writeGitRef(t, nestedSub.GitDir, "refs/heads/vendor-orphan", divergedSHA)
	appendGitConfig(t, nestedSub.GitDir, "\n[branch \"feat/sub-branch\"]\n\tremote = origin\n\tmerge = refs/heads/feat/sub-branch\n")
	st, err = InspectEmbeddedRepo(ctx, nestedSub)
	if err != nil || st.Dirty || st.Unpushed || !st.Unmerged {
		t.Fatalf("pushed unmerged submodule branch must remain Unmerged even with local-only merge: %+v, err=%v", st, err)
	}
	removeGitRef(t, nestedSub.GitDir, "refs/heads/vendor-orphan")

	// 4. Merged into origin/vendor-orphan -> landed; stash inside submodule -> Unpushed + Unmerged.
	writeGitRef(t, nestedSub.GitDir, "refs/remotes/origin/vendor-orphan", divergedSHA)
	st, err = InspectEmbeddedRepo(ctx, nestedSub)
	if err != nil || st.Dirty || st.Unpushed || st.Unmerged {
		t.Fatalf("submodule branch merged into configured .gitmodules branch should be landed: %+v, err=%v", st, err)
	}

	writeGitRef(t, nestedSub.GitDir, "refs/stash", divergedSHA)
	st, err = InspectEmbeddedRepo(ctx, nestedSub)
	if err != nil || st.Dirty || !st.Unpushed || !st.Unmerged {
		t.Fatalf("stash in submodule must be protected: %+v, err=%v", st, err)
	}
}

func TestRemoveWorktreeWithCleanAndDirtySubmodule(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	t.Run("nested worktree with initialized orphan submodule preserves host config and respects guards", func(t *testing.T) {
		t.Parallel()
		remoteBare, orphanSHA := initOrphanSubmoduleRemote(t)
		remoteURL := fileURL(remoteBare)

		// External host repo cloned from remoteBare with initialized submodule config.
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

		outer, _ := initGit(t)
		writeGitExclude(t, outer, "nested-worktrees/\n")

		factory := newFactory(t, FactoryOptions{Path: outer, DataDir: shortTempDir(t), DisableClone: true})
		ws, err := factory.Open(ctx, OpenRequest{ThreadID: "thr_nested_submod"})
		if err != nil {
			t.Fatal(err)
		}
		dir := ws.Info().Label
		ws.Close()

		nestedWT := filepath.Join(dir, "nested-worktrees", "feat-clean", "pkg")
		if err := os.MkdirAll(filepath.Dir(nestedWT), 0o755); err != nil {
			t.Fatal(err)
		}
		gitOK(t, hostGit, "worktree", "add", "-qb", "feat/clean", nestedWT)
		nestedGit := NewGit(nestedWT, "file")
		subDir := filepath.Join(nestedWT, "vendor-bundle")
		subGit := initOrphanSubmoduleFast(t, nestedWT, "vendor-bundle", remoteURL)

		// Verify external-host nested worktree keeps ObjectStoreInsideWorktree==false while
		// its per-worktree submodule (under host/.git/worktrees/<wt>/modules/vendor-bundle)
		// is recognized with ObjectStoreInsideWorktree==true.
		const nestedRel = "nested-worktrees/feat-clean/pkg"
		const nestedSubRel = "nested-worktrees/feat-clean/pkg/vendor-bundle"
		discovered, err := DiscoverEmbeddedRepos(ctx, dir)
		if err != nil {
			t.Fatal(err)
		}
		byRel := map[string]EmbeddedRepo{}
		for _, r := range discovered {
			byRel[r.RelPath] = r
		}
		if r := byRel[nestedRel]; r.Kind != EmbeddedWorktree || r.ObjectStoreInsideWorktree {
			t.Fatalf("external-host nested worktree must keep ObjectStoreInsideWorktree=false: %+v", r)
		}
		nestedSubRepo := byRel[nestedSubRel]
		if !nestedSubRepo.IsSubmodule() || !nestedSubRepo.ObjectStoreInsideWorktree {
			t.Fatalf("nested worktree submodule must have ObjectStoreInsideWorktree=true: %+v", nestedSubRepo)
		}

		// 1. Dirty submodule blocks removal.
		writeGit(t, subDir, "bundle.js", "dirty\n")
		if removed, err := factory.Remove(ctx, "thr_nested_submod", RemoveOptions{Merged: true}); err != nil || removed {
			t.Fatalf("dirty submodule must block Remove: removed=%t err=%v", removed, err)
		}

		// 2. Named-branch unpushed commit in submodule with updated parent gitlink (alignedSubmodule==true)
		// must still report Unpushed+Unmerged and block non-discard removals.
		writeGitRef(t, nestedSubRepo.GitDir, "refs/heads/feat/local-merge", orphanSHA)
		writeGitRef(t, nestedSubRepo.GitDir, "HEAD", "ref: refs/heads/feat/local-merge")
		writeGit(t, subDir, "bundle.js", "new commit\n")
		gitCommitTracked(t, subGit, "diverge from gitlink")
		divergedSHA := readHeadSHA(t, subDir)
		gitCommitTracked(t, nestedGit, "bump submodule gitlink")
		if removed, err := factory.Remove(ctx, "thr_nested_submod", RemoveOptions{Merged: true}); err != nil || removed {
			t.Fatalf("unpushed aligned submodule commit must block merged Remove: removed=%t err=%v", removed, err)
		}

		// 3. Mark submodule commit pushed/landed and nested worktree merged -> Remove succeeds without mutating host config!
		writeGitRef(t, nestedSubRepo.GitDir, "refs/remotes/origin/vendor-orphan", divergedSHA)
		writeGitRef(t, nestedSubRepo.GitDir, "HEAD", divergedSHA)
		removeGitRef(t, nestedSubRepo.GitDir, "refs/heads/feat/local-merge")
		writeGitRef(t, filepath.Join(hostDir, ".git"), "refs/heads/main", readHeadSHA(t, nestedWT))
		if removed, err := factory.Remove(ctx, "thr_nested_submod", RemoveOptions{Merged: true}); err != nil || !removed {
			t.Fatalf("clean aligned submodule should be removed on merged: %t, %v", removed, err)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
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
	})

	t.Run("outer worktree with direct initialized submodule guards dirty files and lock before single force", func(t *testing.T) {
		t.Parallel()
		remoteBare, _ := initOrphanSubmoduleRemote(t)
		remoteURL := fileURL(remoteBare)

		outer := filepath.Join(t.TempDir(), "outer-submod")
		cloneOrphanSubmoduleRepo(t, remoteBare, outer)

		factory := newFactory(t, FactoryOptions{Path: outer, DataDir: shortTempDir(t), DisableClone: true})
		ws, err := factory.Open(ctx, OpenRequest{ThreadID: "thr_outer_submod"})
		if err != nil {
			t.Fatal(err)
		}
		dir := ws.Info().Label
		ws.Close()

		initOrphanSubmoduleFast(t, dir, "vendor-bundle", remoteURL)

		// 1. Dirty file in outer worktree must block removal even though submodule is clean.
		dirtyFile := filepath.Join(dir, "keep-me.txt")
		writeGit(t, dir, "keep-me.txt", "do not delete\n")
		if removed, err := factory.Remove(ctx, "thr_outer_submod", RemoveOptions{Merged: true}); err != nil || removed {
			t.Fatalf("dirty outer worktree with clean submodule must block Remove: removed=%t err=%v", removed, err)
		}
		if _, err := os.Stat(dirtyFile); err != nil {
			t.Fatalf("dirty outer file was deleted: %v", err)
		}
		if err := os.Remove(dirtyFile); err != nil {
			t.Fatal(err)
		}

		// 2. Locked outer worktree must block removal.
		outerWTGitDir := resolveWorktreeGitDir(t, dir)
		writeGitRef(t, outerWTGitDir, "locked", "")
		if removed, err := factory.Remove(ctx, "thr_outer_submod", RemoveOptions{Merged: true}); err != nil || removed {
			t.Fatalf("locked outer worktree with clean submodule must block Remove: removed=%t err=%v", removed, err)
		}
		removeGitRef(t, outerWTGitDir, "locked")

		// 3. Clean outer worktree with clean aligned submodule -> Remove succeeds cleanly.
		if removed, err := factory.Remove(ctx, "thr_outer_submod", RemoveOptions{Merged: true}); err != nil || !removed {
			t.Fatalf("clean outer worktree with clean submodule should be removed: removed=%t err=%v", removed, err)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("outer worktree still exists: %v", err)
		}
	})
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

	// 1. Real on-disk submodule with bracket path (common in frontend monorepos).
	outer, outerGit := initGit(t)
	subSHA := readHeadSHA(t, outer)
	const bracketPath = "packages/[pkg]"
	addSubmoduleFast(t, outer, outerGit, bracketPath, "https://example.invalid/pkg.git", "main", subSHA)

	// Materialize the submodule module gitdir inside outer/.git/modules/<bracketPath>.
	modDir := filepath.Join(outer, ".git", "modules", filepath.FromSlash(bracketPath))
	subDir := filepath.Join(outer, filepath.FromSlash(bracketPath))
	copyTree(t, filepath.Join(ensureBaseGitTemplate(t), ".git"), modDir)
	writeGit(t, subDir, "file.txt", "base\n")
	writeGit(t, subDir, ".git", "gitdir: "+filepath.ToSlash(modDir)+"\n")

	if got := readSubmoduleHeadGitlink(ctx, outer, bracketPath); got != subSHA {
		t.Fatalf("readSubmoduleHeadGitlink(%q) = %q, want %q", bracketPath, got, subSHA)
	}
	if got := readSubmoduleIndexGitlink(ctx, outer, bracketPath); got != subSHA {
		t.Fatalf("readSubmoduleIndexGitlink(%q) = %q, want %q", bracketPath, got, subSHA)
	}
	if got := readSubmoduleConfiguredBranch(ctx, outer, bracketPath); got != "main" {
		t.Fatalf("readSubmoduleConfiguredBranch(%q) = %q, want main", bracketPath, got)
	}
	repos, err := DiscoverEmbeddedRepos(ctx, outer)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || !repos[0].IsSubmodule() || repos[0].SubmodulePath != bracketPath || !repos[0].ObjectStoreInsideWorktree {
		t.Fatalf("expected bracket submodule to be discovered as submodule with ObjectStoreInsideWorktree=true, got %+v", repos)
	}

	// 2. Pathspec-magic prefixes (":" / ":(bad)") in git tree & index must match literally
	// instead of being parsed as Git pathspec magic.
	magicRepo, magicGit := initGit(t)
	baseSHA := readHeadSHA(t, magicRepo)
	for _, subPath := range []string{":colon-sub", ":(bad)sub"} {
		gitOK(t, magicGit, "-c", "core.protectNTFS=false", "update-index", "--add", "--cacheinfo", "160000,"+baseSHA+","+subPath)
	}
	gitOK(t, magicGit, "-c", "core.protectNTFS=false", "commit", "-q", "-m", "add pathspec-magic gitlinks")

	for _, subPath := range []string{":colon-sub", ":(bad)sub"} {
		if got := readSubmoduleIndexGitlink(ctx, magicRepo, subPath); got != baseSHA {
			t.Fatalf("readSubmoduleIndexGitlink(%q) = %q, want %q", subPath, got, baseSHA)
		}
		if got := readSubmoduleHeadGitlink(ctx, magicRepo, subPath); got != baseSHA {
			t.Fatalf("readSubmoduleHeadGitlink(%q) = %q, want %q", subPath, got, baseSHA)
		}
	}
}
