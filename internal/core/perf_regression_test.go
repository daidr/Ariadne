package core

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/eventstore"
	"github.com/daidr/Ariadne/internal/schema"
)

func TestQueryPlansAvoidEventsScanAndUseProjectIndexes(t *testing.T) {
	repo, err := Open(t.Context(), Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	explain := func(query string, args ...any) []string {
		t.Helper()
		rows, err := repo.Store.DB().QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+query, args...)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var details []string
		for rows.Next() {
			var id, parent, notused int
			var detail string
			if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
				t.Fatal(err)
			}
			details = append(details, detail)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return details
	}

	threadPlan := strings.Join(explain(threadSelect+" WHERE t.project_id = ? ORDER BY t.id", "prj_a"), "\n")
	if strings.Contains(threadPlan, "SCAN events") {
		t.Fatalf("threadSelect unexpectedly scans events table:\n%s", threadPlan)
	}

	convPlan := strings.Join(explain("SELECT sum(input_tokens), count(turn_id) FROM conversations WHERE project_id = ?", "prj_a"), "\n")
	if !strings.Contains(convPlan, "conversations_project") {
		t.Fatalf("conversations query did not use conversations_project index:\n%s", convPlan)
	}

	changedPlan := strings.Join(explain("SELECT id FROM threads WHERE project_id = ? AND changed_seq > ?", "prj_a", 10), "\n")
	if !strings.Contains(changedPlan, "threads_project_changed") {
		t.Fatalf("incremental threads query did not use threads_project_changed index:\n%s", changedPlan)
	}

	backfillPlan := strings.Join(explain(`UPDATE threads SET workspace_source_id = (
		 SELECT json_extract(e.data, '$.sourceId') FROM events e
		 WHERE e.project_id = threads.project_id
		  AND e.type = 'workspace.opened'
		  AND json_extract(e.data, '$.sourceId') IS NOT NULL
		  AND json_extract(e.data, '$.threadId') = threads.id
		 ORDER BY e.seq DESC LIMIT 1
		) WHERE workspace_source_id IS NULL AND workspace_label IS NOT NULL`), "\n")
	if !strings.Contains(backfillPlan, "events_project") || strings.Contains(backfillPlan, "SCAN events") {
		t.Fatalf("v68 backfill query did not use events_project index:\n%s", backfillPlan)
	}
}

func TestSchemaV68MigrationBackfillsWorkspaceSourceIDAndChangedSeq(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v68.sqlite")
	repo, err := Open(t.Context(), Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Store.Append(t.Context(), []eventstore.NewEvent{
		{Type: "project.created", ProjectID: "prj_a", Data: json.RawMessage(`{"name":"迁移测试","coordinatorId":"crd_a"}`)},
		{Type: "thread.created", ProjectID: "prj_a", Data: json.RawMessage(`{"threadId":"thr_a","title":"任务 A"}`)},
		{Type: "thread.created", ProjectID: "prj_a", Data: json.RawMessage(`{"threadId":"thr_no_ws","title":"无工作区任务"}`)},
		{Type: "workspace.opened", ProjectID: "prj_a", Data: json.RawMessage(`{"threadId":"thr_a","sourceId":"src_primary","label":"repo","branch":"work/a","base":"main"}`)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}

	// Simulate an existing v68 database lacking workspace_source_id, changed_seq, and the v69 indexes.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		"DROP INDEX IF EXISTS conversations_project",
		"DROP INDEX IF EXISTS threads_project_changed",
		"ALTER TABLE threads DROP COLUMN workspace_source_id",
		"ALTER TABLE threads DROP COLUMN changed_seq",
		"PRAGMA user_version = 68",
	} {
		if _, err := db.ExecContext(t.Context(), stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	migrated, err := Open(t.Context(), Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()

	var version int
	if err := migrated.Store.DB().QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version); err != nil || version != schema.Version {
		t.Fatalf("user_version = %d, want %d (err=%v)", version, schema.Version, err)
	}
	thread, err := migrated.Reader.GetThread(t.Context(), "thr_a")
	if err != nil {
		t.Fatal(err)
	}
	if thread.Workspace == nil || thread.Workspace.SourceID != "src_primary" {
		t.Fatalf("workspace after migration = %+v", thread.Workspace)
	}
	var changedSeq int64
	if err := migrated.Store.DB().QueryRowContext(t.Context(), "SELECT changed_seq FROM threads WHERE id = 'thr_a'").Scan(&changedSeq); err != nil || changedSeq != 4 {
		t.Fatalf("changed_seq after migration = %d, want 4 (err=%v)", changedSeq, err)
	}
	noWS, err := migrated.Reader.GetThread(t.Context(), "thr_no_ws")
	if err != nil {
		t.Fatal(err)
	}
	if noWS.Workspace != nil {
		t.Fatalf("expected nil workspace for thr_no_ws, got %+v", noWS.Workspace)
	}
}

func TestListProjectsBatchAggregationAndOpenAttentions(t *testing.T) {
	now := int64(1800000000000)
	repo, err := Open(t.Context(), Options{Path: ":memory:", Now: func() time.Time { return time.UnixMilli(now) }})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	if _, err := repo.Store.Append(t.Context(), []eventstore.NewEvent{
		{Type: "project.created", ProjectID: "prj_a", Data: json.RawMessage(`{"name":"项目 A","coordinatorId":"crd_a"}`)},
		{Type: "project.created", ProjectID: "prj_b", Data: json.RawMessage(`{"name":"项目 B","coordinatorId":"crd_b"}`)},
		{Type: "project.created", ProjectID: "prj_c", Data: json.RawMessage(`{"name":"空项目","coordinatorId":"crd_c"}`)},
		{Type: "thread.created", ProjectID: "prj_a", Data: json.RawMessage(`{"threadId":"thr_a1","title":"提问者"}`)},
		{Type: "thread.created", ProjectID: "prj_a", Data: json.RawMessage(`{"threadId":"thr_a2","title":"等待者 1"}`)},
		{Type: "thread.created", ProjectID: "prj_a", Data: json.RawMessage(`{"threadId":"thr_a3","title":"等待者 2"}`)},
		{Type: "thread.created", ProjectID: "prj_a", Data: json.RawMessage(`{"threadId":"thr_a4","title":"待验收"}`)},
		{Type: "thread.started", ProjectID: "prj_a", Data: json.RawMessage(`{"threadId":"thr_a4"}`)},
		{Type: "thread.reported", ProjectID: "prj_a", Data: json.RawMessage(`{"threadId":"thr_a4","report":"ready","headline":"完成","summary":"已检查","artifacts":[]}`)},
		{Type: "attention.opened", ProjectID: "prj_a", Data: json.RawMessage(`{"attentionId":"att_1","threadId":"thr_a1","block":{"type":"confirm","title":"确认 1"}}`)},
		{Type: "attention.joined", ProjectID: "prj_a", Data: json.RawMessage(`{"attentionId":"att_1","threadId":"thr_a1"}`)},
		{Type: "attention.joined", ProjectID: "prj_a", Data: json.RawMessage(`{"attentionId":"att_1","threadId":"thr_a2"}`)},
		{Type: "attention.joined", ProjectID: "prj_a", Data: json.RawMessage(`{"attentionId":"att_1","threadId":"thr_a3"}`)},
		{Type: "attention.opened", ProjectID: "prj_a", Data: json.RawMessage(`{"attentionId":"att_2","threadId":null,"block":{"type":"confirm","title":"确认 2"}}`)},
		{Type: "turn.started", ProjectID: "prj_a", Data: json.RawMessage(`{"conversationId":"thr_a1","turnId":"turn_1","model":"gpt-5"}`)},
		{Type: "turn.step", ProjectID: "prj_a", Data: json.RawMessage(`{"conversationId":"thr_a1","sawSeq":1,"usage":{"input":100,"output":25,"cost":{"currency":"USD","amount":0.12}}}`)},
		{Type: "thread.created", ProjectID: "prj_b", Data: json.RawMessage(`{"threadId":"thr_b1","title":"失败任务"}`)},
		{Type: "turn.started", ProjectID: "prj_b", Data: json.RawMessage(`{"conversationId":"thr_b1","turnId":"turn_b1","model":"gpt-5"}`)},
		{Type: "turn.step", ProjectID: "prj_b", Data: json.RawMessage(`{"conversationId":"thr_b1","sawSeq":1,"usage":{"input":40,"output":10,"cost":{"currency":"CNY","amount":0.5}}}`)},
		{Type: "turn.finished", ProjectID: "prj_b", Data: json.RawMessage(`{"conversationId":"thr_b1","outcome":"failed","error":{"message":"boom"}}`)},
	}); err != nil {
		t.Fatal(err)
	}

	projects, err := repo.Reader.ListProjects(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]ProjectView{}
	for _, p := range projects {
		byID[p.ID] = p
	}
	if byID["prj_a"].NeedsMe != 3 || byID["prj_a"].RunningTurns != 1 || byID["prj_a"].Usage["input"] != int64(100) {
		t.Fatalf("prj_a summary mismatch: %+v", byID["prj_a"])
	}
	if byID["prj_b"].NeedsMe != 1 || byID["prj_b"].RunningTurns != 0 || byID["prj_b"].Usage["output"] != int64(10) {
		t.Fatalf("prj_b summary mismatch: %+v", byID["prj_b"])
	}
	if byID["prj_c"].NeedsMe != 0 || byID["prj_c"].RunningTurns != 0 || byID["prj_c"].Usage["input"] != int64(0) {
		t.Fatalf("prj_c summary mismatch: %+v", byID["prj_c"])
	}

	filtered, err := repo.Reader.ListProjectsByID(t.Context(), []string{"prj_a", "prj_c"})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 2 {
		t.Fatalf("filtered projects len = %d, want 2", len(filtered))
	}

	detail, err := repo.Reader.GetProjectDetail(t.Context(), "prj_a")
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.NeedsMe) != 3 {
		t.Fatalf("prj_a detail needsMe len = %d, want 3", len(detail.NeedsMe))
	}
	firstAtt := detail.NeedsMe[0]["attention"].(AttentionView)
	if firstAtt.ID != "att_1" || len(firstAtt.JoinedBy) != 2 || firstAtt.JoinedBy[0] != "thr_a2" || firstAtt.JoinedBy[1] != "thr_a3" {
		t.Fatalf("att_1 waiters mismatch: %+v", firstAtt)
	}
	if detail.NeedsMe[0]["threadId"] != "thr_a1" {
		t.Fatalf("att_1 threadId = %v, want thr_a1", detail.NeedsMe[0]["threadId"])
	}
	joinedIDs := detail.NeedsMe[0]["joinedIds"].([]string)
	if len(joinedIDs) != 2 || joinedIDs[0] != "thr_a2" || joinedIDs[1] != "thr_a3" {
		t.Fatalf("att_1 joinedIds = %v", joinedIDs)
	}
}

func TestGetProjectDetailSinceTracksChangedSeqAndSameHourArchive(t *testing.T) {
	const hourMs = int64(time.Hour / time.Millisecond)
	const dayMs = int64(24 * time.Hour / time.Millisecond)
	now := (1800000000000 / hourMs) * hourMs // Exact start of an hour.
	repo, err := Open(t.Context(), Options{Path: ":memory:", Now: func() time.Time { return time.UnixMilli(now) }})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	appendOne := func(eventType string, data string) {
		t.Helper()
		if _, err := repo.Store.Append(t.Context(), []eventstore.NewEvent{{Type: eventType, ProjectID: "prj_a", Data: json.RawMessage(data)}}); err != nil {
			t.Fatal(err)
		}
	}

	appendOne("project.created", `{"name":"增量测试","coordinatorId":"crd_a"}`)
	appendOne("thread.created", `{"threadId":"thr_a","title":"任务 A"}`)
	appendOne("thread.created", `{"threadId":"thr_b","title":"任务 B"}`)
	appendOne("artifact.created", `{"artifactId":"art_1","kind":"document","title":"初版文档","content":"hello","author":"thr_a"}`)
	appendOne("thread.reported", `{"threadId":"thr_a","report":"ready","headline":"交付","summary":"说明","artifacts":["art_1"]}`)
	appendOne("branch.merged", `{"threadId":"thr_b","head":"abc","into":"main"}`)
	appendOne("thread.reported", `{"threadId":"thr_b","report":"done","headline":"已合入","summary":"完成","artifacts":[]}`)

	base, err := repo.Reader.GetProjectDetailSince(t.Context(), "prj_a", 0, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if base.Partial || len(base.Threads) != 2 {
		t.Fatalf("initial delta = %+v", base)
	}

	// Updating todo on thr_b should return only thr_b.
	appendOne("todo.updated", `{"conversationId":"thr_b","items":[{"label":"第一步","status":"doing"}]}`)
	delta, err := repo.Reader.GetProjectDetailSince(t.Context(), "prj_a", base.Seq, base.At, true)
	if err != nil {
		t.Fatal(err)
	}
	if !delta.Partial || len(delta.Threads) != 1 || delta.Threads[0].ID != "thr_b" || delta.Threads[0].Doing != "第一步" {
		t.Fatalf("todo delta mismatch: %+v", delta.Threads)
	}

	// Revising art_1 (referenced in thr_a.last_artifacts) should touch thr_a.
	appendOne("artifact.revised", `{"artifactId":"art_1","title":"新版文档","version":2,"content":"world","author":"user","reason":"update","quotes":{}}`)
	delta2, err := repo.Reader.GetProjectDetailSince(t.Context(), "prj_a", delta.Seq, delta.At, true)
	if err != nil {
		t.Fatal(err)
	}
	if !delta2.Partial || len(delta2.Threads) != 1 || delta2.Threads[0].ID != "thr_a" {
		t.Fatalf("artifact revise delta mismatch: %+v", delta2.Threads)
	}

	// Advance clock to just before 24h after landing (same hour as 24h boundary), then cross 24h within the same hour.
	if _, err := repo.Store.DB().ExecContext(t.Context(), "UPDATE threads SET landed_at = ?, reported_at = ? WHERE id = 'thr_b'", now+1000, now+1000); err != nil {
		t.Fatal(err)
	}
	now = now + dayMs + 500 // 500ms before 24h archive boundary, inside the same hour.
	beforeArchive, err := repo.Reader.GetProjectDetailSince(t.Context(), "prj_a", 0, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	now += 1000 // Crosses landed_at + 24h within the same hour.
	afterArchive, err := repo.Reader.GetProjectDetailSince(t.Context(), "prj_a", beforeArchive.Seq, beforeArchive.At, true)
	if err != nil {
		t.Fatal(err)
	}
	if !afterArchive.Partial || len(afterArchive.Threads) != 1 || afterArchive.Threads[0].ID != "thr_b" || !afterArchive.Threads[0].Archived {
		t.Fatalf("same-hour 24h archive delta mismatch: %+v", afterArchive.Threads)
	}
}
