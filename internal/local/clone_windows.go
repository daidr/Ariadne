package local

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type cloneVolume struct {
	supported bool
	expires   time.Time
}

var cloneVolumes = struct {
	sync.Mutex
	values map[byte]cloneVolume
}{values: map[byte]cloneVolume{}}
var copyFileProc = windows.NewLazySystemDLL("kernel32.dll").NewProc("CopyFileW")

func cloneDrive(path string) byte {
	absolute, err := filepath.Abs(path)
	if err != nil || len(absolute) < 3 || absolute[1] != ':' {
		return 0
	}
	drive := strings.ToUpper(absolute[:1])[0]
	if drive < 'A' || drive > 'Z' {
		return 0
	}
	return drive
}
func supportsClone(drive byte) bool {
	if drive == 0 || windows.RtlGetVersion().BuildNumber < 26100 {
		return false
	}
	cloneVolumes.Lock()
	defer cloneVolumes.Unlock()
	if known, ok := cloneVolumes.values[drive]; ok && time.Now().Before(known.expires) {
		return known.supported
	}
	root, _ := windows.UTF16PtrFromString(string(drive) + ":\\")
	var name [32]uint16
	err := windows.GetVolumeInformation(root, nil, 0, nil, nil, nil, &name[0], uint32(len(name)))
	supported := err == nil && strings.EqualFold(windows.UTF16ToString(name[:]), "ReFS")
	cloneVolumes.values[drive] = cloneVolume{supported: supported, expires: time.Now().Add(time.Minute)}
	return supported
}
func cloneFile(ctx context.Context, from, to string, _ os.FileMode) error {
	drive := cloneDrive(from)
	if drive != cloneDrive(to) || !supportsClone(drive) {
		return errors.New("filesystem block cloning is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	source, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	target, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	// ReFS copies share storage on Windows 11 24H2 / Server 2025 and later.
	// https://learn.microsoft.com/windows-server/storage/refs/block-cloning
	result, _, err := copyFileProc.Call(uintptr(unsafe.Pointer(source)), uintptr(unsafe.Pointer(target)), 1)
	if result == 0 {
		return err
	}
	return ctx.Err()
}
