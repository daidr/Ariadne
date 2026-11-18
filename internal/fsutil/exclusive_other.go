//go:build !linux

package fsutil

import (
	"context"
	"io/fs"
)

type exclusiveBlocks struct{}

func (*exclusiveBlocks) read(context.Context, string, fs.FileInfo) *int64 { return nil }
