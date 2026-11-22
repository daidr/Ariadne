//go:build windows

package service

import (
	"errors"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

const stillActive = 259

// ProcessAlive reports whether pid refers to a currently live Windows process.
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	var code uint32
	if err := windows.GetExitCodeProcess(handle, &code); err != nil {
		return false
	}
	return code == stillActive
}

// TerminateProcess forcefully terminates a Windows process by PID.
// Callers should prefer the HTTP shutdown endpoint for graceful stops.
func TerminateProcess(pid int) error {
	if pid <= 0 {
		return errors.New("invalid pid")
	}
	handle, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(handle)
	return windows.TerminateProcess(handle, 1)
}

// SpawnDetached starts executable without a console window, detached from the caller.
func SpawnDetached(executable string, args []string, env []string) (int, error) {
	cmd := exec.Command(executable, args...)
	if len(env) > 0 {
		cmd.Env = env
	} else {
		cmd.Env = os.Environ()
	}
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err == nil {
		defer devNull.Close()
		cmd.Stdin = devNull
		cmd.Stdout = devNull
		cmd.Stderr = devNull
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW | windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
	}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	return pid, nil
}
