package local

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
)

type Cloner func(context.Context, string, string) bool

// PlatformCloner attempts only filesystem clones. A failure leaves any partial
// destination for the owner to remove; it never falls back to a full copy.
func PlatformCloner(ctx context.Context, from, to string) bool {
	if ctx.Err() != nil {
		return false
	}
	info, err := os.Lstat(from)
	if err != nil {
		return false
	}
	if _, err := os.Lstat(to); !errors.Is(err, os.ErrNotExist) {
		return false
	}
	if info.Mode()&os.ModeSymlink != 0 {
		link, err := os.Readlink(from)
		return err == nil && os.Symlink(link, to) == nil
	}
	if info.Mode().IsRegular() {
		return cloneFile(ctx, from, to, info.Mode().Perm()) == nil
	}
	if !info.IsDir() {
		return false
	}
	if os.Mkdir(to, 0700) != nil {
		return false
	}
	type pair struct {
		from, to string
		mode     os.FileMode
	}
	dirs := []pair{{from, to, info.Mode().Perm()}}
	files := 0
	for next := 0; next < len(dirs); next++ {
		if ctx.Err() != nil {
			return false
		}
		current := dirs[next]
		directory, err := os.Open(current.from)
		if err != nil {
			return false
		}
		for {
			entries, err := directory.ReadDir(256)
			if err != nil && len(entries) == 0 {
				directory.Close()
				if !errors.Is(err, io.EOF) {
					return false
				}
				break
			}
			for _, entry := range entries {
				if ctx.Err() != nil {
					directory.Close()
					return false
				}
				files++
				if files > 1_000_000 {
					directory.Close()
					return false
				}
				source := filepath.Join(current.from, entry.Name())
				target := filepath.Join(current.to, entry.Name())
				stat, err := os.Lstat(source)
				if err != nil {
					directory.Close()
					return false
				}
				if stat.IsDir() {
					if os.Mkdir(target, 0700) != nil {
						directory.Close()
						return false
					}
					dirs = append(dirs, pair{source, target, stat.Mode().Perm()})
				} else if !PlatformCloner(ctx, source, target) {
					directory.Close()
					return false
				}
			}
		}
	}
	for index := len(dirs) - 1; index >= 0; index-- {
		if os.Chmod(dirs[index].to, dirs[index].mode) != nil {
			return false
		}
	}
	return true
}
