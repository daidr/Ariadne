package local

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/internal/fsutil"
	"github.com/daidr/Ariadne/sdk"
	"golang.org/x/sys/windows"
)

func shortPath83(t *testing.T, path string) string {
	t.Helper()
	input, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	size, err := windows.GetShortPathName(input, nil, 0)
	if err != nil || size == 0 {
		t.Skip("8.3 short paths unavailable")
	}
	buffer := make([]uint16, size)
	if _, err := windows.GetShortPathName(input, &buffer[0], size); err != nil {
		t.Skip("8.3 short paths unavailable")
	}
	return windows.UTF16ToString(buffer)
}

func TestWindowsShortPathsNormalizeWorktreesAndHostedStorage(t *testing.T) {
	base := filepath.Join(t.TempDir(), "runneradmin long storage directory")
	if err := os.MkdirAll(base, 0755); err != nil {
		t.Fatal(err)
	}
	shortBase := shortPath83(t, base)
	canonicalBase, err := fsutil.CanonicalPath(base)
	if err != nil {
		t.Fatal(err)
	}
	if strings.EqualFold(shortBase, canonicalBase) {
		t.Skip("8.3 short path generation is disabled on this volume")
	}

	ctx := t.Context()
	root, git := initGit(t)
	writeGit(t, root, "app/file.txt", "tracked")
	gitCommit(t, git, "add nested source")

	shortData := filepath.Join(shortBase, "data")
	factory := newFactory(t, FactoryOptions{Path: filepath.Join(root, "app"), DataDir: shortData, DisableClone: true})
	key := filepath.Base(factory.roots[0])
	shortRoot := filepath.Join(shortData, "worktrees", key)
	canonicalRoot := factory.roots[0]

	normalShort := filepath.Join(shortRoot, "thr_normal")
	normalCanonical := filepath.Join(canonicalRoot, "thr_normal")
	gitOK(t, git, "worktree", "add", "-b", BranchFor("thr_normal"), normalShort)

	orphanShort := filepath.Join(shortRoot, "thr_orphan")
	orphanCanonical := filepath.Join(canonicalRoot, "thr_orphan")
	gitOK(t, git, "worktree", "add", "-b", BranchFor("thr_orphan"), orphanShort)
	if err := os.Remove(filepath.Join(orphanCanonical, ".git")); err != nil {
		t.Fatal(err)
	}
	gitOK(t, git, "worktree", "prune", "--expire", "now")

	worktrees, err := factory.Worktrees(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	byThread := map[string]sdk.WorktreeInfo{}
	for _, item := range worktrees {
		byThread[item.ThreadID] = item
	}
	if got := byThread["thr_normal"]; got.Path != normalCanonical || got.Orphaned {
		t.Fatalf("normal 8.3 worktree mismatch: got %+v, want %q", got, normalCanonical)
	}
	if got := byThread["thr_orphan"]; got.Path != orphanCanonical || !got.Orphaned {
		t.Fatalf("orphan 8.3 worktree mismatch: got %+v, want %q", got, orphanCanonical)
	}
	if removed, err := factory.RemoveOrphaned(ctx, "thr_orphan", filepath.Join(orphanShort, "app")); err != nil || !removed {
		t.Fatalf("orphan cleanup rejected 8.3 short path label: %t, %v", removed, err)
	}

	hostedStorage := filepath.Join(shortBase, "hosted")
	if err := os.MkdirAll(hostedStorage, 0755); err != nil {
		t.Fatal(err)
	}
	hostedCanonical := filepath.Join(canonicalBase, "hosted")
	defaults := filepath.Join(t.TempDir(), "github")
	data, err := HostedDataDir(defaults, hostedStorage)
	if err != nil || data != filepath.Join(hostedCanonical, "ariadne", "github") {
		t.Fatalf("hosted data dir from 8.3 path: %q, %v", data, err)
	}
	remote, _ := initGit(t)
	host := newHostedTest(t, remote, data, nil)
	workspace, err := host.Open(ctx, sdk.WorkspaceRequest{ThreadID: "thr_hosted"})
	if err != nil {
		t.Fatal(err)
	}
	if host.bare != filepath.Join(hostedCanonical, "ariadne", "github", "repos", "owner", "repository.git") ||
		filepath.Dir(workspace.Info().Label) != filepath.Join(hostedCanonical, "ariadne", "github", "worktrees", "owner", "repository") {
		t.Fatalf("hosted paths were not canonicalized from 8.3 storage: bare=%q label=%q", host.bare, workspace.Info().Label)
	}
	if err := CheckHostedDirectoryChange(defaults, "owner/repository", nil, hostedCanonical, hostedStorage); err != nil {
		t.Fatalf("equivalent 8.3 and long hosted storage paths rejected: %v", err)
	}
}
