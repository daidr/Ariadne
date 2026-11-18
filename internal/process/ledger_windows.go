//go:build windows

package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

func identityHandle(handle windows.Handle) (processIdentity, error) {
	status, err := windows.WaitForSingleObject(handle, 0)
	if err != nil {
		return processIdentity{}, err
	}
	if status != uint32(windows.WAIT_TIMEOUT) {
		return processIdentity{}, os.ErrProcessDone
	}
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		return processIdentity{}, err
	}
	stamp := uint64(creation.HighDateTime)<<32 | uint64(creation.LowDateTime)
	return processIdentity{startedAt: creation.Nanoseconds() / int64(time.Millisecond), token: fmt.Sprintf("windows:%d", stamp)}, nil
}
func identityOf(pid int) (processIdentity, error) {
	if pid <= 0 {
		return processIdentity{}, errors.New("invalid process")
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return processIdentity{}, err
	}
	defer windows.CloseHandle(handle)
	return identityHandle(handle)
}
func tryLedgerLock(file *os.File) (bool, error) {
	err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{})
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return false, nil
	}
	return err == nil, err
}
func releaseLedgerLock(file *os.File) {
	windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &windows.Overlapped{})
}
func recordedGroupExists(int) bool { return false }
func recordedPIDExists(pid int) bool {
	_, err := identityOf(pid)
	return err == nil || errors.Is(err, windows.ERROR_ACCESS_DENIED)
}
func killRecorded(ctx context.Context, entry TrackedProcess) error {
	if entry.PID <= 0 || entry.PID == os.Getpid() {
		return errors.New("invalid leftover process")
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(entry.PID))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	actual, err := identityHandle(handle)
	if err != nil {
		return err
	}
	if entry.StartToken != "" && entry.StartToken != actual.token || entry.StartToken == "" && (actual.startedAt-entry.StartedAt > 3000 || actual.startedAt-entry.StartedAt < -3000) {
		return errors.New("leftover process identity changed")
	}
	// Hold the process handle until taskkill returns, so the PID cannot be
	// recycled while Windows discovers descendants of a legacy Bun process.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "taskkill", "/T", "/F", "/PID", strconv.Itoa(entry.PID))
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	err = command.Run()
	if err != nil {
		if status, _ := windows.WaitForSingleObject(handle, 0); status == uint32(windows.WAIT_TIMEOUT) {
			return windows.TerminateProcess(handle, 1)
		}
	}
	return nil
}
