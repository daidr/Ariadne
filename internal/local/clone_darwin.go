package local

import (
	"context"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func cloneFile(ctx context.Context, from, to string, _ os.FileMode) error {
	source, err := os.OpenFile(from, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer source.Close()
	parent, err := os.Open(filepath.Dir(to))
	if err != nil {
		return err
	}
	defer parent.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := unix.Fclonefileat(int(source.Fd()), int(parent.Fd()), filepath.Base(to), 0); err != nil {
		return err
	}
	return ctx.Err()
}
