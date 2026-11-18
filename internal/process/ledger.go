package process

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
	"unicode/utf8"
)

const maxTrackedProcesses = 8192

type TrackedProcess struct {
	PID        int    `json:"pid"`
	Group      bool   `json:"group"`
	StartedAt  int64  `json:"startedAt"`
	Label      string `json:"label"`
	StartToken string `json:"startToken,omitempty"`
}
type processOwner struct {
	PID        int    `json:"pid"`
	StartedAt  int64  `json:"startedAt"`
	StartToken string `json:"startToken,omitempty"`
}
type ledgerFile struct {
	Owner     processOwner     `json:"owner"`
	Processes []TrackedProcess `json:"processes"`
}
type Leftovers struct {
	Killed     []TrackedProcess `json:"killed"`
	OwnerAlive bool             `json:"ownerAlive"`
}
type Ledger struct {
	mu      sync.Mutex
	path    string
	owner   processOwner
	running map[int]*TrackedProcess
	order   []int
	lock    *os.File
	closed  bool
}
type processIdentity struct {
	startedAt int64
	token     string
	exited    bool
}

func NewLedger() *Ledger {
	owner := processOwner{PID: os.Getpid(), StartedAt: time.Now().UnixMilli()}
	if identity, err := identityOf(owner.PID); err == nil {
		owner.StartedAt, owner.StartToken = identity.startedAt, identity.token
	}
	return &Ledger{owner: owner, running: map[int]*TrackedProcess{}}
}

// DefaultLedger tracks child processes in memory until the native CLI opens its
// private journal. It must be closed after the services that own those children.
var DefaultLedger = NewLedger()

func readLedger(path string) (ledgerFile, error) {
	var file ledgerFile
	handle, err := os.Open(path)
	if err != nil {
		return file, err
	}
	defer handle.Close()
	data, err := io.ReadAll(io.LimitReader(handle, 8*1024*1024+1))
	if err != nil {
		return file, err
	}
	if len(data) > 8*1024*1024 {
		return file, errors.New("process ledger exceeds 8 MiB")
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return file, err
	}
	if file.Owner.PID <= 0 || file.Processes == nil || len(file.Processes) > maxTrackedProcesses {
		return file, errors.New("invalid process ledger")
	}
	return file, nil
}
func sameProcess(pid int, at int64, token string) bool {
	if pid <= 0 || pid > 1<<31-1 {
		return false
	}
	actual, err := identityOf(pid)
	if err != nil {
		return false
	}
	if token != "" {
		return token == actual.token
	}
	delta := actual.startedAt - at
	return delta >= -3000 && delta <= 3000
}
func liveOwner(owner processOwner) bool {
	identity, err := identityOf(owner.PID)
	return err == nil && !identity.exited && sameProcess(owner.PID, owner.StartedAt, owner.StartToken)
}
func (ledger *Ledger) Open(ctx context.Context, path string) (Leftovers, error) {
	left := Leftovers{Killed: []TrackedProcess{}}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed || ledger.path != "" {
		return left, errors.New("process ledger is closed or already open")
	}
	if err := ctx.Err(); err != nil {
		return left, err
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return left, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return left, err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return left, err
	}
	locked, err := tryLedgerLock(lock)
	if err != nil {
		lock.Close()
		return left, err
	}
	if !locked {
		lock.Close()
		left.OwnerAlive = true
		return left, nil
	}
	keep := false
	defer func() {
		if !keep {
			releaseLedgerLock(lock)
			lock.Close()
		}
	}()
	file, readErr := readLedger(path)
	if readErr == nil {
		if file.Owner.PID != os.Getpid() && liveOwner(file.Owner) {
			left.OwnerAlive = true
			return left, nil
		}
		for _, entry := range file.Processes {
			if err := ctx.Err(); err != nil {
				return left, err
			}
			if entry.PID <= 0 || entry.PID == os.Getpid() || entry.PID > 1<<31-1 {
				continue
			}
			if sameProcess(entry.PID, entry.StartedAt, entry.StartToken) {
				if err := killRecorded(ctx, entry); err == nil {
					left.Killed = append(left.Killed, entry)
				}
			} else if runtime.GOOS != "windows" && entry.Group && !identityExists(entry.PID) && recordedGroupExists(entry.PID) {
				if err := killRecorded(ctx, entry); err == nil {
					left.Killed = append(left.Killed, entry)
				}
			}
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		// A corrupt journal cannot authorize killing any process. Replace it
		// with a valid private journal for this server's new children.
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
			return left, readErr
		}
	}
	ledger.path, ledger.lock = path, lock
	if err := ledger.writeLocked(); err != nil {
		ledger.path = ""
		ledger.lock = nil
		return left, err
	}
	keep = true
	return left, nil
}
func identityExists(pid int) bool { return recordedPIDExists(pid) }
func (ledger *Ledger) Track(pid int, group bool, label string) (func(), error) {
	if pid <= 0 || pid == os.Getpid() || pid > 1<<31-1 {
		return func() {}, nil
	}
	if len(label) > 256 {
		label = label[:256]
		for !utf8.ValidString(label) {
			label = label[:len(label)-1]
		}
	}
	entry := &TrackedProcess{PID: pid, Group: group, Label: label, StartedAt: time.Now().UnixMilli()}
	if identity, err := identityOf(pid); err == nil {
		entry.StartedAt, entry.StartToken = identity.startedAt, identity.token
	}
	ledger.mu.Lock()
	if ledger.closed {
		ledger.mu.Unlock()
		return nil, errors.New("process ledger is closed")
	}
	old := ledger.running[pid]
	if old == nil && len(ledger.running) >= maxTrackedProcesses {
		ledger.mu.Unlock()
		return nil, errors.New("too many tracked processes")
	}
	if old == nil {
		ledger.order = append(ledger.order, pid)
	}
	ledger.running[pid] = entry
	if err := ledger.writeLocked(); err != nil {
		if old == nil {
			delete(ledger.running, pid)
			ledger.order = ledger.order[:len(ledger.order)-1]
		} else {
			ledger.running[pid] = old
		}
		ledger.mu.Unlock()
		return nil, err
	}
	ledger.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			ledger.mu.Lock()
			if ledger.running[pid] != entry {
				ledger.mu.Unlock()
				return
			}
			delete(ledger.running, pid)
			for i, id := range ledger.order {
				if id == pid {
					copy(ledger.order[i:], ledger.order[i+1:])
					ledger.order[len(ledger.order)-1] = 0
					ledger.order = ledger.order[:len(ledger.order)-1]
					break
				}
			}
			err := ledger.writeLocked()
			ledger.mu.Unlock()
			if err != nil {
				log.Printf("could not update process ledger: %v", err)
			}
		})
	}, nil
}
func (ledger *Ledger) listLocked() []TrackedProcess {
	list := make([]TrackedProcess, 0, len(ledger.running))
	for _, pid := range ledger.order {
		if entry := ledger.running[pid]; entry != nil {
			list = append(list, *entry)
		}
	}
	return list
}
func (ledger *Ledger) List() []TrackedProcess {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	return ledger.listLocked()
}
func (ledger *Ledger) writeLocked() error {
	if ledger.path == "" {
		return nil
	}
	data, err := json.Marshal(ledgerFile{Owner: ledger.owner, Processes: ledger.listLocked()})
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(ledger.path), ".processes-*.tmp")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(temporary, ledger.path)
}
func (ledger *Ledger) Close() error {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return nil
	}
	ledger.closed = true
	// Preserve still-running entries on disk: closing the journal alone does
	// not assert that its process owners have completed shutdown.
	var err error
	if ledger.lock != nil {
		err = ledger.writeLocked()
		releaseLedgerLock(ledger.lock)
		err = errors.Join(err, ledger.lock.Close())
		ledger.lock = nil
	}
	ledger.path = ""
	clear(ledger.running)
	ledger.order = nil
	return err
}

func StartTimes(pids []int) map[int]int64 {
	result := map[int]int64{}
	for _, pid := range pids {
		if pid <= 0 || pid > 1<<31-1 {
			continue
		}
		if _, exists := result[pid]; exists {
			continue
		}
		if identity, err := identityOf(pid); err == nil {
			result[pid] = identity.startedAt
		}
	}
	return result
}
func ledgerError(action string, err error) error {
	return fmt.Errorf("process ledger %s: %w", action, err)
}
