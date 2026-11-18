package core

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"github.com/daidr/Ariadne/internal/legacytest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/daidr/Ariadne/internal/eventstore"
	"github.com/daidr/Ariadne/internal/projectsettings"
)

func lifecycleEvents() []eventstore.Event {
	type input struct {
		kind string
		data object
	}
	step := func(usage any, sawSeq int, finish, text string) object {
		return object{"conversationId": "thr_a", "sawSeq": sawSeq, "usage": usage, "finish": finish, "text": text}
	}
	used := object{"input": 400, "output": 20, "cached": 200, "cacheWrite": 10, "reasoning": 12, "contextInput": 450, "cost": object{"currency": "USD", "amount": 0.012}}
	cases := []input{
		{"project.created", object{"name": "示例", "coordinatorId": "crd_a", "unnamed": true}},
		{"project.renamed", object{"name": "项目"}},
		{"project.slots.set", object{"slots": 2}},
		{"project.slots.set", object{"slots": nil}},
		{"project.mcp.set", object{"servers": []string{"mcp_b", "mcp_a"}}},
		{"project.references.set", object{"references": []any{object{"id": "ref_a", "where": "docs", "source": object{"kind": "local", "settings": object{"path": "D:/项目"}}}}}},
		{"project.source.set", object{"source": object{"kind": "local", "settings": object{"path": "D:/src"}}}},
		{"project.setup.noted", object{"threadId": "thr_a", "command": "go mod download"}},
		{"project.check.noted", object{"threadId": "thr_a", "command": "go test ./..."}},
		{"project.source.set", object{"source": nil}},
		{"project.autopilot.paused", object{"paused": true}},
		{"project.settings.set", object{"settings": object{"autopilot": object{"enabled": true}}}},
		{"project.autopilot.paused", object{"paused": true}},
		{"project.settings.set", object{"settings": object{"autopilot": object{"enabled": true}}}},
		{"project.autopilot.paused", object{"paused": false}},
		{"project.settings.set", object{"settings": projectsettings.Defaults()}},
		{"thread.created", object{"threadId": "thr_a", "title": "测试讨论串", "nodeId": nil, "model": nil, "branch": "tests", "auto": true}},
		{"thread.created", object{"threadId": "thr_review", "title": "审查", "nodeId": nil, "model": "model_x", "branch": nil, "reviewOf": "thr_a"}},
		{"thread.started", object{"threadId": "thr_a"}},
		{"thread.renamed", object{"threadId": "thr_a", "title": "更新的标题"}},
		{"thread.model_set", object{"threadId": "thr_a", "model": "model_b"}},
		{"thread.references.set", object{"threadId": "thr_a", "references": []any{}, "ended": object{"attentionId": "att_a", "name": "readme", "now": nil}}},
		{"thread.references.set", object{"threadId": "thr_a", "references": []any{}, "ended": object{"attentionId": "att_b", "name": "lib", "now": "main"}}},
		{"workspace.opened", object{"threadId": "thr_a", "label": "workspace_a", "branch": "work/test", "base": "main"}},
		{"workspace.removed", object{"threadId": "thr_a", "reason": "idle"}},
		{"turn.changed", object{"conversationId": "thr_a", "branch": object{"added": 20, "removed": 4, "files": 2}}},
		{"thread.reported", object{"threadId": "thr_a", "report": "ready", "summary": "待审查", "headline": "检查完成", "artifacts": []string{"art_a"}, "head": "abc", "reviewing": true}},
		{"review.requested", object{"threadId": "thr_a", "round": 1}},
		{"review.submitted", object{"threadId": "thr_a", "verdict": "changes"}},
		{"review.requested", object{"threadId": "thr_a", "round": 2}},
		{"review.submitted", object{"threadId": "thr_a", "verdict": "pass"}},
		{"review.abandoned", object{"threadId": "thr_a"}},
		{"branch.merged", object{"threadId": "thr_a", "head": "abc", "method": "squash", "commit": "def"}},
		{"branch.merge_undone", object{"threadId": "thr_a", "head": "abc"}},
		{"checks.failed", object{"threadId": "thr_a"}},
		{"checks.failed", object{"threadId": "thr_a", "attempt": 3}},
		{"pull_request.synced", object{"threadId": "thr_a", "number": 1, "state": "open", "checks": object{"state": "failure"}}},
		{"pull_request.synced", object{"threadId": "thr_a", "number": 1, "state": "merged", "checks": object{"state": "success"}}},
		{"pull_request.closed", object{"threadId": "thr_a", "merged": true}},
		{"autopilot.held", object{"threadId": "thr_a", "head": nil}},
		{"thread.set_aside", object{"threadId": "thr_a", "until": 1800000000100, "reason": "稍后继续", "by": "user"}},
		{"thread.aside_due", object{"threadId": "thr_a"}},
		{"message.posted", object{"conversationId": "thr_a", "author": "user"}},
		{"thread.aside_due", object{"threadId": "thr_a"}},
		{"thread.aside_ended", object{"threadId": "thr_a"}},
		{"thread.stopped", object{"threadId": "thr_a", "reason": "暂停", "by": "user"}},
		{"thread.nudged", object{"threadId": "thr_a"}},
		{"thread.reviewed", object{"threadId": "thr_a"}},
		{"thread.resumed", object{"threadId": "thr_a"}},
		{"thread.reported", object{"threadId": "thr_a", "report": "ready", "summary": "直接交付", "headline": "完成", "artifacts": []any{}, "head": "ghi", "reviewing": false}},
		{"thread.reported", object{"threadId": "thr_a", "report": "done", "summary": "完成", "headline": "无需改动", "artifacts": []any{}}},
		{"thread.reported", object{"threadId": "thr_a", "report": "failed", "summary": "失败", "headline": "失败", "artifacts": []any{}}},
		{"conversation.instructions", object{"conversationId": "thr_a", "path": "AGENTS.md", "hash": "hash_a", "content": "说明"}},
		{"turn.requested", object{"conversationId": "thr_a"}},
		{"turn.started", object{"conversationId": "thr_a", "turnId": "turn_a", "model": "model_a"}},
		{"turn.requested", object{"conversationId": "thr_a"}},
		{"turn.step", step(used, 54, "tool_calls", "  正在读取代码\n第二行  ")},
		{"conversation.model_fallback", object{"conversationId": "thr_a", "turnId": "wrong_turn", "to": "wrong"}},
		{"conversation.model_fallback", object{"conversationId": "thr_a", "turnId": "turn_a", "to": "model_b"}},
		{"conversation.wait_set", object{"conversationId": "thr_a", "waitId": "wait_a", "until": 1800000000200, "note": "等待消息"}},
		{"conversation.wait_ended", object{"conversationId": "thr_a", "waitId": "wrong_wait"}},
		{"conversation.wait_ended", object{"conversationId": "thr_a", "waitId": "wait_a"}},
		{"turn.step", step(nil, 56, "stop", "完成")},
		{"turn.finished", object{"conversationId": "thr_a", "outcome": "failed", "error": object{"key": "error.test"}}},
		{"turn.requested", object{"conversationId": "thr_a"}},
		{"turn.started", object{"conversationId": "thr_a", "turnId": "turn_b", "model": "model_b"}},
		{"turn.finished", object{"conversationId": "thr_a", "outcome": "cancelled", "error": nil}},
		{"turn.requested", object{"conversationId": "thr_a"}},
		{"turn.started", object{"conversationId": "thr_a", "turnId": "turn_c", "model": "model_c"}},
		{"turn.finished", object{"conversationId": "thr_a", "outcome": "interrupted", "error": nil}},
		{"turn.started", object{"conversationId": "thr_a", "turnId": "turn_d", "model": "model_d"}},
		{"turn.step", step(object{"input": 2, "output": 1}, 70, "stop", "")},
		{"turn.finished", object{"conversationId": "thr_a", "outcome": "completed", "error": nil}},
		{"research.step", object{"conversationId": "thr_a", "usage": used}},
		{"search.answered", object{"conversationId": "thr_a", "usage": object{"input": 5, "output": 3, "cost": object{"currency": "CNY", "amount": 0.004}}}},
		{"conversation.compacted", object{"conversationId": "thr_a", "usage": nil}},
	}
	events := make([]eventstore.Event, 0, len(cases))
	for i, c := range cases {
		events = append(events, eventstore.Event{Seq: int64(i + 1), ID: "evt_test", ProjectID: "prj_a", Type: c.kind, Data: json.RawMessage(encoded(c.data)), At: 1800000000000 + int64(i)})
	}
	return events
}

func oracle(t *testing.T, path string, input any, result any) {
	t.Helper()
	bun, err := exec.LookPath("bun")
	if err != nil {
		if os.Getenv("ARIADNE_REQUIRE_LEGACY") == "1" {
			t.Fatal(err)
		}
		t.Skip("Bun is needed only for legacy comparison tests")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	cmd := legacytest.Command(t, bun, path)
	cmd.Dir = root
	cmd.Stdin = bytes.NewBufferString(encoded(input))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("legacy comparison: %v\n%s", err, output)
	}
	if err := json.Unmarshal(output, result); err != nil {
		t.Fatalf("decode legacy: %v\n%s", err, output)
	}
}

func jsonValue(value any) any {
	encoded := encoded(value)
	var result any
	_ = json.Unmarshal([]byte(encoded), &result)
	return result
}

func normalize(value any) any {
	result := jsonValue(value)
	var visit func(any) any
	visit = func(v any) any {
		switch node := v.(type) {
		case map[string]any:
			for key, child := range node {
				// Persisted JSON strings may use different object key order.
				if s, ok := child.(string); ok && json.Valid([]byte(s)) && len(s) > 0 && (s[0] == '{' || s[0] == '[') {
					var decoded any
					_ = json.Unmarshal([]byte(s), &decoded)
					child = decoded
				}
				node[key] = visit(child)
			}
		case []any:
			for i := range node {
				node[i] = visit(node[i])
			}
		}
		return v
	}
	return visit(result)
}

func TestLifecycleMatchesLegacyAfterEveryEvent(t *testing.T) {
	events := lifecycleEvents()
	var expected []map[string]any
	oracle(t, "packages/core/src/testing/go-lifecycle-fixture.ts", events, &expected)
	store, err := eventstore.Open(t.Context(), eventstore.Options{Path: filepath.Join(t.TempDir(), "lifecycle.sqlite"), Projections: []eventstore.Projection{LifecycleProjection{}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for i, event := range events {
		actual := map[string]any{}
		err := store.Within(t.Context(), func(tx *eventstore.Transaction) error {
			if err := (LifecycleProjection{}).Apply(t.Context(), tx.SQL(), event); err != nil {
				return err
			}
			for _, table := range []string{"projects", "threads", "conversations", "transcript"} {
				rows, err := records(t.Context(), tx.SQL(), "SELECT * FROM "+table+" ORDER BY rowid")
				if err != nil {
					return err
				}
				actual[table] = rows
			}
			return nil
		})
		if err != nil {
			t.Fatalf("event %d %s: %v", i+1, event.Type, err)
		}
		if !reflect.DeepEqual(normalize(actual), normalize(expected[i])) {
			t.Fatalf("event %d %s differs\ngot  %s\nwant %s", i+1, event.Type, encoded(normalize(actual)), encoded(normalize(expected[i])))
		}
	}
}

func TestPurgeReleasesAllProjectData(t *testing.T) {
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
