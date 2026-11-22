package fsutil

import (
	"context"
	"encoding/binary"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

type nativeDirectory struct{}

var getCompressedFileSize = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetCompressedFileSizeW")

func windowsPath(path string) (*uint16, error) {
	if filepath.IsAbs(path) && !strings.HasPrefix(path, `\\?\`) {
		if strings.HasPrefix(path, `\\`) {
			path = `\\?\UNC\` + path[2:]
		} else {
			path = `\\?\` + path
		}
	}
	return windows.UTF16PtrFromString(path)
}

func openWindowsDirectory(path string) (windows.Handle, error) {
	name, err := windowsPath(path)
	if err != nil {
		return 0, err
	}
	return windows.CreateFile(name, windows.FILE_LIST_DIRECTORY|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
}

func isDotDir(chars []uint16) bool {
	return (len(chars) == 1 && chars[0] == '.') || (len(chars) == 2 && chars[0] == '.' && chars[1] == '.')
}

func windowsWorkers() int {
	return max(1, min(runtime.GOMAXPROCS(0), 4))
}

type winDirJob struct {
	path       string
	counts     []*sizeCounter
	inWorktree bool
}

type winFileEntry struct {
	bytes, logical int64
	id             fileIdentity
}

type winDirBatch struct {
	job     winDirJob
	files   []winFileEntry
	dirs    []string
	logical bool
	read    bool
	missing bool
	notDir  bool
	failed  bool
}

// Batch directory metadata, including allocation size and the 128-bit file ID,
// across a bounded worker pool without opening regular files or needing raw
// volume access. All non-reparse descendants live on the root handle's volume,
// so FileIdInfo is queried once per root.
func (scan *directoryScan) readNative(ctx context.Context, path string, _ fs.FileInfo, counts []*sizeCounter, inWorktree bool) bool {
	handle, err := openWindowsDirectory(path)
	if err != nil {
		return false
	}
	var volumeInfo [3]uint64
	if windows.GetFileInformationByHandleEx(handle, windows.FileIdInfo, (*byte)(unsafe.Pointer(&volumeInfo[0])), uint32(unsafe.Sizeof(volumeInfo))) != nil {
		_ = windows.CloseHandle(handle)
		return false
	}
	device := volumeInfo[0]

	var rootBuf [8 << 10]uint64 // Windows directory records require 8-byte alignment.
	rootJob := winDirJob{path: path, counts: counts, inWorktree: inWorktree}
	rootBatch := readWinDirHandle(ctx, handle, rootJob, device, &rootBuf)
	if !rootBatch.read {
		return false
	}
	scan.applyWinDirBatch(rootBatch)
	if len(rootBatch.dirs) == 0 {
		return true
	}

	workers := windowsWorkers()
	jobs := make(chan winDirJob)
	results := make(chan winDirBatch, workers)
	var wait sync.WaitGroup
	for range workers {
		wait.Go(func() {
			var buf [8 << 10]uint64
			for job := range jobs {
				results <- readWinDirBatch(ctx, job, device, &buf)
			}
		})
	}

	pending := make([]winDirJob, 0, len(rootBatch.dirs))
	for _, name := range rootBatch.dirs {
		childPath := filepath.Join(path, name)
		childCounts, childInWorktree := scan.directoryCounters(childPath, counts, inWorktree)
		pending = append(pending, winDirJob{path: childPath, counts: childCounts, inWorktree: childInWorktree})
	}

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
			pending[last] = winDirJob{}
			pending = pending[:last]
			active++
			continue
		}
		if active == 0 {
			break
		}
		batch := <-results
		active--
		if batch.missing || batch.notDir {
			continue
		}
		for _, counter := range batch.job.counts {
			counter.found = true
		}
		scan.applyWinDirBatch(batch)
		for _, name := range batch.dirs {
			childPath := filepath.Join(batch.job.path, name)
			childCounts, childInWorktree := scan.directoryCounters(childPath, batch.job.counts, batch.job.inWorktree)
			pending = append(pending, winDirJob{path: childPath, counts: childCounts, inWorktree: childInWorktree})
		}
	}
	close(jobs)
	wait.Wait()
	return true
}

func (scan *directoryScan) applyWinDirBatch(batch winDirBatch) {
	if batch.logical {
		scan.logical = true
	}
	if batch.failed {
		markFailed(batch.job.counts)
	}
	for _, entry := range batch.files {
		for _, counter := range batch.job.counts {
			counter.add(entry.bytes, entry.logical, entry.id, nil)
		}
	}
}

func readWinDirBatch(ctx context.Context, job winDirJob, device uint64, buf *[8 << 10]uint64) winDirBatch {
	if ctx.Err() != nil {
		return winDirBatch{job: job, failed: true}
	}
	handle, err := openWindowsDirectory(job.path)
	if err != nil {
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			return winDirBatch{job: job, missing: true}
		}
		return classifyWinDirOpenFailure(job)
	}
	batch := readWinDirHandle(ctx, handle, job, device, buf)
	if !batch.read {
		return classifyWinDirOpenFailure(job)
	}
	return batch
}

func classifyWinDirOpenFailure(job winDirJob) winDirBatch {
	info, err := os.Lstat(job.path)
	if errors.Is(err, fs.ErrNotExist) {
		return winDirBatch{job: job, missing: true}
	}
	if err == nil && !info.IsDir() {
		return winDirBatch{job: job, notDir: true}
	}
	return winDirBatch{job: job, failed: true}
}

func readWinDirHandle(ctx context.Context, handle windows.Handle, job winDirJob, device uint64, buffer *[8 << 10]uint64) winDirBatch {
	defer windows.CloseHandle(handle)
	batch := winDirBatch{job: job}
	raw := unsafe.Slice((*byte)(unsafe.Pointer(&buffer[0])), int(unsafe.Sizeof(*buffer)))
	for ctx.Err() == nil {
		err := windows.GetFileInformationByHandleEx(handle, windows.FileIdExtdDirectoryInfo, &raw[0], uint32(len(raw)))
		if err == windows.ERROR_NO_MORE_FILES || err == windows.ERROR_FILE_NOT_FOUND {
			batch.read = true
			return batch
		}
		if err != nil {
			if batch.read {
				batch.failed = true
			}
			return batch
		}
		batch.read = true
		if !parseWinDirEntries(raw, device, job.path, len(job.counts) > 0, &batch) {
			batch.failed = true
			return batch
		}
	}
	batch.failed = true
	return batch
}

func parseWinDirEntries(buf []byte, device uint64, path string, wantFiles bool, batch *winDirBatch) bool {
	// Offsets of FILE_ID_EXTD_DIR_INFO, including FileName after FileId.
	const header = 88
	for len(buf) >= header {
		offset := int(binary.LittleEndian.Uint32(buf[:4]))
		attrs := binary.LittleEndian.Uint32(buf[56:60])
		nameBytes := int(binary.LittleEndian.Uint32(buf[60:64]))
		if nameBytes < 2 || nameBytes%2 != 0 || nameBytes > len(buf)-header ||
			(offset != 0 && (offset < header+nameBytes || offset > len(buf))) {
			return false
		}
		if attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0 {
			if attrs&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
				chars := unsafe.Slice((*uint16)(unsafe.Pointer(&buf[header])), nameBytes/2)
				if !isDotDir(chars) {
					batch.dirs = append(batch.dirs, string(utf16.Decode(chars)))
				}
			} else if wantFiles {
				logical := int64(binary.LittleEndian.Uint64(buf[40:48]))
				bytes := int64(binary.LittleEndian.Uint64(buf[48:56]))
				if attrs&(windows.FILE_ATTRIBUTE_SPARSE_FILE|windows.FILE_ATTRIBUTE_COMPRESSED) != 0 {
					chars := unsafe.Slice((*uint16)(unsafe.Pointer(&buf[header])), nameBytes/2)
					var ok bool
					bytes, ok = compressedSize(filepath.Join(path, string(utf16.Decode(chars))))
					if !ok {
						batch.logical = true
						bytes = logical
					}
				}
				id := fileIdentity{
					device: device,
					file:   binary.LittleEndian.Uint64(buf[72:80]),
					high:   binary.LittleEndian.Uint64(buf[80:88]),
				}
				if id.file == 0 && id.high == 0 {
					id = fileIdentity{}
				}
				batch.files = append(batch.files, winFileEntry{bytes: bytes, logical: logical, id: id})
			}
		}
		if offset == 0 {
			return true
		}
		buf = buf[offset:]
	}
	return false
}

func compressedSize(path string) (int64, bool) {
	name, err := windowsPath(path)
	if err != nil {
		return 0, false
	}
	var high uint32
	low, _, callErr := getCompressedFileSize.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&high)))
	if uint32(low) == 0xffffffff && callErr != syscall.Errno(0) {
		return 0, false
	}
	return int64(uint64(high)<<32 | uint64(uint32(low))), true
}
