//go:build linux

package process

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

var clockTicks = sync.OnceValue(func() uint64 {
	data, err := os.ReadFile("/proc/self/auxv")
	if err == nil {
		for len(data) >= 16 {
			tag, value := binary.NativeEndian.Uint64(data[:8]), binary.NativeEndian.Uint64(data[8:16])
			if tag == 17 && value > 0 {
				return value
			}
			data = data[16:]
		}
	}
	return 100
})
var bootToken = sync.OnceValue(func() string {
	data, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(data))
})

func identityOf(pid int) (processIdentity, error) {
	if pid <= 0 {
		return processIdentity{}, errors.New("invalid process")
	}
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return processIdentity{}, err
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return processIdentity{}, errors.New("invalid process stat")
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) <= 19 {
		return processIdentity{}, os.ErrProcessDone
	}
	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return processIdentity{}, err
	}
	var elapsed unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_BOOTTIME, &elapsed); err != nil {
		return processIdentity{}, err
	}
	start := time.Now().UnixMilli() - (elapsed.Sec*1000 + elapsed.Nsec/1_000_000) + int64(ticks*1000/clockTicks())
	return processIdentity{startedAt: start, token: fmt.Sprintf("linux:%s:%d", bootToken(), ticks), exited: fields[0] == "Z"}, nil
}
