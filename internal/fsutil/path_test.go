package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCanonicalPathResolvesExistingAncestorForMissingChildren(t *testing.T) {
	if got, err := CanonicalPath(""); err != nil || got != "" {
		t.Fatalf("empty path: %q, %v", got, err)
	}
	realDir := t.TempDir()
	canonicalReal, err := filepath.EvalSymlinks(realDir)
	if err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(realDir, "not", "created", "yet")
	got, err := CanonicalPath(missing)
	if err != nil || got != filepath.Join(canonicalReal, "not", "created", "yet") {
		t.Fatalf("missing children under temp dir: got %q, want %q, err %v", got, filepath.Join(canonicalReal, "not", "created", "yet"), err)
	}

	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(realDir, alias); err != nil {
		t.Skip("symlink privileges unavailable")
	}
	got, err = CanonicalPath(filepath.Join(alias, "sub", "leaf"))
	if err != nil || got != filepath.Join(canonicalReal, "sub", "leaf") {
		t.Fatalf("missing children under symlink: got %q, want %q, err %v", got, filepath.Join(canonicalReal, "sub", "leaf"), err)
	}

	dangling := filepath.Join(realDir, "dangling")
	if err := os.Symlink(filepath.Join(realDir, "does-not-exist"), dangling); err != nil {
		t.Fatal(err)
	}
	got, err = CanonicalPath(filepath.Join(dangling, "child"))
	if err != nil || got != filepath.Join(canonicalReal, "dangling", "child") {
		t.Fatalf("dangling symlink: got %q, want %q, err %v", got, filepath.Join(canonicalReal, "dangling", "child"), err)
	}
}
