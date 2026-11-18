package core

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/internal/domainerr"
)

func TestArtifactContentMatchesLegacy(t *testing.T) {
	tests := []object{}
	add := func(kind, content string) { tests = append(tests, object{"kind": kind, "content": content}) }
	for _, content := range []string{
		"", "# 文档\n\n**重点** 和 [链接](https://example.com)\n- 第一项\n2. 第二项\n---\n> 引用",
		"<pending>待补充\n内容</pending> 以及 \\[转义\\] ![图片](x.png)\n```go\nfunc main() {}\n```",
		"段落\n```dou-whiteboard\n{\"height\":100,\"board\":{\"version\":1,\"nodes\":[],\"edges\":[]}}\n```\n后续内容",
		strings.Repeat("中", 170), strings.Repeat("😀", 100), strings.Repeat("x", 599) + "😀尾部", strings.Repeat("行\n", 180) + strings.Repeat("后", 400),
		"```go\n" + strings.Repeat("x", 1000), strings.Repeat("x", 400) + "\n" + strings.Repeat("y", 400),
		"\ufeff# 标题\u00a0\u3000内容\u2028后续",
	} {
		add("document", content)
	}
	for _, content := range []string{
		emptyContent("whiteboard"), "not JSON", "null", "{}", `{"version":1}`, `{"version":1,"nodes":[],"edges":null}`,
		`{"version":1,"nodes":[{"id":"a","type":"text","x":0,"y":1,"data":{"text":"文字"}},{"id":"b","type":"shape","x":2,"y":3,"data":{"text":"  "}}],"edges":[{"id":"e","source":{"node":"a","port":"out"},"target":{"node":"b","port":"in"}}]}`,
		`{"version":1,"nodes":[{"id":"g","type":"group","x":0,"y":0},{"id":"o","type":"option","x":1,"y":1,"data":{"title":"候选"}},{"id":"a","type":"pending","parent":"o","x":1,"y":1,"data":{"text":"待补充"}}],"edges":[]}`,
		`{"version":1,"nodes":[null],"edges":[]}`,
		`{"version":1,"nodes":[{"id":"a","type":"text","x":0,"y":0},{"id":"a","type":"text","x":0,"y":0}],"edges":[]}`,
		`{"version":1,"nodes":[{"id":"a","type":"bad","x":0,"y":0}],"edges":[]}`,
		`{"version":1,"nodes":[{"id":"a","x":0,"y":0}],"edges":[]}`,
		`{"version":1,"nodes":[{"id":"a","type":"text","x":"0","y":0}],"edges":[]}`,
		`{"version":1,"nodes":[{"id":"a","type":"text","x":0,"y":0,"data":null}],"edges":[]}`,
		`{"version":1,"nodes":[{"id":"a","type":"option","x":0,"y":0}],"edges":[]}`,
		`{"version":1,"nodes":[{"id":"a","type":"text","x":0,"y":0,"parent":"missing"}],"edges":[]}`,
		`{"version":1,"nodes":[],"edges":[{"id":"e","source":{"node":"missing","port":"out"},"target":{"node":"missing","port":"in"}}]}`,
		`{"version":1,"nodes":[],"edges":[{"id":"e","source":{}}]}`,
	} {
		add("whiteboard", content)
	}
	for _, content := range []string{
		emptyContent("dashboard"), "not JSON", "null", "{}", `{"version":1}`, `{"version":1,"dashboard":{"version":1,"widgets":[]},"data":null}`,
		`{"version":1,"dashboard":{"version":1,"widgets":[{"id":"a","type":"stat","title":"指标","props":{"source":"stats"},"place":{"lg":{"x":1,"y":2,"w":3,"h":4}}},{"id":"b","type":"text","title":"说明","props":{"text":"文字"}},{"id":"c","type":"pending","props":{"text":"待补充"}}]},"data":{"stats":1},"takenAt":1800000000000,"source":{"kind":"demo","settings":{}}}`,
		`{"version":1,"dashboard":{"version":1,"widgets":[]},"data":{},"takenAt":"now"}`,
		`{"version":1,"dashboard":{"version":1,"widgets":[]},"data":{},"source":{"kind":""}}`,
		`{"version":1,"dashboard":{"version":1,"widgets":[]},"data":{},"source":{"kind":"demo","settings":null}}`,
		`{"version":1,"dashboard":{"version":1,"widgets":[null]},"data":{}}`,
		`{"version":1,"dashboard":{"version":1,"widgets":[{"id":"a","type":"bad","props":{}}]},"data":{}}`,
		`{"version":1,"dashboard":{"version":1,"widgets":[{"id":"a","type":"stat","props":null}]},"data":{}}`,
		`{"version":1,"dashboard":{"version":1,"widgets":[{"id":"a","type":"stat","props":{"source":"missing"}}]},"data":{}}`,
		`{"version":1,"dashboard":{"version":1,"widgets":[{"id":"a","type":"stat","props":{},"place":{"sm":{"x":-1,"y":0,"w":1,"h":1}}}]},"data":{}}`,
	} {
		add("dashboard", content)
	}
	var expected []any
	oracle(t, "packages/core/src/testing/go-artifact-content-fixture.ts", tests, &expected)
	for i, test := range tests {
		kind, content := test.str("kind"), test.str("content")
		var localized any
		if err := checkArtifactContent(kind, content); err != nil {
			var domain *domainerr.Error
			if !errors.As(err, &domain) {
				t.Fatal(err)
			}
			localized = domain
		}
		actual := object{"error": localized, "preview": artifactPreview(kind, content), "empty": emptyContent(kind)}
		if !reflect.DeepEqual(normalize(actual), normalize(expected[i])) {
			t.Fatalf("case %d %s differs\ngot %s\nwant%s", i, kind, encoded(normalize(actual)), encoded(normalize(expected[i])))
		}
	}
}
