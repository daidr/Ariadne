package core

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/daidr/Ariadne/internal/eventstore"
)

func TestTranscriptAndWakeCursorsMatchLegacy(t *testing.T) {
	const seed = `INSERT INTO projects (id,name,coordinator_id,created_at,updated_at) VALUES ('prj_a','项目','crd_a',0,0);
	INSERT INTO conversations (id,project_id,role) VALUES ('crd_a','prj_a','coordinator'),('thr_a','prj_a','thread'),('thr_b','prj_a','thread'),('thr_review','prj_a','thread');
	INSERT INTO threads (id,project_id,title,node_id,review_of,last_summary,last_headline,last_artifacts,created_at,updated_at) VALUES ('thr_a','prj_a','任务','node_a',NULL,'交付摘要','交付标题','["art_a"]',0,0),('thr_b','prj_a','依赖任务','node_b',NULL,NULL,NULL,'[]',0,0),('thr_review','prj_a','审查任务',NULL,'thr_a',NULL,NULL,'[]',0,0);
	INSERT INTO plan_nodes (id,project_id,kind,title,description,acceptance,position,created_at,updated_at) VALUES ('node_a','prj_a','task','上游工作','','',0,0,0),('node_b','prj_a','task','下游工作','','',1,0,0);
	INSERT INTO plan_edges VALUES ('prj_a','node_a','node_b',NULL);
	INSERT INTO reviews (id,project_id,thread_id,reviewer_id,round,requested_at,comments,replies) VALUES (1,'prj_a','thr_a','thr_review',1,0,'[{"body":"修改"}]','[{"comment":1,"outcome":"fixed","note":"已修复"}]');
	INSERT INTO artifacts (id,project_id,kind,title,version,content,preview,created_by,updated_by,created_at,updated_at) VALUES ('art_a','prj_a','document','文档',1,'{}','{}','user','user',0,0);
	INSERT INTO artifact_comments (id,artifact_id,quote,comments,created_at) VALUES ('comment_a','art_a','原文','[]',0);
	INSERT INTO plan_proposals (id,project_id,attention_id,reason,ops,status,created_at) VALUES ('proposal_a','prj_a','att_proposal','修改计划','[]','open',0);
	INSERT INTO attentions (id,project_id,thread_id,conversation_id,message_id,block,created_at) VALUES ('att_a','prj_a','thr_a','thr_a','msg_ask','{"type":"choice","question":"选哪项？","options":[{"label":"第一项"}]}',0),('att_b','prj_a',NULL,'crd_a','msg_ask2','{"type":"confirm","title":"确认？"}',0),('att_c','prj_a','thr_b','thr_b',NULL,'{"type":"form","title":"填写","fields":[{"key":"name","label":"名称"},{"key":"count","label":"数量"}]}',0);
	INSERT INTO attention_waiters VALUES ('att_a','thr_a'),('att_a','thr_b');
	INSERT INTO issue_writes VALUES ('att_a','prj_a','purpose','thr_a','owner/repo',8,'comment','written','https://example.com/8',1);`
	type event struct {
		kind string
		data object
	}
	cases := []event{
		{"project.source.set", object{"source": object{"kind": "local"}}},
		{"message.posted", object{"conversationId": "crd_a", "messageId": "msg_a", "author": "user", "kind": "chat", "content": "开始"}},
		{"project.source.set", object{"source": object{"kind": "local"}}},
		{"project.source.set", object{"source": nil}},
		{"message.posted", object{"conversationId": "thr_a", "messageId": "msg_b", "author": "user", "kind": "chat", "content": "原文"}},
		{"message.posted", object{"conversationId": "thr_a", "messageId": "msg_c", "author": "user", "kind": "chat", "content": "行评论", "comments": []any{object{"body": "建议"}}}},
		{"message.posted", object{"conversationId": "thr_a", "messageId": "msg_d", "author": "coordinator", "kind": "relay", "content": "转交"}},
		{"message.posted", object{"conversationId": "thr_a", "messageId": "msg_e", "author": "thread", "kind": "chat", "content": "回复"}},
		{"thread.reported", object{"threadId": "thr_a", "report": "progress", "summary": "进行中"}},
		{"thread.reported", object{"threadId": "thr_a", "report": "ready", "summary": "提交审查", "reviewing": true}},
		{"thread.reported", object{"threadId": "thr_review", "report": "done", "summary": "审查完成"}},
		{"thread.reported", object{"threadId": "thr_a", "report": "done", "summary": "已完成"}},
		{"review.requested", object{"threadId": "thr_a", "reviewerId": "thr_review", "round": 1, "head": "head_a"}},
		{"review.requested", object{"threadId": "thr_a", "reviewerId": "thr_review", "round": 2, "head": "head_b"}},
		{"review.submitted", object{"threadId": "thr_a", "reviewerId": "thr_review", "round": 1, "verdict": "changes", "summary": "请修改", "comments": []any{object{"body": "建议"}}}},
		{"review.submitted", object{"threadId": "thr_a", "reviewerId": "thr_review", "round": 2, "verdict": "changes", "summary": "请修改", "comments": []any{}}},
		{"review.submitted", object{"threadId": "thr_a", "reviewerId": "thr_review", "round": 2, "verdict": "pass", "summary": "通过", "comments": []any{}}},
		{"review.abandoned", object{"threadId": "thr_a", "reviewerId": "thr_review", "round": 2}},
		{"checks.failed", object{"threadId": "thr_a", "detail": "失败"}},
		{"checks.failed", object{"threadId": "thr_a", "detail": "失败", "attempt": 3}},
		{"checks.failed", object{"threadId": "thr_a", "detail": "失败", "attempt": 4}},
		{"conversation.instructions", object{"conversationId": "thr_a", "path": "AGENTS.md", "content": "规则", "changed": false}},
		{"conversation.instructions", object{"conversationId": "thr_a", "path": "AGENTS.md", "content": "新规则", "changed": true}},
		{"conversation.instructions", object{"conversationId": "thr_a", "path": "AGENTS.md", "content": nil, "changed": true}},
		{"thread.stopped", object{"threadId": "thr_a", "reason": "停止", "by": "user"}},
		{"thread.stopped", object{"threadId": "thr_a", "reason": "停止", "by": "crd_a"}},
		{"thread.created", object{"threadId": "thr_auto", "nodeId": "node_a", "title": "自动任务", "auto": true}},
		{"thread.created", object{"threadId": "thr_manual", "nodeId": nil, "title": "手工任务"}},
		{"thread.nudged", object{"threadId": "thr_a"}},
		{"thread.reviewed", object{"threadId": "thr_a"}},
		{"thread.resumed", object{"threadId": "thr_a", "reason": "继续", "by": "user"}},
		{"thread.resumed", object{"threadId": "thr_a", "reason": "继续", "by": "crd_a"}},
		{"pull_request.closed", object{"threadId": "thr_a", "number": 8, "merged": true}},
		{"branch.merged", object{"threadId": "thr_a", "head": "head_a", "sha": "sha_a", "method": "squash", "base": "main"}},
		{"branch.merge_undone", object{"threadId": "thr_a", "head": "head_a", "sha": "sha_a", "base": "main"}},
		{"branch.conflicted", object{"threadId": "thr_a", "base": "main", "files": []any{"a.go"}}},
		{"message.reacted", object{"conversationId": "thr_a", "messageId": "msg_b", "emoji": "👍", "by": "user", "removed": false}},
		{"message.reacted", object{"conversationId": "thr_a", "messageId": "msg_b", "emoji": "👍", "by": "user", "quote": "原文", "removed": true}},
		{"message.reacted", object{"conversationId": "thr_a", "messageId": "msg_b", "emoji": "👍", "by": "thread", "removed": false}},
		{"artifact.revised", object{"artifactId": "art_a", "title": "文档", "version": 2, "notify": "thr_a", "diff": "新增内容"}},
		{"artifact.revised", object{"artifactId": "art_a", "title": "文档", "version": 3, "notify": "thr_a"}},
		{"artifact.revised", object{"artifactId": "art_a", "title": "文档", "version": 4}},
		{"artifact.commented", object{"artifactId": "art_a", "notify": "thr_a", "action": object{"threadId": "comment_a", "type": "create", "body": "建议"}}},
		{"artifact.commented", object{"artifactId": "art_a", "notify": "thr_a", "action": object{"threadId": "missing", "type": "reply", "body": "回复"}}},
		{"artifact.commented", object{"artifactId": "art_a", "notify": "thr_a", "action": object{"threadId": "comment_a", "type": "resolve"}}},
		{"artifact.feedback", object{"artifactId": "art_a", "notify": "thr_a", "round": 1, "items": []any{}, "images": []any{}}},
		{"artifact.feedback", object{"artifactId": "missing", "notify": "thr_a", "round": 1, "items": []any{}, "images": []any{}}},
		{"plan.proposal.stale", object{"proposalId": "proposal_a", "reason": object{"key": "error.planConflict"}}},
		{"plan.proposal.stale", object{"proposalId": "missing", "reason": object{"key": "error.planConflict"}}},
		{"plan.revised", object{"revisionId": "rev_a", "author": "user", "revertOf": nil, "reason": "修改"}},
		{"plan.revised", object{"revisionId": "rev_b", "author": "user", "revertOf": "rev_a", "reason": "撤销"}},
		{"plan.revised", object{"revisionId": "rev_c", "author": "coordinator", "revertOf": nil, "reason": "修改"}},
		{"attention.answered", object{"attentionId": "att_a", "answer": object{"type": "choice", "choice": 0}}},
		{"attention.answered", object{"attentionId": "att_b", "answer": object{"type": "confirm", "approved": true}}},
		{"attention.answered", object{"attentionId": "att_b", "answer": object{"type": "confirm", "approved": false}}},
		{"attention.answered", object{"attentionId": "att_c", "answer": object{"type": "form", "values": object{"name": "名称"}}}},
		{"issue.written", object{"attentionId": "att_a", "url": "https://example.com/8"}},
		{"issue.write_unsure", object{"attentionId": "att_a", "threadId": "thr_b"}},
		{"issue.write_unsure", object{"attentionId": "att_a"}},
		{"conversation.compacted", object{"conversationId": "thr_a", "summary": "摘要", "upTo": 10, "reasoning": []any{}, "call": "hash_a", "state": "{}", "userTokens": 0}},
		{"conversation.compacted", object{"conversationId": "thr_a", "summary": "摘要", "upTo": 20, "reasoning": []any{}, "call": "hash_a", "state": "{}", "userTokens": 40}},
		{"conversation.wait_ended", object{"conversationId": "thr_a", "how": "timeout", "until": 1800000000000, "note": "检查"}},
		{"conversation.wait_ended", object{"conversationId": "thr_a", "how": "cancelled", "by": "user", "until": 1800000000000, "note": "检查"}},
		{"conversation.wait_ended", object{"conversationId": "thr_a", "how": "cancelled", "by": "crd_a", "until": 1800000000000, "note": "检查"}},
		{"routine.ran", object{"routineId": "routine_a", "prompt": "定期检查", "dueAt": 1800000000000}},
		{"conversation.prompt_updated", object{"conversationId": "thr_a", "base": true, "sections": []any{[]any{"system", "规则"}}, "tools": "hash_tools"}},
		{"conversation.prompt_updated", object{"conversationId": "thr_a", "base": false, "sections": []any{}, "tools": "", "removedTools": []any{"tool_a"}}},
		{"conversation.results_cleared", object{"conversationId": "thr_a", "upTo": 10}},
		{"turn.step", object{"conversationId": "thr_a", "text": "回复", "reasoning": []any{}, "toolCalls": []any{}, "sawSeq": 20, "finish": "length", "input": object{"call": "hash_a"}}},
		{"turn.step", object{"conversationId": "thr_a", "text": "回复", "reasoning": []any{}, "toolCalls": []any{}, "sawSeq": 20, "finish": "stop", "input": object{"call": "hash_a"}}},
		{"tool.finished", object{"conversationId": "thr_a", "toolCallId": "call_a", "name": "read", "content": "内容", "isError": false, "files": []any{object{"fileId": "fil_a"}}}},
		{"tool.finished", object{"conversationId": "thr_a", "toolCallId": "call_b", "name": "read", "content": "失败", "isError": true, "files": []any{}}},
	}
	events := []eventstore.Event{}
	for i, c := range cases {
		events = append(events, eventstore.Event{Seq: int64(i + 2), ID: "evt_test", ProjectID: "prj_a", Type: c.kind, Data: json.RawMessage(encoded(c.data)), At: 1800000000000 + int64(i)})
	}
	var expected []map[string]any
	oracle(t, "packages/core/src/testing/go-transcript-fixture.ts", object{"seed": seed, "events": events}, &expected)
	store, err := eventstore.Open(t.Context(), eventstore.Options{Path: filepath.Join(t.TempDir(), "transcript.sqlite"), Projections: []eventstore.Projection{LifecycleProjection{}}})
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
			if err := (TranscriptProjection{}).Apply(t.Context(), tx.SQL(), e); err != nil {
				return err
			}
			for _, table := range []string{"transcript", "conversations"} {
				rows, err := records(t.Context(), tx.SQL(), "SELECT * FROM "+table+" ORDER BY id")
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
