package update

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

func lockExecutable(file *os.File) error {
	return windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{})
}

func replaceExecutable(staged, executable string) error {
	// Windows allows renaming a running executable, but replacing it in place
	// returns access denied. Keep that original until the next clean start.
	previous := executable + ".ariadne-previous"
	if err := os.Remove(previous); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(executable, previous); err != nil {
		return err
	}
	if err := os.Rename(staged, executable); err != nil {
		if rollback := os.Rename(previous, executable); rollback != nil {
			return errors.Join(err, fmt.Errorf("restore the original program from %s: %w", previous, rollback))
		}
		return err
	}
	return nil
}

func prepareCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}

func cleanPrevious(executable string) {
	// If the old server is still running, the file stays until a later launch.
	_ = os.Remove(executable + ".ariadne-previous")
}
