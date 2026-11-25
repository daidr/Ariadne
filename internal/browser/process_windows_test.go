//go:build windows

package browser

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

	descendants := collectDescendantPIDs(uint32(parent.Process.Pid))
	if !slices.Contains(descendants, childPID) {
		t.Fatalf("collectDescendantPIDs(%d) = %v, missing child %d", parent.Process.Pid, descendants, childPID)
	}

	childHandle, openErr := windows.OpenProcess(windows.SYNCHRONIZE, false, childPID)
	if openErr == nil {
		defer windows.CloseHandle(childHandle)
	}

	if err := terminateBrowserTree(parent.Process); err != nil {
		t.Fatalf("terminateBrowserTree: %v", err)
	}
	_ = parent.Wait()

	if openErr == nil {
		event, err := windows.WaitForSingleObject(childHandle, 5000)
		if err != nil || event != windows.WAIT_OBJECT_0 {
			t.Fatalf("child process %d did not exit after terminateBrowserTree (event=%d, err=%v)", childPID, event, err)
		}
	}
	if err := os.Remove(lockPath); err != nil {
		t.Fatalf("lock file still held after terminateBrowserTree: %v", err)
	}
}
