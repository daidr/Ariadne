package core

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/internal/eventstore"
)

func TestArtifactsAndLocalSearchMatchLegacy(t *testing.T) {
	board := `{"version":1,"nodes":[{"id":"a","type":"text","x":0,"y":0,"data":{"text":"终端测试","label":"备注"}}],"edges":[]}`
	dashboard := `{"version":1,"dashboard":{"version":1,"widgets":[{"id":"a","type":"text","title":"指标","props":{"text":"终端测试","content":"数据说明"}}]},"data":{}}`
	create := func(id, kind, content string) object {
		return object{"artifactId": id, "kind": kind, "title": "初始标题", "nodeId": nil, "author": "user", "content": content}
	}
	revise := func(id string, version int, author, content string) object {
		return object{"artifactId": id, "version": version, "title": "最新标题", "author": author, "content": content, "reason": "更新"}
	}
	comment := func(action object) object { return object{"artifactId": "art_a", "by": "user", "action": action} }
	type testEvent struct {
		kind string
		data object
	}
	cases := []testEvent{
		{"thread.created", object{"threadId": "thr_a", "title": "终端测试任务"}},
		{"thread.renamed", object{"threadId": "thr_a", "title": "终端开发测试任务"}},
		{"thread.reported", object{"threadId": "thr_a", "headline": "实现终端测试"}},
		{"message.posted", object{"messageId": "msg_a", "conversationId": "thr_a", "content": "终端测试 alpha beta 100% _under_ \\path"}},
		{"message.edited", object{"messageId": "msg_a", "content": "修改后终端测试 alpha beta 100% _under_ \\path"}},
		{"message.edited", object{"messageId": "missing", "content": "不应新增索引"}},
		{"artifact.created", create("art_a", "document", "# 终端测试文档\n[说明](https://example.com)")},
		{"artifact.created", create("art_board", "whiteboard", board)},
		{"artifact.created", create("art_dashboard", "dashboard", dashboard)},
		{"artifact.commented", comment(object{"type": "create", "threadId": "comment_a", "commentId": "item_a", "body": "请修改", "quote": "原文"})},
		{"artifact.commented", comment(object{"type": "reply", "threadId": "comment_a", "commentId": "item_b", "body": "已修改"})},
		{"artifact.commented", comment(object{"type": "edit", "threadId": "comment_a", "commentId": "item_b", "body": "已补充"})},
		{"artifact.commented", comment(object{"type": "delete", "threadId": "comment_a", "commentId": "item_a"})},
		{"artifact.commented", comment(object{"type": "resolve", "threadId": "comment_a"})},
		{"artifact.commented", comment(object{"type": "reopen", "threadId": "comment_a"})},
		{"artifact.revised", revise("art_a", 2, "thr_a", "# 新增终端测试说明")},
		{"artifact.revised", func() object {
			value := revise("art_a", 3, "user", "# 终端测试说明\n用户更改")
			value["quotes"] = object{"comment_a": "更新后的引用", "other": "不会创建"}
			return value
		}()},
		{"artifact.feedback", object{"artifactId": "art_a", "round": 1, "version": 3, "notify": "thr_a", "items": []any{object{"text": "反馈"}}, "images": []any{}}},
		{"artifact.feedback_done", object{"artifactId": "art_a", "round": 1, "by": "thr_a", "summary": "完成"}},
		{"artifact.commented", comment(object{"type": "remove", "threadId": "comment_a"})},
		{"artifact.revised", revise("art_board", 2, "thr_a", board)},
		{"artifact.revised", revise("art_dashboard", 2, "thr_a", dashboard)},
		{"artifact.commented", object{"artifactId": "art_board", "by": "thr_a", "action": object{"type": "create", "threadId": "board_comment", "commentId": "board_item", "body": "画板评论", "quote": "节点", "anchor": object{"kind": "node", "id": "a"}}}},
	}
	events := []eventstore.Event{}
	for i, c := range cases {
		events = append(events, eventstore.Event{Seq: int64(i + 1), ID: "evt_test", ProjectID: "prj_a", Type: c.kind, Data: json.RawMessage(encoded(c.data)), At: 1800000000000 + int64(i)})
	}
	events = append(events, eventstore.Event{Seq: 100, ID: "evt_other", ProjectID: "prj_b", Type: "message.posted", Data: json.RawMessage(encoded(object{"messageId": "msg_other", "conversationId": "crd_b", "content": "终端测试 alpha"})), At: 1800000000100})
	queries := []object{}
	for _, kind := range []string{"thread", "artifact", "message"} {
		for _, query := range []string{"终端测试", "终端 测试", "alpha beta", "100%", "_under_", `\path`, "\ufeff终端测试\u3000 终端测试", "absent", "", "a b c d e f g h i j"} {
			queries = append(queries, object{"projectId": "prj_a", "kind": kind, "query": query})
		}
	}
	queries = append(queries, object{"projectId": "prj_b", "kind": "message", "query": "终端测试"})
	texts := []object{}
	for _, pair := range [][2]string{{"whiteboard", board}, {"dashboard", dashboard}, {"whiteboard", "{}"}, {"dashboard", "{}"}, {"document", "# 文档\n```dou-whiteboard\n{\"board\":" + board + "}\n```\n其余"}, {"document", "```dou-whiteboard\nbad JSON\n```"}, {"document", strings.Repeat("前", 50) + "终端测试" + strings.Repeat("后", 180)}, {"document", "# 标题 [链接](x)\n**重点** _单字_ ~~删除~~ `code`\n一行"}, {"document", "İFoo ΟΣ 内容"}} {
		texts = append(texts, object{"kind": pair[0], "content": pair[1], "query": "终端测试 foo ος"})
	}
	var expected struct {
		Snapshots []any `json:"snapshots"`
		Searches  []any `json:"searches"`
		Texts     []any `json:"texts"`
	}
	oracle(t, "packages/core/src/testing/go-artifacts-fixture.ts", object{"events": events, "queries": queries, "texts": texts}, &expected)
	store, err := eventstore.Open(t.Context(), eventstore.Options{Path: filepath.Join(t.TempDir(), "artifacts.sqlite"), Projections: []eventstore.Projection{LifecycleProjection{}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for i, e := range events {
		actual := object{}
		err := store.Within(t.Context(), func(tx *eventstore.Transaction) error {
			for _, projection := range []eventstore.Projection{ArtifactProjection{}, SearchProjection{}} {
				if err := projection.Apply(t.Context(), tx.SQL(), e); err != nil {
					return err
				}
			}
			for _, table := range []string{"artifacts", "artifact_versions", "artifact_comments", "artifact_feedback", "search_docs", "search_text"} {
				fields := "*"
				if table == "search_text" {
					fields = "rowid, *"
				}
				rows, err := records(t.Context(), tx.SQL(), "SELECT "+fields+" FROM "+table+" ORDER BY rowid")
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
		if !reflect.DeepEqual(normalize(actual), normalize(expected.Snapshots[i])) {
			t.Fatalf("event %d %s differs\ngot %s\nwant%s", i+1, e.Type, encoded(normalize(actual)), encoded(normalize(expected.Snapshots[i])))
		}
	}
	for i, query := range queries {
		terms := searchTerms(query.str("query"))
		rows, err := searchRows(t.Context(), store.DB(), query.str("projectId"), query.str("kind"), terms, 30)
		if err != nil {
			t.Fatalf("query %s: %v", encoded(query), err)
		}
		actual := object{"terms": terms, "rows": rows}
		if !reflect.DeepEqual(normalize(actual), normalize(expected.Searches[i])) {
			t.Fatalf("query %s differs\ngot %s\nwant%s", encoded(query), encoded(normalize(actual)), encoded(normalize(expected.Searches[i])))
		}
	}
	for i, test := range texts {
		actual := object{"text": searchText(test.str("kind"), test.str("content")), "snippet": snippet(test.str("content"), searchTerms(test.str("query")))}
		if !reflect.DeepEqual(normalize(actual), normalize(expected.Texts[i])) {
			t.Fatalf("text %d differs\ngot %s\nwant%s", i, encoded(normalize(actual)), encoded(normalize(expected.Texts[i])))
		}
	}
}
