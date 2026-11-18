//go:build !windows

package process

import "os/exec"

func prepareInvocation(*exec.Cmd) error { return nil }
