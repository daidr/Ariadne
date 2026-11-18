package fsutil

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRemovalCannotEscapeTheOwnedRoot(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(filepath.Join(workspace, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	keep := filepath.Join(outside, "keep")
	if err := os.WriteFile(keep, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{root, outside, filepath.Join(root, "..", filepath.Base(outside))} {
		if err := RemoveTree(t.Context(), root, target); err == nil {
			t.Fatalf("unsafe target accepted: %s", target)
		}
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "link")); err == nil {
		if err := RemoveTree(t.Context(), root, filepath.Join(workspace, "link", "keep")); err == nil {
			t.Fatal("rooted removal traversed an external symlink")
		}
	}
	if err := RemoveTree(t.Context(), root, workspace); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("outside file changed", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := RemoveTree(ctx, root, filepath.Join(root, "another")); err == nil {
		t.Fatal("cancelled cleanup continued")
	}
}

func TestRemovalAcceptsCanonicalChildUnderRootAlias(t *testing.T) {
	root := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Skip("symlink privileges unavailable")
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	if err := RemoveTree(t.Context(), alias, root); err == nil {
		t.Fatal("cleanup accepted its canonical root")
	}
	if err := RemoveTree(t.Context(), alias, child); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(child); !os.IsNotExist(err) {
		t.Fatal("owned child was not removed", err)
	}
}
