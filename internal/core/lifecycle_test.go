package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/daidr/Ariadne/internal/eventstore"
)

func TestPurgeReleasesAllProjectData(t *testing.T) {
	t.Parallel()
	store, err := eventstore.Open(t.Context(), eventstore.Options{Path: filepath.Join(t.TempDir(), "delete.sqlite"), Projections: []eventstore.Projection{LifecycleProjection{}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := t.Context()
	for _, id := range []string{"prj_a", "prj_b"} {
		if _, err := store.Append(ctx, []eventstore.NewEvent{{Type: "project.created", ProjectID: id, Data: json.RawMessage(encoded(object{"name": id, "coordinatorId": "crd_" + id}))}}); err != nil {
			t.Fatal(err)
		}
	}
	err = store.Within(ctx, func(tx *eventstore.Transaction) error {
		for _, query := range []string{
			`INSERT INTO attentions (id,project_id,block,created_at) VALUES ('att_a','prj_a','{}',0),('att_b','prj_b','{}',0)`,
			`INSERT INTO attention_waiters VALUES ('att_a','thr_a'),('att_b','thr_b')`,
			`INSERT INTO used_approvals VALUES ('att_a','thr_a'),('att_b','thr_b')`,
			`INSERT INTO artifacts (id,project_id,kind,title,version,content,preview,created_by,updated_by,created_at,updated_at) VALUES ('art_a','prj_a','document','a',1,'{}','{}','user','user',0,0),('art_b','prj_b','document','b',1,'{}','{}','user','user',0,0)`,
			`INSERT INTO artifact_comments (id,artifact_id,quote,comments,created_at) VALUES ('c_a','art_a','','[]',0),('c_b','art_b','','[]',0)`,
			`INSERT INTO artifact_versions VALUES ('art_a',1,'a','{}','user','',0),('art_b',1,'b','{}','user','',0)`,
			`INSERT INTO artifact_feedback (artifact_id,round,version,notify,items,images,created_at) VALUES ('art_a',1,1,'[]','[]','[]',0),('art_b',1,1,'[]','[]','[]',0)`,
			`INSERT INTO artifact_shared VALUES ('art_a','prj_a',1,x'0001',0),('art_b','prj_b',1,x'0102',0)`,
			`INSERT INTO files VALUES ('fil_a','prj_a','text/plain',2,'hash',x'0001',0),('fil_b','prj_b','text/plain',2,'hash',x'0102',0)`,
			`INSERT INTO search_docs VALUES (1,'prj_a','thread','thr_a',NULL,0),(2,'prj_b','thread','thr_b',NULL,0)`,
			`INSERT INTO search_text (rowid,title,body) VALUES (1,'project a','project a'),(2,'project b','project b')`,
			`CREATE TABLE plugin_data (project_id TEXT); INSERT INTO plugin_data VALUES ('prj_a')`,
		} {
			if _, err := tx.SQL().ExecContext(ctx, query); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// A domain precondition and purge can now share the same writer transaction.
	err = store.Within(ctx, func(tx *eventstore.Transaction) error {
		_, err := tx.Purge("prj_a", eventstore.NewEvent{Type: "project.deleted", ProjectID: "prj_a", Data: json.RawMessage(`{}`)}, nil)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"projects", "conversations", "attentions", "attention_waiters", "used_approvals", "artifacts", "artifact_comments", "artifact_versions", "artifact_feedback", "artifact_shared", "files", "search_docs", "search_text", "plugin_data"} {
		var count int
		if err := store.DB().QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("%s retained %d rows", table, count)
		}
	}
	var project string
	if err := store.DB().QueryRowContext(ctx, "SELECT project_id FROM search_docs").Scan(&project); err != nil || project != "prj_b" {
		t.Fatal(project, err)
	}
	events, err := store.Read(ctx, eventstore.ReadOptions{ProjectID: "prj_a"})
	if err != nil || len(events) != 1 || events[0].Type != "project.deleted" {
		t.Fatalf("deleted log: %v %v", events, err)
	}
}

// Check the schema refusal does not mutate an incompatible database.
func TestUnsupportedProjectionVersionIsPreserved(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "old.sqlite")
	store, err := eventstore.Open(t.Context(), eventstore.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.DB().ExecContext(t.Context(), "PRAGMA user_version = 1")
	if err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	_, err = eventstore.Open(t.Context(), eventstore.Options{Path: path, Projections: []eventstore.Projection{LifecycleProjection{}}})
	if err == nil {
		t.Fatal("old projection version opened without replay")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err := db.QueryRowContext(context.Background(), "PRAGMA user_version").Scan(&version); err != nil || version != 1 {
		t.Fatal(version, err)
	}
}
