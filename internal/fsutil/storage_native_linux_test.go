package fsutil

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestExt4BatchMatchesFileAllocation(t *testing.T) {
	root := t.TempDir()
	var volume unix.Statfs_t
	if err := unix.Statfs(root, &volume); err != nil || uint32(volume.Type) != unix.EXT4_SUPER_MAGIC {
		t.Skip("test volume is not ext4")
	}
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workspace, "sparse")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := file.Truncate(64 << 20); err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte("payload"), 0); err != nil {
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(path, filepath.Join(workspace, "link")); err != nil {
		t.Fatal(err)
	}
	previous := DiskUsage(t.Context(), workspace)
	current := MeasureStorage(t.Context(), root, []string{workspace})
	size := current.Worktrees[workspace]
	if previous == nil || size.Bytes == nil || *size.Bytes != *previous || *size.Bytes >= 64<<20 {
		t.Fatalf("ext4 measurement differs from allocated blocks: %+v", size)
	}
}

func TestExt4ParallelScanKeepsNestedWorktreeTotals(t *testing.T) {
	root := t.TempDir()
	var volume unix.Statfs_t
	if err := unix.Statfs(root, &volume); err != nil || uint32(volume.Type) != unix.EXT4_SUPER_MAGIC {
		t.Skip("test volume is not ext4")
	}
	parent := filepath.Join(root, "outer")
	left, right := filepath.Join(parent, "left"), filepath.Join(parent, "right")
	for _, path := range []string{left, right} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	file := filepath.Join(left, "shared")
	if err := os.WriteFile(file, make([]byte, 64<<10), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(file, filepath.Join(right, "shared")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(right, "unique"), make([]byte, 128<<10), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot := MeasureStorage(t.Context(), root, []string{parent, left, right})
	for _, path := range []string{parent, left, right} {
		want := DiskUsage(t.Context(), path)
		got := snapshot.Worktrees[path].Bytes
		if want == nil || got == nil || *got != *want {
			t.Fatalf("wrong allocation for %s: got %v, want %v", filepath.Base(path), got, want)
		}
	}
	if snapshot.Total == nil || *snapshot.Total != *snapshot.Worktrees[parent].Bytes {
		t.Fatal("nested worktrees were double counted in the total")
	}
}
