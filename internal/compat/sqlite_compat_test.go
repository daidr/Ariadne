package compat

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/eventstore"
	"github.com/daidr/Ariadne/internal/schema"
	_ "modernc.org/sqlite"
)

func initLegacySchemaDB(t *testing.T, path string, userVersion int) *sql.DB {
	t.Helper()
	ddl, err := os.ReadFile(filepath.Join("testdata", "schema.sql"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(t.Context(), string(ddl)); err != nil {
		t.Fatalf("load legacy schema.sql: %v", err)
	}
	if _, err := db.ExecContext(t.Context(), "PRAGMA user_version = "+jsonNumber(userVersion)); err != nil {
		t.Fatal(err)
	}
	return db
}

func jsonNumber(v int) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestLegacyV67StaticSQLiteUpgradesAndPreservesData(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "旧数据-v67.sqlite")
	db := initLegacySchemaDB(t, path, 67)

	largeOutput := strings.Repeat("历史工具输出\n", 4096)
	toolEventData, _ := json.Marshal(map[string]any{
		"conversationId": "thr_legacy",
		"toolCallId":     "call_legacy",
		"name":           "run_command",
		"content":        largeOutput,
		"isError":        false,
	})
	toolTranscriptPayload, _ := json.Marshal(map[string]any{
		"toolCallId": "call_legacy",
		"name":       "run_command",
		"content":    largeOutput,
		"isError":    false,
	})
	fileBytes := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a}
	yjsBytes := []byte{0x01, 0x02, 0x03, 0x04}

	for _, stmt := range []struct {
		query string
		args  []any
	}{
		{
			`INSERT INTO events (seq, id, type, project_id, data, at) VALUES
			 (1, 'evt_1', 'project.created', 'prj_legacy', '{"name":"旧版项目","coordinatorId":"crd_legacy"}', 1800000000000),
			 (2, 'evt_2', 'thread.created', 'prj_legacy', '{"threadId":"thr_legacy","title":"旧版讨论串"}', 1800000000001),
			 (3, 'evt_3', 'workspace.opened', 'prj_legacy', '{"threadId":"thr_legacy","sourceId":"src_primary","label":"repo","branch":"work/legacy","base":"main"}', 1800000000002)`,
			nil,
		},
		{
			`INSERT INTO events (seq, id, type, project_id, data, at) VALUES (4, 'evt_4', 'tool.finished', 'prj_legacy', ?, 1800000000003)`,
			[]any{string(toolEventData)},
		},
		{
			`INSERT INTO projects (id, name, coordinator_id, created_at, updated_at) VALUES ('prj_legacy', '旧版项目', 'crd_legacy', 1800000000000, 1800000000000)`,
			nil,
		},
		{
			`INSERT INTO conversations (id, project_id, role) VALUES ('crd_legacy', 'prj_legacy', 'coordinator'), ('thr_legacy', 'prj_legacy', 'thread')`,
			nil,
		},
		{
			`INSERT INTO threads (id, project_id, title, workspace_label, workspace_branch, workspace_base, created_at, updated_at)
			 VALUES ('thr_legacy', 'prj_legacy', '旧版讨论串', 'repo', 'work/legacy', 'main', 1800000000001, 1800000000002)`,
			nil,
		},
		{
			`INSERT INTO transcript (project_id, conversation_id, seq, kind, payload) VALUES ('prj_legacy', 'thr_legacy', 4, 'tool', ?)`,
			[]any{string(toolTranscriptPayload)},
		},
		{
			`INSERT INTO files (id, project_id, mime, size, sha256, data, created_at) VALUES ('fil_legacy', 'prj_legacy', 'image/png', ?, 'hash_legacy', ?, 1800000000000)`,
			[]any{len(fileBytes), fileBytes},
		},
		{
			`INSERT INTO artifact_shared (artifact_id, project_id, version, state, updated_at) VALUES ('art_legacy', 'prj_legacy', 1, ?, 1800000000000)`,
			[]any{yjsBytes},
		},
		{
			`CREATE TABLE plugin_fixture (value TEXT); INSERT INTO plugin_fixture VALUES ('keep')`,
			nil,
		},
	} {
		if _, err := db.ExecContext(ctx, stmt.query, stmt.args...); err != nil {
			t.Fatalf("seed v67 db (%s): %v", stmt.query, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	repo, err := core.Open(ctx, core.Options{
		Path: path,
		Now:  func() time.Time { return time.UnixMilli(1800000000010) },
	})
	if err != nil {
		t.Fatalf("open v67 db: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	var version int
	if err := repo.Store.DB().QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil || version != schema.Version {
		t.Fatalf("user_version = %d, want %d (err=%v)", version, schema.Version, err)
	}

	thread, err := repo.Reader.GetThread(ctx, "thr_legacy")
	if err != nil {
		t.Fatal(err)
	}
	if thread.Workspace == nil || thread.Workspace.SourceID != "src_primary" || thread.Workspace.Label != "repo" {
		t.Fatalf("upgraded workspace = %+v", thread.Workspace)
	}

	var changedSeq int64
	if err := repo.Store.DB().QueryRowContext(ctx, "SELECT changed_seq FROM threads WHERE id = 'thr_legacy'").Scan(&changedSeq); err != nil || changedSeq != 4 {
		t.Fatalf("changed_seq = %d, want 4 (err=%v)", changedSeq, err)
	}
	var hasGroupCol int
	if err := repo.Store.DB().QueryRowContext(ctx, "SELECT 1 FROM pragma_table_info('plan_nodes') WHERE name = 'group_name'").Scan(&hasGroupCol); err != nil || hasGroupCol != 1 {
		t.Fatalf("plan_nodes.group_name missing after v67 upgrade: has=%d err=%v", hasGroupCol, err)
	}
	var hasEffortCol int
	if err := repo.Store.DB().QueryRowContext(ctx, "SELECT 1 FROM pragma_table_info('threads') WHERE name = 'effort'").Scan(&hasEffortCol); err != nil || hasEffortCol != 1 {
		t.Fatalf("threads.effort missing after v67 upgrade: has=%d err=%v", hasEffortCol, err)
	}

	var payloadLen int
	if err := repo.Store.DB().QueryRowContext(ctx, "SELECT length(payload) FROM transcript WHERE seq = 4").Scan(&payloadLen); err != nil || payloadLen > 300 {
		t.Fatalf("large inline tool output not compacted on v67 upgrade: length=%d err=%v", payloadLen, err)
	}
	entries, err := repo.ListTranscript(ctx, "thr_legacy", core.TranscriptOptions{})
	if err != nil || len(entries) != 1 || entries[0]["content"] != largeOutput {
		t.Fatalf("compacted tool output not restored from event log: len=%d err=%v", len(entries), err)
	}

	var storedFile, storedYjs []byte
	if err := repo.Store.DB().QueryRowContext(ctx, "SELECT data FROM files WHERE id = 'fil_legacy'").Scan(&storedFile); err != nil || !bytes.Equal(storedFile, fileBytes) {
		t.Fatalf("file bytes changed: %x %v", storedFile, err)
	}
	if err := repo.Store.DB().QueryRowContext(ctx, "SELECT state FROM artifact_shared WHERE artifact_id = 'art_legacy'").Scan(&storedYjs); err != nil || !bytes.Equal(storedYjs, yjsBytes) {
		t.Fatalf("Yjs state bytes changed: %x %v", storedYjs, err)
	}
	var pluginValue string
	if err := repo.Store.DB().QueryRowContext(ctx, "SELECT value FROM plugin_fixture").Scan(&pluginValue); err != nil || pluginValue != "keep" {
		t.Fatalf("plugin table lost: %q %v", pluginValue, err)
	}

	// Appending new events and taking a backup work cleanly on the upgraded database.
	if _, err := repo.Store.Append(ctx, []eventstore.NewEvent{{
		Type:      "project.renamed",
		ProjectID: "prj_legacy",
		Data:      json.RawMessage(`{"name":"升级后的项目"}`),
	}}); err != nil {
		t.Fatal(err)
	}
	project, err := repo.Reader.GetProject(ctx, "prj_legacy")
	if err != nil || project.Name != "升级后的项目" {
		t.Fatalf("renamed project = %+v, err=%v", project, err)
	}

	backupPath := filepath.Join(t.TempDir(), "upgraded-backup.sqlite")
	if err := repo.Store.Backup(ctx, backupPath); err != nil {
		t.Fatal(err)
	}
	backupRepo, err := core.Open(ctx, core.Options{Path: backupPath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backupRepo.Close() })
	backupProject, err := backupRepo.Reader.GetProject(ctx, "prj_legacy")
	if err != nil || backupProject.Name != "升级后的项目" {
		t.Fatalf("backup project = %+v, err=%v", backupProject, err)
	}
}

func TestLegacyPreV67StaticSQLiteReplaysAndRepairsStaleProjections(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "旧数据-v66.sqlite")
	db := initLegacySchemaDB(t, path, 66)

	fileBytes := []byte("preserved-file")
	yjsBytes := []byte{0x0a, 0x0b, 0x0c}
	for _, stmt := range []struct {
		query string
		args  []any
	}{
		{
			`INSERT INTO events (seq, id, type, project_id, data, at) VALUES
			 (1, 'evt_1', 'project.created', 'prj_old', '{"name":"正确名称","coordinatorId":"crd_old"}', 1800000000000),
			 (2, 'evt_2', 'thread.created', 'prj_old', '{"threadId":"thr_old","title":"历史讨论串"}', 1800000000001)`,
			nil,
		},
		{
			`INSERT INTO projects (id, name, coordinator_id, created_at, updated_at) VALUES ('prj_old', 'stale', 'crd_old', 1800000000000, 1800000000000)`,
			nil,
		},
		{
			`INSERT INTO files (id, project_id, mime, size, sha256, data, created_at) VALUES ('fil_old', 'prj_old', 'text/plain', ?, 'hash_old', ?, 1800000000000)`,
			[]any{len(fileBytes), fileBytes},
		},
		{
			`INSERT INTO artifact_shared (artifact_id, project_id, version, state, updated_at) VALUES ('art_old', 'prj_old', 1, ?, 1800000000000)`,
			[]any{yjsBytes},
		},
		{
			`CREATE TABLE plugin_fixture (value TEXT); INSERT INTO plugin_fixture VALUES ('keep')`,
			nil,
		},
	} {
		if _, err := db.ExecContext(ctx, stmt.query, stmt.args...); err != nil {
			t.Fatalf("seed v66 db: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	repo, err := core.Open(ctx, core.Options{Path: path})
	if err != nil {
		t.Fatalf("open v66 db: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	var version int
	if err := repo.Store.DB().QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil || version != schema.Version {
		t.Fatalf("user_version = %d, want %d (err=%v)", version, schema.Version, err)
	}
	project, err := repo.Reader.GetProject(ctx, "prj_old")
	if err != nil || project.Name != "正确名称" {
		t.Fatalf("replayed project = %+v, err=%v", project, err)
	}
	thread, err := repo.Reader.GetThread(ctx, "thr_old")
	if err != nil || thread.Title != "历史讨论串" {
		t.Fatalf("replayed thread = %+v, err=%v", thread, err)
	}
	var storedFile, storedYjs []byte
	if err := repo.Store.DB().QueryRowContext(ctx, "SELECT data FROM files WHERE id = 'fil_old'").Scan(&storedFile); err != nil || !bytes.Equal(storedFile, fileBytes) {
		t.Fatalf("file bytes lost on replay: %x %v", storedFile, err)
	}
	if err := repo.Store.DB().QueryRowContext(ctx, "SELECT state FROM artifact_shared WHERE artifact_id = 'art_old'").Scan(&storedYjs); err != nil || !bytes.Equal(storedYjs, yjsBytes) {
		t.Fatalf("Yjs state lost on replay: %x %v", storedYjs, err)
	}
	var pluginValue string
	if err := repo.Store.DB().QueryRowContext(ctx, "SELECT value FROM plugin_fixture").Scan(&pluginValue); err != nil || pluginValue != "keep" {
		t.Fatalf("plugin table lost on replay: %q %v", pluginValue, err)
	}
}
