//go:build !windows && !linux

package fsutil

import (
	"context"
	"io/fs"
)

type nativeDirectory struct{}

func (*directoryScan) readNative(context.Context, string, fs.FileInfo, []*sizeCounter, bool) bool {
	return false
}
