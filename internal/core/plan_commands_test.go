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

func TestPlanCommandsApprovalUndoAndStaleProposalMatchLegacy(t *testing.T) {
	seed := []eventstore.NewEvent{{Type: "project.created", ProjectID: "prj_a", Data: json.RawMessage(`{"name":"项目","coordinatorId":"crd_a"}`)}, {Type: "plan.revised", ProjectID: "prj_a", Data: json.RawMessage(encoded(object{"revisionId": "rev_seed", "author": "user", "reason": "起步", "ops": []object{planAdd("a", "A"), planAdd("b", "B"), {"op": "link", "from": "a", "to": "b"}}, "revertOf": nil, "proposalId": nil}))}, {Type: "thread.created", ProjectID: "prj_a", Data: json.RawMessage(`{"threadId":"thr_a","title":"执行 A","nodeId":"a","model":null,"branch":null}`)}}
	commands := []object{}
	add := func(op string, args object) { commands = append(commands, object{"op": op, "args": args}) }
	revise := func(author, reason string, ops []object, confirm bool, tag string) {
		add("revise", object{"author": author, "reason": reason, "ops": ops, "confirm": confirm, "tag": tag})
	}
	revert := func(revision, tag string) {
		add("revert", object{"revision": revision, "author": "user", "reason": "", "tag": tag})
	}
	add("describe", object{"ops": []object{{"op": "update", "id": "a", "title": "A1", "acceptance": "测试通过"}, {"op": "remove", "id": "a"}}})
	revise("user", "重命名", []object{{"op": "update", "id": "a", "title": "A1"}}, false, "rename1")
	revise("coordinator", "变更验收", []object{{"op": "update", "id": "a", "acceptance": "测试通过"}}, false, "")
	for repeat := 0; repeat < 2; repeat++ {
		revise("coordinator", "删除任务", []object{{"op": "remove", "id": "a"}}, true, "")
		if repeat == 0 {
			add("answer", object{"answer": object{"type": "confirm", "approved": false}})
		} else {
			add("answer", object{"answer": object{"type": "confirm", "approved": true}, "tag": "removed"})
		}
	}
	add("preview", object{"revision": "removed"})
	revert("removed", "undo_removed")
	revert("removed", "")
	revert("undo_removed", "restored_removal")
	add("preview", object{"revision": "restored_removal"})
	revert("restored_removal", "")
	revert("removed", "undo_removed_again")
	revise("coordinator", "补充验收", []object{{"op": "update", "id": "a", "acceptance": "完成", "expect": map[string]any{"title": "A1"}}}, true, "")
	revise("user", "再次重命名", []object{{"op": "update", "id": "a", "title": "A2"}}, false, "rename2")
	add("answer", object{"answer": object{"type": "confirm", "approved": true}})
	add("dismiss", object{})
	add("dismiss", object{})
	add("preview", object{"revision": "rename1"})
	revert("rename1", "")
	revert("rename2", "undo_rename2")
	revise("coordinator", "新增里程碑", []object{{"op": "add", "id": "m", "kind": "milestone", "title": "交付", "description": "", "acceptance": "验收"}}, true, "")
	add("answer", object{"answer": object{"type": "confirm", "approved": true}, "tag": "milestone"})
	revise("user", "无变更", []object{}, false, "")
	revise("user", " ", []object{planAdd("new", "新节点")}, false, "")
	revise("user", "重复节点", []object{planAdd("a", "重复")}, false, "")
	add("linkIssue", object{"nodeId": "a", "issue": object{"tracker": "github", "number": 1, "title": "问题", "url": "https://example.com/1"}})
	add("linkIssue", object{"nodeId": "a", "issue": object{"tracker": "github", "number": 1, "title": "更新标题", "url": "https://example.com/1"}})
	add("linkIssue", object{"nodeId": "a", "issue": object{"tracker": "github", "number": 0, "title": "无效", "url": ""}})
	add("unlinkIssue", object{"nodeId": "a", "issue": object{"tracker": "github", "number": 1}})
	add("unlinkIssue", object{"nodeId": "a", "issue": object{"tracker": "github", "number": 1}})
	add("gate", object{"nodeId": "missing"})
	add("gate", object{"nodeId": "m"})
	add("answer", object{"answer": object{"type": "confirm", "approved": true}})
	add("auto", object{"nodeId": "a", "title": "自动执行", "brief": "根据验收标准完成"})
	add("auto", object{"nodeId": "a", "title": "事务回滚", "brief": ""})
	var expected []any
	oracle(t, "packages/core/src/testing/go-plan-commands-fixture.ts", object{"seed": seed, "commands": commands}, &expected)
	repo, err := Open(t.Context(), Options{Path: filepath.Join(t.TempDir(), "plan-commands.sqlite"), Now: func() time.Time { return time.UnixMilli(1800000000000) }})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if _, err := repo.Store.Append(t.Context(), seed); err != nil {
		t.Fatal(err)
	}
	revisions := map[string]string{}
	attention, proposal := "", ""
	for i, command := range commands {
		a := asObject(jsonValue(command["args"]))
		revision := a.str("revision")
		if value, has := revisions[revision]; has {
			revision = value
		}
		var confirmation PlanConfirmation
		if a.yes("confirm") {
			confirmation = func([]map[string]any) (map[string]any, error) {
				return ConfirmBlock(map[string]any{"title": "确认调整计划？"}), nil
			}
		}
		var value any
		var err error
		var changed object
		switch command.str("op") {
		case "revise":
			ops := []map[string]any{}
			for _, raw := range array(a, "ops") {
				ops = append(ops, asObject(raw))
			}
			changed, err = repo.RevisePlan(t.Context(), "prj_a", PlanRevision{Author: a.str("author"), Reason: a.str("reason"), Ops: ops, Confirmation: confirmation})
			value = changed
		case "revert":
			changed, err = repo.RevertPlanRevision(t.Context(), "prj_a", revision, a.str("author"), a.str("reason"), confirmation)
			value = changed
		case "preview":
			value, err = repo.PreviewPlanUndo(t.Context(), "prj_a", revision)
		case "describe":
			ops := []map[string]any{}
			for _, raw := range array(a, "ops") {
				ops = append(ops, asObject(raw))
			}
			value, err = repo.DescribePlanChange(t.Context(), "prj_a", ops)
		case "answer":
			err = repo.AnswerAttention(t.Context(), attention, a["answer"])
		case "dismiss":
			err = repo.DismissPlanProposal(t.Context(), "prj_a", proposal)
		case "linkIssue":
			var issue LinkedIssue
			decodeInput(t, a["issue"], &issue)
			value, err = repo.LinkIssue(t.Context(), "prj_a", a.str("nodeId"), issue, "user")
		case "unlinkIssue":
			var issue LinkedIssue
			decodeInput(t, a["issue"], &issue)
			value, err = repo.UnlinkIssue(t.Context(), "prj_a", a.str("nodeId"), issue.Tracker, issue.Number, "user")
		case "gate":
			var card AttentionView
			card, err = repo.AskGate(t.Context(), "prj_a", a.str("nodeId"), ConfirmBlock(map[string]any{"title": "继续下一阶段？"}))
			if err == nil {
				value = card
				attention = card.ID
			}
		case "auto":
			value, err = repo.StartAutoThread(t.Context(), "prj_a", a.str("title"), a.str("nodeId"), a.str("brief"))
		default:
			t.Fatal(command)
		}
		if err == nil {
			if changed.str("status") == "applied" && a.str("tag") != "" {
				revisions[a.str("tag")] = changed.str("revisionId")
			}
			if changed.str("status") == "proposed" {
				attention, proposal = changed.str("attentionId"), changed.str("proposalId")
			}
			if command.str("op") == "answer" && a.str("tag") != "" {
				latest, err := records(t.Context(), repo.Store.DB(), "SELECT id FROM plan_revisions ORDER BY seq DESC LIMIT 1")
				if err != nil {
					t.Fatal(err)
				}
				revisions[a.str("tag")] = latest[0].text("id")
			}
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
		plan, err := repo.GetPlan(t.Context(), "prj_a")
		if err != nil {
			t.Fatal(err)
		}
		detail, err := repo.GetProjectDetail(t.Context(), "prj_a")
		if err != nil {
			t.Fatal(err)
		}
		transcript, err := repo.ListTranscript(t.Context(), "crd_a", TranscriptOptions{})
		if err != nil {
			t.Fatal(err)
		}
		proposals, err := records(t.Context(), repo.Store.DB(), "SELECT * FROM plan_proposals ORDER BY id")
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
		actual := object{"value": value, "error": localized, "plan": plan, "detail": detail, "transcript": transcript, "proposals": proposals, "events": events}
		want := normalizedIDs(normalizeLegacyExpected(expected[i]))
		if !reflect.DeepEqual(normalizedIDs(actual), want) {
			t.Fatalf("command %d %s differs\ngot %s\nwant%s", i, encoded(command), encoded(normalizedIDs(actual)), encoded(want))
		}
	}
}
