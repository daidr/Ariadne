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
	bare := filepath.Join(t.TempDir(), "origin.git")
	seedRoot, seedGit := initGit(t)
	gitOK(t, seedGit, "clone", "--bare", seedRoot, bare)
	if err := os.MkdirAll(filepath.Dir(cloneDir), 0755); err != nil {
		t.Fatal(err)
	}
	gitOK(t, NewGit(filepath.Dir(cloneDir), "file"), "clone", bare, cloneDir)
	cloneGit := NewGit(cloneDir, "file")
	gitOK(t, cloneGit, "config", "user.name", "Ariadne Test")
	gitOK(t, cloneGit, "config", "user.email", "test@example.invalid")
	return bare, NewGit(bare, "file"), cloneGit
}

func registeredWorktreeCount(t *testing.T, git GitRun) int {
	t.Helper()
	paths, err := registeredWorktrees(t.Context(), git)
	if err != nil {
		t.Fatal(err)
	}
	return len(paths)
}

func TestDiscoverEmbeddedReposKindsPruningAndSymlinks(t *testing.T) {
	t.Parallel()
	outer, outerGit := initGit(t)
	writeGit(t, outer, ".gitignore", "subrepos/\nnested/\nnode_modules/\nvendor/\n.pnpm-store/\n.cache/\n")
	gitCommit(t, outerGit, "ignore subdirs")

	wt := filepath.Join(t.TempDir(), "wt")
	gitOK(t, outerGit, "worktree", "add", "-qb", "ariadne/discover", wt)

	// 1. Standalone .git directory.
	standaloneDir := filepath.Join(wt, "subrepos", "app")
	initRemoteAndClone(t, standaloneDir)

	// 2. Nested linked worktree + deeper nested linked worktree inside it.
	host1, host1Git := initGit(t)
	writeGit(t, host1, ".gitignore", "deep/\n")
	gitCommit(t, host1Git, "ignore deep")
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
	writeGit(t, outer, ".gitignore", "forged/\n")
	gitCommit(t, outerGit, "ignore forged")
	wt := filepath.Join(t.TempDir(), "wt")
	gitOK(t, outerGit, "worktree", "add", "-qb", "ariadne/forged", wt)

	// External host and a real worktree outside wt.
	_, hostGit := initGit(t)
	outsideWT := filepath.Join(t.TempDir(), "important-outside")
	gitOK(t, hostGit, "worktree", "add", "-qb", "feat/important", outsideWT)
	outsideGitDir := gitOK(t, NewGit(outsideWT, "file"), "rev-parse", "--path-format=absolute", "--git-dir")

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

		discoverOne := func() EmbeddedRepo {
			t.Helper()
			repos, err := DiscoverEmbeddedRepos(ctx, wt)
			if err != nil || len(repos) != 1 {
				t.Fatalf("discover: %+v, %v", repos, err)
			}
			return repos[0]
		}

		// Clean read-only clone at origin/main.
		st, err := InspectEmbeddedRepo(ctx, discoverOne())
		if err != nil || st.Dirty || st.Unmerged || st.Unpushed {
			t.Fatalf("clean clone: %+v, %v", st, err)
		}

		// Untracked file -> dirty.
		writeGit(t, repoDir, "untracked.txt", "draft\n")
		st, err = InspectEmbeddedRepo(ctx, discoverOne())
		if err != nil || !st.Dirty {
			t.Fatalf("untracked should be dirty: %+v, %v", st, err)
		}
		if err := os.Remove(filepath.Join(repoDir, "untracked.txt")); err != nil {
			t.Fatal(err)
		}

		// In-progress operation markers (MERGE_HEAD, rebase-merge, etc.) -> dirty.
		for _, marker := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD"} {
			p := filepath.Join(repoDir, ".git", marker)
			if err := os.WriteFile(p, []byte("deadbeef\n"), 0600); err != nil {
				t.Fatal(err)
			}
			st, err = InspectEmbeddedRepo(ctx, discoverOne())
			if err != nil || !st.Dirty {
				t.Fatalf("%s should be dirty: %+v, %v", marker, st, err)
			}
			_ = os.Remove(p)
		}
		rebaseDir := filepath.Join(repoDir, ".git", "rebase-merge")
		if err := os.Mkdir(rebaseDir, 0700); err != nil {
			t.Fatal(err)
		}
		st, err = InspectEmbeddedRepo(ctx, discoverOne())
		if err != nil || !st.Dirty {
			t.Fatalf("rebase-merge should be dirty: %+v, %v", st, err)
		}
		_ = os.Remove(rebaseDir)

		// Detached HEAD -> dirty and unmerged.
		gitOK(t, repoGit, "checkout", "--detach", "HEAD")
		st, err = InspectEmbeddedRepo(ctx, discoverOne())
		if err != nil || !st.Dirty || !st.Unmerged {
			t.Fatalf("detached HEAD should be dirty+unmerged: %+v, %v", st, err)
		}
		gitOK(t, repoGit, "checkout", "main")

		// Local unpushed commit on feature branch -> unpushed + unmerged.
		gitOK(t, repoGit, "checkout", "-qb", "feat/work")
		writeGit(t, repoDir, "feature.txt", "new feature\n")
		gitCommit(t, repoGit, "feat commit")
		st, err = InspectEmbeddedRepo(ctx, discoverOne())
		if err != nil || st.Dirty || !st.Unpushed || !st.Unmerged {
			t.Fatalf("unpushed feat commit: %+v, %v", st, err)
		}

		// Switching back to clean main while feat/work is still unpushed -> STILL unpushed!
		gitOK(t, repoGit, "checkout", "main")
		st, err = InspectEmbeddedRepo(ctx, discoverOne())
		if err != nil || st.Dirty || !st.Unpushed || !st.Unmerged {
			t.Fatalf("other unpushed branch must still be protected: %+v, %v", st, err)
		}

		// Merging feat/work into local main without pushing -> STILL unpushed!
		gitOK(t, repoGit, "merge", "--ff-only", "feat/work")
		st, err = InspectEmbeddedRepo(ctx, discoverOne())
		if err != nil || st.Dirty || !st.Unpushed || !st.Unmerged {
			t.Fatalf("merged locally without push must still be unpushed: %+v, %v", st, err)
		}

		// Reset local main back to origin/main, push feat/work to origin/feat/work (PR open, not merged to origin/main).
		gitOK(t, repoGit, "reset", "--hard", "origin/main")
		gitOK(t, repoGit, "checkout", "feat/work")
		gitOK(t, repoGit, "push", "-u", "origin", "feat/work")
		st, err = InspectEmbeddedRepo(ctx, discoverOne())
		if err != nil || st.Dirty || st.Unpushed || !st.Unmerged {
			t.Fatalf("pushed but unmerged feat should be Unpushed=false Unmerged=true: %+v, %v", st, err)
		}

		// Push feat/work into origin/main (landed) -> clean, pushed, merged.
		gitOK(t, repoGit, "push", "origin", "feat/work:main")
		gitOK(t, repoGit, "fetch", "origin")
		st, err = InspectEmbeddedRepo(ctx, discoverOne())
		if err != nil || st.Dirty || st.Unpushed || st.Unmerged {
			t.Fatalf("merged into origin/main should be landed: %+v, %v", st, err)
		}
	})

	t.Run("standalone local-only repo without remotes", func(t *testing.T) {
		t.Parallel()
		wt := t.TempDir()
		repoDir := filepath.Join(wt, "local-repo")
		if err := os.MkdirAll(repoDir, 0755); err != nil {
			t.Fatal(err)
		}
		repoGit := NewGit(repoDir, "file")
		gitOK(t, repoGit, "init", "-b", "main")
		gitOK(t, repoGit, "config", "user.name", "Ariadne Test")
		gitOK(t, repoGit, "config", "user.email", "test@example.invalid")
		writeGit(t, repoDir, "a.txt", "only local\n")
		gitCommit(t, repoGit, "local commit")

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
		gitOK(t, hostGit, "branch", "-m", "main", "develop")
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
		gitCommit(t, nestedGit, "svc change")
		st, err = InspectEmbeddedRepo(ctx, repos[0])
		if err != nil || st.Dirty || !st.Unmerged || st.Unpushed {
			t.Fatalf("unmerged nested worktree: %+v, %v", st, err)
		}

		// Merge into host's develop -> landed!
		gitOK(t, hostGit, "merge", "--ff-only", "wt/task")
		st, err = InspectEmbeddedRepo(ctx, repos[0])
		if err != nil || st.Dirty || st.Unmerged || st.Unpushed {
			t.Fatalf("merged nested worktree should be landed: %+v, %v", st, err)
		}
		_ = host
	})
}

func TestRemoveWorktreeSingleRepoWithIgnoredDistStillCleansOnMerged(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	root, git := initGit(t)
	writeGit(t, root, ".gitignore", "dist/\n.env\n")
	gitCommit(t, git, "gitignore")

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

	merged, err := factory.Merged(ctx, sdk.ThreadBranch{ThreadID: "thr_single", Branch: *info.Branch}, *info.Base)
	if err != nil || !merged {
		t.Fatalf("single repo with dist/ should be merged: %t, %v", merged, err)
	}
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
	outer, outerGit := initGit(t)
	writeGit(t, outer, ".gitignore", "subrepos/\n")
	gitCommit(t, outerGit, "ignore subrepos")

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

	// 1. Clean read-only clone -> Merged=true.
	merged, err := factory.Merged(ctx, sdk.ThreadBranch{ThreadID: "thr_standalone", Branch: *info.Branch}, *info.Base)
	if err != nil || !merged {
		t.Fatalf("clean read-only clone should be merged: %t, %v", merged, err)
	}

	// 2. Uncommitted change -> protected in Merged, Spare, and user modes.
	writeGit(t, subDir, "dirty.txt", "wip\n")
	if merged, err := factory.Merged(ctx, sdk.ThreadBranch{ThreadID: "thr_standalone", Branch: *info.Branch}, *info.Base); err != nil || merged {
		t.Fatalf("dirty embedded repo must not be merged: %t, %v", merged, err)
	}
	for _, opts := range []RemoveOptions{{Merged: true}, {Spare: true}, {}} {
		if removed, err := factory.Remove(ctx, "thr_standalone", opts); err != nil || removed {
			t.Fatalf("dirty embedded repo removed with opts=%+v: %t, %v", opts, removed, err)
		}
	}

	// 3. Committed locally without push -> still protected in Merged, Spare, and user modes.
	gitOK(t, subGit, "checkout", "-qb", "feat/local")
	gitCommit(t, subGit, "unpushed work")
	if merged, err := factory.Merged(ctx, sdk.ThreadBranch{ThreadID: "thr_standalone", Branch: *info.Branch}, *info.Base); err != nil || merged {
		t.Fatalf("unpushed embedded repo must not be merged: %t, %v", merged, err)
	}
	for _, opts := range []RemoveOptions{{Merged: true}, {Spare: true}, {}} {
		if removed, err := factory.Remove(ctx, "thr_standalone", opts); err != nil || removed {
			t.Fatalf("unpushed standalone repo removed with opts=%+v: %t, %v", opts, removed, err)
		}
	}

	// 4. Push to origin/feat/local (not merged into origin/main) -> protected in Merged, allowed in user remove.
	gitOK(t, subGit, "push", "-u", "origin", "feat/local")
	if merged, err := factory.Merged(ctx, sdk.ThreadBranch{ThreadID: "thr_standalone", Branch: *info.Branch}, *info.Base); err != nil || merged {
		t.Fatalf("unmerged pushed branch must not be merged: %t, %v", merged, err)
	}
	if removed, err := factory.Remove(ctx, "thr_standalone", RemoveOptions{Merged: true}); err != nil || removed {
		t.Fatalf("unmerged pushed branch removed on merged: %t, %v", removed, err)
	}

	// 5. Merge into origin/main and fetch -> Merged=true and removed cleanly!
	gitOK(t, subGit, "push", "origin", "feat/local:main")
	gitOK(t, subGit, "fetch", "origin")
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

	outer, outerGit := initGit(t)
	writeGit(t, outer, ".gitignore", "nested/\n")
	gitCommit(t, outerGit, "ignore nested")

	host1, host1Git := initGit(t)
	writeGit(t, host1, ".gitignore", "inner/\n")
	gitCommit(t, host1Git, "ignore inner")
	host2, host2Git := initGit(t)
	_ = host2

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
	gitCommit(t, NewGit(nested2, "file"), "deep commit")

	if merged, err := factory.Merged(ctx, sdk.ThreadBranch{ThreadID: "thr_nested", Branch: *info.Branch}, *info.Base); err != nil || merged {
		t.Fatalf("unmerged nested worktree must not be merged: %t, %v", merged, err)
	}
	if removed, err := factory.Remove(ctx, "thr_nested", RemoveOptions{Merged: true}); err != nil || removed {
		t.Fatalf("unmerged nested worktree was removed: %t, %v", removed, err)
	}

	// Merge deepest branch into host2 main, BUT make outer worktree dirty ->
	// removal must refuse WITHOUT partially deleting nested worktrees!
	gitOK(t, host2Git, "merge", "--ff-only", "feat/level2")
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

	// Now outer and both nested worktrees are clean and merged -> deepest-first removal succeeds with no dangling registrations!
	if merged, err := factory.Merged(ctx, sdk.ThreadBranch{ThreadID: "thr_nested", Branch: *info.Branch}, *info.Base); err != nil || !merged {
		t.Fatalf("clean merged nested worktrees should be merged: %t, %v", merged, err)
	}
	if removed, err := factory.Remove(ctx, "thr_nested", RemoveOptions{Merged: true}); err != nil || !removed {
		t.Fatalf("clean merged nested worktrees should be removed: %t, %v", removed, err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("outer worktree still exists: %v", err)
	}
	if got := registeredWorktreeCount(t, host1Git); got != 1 {
		t.Fatalf("host1 left dangling worktree registration: count=%d", got)
	}
	if got := registeredWorktreeCount(t, host2Git); got != 1 {
		t.Fatalf("host2 left dangling worktree registration: count=%d", got)
	}
}

func TestRemoveWorktreeDiscardCleansDirtyNestedWorktreeWithoutDangling(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	outer, outerGit := initGit(t)
	writeGit(t, outer, ".gitignore", "nested/\n")
	gitCommit(t, outerGit, "ignore nested")
	_, hostGit := initGit(t)

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
	if got := registeredWorktreeCount(t, hostGit); got != 1 {
		t.Fatalf("host left dangling worktree after discard: count=%d", got)
	}
	// Branch in host repo stays preserved.
	if sha := commitSHA(ctx, hostGit, "refs/heads/feat/dirty", ""); strings.TrimSpace(sha) == "" {
		t.Fatal("host branch lost")
	}
}

func TestInspectEmbeddedRepoCustomDefaultBranchAndRemote(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	t.Run("linked worktree on develop remote without origin HEAD protects pushed feature branch", func(t *testing.T) {
		bareRemote := t.TempDir()
		bareGit := NewGit(bareRemote, "file")
		gitOK(t, bareGit, "init", "--bare", "-b", "develop")

		hostDir := t.TempDir()
		hostGit := NewGit(hostDir, "file")
		gitOK(t, hostGit, "init", "-b", "develop")
		gitOK(t, hostGit, "config", "user.name", "Test")
		gitOK(t, hostGit, "config", "user.email", "test@example.com")
		writeGit(t, hostDir, "base.txt", "develop base\n")
		gitCommit(t, hostGit, "develop base")
		gitOK(t, hostGit, "remote", "add", "origin", bareRemote)
		gitOK(t, hostGit, "push", "-u", "origin", "develop")
		// Ensure refs/remotes/origin/HEAD is absent.
		_ = hostGit(ctx, []string{"symbolic-ref", "-d", "refs/remotes/origin/HEAD"}, GitOptions{})

		workspace := t.TempDir()
		nested := filepath.Join(workspace, "sub-wt")
		gitOK(t, hostGit, "worktree", "add", "-qb", "feat/work", nested)
		nestedGit := NewGit(nested, "file")
		writeGit(t, nested, "feat.txt", "feature work\n")
		gitCommit(t, nestedGit, "feature commit")
		gitOK(t, nestedGit, "push", "-u", "origin", "feat/work")

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

		// Merge feat/work on the remote develop branch and fetch without fast-forwarding local host develop.
		mergerDir := t.TempDir()
		gitOK(t, NewGit(mergerDir, "file"), "clone", "-b", "develop", bareRemote, mergerDir)
		mergerGit := NewGit(mergerDir, "file")
		gitOK(t, mergerGit, "config", "user.name", "Test")
		gitOK(t, mergerGit, "config", "user.email", "test@example.com")
		gitOK(t, mergerGit, "fetch", "origin", "feat/work")
		gitOK(t, mergerGit, "merge", "--ff-only", "FETCH_HEAD")
		gitOK(t, mergerGit, "push", "origin", "develop")
		gitOK(t, hostGit, "fetch", "origin")

		status, err = InspectEmbeddedRepo(ctx, repos[0])
		if err != nil {
			t.Fatal(err)
		}
		if status.Dirty || status.Unmerged || status.Unpushed {
			t.Fatalf("merged feat/work on origin/develop should be clean & merged: %+v", status)
		}
	})

	t.Run("standalone clone without origin HEAD protects pushed feature branch on develop", func(t *testing.T) {
		bareRemote := t.TempDir()
		gitOK(t, NewGit(bareRemote, "file"), "init", "--bare", "-b", "develop")

		workspace := t.TempDir()
		repoDir := filepath.Join(workspace, "sub-standalone")
		if err := os.MkdirAll(repoDir, 0o755); err != nil {
			t.Fatal(err)
		}
		repoGit := NewGit(repoDir, "file")
		gitOK(t, repoGit, "init", "-b", "develop")
		gitOK(t, repoGit, "config", "user.name", "Test")
		gitOK(t, repoGit, "config", "user.email", "test@example.com")
		writeGit(t, repoDir, "base.txt", "base\n")
		gitCommit(t, repoGit, "base")
		gitOK(t, repoGit, "remote", "add", "origin", bareRemote)
		gitOK(t, repoGit, "push", "-u", "origin", "develop")

		gitOK(t, repoGit, "checkout", "-b", "feat/work")
		writeGit(t, repoDir, "feat.txt", "feature\n")
		gitCommit(t, repoGit, "feature commit")
		gitOK(t, repoGit, "push", "-u", "origin", "feat/work")

		repos, err := DiscoverEmbeddedRepos(ctx, workspace)
		if err != nil || len(repos) != 1 {
			t.Fatalf("DiscoverEmbeddedRepos = %+v, %v", repos, err)
		}
		status, err := InspectEmbeddedRepo(ctx, repos[0])
		if err != nil {
			t.Fatal(err)
		}
		if status.Unpushed || !status.Unmerged {
			t.Fatalf("expected pushed but unmerged feat/work, got %+v", status)
		}

		// Even if local develop branch is deleted, refs/remotes/origin/develop still exists.
		gitOK(t, repoGit, "branch", "-D", "develop")
		status, err = InspectEmbeddedRepo(ctx, repos[0])
		if err != nil {
			t.Fatal(err)
		}
		if !status.Unmerged {
			t.Fatalf("expected Unmerged when origin/develop exists without origin/HEAD, got %+v", status)
		}
	})

	t.Run("non-origin remote upstream checks upstream/main without HEAD", func(t *testing.T) {
		bareRemote := t.TempDir()
		gitOK(t, NewGit(bareRemote, "file"), "init", "--bare", "-b", "main")

		workspace := t.TempDir()
		repoDir := filepath.Join(workspace, "sub-upstream")
		if err := os.MkdirAll(repoDir, 0o755); err != nil {
			t.Fatal(err)
		}
		repoGit := NewGit(repoDir, "file")
		gitOK(t, repoGit, "init", "-b", "main")
		gitOK(t, repoGit, "config", "user.name", "Test")
		gitOK(t, repoGit, "config", "user.email", "test@example.com")
		writeGit(t, repoDir, "base.txt", "base\n")
		gitCommit(t, repoGit, "base")
		gitOK(t, repoGit, "remote", "add", "upstream", bareRemote)
		gitOK(t, repoGit, "push", "-u", "upstream", "main")

		gitOK(t, repoGit, "checkout", "-b", "feat/pr")
		writeGit(t, repoDir, "pr.txt", "pr\n")
		gitCommit(t, repoGit, "pr commit")
		gitOK(t, repoGit, "push", "-u", "upstream", "feat/pr")

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

		// Merge feat/pr into upstream/main and fetch.
		gitOK(t, repoGit, "push", "upstream", "feat/pr:main")
		gitOK(t, repoGit, "fetch", "upstream")
		status, err = InspectEmbeddedRepo(ctx, repos[0])
		if err != nil {
			t.Fatal(err)
		}
		if status.Dirty || status.Unpushed || status.Unmerged {
			t.Fatalf("merged feat/pr on upstream/main should be merged: %+v", status)
		}
	})

	t.Run("pure single-branch readonly clone without origin HEAD is considered merged", func(t *testing.T) {
		bareRemote := t.TempDir()
		gitOK(t, NewGit(bareRemote, "file"), "init", "--bare", "-b", "trunk")

		seed := t.TempDir()
		seedGit := NewGit(seed, "file")
		gitOK(t, seedGit, "init", "-b", "trunk")
		gitOK(t, seedGit, "config", "user.name", "Test")
		gitOK(t, seedGit, "config", "user.email", "test@example.com")
		writeGit(t, seed, "readme.txt", "readonly\n")
		gitCommit(t, seedGit, "seed")
		gitOK(t, seedGit, "remote", "add", "origin", bareRemote)
		gitOK(t, seedGit, "push", "origin", "trunk")

		workspace := t.TempDir()
		cloneDir := filepath.Join(workspace, "readonly-trunk")
		gitOK(t, NewGit(workspace, "file"), "clone", "--single-branch", "-b", "trunk", bareRemote, cloneDir)
		cloneGit := NewGit(cloneDir, "file")
		_ = cloneGit(ctx, []string{"symbolic-ref", "-d", "refs/remotes/origin/HEAD"}, GitOptions{})

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

func TestRemoveWorktreeLockedNestedWorktreeNoPartialDelete(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	outer, outerGit := initGit(t)
	writeGit(t, outer, ".gitignore", "nested/\n")
	gitCommit(t, outerGit, "ignore nested")

	host1, host1Git := initGit(t)
	writeGit(t, host1, ".gitignore", "inner/\n")
	gitCommit(t, host1Git, "ignore inner")
	_, host2Git := initGit(t)

	factory := newFactory(t, FactoryOptions{Path: outer, DataDir: t.TempDir(), DisableClone: true})
	ws, err := factory.Open(ctx, OpenRequest{ThreadID: "thr_locked_nested"})
	if err != nil {
		t.Fatal(err)
	}
	info := ws.Info()
	dir := info.Label
	ws.Close()

	nested1 := filepath.Join(dir, "nested", "level1")
	gitOK(t, host1Git, "worktree", "add", "-qb", "feat/locked1", nested1)
	nested2 := filepath.Join(nested1, "inner", "level2")
	gitOK(t, host2Git, "worktree", "add", "-qb", "feat/unlocked2", nested2)

	// Lock the shallower nested worktree while the deeper one is clean, merged, and unlocked.
	gitOK(t, host1Git, "worktree", "lock", nested1)

	repos, err := DiscoverEmbeddedRepos(ctx, dir)
	if err != nil || len(repos) != 2 {
		t.Fatalf("DiscoverEmbeddedRepos = %+v, %v", repos, err)
	}
	var lockedStatus EmbeddedRepoStatus
	for _, repo := range repos {
		if repo.RelPath == "nested/level1" {
			lockedStatus, err = InspectEmbeddedRepo(ctx, repo)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if !lockedStatus.Dirty {
		t.Fatalf("locked nested worktree must be marked Dirty: %+v", lockedStatus)
	}

	if merged, err := factory.Merged(ctx, sdk.ThreadBranch{ThreadID: "thr_locked_nested", Branch: *info.Branch}, *info.Base); err != nil || merged {
		t.Fatalf("locked nested worktree must prevent Merged=true: %t, %v", merged, err)
	}
	if removed, err := factory.Remove(ctx, "thr_locked_nested", RemoveOptions{Merged: true}); err != nil || removed {
		t.Fatalf("locked nested worktree must block Remove: %t, %v", removed, err)
	}
	if _, err := os.Stat(nested2); err != nil {
		t.Fatalf("deeper unlocked nested worktree was partially deleted: %v", err)
	}
	if _, err := os.Stat(nested1); err != nil {
		t.Fatalf("locked nested worktree disappeared: %v", err)
	}

	// Unlock -> now clean merged removal succeeds completely without dangling worktrees.
	gitOK(t, host1Git, "worktree", "unlock", nested1)
	if merged, err := factory.Merged(ctx, sdk.ThreadBranch{ThreadID: "thr_locked_nested", Branch: *info.Branch}, *info.Base); err != nil || !merged {
		t.Fatalf("unlocked clean nested worktrees should be merged: %t, %v", merged, err)
	}
	if removed, err := factory.Remove(ctx, "thr_locked_nested", RemoveOptions{Merged: true}); err != nil || !removed {
		t.Fatalf("unlocked clean nested worktrees should be removed: %t, %v", removed, err)
	}
	if got := registeredWorktreeCount(t, host1Git); got != 1 {
		t.Fatalf("host1 left dangling worktree: count=%d", got)
	}
	if got := registeredWorktreeCount(t, host2Git); got != 1 {
		t.Fatalf("host2 left dangling worktree: count=%d", got)
	}
}

func TestInspectEmbeddedRepoCorruptedGitDirFailsClosed(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	outer, outerGit := initGit(t)
	writeGit(t, outer, ".gitignore", "broken/\n")
	gitCommit(t, outerGit, "ignore broken")

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
	ctx := t.Context()
	remoteBare = filepath.Join(t.TempDir(), "pkg-remote.git")
	if err := os.MkdirAll(remoteBare, 0o755); err != nil {
		t.Fatal(err)
	}
	gitOK(t, NewGit(remoteBare, "file"), "init", "--bare", "-q", "-b", "main")
	remoteURL := fileURL(remoteBare)

	// 1. Seed orphan branch "vendor-orphan" with its own independent root commit.
	orphanSeed := t.TempDir()
	orphanGit := NewGit(orphanSeed, "file")
	gitOK(t, orphanGit, "init", "-q", "-b", "vendor-orphan")
	gitOK(t, orphanGit, "config", "user.name", "Ariadne Test")
	gitOK(t, orphanGit, "config", "user.email", "test@example.invalid")
	writeGit(t, orphanSeed, "bundle.js", "export const version = 1;\n")
	writeGit(t, orphanSeed, "assets/info.txt", "orphan bundle\n")
	gitCommit(t, orphanGit, "initial orphan bundle")
	orphanSHA = commitSHA(ctx, orphanGit, "HEAD", "")
	if orphanSHA == "" {
		t.Fatal("failed to resolve orphan HEAD sha")
	}
	gitOK(t, orphanGit, "remote", "add", "origin", remoteURL)
	gitOK(t, orphanGit, "push", "-q", "origin", "vendor-orphan")

	// 2. Seed main branch (completely unrelated history) and attach vendor-bundle submodule.
	mainSeed := t.TempDir()
	mainGit := NewGit(mainSeed, "file")
	gitOK(t, mainGit, "init", "-q", "-b", "main")
	gitOK(t, mainGit, "config", "user.name", "Ariadne Test")
	gitOK(t, mainGit, "config", "user.email", "test@example.invalid")
	writeGit(t, mainSeed, "README.md", "# pkg\n")
	writeGit(t, mainSeed, "src/index.ts", "export const main = true;\n")
	writeGit(t, mainSeed, "src/util.ts", "export const util = 1;\n")
	gitCommit(t, mainGit, "initial main")
	gitOK(t, mainGit, "-c", "protocol.file.allow=always", "submodule", "add", "-q", "-b", "vendor-orphan", remoteURL, "vendor-bundle")
	gitCommit(t, mainGit, "add vendor-bundle orphan submodule")
	gitOK(t, mainGit, "remote", "add", "origin", remoteURL)
	gitOK(t, mainGit, "push", "-q", "origin", "main")

	return remoteBare, orphanSHA
}

func TestDiscoverAndInspectEmbeddedSubmodule(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	remoteBare, orphanSHA := initOrphanSubmoduleRemote(t)
	remoteURL := fileURL(remoteBare)

	// Outer umbrella repo with both a direct submodule and an ignored nested worktree containing a submodule.
	outer, outerGit := initGit(t)
	writeGit(t, outer, ".gitignore", "subrepos/\nnested-worktrees/\n")
	gitCommit(t, outerGit, "ignore subrepos and nested worktrees")
	gitOK(t, outerGit, "-c", "protocol.file.allow=always", "submodule", "add", "-q", "-b", "vendor-orphan", remoteURL, "direct-sub")
	gitCommit(t, outerGit, "add direct submodule")

	wt := filepath.Join(shortTempDir(t), "wt")
	gitOK(t, outerGit, "worktree", "add", "-qb", "ariadne/submod-inspect", wt)
	wtGit := NewGit(wt, "file")
	gitOK(t, wtGit, "-c", "protocol.file.allow=always", "submodule", "update", "--init", "-q", "direct-sub")

	// Standalone clone inside worktree + nested linked worktree whose submodule object store lies inside wt.
	pkgClone := filepath.Join(wt, "subrepos", "pkg-ui")
	if err := os.MkdirAll(filepath.Dir(pkgClone), 0o755); err != nil {
		t.Fatal(err)
	}
	gitOK(t, NewGit(filepath.Dir(pkgClone), "file"), "clone", "-q", remoteURL, pkgClone)
	pkgCloneGit := NewGit(pkgClone, "file")
	gitOK(t, pkgCloneGit, "config", "user.name", "Ariadne Test")
	gitOK(t, pkgCloneGit, "config", "user.email", "test@example.invalid")

	nestedWT := filepath.Join(wt, "nested-worktrees", "feat-split", "subrepos", "pkg-ui")
	if err := os.MkdirAll(filepath.Dir(nestedWT), 0o755); err != nil {
		t.Fatal(err)
	}
	gitOK(t, pkgCloneGit, "worktree", "add", "-qb", "feat/split", nestedWT)
	nestedGit := NewGit(nestedWT, "file")
	gitOK(t, nestedGit, "-c", "protocol.file.allow=always", "submodule", "update", "--init", "-q", "vendor-bundle")

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

	directSub, ok := byRel["direct-sub"]
	if !ok {
		t.Fatalf("missing direct-sub in %+v", repos)
	}
	if !directSub.IsSubmodule() || directSub.Kind != EmbeddedGitFile || directSub.SubmodulePath != "direct-sub" || worktreePathKey(directSub.SuperprojectPath) != worktreePathKey(wt) {
		t.Fatalf("unexpected direct-sub metadata: %+v", directSub)
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
	gitOK(t, nestedSubGit, "config", "user.name", "Ariadne Test")
	gitOK(t, nestedSubGit, "config", "user.email", "test@example.invalid")

	// 1. Clean detached HEAD aligned with parent gitlink -> landed, even if remote tracking ref for orphan branch is removed.
	_ = nestedSubGit(ctx, []string{"update-ref", "-d", "refs/remotes/origin/vendor-orphan"}, GitOptions{})
	st, err := InspectEmbeddedRepo(ctx, nestedSub)
	if err != nil || st.Dirty || st.Unmerged || st.Unpushed {
		t.Fatalf("clean aligned detached submodule should be landed, got %+v, err=%v", st, err)
	}

	// 2. Modified tracked file -> Dirty.
	writeGit(t, nestedSub.Path, "bundle.js", "export const version = 2;\n")
	st, err = InspectEmbeddedRepo(ctx, nestedSub)
	if err != nil || !st.Dirty {
		t.Fatalf("modified submodule should be Dirty: %+v, err=%v", st, err)
	}
	gitOK(t, nestedSubGit, "checkout", "--", "bundle.js")

	// 3. Untracked file -> Dirty.
	writeGit(t, nestedSub.Path, "untracked.js", "draft\n")
	st, err = InspectEmbeddedRepo(ctx, nestedSub)
	if err != nil || !st.Dirty {
		t.Fatalf("untracked file in submodule should be Dirty: %+v, err=%v", st, err)
	}
	if err := os.Remove(filepath.Join(nestedSub.Path, "untracked.js")); err != nil {
		t.Fatal(err)
	}

	// 4. Detached HEAD diverging from parent HEAD gitlink -> Dirty + Unmerged.
	writeGit(t, nestedSub.Path, "bundle.js", "export const version = 3;\n")
	gitCommit(t, nestedSubGit, "diverged detached commit")
	divergedSHA := commitSHA(ctx, nestedSubGit, "HEAD", "")
	if divergedSHA == "" || divergedSHA == orphanSHA {
		t.Fatalf("expected new diverged commit, got %s", divergedSHA)
	}
	st, err = InspectEmbeddedRepo(ctx, nestedSub)
	if err != nil || !st.Dirty || !st.Unmerged {
		t.Fatalf("diverged detached submodule should be Dirty+Unmerged: %+v, err=%v", st, err)
	}
	gitOK(t, nestedSubGit, "checkout", "--detach", "-q", orphanSHA)

	// 5. Unpushed local branch inside submodule (ObjectStoreInsideWorktree==true) -> Unpushed + Unmerged.
	gitOK(t, nestedSubGit, "branch", "local-wip", divergedSHA)
	st, err = InspectEmbeddedRepo(ctx, nestedSub)
	if err != nil || st.Dirty || !st.Unpushed || !st.Unmerged {
		t.Fatalf("unpushed local branch in submodule must be protected: %+v, err=%v", st, err)
	}
	gitOK(t, nestedSubGit, "branch", "-D", "local-wip")

	// 6. Stash inside submodule -> Unpushed + Unmerged.
	writeGit(t, nestedSub.Path, "bundle.js", "export const version = 4;\n")
	gitOK(t, nestedSubGit, "stash", "push", "-q", "-m", "wip")
	st, err = InspectEmbeddedRepo(ctx, nestedSub)
	if err != nil || st.Dirty || !st.Unpushed || !st.Unmerged {
		t.Fatalf("stash in submodule must be protected: %+v, err=%v", st, err)
	}
	gitOK(t, nestedSubGit, "stash", "drop", "-q")

	// 7. Named branch in submodule ahead of .gitmodules branch -> Unmerged until merged into origin/vendor-orphan.
	gitOK(t, nestedSubGit, "fetch", "-q", "origin")
	gitOK(t, nestedSubGit, "checkout", "-qb", "feat/sub-branch", divergedSHA)
	gitOK(t, nestedSubGit, "push", "-q", "-u", "origin", "feat/sub-branch")
	st, err = InspectEmbeddedRepo(ctx, nestedSub)
	if err != nil || st.Dirty || st.Unpushed || !st.Unmerged {
		t.Fatalf("pushed unmerged submodule branch must be Unmerged: %+v, err=%v", st, err)
	}
	gitOK(t, nestedSubGit, "push", "-q", "origin", "feat/sub-branch:vendor-orphan")
	gitOK(t, nestedSubGit, "fetch", "-q", "origin")
	st, err = InspectEmbeddedRepo(ctx, nestedSub)
	if err != nil || st.Dirty || st.Unpushed || st.Unmerged {
		t.Fatalf("submodule branch merged into configured .gitmodules branch should be landed: %+v, err=%v", st, err)
	}
}

func TestRemoveWorktreeWithCleanAndDirtySubmodule(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	t.Run("nested worktree with initialized orphan submodule preserves host config and respects guards", func(t *testing.T) {
		t.Parallel()
		remoteBare, orphanSHA := initOrphanSubmoduleRemote(t)
		remoteURL := fileURL(remoteBare)

		// External host repo cloned from remoteBare.
		hostDir := filepath.Join(t.TempDir(), "host-pkg")
		gitOK(t, NewGit(filepath.Dir(hostDir), "file"), "clone", "-q", remoteURL, hostDir)
		hostGit := NewGit(hostDir, "file")
		gitOK(t, hostGit, "config", "user.name", "Ariadne Test")
		gitOK(t, hostGit, "config", "user.email", "test@example.invalid")
		gitOK(t, hostGit, "-c", "protocol.file.allow=always", "submodule", "update", "--init", "-q", "vendor-bundle")

		hostConfigPath := filepath.Join(hostDir, ".git", "config")
		hostConfigBefore, err := os.ReadFile(hostConfigPath)
		if err != nil {
			t.Fatal(err)
		}

		outer, outerGit := initGit(t)
		writeGit(t, outer, ".gitignore", "nested-worktrees/\n")
		gitCommit(t, outerGit, "ignore nested-worktrees")

		factory := newFactory(t, FactoryOptions{Path: outer, DataDir: shortTempDir(t), DisableClone: true})
		ws, err := factory.Open(ctx, OpenRequest{ThreadID: "thr_nested_submod"})
		if err != nil {
			t.Fatal(err)
		}
		info := ws.Info()
		dir := info.Label
		ws.Close()

		nestedWT := filepath.Join(dir, "nested-worktrees", "feat-clean", "pkg")
		if err := os.MkdirAll(filepath.Dir(nestedWT), 0o755); err != nil {
			t.Fatal(err)
		}
		gitOK(t, hostGit, "worktree", "add", "-qb", "feat/clean", nestedWT)
		nestedGit := NewGit(nestedWT, "file")
		gitOK(t, nestedGit, "-c", "protocol.file.allow=always", "submodule", "update", "--init", "-q", "vendor-bundle")

		subDir := filepath.Join(nestedWT, "vendor-bundle")
		subGit := NewGit(subDir, "file")
		gitOK(t, subGit, "config", "user.name", "Ariadne Test")
		gitOK(t, subGit, "config", "user.email", "test@example.invalid")

		// 1. Dirty submodule blocks removal.
		writeGit(t, subDir, "bundle.js", "dirty\n")
		if removed, err := factory.Remove(ctx, "thr_nested_submod", RemoveOptions{Merged: true}); err != nil || removed {
			t.Fatalf("dirty submodule must block Remove: removed=%t err=%v", removed, err)
		}
		gitOK(t, subGit, "checkout", "--", "bundle.js")

		// 2. Diverged detached HEAD in submodule blocks removal.
		writeGit(t, subDir, "bundle.js", "new commit\n")
		gitCommit(t, subGit, "diverge from gitlink")
		if removed, err := factory.Remove(ctx, "thr_nested_submod", RemoveOptions{Merged: true}); err != nil || removed {
			t.Fatalf("diverged submodule must block Remove: removed=%t err=%v", removed, err)
		}
		gitOK(t, subGit, "checkout", "--detach", "-q", orphanSHA)

		// 3. Locked nested worktree with clean submodule still blocks removal.
		gitOK(t, hostGit, "worktree", "lock", nestedWT)
		if removed, err := factory.Remove(ctx, "thr_nested_submod", RemoveOptions{Merged: true}); err != nil || removed {
			t.Fatalf("locked nested worktree with clean submodule must block Remove: removed=%t err=%v", removed, err)
		}
		if _, err := os.Stat(subDir); err != nil {
			t.Fatalf("submodule disappeared after refused locked removal: %v", err)
		}
		gitOK(t, hostGit, "worktree", "unlock", nestedWT)

		// 4. Clean aligned submodule + unlocked merged nested worktree -> Merged=true and Remove succeeds!
		if merged, err := factory.Merged(ctx, sdk.ThreadBranch{ThreadID: "thr_nested_submod", Branch: *info.Branch}, *info.Base); err != nil || !merged {
			t.Fatalf("clean aligned submodule should be merged: %t, %v", merged, err)
		}
		if removed, err := factory.Remove(ctx, "thr_nested_submod", RemoveOptions{Merged: true}); err != nil || !removed {
			t.Fatalf("clean aligned submodule should be removed on merged: %t, %v", removed, err)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("outer worktree directory still exists: %v", err)
		}
		if got := registeredWorktreeCount(t, hostGit); got != 1 {
			t.Fatalf("host left dangling worktree registration: count=%d", got)
		}
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

		outer, outerGit := initGit(t)
		gitOK(t, outerGit, "-c", "protocol.file.allow=always", "submodule", "add", "-q", "-b", "vendor-orphan", remoteURL, "vendor-bundle")
		gitCommit(t, outerGit, "add outer submodule")

		factory := newFactory(t, FactoryOptions{Path: outer, DataDir: shortTempDir(t), DisableClone: true})
		ws, err := factory.Open(ctx, OpenRequest{ThreadID: "thr_outer_submod"})
		if err != nil {
			t.Fatal(err)
		}
		info := ws.Info()
		dir := info.Label
		ws.Close()

		gitOK(t, NewGit(dir, "file"), "-c", "protocol.file.allow=always", "submodule", "update", "--init", "-q", "vendor-bundle")

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
		gitOK(t, outerGit, "worktree", "lock", dir)
		if removed, err := factory.Remove(ctx, "thr_outer_submod", RemoveOptions{Merged: true}); err != nil || removed {
			t.Fatalf("locked outer worktree with clean submodule must block Remove: removed=%t err=%v", removed, err)
		}
		gitOK(t, outerGit, "worktree", "unlock", dir)

		// 3. Clean unlocked outer worktree with clean aligned submodule -> Remove succeeds.
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
	repoDir, repoGit := initGit(t)
	gitOK(t, repoGit, "checkout", "-qb", "release/v2")
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
	gitOK(t, repoGit, "checkout", "--detach", "-q", "HEAD")
	if got := readSubmoduleConfiguredBranch(ctx, repoDir, "libs/follow"); got != "" {
		t.Fatalf("dot branch on detached superproject must be empty, got %q", got)
	}
}
