package process

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLedgerBackendChild(t *testing.T) {
	if len(os.Args) < 4 || os.Args[len(os.Args)-3] != "--ledger-backend" {
		return
	}
	path, pidPath := os.Args[len(os.Args)-2], os.Args[len(os.Args)-1]
	if _, err := DefaultLedger.Open(context.Background(), path); err != nil {
		os.Exit(51)
	}
	program, err := os.Executable()
	if err != nil {
		os.Exit(52)
	}
	child, err := Start(context.Background(), []string{program, "-test.run=^TestLedgerChild$", "--", "--ledger-child"}, Options{Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		os.Exit(53)
	}
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(child.PID())), 0600); err != nil {
		os.Exit(54)
	}
	// Deliberately skip service and ledger cleanup, as a killed backend would.
	os.Exit(0)
}
func TestActualBackendExitLeavesNoUnownedProcessesAfterRecovery(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path, pidPath := filepath.Join(root, "processes.json"), filepath.Join(root, "child.pid")
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	memory := NewLedger()
	defer memory.Close()
	backend, err := Start(t.Context(), []string{program, "-test.run=^TestLedgerBackendChild$", "--", "--ledger-backend", path, pidPath}, Options{Ledger: memory, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { backend.Kill(); backend.Wait() }()
	if err := backend.Wait(); err != nil {
		t.Fatal("backend fixture failed", err)
	}
	file, err := readLedger(path)
	if err != nil || len(file.Processes) != 1 {
		t.Fatal("backend did not journal child", err)
	}
	entry := file.Processes[0]
	defer func() {
		if sameProcess(entry.PID, entry.StartedAt, entry.StartToken) {
			killRecorded(context.Background(), entry)
		}
	}()
	data, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	if pid != entry.PID {
		t.Fatal("fixture PID mismatch")
	}
	next := NewLedger()
	defer next.Close()
	left, err := next.Open(t.Context(), path)
	if err != nil || left.OwnerAlive {
		t.Fatal("recovery failed", left, err)
	}
	if runtime.GOOS != "windows" && len(left.Killed) != 1 {
		t.Fatal("Unix leftover not recovered", left)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		identity, err := identityOf(entry.PID)
		if err != nil || identity.exited {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("orphan survived recovery")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
