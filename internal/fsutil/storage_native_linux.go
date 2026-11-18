package fsutil

import (
	"context"
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

type nativeDirectory struct {
	ext4 map[uint64]bool
}

// ext4 directory entries do not contain block counts. Resolve each name
// against the already-open directory rather than walking its parent path again.
func (scan *directoryScan) readNative(ctx context.Context, path string, info fs.FileInfo, counts []*sizeCounter, inWorktree bool) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	device := uint64(stat.Dev)
	if scan.native.ext4 == nil {
		scan.native.ext4 = make(map[uint64]bool)
	}
	supported, checked := scan.native.ext4[device]
	if !checked {
		var volume unix.Statfs_t
		if unix.Statfs(path, &volume) != nil {
			return false
		}
		supported = uint32(volume.Type) == unix.EXT4_SUPER_MAGIC
		scan.native.ext4[device] = supported
	}
	if !supported {
		return false
	}
	scan.readExt4(ctx, device, ext4Job{path: path, counts: counts, inWorktree: inWorktree})
	return true
}

type ext4Job struct {
	path       string
	counts     []*sizeCounter
	inWorktree bool
}

type ext4Subdirectory struct {
	name   string
	device uint64
	bytes  int64
}

type ext4Link struct {
	id    fileIdentity
	bytes int64
	nlink uint32
}

type ext4Batch struct {
	job             ext4Job
	bytes           int64
	singleLinkBytes int64
	links           []ext4Link
	dirs            []ext4Subdirectory
	failed          bool
	foreignFile     bool
}

// Workers read metadata only. The caller applies batches and owns all counters,
// so hard links spanning directories are still deduplicated within each scope.
func (scan *directoryScan) readExt4(ctx context.Context, device uint64, root ext4Job) {
	workers := min(runtime.GOMAXPROCS(0), 4)
	jobs := make(chan ext4Job)
	results := make(chan ext4Batch, workers)
	var wait sync.WaitGroup
	for range workers {
		wait.Go(func() {
			for job := range jobs {
				results <- readExt4Batch(ctx, job, device)
			}
		})
	}

	pending := []ext4Job{root}
	var otherVolumes []ext4Job
	active := 0
	for len(pending) > 0 || active > 0 {
		if ctx.Err() != nil {
			for _, job := range pending {
				markFailed(job.counts)
			}
			pending = nil
		}
		if len(pending) > 0 && active < workers {
			// Process newly discovered directories first to keep the frontier small.
			last := len(pending) - 1
			jobs <- pending[last]
			pending[last] = ext4Job{}
			pending = pending[:last]
			active++
			continue
		}
		if active == 0 {
			break
		}
		batch := <-results
		active--
		if batch.failed {
			markFailed(batch.job.counts)
		}
		if batch.foreignFile {
			markReclaimIncomplete(batch.job.counts)
		}
		for _, counter := range batch.job.counts {
			counter.add(batch.bytes, 0, fileIdentity{}, nil)
			if estimate := counter.reclaim; estimate != nil {
				estimate.measured = true
				estimate.singleLinkBytes += batch.singleLinkBytes
			}
			for _, link := range batch.links {
				counter.addReclaimLink(link.id, link.bytes, link.nlink)
			}
		}
		for _, child := range batch.dirs {
			path := filepath.Join(batch.job.path, child.name)
			if child.device != device {
				markReclaimIncomplete(batch.job.counts)
				otherVolumes = append(otherVolumes, ext4Job{path, batch.job.counts, batch.job.inWorktree})
				continue
			}
			counts, inWorktree := scan.directoryCounters(path, batch.job.counts, batch.job.inWorktree)
			for _, counter := range counts {
				counter.found = true
				counter.add(child.bytes, 0, fileIdentity{}, nil)
			}
			pending = append(pending, ext4Job{path, counts, inWorktree})
		}
	}
	close(jobs)
	wait.Wait()

	// Run the other filesystem's scanner after the ext4 workers have exited.
	for _, job := range otherVolumes {
		info, err := os.Lstat(job.path)
		if err == nil && info.IsDir() {
			scan.visitDirectory(ctx, job.path, info, job.counts, job.inWorktree)
		} else if err != nil && !os.IsNotExist(err) {
			markFailed(job.counts)
		}
	}
}

func readExt4Batch(ctx context.Context, job ext4Job, device uint64) ext4Batch {
	batch := ext4Batch{job: job}
	if ctx.Err() != nil {
		batch.failed = true
		return batch
	}
	fd, err := unix.Open(job.path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		batch.failed = true
		return batch
	}
	dir := os.NewFile(uintptr(fd), job.path)
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		batch.failed = true
		return batch
	}
	for _, entry := range entries {
		if ctx.Err() != nil {
			batch.failed = true
			break
		}
		var child unix.Stat_t
		err := unix.Fstatat(fd, entry.Name(), &child, unix.AT_SYMLINK_NOFOLLOW)
		if errors.Is(err, unix.ENOENT) {
			continue
		}
		if err != nil || child.Blocks < 0 || child.Blocks > math.MaxInt64/512 {
			batch.failed = true
			continue
		}
		bytes := child.Blocks * 512
		mode := child.Mode & unix.S_IFMT
		if mode == unix.S_IFDIR {
			batch.dirs = append(batch.dirs, ext4Subdirectory{entry.Name(), uint64(child.Dev), bytes})
			continue
		}
		var nlink uint32
		if job.inWorktree {
			if uint64(child.Dev) != device {
				batch.foreignFile = true
			} else if mode == unix.S_IFREG {
				nlink = uint32(child.Nlink)
			}
		}
		switch {
		case child.Nlink > 1:
			id := fileIdentity{device: uint64(child.Dev), file: uint64(child.Ino)}
			batch.links = append(batch.links, ext4Link{id, bytes, nlink})
		case bytes > math.MaxInt64-batch.bytes:
			batch.failed = true
		default:
			batch.bytes += bytes
			if nlink == 1 {
				batch.singleLinkBytes += bytes
			}
		}
	}
	return batch
}
