package workspaces

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/local"
	"github.com/daidr/Ariadne/internal/sources"
)

func runLocalGitOK(t *testing.T, dir string, args ...string) {
	t.Helper()
	res := local.NewGit(dir, "file")(t.Context(), args, local.GitOptions{})
	if res.ExitCode != 0 {
		t.Fatalf("git %v in %s failed: %s", args, dir, res.Stderr)
	}
}

func commitAll(t *testing.T, dir, message string) {
	t.Helper()
	runLocalGitOK(t, dir, "add", "-A")
	runLocalGitOK(t, dir, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", message)
}

func realLocalManager(t *testing.T, dataDir string, configure func(*Options)) (*Manager, *core.Repository) {
	t.Helper()
	repo, err := core.Open(t.Context(), core.Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	registry := sources.New()
	if _, err := registry.Register(local.DirectoryProvider(dataDir, nil, true)); err != nil {
		t.Fatal(err)
	}
	options := Options{Repository: repo, Sources: registry}
	if configure != nil {
		configure(&options)
	}
	manager, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeManager(t, manager) })
	return manager, repo
}

func TestUnbornGitDirectoryLifecycleAndRecovery(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	repoDir := t.TempDir()
	dataDir := t.TempDir()
	runLocalGitOK(t, repoDir, "init", "-q")
	runLocalGitOK(t, repoDir, "config", "user.name", "Test")
	runLocalGitOK(t, repoDir, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("hello unborn\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	manager, repo := realLocalManager(t, dataDir, func(o *Options) { o.CacheTTL = 15 * time.Millisecond })
	project, err := repo.CreateProject(ctx, new("Unborn"), core.ProjectStart{
		Source: map[string]any{"kind": "local-directory", "settings": map[string]any{"path": repoDir}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// 1. Thread opened while HEAD is unborn smoothly degrades to the shared directory.
	firstThread := newThread(t, repo, project.ID)
	ws1, err := manager.ForThread(ctx, project.ID, firstThread.ID)
	if err != nil {
		t.Fatalf("ForThread on unborn repo failed: %v", err)
	}
	if ws1.Info().Branch != nil || ws1.Info().Base != nil {
		t.Fatalf("unborn thread should have no branch/base: %+v", ws1.Info())
	}
	if err := ws1.WriteFile(ctx, "draft.txt", "written in shared mode\n"); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(repoDir, "draft.txt")); err != nil || string(got) != "written in shared mode\n" {
		t.Fatalf("shared write did not update source directory: %q, %v", got, err)
	}
	if note, err := manager.TakeSetup(ctx, firstThread.ID); err != nil || strings.Contains(note, "note_setup") {
		t.Fatalf("shared unborn workspace should not prompt isolated note_setup: %q, %v", note, err)
	}
	if branches, err := manager.Branches(ctx, project.ID); err != nil || branches != nil {
		t.Fatalf("unborn repo should not expose Branches: %+v, %v", branches, err)
	}
	if changes, err := manager.ThreadChanges(ctx, firstThread.ID); err != nil || changes.Changes != nil || len(changes.Repos) != 0 {
		t.Fatalf("unborn ThreadChanges: %+v, %v", changes, err)
	}
	if _, err := manager.MergeThread(ctx, firstThread.ID, "merge", "merge", ""); err == nil {
		t.Fatal("MergeThread succeeded without branch")
	} else {
		var domain *domainerr.Error
		if !errors.As(err, &domain) || domain.Key != "error.noBranch" {
			t.Fatalf("expected error.noBranch, got %v", err)
		}
	}
	if worktrees, err := manager.ListWorktrees(ctx, false); err != nil || len(worktrees) != 1 || worktrees[0].Error != nil || len(worktrees[0].Worktrees) != 0 {
		t.Fatalf("unborn ListWorktrees: %+v, %v", worktrees, err)
	}
	if files, err := manager.MentionFiles(ctx, project.ID, firstThread.ID, ""); err != nil || !slices.Contains(files, "README.md") || !slices.Contains(files, "draft.txt") {
		t.Fatalf("unborn MentionFiles: %v, %v", files, err)
	} else {
		for _, file := range files {
			if strings.HasPrefix(file, ".git/") || file == ".git" {
				t.Fatalf("MentionFiles leaked .git entry: %s", file)
			}
		}
	}
	if entries, err := os.ReadDir(dataDir); err == nil && len(entries) != 0 {
		t.Fatalf("unborn open created unexpected worktree storage: %v", entries)
	}

	// 2. Make the first commit; cached connection refreshes automatically on next access.
	commitAll(t, repoDir, "Initial commit")

	// Existing cached shared workspace continues working without interruption.
	if got, err := ws1.ReadFile(ctx, "draft.txt"); err != nil || got != "written in shared mode\n" {
		t.Fatalf("existing shared workspace disrupted after first commit: %q, %v", got, err)
	}

	// 3. New thread automatically recovers isolated worktree support.
	secondThread := newThread(t, repo, project.ID)
	ws2, err := manager.ForThread(ctx, project.ID, secondThread.ID)
	if err != nil {
		t.Fatalf("ForThread after first commit failed: %v", err)
	}
	if ws2.Info().Branch == nil || ws2.Info().Base == nil {
		t.Fatalf("new thread after first commit was not isolated: %+v", ws2.Info())
	}
	if note, err := manager.TakeSetup(ctx, secondThread.ID); err != nil || !strings.Contains(note, "note_setup") {
		t.Fatalf("isolated workspace should include setup hint: %q, %v", note, err)
	}
	if err := ws2.WriteFile(ctx, "README.md", "isolated branch change\n"); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(repoDir, "README.md")); err != nil || string(got) != "hello unborn\n" {
		t.Fatalf("isolated worktree leaked edit into source directory: %q, %v", got, err)
	}
	commitAll(t, ws2.Info().Label, "Thread change")
	if changes, err := manager.ThreadChanges(ctx, secondThread.ID); err != nil || changes.Changes == nil || len(changes.Changes.Files) != 1 {
		t.Fatalf("isolated ThreadChanges failed after recovery: %+v, %v", changes, err)
	}
	if worktrees, err := manager.ListWorktrees(ctx, false); err != nil || len(worktrees) != 1 || worktrees[0].Error != nil || len(worktrees[0].Worktrees) != 1 || worktrees[0].Worktrees[0].ThreadID != secondThread.ID {
		t.Fatalf("expected only second thread worktree listed: %+v, %v", worktrees, err)
	}

	// 4. Even after handle expiry/reopen, the early shared thread keeps its shared boundary.
	time.Sleep(40 * time.Millisecond)
	reopened1, err := manager.ForThread(ctx, project.ID, firstThread.ID)
	if err != nil {
		t.Fatalf("reopening early shared thread failed: %v", err)
	}
	if reopened1.Info().Branch != nil || reopened1.Info().Base != nil || reopened1.Info().Label != ws1.Info().Label {
		t.Fatalf("early shared thread switched to isolated worktree on reopen: %+v vs %+v", reopened1.Info(), ws1.Info())
	}
	if changes, err := manager.ThreadChanges(ctx, firstThread.ID); err != nil || changes.Changes != nil || len(changes.Repos) != 0 {
		t.Fatalf("early shared thread ThreadChanges should stay empty: %+v, %v", changes, err)
	}
	if status, err := manager.RemoveWorktree(ctx, project.ID, firstThread.ID, "user"); err != nil || status != "dirty" {
		t.Fatalf("RemoveWorktree on shared thread should not remove: %q, %v", status, err)
	}
	known1, err := repo.GetThread(ctx, firstThread.ID)
	if err != nil || known1.Workspace == nil || known1.Workspace.Removed {
		t.Fatalf("RemoveWorktree mutated shared thread state: %+v, %v", known1.Workspace, err)
	}

	// 5. Merge works on the recovered isolated thread.
	merged, err := manager.MergeThread(ctx, secondThread.ID, "merge", "Merge isolated change", "")
	if err != nil || merged.Outcome.Kind != "merged" {
		t.Fatalf("MergeThread on recovered thread failed: %+v, %v", merged.Outcome, err)
	}
	if got, err := os.ReadFile(filepath.Join(repoDir, "README.md")); err != nil || string(got) != "isolated branch change\n" {
		t.Fatalf("merged change not present in source repo: %q, %v", got, err)
	}

	// 6. Discarding a project containing both shared and isolated threads removes only worktrees and keeps the source dir intact.
	wt2Path := ws2.Info().Label
	if err := manager.DiscardProject(ctx, project.ID); err != nil {
		t.Fatalf("DiscardProject with mixed shared+isolated threads failed: %v", err)
	}
	if _, err := os.Stat(wt2Path); !os.IsNotExist(err) {
		t.Fatalf("isolated worktree remained after DiscardProject: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(repoDir, "draft.txt")); err != nil || string(got) != "written in shared mode\n" {
		t.Fatalf("DiscardProject damaged original repository: %q, %v", got, err)
	}
}

func TestPlainNonGitDirectoryTransitionsToGitAfterInitAndCommit(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	dir := t.TempDir()
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("plain\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	manager, repo := realLocalManager(t, dataDir, func(o *Options) { o.CacheTTL = 15 * time.Millisecond })
	project, err := repo.CreateProject(ctx, new("Plain"), core.ProjectStart{
		Source: map[string]any{"kind": "local-directory", "settings": map[string]any{"path": dir}},
	})
	if err != nil {
		t.Fatal(err)
	}

	t1 := newThread(t, repo, project.ID)
	ws1, err := manager.ForThread(ctx, project.ID, t1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ws1.Info().Branch != nil || ws1.Info().Base != nil {
		t.Fatalf("plain dir should not isolate: %+v", ws1.Info())
	}

	// git init without a commit stays shared without failing.
	runLocalGitOK(t, dir, "init", "-q")
	t2 := newThread(t, repo, project.ID)
	ws2, err := manager.ForThread(ctx, project.ID, t2.ID)
	if err != nil {
		t.Fatalf("ForThread after git init without commit failed: %v", err)
	}
	if ws2.Info().Branch != nil || ws2.Info().Base != nil {
		t.Fatalf("git init without commit should stay shared: %+v", ws2.Info())
	}

	// First commit enables isolation for new threads while older threads remain shared.
	commitAll(t, dir, "Initial commit")
	t3 := newThread(t, repo, project.ID)
	ws3, err := manager.ForThread(ctx, project.ID, t3.ID)
	if err != nil {
		t.Fatalf("ForThread after git init + commit failed: %v", err)
	}
	if ws3.Info().Branch == nil || ws3.Info().Base == nil {
		t.Fatalf("expected isolated thread after first commit: %+v", ws3.Info())
	}
	time.Sleep(40 * time.Millisecond)
	reopened1, err := manager.ForThread(ctx, project.ID, t1.ID)
	if err != nil || reopened1.Info().Branch != nil {
		t.Fatalf("t1 should remain shared after reopen: %+v, %v", reopened1.Info(), err)
	}
	if err := manager.DiscardProject(ctx, project.ID); err != nil {
		t.Fatalf("DiscardProject failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "note.txt")); err != nil {
		t.Fatalf("source directory file lost after DiscardProject: %v", err)
	}
}

func TestSecondaryUnbornFileSourceCoexistsWithIsolatedPrimary(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	primaryDir := t.TempDir()
	secondaryDir := t.TempDir()
	dataDir := t.TempDir()

	initTemplateRepo(t, primaryDir)
	runLocalGitOK(t, secondaryDir, "init", "-q")
	if err := os.WriteFile(filepath.Join(secondaryDir, "extra.txt"), []byte("unborn secondary\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	manager, repo := realLocalManager(t, dataDir, nil)
	set := core.FileSourceSet{
		PrimaryID: "src_primary",
		Sources: []core.FileSource{
			{ID: "src_primary", Name: "main", Kind: "local-directory", Settings: map[string]any{"path": primaryDir}},
			{ID: "src_unborn", Name: "scratch", Kind: "local-directory", Settings: map[string]any{"path": secondaryDir}},
		},
	}
	project, err := repo.CreateProject(ctx, new("Mixed sources"), core.ProjectStart{FileSources: &set})
	if err != nil {
		t.Fatal(err)
	}

	thread := newThread(t, repo, project.ID)
	mainWS, err := manager.ForThread(ctx, project.ID, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if mainWS.Info().Branch == nil {
		t.Fatalf("primary committed repo should be isolated: %+v", mainWS.Info())
	}
	extraWS, err := manager.ForThreadSource(ctx, project.ID, thread.ID, "src_unborn")
	if err != nil {
		t.Fatalf("opening unborn secondary source failed: %v", err)
	}
	if extraWS.Info().Branch != nil || extraWS.Info().Base != nil {
		t.Fatalf("unborn secondary should degrade to shared: %+v", extraWS.Info())
	}
	if got, err := extraWS.ReadFile(ctx, "extra.txt"); err != nil || got != "unborn secondary\n" {
		t.Fatalf("secondary read failed: %q, %v", got, err)
	}
	thread, err = repo.GetThread(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	threadSources, err := manager.ThreadSources(ctx, thread)
	if err != nil || len(threadSources) != 1 || threadSources[0].ID != "src_primary" {
		t.Fatalf("expected only isolated primary in ThreadSources, got %+v, %v", threadSources, err)
	}
	if _, err := manager.ThreadChanges(ctx, thread.ID); err != nil {
		t.Fatalf("ThreadChanges failed with unborn secondary: %v", err)
	}
	if err := manager.DiscardProject(ctx, project.ID); err != nil {
		t.Fatalf("DiscardProject failed with mixed sources: %v", err)
	}
	if _, err := os.Stat(filepath.Join(secondaryDir, "extra.txt")); err != nil {
		t.Fatalf("secondary unborn dir damaged by DiscardProject: %v", err)
	}
}
