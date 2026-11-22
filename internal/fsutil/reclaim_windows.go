package fsutil

import (
	"context"
	"encoding/binary"
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

var getDiskFreeSpace = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetDiskFreeSpaceW")

type refsProbe struct {
	clusterBytes int64
	hardLinks    bool
}

type refsBatch struct {
	exclusive       int64
	dirs            []string
	hasExclusive    bool
	sawNonZeroBytes bool
	read            bool
	missing         bool
	notDir          bool
	failed          bool
}

// MeasureReFSExclusive counts allocated extents in files with a single link
// and a single block reference. Shared extents contribute zero to this bound.
func MeasureReFSExclusive(ctx context.Context, path string) *int64 {
	probe := newReFSProbe(path)
	if probe == nil {
		return nil
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() {
		return nil
	}
	select {
	case diskScans <- struct{}{}:
		defer func() { <-diskScans }()
	case <-ctx.Done():
		return nil
	}
	handle, err := openWindowsDirectory(path)
	if err != nil {
		return nil
	}

	var dirBuf [8 << 10]uint64
	var ioctlBuf [4096]byte
	rootBatch := probe.readDirectoryHandle(ctx, handle, path, &dirBuf, &ioctlBuf)
	if !rootBatch.read {
		return nil
	}

	var (
		totalExclusive  int64
		hasExclusive    bool
		sawNonZeroBytes bool
		failed          bool
	)
	merge := func(batch refsBatch) {
		if batch.failed {
			failed = true
		}
		if batch.sawNonZeroBytes {
			sawNonZeroBytes = true
		}
		if batch.hasExclusive {
			if batch.exclusive > math.MaxInt64-totalExclusive {
				failed = true
			} else {
				totalExclusive += batch.exclusive
				hasExclusive = true
			}
		}
	}
	merge(rootBatch)

	if len(rootBatch.dirs) > 0 {
		workers := windowsWorkers()
		jobs := make(chan string)
		results := make(chan refsBatch, workers)
		var wait sync.WaitGroup
		for range workers {
			wait.Go(func() {
				var wDirBuf [8 << 10]uint64
				var wIoctlBuf [4096]byte
				for dir := range jobs {
					results <- probe.readDirectory(ctx, dir, &wDirBuf, &wIoctlBuf)
				}
			})
		}

		pending := rootBatch.dirs
		active := 0
		for len(pending) > 0 || active > 0 {
			if ctx.Err() != nil {
				if len(pending) > 0 {
					failed = true
					pending = nil
				}
			}
			if len(pending) > 0 && active < workers {
				last := len(pending) - 1
				jobs <- pending[last]
				pending[last] = ""
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
			merge(batch)
			if ctx.Err() == nil && len(batch.dirs) > 0 {
				pending = append(pending, batch.dirs...)
			} else if len(batch.dirs) > 0 {
				failed = true
			}
		}
		close(jobs)
		wait.Wait()
	}

	if totalExclusive > 0 || hasExclusive && !failed {
		// Confirmed extents still form a lower bound if the deadline ended the scan.
		return new(totalExclusive)
	}
	if !failed && !sawNonZeroBytes {
		return new(int64(0))
	}
	return nil
}

func newReFSProbe(path string) *refsProbe {
	name, err := windowsPath(path)
	if err != nil {
		return nil
	}
	var root [32768]uint16
	if windows.GetVolumePathName(name, &root[0], uint32(len(root))) != nil {
		return nil
	}
	var filesystem [32]uint16
	var flags uint32
	if windows.GetVolumeInformation(&root[0], nil, 0, nil, nil, &flags, &filesystem[0], uint32(len(filesystem))) != nil ||
		!strings.EqualFold(windows.UTF16ToString(filesystem[:]), "ReFS") {
		return nil
	}
	var sectors, sectorBytes uint32
	ok, _, _ := getDiskFreeSpace.Call(uintptr(unsafe.Pointer(&root[0])), uintptr(unsafe.Pointer(&sectors)),
		uintptr(unsafe.Pointer(&sectorBytes)), 0, 0)
	if size := uint64(sectors) * uint64(sectorBytes); ok != 0 && size > 0 && size <= math.MaxInt64 {
		return &refsProbe{clusterBytes: int64(size), hardLinks: flags&windows.FILE_SUPPORTS_HARD_LINKS != 0}
	}
	return nil
}

func (probe *refsProbe) readDirectory(ctx context.Context, dirPath string, dirBuf *[8 << 10]uint64, ioctlBuf *[4096]byte) refsBatch {
	if ctx.Err() != nil {
		return refsBatch{failed: true}
	}
	handle, err := openWindowsDirectory(dirPath)
	if err != nil {
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			return refsBatch{missing: true}
		}
		return classifyReFSDirOpenFailure(dirPath)
	}
	batch := probe.readDirectoryHandle(ctx, handle, dirPath, dirBuf, ioctlBuf)
	if !batch.read {
		return classifyReFSDirOpenFailure(dirPath)
	}
	return batch
}

func classifyReFSDirOpenFailure(dirPath string) refsBatch {
	info, err := os.Lstat(dirPath)
	if errors.Is(err, fs.ErrNotExist) {
		return refsBatch{missing: true}
	}
	if err == nil && !info.IsDir() {
		return refsBatch{notDir: true}
	}
	return refsBatch{failed: true}
}

func (probe *refsProbe) readDirectoryHandle(ctx context.Context, handle windows.Handle, dirPath string, dirBuf *[8 << 10]uint64, ioctlBuf *[4096]byte) refsBatch {
	defer windows.CloseHandle(handle)
	var batch refsBatch
	raw := unsafe.Slice((*byte)(unsafe.Pointer(&dirBuf[0])), int(unsafe.Sizeof(*dirBuf)))
	for ctx.Err() == nil {
		// FileFullDirectoryInfo skips the 128-bit FileId and ReparsePointTag that
		// ReFS exclusive counting does not need, packing more entries per call.
		err := windows.GetFileInformationByHandleEx(handle, windows.FileFullDirectoryInfo, &raw[0], uint32(len(raw)))
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
		if !probe.scanEntries(ctx, handle, dirPath, raw, ioctlBuf, &batch) {
			batch.failed = true
			return batch
		}
	}
	batch.failed = true
	return batch
}

func (probe *refsProbe) scanEntries(ctx context.Context, dirHandle windows.Handle, dirPath string, buf []byte, ioctlBuf *[4096]byte, batch *refsBatch) bool {
	// Offsets of FILE_FULL_DIR_INFO: AllocationSize at 48, FileAttributes at 56,
	// FileNameLength at 60, EaSize at 64, FileName at 68.
	const header = 68
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
					batch.dirs = append(batch.dirs, filepath.Join(dirPath, string(utf16.Decode(chars))))
				}
			} else {
				bytes := int64(binary.LittleEndian.Uint64(buf[48:56]))
				exactBytes := true
				if attrs&(windows.FILE_ATTRIBUTE_SPARSE_FILE|windows.FILE_ATTRIBUTE_COMPRESSED) != 0 {
					chars := unsafe.Slice((*uint16)(unsafe.Pointer(&buf[header])), nameBytes/2)
					if compressed, ok := compressedSize(filepath.Join(dirPath, string(utf16.Decode(chars)))); ok {
						bytes = compressed
					} else {
						exactBytes = false
					}
				}
				if bytes > 0 || !exactBytes {
					batch.sawNonZeroBytes = true
				}
				// ReFS stores files smaller than a cluster inline without cluster extents.
				// By [MS-FSA] 2.1.5.10.16, AllocationSize < ClusterSize always yields EOF
				// (zero exclusive clusters), so skip opening the file entirely.
				if exactBytes && bytes >= 0 && bytes < probe.clusterBytes {
					batch.hasExclusive = true
				} else if ctx.Err() == nil {
					exclusive := probe.readRelative(ctx, dirHandle, buf[header:header+nameBytes], ioctlBuf)
					if exclusive != nil && (!exactBytes || *exclusive <= bytes) {
						if *exclusive > math.MaxInt64-batch.exclusive {
							return false
						}
						batch.exclusive += *exclusive
						batch.hasExclusive = true
					}
				}
			}
		}
		if offset == 0 {
			return true
		}
		buf = buf[offset:]
	}
	return false
}

func (probe *refsProbe) readRelative(ctx context.Context, dirHandle windows.Handle, nameUTF16 []byte, ioctlBuf *[4096]byte) *int64 {
	if len(nameUTF16) == 0 || len(nameUTF16) > math.MaxUint16 || len(nameUTF16)%2 != 0 {
		return nil
	}
	// Open relative to the already-open parent directory handle to avoid
	// decoding UTF-16, joining paths, and re-parsing the full path in the kernel.
	name := windows.NTUnicodeString{
		Length:        uint16(len(nameUTF16)),
		MaximumLength: uint16(len(nameUTF16)),
		Buffer:        (*uint16)(unsafe.Pointer(&nameUTF16[0])),
	}
	oa := windows.OBJECT_ATTRIBUTES{
		Length:        uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory: dirHandle,
		ObjectName:    &name,
	}
	var handle windows.Handle
	var iosb windows.IO_STATUS_BLOCK
	err := windows.NtCreateFile(
		&handle,
		windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE,
		&oa,
		&iosb,
		nil,
		0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_OPEN,
		windows.FILE_OPEN_REPARSE_POINT|windows.FILE_NON_DIRECTORY_FILE|windows.FILE_SYNCHRONOUS_IO_NONALERT,
		0,
		0,
	)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(handle)
	if probe.hardLinks {
		var info [3]uint64
		if windows.GetFileInformationByHandleEx(handle, windows.FileStandardInfo,
			(*byte)(unsafe.Pointer(&info[0])), uint32(unsafe.Sizeof(info))) != nil {
			return nil
		}
		if uint32(info[2]) != 1 || int64(info[0]) < probe.clusterBytes {
			return new(int64(0))
		}
	}
	var total, start int64
	for ctx.Err() == nil {
		var used uint32
		err := windows.DeviceIoControl(handle, windows.FSCTL_GET_RETRIEVAL_POINTERS_AND_REFCOUNT,
			(*byte)(unsafe.Pointer(&start)), 8, &ioctlBuf[0], uint32(len(ioctlBuf)), &used, nil)
		if err == windows.ERROR_HANDLE_EOF {
			return new(total)
		}
		if err != nil && err != windows.ERROR_MORE_DATA || used > uint32(len(ioctlBuf)) {
			return nil
		}
		next, bytes, valid := refsExtents(ioctlBuf[:used], start, probe.clusterBytes)
		if !valid || bytes > math.MaxInt64-total {
			return nil
		}
		total += bytes
		if err == nil {
			return new(total)
		}
		if next <= start {
			return nil
		}
		start = next
	}
	return nil
}

func refsExtents(data []byte, start, clusterBytes int64) (next, bytes int64, valid bool) {
	// RETRIEVAL_POINTERS_AND_REFCOUNT_BUFFER has a 16-byte header and
	// 24-byte, 8-byte-aligned extent records.
	const header, extentSize = 16, 24
	if len(data) < header || clusterBytes <= 0 {
		return 0, 0, false
	}
	count := int(binary.LittleEndian.Uint32(data[:4]))
	next = int64(binary.LittleEndian.Uint64(data[8:16]))
	if count < 0 || count > (len(data)-header)/extentSize || next < 0 || next > start {
		return 0, 0, false
	}
	for i := range count {
		row := data[header+i*extentSize:]
		end := int64(binary.LittleEndian.Uint64(row[:8]))
		lcn := int64(binary.LittleEndian.Uint64(row[8:16]))
		refs := binary.LittleEndian.Uint32(row[16:20])
		if end <= next || end <= start || lcn < -1 {
			return 0, 0, false
		}
		if lcn >= 0 && refs == 1 {
			clusters := end - max(next, start)
			if clusters > (math.MaxInt64-bytes)/clusterBytes {
				return 0, 0, false
			}
			bytes += clusters * clusterBytes
		}
		next = end
	}
	return next, bytes, true
}
