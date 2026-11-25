//go:build windows

package browser

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestTerminateBrowserTreeKillsDescendants(t *testing.T) {
	role := os.Getenv("ARIADNE_BROWSER_TREE_HELPER")
	switch role {
	case "child":
		lockPath := os.Getenv("ARIADNE_BROWSER_TREE_LOCK")
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			os.Exit(2)
		}
		defer f.Close()
		fmt.Println("ready")
		time.Sleep(time.Minute)
		return
	case "parent":
		exe, err := os.Executable()
		if err != nil {
			os.Exit(3)
		}
		child := exec.Command(exe, "-test.run=^TestTerminateBrowserTreeKillsDescendants$")
		child.Env = append(os.Environ(), "ARIADNE_BROWSER_TREE_HELPER=child")
		childOut, err := child.StdoutPipe()
		if err != nil {
			os.Exit(4)
		}
		if err := child.Start(); err != nil {
			os.Exit(5)
		}
		line, err := bufio.NewReader(childOut).ReadString('\n')
		if err != nil || strings.TrimSpace(line) != "ready" {
			_ = child.Process.Kill()
			os.Exit(6)
		}
		fmt.Println(child.Process.Pid)
		time.Sleep(time.Minute)
		return
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "profile.lock")
	parent := exec.Command(exe, "-test.run=^TestTerminateBrowserTreeKillsDescendants$")
	parent.Env = append(os.Environ(),
		"ARIADNE_BROWSER_TREE_HELPER=parent",
		"ARIADNE_BROWSER_TREE_LOCK="+lockPath,
	)
	stdout, err := parent.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = terminateBrowserTree(parent.Process)
		_ = parent.Wait()
	}()

	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("read child pid: %v", err)
	}
	childPID64, err := strconv.ParseUint(strings.TrimSpace(line), 10, 32)
	if err != nil {
		t.Fatalf("parse child pid %q: %v", line, err)
	}
	childPID := uint32(childPID64)

	parentHandle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(parent.Process.Pid))
	if err != nil {
		t.Fatalf("open parent: %v", err)
	}
	parentCreated, ok := processCreationTime(parentHandle)
	_ = windows.CloseHandle(parentHandle)
	if !ok {
		t.Fatal("failed to query parent creation time")
	}

	// Verify creation-time guard rejects candidates created earlier than the claimed parent.
	futureDescendants := collectDescendants([]processAnchor{{pid: uint32(parent.Process.Pid), created: math.MaxUint64}}, nil)
	closeDescendants(futureDescendants)
	if len(futureDescendants) != 0 {
		t.Fatalf("expected 0 descendants when parent creation time is in the future, got %d", len(futureDescendants))
	}

	// Verify current process is never collected or traversed.
	selfDescendants := collectDescendants([]processAnchor{{pid: uint32(os.Getpid()), created: 0}}, nil)
	closeDescendants(selfDescendants)
	if len(selfDescendants) != 0 {
		t.Fatalf("expected 0 descendants when anchor is self PID, got %d", len(selfDescendants))
	}

	descendants := collectDescendants([]processAnchor{{pid: uint32(parent.Process.Pid), created: parentCreated}}, nil)
	foundChild := false
	for _, d := range descendants {
		if d.pid == childPID {
			foundChild = true
		}
	}
	closeDescendants(descendants)
	if !foundChild {
		t.Fatalf("collectDescendants(%d) missed child %d", parent.Process.Pid, childPID)
	}

	childHandle, openErr := windows.OpenProcess(windows.SYNCHRONIZE, false, childPID)
	if openErr == nil {
		defer windows.CloseHandle(childHandle)
	}

	if err := terminateBrowserTree(parent.Process); err != nil {
		t.Fatalf("terminateBrowserTree: %v", err)
	}
	_ = parent.Wait()

	// terminateBrowserTree already waited for verified descendants to exit, so
	// childHandle must be signaled immediately and lockPath removable without extra waiting.
	if openErr == nil {
		event, err := windows.WaitForSingleObject(childHandle, 0)
		if err != nil || event != windows.WAIT_OBJECT_0 {
			t.Fatalf("child process %d still running after terminateBrowserTree returned (event=%d, err=%v)", childPID, event, err)
		}
	}
	if err := os.Remove(lockPath); err != nil {
		t.Fatalf("lock file still held immediately after terminateBrowserTree: %v", err)
	}
}
