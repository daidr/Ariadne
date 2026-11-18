package fsutil

import (
	"context"
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"time"
)

var diskScans = make(chan struct{}, 4)

// DiskUsage walks directory metadata without reading file contents or following
// symlinks. A scan that is interrupted or can't inspect a file is unknown.
func DiskUsage(ctx context.Context, directory string) *int64 {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	select {
	case diskScans <- struct{}{}:
		defer func() { <-diskScans }()
	case <-ctx.Done():
		return nil
	}
	var total int64
	seen := map[fileIdentity]bool{}
	err := filepath.WalkDir(directory, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := entry.Info()
		if errors.Is(err, os.ErrNotExist) {
			// Worktree files may disappear while reading the directory.
			return nil
		}
		if err != nil {
			return err
		}
		bytes, identity := diskEntry(info)
		if identity != (fileIdentity{}) {
			if seen[identity] {
				return nil
			}
			seen[identity] = true
		}
		if bytes < 0 || bytes > math.MaxInt64-total {
			return errors.New("directory size exceeds the supported range")
		}
		total += bytes
		return nil
	})
	if err != nil {
		return nil
	}
	return &total
}

type fileIdentity struct{ device, file, high uint64 }
