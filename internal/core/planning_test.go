package core

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/daidr/Ariadne/internal/eventstore"
)

func TestPlanHistoryReviewsAndPromptsMatchLegacy(t *testing.T) {
	type input struct {
		kind string
		data object
	}
	revise := func(id string, ops []object, revert, proposal any) object {
		return object{"revisionId": id, "author": "user", "reason": "修改计划", "ops": ops, "revertOf": revert, "proposalId": proposal}
	}
	propose := func(id, attention string) object {
		return object{"proposalId": id, "attentionId": attention, "reason": "变更验收", "ops": []object{{"op": "update", "id": "a", "acceptance": "完成"}}, "revertOf": nil}
	}
	issue := func(title string) object {
		return object{"nodeId": "b", "by": "crd_a", "issue": object{"tracker": "owner/repo", "number": 1, "title": title, "url": "https://example.com/1"}}
	}
	write := object{"attentionId": "att_write", "purpose": "issue-comment", "threadId": "thr_a", "tracker": "owner/repo", "number": 1, "action": "comment", "text": "说明", "url": "https://example.com/1"}
	cases := []input{
		{"plan.revised", revise("rev_1", []object{planAdd("a", "A"), planAdd("b", "B"), planAdd("c", "C"), {"op": "link", "from": "a", "to": "b"}, {"op": "link", "from": "b", "to": "c"}}, nil, nil)},
		{"plan.issue_linked", issue("提票")},
		{"plan.issue_linked", issue("新标题")},
		{"plan.proposed", propose("proposal_1", "att_1")},
		{"attention.answered", object{"attentionId": "att_1", "answer": object{"type": "confirm", "approved": true}}},
		{"plan.revised", revise("rev_2", []object{{"op": "update", "id": "a", "acceptance": "完成"}}, nil, "proposal_1")},
		{"plan.revised", revise("rev_3", []object{{"op": "update", "id": "a", "acceptance": "", "expect": map[string]any{"acceptance": "完成"}}}, "rev_2", nil)},
		{"plan.revised", revise("rev_4", []object{{"op": "update", "id": "a", "acceptance": "完成", "expect": map[string]any{"acceptance": ""}}}, "rev_3", nil)},
		{"plan.revised", revise("invalid_cycle", []object{{"op": "link", "from": "c", "to": "a"}}, nil, nil)},
		{"plan.revised", revise("invalid_expect", []object{{"op": "update", "id": "a", "title": "X", "expect": map[string]any{"title": "stale"}}}, nil, nil)},
		{"plan.revised", revise("rev_5", []object{{"op": "remove", "id": "b"}, {"op": "update", "id": "a", "title": "新 A"}, planAdd("d", "D"), {"op": "link", "from": "a", "to": "d"}}, nil, nil)},
		{"plan.revised", revise("rev_6", []object{{"op": "unlink", "from": "a", "to": "d"}, {"op": "remove", "id": "d"}, {"op": "update", "id": "a", "title": "A", "expect": map[string]any{"title": "新 A"}}, planAdd("b", "B"), {"op": "link", "from": "a", "to": "b"}, {"op": "link", "from": "b", "to": "c"}}, "rev_5", nil)},
		{"plan.issue_linked", issue("新关联")},
		{"plan.issue_unlinked", object{"nodeId": "b", "tracker": "owner/repo", "number": 1}},
		{"plan.proposed", propose("proposal_2", "att_2")},
		{"plan.proposal.stale", object{"proposalId": "proposal_2", "reason": "内容已更改"}},
		{"plan.proposal.dismissed", object{"proposalId": "proposal_2"}},
		{"plan.proposed", propose("proposal_3", "att_3")},
		{"plan.proposal.dismissed", object{"proposalId": "proposal_3"}},
		{"plan.proposal.stale", object{"proposalId": "proposal_3", "reason": "节点已删除"}},
		{"plan.proposed", propose("proposal_4", "att_4")},
		{"attention.answered", object{"attentionId": "att_4", "answer": object{"type": "confirm", "approved": false}}},
		{"plan.proposed", propose("proposal_5", "att_5")},
		{"attention.answered", object{"attentionId": "att_5", "answer": object{"type": "form", "values": object{}}}},
		{"issue.writing", write},
		{"issue.writing", write},
		{"issue.write_unsure", object{"attentionId": "att_write"}},
		{"issue.writing", write},
		{"issue.written", write},
		{"review.requested", object{"threadId": "thr_a", "reviewerId": "thr_review", "round": 1, "head": "head_a"}},
		{"review.submitted", object{"threadId": "thr_a", "verdict": "changes", "summary": "建议修改", "comments": []any{object{"body": "建议"}}}},
		{"review.submitted", object{"threadId": "thr_a", "verdict": "pass", "summary": "不可覆盖", "comments": []any{}}},
		{"review.replied", object{"threadId": "thr_a", "replies": []any{object{"comment": 1, "outcome": "fixed", "note": "完成"}}}},
		{"review.replied", object{"threadId": "thr_a", "replies": []any{}}},
		{"review.abandoned", object{"threadId": "thr_a"}},
		{"review.requested", object{"threadId": "thr_a", "reviewerId": "thr_review", "round": 2, "head": "head_b"}},
		{"review.abandoned", object{"threadId": "thr_a"}},
		{"prompt.recorded", object{"hash": "hash_a", "content": "提示词"}},
		{"prompt.recorded", object{"hash": "hash_a", "content": "不能覆盖"}},
		{"prompt.recorded", object{"hash": "hash_b", "content": "工具"}},
	}
	events := []eventstore.Event{}
	for i, c := range cases {
		events = append(events, eventstore.Event{Seq: int64(i + 1), ID: "evt_test", ProjectID: "prj_a", Type: c.kind, Data: json.RawMessage(encoded(c.data)), At: 1800000000000 + int64(i)})
	}
	var expected []map[string]any
	oracle(t, "packages/core/src/testing/go-planning-fixture.ts", events, &expected)
	store, err := eventstore.Open(t.Context(), eventstore.Options{Path: filepath.Join(t.TempDir(), "planning.sqlite"), Projections: []eventstore.Projection{LifecycleProjection{}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for i, e := range events {
		actual := object{}
		err := store.Within(t.Context(), func(tx *eventstore.Transaction) error {
			if err := (PlanningProjection{}).Apply(t.Context(), tx.SQL(), e); err != nil {
				return err
			}
			for _, table := range []string{"plan_nodes", "plan_edges", "plan_node_issues", "plan_revisions", "plan_proposals", "issue_writes", "reviews", "prompts"} {
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
