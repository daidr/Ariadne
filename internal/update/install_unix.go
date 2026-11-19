//go:build !windows

package update

import (
	"os"
	"os/exec"

	"golang.org/x/sys/unix"
)

func lockExecutable(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

func replaceExecutable(staged, executable string) error {
	return os.Rename(staged, executable)
}

func prepareCommand(*exec.Cmd) {}
func cleanPrevious(string)     {}
