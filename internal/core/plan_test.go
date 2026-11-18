package core

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/internal/domainerr"
)

func planAdd(id, title string) object {
	return object{"op": "add", "id": id, "kind": "task", "title": title, "description": "", "acceptance": ""}
}
func planJSON(graph planGraph) object {
	nodes := []any{}
	for _, id := range graph.order {
		nodes = append(nodes, []any{id, graph.nodes[id]})
	}
	return object{"nodes": nodes, "order": graph.order, "edges": graph.edges}
}

func TestPlanOperationsMatchLegacy(t *testing.T) {
	initial := []object{planAdd("a", "原标题"), planAdd("b", "B"), planAdd("c", "C"), {"op": "link", "from": "a", "to": "b"}, {"op": "link", "from": "b", "to": "c"}}
	inputs := []any{
		nil, []any{}, "bad", []any{nil}, []any{3}, []any{[]any{}}, []any{object{"op": "bad"}},
		[]any{object{"op": "add", "id": "d", "title": " D ", "extra": "discard"}},
		[]any{object{"op": "add", "id": "a", "title": "duplicate"}},
		[]any{object{"op": "add", "id": "d", "title": " "}},
		[]any{object{"op": "add", "id": "", "title": "D"}},
		[]any{object{"op": "add", "id": strings.Repeat("😀", 41), "title": "D"}},
		[]any{object{"op": "add", "id": "d", "title": "D", "kind": "bad"}},
		[]any{object{"op": "add", "id": "d", "title": nil}},
		[]any{object{"op": "add", "id": "d", "title": strings.Repeat("x", 20001)}},
		[]any{object{"op": "add", "id": "d", "title": "D", "kind": "milestone"}},
		[]any{object{"op": "add", "id": "d", "title": "D", "kind": "decision", "description": "说明", "acceptance": "测试通过"}},
		[]any{object{"op": "remove", "id": "b"}, object{"op": "update", "id": "a", "title": "新标题"}, planAdd("d", "D"), object{"op": "link", "from": "a", "to": "d"}},
		[]any{object{"op": "remove", "id": "gone"}},
		[]any{object{"op": "update", "id": "a", "title": " 新标题 ", "description": "新说明", "acceptance": "完成", "expect": object{"title": "原标题"}}},
		[]any{object{"op": "update", "id": "a", "title": "X", "expect": object{"title": "旧标题"}}},
		[]any{object{"op": "update", "id": "a", "title": " "}},
		[]any{object{"op": "update", "id": "a", "expect": nil}},
		[]any{object{"op": "update", "id": "a", "expect": []any{}}},
		[]any{object{"op": "update", "id": "a", "expect": object{"unknown": "ignored"}}},
		[]any{object{"op": "update", "id": "a", "description": 3}},
		[]any{object{"op": "update", "id": "a", "expect": object{"title": 3}}},
		[]any{object{"op": "update", "id": "gone", "title": "X"}},
		[]any{object{"op": "link", "from": "a", "to": "b"}},
		[]any{object{"op": "link", "from": "a", "to": "a"}},
		[]any{object{"op": "link", "from": "c", "to": "a"}},
		[]any{object{"op": "link", "from": "a", "to": "gone"}},
		[]any{object{"op": "unlink", "from": "a", "to": "b"}},
		[]any{object{"op": "unlink", "from": "c", "to": "a"}},
		[]any{object{"op": "update", "id": "a"}},
	}
	tests := []object{}
	for _, input := range inputs {
		tests = append(tests, object{"initial": initial, "ops": input})
	}
	var expected []any
	oracle(t, "packages/core/src/testing/go-plan-fixture.ts", tests, &expected)
	for i, test := range tests {
		// Exercise the same JSON boundary the API will use, including optional keys.
		value := jsonValue(test).(map[string]any)
		rawInitial := value["initial"].([]any)
		start := []object{}
		for _, v := range rawInitial {
			start = append(start, v.(map[string]any))
		}
		graph, _, err := applyOps(emptyGraph(), start)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := parsePlanOps(value["ops"])
		var actual any
		if err == nil {
			preview, confirmation, failure := previewPlanOps(graph, parsed), needsConfirmation(graph, parsed), whyUndoFails(graph, parsed, map[string]string{"gone": "原节点"})
			var next planGraph
			var inverse []object
			next, inverse, err = applyOps(graph, parsed)
			if err == nil {
				restored, _, restoreErr := applyOps(next, inverse)
				if restoreErr != nil {
					t.Fatal(restoreErr)
				}
				actual = object{"parsed": parsed, "preview": preview, "confirmation": confirmation, "failure": failure, "graph": planJSON(next), "inverse": inverse, "restored": planJSON(restored)}
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
			t.Fatalf("case %d differs\ngot %s\nwant%s", i, encoded(normalize(actual)), encoded(normalize(expected[i])))
		}
	}
}
