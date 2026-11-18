package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/eventstore"
)

func TestArtifactEditsCommentsFeedbackAndImagesMatchLegacy(t *testing.T) {
	seed := []eventstore.NewEvent{{Type: "project.created", ProjectID: "prj_a", Data: json.RawMessage(`{"name":"产物测试","coordinatorId":"crd_a"}`)}, {Type: "project.created", ProjectID: "prj_other", Data: json.RawMessage(`{"name":"其他","coordinatorId":"crd_other"}`)}, {Type: "thread.created", ProjectID: "prj_a", Data: json.RawMessage(`{"threadId":"thr_a","title":"实现","nodeId":null,"branch":null,"model":null}`)}}
	hash := sha256.Sum256([]byte("abc"))
	sql := fmt.Sprintf(`INSERT INTO files VALUES ('fil_image','prj_a','image/png',3,'%s',x'616263',0),('fil_text','prj_a','text/plain',3,'text',x'616263',0),('fil_other','prj_other','image/png',3,'other',x'616263',0)`, hex.EncodeToString(hash[:]))
	commands := []object{}
	add := func(op string, args object) { commands = append(commands, object{"op": op, "args": args}) }
	create := func(id, kind, content string, extra object) {
		input := object{"title": " 测试产物 ", "kind": kind, "content": content, "author": "thr_a"}
		for k, v := range extra {
			input[k] = v
		}
		add("create", object{"id": id, "input": input})
	}
	revise := func(content, author string, extra object) {
		input := object{"content": content, "author": author, "reason": "修改"}
		for k, v := range extra {
			input[k] = v
		}
		add("revise", object{"id": "doc", "input": input})
	}
	comment := func(id, by string, action object) { add("comment", object{"id": id, "by": by, "action": action}) }
	create("invalid", "document", "原文", object{"title": " "})
	create("invalid", "document", "原文", object{"nodeId": "missing"})
	create("doc", "document", "第一行\n原文\n最后一行", nil)
	create("badboard", "whiteboard", `{"version":1,"nodes":[],"edges":"bad"}`, nil)
	dashboard := `{"version":1,"dashboard":{"version":1,"widgets":[]},"data":{}}`
	create("dashboard", "dashboard", dashboard, nil)
	board := encoded(object{"version": 1, "nodes": []object{{"id": "image_a", "type": "image", "x": 0, "y": 0, "data": object{"src": "data:image/png;base64,YW Jj!"}}}, "edges": []any{}})
	create("board", "whiteboard", board, nil)
	badBoard := encoded(object{"version": 1, "nodes": []object{{"id": "image_a", "type": "image", "x": 0, "y": 0, "data": object{"src": "data:image/png;base64,eHl6"}}}, "edges": "bad"})
	create("failed-image", "whiteboard", badBoard, nil)
	comment("doc", "user", object{"type": "create", "threadId": "comment_a", "commentId": "item_a", "quote": " 原文 ", "body": " 请修改 "})
	comment("doc", "user", object{"type": "create", "threadId": "comment_a", "commentId": "item_b", "quote": "原文", "body": "重复"})
	comment("doc", "thr_a", object{"type": "reply", "threadId": "comment_a", "commentId": "item_b", "body": " 已处理 "})
	comment("doc", "thr_a", object{"type": "reply", "threadId": "comment_a", "commentId": "item_b", "body": "重复"})
	comment("doc", "user", object{"type": "edit", "threadId": "comment_a", "commentId": "item_b", "body": "不能改"})
	comment("doc", "thr_a", object{"type": "edit", "threadId": "comment_a", "commentId": "item_b", "body": " 已补充 "})
	comment("doc", "user", object{"type": "delete", "threadId": "comment_a", "commentId": "item_a"})
	comment("doc", "thr_a", object{"type": "delete", "threadId": "comment_a", "commentId": "item_b"})
	comment("doc", "user", object{"type": "resolve", "threadId": "comment_a"})
	comment("doc", "user", object{"type": "resolve", "threadId": "comment_a"})
	comment("doc", "thr_a", object{"type": "reopen", "threadId": "comment_a"})
	comment("doc", "user", object{"type": "reply", "threadId": "comment_a", "commentId": "item_c", "body": " "})
	comment("doc", "user", object{"type": "edit", "threadId": "missing", "commentId": "missing", "body": "正文"})
	comment("dashboard", "user", object{"type": "create", "threadId": "dash_comment", "commentId": "dash_item", "quote": "原文", "body": "评论"})
	comment("board", "user", object{"type": "create", "threadId": "board_bad", "commentId": "board_item", "quote": "原文", "body": "评论"})
	comment("board", "user", object{"type": "create", "threadId": "board_bad", "commentId": "board_item", "quote": "原文", "body": "评论", "anchor": object{"node": "image_a", "edge": "edge_a"}})
	comment("board", "user", object{"type": "create", "threadId": "board_a", "commentId": "board_item", "quote": "忽略", "body": "图片说明", "anchor": object{"node": "image_a"}})
	comment("doc", "user", object{"type": "create", "threadId": "embedded", "commentId": "embedded_item", "quote": "", "body": "嵌入画板评论", "anchor": object{"edge": "edge_a", "block": " board_block "}})
	revise("第一行\n原文\n最后一行", "user", object{"quotes": object{"comment_a": "引用移动不单独保存"}})
	revise("第一行\n修改后的原文\n最后一行", "user", object{"baseVersion": 0})
	revise("第一行\n修改后的原文\n最后一行", "user", object{"baseVersion": 1, "quotes": object{"comment_a": "修改后的原文", "missing": "忽略"}})
	revise("共享编辑内容", "user", object{"shared": true, "title": " "})
	revise("代理更新", "thr_a", nil)
	add("feedback", object{"id": "doc", "input": object{"items": []any{}}})
	add("feedback", object{"id": "doc", "input": object{"items": []object{{"text": "反馈"}}, "images": []string{"fil_text"}}})
	add("feedback", object{"id": "doc", "input": object{"items": []object{{"text": "反馈"}}, "images": []string{"fil_other"}}})
	add("feedback", object{"id": "doc", "input": object{"items": []object{{"text": "反馈"}}, "images": []string{"fil_image", "fil_image", "fil_image", "fil_image", "fil_image", "fil_image", "fil_text"}}})
	add("feedback", object{"id": "doc", "input": object{"items": []object{{"text": "反馈"}}, "images": []string{"fil_image"}}})
	add("finish", object{"id": "doc", "round": 99, "by": "thr_a", "summary": "无效"})
	add("finish", object{"id": "doc", "round": 1, "by": "thr_a", "summary": " 已处理 "})
	add("finish", object{"id": "doc", "round": 1, "by": "thr_a", "summary": "幂等"})
	add("stop", object{"id": "thr_a"})
	revise("用户后续修改", "user", nil)
	add("feedback", object{"id": "doc", "input": object{"items": []object{{"text": "通知协调器"}}}})
	comment("doc", "thr_a", object{"type": "remove", "threadId": "comment_a"})
	comment("doc", "user", object{"type": "remove", "threadId": "comment_a"})
	for _, pair := range [][2]string{{"", ""}, {"a", "b"}, {"a\nb\nc\nd\ne", "a\nb\nC\nd\ne"}, {"a\nb\nc\nd\ne\nf\ng", "A\nb\nc\nd\ne\nf\nG"}, {"a\nb", "a\nb\nc"}, {strings.Repeat("a\n", 4002), "b"}, {strings.Repeat("a\n", 2001), strings.Repeat("b\n", 2001)}, {"😀\n原文", "😁\n修改"}} {
		for _, limit := range []int{0, 5, 4000} {
			add("diff", object{"before": pair[0], "after": pair[1], "maxChars": limit})
		}
	}
	var expected []any
	oracle(t, "packages/core/src/testing/go-artifact-commands-fixture.ts", object{"seed": seed, "sql": sql, "commands": commands}, &expected)
	repo, err := Open(t.Context(), Options{Path: filepath.Join(t.TempDir(), "artifact-commands.sqlite"), Now: func() time.Time { return time.UnixMilli(1800000000000) }})
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
	ids := map[string]string{}
	for i, command := range commands {
		a := asObject(jsonValue(command["args"]))
		id := a.str("id")
		if actual, ok := ids[id]; ok {
			id = actual
		}
		var value any
		var err error
		switch command.str("op") {
		case "create":
			var input NewArtifact
			decodeInput(t, a["input"], &input)
			var artifact ArtifactView
			artifact, err = repo.CreateArtifact(t.Context(), "prj_a", input)
			if err == nil {
				value = artifact
				ids[a.str("id")] = artifact.ID
			}
		case "revise":
			var input ArtifactRevision
			decodeInput(t, a["input"], &input)
			value, err = repo.ReviseArtifact(t.Context(), id, input)
		case "comment":
			value, err = repo.CommentOnArtifact(t.Context(), id, a.str("by"), a.obj("action"))
		case "feedback":
			var input ArtifactFeedback
			decodeInput(t, a["input"], &input)
			value, err = repo.SendArtifactFeedback(t.Context(), id, input)
		case "finish":
			round, _ := number(a["round"])
			value, err = repo.FinishArtifactFeedback(t.Context(), id, int64(round), a.str("by"), a.str("summary"))
		case "stop":
			value, err = repo.StopThread(t.Context(), id, "暂停", "user")
		case "diff":
			limit, _ := number(a["maxChars"])
			value = LineDiff(a.str("before"), a.str("after"), int(limit))
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
		artifacts, err := repo.ListArtifacts(t.Context(), "prj_a")
		if err != nil {
			t.Fatal(err)
		}
		views := []object{}
		for _, artifact := range artifacts {
			view, err := repo.GetArtifact(t.Context(), artifact.ID, nil)
			if err != nil {
				t.Fatal(err)
			}
			versions, err := repo.ListArtifactVersions(t.Context(), artifact.ID)
			if err != nil {
				t.Fatal(err)
			}
			comments, err := repo.ListArtifactComments(t.Context(), artifact.ID)
			if err != nil {
				t.Fatal(err)
			}
			feedback, err := repo.ListArtifactFeedback(t.Context(), artifact.ID)
			if err != nil {
				t.Fatal(err)
			}
			views = append(views, object{"view": view, "versions": versions, "comments": comments, "feedback": feedback})
		}
		reads := []object{}
		for _, conversation := range []string{"crd_a", "thr_a"} {
			transcript, err := repo.ListTranscript(t.Context(), conversation, TranscriptOptions{})
			if err != nil {
				t.Fatal(err)
			}
			notices, err := repo.ListNotices(t.Context(), conversation, NoticeRange{})
			if err != nil {
				t.Fatal(err)
			}
			reads = append(reads, object{"transcript": transcript, "notices": notices})
		}
		fileRows, err := records(t.Context(), repo.Store.DB(), "SELECT id, project_id, mime, size, sha256 FROM files ORDER BY id")
		if err != nil {
			t.Fatal(err)
		}
		stored, err := repo.Store.Read(t.Context(), eventstore.ReadOptions{})
		if err != nil {
			t.Fatal(err)
		}
		events := []object{}
		for _, e := range stored {
			events = append(events, object{"seq": e.Seq, "type": e.Type, "projectId": e.ProjectID, "data": e.Data, "at": e.At})
		}
		actual := object{"value": value, "error": localized, "artifacts": views, "reads": reads, "files": fileRows, "events": events}
		if !reflect.DeepEqual(normalizedIDs(actual), normalizedIDs(expected[i])) {
			t.Fatalf("command %d %s differs\ngot %s\nwant%s", i, encoded(command), encoded(normalizedIDs(actual)), encoded(normalizedIDs(expected[i])))
		}
	}
}
