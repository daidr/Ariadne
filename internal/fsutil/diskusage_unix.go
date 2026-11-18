//go:build linux || darwin

package fsutil

import (
	"io/fs"
	"syscall"
)

func diskEntry(info fs.FileInfo) (int64, fileIdentity) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return info.Size(), fileIdentity{}
	}
	id := fileIdentity{}
	if stat.Nlink > 1 && !info.IsDir() {
		id = fileIdentity{device: uint64(stat.Dev), file: stat.Ino}
	}
	return stat.Blocks * 512, id
}
