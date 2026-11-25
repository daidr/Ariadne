package workspaces

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/local"
	"github.com/daidr/Ariadne/internal/sources"
)

func TestFileSourcesUseSeparateGitWorktreesAndPinTheDefault(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	first, second, managed := t.TempDir(), t.TempDir(), t.TempDir()
	git := func(dir string, args ...string) {
		t.Helper()
		res := local.NewGit(dir, "file")(ctx, args, local.GitOptions{})
		if res.ExitCode != 0 {
			t.Fatalf("git %v: %s", args, res.Stderr)
		}
	}
	makeRepo := func(dir, branch, contents string) {
		t.Helper()
		sha := initTemplateRepo(t, dir)
		if err := os.Remove(filepath.Join(dir, ".git", "refs", "heads", "main")); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".git", "refs", "heads", branch), []byte(sha+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/"+branch+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		git(dir, "add", "file.txt")
		git(dir, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "initial")
	}
	makeRepo(first, "first-base", "first")
	makeRepo(second, "second-base", "second")

	repo, err := core.Open(ctx, core.Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	registry := sources.New()
	if _, err := registry.Register(local.DirectoryProvider(managed, nil, true)); err != nil {
		t.Fatal(err)
	}
	manager, err := New(Options{Repository: repo, Sources: registry})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	set := core.FileSourceSet{PrimaryID: "src_first", Sources: []core.FileSource{
		{ID: "src_first", Name: "frontend", Kind: "local-directory", Settings: map[string]any{"path": first}},
		{ID: "src_second", Name: "backend", Kind: "local-directory", Settings: map[string]any{"path": second}},
	}}
	project, err := repo.CreateProject(ctx, new("Two repositories"), core.ProjectStart{FileSources: &set})
	if err != nil {
		t.Fatal(err)
	}
	thread := newThread(t, repo, project.ID)
	main, err := manager.ForThread(ctx, project.ID, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	extra, err := manager.ForThreadSource(ctx, project.ID, thread.ID, "src_second")
	if err != nil {
		t.Fatal(err)
	}
	if main.Info().Label == extra.Info().Label {
		t.Fatal("the two repositories share a checkout")
	}
	for _, workspace := range []struct {
		source, want string
	}{
		{"src_first", "first"}, {"src_second", "second"},
	} {
		view, err := manager.ForThreadSource(ctx, project.ID, thread.ID, workspace.source)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := view.ReadFile(ctx, "file.txt"); got != workspace.want || err != nil {
			t.Fatalf("%s read %q: %v", workspace.source, got, err)
		}
		if err := view.WriteFile(ctx, "file.txt", "edited "+workspace.want); err != nil {
			t.Fatal(err)
		}
	}
	for folder, want := range map[string]string{first: "first", second: "second"} {
		if got, err := os.ReadFile(filepath.Join(folder, "file.txt")); string(got) != want || err != nil {
			t.Fatalf("original checkout %s changed: %q, %v", folder, got, err)
		}
	}
	switched := set
	switched.PrimaryID = "src_second"
	if _, err := repo.SetProjectFileSources(ctx, project.ID, set, switched); err != nil {
		t.Fatal(err)
	}
	if err := main.Close(); err != nil {
		t.Fatal(err)
	}
	old, err := manager.ForThread(ctx, project.ID, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := old.ReadFile(ctx, "file.txt"); got != "edited first" || err != nil {
		t.Fatalf("existing thread opened a different repository: %q, %v", got, err)
	}
	next := newThread(t, repo, project.ID)
	current, err := manager.ForThread(ctx, project.ID, next.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := current.ReadFile(ctx, "file.txt"); got != "second" || err != nil {
		t.Fatalf("new thread did not use the new primary: %q, %v", got, err)
	}
	if result, err := manager.ThreadChanges(ctx, thread.ID); err != nil || result.Changes == nil || result.Changes.Base != "first-base" {
		t.Fatalf("existing branch was compared in the wrong repository: %+v, %v", result, err)
	}
	list, err := manager.ListWorktrees(ctx, false)
	if err != nil || len(list) != 1 || len(list[0].Worktrees) != 3 {
		t.Fatalf("worktrees: %+v, %v", list, err)
	}
	extraPath := extra.Info().Label
	if status, err := manager.RemoveOrphanedWorktree(ctx, project.ID, thread.ID, "src_second"); err != nil || status != "orphan-changed" {
		t.Fatalf("registered checkout accepted orphan cleanup: %s, %v", status, err)
	}
	if err := os.WriteFile(filepath.Join(extraPath, "file.txt"), []byte("second"), 0600); err != nil {
		t.Fatal(err)
	}
	if status, err := manager.RemoveSourceWorktree(ctx, project.ID, thread.ID, "src_second", "user"); err != nil || status != "removed" {
		t.Fatalf("remove secondary worktree: %s, %v", status, err)
	}
	known, err := repo.GetThread(ctx, thread.ID)
	if err != nil || known.Workspace == nil || known.Workspace.Removed || known.Workspace.SourceID != "src_first" {
		t.Fatalf("removing the secondary worktree changed the primary: %+v, %v", known.Workspace, err)
	}

	// A past cleanup can leave a directory even after the record says it was removed.
	missingRecord := newThread(t, repo, project.ID)
	unowned := filepath.Join(filepath.Dir(extraPath), missingRecord.ID)
	for _, dir := range []string{extraPath, unowned} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "leftover.txt"), []byte("left behind"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	list, err = manager.ListWorktrees(ctx, false)
	if err != nil || len(list) != 1 || len(list[0].Worktrees) != 3 {
		t.Fatalf("worktrees including the owned orphan: %+v, %v", list, err)
	}
	var orphans []ListedWorktree
	for _, workspace := range list[0].Worktrees {
		if workspace.Orphaned {
			orphans = append(orphans, workspace)
		}
	}
	if len(orphans) != 1 || orphans[0].Path != extraPath || orphans[0].SourceID != "src_second" || orphans[0].ThreadID != thread.ID {
		t.Fatalf("unexpected orphans: %+v", orphans)
	}
	if status, err := manager.RemoveOrphanedWorktree(ctx, project.ID, missingRecord.ID, "src_second"); err != nil || status != "orphan-changed" {
		t.Fatalf("unrecorded directory accepted orphan cleanup: %s, %v", status, err)
	}
	if status, err := manager.RemoveOrphanedWorktree(ctx, project.ID, thread.ID, "src_second"); err != nil || status != "removed" {
		t.Fatalf("remove owned orphan: %s, %v", status, err)
	}
	if _, err := os.Stat(extraPath); !os.IsNotExist(err) {
		t.Fatal("owned orphan is still on disk", err)
	}
	if _, err := os.Stat(filepath.Join(unowned, "leftover.txt")); err != nil {
		t.Fatal("unrecorded directory was changed", err)
	}
	if _, err := os.Stat(filepath.Join(second, ".git", "refs", "heads", filepath.FromSlash(local.BranchFor(thread.ID)))); err != nil {
		t.Fatalf("expected branch ref to remain after orphan cleanup: %v", err)
	}
}

func TestOrphanMatchesRecordedLabelNormalizesSymlink(t *testing.T) {
	t.Parallel()
	realRoot := t.TempDir()
	worktree := filepath.Join(realRoot, "thr_orphan")
	if err := os.MkdirAll(worktree, 0755); err != nil {
		t.Fatal(err)
	}
	aliasRoot := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(realRoot, aliasRoot); err != nil {
		t.Skip("symlink privileges unavailable")
	}
	canonicalWorktree, err := filepath.EvalSymlinks(worktree)
	if err != nil {
		t.Fatal(err)
	}
	if !orphanMatchesRecordedLabel(canonicalWorktree, filepath.Join(aliasRoot, "thr_orphan", "app")) {
		t.Fatal("historical symlinked label was not matched to its canonical orphan worktree")
	}
	if orphanMatchesRecordedLabel(canonicalWorktree, filepath.Join(aliasRoot, "other", "app")) {
		t.Fatal("unrelated symlinked label matched the orphan worktree")
	}
}
