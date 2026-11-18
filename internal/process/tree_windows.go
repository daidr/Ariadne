//go:build windows

package process

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Every child enters its job before any of its code runs. Closing the parent's
// non-inherited handle also ends the job if the backend exits unexpectedly.
type processTree struct {
	mu  sync.Mutex
	job windows.Handle
}
type jobAccounting struct {
	TotalUserTime, TotalKernelTime, ThisPeriodTotalUserTime, ThisPeriodTotalKernelTime int64
	TotalPageFaultCount, TotalProcesses, ActiveProcesses, TotalTerminatedProcesses     uint32
}

func prepareTree(command *exec.Cmd) (*processTree, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	command.SysProcAttr.HideWindow = true
	command.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED | windows.CREATE_NO_WINDOW
	return &processTree{job: job}, nil
}
func (tree *processTree) attach(command *exec.Cmd) error {
	if command.Process == nil || command.Process.Pid == os.Getpid() {
		return errors.New("invalid command process")
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(command.Process.Pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(process)
	tree.mu.Lock()
	job := tree.job
	if job == 0 {
		tree.mu.Unlock()
		return os.ErrProcessDone
	}
	err = windows.AssignProcessToJobObject(job, process)
	tree.mu.Unlock()
	if err != nil {
		return err
	}
	return resumeProcess(uint32(command.Process.Pid))
}
func resumeProcess(pid uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	if err := windows.Thread32First(snapshot, &entry); err != nil {
		return err
	}
	resumed := false
	for {
		if entry.OwnerProcessID == pid {
			thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
			if err == nil {
				previous, resumeErr := windows.ResumeThread(thread)
				windows.CloseHandle(thread)
				if resumeErr == nil && previous > 0 {
					resumed = true
				}
			}
		}
		if err := windows.Thread32Next(snapshot, &entry); err != nil {
			if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
				return err
			}
			break
		}
	}
	if !resumed {
		return errors.New("command's suspended thread could not be resumed")
	}
	return nil
}
func (tree *processTree) kill(command *exec.Cmd) error {
	tree.mu.Lock()
	defer tree.mu.Unlock()
	var err error
	if tree.job != 0 {
		err = windows.TerminateJobObject(tree.job, 1)
	}
	// A cancellation may arrive between CreateProcess and job assignment.
	if command.Process != nil {
		_ = command.Process.Kill()
	}
	return err
}
func (tree *processTree) close(command *exec.Cmd) {
	_ = tree.kill(command)
	tree.mu.Lock()
	defer tree.mu.Unlock()
	if tree.job == 0 {
		return
	}
	// Termination is asynchronous. Wait briefly for file handles to be released
	// before callers remove a worktree, with a fixed upper bound on cleanup.
	until := time.Now().Add(2 * time.Second)
	for time.Now().Before(until) {
		var accounting jobAccounting
		err := windows.QueryInformationJobObject(tree.job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&accounting)), uint32(unsafe.Sizeof(accounting)), nil)
		if err != nil || accounting.ActiveProcesses == 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	windows.CloseHandle(tree.job)
	tree.job = 0
}
