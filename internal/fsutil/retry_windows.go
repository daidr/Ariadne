//go:build windows

package fsutil

import (
	"errors"
	"golang.org/x/sys/windows"
)

func retryRemoval(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_DIR_NOT_EMPTY) || errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}
