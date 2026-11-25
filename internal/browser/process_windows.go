//go:build windows

package browser

import (
	"os"
	"os/exec"
	"time"
	"unsafe"

	"github.com/chromedp/chromedp"
	"golang.org/x/sys/windows"
)

func platformExecOptions() []chromedp.ExecAllocatorOption {
	return []chromedp.ExecAllocatorOption{
		chromedp.ModifyCmdFunc(func(cmd *exec.Cmd) {
			cmd.WaitDelay = 500 * time.Millisecond
			cmd.Cancel = func() error {
				return terminateBrowserTree(cmd.Process)
			}
		}),
	}
}

// terminateBrowserTree kills the browser process and any descendant helper
// processes (such as crashpad, GPU, or utility processes) on Windows so
// inherited stdio handles and user-data-dir locks release immediately.
func terminateBrowserTree(proc *os.Process) error {
	if proc == nil || proc.Pid <= 0 {
		return nil
	}
	root := uint32(proc.Pid)
	descendants := collectDescendantPIDs(root)
	err := proc.Kill()
	for _, pid := range descendants {
		terminatePID(pid)
	}
	// Catch any child spawned right before the root process exited.
	if extras := collectDescendantPIDs(root); len(extras) > 0 {
		for _, pid := range extras {
			terminatePID(pid)
		}
	}
	return err
}

func collectDescendantPIDs(root uint32) []uint32 {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snapshot)

	children := map[uint32][]uint32{}
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		if entry.ProcessID == 0 || entry.ProcessID == entry.ParentProcessID {
			continue
		}
		children[entry.ParentProcessID] = append(children[entry.ParentProcessID], entry.ProcessID)
	}

	var result []uint32
	seen := map[uint32]bool{root: true}
	queue := []uint32{root}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, child := range children[current] {
			if seen[child] {
				continue
			}
			seen[child] = true
			result = append(result, child)
			queue = append(queue, child)
		}
	}
	return result
}

func terminatePID(pid uint32) {
	handle, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		return
	}
	defer windows.CloseHandle(handle)
	_ = windows.TerminateProcess(handle, 1)
}
