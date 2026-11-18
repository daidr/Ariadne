package fsutil

import (
	"context"
	"io/fs"
	"math"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Linux UAPI linux/fiemap.h. Do not request FIEMAP_FLAG_SYNC: measuring
// storage should never force writeback on an active worktree.
const (
	fiemapRequest   = 0xc020660b
	extentLast      = 0x0001
	extentUnwritten = 0x0800
)

type fileExtent struct {
	logical, physical, length uint64
	reserved64                [2]uint64
	flags                     uint32
	reserved                  [3]uint32
}

type fiemap struct {
	start, length                  uint64
	flags, mapped, count, reserved uint32
	extents                        [64]fileExtent
}

type exclusiveBlocks struct {
	supported map[uint64]bool
	buffer    fiemap
}

func (probe *exclusiveBlocks) read(ctx context.Context, path string, info fs.FileInfo) *int64 {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		return nil
	}
	device := uint64(stat.Dev)
	if probe.supported == nil {
		probe.supported = make(map[uint64]bool)
	}
	supported, checked := probe.supported[device]
	if !checked {
		var volume unix.Statfs_t
		if unix.Statfs(path, &volume) != nil {
			return nil
		}
		supported = uint32(volume.Type) == unix.BTRFS_SUPER_MAGIC || uint32(volume.Type) == unix.XFS_SUPER_MAGIC
		probe.supported[device] = supported
	}
	if !supported {
		return nil
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil
	}
	defer unix.Close(fd)
	var current unix.Stat_t
	if unix.Fstat(fd, &current) != nil || current.Mode&unix.S_IFMT != unix.S_IFREG ||
		uint64(current.Dev) != device || current.Ino != stat.Ino || current.Nlink != 1 {
		return nil
	}
	total := uint64(0)
	for start := uint64(0); ctx.Err() == nil; {
		buffer := &probe.buffer
		buffer.start, buffer.length = start, math.MaxUint64-start
		buffer.flags, buffer.mapped, buffer.count, buffer.reserved = 0, 0, uint32(len(buffer.extents)), 0
		_, _, err := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), fiemapRequest, uintptr(unsafe.Pointer(buffer)))
		if err != 0 || buffer.mapped > buffer.count {
			return nil
		}
		extents := buffer.extents[:buffer.mapped]
		for _, extent := range extents {
			length := exclusiveExtentLength(extent)
			if length > math.MaxInt64-total {
				return nil
			}
			total += length
		}
		if len(extents) == 0 || extents[len(extents)-1].flags&extentLast != 0 {
			if current.Blocks < 0 || total > uint64(current.Blocks)*512 {
				return nil
			}
			return new(int64(total))
		}
		last := extents[len(extents)-1]
		next := last.logical + last.length
		if next <= start || next < last.logical {
			return nil
		}
		start = next
	}
	return nil
}

func exclusiveExtentLength(extent fileExtent) uint64 {
	// Shared, encoded, inline, delayed and unrecognized mappings cannot
	// contribute to the lower bound. A sparse hole has no physical mapping.
	if extent.physical == 0 || extent.flags&^(extentLast|extentUnwritten) != 0 {
		return 0
	}
	return extent.length
}
