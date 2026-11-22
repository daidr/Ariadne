package fsutil

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func refsTestRoot(t *testing.T) (string, *refsProbe) {
	t.Helper()
	root := t.TempDir()
	if volume := os.Getenv("ARIADNE_TEST_REFS_ROOT"); volume != "" {
		var err error
		root, err = os.MkdirTemp(volume, "ariadne-refs-test-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := os.RemoveAll(root); err != nil {
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
	return root, probe
}

func cloneReFSFile(t *testing.T, source *os.File, targetPath string, size int64) *os.File {
	t.Helper()
	clone, err := os.Create(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := clone.Truncate(size); err != nil {
		_ = clone.Close()
		t.Fatal(err)
	}
	request := struct {
		source                     windows.Handle
		sourceOffset, targetOffset int64
		byteCount                  int64
	}{source: windows.Handle(source.Fd()), byteCount: size}
	var used uint32
	if err := windows.DeviceIoControl(windows.Handle(clone.Fd()), windows.FSCTL_DUPLICATE_EXTENTS_TO_FILE,
		(*byte)(unsafe.Pointer(&request)), uint32(unsafe.Sizeof(request)), nil, 0, &used, nil); err != nil {
		_ = clone.Close()
		t.Fatal(err)
	}
	return clone
}

func TestReFSExclusiveExcludesSharedBlocks(t *testing.T) {
	root, probe := refsTestRoot(t)
	worktree := filepath.Join(root, "worktree")
	if err := os.Mkdir(worktree, 0700); err != nil {
		t.Fatal(err)
	}
	source, err := os.Create(filepath.Join(root, "source"))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
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
	clone := cloneReFSFile(t, source, filepath.Join(worktree, "clone"), copied)
	defer clone.Close()
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
		if err := os.Link(clone.Name(), linked); err != nil {
			t.Fatal(err)
		}
		check(0)
	}
}

func TestReFSExclusiveMixedTreeAndInlineFastPath(t *testing.T) {
	root, probe := refsTestRoot(t)
	worktree := filepath.Join(root, "worktree")
	subA := filepath.Join(worktree, "sub-a", "nested")
	subB := filepath.Join(worktree, "sub-b")
	for _, dir := range []string{subA, subB} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}

	writeSynced := func(path string, content []byte) {
		t.Helper()
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(content) > 0 {
			if _, err := file.Write(content); err != nil {
				_ = file.Close()
				t.Fatal(err)
			}
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}

	// Empty and tiny inline files (< clusterBytes) contribute 0 exclusive cluster bytes.
	writeSynced(filepath.Join(worktree, "empty"), nil)
	writeSynced(filepath.Join(subA, "tiny1"), []byte("x"))
	writeSynced(filepath.Join(subA, "tiny100"), make([]byte, 100))

	// Multi-cluster exclusive files across subdirectories.
	payload3 := make([]byte, 3*probe.clusterBytes)
	for i := range payload3 {
		payload3[i] = byte(i % 239)
	}
	writeSynced(filepath.Join(subA, "exclusive3"), payload3)
	payload2 := make([]byte, 2*probe.clusterBytes)
	for i := range payload2 {
		payload2[i] = byte((i + 7) % 239)
	}
	writeSynced(filepath.Join(subB, "exclusive2"), payload2)

	// Block-cloned file (fully shared -> 0) and partially modified clone (1 cluster exclusive).
	source, err := os.Create(filepath.Join(root, "source"))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	clonedSize := 4 * probe.clusterBytes
	clonedData := make([]byte, clonedSize)
	for i := range clonedData {
		clonedData[i] = byte(i % 251)
	}
	if _, err := source.Write(clonedData); err != nil {
		t.Fatal(err)
	}
	if err := source.Sync(); err != nil {
		t.Fatal(err)
	}
	sharedClone := cloneReFSFile(t, source, filepath.Join(subB, "shared"), clonedSize)
	_ = sharedClone.Close()

	dirtyClone := cloneReFSFile(t, source, filepath.Join(subA, "dirty"), clonedSize)
	if _, err := dirtyClone.WriteAt([]byte("modified"), 0); err != nil {
		_ = dirtyClone.Close()
		t.Fatal(err)
	}
	if err := dirtyClone.Sync(); err != nil {
		_ = dirtyClone.Close()
		t.Fatal(err)
	}
	_ = dirtyClone.Close()

	// Hard-linked file contributes 0 exclusive bytes when volume supports hard links.
	if probe.hardLinks {
		linkedSrc := filepath.Join(root, "hardlink-outside")
		writeSynced(linkedSrc, payload2)
		if err := os.Link(linkedSrc, filepath.Join(subB, "hardlink-inside")); err != nil {
			t.Fatal(err)
		}
	}

	// Sparse file with 1 written cluster contributes 1 cluster exclusive.
	sparsePath := filepath.Join(subB, "sparse")
	sparseFile, err := os.Create(sparsePath)
	if err != nil {
		t.Fatal(err)
	}
	var ret uint32
	if err := windows.DeviceIoControl(windows.Handle(sparseFile.Fd()), windows.FSCTL_SET_SPARSE,
		nil, 0, nil, 0, &ret, nil); err != nil {
		_ = sparseFile.Close()
		t.Fatalf("FSCTL_SET_SPARSE failed: %v", err)
	}
	if err := sparseFile.Truncate(16 * probe.clusterBytes); err != nil {
		_ = sparseFile.Close()
		t.Fatal(err)
	}
	if _, err := sparseFile.WriteAt([]byte("sparse"), probe.clusterBytes); err != nil {
		_ = sparseFile.Close()
		t.Fatal(err)
	}
	if err := sparseFile.Sync(); err != nil {
		_ = sparseFile.Close()
		t.Fatal(err)
	}
	_ = sparseFile.Close()

	// Symlink to external directory must not be followed.
	external := filepath.Join(root, "external")
	if err := os.Mkdir(external, 0700); err != nil {
		t.Fatal(err)
	}
	writeSynced(filepath.Join(external, "outside"), payload3)
	_ = os.Symlink(external, filepath.Join(worktree, "symlink-dir"))

	// Expected: exclusive3 (3) + exclusive2 (2) + dirty (1) + sparse (1) = 7 clusters.
	want := 7 * probe.clusterBytes
	got := MeasureReFSExclusive(t.Context(), worktree)
	if got == nil {
		t.Fatalf("exclusive is unknown, want %d", want)
	}
	if *got != want {
		t.Fatalf("exclusive = %d, want %d (clusterBytes=%d)", *got, want, probe.clusterBytes)
	}
}

func TestReFSExclusiveEmptyInlineAndCanceled(t *testing.T) {
	root, _ := refsTestRoot(t)

	emptyDir := filepath.Join(root, "empty")
	if err := os.Mkdir(emptyDir, 0700); err != nil {
		t.Fatal(err)
	}
	gotEmpty := MeasureReFSExclusive(t.Context(), emptyDir)
	if gotEmpty == nil || *gotEmpty != 0 {
		t.Fatalf("empty dir exclusive = %v, want 0", gotEmpty)
	}

	inlineDir := filepath.Join(root, "inline-only")
	if err := os.Mkdir(inlineDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inlineDir, "tiny.txt"), []byte("hi"), 0600); err != nil {
		t.Fatal(err)
	}
	gotInline := MeasureReFSExclusive(t.Context(), inlineDir)
	if gotInline == nil || *gotInline != 0 {
		t.Fatalf("inline-only dir exclusive = %v, want 0", gotInline)
	}

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if got := MeasureReFSExclusive(canceled, inlineDir); got != nil {
		t.Fatalf("canceled scan returned %d, want nil", *got)
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

func TestMeasureStorageWindowsParallelNestedScopesAndHardLinks(t *testing.T) {
	root := t.TempDir()
	managed := filepath.Join(root, "managed")
	wt1 := filepath.Join(managed, "worktrees", "wt1")
	wt2 := filepath.Join(managed, "worktrees", "wt2")
	for _, dir := range []string{
		filepath.Join(managed, "plugins", "a"),
		filepath.Join(wt1, "src", "deep"),
		filepath.Join(wt2, "src", "deep"),
	} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	chunk := make([]byte, 16<<10)
	for i := range chunk {
		chunk[i] = byte(i % 251)
	}
	pluginFile := filepath.Join(managed, "plugins", "a", "plugin.bin")
	if err := os.WriteFile(pluginFile, chunk, 0600); err != nil {
		t.Fatal(err)
	}
	wt1File := filepath.Join(wt1, "src", "deep", "shared.bin")
	if err := os.WriteFile(wt1File, chunk, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(wt1File, filepath.Join(wt2, "src", "deep", "shared.bin")); err != nil {
		t.Skipf("test volume does not support hard links: %v", err)
	}
	for i := range 8 {
		sub := filepath.Join(wt1, fmt.Sprintf("dir-%d", i))
		if err := os.Mkdir(sub, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sub, "f.bin"), chunk, 0600); err != nil {
			t.Fatal(err)
		}
	}

	result := MeasureStorage(t.Context(), managed, []string{wt1, wt2})
	if result.Logical {
		t.Fatalf("expected native allocation scan, got logical fallback: %+v", result)
	}
	size1 := result.Worktrees[wt1].Bytes
	size2 := result.Worktrees[wt2].Bytes
	if size1 == nil || size2 == nil || result.Total == nil || result.Managed == nil {
		t.Fatalf("missing sizes in result: %+v", result)
	}
	// wt1 has 9 files (1 shared + 8 unique), wt2 has 1 shared file (hard link to wt1).
	// Total deduplicates the hard link across wt1 and wt2, so Total == size1.
	if *result.Total != *size1 {
		t.Fatalf("total = %d, want %d (wt1=%d, wt2=%d)", *result.Total, *size1, *size1, *size2)
	}
	if *result.Managed != *size1+*size2 {
		// pluginFile has same allocated size as wt2's single file.
		t.Fatalf("managed = %d, want %d", *result.Managed, *size1+*size2)
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

func benchmarkRoot(b *testing.B) string {
	b.Helper()
	if dir := os.Getenv("ARIADNE_BENCH_DIR"); dir != "" {
		return dir
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		b.Fatal(err)
	}
	return root
}

func BenchmarkMeasureStorageWindows(b *testing.B) {
	root := benchmarkRoot(b)
	managed := filepath.Join(root, ".ariadne-bench-none")
	b.ResetTimer()
	for b.Loop() {
		res := MeasureStorage(b.Context(), managed, []string{root})
		if res.Worktrees[root].Bytes == nil {
			b.Fatal("MeasureStorage returned nil bytes")
		}
	}
}

func BenchmarkMeasureReFSExclusive(b *testing.B) {
	root := benchmarkRoot(b)
	if newReFSProbe(root) == nil {
		b.Skip("benchmark directory is not on ReFS")
	}
	b.ResetTimer()
	for b.Loop() {
		if MeasureReFSExclusive(b.Context(), root) == nil {
			b.Fatal("MeasureReFSExclusive returned nil")
		}
	}
}
