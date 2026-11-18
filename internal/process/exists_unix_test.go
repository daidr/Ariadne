//go:build linux || darwin

package process

import (
	"errors"
	"syscall"
	"time"
)

func processHasExited(pid int) bool {
	for i := 0; i < 100; i++ {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}
