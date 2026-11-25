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

const browserTreeWaitTimeout = 2 * time.Second

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

type processAnchor struct {
	pid     uint32
	created uint64
}

type descendantProcess struct {
	pid     uint32
	handle  windows.Handle
	created uint64
}

// terminateBrowserTree kills the browser process and any verified descendant
// helper processes (such as crashpad, GPU, or utility processes) on Windows,
// then briefly waits for their handles to signal so inherited stdio handles
// and user-data-dir locks release before cleanup runs.
func terminateBrowserTree(proc *os.Process) error {
	if proc == nil || proc.Pid <= 0 || proc.Pid == os.Getpid() {
		return nil
	}
	rootPID := uint32(proc.Pid)
	rootHandle, err := windows.OpenProcess(
		windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE,
		false,
		rootPID,
	)
	if err != nil {
		// Fail closed: without a verified root handle and creation time, do not
		// walk ParentProcessIDs that might belong to a recycled PID.
		return proc.Kill()
	}
	defer windows.CloseHandle(rootHandle)

	rootCreated, ok := processCreationTime(rootHandle)
	if !ok {
		return proc.Kill()
	}

	rootAnchor := processAnchor{pid: rootPID, created: rootCreated}
	descendants := collectDescendants([]processAnchor{rootAnchor}, nil)
	defer closeDescendants(descendants)

	killErr := proc.Kill()
	for _, d := range descendants {
		_ = windows.TerminateProcess(d.handle, 1)
	}

	// Catch any child spawned right before the root or pass-1 descendants exited.
	// Holding rootHandle and pass-1 handles open prevents PID reuse during this pass.
	anchors := make([]processAnchor, 0, 1+len(descendants))
	anchors = append(anchors, rootAnchor)
	held := make(map[uint32]bool, len(descendants))
	for _, d := range descendants {
		anchors = append(anchors, processAnchor{pid: d.pid, created: d.created})
		held[d.pid] = true
	}
	extras := collectDescendants(anchors, held)
	defer closeDescendants(extras)
	for _, d := range extras {
		_ = windows.TerminateProcess(d.handle, 1)
	}

	waitHandles := make([]windows.Handle, 0, 1+len(descendants)+len(extras))
	waitHandles = append(waitHandles, rootHandle)
	for _, d := range descendants {
		waitHandles = append(waitHandles, d.handle)
	}
	for _, d := range extras {
		waitHandles = append(waitHandles, d.handle)
	}
	waitForHandles(waitHandles, browserTreeWaitTimeout)
	return killErr
}

func collectDescendants(anchors []processAnchor, alreadyHeld map[uint32]bool) []descendantProcess {
	if len(anchors) == 0 {
		return nil
	}
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

	selfPID := uint32(os.Getpid())
	seen := make(map[uint32]bool, len(anchors)+1)
	seen[selfPID] = true
	queue := make([]processAnchor, 0, len(anchors))
	for _, anchor := range anchors {
		if anchor.pid == 0 || anchor.pid == selfPID {
			continue
		}
		seen[anchor.pid] = true
		queue = append(queue, anchor)
	}

	const access = windows.PROCESS_QUERY_LIMITED_INFORMATION | windows.PROCESS_TERMINATE | windows.SYNCHRONIZE
	var result []descendantProcess
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		for _, childPID := range children[parent.pid] {
			if seen[childPID] {
				continue
			}
			seen[childPID] = true
			if alreadyHeld[childPID] {
				continue
			}
			handle, err := windows.OpenProcess(access, false, childPID)
			if err != nil {
				continue
			}
			childCreated, ok := processCreationTime(handle)
			if !ok || childCreated < parent.created {
				windows.CloseHandle(handle)
				continue
			}
			result = append(result, descendantProcess{
				pid:     childPID,
				handle:  handle,
				created: childCreated,
			})
			queue = append(queue, processAnchor{pid: childPID, created: childCreated})
		}
	}
	return result
}

func processCreationTime(handle windows.Handle) (uint64, bool) {
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &created, &exited, &kernel, &user); err != nil {
		return 0, false
	}
	return uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime), true
}

func closeDescendants(descendants []descendantProcess) {
	for _, d := range descendants {
		if d.handle != 0 {
			_ = windows.CloseHandle(d.handle)
		}
	}
}

func waitForHandles(handles []windows.Handle, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for _, h := range handles {
		if h == 0 {
			continue
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return
		}
		ms := uint32(remaining.Milliseconds())
		if ms == 0 {
			ms = 1
		}
		_, _ = windows.WaitForSingleObject(h, ms)
	}
}
