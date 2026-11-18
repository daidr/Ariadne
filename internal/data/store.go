// Package data manages database snapshots and directory usage independently of
// the HTTP layer. Slow usage scans are shared and never hold the database writer.
package data

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/eventstore"
	"github.com/daidr/Ariadne/internal/fsutil"
)

const AutoBackupsKept = 7

var backupName = regexp.MustCompile(`^ariadne-[0-9]{8}-[0-9]{6}(-[0-9]+)?(-auto)?\.db$`)

type BackupInfo struct {
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	CreatedAt int64  `json:"createdAt"`
	Auto      bool   `json:"auto"`
}
type Usage struct {
	Home       string       `json:"home"`
	Database   int64        `json:"database"`
	Plugins    *int64       `json:"plugins"`
	Backups    []BackupInfo `json:"backups"`
	BackupsDir string       `json:"backupsDir"`
}
type Options struct {
	Events             *eventstore.Store
	Home, DatabasePath string
	Now                func() time.Time
	Scan               func(context.Context, string) *int64
	ScanTTL            time.Duration
}
type scan struct {
	done       chan struct{}
	bytes      *int64
	generation uint64
}
type Store struct {
	options       Options
	backupMu      sync.Mutex
	mu            sync.Mutex
	ctx           context.Context
	cancel        context.CancelFunc
	closed        bool
	pending       *scan
	known         bool
	bytes         *int64
	expires       time.Time
	generation    uint64
	writingBackup string
}

func New(options Options) (*Store, error) {
	if options.Events == nil || options.Home == "" || options.DatabasePath == "" {
		return nil, errors.New("data store events, home, and database path are required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Scan == nil {
		options.Scan = fsutil.DiskUsage
	}
	if options.ScanTTL <= 0 {
		options.ScanTTL = 30 * time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Store{options: options, ctx: ctx, cancel: cancel}, nil
}
func (store *Store) backupsDir() string { return filepath.Join(store.options.Home, "backups") }
func (store *Store) Backups() ([]BackupInfo, error) {
	entries, err := os.ReadDir(store.backupsDir())
	if errors.Is(err, os.ErrNotExist) {
		return []BackupInfo{}, nil
	}
	if err != nil {
		return nil, err
	}
	result := []BackupInfo{}
	for _, entry := range entries {
		store.mu.Lock()
		writing := store.writingBackup
		store.mu.Unlock()
		if entry.Name() == writing || !backupName.MatchString(entry.Name()) {
			continue
		}
		info, err := os.Stat(filepath.Join(store.backupsDir(), entry.Name()))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		result = append(result, BackupInfo{Name: entry.Name(), Size: info.Size(), CreatedAt: info.ModTime().UnixMilli(), Auto: strings.HasSuffix(entry.Name(), "-auto.db")})
	}
	slices.SortStableFunc(result, func(a, b BackupInfo) int {
		if a.CreatedAt > b.CreatedAt {
			return -1
		}
		if a.CreatedAt < b.CreatedAt {
			return 1
		}
		return 0
	})
	return result, nil
}
func fileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}
func (store *Store) Usage(ctx context.Context) (Usage, error) {
	result := Usage{Home: store.options.Home, BackupsDir: store.backupsDir()}
	for _, path := range []string{store.options.DatabasePath, store.options.DatabasePath + "-wal", store.options.DatabasePath + "-shm"} {
		size, err := fileSize(path)
		if err != nil {
			return Usage{}, err
		}
		result.Database += size
	}
	var err error
	result.Backups, err = store.Backups()
	if err != nil {
		return Usage{}, err
	}
	result.Plugins, err = store.plugins(ctx)
	return result, err
}
func (store *Store) plugins(ctx context.Context) (*int64, error) {
	store.mu.Lock()
	if store.closed {
		store.mu.Unlock()
		return nil, errors.New("data store is closed")
	}
	if store.known && store.options.Now().Before(store.expires) {
		value := store.bytes
		store.mu.Unlock()
		return copyBytes(value), nil
	}
	work := store.pending
	if work == nil {
		work = &scan{done: make(chan struct{}), generation: store.generation}
		store.pending = work
		go func() {
			dir := filepath.Join(store.options.Home, "plugins")
			var bytes *int64
			if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
				zero := int64(0)
				bytes = &zero
			} else {
				ctx, cancel := context.WithTimeout(store.ctx, time.Minute)
				bytes = store.options.Scan(ctx, dir)
				cancel()
			}
			store.mu.Lock()
			work.bytes = bytes
			if !store.closed && work.generation == store.generation {
				store.known = true
				store.bytes = bytes
				store.expires = store.options.Now().Add(store.options.ScanTTL)
			}
			store.pending = nil
			close(work.done)
			store.mu.Unlock()
		}()
	}
	store.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-work.done:
		return copyBytes(work.bytes), nil
	}
}

// InvalidateUsage is called after worktree removal or a source change. An older
// scan may finish, so waiters share it, but it cannot become a fresh cache entry.
func (store *Store) InvalidateUsage() {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.known = false
	store.generation++
	store.bytes = nil
	store.expires = time.Time{}
}
func (store *Store) Backup(ctx context.Context, auto bool, now time.Time) (BackupInfo, error) {
	store.backupMu.Lock()
	defer store.backupMu.Unlock()
	return store.backup(ctx, auto, now)
}
func (store *Store) backup(ctx context.Context, auto bool, now time.Time) (BackupInfo, error) {
	if store.ctx.Err() != nil {
		return BackupInfo{}, errors.New("data store is closed")
	}
	invocation, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(store.ctx, cancel)
	defer stop()
	ctx = invocation
	if err := os.MkdirAll(store.backupsDir(), 0700); err != nil {
		return BackupInfo{}, err
	}
	tail := ".db"
	if auto {
		tail = "-auto.db"
	}
	stamp := now.UTC().Format("20060102-150405")
	for n := 1; ; n++ {
		name := "ariadne-" + stamp
		if n > 1 {
			name += fmt.Sprintf("-%d", n)
		}
		name += tail
		path := filepath.Join(store.backupsDir(), name)
		if _, err := os.Lstat(path); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return BackupInfo{}, err
		}
		store.mu.Lock()
		store.writingBackup = name
		store.mu.Unlock()
		defer func() { store.mu.Lock(); store.writingBackup = ""; store.mu.Unlock() }()
		if err := store.options.Events.Backup(ctx, path); err != nil {
			return BackupInfo{}, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return BackupInfo{}, err
		}
		return BackupInfo{Name: name, Size: info.Size(), CreatedAt: info.ModTime().UnixMilli(), Auto: auto}, nil
	}
}
func (store *Store) DailyBackup(ctx context.Context, now time.Time) (*BackupInfo, error) {
	store.backupMu.Lock()
	defer store.backupMu.Unlock()
	backups, err := store.Backups()
	if err != nil {
		return nil, err
	}
	daily := []BackupInfo{}
	for _, backup := range backups {
		if backup.Auto {
			daily = append(daily, backup)
		}
	}
	if len(daily) > 0 {
		last := daily[0]
		lastDay := time.UnixMilli(last.CreatedAt).In(time.Local)
		local := now.In(time.Local)
		sameDay := lastDay.Year() == local.Year() && lastDay.YearDay() == local.YearDay()
		written := int64(0)
		for _, path := range []string{store.options.DatabasePath, store.options.DatabasePath + "-wal"} {
			info, err := os.Stat(path)
			if err == nil {
				written = max(written, info.ModTime().UnixMilli())
			} else if !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
		}
		if sameDay || written <= last.CreatedAt {
			return nil, nil
		}
	}
	made, err := store.backup(ctx, true, now)
	if err != nil {
		return nil, err
	}
	backups, err = store.Backups()
	if err != nil {
		return nil, err
	}
	count := 0
	for _, backup := range backups {
		if backup.Auto {
			count++
			if count > AutoBackupsKept {
				if err := os.Remove(filepath.Join(store.backupsDir(), backup.Name)); err != nil && !errors.Is(err, os.ErrNotExist) {
					return nil, err
				}
			}
		}
	}
	return &made, nil
}
func (store *Store) RemoveBackup(name string) error {
	if !backupName.MatchString(name) || filepath.Base(name) != name {
		return domainerr.New("error.invalidBody", map[string]any{"reason": "name"})
	}
	store.backupMu.Lock()
	defer store.backupMu.Unlock()
	err := os.Remove(filepath.Join(store.backupsDir(), name))
	if errors.Is(err, os.ErrNotExist) {
		return domainerr.New("error.invalidBody", map[string]any{"reason": "name"})
	}
	return err
}
func (store *Store) Close() {
	store.mu.Lock()
	store.closed = true
	store.cancel()
	store.bytes = nil
	store.known = false
	work := store.pending
	store.mu.Unlock()
	if work != nil {
		<-work.done
	}
	store.backupMu.Lock()
	store.backupMu.Unlock()
}
func copyBytes(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
