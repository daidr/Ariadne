package eventstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

type namesProjection struct{}

func (namesProjection) Setup(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS names (id TEXT PRIMARY KEY, project_id TEXT NOT NULL, name TEXT NOT NULL)")
	return err
}

func (namesProjection) Apply(ctx context.Context, tx *sql.Tx, event Event) error {
	if event.Type == "fail" {
		return errors.New("projection failed")
	}
	var data struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "INSERT INTO names VALUES (?, ?, ?)", event.ID, event.ProjectID, data.Name)
	return err
}

func (namesProjection) Forget(ctx context.Context, tx *sql.Tx, id string) error {
	_, err := tx.ExecContext(ctx, "DELETE FROM names WHERE project_id = ?", id)
	return err
}

func openTestStore(t *testing.T, options Options) *Store {
	t.Helper()
	if options.Path == "" {
		options.Path = ":memory:"
	}
	store, err := Open(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return store
}

func input(kind, project string) NewEvent {
	return NewEvent{Type: kind, ProjectID: project, Data: json.RawMessage(`{"name":"保留原文"}`)}
}

func TestCommitCallbacksFollowNestedRollbackAndCancellation(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, Options{})
	called := []string{}
	mark := func(name string) func() { return func() { called = append(called, name) } }
	err := store.Within(t.Context(), func(tx *Transaction) error {
		tx.AfterCommit(mark("outer"))
		if err := tx.Within(func(inner *Transaction) error {
			inner.AfterCommit(mark("rolled back"))
			return errors.New("handled")
		}); err == nil {
			t.Fatal("missing savepoint error")
		}
		return tx.Within(func(inner *Transaction) error { inner.AfterCommit(mark("inner")); return nil })
	})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(called) != "[outer inner]" {
		t.Fatal(called)
	}
	err = store.Within(t.Context(), func(tx *Transaction) error { tx.AfterCommit(mark("failed")); return errors.New("rollback") })
	if err == nil {
		t.Fatal("missing transaction error")
	}
	ctx, cancel := context.WithCancel(t.Context())
	err = store.Within(ctx, func(tx *Transaction) error { tx.AfterCommit(mark("cancelled")); cancel(); return nil })
	if err == nil {
		t.Fatal("cancelled transaction committed")
	}
	if fmt.Sprint(called) != "[outer inner]" {
		t.Fatal(called)
	}
}

func TestAtomicAppendAndProjectionRollback(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, Options{Projections: []Projection{namesProjection{}}})
	wake, cancel := store.Watch()
	defer cancel()
	if _, err := store.Append(t.Context(), []NewEvent{input("ok", "p1"), input("fail", "p1")}); err == nil {
		t.Fatal("failed projection committed")
	}
	if seq, err := store.LastSeq(t.Context()); err != nil || seq != 0 {
		t.Fatalf("log after rollback: %d, %v", seq, err)
	}
	var count int
	if err := store.DB().QueryRow("SELECT count(*) FROM names").Scan(&count); err != nil || count != 0 {
		t.Fatalf("projection after rollback: %d, %v", count, err)
	}
	select {
	case <-wake:
		t.Fatal("rollback notified subscribers")
	default:
	}
	events, err := store.Append(t.Context(), []NewEvent{input("ok", "p1")})
	if err != nil || len(events) != 1 {
		t.Fatalf("append: %v %v", events, err)
	}
	select {
	case <-wake:
	default:
		t.Fatal("commit did not notify")
	}
	if err := store.DB().QueryRow("SELECT count(*) FROM names").Scan(&count); err != nil || count != 1 {
		t.Fatalf("projection after commit: %d, %v", count, err)
	}
}

func TestNestedSavepointKeepsOnlySuccessfulEvents(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, Options{Projections: []Projection{namesProjection{}}})
	err := store.Within(t.Context(), func(tx *Transaction) error {
		if _, err := tx.Append([]NewEvent{input("before", "p1")}); err != nil {
			return err
		}
		err := tx.Within(func(inner *Transaction) error {
			if _, err := inner.Append([]NewEvent{input("discard", "p1")}); err != nil {
				return err
			}
			return errors.New("undo inner work")
		})
		if err == nil {
			return errors.New("inner transaction should fail")
		}
		_, err = tx.Append([]NewEvent{input("after", "p1")})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	events, err := store.Read(t.Context(), ReadOptions{})
	if err != nil || len(events) != 2 || events[0].Type != "before" || events[1].Type != "after" {
		t.Fatalf("nested transaction: %v, %v", events, err)
	}
}

func TestReadCursorProjectAndExistingSQLiteFile(t *testing.T) {
	t.Parallel()
	name := "含 空格 # 数据.sqlite"
	if runtime.GOOS != "windows" {
		name = "含 空格 # ? 数据.sqlite"
	}
	path := filepath.Join(t.TempDir(), name)
	store := openTestStore(t, Options{Path: path, Now: func() time.Time { return time.UnixMilli(1234) }})
	for _, project := range []string{"p1", "p2", "p1"} {
		if _, err := store.Append(t.Context(), []NewEvent{input("ok", project)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTestStore(t, Options{Path: path})
	events, err := reopened.Read(t.Context(), ReadOptions{AfterSeq: 1, ProjectID: "p1", Limit: 1})
	if err != nil || len(events) != 1 || events[0].Seq != 3 || events[0].At != 1234 {
		t.Fatalf("read existing log: %v, %v", events, err)
	}
	if string(events[0].Data) != `{"name":"保留原文"}` {
		t.Fatalf("changed JSON: %s", events[0].Data)
	}
}

func TestSubscriptionsAreBoundedAndReleased(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, Options{})
	wake, cancel := store.Watch()
	for n := range 100 {
		if _, err := store.Append(t.Context(), []NewEvent{input("ok", fmt.Sprintf("p%d", n))}); err != nil {
			t.Fatal(err)
		}
	}
	if len(wake) != 1 {
		t.Fatalf("queued %d wakeups", len(wake))
	}
	if seq, err := store.LastSeq(t.Context()); err != nil || seq != 100 {
		t.Fatalf("durable log: %d, %v", seq, err)
	}
	cancel()
	cancel()
	if len(store.watches) != 0 {
		t.Fatal("cancel retained subscription")
	}
	second, stopSecond := store.Watch()
	defer stopSecond()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, open := <-second; open {
		t.Fatal("close retained subscription")
	}
	if _, err := store.Append(t.Context(), []NewEvent{input("ok", "p1")}); !errors.Is(err, ErrClosed) {
		t.Fatalf("append after close: %v", err)
	}
}

func TestConcurrentAppendsAndReads(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, Options{Path: filepath.Join(t.TempDir(), "concurrent.sqlite")})
	var workers sync.WaitGroup
	errorsOut := make(chan error, 64)
	for n := range 32 {
		workers.Go(func() {
			_, err := store.Append(t.Context(), []NewEvent{input("ok", fmt.Sprintf("p%d", n))})
			if err != nil {
				errorsOut <- err
				return
			}
			_, err = store.Read(t.Context(), ReadOptions{Limit: 10})
			if err != nil {
				errorsOut <- err
			}
		})
	}
	workers.Wait()
	close(errorsOut)
	for err := range errorsOut {
		t.Error(err)
	}
	events, err := store.Read(t.Context(), ReadOptions{})
	if err != nil || len(events) != 32 {
		t.Fatalf("concurrent log: %d, %v", len(events), err)
	}
	ids := map[string]bool{}
	for i, event := range events {
		if event.Seq != int64(i+1) || ids[event.ID] {
			t.Fatalf("invalid sequence/id: %+v", event)
		}
		ids[event.ID] = true
	}
}

func TestInvalidEventAndCancelledTransactionLeaveNoWrites(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, Options{})
	for _, invalid := range []NewEvent{{}, {Type: "ok", ProjectID: "p1", Data: []byte("{")}} {
		if _, err := store.Append(t.Context(), []NewEvent{invalid}); err == nil {
			t.Fatal("accepted invalid event")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.Append(ctx, []NewEvent{input("ok", "p1")}); err == nil {
		t.Fatal("accepted cancelled transaction")
	}
	if seq, err := store.LastSeq(t.Context()); err != nil || seq != 0 {
		t.Fatalf("log after failure: %d, %v", seq, err)
	}
}

func TestPurgeIsAtomicAndKeepsOnlyTheTombstoneForTheProject(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, Options{Projections: []Projection{namesProjection{}}})
	if _, err := store.Append(t.Context(), []NewEvent{input("ok", "p1"), input("ok", "p2")}); err != nil {
		t.Fatal(err)
	}
	tombstone := NewEvent{Type: "project.deleted", ProjectID: "p1", Data: json.RawMessage(`{}`)}
	if _, err := store.Purge(t.Context(), "p1", tombstone, func(context.Context, *sql.Tx) error { return errors.New("refused") }); err == nil {
		t.Fatal("purge should fail")
	}
	before, err := store.Read(t.Context(), ReadOptions{})
	if err != nil || len(before) != 2 {
		t.Fatalf("failed purge changed log: %v, %v", before, err)
	}
	event, err := store.Purge(t.Context(), "p1", tombstone, nil)
	if err != nil || event.Seq != 3 {
		t.Fatalf("purge: %+v, %v", event, err)
	}
	after, err := store.Read(t.Context(), ReadOptions{ProjectID: "p1"})
	if err != nil || len(after) != 1 || after[0].Type != "project.deleted" {
		t.Fatalf("purged log: %v, %v", after, err)
	}
	var count int
	if err := store.DB().QueryRow("SELECT count(*) FROM names WHERE project_id = 'p1'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("project rows retained: %d, %v", count, err)
	}
	if err := store.DB().QueryRow("SELECT count(*) FROM names WHERE project_id = 'p2'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("other project changed: %d, %v", count, err)
	}
}

func TestRebuildAndBackupPreserveTheLog(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, Options{Projections: []Projection{namesProjection{}}})
	if _, err := store.Append(t.Context(), []NewEvent{input("ok", "p1"), input("ok", "p2")}); err != nil {
		t.Fatal(err)
	}
	before, err := store.Read(t.Context(), ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec("DELETE FROM names"); err != nil {
		t.Fatal(err)
	}
	if err := store.Rebuild(t.Context(), func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DROP TABLE names")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.DB().QueryRow("SELECT count(*) FROM names").Scan(&count); err != nil || count != 2 {
		t.Fatalf("rebuild rows: %d, %v", count, err)
	}
	path := filepath.Join(t.TempDir(), "backup.sqlite")
	if err := store.Backup(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	copy := openTestStore(t, Options{Path: path})
	after, err := copy.Read(t.Context(), ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	left, _ := json.Marshal(before)
	right, _ := json.Marshal(after)
	if string(left) != string(right) {
		t.Fatalf("backup changed log: %s / %s", left, right)
	}
	if err := store.Backup(t.Context(), path); err == nil {
		t.Fatal("backup overwrote existing destination")
	}
	empty := filepath.Join(t.TempDir(), "empty.sqlite")
	if err := os.WriteFile(empty, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.Backup(t.Context(), empty); !errors.Is(err, os.ErrExist) {
		t.Fatalf("existing empty backup was not protected: %v", err)
	}
	if info, err := os.Stat(empty); err != nil || info.Size() != 0 {
		t.Fatal("empty destination changed", info, err)
	}
}

func TestBackupFailureReleasesItsReservedFile(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, Options{})
	if err := store.DB().Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "failed.sqlite")
	if err := store.Backup(t.Context(), path); err == nil {
		t.Fatal("closed connection produced a backup")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed backup retained its reserved destination", err)
	}
}

func TestWatchProjectIsolatesWakeupsAndCleansUp(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, Options{})
	allWake, stopAll := store.Watch()
	defer stopAll()
	p1Wake, stopP1 := store.WatchProject("p1")
	defer stopP1()
	p2Wake, stopP2 := store.WatchProject("p2")
	defer stopP2()

	if _, err := store.Append(t.Context(), []NewEvent{input("ok", "p1")}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-allWake:
	default:
		t.Fatal("global watch missed p1 append")
	}
	select {
	case <-p1Wake:
	default:
		t.Fatal("p1 watch missed p1 append")
	}
	select {
	case <-p2Wake:
		t.Fatal("p2 watch woke for p1 append")
	default:
	}

	// Rolled-back savepoint for p2 must not wake p2 when outer tx commits p1.
	if err := store.Within(t.Context(), func(tx *Transaction) error {
		if _, err := tx.Append([]NewEvent{input("outer", "p1")}); err != nil {
			return err
		}
		_ = tx.Within(func(inner *Transaction) error {
			if _, err := inner.Append([]NewEvent{input("rolled", "p2")}); err != nil {
				return err
			}
			return errors.New("rollback p2")
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-allWake:
	default:
		t.Fatal("global watch missed outer commit")
	}
	select {
	case <-p1Wake:
	default:
		t.Fatal("p1 watch missed outer commit")
	}
	select {
	case <-p2Wake:
		t.Fatal("p2 watch woke for rolled-back savepoint")
	default:
	}

	// Purge wakes the purged project and global watchers, not unrelated projects.
	if _, err := store.Purge(t.Context(), "p1", NewEvent{
		Type:      "project.deleted",
		ProjectID: "p1",
		Data:      json.RawMessage(`{"id":"p1"}`),
	}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-allWake:
	default:
		t.Fatal("global watch missed purge")
	}
	select {
	case <-p1Wake:
	default:
		t.Fatal("p1 watch missed purge")
	}
	select {
	case <-p2Wake:
		t.Fatal("p2 watch woke for p1 purge")
	default:
	}

	stopP1()
	stopP2()
	store.watchMu.Lock()
	projectGroups := len(store.projectWatches)
	store.watchMu.Unlock()
	if projectGroups != 0 {
		t.Fatalf("cancelled project watchers retained %d map groups", projectGroups)
	}
}

func TestRecentRingBufferServesLiveReadsAndHandlesPurgeEviction(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, Options{})
	if _, err := store.Append(t.Context(), []NewEvent{
		input("a", "p1"),
		input("b", "p2"),
		input("c", "p1"),
	}); err != nil {
		t.Fatal(err)
	}

	// Live read hits ring buffer and clones Data so caller mutation cannot poison cache.
	got, ok := store.readRecent(ReadOptions{AfterSeq: 0, ProjectID: "p1", Limit: 10}, 10)
	if !ok || len(got) != 2 || got[0].Seq != 1 || got[1].Seq != 3 {
		t.Fatalf("unexpected ring read: ok=%v events=%+v", ok, got)
	}
	got[0].Data[0] = 'X'
	again, err := store.Read(t.Context(), ReadOptions{AfterSeq: 0, ProjectID: "p1", Limit: 10})
	if err != nil || len(again) != 2 || string(again[0].Data) != `{"name":"保留原文"}` {
		t.Fatalf("caller mutation leaked into ring buffer: %+v err=%v", again, err)
	}

	// OmitData returns empty payload matching SQLite behavior.
	omitted, err := store.Read(t.Context(), ReadOptions{AfterSeq: 0, OmitData: true})
	if err != nil || len(omitted) != 3 || len(omitted[0].Data) != 0 {
		t.Fatalf("OmitData mismatch: %+v err=%v", omitted, err)
	}

	// Purge removes p1's prior events from the ring and keeps only the tombstone.
	tombstone, err := store.Purge(t.Context(), "p1", NewEvent{
		Type:      "project.deleted",
		ProjectID: "p1",
		Data:      json.RawMessage(`{"id":"p1"}`),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	afterPurge, ok := store.readRecent(ReadOptions{AfterSeq: 0, ProjectID: "p1", Limit: 10}, 10)
	if !ok || len(afterPurge) != 1 || afterPurge[0].Seq != tombstone.Seq || afterPurge[0].Type != "project.deleted" {
		t.Fatalf("ring buffer after purge: ok=%v events=%+v", ok, afterPurge)
	}

	// Exceeding recentEventLimit evicts old entries and falls back to SQLite seamlessly.
	batch := make([]NewEvent, recentEventLimit+20)
	for i := range batch {
		batch[i] = input(fmt.Sprintf("ev-%d", i), "p2")
	}
	if _, err := store.Append(t.Context(), batch); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.readRecent(ReadOptions{AfterSeq: 0, Limit: 10}, 10); ok {
		t.Fatal("expected historical AfterSeq=0 to miss evicted ring buffer")
	}
	all, err := store.Read(t.Context(), ReadOptions{AfterSeq: 0, Limit: 1000})
	if err != nil || len(all) != len(batch)+2 {
		t.Fatalf("historical fallback read: len=%d err=%v", len(all), err)
	}
	recentTail, ok := store.readRecent(ReadOptions{AfterSeq: all[len(all)-5].Seq, Limit: 10}, 10)
	if !ok || len(recentTail) != 4 || recentTail[3].Seq != all[len(all)-1].Seq {
		t.Fatalf("recent tail miss: ok=%v tail=%+v", ok, recentTail)
	}
}

func TestSQLiteConnectionPragmas(t *testing.T) {
	t.Parallel()
	check := func(t *testing.T, store *Store, wantWAL bool) {
		t.Helper()
		ctx := t.Context()
		// Hold multiple concurrent pooled connections to verify PRAGMAs apply to every physical connection.
		conns := make([]*sql.Conn, 4)
		for i := range conns {
			conn, err := store.DB().Conn(ctx)
			if err != nil {
				t.Fatalf("conn %d: %v", i, err)
			}
			conns[i] = conn
			defer conn.Close()
		}
		for i, conn := range conns {
			var cacheSize, tempStore, foreignKeys, busyTimeout, synchronous int
			var journalMode string
			if err := conn.QueryRowContext(ctx, "PRAGMA cache_size").Scan(&cacheSize); err != nil {
				t.Fatalf("conn %d cache_size: %v", i, err)
			}
			if cacheSize != -32000 {
				t.Fatalf("conn %d cache_size = %d, want -32000", i, cacheSize)
			}
			if err := conn.QueryRowContext(ctx, "PRAGMA temp_store").Scan(&tempStore); err != nil {
				t.Fatalf("conn %d temp_store: %v", i, err)
			}
			if tempStore != 2 {
				t.Fatalf("conn %d temp_store = %d, want 2 (MEMORY)", i, tempStore)
			}
			if err := conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
				t.Fatalf("conn %d foreign_keys: %v", i, err)
			}
			if foreignKeys != 1 {
				t.Fatalf("conn %d foreign_keys = %d, want 1", i, foreignKeys)
			}
			if err := conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
				t.Fatalf("conn %d busy_timeout: %v", i, err)
			}
			if busyTimeout != 5000 {
				t.Fatalf("conn %d busy_timeout = %d, want 5000", i, busyTimeout)
			}
			if err := conn.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous); err != nil {
				t.Fatalf("conn %d synchronous: %v", i, err)
			}
			if synchronous != 1 {
				t.Fatalf("conn %d synchronous = %d, want 1 (NORMAL)", i, synchronous)
			}
			if wantWAL {
				if err := conn.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
					t.Fatalf("conn %d journal_mode: %v", i, err)
				}
				if journalMode != "wal" {
					t.Fatalf("conn %d journal_mode = %q, want wal", i, journalMode)
				}
			}
		}
	}

	t.Run("memory", func(t *testing.T) {
		store := openTestStore(t, Options{Path: ":memory:"})
		check(t, store, false)
	})
	t.Run("file", func(t *testing.T) {
		store := openTestStore(t, Options{Path: filepath.Join(t.TempDir(), "pragmas.sqlite")})
		check(t, store, true)
	})
}
