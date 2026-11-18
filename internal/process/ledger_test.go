package process

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func ledgerChild(t *testing.T, ledger *Ledger) *Command {
	t.Helper()
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command, err := Start(t.Context(), []string{program, "-test.run=^TestLedgerChild$", "--", "--ledger-child"}, Options{Ledger: ledger, Label: "test-child", Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { command.Kill(); command.Wait() })
	return command
}
func TestLedgerChild(t *testing.T) {
	if os.Args[len(os.Args)-1] != "--ledger-child" {
		return
	}
	time.Sleep(time.Minute)
}
func TestLedgerWritesTracksAndReleasesNativeProcess(t *testing.T) {
	ledger := NewLedger()
	defer ledger.Close()
	path := filepath.Join(t.TempDir(), "processes.json")
	if left, err := ledger.Open(t.Context(), path); err != nil || left.OwnerAlive || len(left.Killed) != 0 {
		t.Fatal("open failed", left, err)
	}
	child := ledgerChild(t, ledger)
	entries := ledger.List()
	if len(entries) != 1 || entries[0].PID != child.PID() || entries[0].StartToken == "" || entries[0].Label != "test-child" {
		t.Fatal("native process not identified", entries)
	}
	file, err := readLedger(path)
	if err != nil || len(file.Processes) != 1 || file.Owner.PID != os.Getpid() || file.Owner.StartToken == "" {
		t.Fatal("journal missing", err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("journal not private", err)
		}
	}
	child.Kill()
	child.Wait()
	if len(ledger.List()) != 0 {
		t.Fatal("completed process retained")
	}
	file, err = readLedger(path)
	if err != nil || len(file.Processes) != 0 {
		t.Fatal("completion not persisted", err)
	}
	ledger.Close()
	if _, err := ledger.Track(424242, false, "later"); err == nil {
		t.Fatal("closed ledger accepted work")
	}
}
func TestLedgerLockProtectsConcurrentServerAndOldReleaseCannotDropReusedPID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "processes.json")
	one, two := NewLedger(), NewLedger()
	defer one.Close()
	defer two.Close()
	if _, err := one.Open(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if left, err := two.Open(t.Context(), path); err != nil || !left.OwnerAlive {
		t.Fatal("second server took journal", err)
	}
	first, err := two.Track(2147483000, true, "first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := two.Track(2147483000, true, "second")
	if err != nil {
		t.Fatal(err)
	}
	first()
	if entries := two.List(); len(entries) != 1 || entries[0].Label != "second" {
		t.Fatal("old release deleted reused PID")
	}
	second()
	if len(two.List()) != 0 {
		t.Fatal("in-memory process retained")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("second server overwrote live journal")
	}
	one.Close()
	three := NewLedger()
	defer three.Close()
	if left, err := three.Open(t.Context(), path); err != nil || left.OwnerAlive {
		t.Fatal("journal lock not released", err)
	}
}
func writeOldLedger(t *testing.T, path string, owner processOwner, entries []TrackedProcess) {
	t.Helper()
	data, err := json.Marshal(ledgerFile{Owner: owner, Processes: entries})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}
func TestLedgerReapsNativeAndLegacyLeftoversButPreservesPIDReuse(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "native", true: "legacy"}[legacy], func(t *testing.T) {
			memory := NewLedger()
			defer memory.Close()
			child := ledgerChild(t, memory)
			entry := memory.List()[0]
			if legacy {
				entry.StartToken = ""
			}
			path := filepath.Join(t.TempDir(), "processes.json")
			writeOldLedger(t, path, processOwner{PID: 2147483000, StartedAt: 0}, []TrackedProcess{entry})
			next := NewLedger()
			defer next.Close()
			left, err := next.Open(t.Context(), path)
			if err != nil || left.OwnerAlive || len(left.Killed) != 1 || left.Killed[0].PID != child.PID() {
				t.Fatal("leftover not reaped", left, err)
			}
			finished := make(chan struct{})
			go func() { child.Wait(); close(finished) }()
			select {
			case <-finished:
			case <-time.After(3 * time.Second):
				t.Fatal("reaped process still runs")
			}
		})
	}
	memory := NewLedger()
	defer memory.Close()
	child := ledgerChild(t, memory)
	for _, identity := range []struct {
		token string
		at    int64
	}{{"a-different-process", memory.List()[0].StartedAt}, {"", 1}} {
		entry := memory.List()[0]
		entry.StartToken, entry.StartedAt = identity.token, identity.at
		path := filepath.Join(t.TempDir(), "processes.json")
		writeOldLedger(t, path, processOwner{PID: 2147483000, StartedAt: 0}, []TrackedProcess{entry})
		next := NewLedger()
		left, err := next.Open(t.Context(), path)
		next.Close()
		if err != nil || len(left.Killed) != 0 {
			t.Fatal("reused PID authorized kill", left, err)
		}
		select {
		case <-child.Done():
			t.Fatal("unrelated process killed")
		default:
		}
	}
}
func TestLedgerRespectsAnUnmodifiedLegacyLiveOwner(t *testing.T) {
	memory := NewLedger()
	defer memory.Close()
	owner := ledgerChild(t, memory)
	worker := ledgerChild(t, memory)
	var ownerEntry, workerEntry TrackedProcess
	for _, entry := range memory.List() {
		if entry.PID == owner.PID() {
			ownerEntry = entry
		} else {
			workerEntry = entry
		}
	}
	path := filepath.Join(t.TempDir(), "processes.json")
	writeOldLedger(t, path, processOwner{PID: owner.PID(), StartedAt: ownerEntry.StartedAt}, []TrackedProcess{workerEntry})
	before, _ := os.ReadFile(path)
	ledger := NewLedger()
	defer ledger.Close()
	left, err := ledger.Open(t.Context(), path)
	if err != nil || !left.OwnerAlive || len(left.Killed) != 0 {
		t.Fatal("live legacy server ignored", left, err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("legacy journal overwritten")
	}
	select {
	case <-worker.Done():
		t.Fatal("legacy worker killed")
	default:
	}
}
func TestLedgerCorruptionLimitsAndFailureRollBack(t *testing.T) {
	for _, raw := range []string{"not json", `{"owner":{"pid":1},"processes":null}`, `{"owner":{"pid":0},"processes":[]}`} {
		path := filepath.Join(t.TempDir(), "processes.json")
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		ledger := NewLedger()
		left, err := ledger.Open(t.Context(), path)
		ledger.Close()
		if err != nil || len(left.Killed) != 0 {
			t.Fatal("corrupt journal authorized kill", err)
		}
		if _, err := readLedger(path); err != nil {
			t.Fatal("corrupt journal not repaired", err)
		}
	}
	ledger := NewLedger()
	defer ledger.Close()
	path := filepath.Join(t.TempDir(), "processes.json")
	if _, err := ledger.Open(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Track(2147483000, false, "failed"); err == nil || len(ledger.List()) != 0 {
		t.Fatal("failed journal write not rolled back")
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".processes-") {
			t.Fatal("failed write leaked temporary file")
		}
	}
}
func TestNativeStartTimesAreFiniteAndIdentifyThisProcess(t *testing.T) {
	values := StartTimes([]int{os.Getpid(), -1, 0, 2147483000, os.Getpid()})
	at, exists := values[os.Getpid()]
	if !exists || at <= 0 || at > time.Now().UnixMilli()+1000 {
		t.Fatal("native process start time unavailable")
	}
	if len(values) != 1 {
		t.Fatal("invalid process returned")
	}
	identity, err := identityOf(os.Getpid())
	if err != nil || identity.token == "" || !sameProcess(os.Getpid(), at, identity.token) {
		t.Fatal("process identity unstable", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	ledger := NewLedger()
	defer ledger.Close()
	if _, err := ledger.Open(ctx, filepath.Join(t.TempDir(), "processes.json")); err == nil {
		t.Fatal("cancelled open succeeded")
	}
}
