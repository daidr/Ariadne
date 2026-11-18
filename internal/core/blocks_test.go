package core

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/internal/domainerr"
)

func TestBlocksAndAnswersMatchLegacy(t *testing.T) {
	choice := object{"type": "choice", "question": "  选哪个？  ", "options": []object{{"label": "A", "consequence": "快"}, {"label": "B", "consequence": "稳"}}}
	form := object{"type": "form", "title": "发布", "fields": []object{{"key": "version", "label": "版本", "kind": "text", "required": true}, {"key": "count", "label": "数量", "kind": "number", "min": 1, "max": 5, "default": 2}, {"key": "env", "label": "环境", "kind": "select", "options": []string{"staging", "prod"}}, {"key": "tags", "label": "标签", "kind": "multiselect", "options": []string{"a", "b"}}, {"key": "notify", "label": "通知", "kind": "switch"}, {"key": "notes", "label": "备注", "kind": "textarea"}}}
	tests := []object{}
	add := func(block any) { tests = append(tests, object{"block": block}) }
	answer := func(block, answer any) { tests = append(tests, object{"block": block, "answer": answer}) }
	for _, block := range []any{nil, []any{}, object{"type": "html", "html": "<b>"}, choice, object{"type": "confirm", "title": " 删除吗？ "}, object{"type": "confirm", "title": "x", "onClick": "alert(1)", "2": "extra", "10": "extra"}, object{"type": "confirm", "title": strings.Repeat("😀", 1001)}, object{"type": "confirm", "title": "x", "tone": "bad"}, object{"type": "confirm", "title": "x", "detail": strings.Repeat("x", 10001)}, form, object{"type": "thread", "threadId": " thr_a "}, object{"type": "steps", "items": []object{{"title": "构建", "status": "done"}, {"title": "发布"}}}, object{"type": "table", "columns": []object{{"key": "name", "title": "名称"}, {"key": "size", "title": "大小", "align": "end"}, {"key": "toString", "title": "其他"}}, "rows": []object{{"name": "a.go", "size": 12}, {"name": "b.go"}}}, object{"type": "chart", "kind": "bar", "x": "day", "y": []string{"open", "closed"}, "labels": object{"open": "开放"}, "rows": []object{{"day": "今天", "open": 3, "closed": 1}}, "stacked": true, "horizontal": true}, object{"type": "chart", "kind": "pie", "x": "k", "y": []string{"v"}, "rows": []object{{"k": "a", "v": 1}}, "stacked": "ignored", "horizontal": "ignored"}, object{"type": "connector", "transport": "stdio", "name": "工具", "command": "program", "args": []string{"--stdio"}, "secrets": []string{"API_KEY"}}, object{"type": "connector", "transport": "http", "name": "工具", "url": "HTTPS://example.com/mcp"}, object{"type": "connector", "transport": "http", "name": "工具", "url": "file:///tmp"}, object{"type": "connector", "transport": "stdio", "name": "工具", "command": "program", "secrets": []string{"bad name"}}, object{"type": "connector", "transport": "stdio", "name": "工具", "command": "program", "args": 3}} {
		add(block)
	}
	for _, mutate := range []object{{"recommended": 2}, {"recommended": 0.5}, {"options": []object{{"label": "A", "consequence": "a"}}}, {"options": []object{{"label": "A", "consequence": "a"}, {"label": "B", "consequence": " "}}}} {
		block := except(choice, "")
		for key, value := range mutate {
			block[key] = value
		}
		add(block)
	}
	for _, field := range []object{{"key": "x", "label": "X", "kind": "select"}, {"key": "x", "label": "X", "kind": "select", "options": []string{"a"}, "default": "b"}, {"key": "x", "label": "X", "kind": "multiselect", "options": []string{"a", "a"}}, {"key": "x", "label": "X", "kind": "text", "options": []string{"a"}}, {"key": "x", "label": "X", "kind": "text", "min": 1}, {"key": "x", "label": "X", "kind": "number", "min": 5, "max": 1}, {"key": "constructor", "label": "C", "kind": "text"}, {"key": "x", "label": "X", "kind": "switch", "required": "ignored"}} {
		add(object{"type": "form", "title": "表单", "fields": []object{field}})
	}
	add(object{"type": "form", "title": "t", "fields": []object{{"key": "x", "label": "X", "kind": "text"}, {"key": "x", "label": "Y", "kind": "text"}}})
	for _, rows := range []any{[]object{{"name": object{"nested": true}}}, []object{{"name": "ok", "owner": "x"}}, []object{{"name": strings.Repeat("x", 2001)}}} {
		add(object{"type": "table", "columns": []object{{"key": "name", "title": "名称"}}, "rows": rows})
	}
	for _, extra := range []object{{"y": []string{"open", "open"}}, {"y": []any{"open", "open", 3}}, {"y": []string{"day"}}, {"labels": object{"wrong": "Wrong"}}, {"rows": []object{{"day": "a", "open": "3"}}}, {"title": " ", "stacked": "bad"}} {
		block := object{"type": "chart", "kind": "bar", "x": "day", "y": []string{"open"}, "rows": []object{{"day": "a", "open": 1}}}
		for key, value := range extra {
			block[key] = value
		}
		add(block)
	}
	for _, given := range []any{object{"type": "choice", "choice": 1}, object{"type": "choice", "choice": 2}, object{"type": "confirm", "approved": true}, nil} {
		answer(choice, given)
	}
	for _, approved := range []any{true, false, "true", nil} {
		answer(object{"type": "confirm", "title": "确认？"}, object{"type": "confirm", "approved": approved})
	}
	for _, values := range []object{{"version": "1.0", "count": 3}, {"version": "  "}, {"version": "1", "count": 9}, {"version": "1", "env": "dev"}, {"version": "1", "tags": []string{"a", "a"}}, {"version": "1", "tags": []string{"a", "a", "bad"}}, {"version": "1", "tags": []string{"a", "b"}, "notify": true}, {"version": "1", "extra": 1}, {"version": "1", "notify": "true"}} {
		answer(form, object{"type": "form", "values": values})
	}
	answer(object{"type": "form", "title": "t", "fields": []object{{"key": "constructor", "label": "C", "kind": "text"}}}, object{"type": "form", "values": object{}})
	var expected []any
	oracle(t, "packages/core/src/testing/go-blocks-fixture.ts", tests, &expected)
	for i, test := range tests {
		block, err := ParseBlock(test["block"])
		var actual any
		if err == nil {
			again, againErr := ParseBlock(block)
			if againErr != nil {
				t.Fatalf("case %d native block failed its second validation: %v", i, againErr)
			}
			var answer any
			var description any
			if raw, has := test["answer"]; has {
				var parsed object
				parsed, err = ParseAnswer(block, raw)
				answer = parsed
				if err == nil {
					description = describeAnswer(block, parsed)
				}
			}
			if err == nil {
				actual = object{"block": block, "again": again, "ask": IsAskBlock(block), "answer": answer, "description": description}
			}
		}
		if err != nil {
			var domain *domainerr.Error
			if !errors.As(err, &domain) {
				t.Fatal(err)
			}
			actual = object{"error": domain}
		}
		if !reflect.DeepEqual(normalize(actual), normalize(expected[i])) {
			t.Fatalf("case %d differs\ninput %s\ngot %s\nwant%s", i, encoded(test), encoded(normalize(actual)), encoded(normalize(expected[i])))
		}
	}
}
