package fsutil

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestReFSExclusiveExcludesSharedBlocks(t *testing.T) {
	root := t.TempDir()
	if volume := os.Getenv("ARIADNE_TEST_REFS_ROOT"); volume != "" {
		var err error
		root, err = os.MkdirTemp(volume, "ariadne-refs-test-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			for _, file := range []string{"worktree/clone", "worktree", "source"} {
				_ = os.Remove(filepath.Join(root, file))
			}
			if err := os.Remove(root); err != nil {
				t.Error(err)
			}
		})
	}
	probe := newReFSProbe(root)
	if probe == nil {
		if os.Getenv("ARIADNE_TEST_REFS_ROOT") != "" {
			t.Fatal("could not query the ReFS test volume")
		}
		t.Skip("test volume is not ReFS")
	}
	worktree := filepath.Join(root, "worktree")
	if err := os.Mkdir(worktree, 0700); err != nil {
		t.Fatal(err)
	}
	source, err := os.Create(filepath.Join(root, "source"))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	clone, err := os.Create(filepath.Join(worktree, "clone"))
	if err != nil {
		t.Fatal(err)
	}
	defer clone.Close()
	const copied = 1 << 20
	data := make([]byte, copied)
	for i := range data {
		data[i] = byte(i % 251)
	}
	if _, err := source.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := source.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := clone.Truncate(copied); err != nil {
		t.Fatal(err)
	}
	request := struct {
		source                     windows.Handle
		sourceOffset, targetOffset int64
		byteCount                  int64
	}{source: windows.Handle(source.Fd()), byteCount: copied}
	var used uint32
	if err := windows.DeviceIoControl(windows.Handle(clone.Fd()), windows.FSCTL_DUPLICATE_EXTENTS_TO_FILE,
		(*byte)(unsafe.Pointer(&request)), uint32(unsafe.Sizeof(request)), nil, 0, &used, nil); err != nil {
		t.Fatal(err)
	}
	check := func(want int64) {
		t.Helper()
		got := MeasureReFSExclusive(t.Context(), worktree)
		if got == nil {
			t.Fatalf("exclusive is unknown, want %d bytes", want)
		}
		if *got != want {
			t.Fatalf("exclusive = %d, want %d", *got, want)
		}
	}
	check(0)
	if _, err := clone.WriteAt([]byte("changed"), 0); err != nil {
		t.Fatal(err)
	}
	if err := clone.Sync(); err != nil {
		t.Fatal(err)
	}
	check(probe.clusterBytes)
	if probe.hardLinks {
		linked := filepath.Join(root, "linked")
		defer os.Remove(linked)
		if err := os.Link(clone.Name(), linked); err != nil {
			t.Fatal(err)
		}
		check(0)
	}
}

func TestReFSExtentsCountsOnlyAllocatedUniqueClusters(t *testing.T) {
	data := make([]byte, 16+3*24)
	binary.LittleEndian.PutUint32(data[:4], 3)
	for i, entry := range [][3]int64{{5, 100, 1}, {7, 105, 2}, {9, -1, 0}} {
		row := data[16+i*24:]
		binary.LittleEndian.PutUint64(row, uint64(entry[0]))
		binary.LittleEndian.PutUint64(row[8:], uint64(entry[1]))
		binary.LittleEndian.PutUint32(row[16:], uint32(entry[2]))
	}
	next, bytes, ok := refsExtents(data, 2, 4096)
	if !ok || next != 9 || bytes != 3*4096 {
		t.Fatalf("extents: %d, %d, %v", next, bytes, ok)
	}
	if _, _, ok := refsExtents(data[:len(data)-1], 2, 4096); ok {
		t.Fatal("accepted truncated extent data")
	}
}

func TestMeasureStorageWindowsSparseAllocation(t *testing.T) {
	root := t.TempDir()
	file, err := os.Create(filepath.Join(root, "sparse"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	var returned uint32
	if err := windows.DeviceIoControl(windows.Handle(file.Fd()), windows.FSCTL_SET_SPARSE,
		nil, 0, nil, 0, &returned, nil); err != nil {
		t.Skipf("test volume does not support sparse files: %v", err)
	}
	if err := file.Truncate(64 << 20); err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte("payload"), 4096); err != nil {
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	result := MeasureStorage(t.Context(), root, []string{root})
	if result.Logical || result.Managed == nil || *result.Managed <= 0 || *result.Managed >= 64<<20 {
		t.Fatalf("unexpected sparse allocation: %+v", result)
	}
}

func TestWindowsDirectoryEntryDoesNotFollowReplacedPath(t *testing.T) {
	root, target := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "payload"), make([]byte, 64<<10), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "subdirectory")
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("test volume does not support symlinks: %v", err)
	}
	counter := &sizeCounter{}
	scan := directoryScan{}
	// The parent listing saw a subdirectory before it was replaced by the link.
	scan.visitDirectory(t.Context(), path, nil, []*sizeCounter{counter}, false)
	if counter.bytes != 0 || counter.logical != 0 {
		t.Fatalf("followed link to another directory: %+v", counter)
	}
}
