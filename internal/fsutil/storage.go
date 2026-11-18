package fsutil

import (
	"context"
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"runtime"
)

// DirectorySize reports per-file allocation, with a logical-length fallback on Windows.
// Exclusive is a lower bound on releasable data blocks, when the filesystem
// can identify them. It can exclude shared blocks released by a group deletion.
type DirectorySize struct {
	Bytes     *int64 `json:"bytes"`
	Exclusive *int64 `json:"exclusive"`
}

type StorageSizes struct {
	Managed   *int64                   `json:"managed"`
	Total     *int64                   `json:"total"`
	Worktrees map[string]DirectorySize `json:"worktrees"`
	Logical   bool                     `json:"logical"`
}

type sizeCounter struct {
	bytes, logical, exclusive int64
	seen                      map[fileIdentity]struct{}
	found, failed             bool
	hasExclusive              bool
}

func (counter *sizeCounter) add(bytes, logical int64, id fileIdentity, exclusive *int64) {
	if id != (fileIdentity{}) {
		if counter.seen == nil {
			counter.seen = make(map[fileIdentity]struct{})
		}
		if _, ok := counter.seen[id]; ok {
			return
		}
		counter.seen[id] = struct{}{}
	}
	if bytes < 0 || bytes > math.MaxInt64-counter.bytes || logical < 0 || logical > math.MaxInt64-counter.logical {
		counter.failed = true
		return
	}
	counter.bytes += bytes
	counter.logical += logical
	if exclusive != nil && *exclusive <= math.MaxInt64-counter.exclusive {
		counter.hasExclusive = true
		counter.exclusive += *exclusive
	}
}

func (counter *sizeCounter) size(logical bool) DirectorySize {
	if !counter.found || counter.failed {
		return DirectorySize{}
	}
	var result DirectorySize
	result.Bytes = new(counter.bytes)
	if logical {
		result.Bytes = new(counter.logical)
	}
	if counter.hasExclusive {
		result.Exclusive = new(counter.exclusive)
	}
	return result
}

type directoryScan struct {
	root      string
	managed   *sizeCounter
	total     *sizeCounter
	worktrees map[string]*sizeCounter
	exclusive exclusiveBlocks
	native    nativeDirectory
	logical   bool
}

// MeasureStorage visits the union of the managed directory and worktrees once.
// Each visited entry contributes to the applicable totals, even when a worktree
// lives inside the managed directory. Hard links are deduplicated per total.
func MeasureStorage(ctx context.Context, managed string, worktrees []string) StorageSizes {
	result := StorageSizes{Worktrees: make(map[string]DirectorySize)}
	select {
	case diskScans <- struct{}{}:
		defer func() { <-diskScans }()
	case <-ctx.Done():
		return result
	}
	clean := func(path string) string {
		if info, err := os.Lstat(path); err == nil && !info.IsDir() {
			return filepath.Clean(path)
		}
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			return resolved
		}
		return filepath.Clean(path)
	}
	managed = clean(managed)
	scan := directoryScan{root: managed, managed: &sizeCounter{}, total: &sizeCounter{}, worktrees: map[string]*sizeCounter{}}
	scopePaths := map[string]struct{}{managed: {}}
	aliases := make(map[string]string, len(worktrees))
	for _, path := range worktrees {
		resolved := clean(path)
		aliases[path] = resolved
		scopePaths[resolved] = struct{}{}
		scan.worktrees[resolved] = &sizeCounter{}
	}
	for path := range scopePaths {
		if hasParentScope(path, scopePaths) {
			continue
		}
		info, err := os.Lstat(path)
		if err == nil && !info.IsDir() {
			if path == managed {
				scan.managed.failed = true
			}
			continue
		}
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) && path == managed {
				scan.managed.failed = true
			}
			continue
		}
		scan.visit(ctx, path, info, nil, false)
	}
	for path, resolved := range aliases {
		counter := scan.worktrees[resolved]
		result.Worktrees[path] = counter.size(scan.logical)
		if !counter.found {
			scan.total.failed = true
		}
	}
	scan.managed.found = true // A missing managed folder is empty.
	if ctx.Err() != nil {
		// A canceled scan cannot establish that an unchecked folder was empty.
		scan.managed.failed = true
	}
	scan.total.found = true
	result.Managed = scan.managed.size(scan.logical).Bytes
	result.Total = scan.total.size(scan.logical).Bytes
	result.Logical = scan.logical
	return result
}

func hasParentScope(path string, scopes map[string]struct{}) bool {
	for parent := filepath.Dir(path); parent != path; parent = filepath.Dir(path) {
		if _, ok := scopes[parent]; ok {
			return true
		}
		path = parent
	}
	return false
}

func (scan *directoryScan) visit(ctx context.Context, path string, info fs.FileInfo, counts []*sizeCounter, inWorktree bool) {
	if info.IsDir() {
		scan.visitDirectory(ctx, path, info, counts, inWorktree)
		return
	}
	if ctx.Err() != nil {
		markFailed(counts)
		return
	}
	bytes, id := diskEntry(info)
	var logical int64
	var exclusive *int64
	if info.Mode().IsRegular() {
		logical = info.Size()
		if runtime.GOOS == "windows" && len(counts) > 0 {
			scan.logical = true // Portable fallback has file lengths only.
		}
		if inWorktree && bytes > 0 && id == (fileIdentity{}) {
			exclusive = scan.exclusive.read(ctx, path, info)
		}
	}
	for _, counter := range counts {
		counter.add(bytes, logical, id, exclusive)
	}
}

func (scan *directoryScan) directoryCounters(path string, counts []*sizeCounter, inWorktree bool) ([]*sizeCounter, bool) {
	// Children can share their parent's scope list without modifying it.
	counts = counts[:len(counts):len(counts)]
	if path == scan.root {
		counts = append(counts, scan.managed)
	}
	if counter := scan.worktrees[path]; counter != nil {
		counts = append(counts, counter)
		if !inWorktree {
			counts = append(counts, scan.total)
		}
		inWorktree = true
	}
	return counts, inWorktree
}

// Windows directory entries already identify subdirectories. Their allocation
// is ignored, so the native scanner can open them directly without an Lstat.
func (scan *directoryScan) visitDirectory(ctx context.Context, path string, info fs.FileInfo, counts []*sizeCounter, inWorktree bool) {
	counts, inWorktree = scan.directoryCounters(path, counts, inWorktree)
	if ctx.Err() != nil {
		markFailed(counts)
		return
	}
	if info != nil {
		bytes, id := diskEntry(info)
		for _, counter := range counts {
			counter.add(bytes, 0, id, nil)
		}
	}
	handled := scan.readNative(ctx, path, info, counts, inWorktree)
	if !handled && info == nil {
		// Revalidate before using the portable fallback, which opens by path.
		var err error
		info, err = os.Lstat(path)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			markFailed(counts)
		}
		if err != nil || !info.IsDir() {
			return
		}
	}
	for _, counter := range counts {
		counter.found = true
	}
	if handled {
		return
	}

	dir, err := os.Open(path)
	if err != nil {
		markFailed(counts)
		return
	}
	entries, err := dir.ReadDir(-1) // The order on disk suffices here.
	_ = dir.Close()
	if err != nil {
		markFailed(counts)
		return
	}
	for _, entry := range entries {
		if ctx.Err() != nil {
			markFailed(counts)
			return
		}
		info, err := entry.Info()
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			markFailed(counts)
			continue
		}
		scan.visit(ctx, filepath.Join(path, entry.Name()), info, counts, inWorktree)
	}
}

func markFailed(counts []*sizeCounter) {
	for _, counter := range counts {
		counter.failed = true
	}
}
