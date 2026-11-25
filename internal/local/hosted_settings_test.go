package local

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/daidr/Ariadne/internal/fsutil"
)

func TestHostedStorageUsesOneRootAndProtectsExistingClone(t *testing.T) {
	t.Parallel()
	defaults := filepath.Join(t.TempDir(), "github")
	chosen := t.TempDir()
	expectedChosen, err := fsutil.CanonicalPath(chosen)
	if err != nil {
		t.Fatal(err)
	}
	configured, err := HostedDataDir(defaults, chosen)
	if err != nil || configured != filepath.Join(expectedChosen, "ariadne", "github") {
		t.Fatal(configured, err)
	}
	bare, trees, err := hostedPaths(configured, "group/app", nil)
	if err != nil || bare != filepath.Join(configured, "repos", "group", "app.git") ||
		trees != filepath.Join(configured, "worktrees", "group", "app") {
		t.Fatal(bare, trees, err)
	}
	if _, err := HostedDataDir(defaults, "relative"); err == nil {
		t.Fatal("accepted a relative directory")
	}
	if err := CheckHostedDirectoryChange(defaults, "group/app", nil, chosen, nil); err != nil {
		t.Fatal("empty source could not change directory", err)
	}
	old, _, err := hostedPaths(defaults, "group/app", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(old, 0700); err != nil {
		t.Fatal(err)
	}
	if err := CheckHostedDirectoryChange(defaults, "group/app", nil, chosen, nil); err == nil {
		t.Fatal("repointed a source with an existing clone")
	}
}

func TestHostedStorageCanonicalizesSymlinkAliases(t *testing.T) {
	t.Parallel()
	defaults := filepath.Join(t.TempDir(), "github")
	realStorage := t.TempDir()
	expectedStorage, err := fsutil.CanonicalPath(realStorage)
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "storage-link")
	if err := os.Symlink(realStorage, alias); err != nil {
		t.Skip("symlink privileges unavailable")
	}
	configured, err := HostedDataDir(defaults, alias)
	if err != nil || configured != filepath.Join(expectedStorage, "ariadne", "github") {
		t.Fatalf("symlinked storage was not canonicalized: %q, %v", configured, err)
	}
	bare, _, err := hostedPaths(configured, "group/app", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bare, 0700); err != nil {
		t.Fatal(err)
	}
	if err := CheckHostedDirectoryChange(defaults, "group/app", nil, expectedStorage, alias); err != nil {
		t.Fatalf("equivalent canonical and symlinked directories rejected: %v", err)
	}
	other := t.TempDir()
	if err := CheckHostedDirectoryChange(defaults, "group/app", nil, other, alias); err == nil {
		t.Fatal("repointed a symlinked source with an existing clone")
	}
}
