//go:build linux || darwin

package fsutil

import (
	"errors"
	"syscall"
)

func retryRemoval(err error) bool {
	return errors.Is(err, syscall.EBUSY) || errors.Is(err, syscall.ENOTEMPTY)
}
