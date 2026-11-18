package fsutil

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDiskUsageReadsMetadataWithoutFollowingSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "file"), make([]byte, 8192), 0o600); err != nil {
		t.Fatal(err)
	}
	before := DiskUsage(t.Context(), root)
	if before == nil || *before < 8192 || runtime.GOOS == "windows" && *before != 8192 {
		t.Fatalf("unexpected size %v", before)
	}
	outside := filepath.Join(t.TempDir(), "external")
	if err := os.WriteFile(outside, make([]byte, 64<<10), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "alias")); err == nil {
		after := DiskUsage(t.Context(), root)
		if after == nil || *after >= *before+(64<<10) {
			t.Fatalf("directory scan followed a symlink: %v", after)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if size := DiskUsage(ctx, root); size != nil {
		t.Fatal("canceled scan returned a size")
	}
}
