package data

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/daidr/Ariadne/internal/fsutil"
)

type StorageSnapshot struct {
	fsutil.StorageSizes
	MeasuredAt int64 `json:"measuredAt"`
}

type storageScan struct {
	paths      []string
	generation uint64
	done       chan struct{}
	cancel     context.CancelFunc
	result     *StorageSnapshot
	err        error
	expires    time.Time
}

const storageCacheLimit = 2 << 20

func (store *Store) storageCachePath() string {
	return filepath.Join(store.options.Home, "storage-v1.json")
}

func (store *Store) readStorageCache() *storageScan {
	path := store.storageCachePath()
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > storageCacheLimit {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	buf, err := io.ReadAll(io.LimitReader(file, storageCacheLimit+1))
	var result StorageSnapshot
	if err != nil || len(buf) > storageCacheLimit || json.Unmarshal(buf, &result) != nil ||
		result.MeasuredAt <= 0 || len(result.Worktrees) > 2048 {
		return nil
	}
	// A restored result is display-only until this process measures current paths.
	return &storageScan{result: &result}
}

func (store *Store) saveStorageCache(snapshot *StorageSnapshot) {
	if snapshot.Managed == nil && snapshot.Total == nil {
		return
	}
	buf, err := json.Marshal(snapshot)
	if err != nil || len(buf) > storageCacheLimit {
		return
	}
	file, err := os.CreateTemp(store.options.Home, ".storage-*.tmp")
	if err != nil {
		return
	}
	name := file.Name()
	defer os.Remove(name)
	_, writeErr := file.Write(buf)
	closeErr := file.Close()
	if writeErr == nil && closeErr == nil {
		_ = os.Rename(name, store.storageCachePath())
	}
}

// LatestStorage can be displayed while a fresh measurement is in flight.
func (store *Store) LatestStorage() *StorageSnapshot {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.storage == nil {
		return nil
	}
	return store.storage.result
}

func (store *Store) MeasureStorage(ctx context.Context, paths []string) (*StorageSnapshot, error) {
	paths = slices.Clone(paths)
	slices.Sort(paths)
	paths = slices.Compact(paths)
	var requestGeneration uint64
	started := false
	for ctx.Err() == nil {
		store.mu.Lock()
		if store.closed {
			store.mu.Unlock()
			return nil, errors.New("data store is closed")
		}
		if !started {
			started = true
			requestGeneration = store.generation
		} else if requestGeneration != store.generation {
			store.mu.Unlock()
			return nil, nil
		}
		if saved := store.storage; saved != nil && slices.Equal(saved.paths, paths) && store.options.Now().Before(saved.expires) {
			store.mu.Unlock()
			return saved.result, nil
		}
		scan := store.storagePending
		if scan == nil {
			scan = store.startStorageScan(paths)
		}
		store.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-scan.done:
			store.mu.Lock()
			current := scan.generation == store.generation && !store.closed
			store.mu.Unlock()
			if current && slices.Equal(scan.paths, paths) {
				return scan.result, scan.err
			}
		}
	}
	return nil, ctx.Err()
}

// One worker per store, even when several pages ask for measurements.
func (store *Store) startStorageScan(paths []string) *storageScan {
	ctx, cancel := context.WithTimeout(store.ctx, time.Minute)
	scan := &storageScan{paths: paths, generation: store.generation, done: make(chan struct{}), cancel: cancel}
	store.storagePending = scan
	go func() {
		defer cancel()
		sizes := store.options.Measure(ctx, filepath.Join(store.options.Home, "plugins"), paths)
		now := store.options.Now()
		scan.err = ctx.Err()
		if scan.err == nil {
			scan.result = &StorageSnapshot{StorageSizes: sizes, MeasuredAt: now.UnixMilli()}
		}
		scan.expires = now.Add(store.options.ScanTTL)
		store.mu.Lock()
		if scan.err == nil && !store.closed && scan.generation == store.generation && len(paths) <= 2048 {
			store.storage = scan
			store.saveStorageCache(scan.result)
		}
		store.storagePending = nil
		close(scan.done)
		store.mu.Unlock()
	}()
	return scan
}
