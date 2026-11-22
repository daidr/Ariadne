//go:build !windows

package service

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// ProcessAlive reports whether pid refers to a currently live process.
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// TerminateProcess sends SIGTERM to pid.
func TerminateProcess(pid int) error {
	if pid <= 0 {
		return errors.New("invalid pid")
	}
	err := syscall.Kill(pid, syscall.SIGTERM)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

// SpawnDetached starts executable in a new session detached from the current terminal.
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
		Setsid: true,
	}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	return pid, nil
}
