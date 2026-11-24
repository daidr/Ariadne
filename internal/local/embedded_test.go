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
