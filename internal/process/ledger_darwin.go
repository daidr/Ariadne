//go:build darwin

package process

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func identityOf(pid int) (processIdentity, error) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return processIdentity{}, err
	}
	if info.Proc.P_pid != int32(pid) {
		return processIdentity{}, os.ErrProcessDone
	}
	at := info.Proc.P_starttime
	return processIdentity{startedAt: at.Sec*1000 + int64(at.Usec)/1000, token: fmt.Sprintf("darwin:%d:%d", at.Sec, at.Usec), exited: info.Proc.P_stat == 5}, nil
}
