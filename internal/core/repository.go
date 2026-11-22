package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/eventstore"
	"github.com/daidr/Ariadne/internal/schema"
)

var projectionTables = []string{"projects", "threads", "attentions", "attention_waiters", "messages", "todos", "memories", "notices", "conversations", "transcript", "plan_nodes", "plan_edges", "plan_revisions", "plan_proposals", "plan_node_issues", "issue_writes", "used_approvals", "artifacts", "artifact_versions", "artifact_comments", "artifact_feedback", "reviews", "prompts", "search_docs", "search_text"}

// DomainProjection preserves the dependency order of the original projections.
type DomainProjection struct{}

func (DomainProjection) Forget(ctx context.Context, tx *sql.Tx, projectID string) error {
	return (LifecycleProjection{}).Forget(ctx, tx, projectID)
}
func (projection DomainProjection) Setup(ctx context.Context, tx *sql.Tx) error {
	var version int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > schema.Version {
		return fmt.Errorf("database projection version %d is newer than supported version %d", version, schema.Version)
	}
	if version == 67 {
		// Tool output remains in the event log. The transcript only needs its
		// lookup fields and any associated files.
		if _, err := tx.ExecContext(ctx, `UPDATE transcript SET payload = json_remove(payload, '$.content')
		 WHERE kind = 'tool' AND json_type(payload, '$.content') = 'text'
		 AND length(CAST(json_extract(payload, '$.content') AS BLOB)) >= ?
		 AND EXISTS (
		  SELECT 1 FROM events e WHERE e.seq = transcript.seq AND e.project_id = transcript.project_id
		  AND e.type = 'tool.finished' AND json_extract(e.data, '$.conversationId') = transcript.conversation_id
		  AND json_extract(e.data, '$.content') = json_extract(transcript.payload, '$.content')
		 )`, inlineToolOutputLimit); err != nil {
			return err
		}
	}
	if version == 67 || version == 68 {
		if err := addColumnIfMissing(ctx, tx, "threads", "workspace_source_id", "TEXT"); err != nil {
			return err
		}
		if err := addColumnIfMissing(ctx, tx, "threads", "changed_seq", "INTEGER NOT NULL DEFAULT 0"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE threads SET workspace_source_id = (
		 SELECT json_extract(e.data, '$.sourceId') FROM events e
		 WHERE e.project_id = threads.project_id
		  AND e.type = 'workspace.opened'
		  AND json_extract(e.data, '$.sourceId') IS NOT NULL
		  AND json_extract(e.data, '$.threadId') = threads.id
		 ORDER BY e.seq DESC LIMIT 1
		) WHERE workspace_source_id IS NULL AND workspace_label IS NOT NULL`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE threads SET changed_seq = coalesce((SELECT max(seq) FROM events WHERE project_id = threads.project_id), 0)
		 WHERE changed_seq = 0`); err != nil {
			return err
		}
	}
	if version == 67 || version == 68 || version == 69 {
		if err := addColumnIfMissing(ctx, tx, "plan_nodes", "group_name", "TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
	}
	if version == 67 || version == 68 || version == 69 || version == 70 {
		if err := addColumnIfMissing(ctx, tx, "threads", "effort", "TEXT"); err != nil {
			return err
		}
	}
	if version == schema.Version || version == 67 || version == 68 || version == 69 || version == 70 {
		return schema.Create(ctx, tx)
	}
	if err := dropProjectionTables(ctx, tx); err != nil {
		return err
	}
	if err := schema.Create(ctx, tx); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, "SELECT seq, id, type, project_id, data, at FROM events ORDER BY seq")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var event eventstore.Event
		var data string
		if err := rows.Scan(&event.Seq, &event.ID, &event.Type, &event.ProjectID, &data, &event.At); err != nil {
			return err
		}
		event.Data = json.RawMessage(data)
		if err := projection.Apply(ctx, tx, event); err != nil {
			return fmt.Errorf("replay event %d (%s): %w", event.Seq, event.Type, err)
		}
	}
	return rows.Err()
}
func (DomainProjection) Apply(ctx context.Context, tx *sql.Tx, e eventstore.Event) error {
	var d object
	if err := json.Unmarshal(e.Data, &d); err != nil {
		return err
	}
	w := &projectionWriter{ctx: ctx, tx: tx}
	if e.Type == "prompt.recorded" {
		w.run("INSERT OR IGNORE INTO prompts (project_id, hash, content) VALUES (?, ?, ?)", e.ProjectID, d["hash"], d["content"])
	}
	if w.err != nil {
		return w.err
	}
	if err := projectEvent(w, e, d); err != nil {
		return err
	}
	threadEvent(w, e, d)
	attentionEvent(w, e, d)
	messageEvent(w, e, d)
	todoEvent(w, e, d)
	noticeEvent(w, e, d)
	memoryEvent(w, e, d)
	conversationEvent(w, e, d)
	planEvent(w, e, d)
	artifactEvent(w, e, d)
	reviewEvent(w, e, d)
	searchEvent(w, e, d)
	transcriptEvent(w, e, d)
	return w.err
}
func addColumnIfMissing(ctx context.Context, tx *sql.Tx, table, column, definition string) error {
	var exists int
	err := tx.QueryRowContext(ctx, "SELECT 1 FROM pragma_table_info(?) WHERE name = ?", table, column).Scan(&exists)
	if err == nil {
		return nil
	}
	if err != sql.ErrNoRows {
		return err
	}
	_, err = tx.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, definition))
	return err
}

func dropProjectionTables(ctx context.Context, tx *sql.Tx) error {
	for _, table := range projectionTables {
		if _, err := tx.ExecContext(ctx, "DROP TABLE IF EXISTS "+table); err != nil {
			return err
		}
	}
	return nil
}

type Options struct {
	Path string
	Now  func() time.Time
}

// Repository owns persistence and read models. The HTTP server and agent runtime
// are separate services; opening a repository never starts network or agent work.
type Repository struct {
	Store *eventstore.Store
	Reader
	// Active external writes belong to this process; persisted unfinished writes
	// from an earlier process are reported as uncertain. Access uses the store's
	// writer lock, and entries are released on completion, deletion, and close.
	issueWrites map[string]activeIssueWrite
	closeOnce   sync.Once
	closeErr    error
}

func Open(ctx context.Context, options Options) (*Repository, error) {
	store, err := eventstore.Open(ctx, eventstore.Options{Path: options.Path, Now: options.Now, Projections: []eventstore.Projection{DomainProjection{}}, Check: func(ctx context.Context, tx *sql.Tx, e eventstore.NewEvent) error {
		if e.Type == "project.created" {
			return nil
		}
		var known int
		err := tx.QueryRowContext(ctx, "SELECT 1 FROM projects WHERE id = ?", e.ProjectID).Scan(&known)
		if err == sql.ErrNoRows {
			return domainerr.NotFound(e.ProjectID)
		}
		return err
	}})
	if err != nil {
		return nil, err
	}
	return &Repository{Store: store, Reader: Reader{DB: store.DB(), Now: options.Now}, issueWrites: make(map[string]activeIssueWrite)}, nil
}
func (repo *Repository) Close() error {
	repo.closeOnce.Do(func() { repo.closeErr = repo.Store.Close(); clear(repo.issueWrites) })
	return repo.closeErr
}
func (repo *Repository) Rebuild(ctx context.Context) error {
	return repo.Store.Rebuild(ctx, dropProjectionTables)
}
func (repo *Repository) DeleteProject(ctx context.Context, projectID string) error {
	return repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		var known int
		if err := tx.SQL().QueryRowContext(ctx, "SELECT 1 FROM projects WHERE id = ?", projectID).Scan(&known); err == sql.ErrNoRows {
			return domainerr.NotFound(projectID)
		} else if err != nil {
			return err
		}
		var running int
		if err := tx.SQL().QueryRowContext(ctx, "SELECT count(*) FROM conversations WHERE project_id = ? AND turn_id IS NOT NULL", projectID).Scan(&running); err != nil {
			return err
		}
		if running > 0 {
			return domainerr.New("error.projectBusy", nil)
		}
		_, err := tx.Purge(projectID, eventstore.NewEvent{Type: "project.deleted", ProjectID: projectID, Data: json.RawMessage(`{}`)}, nil)
		if err == nil {
			tx.AfterCommit(func() {
				for id, write := range repo.issueWrites {
					if write.ProjectID == projectID {
						delete(repo.issueWrites, id)
					}
				}
			})
		}
		return err
	})
}
