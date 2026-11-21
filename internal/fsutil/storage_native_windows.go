package fsutil

import (
	"context"
	"encoding/binary"
	"io/fs"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

type nativeDirectory struct {
	buffer [8 << 10]uint64 // Windows directory records require 8-byte alignment.
	refs   *refsProbe
}

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

// Batch directory metadata, including allocation size and the 128-bit file ID,
// without opening each regular file or needing raw access to the NTFS volume.
func (scan *directoryScan) readNative(ctx context.Context, path string, _ fs.FileInfo, counts []*sizeCounter, inWorktree bool) bool {
	name, err := windowsPath(path)
	if err != nil {
		return false
	}
	handle, err := windows.CreateFile(name, windows.FILE_LIST_DIRECTORY|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return false
	}
	defer func() {
		if handle != 0 {
			_ = windows.CloseHandle(handle)
		}
	}()
	var volumeInfo [3]uint64
	if windows.GetFileInformationByHandleEx(handle, windows.FileIdInfo, (*byte)(unsafe.Pointer(&volumeInfo[0])), uint32(unsafe.Sizeof(volumeInfo))) != nil {
		return false
	}
	device := volumeInfo[0]
	var directories []string
	read := false
	for ctx.Err() == nil {
		buf := unsafe.Slice((*byte)(unsafe.Pointer(&scan.native.buffer[0])), int(unsafe.Sizeof(scan.native.buffer)))
		err := windows.GetFileInformationByHandleEx(handle, windows.FileIdExtdDirectoryInfo, &buf[0], uint32(len(buf)))
		if err == windows.ERROR_NO_MORE_FILES || err == windows.ERROR_FILE_NOT_FOUND {
			_ = windows.CloseHandle(handle)
			handle = 0
			for _, name := range directories {
				scan.visitDirectory(ctx, filepath.Join(path, name), nil, counts, inWorktree)
			}
			return true
		}
		if err != nil && !read {
			return false
		}
		if err != nil || !scan.nativeEntries(ctx, buf, device, path, counts, &directories) {
			markFailed(counts)
			return true
		}
		read = true
	}
	markFailed(counts)
	return true
}

func (scan *directoryScan) nativeEntries(ctx context.Context, buf []byte, device uint64, path string, counts []*sizeCounter, directories *[]string) bool {
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
			chars := unsafe.Slice((*uint16)(unsafe.Pointer(&buf[header])), nameBytes/2)
			if attrs&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
				name := string(utf16.Decode(chars))
				if name != "." && name != ".." {
					*directories = append(*directories, name)
				}
			} else if len(counts) > 0 {
				logical := int64(binary.LittleEndian.Uint64(buf[40:48]))
				bytes := int64(binary.LittleEndian.Uint64(buf[48:56]))
				if attrs&(windows.FILE_ATTRIBUTE_SPARSE_FILE|windows.FILE_ATTRIBUTE_COMPRESSED) != 0 {
					var ok bool
					bytes, ok = compressedSize(filepath.Join(path, string(utf16.Decode(chars))))
					if !ok {
						scan.logical = true
						bytes = logical
					}
				}
				id := fileIdentity{device: device, file: binary.LittleEndian.Uint64(buf[72:80]), high: binary.LittleEndian.Uint64(buf[80:88])}
				if id.file == 0 && id.high == 0 {
					id = fileIdentity{}
				}
				var exclusive *int64
				if probe := scan.native.refs; probe != nil && ctx.Err() == nil {
					// The probe excludes hard-linked data, so no file ID set is needed.
					id = fileIdentity{}
					if bytes == 0 {
						exclusive = new(int64(0))
					} else {
						exclusive = probe.read(ctx, path, string(utf16.Decode(chars)))
						if exclusive != nil && *exclusive > bytes {
							exclusive = nil // Allocation changed during the scan.
						}
					}
				}
				for _, counter := range counts {
					counter.add(bytes, logical, id, exclusive)
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
