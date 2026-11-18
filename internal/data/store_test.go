package data

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/core"
)

func testStore(t *testing.T, scan func(context.Context, string) *int64) (*Store, *core.Repository) {
	t.Helper()
	home := t.TempDir()
	path := filepath.Join(home, "ariadne.db")
	repo, err := core.Open(t.Context(), core.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repo.Close() })
	store, err := New(Options{Events: repo.Store, Home: home, DatabasePath: path, Scan: scan})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	return store, repo
}
func TestBackupNamesRestoreRemovalAndDailyRetention(t *testing.T) {
	store, repo := testStore(t, nil)
	ctx := t.Context()
	name := "备份测试"
	if _, err := repo.CreateProject(ctx, &name, core.ProjectStart{}); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now()
	first, err := store.Backup(ctx, false, stamp)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Backup(ctx, false, stamp)
	if err != nil {
		t.Fatal(err)
	}
	if first.Name == second.Name || !backupName.MatchString(first.Name) || !backupName.MatchString(second.Name) {
		t.Fatal(first, second)
	}
	copy, err := core.Open(ctx, core.Options{Path: filepath.Join(store.backupsDir(), first.Name)})
	if err != nil {
		t.Fatal(err)
	}
	projects, err := copy.ListProjects(ctx)
	copy.Close()
	if err != nil || len(projects) != 1 || projects[0].Name != name {
		t.Fatal(projects, err)
	}
	if err := store.RemoveBackup("../ariadne.db"); err == nil {
		t.Fatal("path escape accepted")
	}
	if err := store.RemoveBackup(first.Name); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveBackup(first.Name); err == nil {
		t.Fatal("missing backup accepted")
	}
	if made, err := store.DailyBackup(ctx, stamp); err != nil || made == nil {
		t.Fatal(made, err)
	}
	if made, err := store.DailyBackup(ctx, stamp); err != nil || made != nil {
		t.Fatal(made, err)
	}
	// Seed older snapshots with real, ordered timestamps. The current snapshot
	// must sort ahead of them before retention runs.
	for day := 1; day <= 9; day++ {
		at := stamp.AddDate(0, 0, -day)
		made, err := store.Backup(ctx, true, at)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(filepath.Join(store.backupsDir(), made.Name), at, at); err != nil {
			t.Fatal(err)
		}
	}
	backups, err := store.Backups()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range backups {
		if item.Auto && item.CreatedAt >= stamp.Add(-time.Hour).UnixMilli() {
			at := stamp.Add(-2 * time.Hour)
			if err := os.Chtimes(filepath.Join(store.backupsDir(), item.Name), at, at); err != nil {
				t.Fatal(err)
			}
		}
	}
	changed := stamp.Add(-time.Hour)
	for _, path := range []string{store.options.DatabasePath, store.options.DatabasePath + "-wal"} {
		if err := os.Chtimes(path, changed, changed); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	if made, err := store.DailyBackup(ctx, stamp.AddDate(0, 0, 1)); err != nil || made == nil {
		t.Fatal(made, err)
	}
	if made, err := store.DailyBackup(ctx, stamp.AddDate(0, 0, 2)); err != nil || made != nil {
		t.Fatal(made, err)
	}
	backups, err = store.Backups()
	if err != nil {
		t.Fatal(err)
	}
	count, mine := 0, false
	for _, item := range backups {
		if item.Auto {
			count++
		}
		if item.Name == second.Name {
			mine = true
		}
	}
	if count != AutoBackupsKept || !mine {
		t.Fatal(backups)
	}
}
func TestUsageScansShareWorkExpireAndClose(t *testing.T) {
	var calls atomic.Int64
	entered, release := make(chan struct{}, 10), make(chan struct{}, 10)
	store, _ := testStore(t, func(ctx context.Context, _ string) *int64 {
		calls.Add(1)
		entered <- struct{}{}
		select {
		case <-ctx.Done():
			return nil
		case <-release:
			value := int64(123)
			return &value
		}
	})
	if err := os.Mkdir(filepath.Join(store.options.Home, "plugins"), 0700); err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for i := 0; i < 20; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			usage, err := store.Usage(t.Context())
			if err != nil || usage.Plugins == nil || *usage.Plugins != 123 {
				t.Errorf("%+v %v", usage, err)
			}
		}()
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("scan did not start")
	}
	release <- struct{}{}
	group.Wait()
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
	usage, err := store.Usage(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	*usage.Plugins = 999
	usage, err = store.Usage(t.Context())
	if err != nil || *usage.Plugins != 123 {
		t.Fatal("caller mutated cached value", usage, err)
	}
	store.mu.Lock()
	store.expires = time.Time{}
	store.mu.Unlock()
	done := make(chan struct{})
	go func() { defer close(done); store.Usage(t.Context()) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("expired scan did not refresh")
	}
	store.InvalidateUsage()
	release <- struct{}{}
	<-done
	store.mu.Lock()
	known := store.known
	store.mu.Unlock()
	if known {
		t.Fatal("invalidated scan became a fresh cache entry")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.Usage(ctx); err == nil {
		t.Fatal("cancelled caller did not exit")
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("refresh did not start")
	}
	store.Close()
	store.mu.Lock()
	pending, bytes, known := store.pending, store.bytes, store.known
	store.mu.Unlock()
	if pending != nil || bytes != nil || known {
		t.Fatal("closed store retained scan state")
	}
}

func TestDailyBackupStopsAndCanRestart(t *testing.T) {
	store, _ := testStore(t, nil)
	daily := &Daily{Store: store, OnError: func(err error) { t.Error(err) }}
	daily.Start(t.Context())
	daily.Start(t.Context())
	until := time.NewTimer(5 * time.Second)
	defer until.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		backups, err := store.Backups()
		if err != nil {
			t.Fatal(err)
		}
		if len(backups) > 0 {
			break
		}
		select {
		case <-until.C:
			t.Fatal("initial daily backup did not run")
		case <-ticker.C:
		}
	}
	if err := daily.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	daily.Start(t.Context())
	if err := daily.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	backups, err := store.Backups()
	if err != nil || len(backups) != 1 {
		t.Fatal(backups, err)
	}
}
