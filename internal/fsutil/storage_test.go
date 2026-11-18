package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMeasureStorageOverlappingRootsAndAliases(t *testing.T) {
	root := t.TempDir()
	managed := filepath.Join(root, "plugins")
	first := filepath.Join(managed, "worktree-a")
	second := filepath.Join(root, "worktree-b")
	for _, dir := range []string{first, second} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "payload"), make([]byte, 8192), 0600); err != nil {
			t.Fatal(err)
		}
	}
	result := MeasureStorage(t.Context(), managed, []string{first, second, first})
	a, b := result.Worktrees[first], result.Worktrees[second]
	if a.Bytes == nil || b.Bytes == nil || result.Managed == nil || result.Total == nil {
		t.Fatalf("missing measurements: %+v", result)
	}
	if *result.Managed < *a.Bytes || *result.Managed >= *result.Total || *result.Total != *a.Bytes+*b.Bytes {
		t.Fatalf("scopes are double counted: managed=%d, total=%d, a=%d, b=%d",
			*result.Managed, *result.Total, *a.Bytes, *b.Bytes)
	}
}

func TestMeasureStorageDeduplicatesHardLinksAcrossWorktrees(t *testing.T) {
	root := t.TempDir()
	first, second := filepath.Join(root, "a"), filepath.Join(root, "b")
	for _, path := range []string{first, second} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	data := filepath.Join(first, "payload")
	if err := os.WriteFile(data, make([]byte, 64<<10), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(data, filepath.Join(second, "hardlink")); err != nil {
		t.Skipf("test volume does not support hard links: %v", err)
	}
	result := MeasureStorage(t.Context(), root, []string{first, second})
	a, b := result.Worktrees[first], result.Worktrees[second]
	if a.Bytes == nil || b.Bytes == nil || result.Total == nil || result.Logical {
		t.Fatalf("worktree allocation is unavailable: %+v", result)
	}
	if *a.Bytes+*b.Bytes <= *result.Total {
		t.Fatal("cross-worktree hard link counted twice")
	}
	if a.Exclusive != nil || b.Exclusive != nil {
		t.Fatal("data with a link outside the worktree cannot count as exclusive")
	}
}
