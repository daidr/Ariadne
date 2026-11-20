package local

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHostedStorageUsesOneRootAndProtectsExistingClone(t *testing.T) {
	defaults := filepath.Join(t.TempDir(), "github")
	chosen := t.TempDir()
	configured, err := HostedDataDir(defaults, chosen)
	if err != nil || configured != filepath.Join(chosen, "ariadne", "github") {
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
