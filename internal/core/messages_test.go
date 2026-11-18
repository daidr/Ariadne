package core

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/eventstore"
)

func TestPostingQuestionsRelaysEditsAndReadMatchLegacy(t *testing.T) {
	seed := []eventstore.NewEvent{{Type: "project.created", ProjectID: "prj_a", Data: json.RawMessage(`{"name":"项目","coordinatorId":"crd_a"}`)}, {Type: "project.created", ProjectID: "prj_other", Data: json.RawMessage(`{"name":"其他项目","coordinatorId":"crd_other"}`)}}
	for _, thread := range []struct{ id, project string }{{"thr_a", "prj_a"}, {"thr_b", "prj_a"}, {"thr_other", "prj_other"}} {
		seed = append(seed, eventstore.NewEvent{Type: "thread.created", ProjectID: thread.project, Data: json.RawMessage(encoded(object{"threadId": thread.id, "title": "原始标题", "nodeId": nil, "model": nil, "branch": nil}))})
	}
	seed = append(seed, eventstore.NewEvent{Type: "artifact.created", ProjectID: "prj_a", Data: json.RawMessage(`{"artifactId":"art_a","kind":"document","title":"产物标题","nodeId":null,"author":"thr_a","content":"原文"}`)})
	const sql = `INSERT INTO files VALUES ('fil_a','prj_a','text/plain',3,'hash_a',x'616263',0),('fil_other','prj_other','text/plain',3,'hash_b',x'616263',0)`
	commands := []object{}
	add := func(op string, args object) { commands = append(commands, object{"op": op, "args": args}) }
	post := func(id, conversation, author, content string, extra object) {
		input := object{"messageId": id, "conversationId": conversation, "author": author, "content": content}
		for key, value := range extra {
			input[key] = value
		}
		add("post", object{"projectId": "prj_a", "input": input})
	}
	post("msg_user", "crd_a", "user", "用户原文\n第二行", object{"attachments": []object{{"fileId": "fil_a", "name": "  资料\n\x01.txt  "}}, "mentions": []object{{"kind": "thread", "id": "thr_a", "title": "伪造标题"}, {"kind": "artifact", "id": "art_a"}, {"kind": "file", "path": ".///src/main.go"}, {"kind": "skill", "name": "中文@local"}, {"kind": "thread", "id": "thr_a"}}})
	post("msg_relay", "thr_a", "coordinator", "转交", object{"kind": "relay", "cites": []string{"msg_user", "msg_user"}})
	post("msg_agent", "thr_a", "thread", "请选择", object{"blocks": []object{{"type": "table", "columns": []object{{"key": "name", "title": "名称"}}, "rows": []object{{"name": "示例"}}}, {"type": "confirm", "title": "继续？"}}})
	post("msg_reply", "thr_a", "user", "  回答原文  ", object{"replyTo": "msg_agent"})
	for _, emoji := range []string{" 👍 ", "👍", "😀", "👩🏽‍💻", "✅", "hello", "👍👍", "1️⃣", "a"} {
		add("reactUser", object{"id": "msg_agent", "emoji": emoji})
	}
	add("reactUser", object{"id": "msg_agent", "emoji": "👍", "remove": true})
	add("reactUser", object{"id": "msg_agent", "emoji": "👍", "remove": true})
	add("reactUser", object{"id": "msg_agent", "emoji": "🇨🇳"})
	add("reactUser", object{"id": "msg_relay", "emoji": "👍"})
	add("reactUser", object{"id": "msg_user", "emoji": "👍"})
	add("reactUser", object{"id": "missing", "emoji": "👍"})
	add("react", object{"id": "msg_agent", "author": "thread", "conversationId": "thr_a", "emoji": "👍"})
	add("react", object{"id": "msg_reply", "author": "thread", "conversationId": "thr_b", "emoji": "👍"})
	add("react", object{"id": "msg_reply", "author": "user", "conversationId": "thr_a", "emoji": "👍"})
	add("react", object{"id": "msg_reply", "author": "thread", "conversationId": "thr_a", "emoji": "👨‍👩‍👧‍👦"})
	add("react", object{"id": "msg_user", "author": "coordinator", "conversationId": "crd_a", "emoji": "👍"})
	add("answer", object{"id": "latest", "answer": object{"type": "confirm", "approved": true}})
	add("answer", object{"id": "latest", "answer": object{"type": "confirm", "approved": true}})
	post("msg_form", "thr_a", "thread", "填写", object{"blocks": []object{{"type": "form", "title": "要求", "fields": []object{{"key": "name", "label": "名称", "kind": "text", "required": true}}}}})
	add("answer", object{"id": "latest", "answer": object{"type": "form", "values": object{}}})
	add("stop", object{"id": "thr_a", "reason": "暂停", "by": "user"})
	add("answer", object{"id": "latest", "answer": object{"type": "form", "values": object{"name": "测试"}}})
	post("msg_two_asks", "thr_a", "thread", "不允许", object{"blocks": []object{{"type": "confirm", "title": "A？"}, {"type": "confirm", "title": "B？"}}})
	post("msg_foreign_block", "thr_a", "thread", "不允许", object{"blocks": []object{{"type": "thread", "threadId": "thr_other"}}})
	post("msg_bad_relay", "thr_a", "user", "不允许", object{"kind": "relay"})
	post("msg_user_block", "thr_a", "user", "不允许", object{"blocks": []object{{"type": "thread", "threadId": "thr_a"}}})
	post("msg_mentions_only", "thr_a", "user", "", object{"mentions": []object{{"kind": "thread", "id": "thr_a"}}})
	post("msg_file_mention", "thr_a", "user", "不允许", object{"mentions": []object{{"kind": "file", "path": "/etc/passwd"}}})
	post("msg_skill_mention", "thr_a", "user", "不允许", object{"mentions": []object{{"kind": "skill", "name": "bad skill"}}})
	post("msg_other_file", "thr_a", "user", "附件", object{"attachments": []object{{"fileId": "fil_other", "name": "file.txt"}}})
	tooMany := []object{}
	for i := 0; i < 11; i++ {
		tooMany = append(tooMany, object{"fileId": "fil_a", "name": "file.txt"})
	}
	post("msg_many_files", "thr_a", "user", "附件", object{"attachments": tooMany})
	post("msg_agent_mention", "thr_a", "thread", "不允许", object{"mentions": []object{{"kind": "thread", "id": "thr_a"}}})
	comments := []object{{"path": "  main.go  ", "line": 1, "quote": "原文", "body": "  建议修改  ", "side": "original"}}
	post("msg_bad_comments", "crd_a", "user", "评论", object{"comments": comments})
	post("msg_comments", "thr_a", "user", "", object{"comments": comments})
	post("msg_bad_cites", "thr_a", "coordinator", "不允许", object{"cites": []string{"msg_user"}})
	post("msg_agent_cites", "thr_a", "coordinator", "不允许", object{"kind": "relay", "cites": []string{"msg_agent"}})
	post("msg_quote_relay", "thr_a", "user", "引用转交", object{"replyTo": "msg_relay"})
	post("msg_cross_quote", "thr_b", "user", "错误引用", object{"replyTo": "msg_user"})
	add("edit", object{"id": "msg_agent", "author": "user", "conversationId": "thr_a", "content": "不允许", "reason": "修改"})
	add("edit", object{"id": "msg_user", "author": "user", "conversationId": "crd_a", "content": "用户修改后的原文", "reason": "更新"})
	add("read", object{"conversationId": "thr_a", "upTo": "msg_agent"})
	add("read", object{"conversationId": "thr_a", "upTo": "msg_agent"})
	add("edit", object{"id": "msg_agent", "author": "thread", "conversationId": "thr_a", "content": "已读后的编辑", "reason": "补充"})
	add("read", object{"conversationId": "thr_a", "upTo": "msg_agent"})
	post("msg_withdraw", "thr_a", "thread", "问题", object{"blocks": []object{{"type": "confirm", "title": "是否继续？"}}})
	add("withdraw", object{"conversationId": "thr_b", "id": "latest", "reason": "不允许"})
	add("withdraw", object{"conversationId": "thr_a", "id": "latest", "reason": "  已经解决\n无需回答  "})
	add("answer", object{"id": "latest", "answer": object{"type": "confirm", "approved": true}})
	add("withdraw", object{"conversationId": "thr_a", "id": "latest", "reason": "再次撤回"})
	add("open", object{"projectId": "prj_a", "input": object{"block": object{"type": "confirm", "title": "系统问题"}}})
	add("withdraw", object{"conversationId": "crd_a", "id": "latest", "reason": "不允许撤回系统卡片"})
	add("open", object{"projectId": "prj_a", "input": object{"block": object{"type": "thread", "threadId": "thr_a"}}})
	add("open", object{"projectId": "prj_a", "input": object{"threadId": "thr_other", "block": object{"type": "confirm", "title": "错误线程"}}})
	confirm := object{"type": "confirm", "title": "允许执行？"}
	standing := func(thread, purpose string) {
		add("requestApproval", object{"threadId": thread, "purpose": purpose, "card": confirm})
	}
	call := func(thread, purpose string, card object, options object) {
		add("callApproval", object{"threadId": thread, "purpose": purpose, "card": card, "options": options})
	}
	answer := func(approved bool) {
		add("answer", object{"id": "latest", "answer": object{"type": "confirm", "approved": approved}})
	}
	add("approvalFor", object{"threadId": "thr_a", "purpose": "publish"})
	standing("thr_a", "publish")
	standing("thr_a", "publish")
	standing("thr_b", "publish")
	answer(false)
	standing("thr_a", "publish")
	standing("thr_b", "publish")
	standing("thr_other", "publish")
	standing("thr_a", "push")
	answer(true)
	standing("thr_b", "push")
	add("approvalFor", object{"threadId": "thr_a", "purpose": "push"})
	call("thr_a", "command", confirm, object{})
	call("thr_a", "command", confirm, object{})
	answer(false)
	call("thr_a", "command", confirm, object{})
	post("msg_reconsider", "thr_a", "user", "允许重新执行", nil)
	call("thr_a", "command", confirm, object{"inChat": true})
	answer(true)
	call("thr_a", "command", confirm, object{})
	add("useApproval", object{"threadId": "thr_a", "id": "latest", "what": "执行命令"})
	add("useApproval", object{"threadId": "thr_a", "id": "latest", "what": "重复执行"})
	call("thr_a", "command", confirm, object{})
	choice := object{"type": "choice", "title": "选择处理方式", "options": []string{"执行一次", "取消", "按规则执行"}}
	call("crd_a", "reference", choice, object{"inChat": true, "no": 1})
	add("answer", object{"id": "latest", "answer": object{"type": "choice", "choice": 2}})
	call("crd_a", "reference", choice, object{"no": 1})
	add("useApproval", object{"threadId": "crd_a", "id": "latest", "what": "添加来源"})
	call("crd_a", "reference", choice, object{"no": 1})
	add("answer", object{"id": "latest", "answer": object{"type": "choice", "choice": 1}})
	call("crd_a", "reference", choice, object{"no": 1})
	call("crd_a", "bad-choice", choice, object{})
	call("thr_other", "foreign", confirm, object{})
	issue := func(thread string) {
		add("issueApproval", object{"threadId": thread, "purpose": "issue:close:1", "card": confirm})
	}
	write := object{"threadId": "thr_a", "attentionId": "latest", "purpose": "issue:close:1", "tracker": "github", "number": 1, "action": "close", "text": "已修复"}
	issue("thr_a")
	issue("thr_b")
	answer(true)
	issue("thr_a")
	add("startIssueWrite", object{"write": write})
	issue("thr_b")
	add("startIssueWrite", object{"write": write})
	add("finishIssueWrite", object{"write": write, "url": nil})
	issue("thr_b")
	issue("thr_a")
	answer(false)
	issue("thr_a")
	issue("thr_b")
	answer(true)
	add("startIssueWrite", object{"write": write})
	add("unsureIssueWrite", object{"id": "latest", "threadId": "thr_a"})
	issue("thr_a")
	var expected []any
	oracle(t, "packages/core/src/testing/go-chat-fixture.ts", object{"seed": seed, "sql": sql, "commands": commands}, &expected)
	repo, err := Open(t.Context(), Options{Path: filepath.Join(t.TempDir(), "chat.sqlite"), Now: func() time.Time { return time.UnixMilli(1800000000000) }})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if _, err := repo.Store.Append(t.Context(), seed); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Store.DB().ExecContext(t.Context(), sql); err != nil {
		t.Fatal(err)
	}
	attention := ""
	for i, command := range commands {
		a := asObject(jsonValue(command["args"]))
		id := a.str("id")
		if id == "latest" {
			id = attention
		}
		var value any
		var err error
		switch command.str("op") {
		case "post":
			var input MessageInput
			decodeInput(t, a["input"], &input)
			var message MessageView
			message, err = repo.PostMessage(t.Context(), a.str("projectId"), input)
			if err == nil {
				value = message
				if ask := asObject(message.Ask); ask != nil {
					attention = ask.str("attentionId")
				}
			}
		case "open":
			var input AttentionInput
			decodeInput(t, a["input"], &input)
			var view AttentionView
			view, err = repo.OpenAttention(t.Context(), a.str("projectId"), input)
			if err == nil {
				value = view
				attention = view.ID
			}
		case "answer":
			err = repo.AnswerAttention(t.Context(), id, a["answer"])
		case "withdraw":
			err = repo.WithdrawAttention(t.Context(), a.str("conversationId"), id, a.str("reason"))
		case "stop":
			value, err = repo.StopThread(t.Context(), id, a.str("reason"), a.str("by"))
		case "edit":
			value, err = repo.EditMessage(t.Context(), id, a.str("author"), a.str("conversationId"), a.str("content"), a.str("reason"))
		case "read":
			err = repo.MarkRead(t.Context(), a.str("conversationId"), a.str("upTo"))
		case "reactUser":
			value, err = repo.ReactAsUser(t.Context(), id, a.str("emoji"), a.yes("remove"))
		case "react":
			value, err = repo.React(t.Context(), id, a.str("author"), a.str("conversationId"), a.str("emoji"), a.yes("remove"))
		case "approvalFor":
			value, err = repo.ApprovalFor(t.Context(), "prj_a", a.str("threadId"), a.str("purpose"))
		case "requestApproval":
			value, err = repo.RequestApproval(t.Context(), "prj_a", a.str("threadId"), a.str("purpose"), func() map[string]any { return a.obj("card") })
		case "callApproval":
			var options CallApprovalOptions
			decodeInput(t, a["options"], &options)
			value, err = repo.CallApproval(t.Context(), "prj_a", a.str("threadId"), a.str("purpose"), func() map[string]any { return a.obj("card") }, options)
		case "useApproval":
			err = repo.UseApproval(t.Context(), "prj_a", a.str("threadId"), id, a.str("what"))
		case "issueApproval":
			value, err = repo.IssueApproval(t.Context(), "prj_a", a.str("threadId"), a.str("purpose"), func() map[string]any { return a.obj("card") })
		case "startIssueWrite", "finishIssueWrite":
			var write IssueWrite
			decodeInput(t, a["write"], &write)
			if write.AttentionID == "latest" {
				write.AttentionID = attention
			}
			if command.str("op") == "startIssueWrite" {
				err = repo.StartIssueWrite(t.Context(), "prj_a", write)
			} else {
				var url *string
				decodeInput(t, a["url"], &url)
				err = repo.FinishIssueWrite(t.Context(), "prj_a", write, url)
			}
		case "unsureIssueWrite":
			err = repo.MarkIssueWriteUnsure(t.Context(), "prj_a", id, a.str("threadId"))
		default:
			t.Fatal(command)
		}
		var localized any
		if err != nil {
			var domain *domainerr.Error
			if !errors.As(err, &domain) {
				t.Fatal(err)
			}
			localized = domain
			value = nil
		}
		detail, err := repo.GetProjectDetail(t.Context(), "prj_a")
		if err != nil {
			t.Fatal(err)
		}
		reads := []object{}
		for _, id := range []string{"crd_a", "thr_a", "thr_b"} {
			messages, err := repo.ListMessages(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			transcript, err := repo.ListTranscript(t.Context(), id, TranscriptOptions{})
			if err != nil {
				t.Fatal(err)
			}
			notices, err := repo.ListNotices(t.Context(), id, NoticeRange{})
			if err != nil {
				t.Fatal(err)
			}
			reads = append(reads, object{"messages": messages, "transcript": transcript, "notices": notices})
		}
		stored, err := repo.Store.Read(t.Context(), eventstore.ReadOptions{})
		if err != nil {
			t.Fatal(err)
		}
		events := []object{}
		for _, e := range stored {
			events = append(events, object{"seq": e.Seq, "type": e.Type, "projectId": e.ProjectID, "data": e.Data, "at": e.At})
			if e.Type == "attention.opened" && (command.str("op") == "requestApproval" || command.str("op") == "callApproval" || command.str("op") == "issueApproval") {
				var data object
				decodeInput(t, e.Data, &data)
				attention = data.str("attentionId")
			}
		}
		actual := object{"value": value, "error": localized, "detail": detail, "reads": reads, "events": events}
		if !reflect.DeepEqual(normalizedIDs(actual), normalizedIDs(expected[i])) {
			t.Fatalf("command %d %s differs\ngot %s\nwant%s", i, encoded(command), encoded(normalizedIDs(actual)), encoded(normalizedIDs(expected[i])))
		}
	}
}
