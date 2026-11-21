package fsutil

import (
	"context"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var getDiskFreeSpace = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetDiskFreeSpaceW")

type refsProbe struct {
	clusterBytes int64
	hardLinks    bool
	buffer       [4096]byte
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
	counter := &sizeCounter{}
	scan := directoryScan{native: nativeDirectory{refs: probe}}
	scan.visitDirectory(ctx, path, info, []*sizeCounter{counter}, true)
	if counter.exclusive > 0 || counter.hasExclusive && !counter.failed {
		// Confirmed extents still form a lower bound if the deadline ended the scan.
		return new(counter.exclusive)
	}
	if counter.found && !counter.failed && !scan.logical && counter.bytes == 0 {
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

func (probe *refsProbe) read(ctx context.Context, directory, leaf string) *int64 {
	name, err := windowsPath(filepath.Join(directory, leaf))
	if err != nil {
		return nil
	}
	handle, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
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
		if uint32(info[2]) != 1 {
			return new(int64(0))
		}
	}
	var total, start int64
	for ctx.Err() == nil {
		var used uint32
		err := windows.DeviceIoControl(handle, windows.FSCTL_GET_RETRIEVAL_POINTERS_AND_REFCOUNT,
			(*byte)(unsafe.Pointer(&start)), 8, &probe.buffer[0], uint32(len(probe.buffer)), &used, nil)
		if err == windows.ERROR_HANDLE_EOF {
			return new(total)
		}
		if err != nil && err != windows.ERROR_MORE_DATA || used > uint32(len(probe.buffer)) {
			return nil
		}
		next, bytes, valid := refsExtents(probe.buffer[:used], start, probe.clusterBytes)
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
