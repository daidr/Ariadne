package fsutil

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestExclusiveExtentLengthExcludesCoWAndUncertainMappings(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags uint32
		phys  uint64
		want  uint64
	}{
		{"independent data", extentLast, 16384, 4096},
		{"preallocated data", extentUnwritten, 16384, 4096},
		{"reflink or snapshot", 0x2000, 16384, 0},
		{"encoded", 0x0008, 16384, 0},
		{"inline", 0x0200, 16384, 0},
		{"delayed allocation", 0x0006, 16384, 0},
		{"no physical mapping", 0, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := exclusiveExtentLength(fileExtent{flags: tc.flags, physical: tc.phys, length: 4096}); got != tc.want {
				t.Fatalf("got %d, want %d", got, tc.want)
			}
		})
	}
}

func TestLinuxExtentQueryUsesRealFileMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "extent")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.Write(make([]byte, 64<<10)); err != nil {
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	probe := exclusiveBlocks{supported: map[uint64]bool{uint64(info.Sys().(*syscall.Stat_t).Dev): true}}
	blocks := probe.read(t.Context(), path, info)
	if blocks == nil {
		t.Skip("test filesystem does not report file extents")
	}
	if *blocks < 0 || *blocks > 64<<10 {
		t.Fatalf("unexpected extent count: %d bytes", *blocks)
	}
	var volume unix.Statfs_t
	if err := unix.Fstatfs(int(file.Fd()), &volume); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(path)
	measure := func(want int64) {
		t.Helper()
		if kind := uint32(volume.Type); kind != unix.XFS_SUPER_MAGIC && kind != unix.BTRFS_SUPER_MAGIC {
			return
		}
		size := MeasureStorage(t.Context(), filepath.Join(dir, "missing"), []string{dir}).Worktrees[dir]
		if size.Exclusive == nil {
			t.Fatal("scan did not report releasable data")
		}
		if *size.Exclusive != want {
			t.Fatalf("scan reports %d releasable bytes, want %d", *size.Exclusive, want)
		}
	}
	measure(*blocks)
	clone, err := os.Create(filepath.Join(filepath.Dir(path), "clone"))
	if err != nil {
		t.Fatal(err)
	}
	defer clone.Close()
	if err := unix.IoctlFileClone(int(clone.Fd()), int(file.Fd())); err != nil {
		t.Logf("real extent query passed; test volume cannot clone extents: %v", err)
		return
	}
	info, err = clone.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if got := probe.read(t.Context(), clone.Name(), info); got == nil || *got != 0 {
		t.Fatalf("reflink data counted as releasable: %v", got)
	}
	measure(0)
}
