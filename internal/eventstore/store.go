// Package eventstore stores the existing append-only SQLite log. Domain projections
// run in the same transaction as each append; subscriptions are bounded wakeups.
package eventstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

var ErrClosed = errors.New("event store is closed")

type NewEvent struct {
	Type      string          `json:"type"`
	ProjectID string          `json:"projectId"`
	Data      json.RawMessage `json:"data"`
}

type Event struct {
	Seq       int64           `json:"seq"`
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	ProjectID string          `json:"projectId"`
	Data      json.RawMessage `json:"data"`
	At        int64           `json:"at"`
}

type Projection interface {
	Setup(context.Context, *sql.Tx) error
	Apply(context.Context, *sql.Tx, Event) error
	Forget(context.Context, *sql.Tx, string) error
}

type Options struct {
	Path        string
	Projections []Projection
	Now         func() time.Time
	Check       func(context.Context, *sql.Tx, NewEvent) error
}

const (
	recentEventLimit = 256
	recentByteLimit  = 2 * 1024 * 1024
)

type Store struct {
	db             *sql.DB
	writeMu        sync.Mutex
	watchMu        sync.Mutex
	watches        map[uint64]chan struct{}
	projectWatches map[string]map[uint64]chan struct{}
	nextWatch      uint64
	closed         bool
	recentMu       sync.RWMutex
	recentClosed   bool
	recentBaseSeq  int64
	recentMaxSeq   int64
	recentBytes    int
	recent         []Event
	projections    []Projection
	now            func() time.Time
	check          func(context.Context, *sql.Tx, NewEvent) error
}

const schema = `CREATE TABLE IF NOT EXISTS events (
 seq INTEGER PRIMARY KEY AUTOINCREMENT,
 id TEXT NOT NULL UNIQUE,
 type TEXT NOT NULL,
 project_id TEXT NOT NULL,
 data TEXT NOT NULL,
 at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS events_project ON events (project_id, seq);
CREATE INDEX IF NOT EXISTS events_trace_starts ON events (project_id, json_extract(data, '$.conversationId'), seq)
 WHERE type = 'turn.started';
CREATE INDEX IF NOT EXISTS events_trace_requests ON events (project_id, json_extract(data, '$.conversationId'), seq)
 WHERE type IN ('turn.step', 'conversation.compacted');
CREATE INDEX IF NOT EXISTS events_todo_conversation ON events (json_extract(data, '$.conversationId'), seq)
 WHERE type = 'todo.updated';
CREATE INDEX IF NOT EXISTS events_workspace_source ON events (json_extract(data, '$.threadId'), seq)
 WHERE type = 'workspace.opened' AND json_extract(data, '$.sourceId') IS NOT NULL;
CREATE INDEX IF NOT EXISTS events_secondary_workspace ON events (json_extract(data, '$.threadId'), json_extract(data, '$.sourceId'), seq)
 WHERE type IN ('workspace.source.opened', 'workspace.source.removed');`

func Open(ctx context.Context, options Options) (*Store, error) {
	if options.Path == "" {
		return nil, errors.New("event store path is required")
	}
	var uri url.URL
	uri.Scheme = "file"
	if options.Path == ":memory:" {
		uri.Opaque = "ariadne-" + uuid.NewString()
	} else {
		absolute, err := filepath.Abs(options.Path)
		if err != nil {
			return nil, err
		}
		uri.Path = filepath.ToSlash(absolute)
		// A Windows drive letter needs the leading slash in a file URI.
		if !strings.HasPrefix(uri.Path, "/") {
			uri.Path = "/" + uri.Path
		}
	}
	query := url.Values{}
	if options.Path == ":memory:" {
		query.Set("mode", "memory")
		query.Set("cache", "shared")
	}
	for _, pragma := range []string{"busy_timeout(5000)", "journal_mode(WAL)", "synchronous(NORMAL)", "foreign_keys(1)"} {
		query.Add("_pragma", pragma)
	}
	uri.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, err
	}
	// One writer, a small reader pool; PRAGMAs apply to every physical connection.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	store := &Store{db: db, watches: make(map[uint64]chan struct{}), projections: options.Projections, now: options.Now, check: options.Check}
	if store.now == nil {
		store.now = time.Now
	}
	if err = store.Within(ctx, func(tx *Transaction) error {
		if _, err := tx.SQL().ExecContext(ctx, schema); err != nil {
			return err
		}
		for _, projection := range store.projections {
			if err := projection.Setup(ctx, tx.SQL()); err != nil {
				return err
			}
		}
		var latest int64
		if err := tx.SQL().QueryRowContext(ctx, "SELECT coalesce(max(seq), 0) FROM events").Scan(&latest); err != nil {
			return err
		}
		store.recentBaseSeq = latest
		store.recentMaxSeq = latest
		return nil
	}); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open event store: %w", err)
	}
	return store, nil
}

// DB is for projection reads. Domain writes must use Within so the log and
// materialized state commit together.
func (store *Store) DB() *sql.DB { return store.db }

type txEventOp struct {
	purgeProject string
	event        Event
}

type Transaction struct {
	store       *Store
	tx          *sql.Tx
	ctx         context.Context
	count       int
	save        int
	ops         []txEventOp
	afterCommit []func()
}

func (tx *Transaction) SQL() *sql.Tx { return tx.tx }

// AfterCommit updates process-local state only after durable changes succeed.
// Callbacks run under the writer lock and must not reenter the store.
func (tx *Transaction) AfterCommit(callback func()) {
	tx.afterCommit = append(tx.afterCommit, callback)
}

func (store *Store) Within(ctx context.Context, work func(*Transaction) error) error {
	store.writeMu.Lock()
	defer store.writeMu.Unlock()
	store.watchMu.Lock()
	closed := store.closed
	store.watchMu.Unlock()
	if closed {
		return ErrClosed
	}
	dbtx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer dbtx.Rollback()
	tx := &Transaction{store: store, tx: dbtx, ctx: ctx}
	err = work(tx)
	if err == nil {
		err = dbtx.Commit()
	}
	if err != nil {
		if errors.Is(err, sql.ErrTxDone) && ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	var projects []string
	if len(tx.ops) > 0 {
		projects = store.recordCommitted(tx.ops)
	}
	for _, callback := range tx.afterCommit {
		callback()
	}
	if tx.count > 0 {
		store.notify(projects)
	}
	return nil
}

// Within nests with a savepoint. A failed inner operation leaves no partial
// events or projection writes even when its caller handles the error.
func (tx *Transaction) Within(work func(*Transaction) error) (err error) {
	tx.save++
	name := fmt.Sprintf("ariadne_%d", tx.save)
	if _, err := tx.tx.ExecContext(tx.ctx, "SAVEPOINT "+name); err != nil {
		return err
	}
	count := tx.count
	ops := len(tx.ops)
	callbacks := len(tx.afterCommit)
	defer func() {
		if cause := recover(); cause != nil {
			_, _ = tx.tx.ExecContext(tx.ctx, "ROLLBACK TO "+name)
			_, _ = tx.tx.ExecContext(tx.ctx, "RELEASE "+name)
			tx.count = count
			tx.ops = tx.ops[:ops]
			tx.afterCommit = tx.afterCommit[:callbacks]
			panic(cause)
		}
	}()
	if err = work(tx); err != nil {
		_, rollbackErr := tx.tx.ExecContext(tx.ctx, "ROLLBACK TO "+name)
		_, releaseErr := tx.tx.ExecContext(tx.ctx, "RELEASE "+name)
		tx.count = count
		tx.ops = tx.ops[:ops]
		tx.afterCommit = tx.afterCommit[:callbacks]
		return errors.Join(err, rollbackErr, releaseErr)
	}
	_, err = tx.tx.ExecContext(tx.ctx, "RELEASE "+name)
	return err
}

func (store *Store) Append(ctx context.Context, events []NewEvent) (stored []Event, err error) {
	err = store.Within(ctx, func(tx *Transaction) error {
		stored, err = tx.Append(events)
		return err
	})
	if err != nil {
		return nil, err
	}
	return stored, nil
}

func (tx *Transaction) Append(events []NewEvent) ([]Event, error) {
	stored := make([]Event, 0, len(events))
	err := tx.Within(func(tx *Transaction) error {
		for _, input := range events {
			if input.Type == "" || input.ProjectID == "" || !json.Valid(input.Data) {
				return errors.New("event type, projectId and valid JSON data are required")
			}
			if tx.store.check != nil {
				if err := tx.store.check(tx.ctx, tx.tx, input); err != nil {
					return err
				}
			}
			id, err := uuid.NewV7()
			if err != nil {
				return err
			}
			event := Event{ID: "evt_" + id.String(), Type: input.Type, ProjectID: input.ProjectID, Data: append(json.RawMessage(nil), input.Data...), At: tx.store.now().UnixMilli()}
			if err := tx.tx.QueryRowContext(tx.ctx,
				"INSERT INTO events (id, type, project_id, data, at) VALUES (?, ?, ?, ?, ?) RETURNING seq",
				event.ID, event.Type, event.ProjectID, string(event.Data), event.At).Scan(&event.Seq); err != nil {
				return err
			}
			for _, projection := range tx.store.projections {
				if err := projection.Apply(tx.ctx, tx.tx, event); err != nil {
					return err
				}
			}
			stored = append(stored, event)
			tx.ops = append(tx.ops, txEventOp{event: event})
			tx.count++
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return stored, nil
}

type ReadOptions struct {
	AfterSeq  int64
	ProjectID string
	Limit     int
	OmitData  bool
}

func (store *Store) Read(ctx context.Context, options ReadOptions) ([]Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limit := options.Limit
	if limit <= 0 {
		limit = 1000
	}
	if events, ok := store.readRecent(options, limit); ok {
		return events, nil
	}
	data := "data"
	if options.OmitData {
		data = "''"
	}
	query := "SELECT seq, id, type, project_id, " + data + ", at FROM events WHERE seq > ?"
	args := []any{options.AfterSeq}
	if options.ProjectID != "" {
		query += " AND project_id = ?"
		args = append(args, options.ProjectID)
	}
	query += " ORDER BY seq LIMIT ?"
	args = append(args, limit)
	rows, err := store.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]Event, 0)
	for rows.Next() {
		var event Event
		var data string
		if err := rows.Scan(&event.Seq, &event.ID, &event.Type, &event.ProjectID, &data, &event.At); err != nil {
			return nil, err
		}
		event.Data = json.RawMessage(data)
		events = append(events, event)
	}
	return events, rows.Err()
}

func (store *Store) LastSeq(ctx context.Context) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	store.recentMu.RLock()
	closed, seq := store.recentClosed, store.recentMaxSeq
	store.recentMu.RUnlock()
	if !closed {
		return seq, nil
	}
	var dbSeq int64
	err := store.db.QueryRowContext(ctx, "SELECT coalesce(max(seq), 0) FROM events").Scan(&dbSeq)
	return dbSeq, err
}

// Purge removes one project's log and projections atomically. The only new event
// is its tombstone, so reconnecting clients still learn that it was deleted.
func (store *Store) Purge(ctx context.Context, projectID string, tombstone NewEvent, also func(context.Context, *sql.Tx) error) (event Event, err error) {
	err = store.Within(ctx, func(tx *Transaction) error {
		event, err = tx.Purge(projectID, tombstone, also)
		return err
	})
	if err != nil {
		return Event{}, err
	}
	return event, nil
}

// Purge lets a domain operation check its preconditions and delete under the same
// writer lock. Its savepoint also makes a handled deletion failure atomic.
func (tx *Transaction) Purge(projectID string, tombstone NewEvent, also func(context.Context, *sql.Tx) error) (event Event, err error) {
	if projectID == "" || tombstone.ProjectID != projectID || tombstone.Type != "project.deleted" || !json.Valid(tombstone.Data) {
		return Event{}, errors.New("purge requires a project.deleted event for the same project")
	}
	ctx := tx.ctx
	err = tx.Within(func(tx *Transaction) error {
		for _, projection := range tx.store.projections {
			if err := projection.Forget(ctx, tx.SQL(), projectID); err != nil {
				return err
			}
		}
		if _, err := tx.SQL().ExecContext(ctx, "DELETE FROM events WHERE project_id = ?", projectID); err != nil {
			return err
		}
		if also != nil {
			if err := also(ctx, tx.SQL()); err != nil {
				return err
			}
		}
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		event = Event{ID: "evt_" + id.String(), Type: tombstone.Type, ProjectID: projectID, Data: append(json.RawMessage(nil), tombstone.Data...), At: tx.store.now().UnixMilli()}
		if err := tx.SQL().QueryRowContext(ctx,
			"INSERT INTO events (id, type, project_id, data, at) VALUES (?, ?, ?, ?, ?) RETURNING seq",
			event.ID, event.Type, event.ProjectID, string(event.Data), event.At).Scan(&event.Seq); err != nil {
			return err
		}
		tx.ops = append(tx.ops, txEventOp{purgeProject: projectID, event: event})
		tx.count++
		return nil
	})
	if err != nil {
		return Event{}, err
	}
	return event, nil
}

// Rebuild streams the log through the projections in a single transaction. A
// failed replay restores the prior projections and never rewrites log entries.
func (store *Store) Rebuild(ctx context.Context, reset func(context.Context, *sql.Tx) error) error {
	return store.Within(ctx, func(tx *Transaction) error {
		if err := reset(ctx, tx.SQL()); err != nil {
			return err
		}
		for _, projection := range store.projections {
			if err := projection.Setup(ctx, tx.SQL()); err != nil {
				return err
			}
		}
		rows, err := tx.SQL().QueryContext(ctx, "SELECT seq, id, type, project_id, data, at FROM events ORDER BY seq")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var event Event
			var data string
			if err := rows.Scan(&event.Seq, &event.ID, &event.Type, &event.ProjectID, &data, &event.At); err != nil {
				return err
			}
			event.Data = json.RawMessage(data)
			for _, projection := range store.projections {
				if err := projection.Apply(ctx, tx.SQL(), event); err != nil {
					return err
				}
			}
		}
		return rows.Err()
	})
}

// Backup uses SQLite's consistent snapshot operation rather than copying a live
// database file without its WAL. An existing destination is never overwritten.
func (store *Store) Backup(ctx context.Context, path string) (err error) {
	store.writeMu.Lock()
	defer store.writeMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	store.watchMu.Lock()
	closed := store.closed
	store.watchMu.Unlock()
	if closed {
		return ErrClosed
	}
	// SQLite accepts an existing empty destination, so reserve the name ourselves
	// to protect even zero-byte files and concurrent backup requests.
	reserved, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	info, statErr := reserved.Stat()
	closeErr := reserved.Close()
	defer func() {
		if err != nil && info != nil {
			if current, statErr := os.Lstat(path); statErr == nil && os.SameFile(info, current) {
				_ = os.Remove(path)
			}
		}
	}()
	if statErr != nil {
		return statErr
	}
	if closeErr != nil {
		return closeErr
	}
	_, err = store.db.ExecContext(ctx, "VACUUM INTO ?", path)
	return err
}

func (store *Store) recordCommitted(ops []txEventOp) []string {
	var projects []string
	addProject := func(projectID string) {
		if projectID == "" {
			return
		}
		for _, existing := range projects {
			if existing == projectID {
				return
			}
		}
		projects = append(projects, projectID)
	}
	store.recentMu.Lock()
	defer store.recentMu.Unlock()
	for _, op := range ops {
		if op.purgeProject != "" {
			addProject(op.purgeProject)
			kept := store.recent[:0]
			for _, existing := range store.recent {
				if existing.ProjectID == op.purgeProject {
					store.recentBytes -= len(existing.Data)
					continue
				}
				kept = append(kept, existing)
			}
			clear(store.recent[len(kept):])
			store.recent = kept
		}
		ev := op.event
		addProject(ev.ProjectID)
		if ev.Seq > store.recentMaxSeq {
			store.recentMaxSeq = ev.Seq
		}
		if len(ev.Data) > recentByteLimit {
			clear(store.recent)
			store.recent = store.recent[:0]
			store.recentBytes = 0
			store.recentBaseSeq = ev.Seq
			continue
		}
		store.recent = append(store.recent, ev)
		store.recentBytes += len(ev.Data)
		evict := 0
		for len(store.recent)-evict > recentEventLimit || store.recentBytes > recentByteLimit {
			store.recentBytes -= len(store.recent[evict].Data)
			store.recentBaseSeq = store.recent[evict].Seq
			evict++
		}
		if evict > 0 {
			copy(store.recent, store.recent[evict:])
			clear(store.recent[len(store.recent)-evict:])
			store.recent = store.recent[:len(store.recent)-evict]
		}
	}
	return projects
}

func (store *Store) readRecent(options ReadOptions, limit int) ([]Event, bool) {
	store.recentMu.RLock()
	defer store.recentMu.RUnlock()
	if store.recentClosed {
		return nil, false
	}
	if options.AfterSeq >= store.recentMaxSeq {
		return []Event{}, true
	}
	if options.AfterSeq < store.recentBaseSeq {
		return nil, false
	}
	start, _ := slices.BinarySearchFunc(store.recent, options.AfterSeq+1, func(ev Event, target int64) int {
		if ev.Seq < target {
			return -1
		}
		if ev.Seq > target {
			return 1
		}
		return 0
	})
	events := make([]Event, 0, min(limit, len(store.recent)-start))
	for i := start; i < len(store.recent) && len(events) < limit; i++ {
		ev := store.recent[i]
		if options.ProjectID != "" && ev.ProjectID != options.ProjectID {
			continue
		}
		if options.OmitData {
			ev.Data = json.RawMessage("")
		} else {
			ev.Data = bytes.Clone(ev.Data)
		}
		events = append(events, ev)
	}
	return events, true
}

// Watch wakes a subscriber when the durable log changes. It never queues events
// in memory; consumers read from their own cursor. Cancel releases its channel.
func (store *Store) Watch() (<-chan struct{}, func()) {
	return store.WatchProject("")
}

// WatchProject wakes a subscriber when the durable log changes for projectID,
// or for any project when projectID is empty. Cancel releases its channel.
func (store *Store) WatchProject(projectID string) (<-chan struct{}, func()) {
	store.watchMu.Lock()
	defer store.watchMu.Unlock()
	channel := make(chan struct{}, 1)
	if store.closed {
		close(channel)
		return channel, func() {}
	}
	store.nextWatch++
	id := store.nextWatch
	if projectID == "" {
		store.watches[id] = channel
	} else {
		if store.projectWatches == nil {
			store.projectWatches = make(map[string]map[uint64]chan struct{})
		}
		group := store.projectWatches[projectID]
		if group == nil {
			group = make(map[uint64]chan struct{})
			store.projectWatches[projectID] = group
		}
		group[id] = channel
	}
	var once sync.Once
	return channel, func() {
		once.Do(func() {
			store.watchMu.Lock()
			defer store.watchMu.Unlock()
			if projectID == "" {
				if channel, ok := store.watches[id]; ok {
					delete(store.watches, id)
					close(channel)
				}
				return
			}
			if group := store.projectWatches[projectID]; group != nil {
				if channel, ok := group[id]; ok {
					delete(group, id)
					close(channel)
				}
				if len(group) == 0 {
					delete(store.projectWatches, projectID)
				}
			}
		})
	}
}

func (store *Store) notify(projects []string) {
	store.watchMu.Lock()
	defer store.watchMu.Unlock()
	for _, channel := range store.watches {
		select {
		case channel <- struct{}{}:
		default:
		}
	}
	for _, projectID := range projects {
		for _, channel := range store.projectWatches[projectID] {
			select {
			case channel <- struct{}{}:
			default:
			}
		}
	}
}

func (store *Store) Close() error {
	store.writeMu.Lock()
	defer store.writeMu.Unlock()
	store.watchMu.Lock()
	if store.closed {
		store.watchMu.Unlock()
		return nil
	}
	store.closed = true
	for id, channel := range store.watches {
		delete(store.watches, id)
		close(channel)
	}
	for projectID, group := range store.projectWatches {
		for id, channel := range group {
			delete(group, id)
			close(channel)
		}
		delete(store.projectWatches, projectID)
	}
	store.watchMu.Unlock()
	store.recentMu.Lock()
	store.recentClosed = true
	clear(store.recent)
	store.recent = nil
	store.recentBytes = 0
	store.recentMu.Unlock()
	return store.db.Close()
}
