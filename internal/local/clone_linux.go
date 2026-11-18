package local

import (
	"context"
	"os"

	"golang.org/x/sys/unix"
)

func cloneFile(ctx context.Context, from, to string, mode os.FileMode) error {
	source, err := os.OpenFile(from, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	defer target.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := unix.IoctlFileClone(int(target.Fd()), int(source.Fd())); err != nil {
		return err
	}
	return ctx.Err()
}
