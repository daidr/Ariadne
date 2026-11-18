package local

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

func defaultLocale() string {
	// ICU uses the Windows user locale instead of POSIX environment variables.
	// https://learn.microsoft.com/windows/win32/api/winnls/nf-winnls-getuserdefaultlocalename
	var name [85]uint16
	getLocale := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetUserDefaultLocaleName")
	length, _, _ := getLocale.Call(uintptr(unsafe.Pointer(&name[0])), uintptr(len(name)))
	if length == 0 {
		return "en-US"
	}
	return windows.UTF16ToString(name[:])
}
