package fsutil

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

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
