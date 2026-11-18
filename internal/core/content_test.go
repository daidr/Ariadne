package core

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/daidr/Ariadne/internal/eventstore"
)

func TestContentProjectionsMatchLegacy(t *testing.T) {
	const seed = `INSERT INTO projects (id,name,coordinator_id,created_at,updated_at) VALUES ('prj_a','项目','crd_a',0,0);
	INSERT INTO artifacts (id,project_id,kind,title,version,content,preview,created_by,updated_by,created_at,updated_at) VALUES ('art_a','prj_a','document','文档',1,'{}','{}','user','user',0,0);
	INSERT INTO plan_nodes (id,project_id,kind,title,description,acceptance,position,created_at,updated_at) VALUES ('node_a','prj_a','task','节点标题','','',0,0,0);`
	type event struct {
		kind string
		data object
	}
	cases := []event{
		{"attention.opened", object{"attentionId": "att_a", "threadId": "thr_a", "messageId": "msg_b", "block": object{"type": "confirm", "title": "执行命令？"}, "purpose": "cmd_a"}},
		{"attention.joined", object{"attentionId": "att_a", "threadId": "thr_b"}},
		{"attention.joined", object{"attentionId": "att_a", "threadId": "thr_b"}},
		{"attention.answered", object{"attentionId": "att_a", "answer": object{"type": "confirm", "approved": true}}},
		{"approval.used", object{"attentionId": "att_a", "threadId": "thr_a"}},
		{"approval.used", object{"attentionId": "att_a", "threadId": "thr_a"}},
		{"attention.opened", object{"attentionId": "att_b", "threadId": nil, "messageId": nil, "block": object{"type": "question", "title": "选哪个？"}}},
		{"attention.answered", object{"attentionId": "att_b", "answer": object{"type": "question", "text": "第一个"}}},
		{"attention.opened", object{"attentionId": "att_c", "threadId": "thr_a", "messageId": nil, "block": object{"type": "confirm", "title": "继续？"}}},
		{"attention.withdrawn", object{"attentionId": "att_c", "reason": "已完成"}},
		{"message.posted", object{"messageId": "msg_a", "conversationId": "crd_a", "author": "user", "kind": "chat", "content": "用户原文", "attachments": []any{object{"fileId": "fil_a", "name": "文档.txt", "mime": "text/plain", "size": 8}}, "mentions": []any{object{"kind": "thread", "id": "thr_a", "title": "任务"}}, "comments": []any{}}},
		{"message.posted", object{"messageId": "msg_b", "conversationId": "crd_a", "author": "coordinator", "kind": "chat", "content": "回复", "blocks": []any{object{"type": "text", "text": "内容"}}, "replyTo": object{"messageId": "msg_a", "author": "user", "text": "用户原文"}}},
		{"message.edited", object{"messageId": "msg_b", "content": "未读时编辑", "reason": "更新"}},
		{"message.read", object{"conversationId": "crd_a", "upTo": "msg_b"}},
		{"message.edited", object{"messageId": "msg_b", "content": "已读后编辑", "reason": "补充"}},
		{"message.edited", object{"messageId": "msg_b", "content": "再次编辑", "reason": "补充"}},
		{"message.read", object{"conversationId": "crd_a", "upTo": "msg_b"}},
		{"message.posted", object{"messageId": "msg_c", "conversationId": "thr_a", "author": "coordinator", "kind": "relay", "content": "转交", "cites": []any{object{"conversationId": "crd_a", "messageId": "msg_a", "content": "用户原文", "at": 1}, object{"conversationId": "thr_a", "messageId": "msg_c", "content": "已在此处", "at": 1}}}},
		{"message.reacted", object{"messageId": "msg_b", "emoji": "👍", "by": "user", "removed": false}},
		{"message.reacted", object{"messageId": "msg_b", "emoji": "👍", "by": "thread", "removed": false}},
		{"message.reacted", object{"messageId": "msg_b", "emoji": "👍", "by": "user", "removed": false}},
		{"message.reacted", object{"messageId": "msg_b", "emoji": "👍", "by": "user", "removed": true}},
		{"message.reacted", object{"messageId": "msg_b", "emoji": "👍", "by": "thread", "removed": true}},
		{"message.reacted", object{"messageId": "missing", "emoji": "👍", "by": "user", "removed": true}},
		{"todo.updated", object{"conversationId": "thr_a", "items": []any{object{"label": "读取文件", "status": "doing"}}}},
		{"todo.updated", object{"conversationId": "thr_a", "items": []any{object{"label": "读取文件", "status": "done"}}}},
		{"memory.saved", object{"memoryId": "mem_a", "author": "user", "text": "用户偏好"}},
		{"memory.saved", object{"memoryId": "mem_a", "author": "thr_a", "text": "更改偏好"}},
		{"memory.removed", object{"memoryId": "mem_a", "author": "thr_a"}},
		{"memory.saved", object{"memoryId": "mem_a", "author": "user", "text": "恢复偏好", "undoes": "ntc_29"}},
		{"memory.removed", object{"memoryId": "mem_a", "author": "user"}},
		{"memory.everywhere", object{"conversationId": "thr_a", "action": "saved", "text": "通用偏好"}},
		{"conversation.compaction_failed", object{"conversationId": "thr_a", "detail": "压缩失败"}},
		{"project.settings.set", object{"routine": object{"by": "crd_a", "action": "added", "prompt": "定时检查"}}},
		{"plan.issue_linked", object{"nodeId": "node_a", "by": "thr_a", "issue": object{"tracker": "owner/repo", "number": 8}}},
		{"plan.issue_unlinked", object{"nodeId": "missing", "by": "thr_a", "tracker": "owner/repo", "number": 8}},
		{"plan.issue_linked", object{"nodeId": "node_a", "by": "user", "issue": object{"tracker": "owner/repo", "number": 8}}},
		{"issue.written", object{"threadId": "thr_a", "tracker": "owner/repo", "number": 8, "action": "comment", "text": "说明", "url": "https://example.com/8"}},
		{"branch.merged", object{"threadId": "thr_a", "base": "main", "method": "squash", "sha": "sha_a"}},
		{"branch.merge_undone", object{"threadId": "thr_a", "sha": "sha_a"}},
		{"branch.merged", object{"threadId": "thr_a", "base": "main", "method": "merge", "sha": "sha_b", "auto": true}},
		{"pull_request.closed", object{"threadId": "thr_a", "number": 9, "auto": object{"base": "main", "method": "squash"}}},
		{"pull_request.closed", object{"threadId": "thr_a", "number": 9}},
		{"branch.conflicted", object{"threadId": "thr_a", "base": "main", "files": []any{"a.go"}, "pullRequest": 9}},
		{"plan.revised", object{"author": "coordinator", "revisionId": "rev_a", "reason": "分解工作", "ops": []any{object{"op": "add"}, object{"op": "update"}}, "revertOf": nil}},
		{"thread.created", object{"threadId": "thr_auto", "title": "自动任务", "auto": true}},
		{"autopilot.held", object{"threadId": "thr_a", "reason": "checks", "detail": "检查失败"}},
		{"workspace.removed", object{"threadId": "thr_a", "reason": "idle"}},
		{"review.requested", object{"threadId": "thr_a", "reviewerId": "thr_b", "round": 1}},
		{"review.submitted", object{"threadId": "thr_a", "reviewerId": "thr_b", "round": 1, "verdict": "changes", "comments": []any{object{"body": "建议"}}}},
		{"review.abandoned", object{"threadId": "thr_a", "reviewerId": "thr_b", "round": 1}},
		{"turn.changed", object{"conversationId": "thr_a", "turnId": "turn_a", "from": "head_a", "to": "head_b", "files": []any{}, "total": object{"files": 0, "added": 0, "removed": 0}, "startedAt": 1800000000000}},
		{"conversation.model_fallback", object{"conversationId": "thr_a", "from": "model_a", "to": "model_b", "detail": "繁忙"}},
		{"conversation.wait_ended", object{"conversationId": "thr_a", "how": "timeout", "note": "继续"}},
		{"conversation.wait_ended", object{"conversationId": "thr_a", "how": "cancelled", "note": "取消"}},
		{"routine.ran", object{"routineId": "routine_a", "prompt": "检查", "dueAt": 1800000000000}},
		{"conversation.model_retry", object{"conversationId": "thr_a", "attempt": 2, "delayMs": 1000, "detail": "重试"}},
		{"artifact.revised", object{"artifactId": "art_a", "title": "文档", "version": 2, "notify": "thr_a"}},
		{"artifact.commented", object{"artifactId": "art_a", "by": "user", "notify": "thr_a", "action": object{"type": "create", "body": "修改一下"}}},
		{"artifact.commented", object{"artifactId": "art_a", "by": "thr_a", "notify": nil, "action": object{"type": "reply", "body": "已修改"}}},
		{"artifact.commented", object{"artifactId": "art_a", "by": "user", "notify": "thr_a", "action": object{"type": "resolve"}}},
		{"artifact.feedback", object{"artifactId": "art_a", "notify": "thr_a", "round": 1, "items": []any{object{"text": "建议"}}}},
		{"artifact.feedback_done", object{"artifactId": "art_a", "round": 1, "summary": "已完成"}},
		{"artifact.feedback_done", object{"artifactId": "art_a", "round": 1, "summary": ""}},
		{"artifact.feedback_done", object{"artifactId": "missing", "round": 1, "summary": ""}},
		{"user.returned", object{"since": 1790000000000, "summary": "离开期间"}},
		{"user.returned", object{"since": 1799999999999, "summary": "刚刚"}},
		{"notice.dismissed", object{"noticeId": "ntc_67"}},
	}
	events := make([]eventstore.Event, 0, len(cases))
	for i, c := range cases {
		events = append(events, eventstore.Event{Seq: int64(i + 1), ID: "evt_test", ProjectID: "prj_a", Type: c.kind, Data: json.RawMessage(encoded(c.data)), At: 1800000000000 + int64(i)})
	}
	var expected []map[string]any
	oracle(t, "packages/core/src/testing/go-content-fixture.ts", object{"seed": seed, "events": events}, &expected)
	store, err := eventstore.Open(t.Context(), eventstore.Options{Path: filepath.Join(t.TempDir(), "content.sqlite"), Projections: []eventstore.Projection{LifecycleProjection{}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.DB().ExecContext(t.Context(), seed); err != nil {
		t.Fatal(err)
	}
	for i, e := range events {
		actual := object{}
		err := store.Within(t.Context(), func(tx *eventstore.Transaction) error {
			if err := (ContentProjection{}).Apply(t.Context(), tx.SQL(), e); err != nil {
				return err
			}
			for _, table := range []string{"attentions", "attention_waiters", "used_approvals", "messages", "todos", "notices", "memories"} {
				rows, err := records(t.Context(), tx.SQL(), "SELECT * FROM "+table+" ORDER BY rowid")
				if err != nil {
					return err
				}
				actual[table] = rows
			}
			return nil
		})
		if err != nil {
			t.Fatalf("event %d %s: %v", i+1, e.Type, err)
		}
		if !reflect.DeepEqual(normalize(actual), normalize(expected[i])) {
			t.Fatalf("event %d %s differs\ngot %s\nwant%s", i+1, e.Type, encoded(normalize(actual)), encoded(normalize(expected[i])))
		}
	}
}
