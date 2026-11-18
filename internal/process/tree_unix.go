//go:build linux || darwin

package process

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

type processTree struct{}

func prepareTree(command *exec.Cmd) (*processTree, error) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return &processTree{}, nil
}
func (*processTree) attach(*exec.Cmd) error { return nil }
func (*processTree) kill(command *exec.Cmd) error {
	if command.Process == nil || command.Process.Pid <= 0 || command.Process.Pid == os.Getpid() {
		return os.ErrProcessDone
	}
	err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}
func (tree *processTree) close(command *exec.Cmd) {
	if command.ProcessState != nil {
		if _, err := syscall.Getpgid(command.Process.Pid); err == nil {
			return
		}
	}
	_ = tree.kill(command)
}
