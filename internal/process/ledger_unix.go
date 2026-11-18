//go:build linux || darwin

package process

import (
	"context"
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func tryLedgerLock(file *os.File) (bool, error) {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return false, nil
	}
	return err == nil, err
}
func releaseLedgerLock(file *os.File)  { unix.Flock(int(file.Fd()), unix.LOCK_UN) }
func recordedGroupExists(pid int) bool { return pid > 1 && unix.Kill(-pid, 0) == nil }
func recordedPIDExists(pid int) bool {
	err := unix.Kill(pid, 0)
	return err == nil || errors.Is(err, unix.EPERM)
}
func killRecorded(ctx context.Context, entry TrackedProcess) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	pid := entry.PID
	if pid <= 1 || pid == os.Getpid() {
		return errors.New("invalid leftover process")
	}
	if entry.Group {
		pid = -pid
	}
	err := unix.Kill(pid, unix.SIGKILL)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	return err
}
