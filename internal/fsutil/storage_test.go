package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMeasureStorageOverlappingRootsAndAliases(t *testing.T) {
	root := t.TempDir()
	managed := filepath.Join(root, "plugins")
	cloneDir := filepath.Join(managed, "github", "repo.git")
	first := filepath.Join(managed, "worktree-a")
	second := filepath.Join(root, ".ariadne-worktrees", "repo", "worktree-b")
	for _, dir := range []string{cloneDir, first, second} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "payload"), make([]byte, 8192), 0600); err != nil {
			t.Fatal(err)
		}
	}
	pluginsOnly := MeasureStorage(t.Context(), managed, []string{first})
	result := MeasureStorage(t.Context(), managed, []string{first, second, first})
	a, b := result.Worktrees[first], result.Worktrees[second]
	if a.Bytes == nil || b.Bytes == nil || result.Managed == nil || result.Total == nil || pluginsOnly.Managed == nil {
		t.Fatalf("missing measurements: %+v", result)
	}
	if *result.Total != *a.Bytes+*b.Bytes {
		t.Fatalf("worktree total mismatch: total=%d, a=%d, b=%d", *result.Total, *a.Bytes, *b.Bytes)
	}
	// Managed must include both ~/.ariadne/plugins (with cloneDir and worktree-a once)
	// and external sibling .ariadne-worktrees worktree-b, without double counting worktree-a.
	wantManaged := *pluginsOnly.Managed + *b.Bytes
	if *result.Managed != wantManaged {
		t.Fatalf("managed=%d, want=%d (pluginsOnly=%d, a=%d, b=%d, total=%d)",
			*result.Managed, wantManaged, *pluginsOnly.Managed, *a.Bytes, *b.Bytes, *result.Total)
	}
}

func TestMeasureStorageIncludesExternalWorktreesWhenManagedMissing(t *testing.T) {
	root := t.TempDir()
	managed := filepath.Join(root, "missing-plugins")
	sibling := filepath.Join(root, ".ariadne-worktrees", "repo", "thr_1")
	if err := os.MkdirAll(sibling, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sibling, "payload"), make([]byte, 16384), 0600); err != nil {
		t.Fatal(err)
	}
	result := MeasureStorage(t.Context(), managed, []string{sibling})
	wt := result.Worktrees[sibling]
	if wt.Bytes == nil || result.Managed == nil || result.Total == nil {
		t.Fatalf("missing measurements: %+v", result)
	}
	if *result.Total != *wt.Bytes || *result.Managed != *wt.Bytes {
		t.Fatalf("managed=%d total=%d wt=%d", *result.Managed, *result.Total, *wt.Bytes)
	}
}

func TestMeasureStorageDeduplicatesHardLinksBetweenPluginsAndSiblingWorktree(t *testing.T) {
	root := t.TempDir()
	managed := filepath.Join(root, "plugins")
	sibling := filepath.Join(root, ".ariadne-worktrees", "repo", "thr_1")
	for _, dir := range []string{managed, sibling} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	pluginFile := filepath.Join(managed, "shared.bin")
	if err := os.WriteFile(pluginFile, make([]byte, 64<<10), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(pluginFile, filepath.Join(sibling, "shared.bin")); err != nil {
		t.Skipf("test volume does not support hard links: %v", err)
	}
	result := MeasureStorage(t.Context(), managed, []string{sibling})
	wt := result.Worktrees[sibling]
	if wt.Bytes == nil || result.Managed == nil || result.Total == nil || result.Logical {
		t.Fatalf("allocation unavailable: %+v", result)
	}
	if *result.Managed >= *wt.Bytes*2 {
		t.Fatalf("hard link between plugins and sibling worktree counted twice: managed=%d wt=%d", *result.Managed, *wt.Bytes)
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
