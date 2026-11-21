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

func TestPlanCommandsMatchLegacy(t *testing.T) {
	seed := []eventstore.NewEvent{{Type: "project.created", ProjectID: "prj_a", Data: json.RawMessage(`{"name":"项目","coordinatorId":"crd_a"}`)}, {Type: "plan.revised", ProjectID: "prj_a", Data: json.RawMessage(encoded(object{"revisionId": "rev_seed", "author": "user", "reason": "起步", "ops": []object{planAdd("a", "A"), planAdd("b", "B"), {"op": "link", "from": "a", "to": "b"}}, "revertOf": nil, "proposalId": nil}))}, {Type: "thread.created", ProjectID: "prj_a", Data: json.RawMessage(`{"threadId":"thr_a","title":"执行 A","nodeId":"a","model":null,"branch":null}`)}}
	commands := []object{}
	add := func(op string, args object) { commands = append(commands, object{"op": op, "args": args}) }
	revise := func(author, reason string, ops []object, tag string) {
		add("revise", object{"author": author, "reason": reason, "ops": ops, "tag": tag})
	}
	revert := func(revision, tag string) {
		add("revert", object{"revision": revision, "author": "user", "reason": "", "tag": tag})
	}
	add("describe", object{"ops": []object{{"op": "update", "id": "a", "title": "A1", "acceptance": "测试通过"}, {"op": "remove", "id": "a"}}})
	revise("user", "重命名", []object{{"op": "update", "id": "a", "title": "A1"}}, "rename1")
	revise("coordinator", "补充说明", []object{{"op": "update", "id": "a", "description": "协调器补充说明"}}, "describe1")
	revise("user", "删除任务", []object{{"op": "remove", "id": "a"}}, "removed")
	add("preview", object{"revision": "removed"})
	revert("removed", "undo_removed")
	revert("removed", "")
	revert("undo_removed", "restored_removal")
	add("preview", object{"revision": "restored_removal"})
	revert("restored_removal", "")
	revert("removed", "undo_removed_again")
	revise("user", "再次重命名", []object{{"op": "update", "id": "a", "title": "A2"}}, "rename2")
	add("preview", object{"revision": "rename1"})
	revert("rename1", "")
	revert("rename2", "undo_rename2")
	revise("user", "新增里程碑", []object{{"op": "add", "id": "m", "kind": "milestone", "title": "交付", "description": "", "acceptance": "验收"}}, "milestone")
	revise("user", "无变更", []object{}, "")
	revise("user", " ", []object{planAdd("new", "新节点")}, "")
	revise("user", "重复节点", []object{planAdd("a", "重复")}, "")
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
	attention := ""
	for i, command := range commands {
		a := asObject(jsonValue(command["args"]))
		revision := a.str("revision")
		if value, has := revisions[revision]; has {
			revision = value
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
			changed, err = repo.RevisePlan(t.Context(), "prj_a", PlanRevision{Author: a.str("author"), Reason: a.str("reason"), Ops: ops})
			value = changed
		case "revert":
			changed, err = repo.RevertPlanRevision(t.Context(), "prj_a", revision, a.str("author"), a.str("reason"))
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
		if err == nil && changed.str("status") == "applied" && a.str("tag") != "" {
			revisions[a.str("tag")] = changed.str("revisionId")
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

func TestCoordinatorPlanChangesApplyDirectlyWithoutConfirmation(t *testing.T) {
	ctx := t.Context()
	repo, err := Open(ctx, Options{Path: filepath.Join(t.TempDir(), "direct-plan.sqlite"), Now: func() time.Time { return time.UnixMilli(1800000000000) }})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	project, err := repo.CreateProject(ctx, new("Direct plan"), ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.RevisePlan(ctx, project.ID, PlanRevision{
		Author: "coordinator",
		Reason: "初始化计划",
		Ops:    []map[string]any{planAdd("a", "实现接口"), planAdd("b", "编写测试"), {"op": "link", "from": "a", "to": "b"}},
	}); err != nil {
		t.Fatal(err)
	}
	thread, err := repo.CreateThread(ctx, project.ID, NewThread{Title: "执行接口", NodeID: new("a")})
	if err != nil {
		t.Fatal(err)
	}

	assertNoPendingApproval := func() {
		t.Helper()
		attentions, err := repo.ListOpenAttentions(ctx, project.ID)
		if err != nil || len(attentions) != 0 {
			t.Fatalf("unexpected open attentions: %+v, %v", attentions, err)
		}
		plan, err := repo.GetPlan(ctx, project.ID)
		if err != nil || len(plan["proposals"].([]object)) != 0 {
			t.Fatalf("unexpected plan proposals: %+v, %v", plan, err)
		}
	}

	// Updating acceptance applies directly.
	acceptanceChange, err := repo.RevisePlan(ctx, project.ID, PlanRevision{
		Author: "coordinator",
		Reason: "补充验收标准",
		Ops:    []map[string]any{{"op": "update", "id": "a", "acceptance": "单测全部通过"}},
	})
	if err != nil || acceptanceChange.str("status") != "applied" || acceptanceChange.str("revisionId") == "" {
		t.Fatalf("acceptance update did not apply directly: %+v, %v", acceptanceChange, err)
	}
	assertNoPendingApproval()

	// Adding and updating a milestone apply directly.
	milestoneAdd, err := repo.RevisePlan(ctx, project.ID, PlanRevision{
		Author: "coordinator",
		Reason: "增加发布里程碑",
		Ops:    []map[string]any{{"op": "add", "id": "m", "kind": "milestone", "title": "上线前验收", "description": "", "acceptance": "完成验证"}},
	})
	if err != nil || milestoneAdd.str("status") != "applied" {
		t.Fatalf("milestone add did not apply directly: %+v, %v", milestoneAdd, err)
	}
	milestoneUpdate, err := repo.RevisePlan(ctx, project.ID, PlanRevision{
		Author: "coordinator",
		Reason: "调整里程碑标题",
		Ops:    []map[string]any{{"op": "update", "id": "m", "title": "发布前验收"}},
	})
	if err != nil || milestoneUpdate.str("status") != "applied" {
		t.Fatalf("milestone update did not apply directly: %+v, %v", milestoneUpdate, err)
	}
	assertNoPendingApproval()

	// Removing a node applies directly and stops active threads on that node.
	removeChange, err := repo.RevisePlan(ctx, project.ID, PlanRevision{
		Author: "coordinator",
		Reason: "移除过期任务",
		Ops:    []map[string]any{{"op": "remove", "id": "a"}},
	})
	if err != nil || removeChange.str("status") != "applied" {
		t.Fatalf("remove did not apply directly: %+v, %v", removeChange, err)
	}
	assertNoPendingApproval()
	threads, err := repo.ListThreads(ctx, project.ID)
	if err != nil || len(threads) != 1 || threads[0].ID != thread.ID || threads[0].Stopped == nil || threads[0].Stopped.By != "coordinator" {
		t.Fatalf("thread on removed node was not stopped by coordinator: %+v, %v", threads, err)
	}

	// Coordinator reverting both a removal and a milestone addition applies directly and records revision links.
	revertRemove, err := repo.RevertPlanRevision(ctx, project.ID, removeChange.str("revisionId"), "coordinator", "恢复任务")
	if err != nil || revertRemove.str("status") != "applied" {
		t.Fatalf("revert remove did not apply directly: %+v, %v", revertRemove, err)
	}
	if _, err := repo.RevertPlanRevision(ctx, project.ID, milestoneUpdate.str("revisionId"), "coordinator", "撤销里程碑改名"); err != nil {
		t.Fatal(err)
	}
	revertMilestoneAdd, err := repo.RevertPlanRevision(ctx, project.ID, milestoneAdd.str("revisionId"), "coordinator", "撤销新增里程碑")
	if err != nil || revertMilestoneAdd.str("status") != "applied" {
		t.Fatalf("revert milestone add did not apply directly: %+v, %v", revertMilestoneAdd, err)
	}
	assertNoPendingApproval()

	plan, err := repo.GetPlan(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	nodes := plan["nodes"].([]object)
	if len(nodes) != 2 || nodes[0].str("id") != "b" && nodes[1].str("id") != "b" {
		t.Fatalf("unexpected plan nodes after reverts: %+v", nodes)
	}
	var restoredA object
	for _, node := range nodes {
		if node.str("id") == "a" {
			restoredA = node
		}
		if node.str("id") == "m" {
			t.Fatalf("reverted milestone still exists: %+v", nodes)
		}
	}
	if restoredA == nil || restoredA.str("acceptance") != "单测全部通过" {
		t.Fatalf("restored node did not retain acceptance: %+v", restoredA)
	}
	revisions := plan["revisions"].([]object)
	if len(revisions) != 8 {
		t.Fatalf("want 8 recorded revisions, got %d: %+v", len(revisions), revisions)
	}
	if revisions[0].str("revertOf") != milestoneAdd.str("revisionId") {
		t.Fatalf("latest revision missing revertOf link: %+v", revisions[0])
	}
}

func TestLegacyPlanProposalsRemainSupported(t *testing.T) {
	ctx := t.Context()
	repo, err := Open(ctx, Options{Path: filepath.Join(t.TempDir(), "legacy-proposals.sqlite"), Now: func() time.Time { return time.UnixMilli(1800000000000) }})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	seed := []eventstore.NewEvent{
		{Type: "project.created", ProjectID: "prj_a", Data: json.RawMessage(`{"name":"历史项目","coordinatorId":"crd_a"}`)},
		{Type: "plan.revised", ProjectID: "prj_a", Data: json.RawMessage(encoded(object{"revisionId": "rev_seed", "author": "user", "reason": "起步", "ops": []object{planAdd("a", "A")}, "revertOf": nil, "proposalId": nil}))},
		{Type: "attention.opened", ProjectID: "prj_a", Data: json.RawMessage(encoded(object{"attentionId": "att_reject", "conversationId": nil, "messageId": nil, "purpose": nil, "block": ConfirmBlock(object{"title": "确认删除？"})}))},
		{Type: "plan.proposed", ProjectID: "prj_a", Data: json.RawMessage(encoded(object{"proposalId": "prp_reject", "attentionId": "att_reject", "reason": "删除 A", "ops": []object{{"op": "remove", "id": "a"}}, "revertOf": nil}))},
		{Type: "attention.opened", ProjectID: "prj_a", Data: json.RawMessage(encoded(object{"attentionId": "att_stale", "conversationId": nil, "messageId": nil, "purpose": nil, "block": ConfirmBlock(object{"title": "确认修改验收？"})}))},
		{Type: "plan.proposed", ProjectID: "prj_a", Data: json.RawMessage(encoded(object{"proposalId": "prp_stale", "attentionId": "att_stale", "reason": "补充验收", "ops": []object{{"op": "update", "id": "a", "acceptance": "完成", "expect": map[string]any{"title": "A"}}}, "revertOf": nil}))},
		{Type: "attention.opened", ProjectID: "prj_a", Data: json.RawMessage(encoded(object{"attentionId": "att_apply", "conversationId": nil, "messageId": nil, "purpose": nil, "block": ConfirmBlock(object{"title": "确认新增里程碑？"})}))},
		{Type: "plan.proposed", ProjectID: "prj_a", Data: json.RawMessage(encoded(object{"proposalId": "prp_apply", "attentionId": "att_apply", "reason": "新增里程碑", "ops": []object{{"op": "add", "id": "m", "kind": "milestone", "title": "交付", "description": "", "acceptance": "通过"}}, "revertOf": nil}))},
	}
	if _, err := repo.Store.Append(ctx, seed); err != nil {
		t.Fatal(err)
	}

	modelPlan, err := repo.ModelPlan(ctx, "prj_a")
	if err != nil || len(modelPlan["waitingOnUser"].([]object)) != 3 {
		t.Fatalf("legacy open proposals missing from ModelPlan: %+v, %v", modelPlan, err)
	}

	// Rejecting a legacy proposal closes it without changing the plan.
	if err := repo.AnswerAttention(ctx, "att_reject", object{"type": "confirm", "approved": false}); err != nil {
		t.Fatal(err)
	}
	// Rename node A so prp_stale conflicts when approved.
	if _, err := repo.RevisePlan(ctx, "prj_a", PlanRevision{Author: "user", Reason: "重命名", Ops: []map[string]any{{"op": "update", "id": "a", "title": "A2"}}}); err != nil {
		t.Fatal(err)
	}
	if err := repo.AnswerAttention(ctx, "att_stale", object{"type": "confirm", "approved": true}); err != nil {
		t.Fatal(err)
	}
	plan, err := repo.GetPlan(ctx, "prj_a")
	if err != nil {
		t.Fatal(err)
	}
	var staleFound bool
	for _, item := range plan["proposals"].([]object) {
		if item.str("id") == "prp_stale" && item.str("status") == "stale" {
			staleFound = true
		}
	}
	if !staleFound {
		t.Fatalf("expected prp_stale to become stale: %+v", plan["proposals"])
	}
	if err := repo.DismissPlanProposal(ctx, "prj_a", "prp_stale"); err != nil {
		t.Fatal(err)
	}

	// Approving a still-valid legacy proposal applies it.
	if err := repo.AnswerAttention(ctx, "att_apply", object{"type": "confirm", "approved": true}); err != nil {
		t.Fatal(err)
	}
	plan, err = repo.GetPlan(ctx, "prj_a")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan["proposals"].([]object)) != 0 || len(plan["nodes"].([]object)) != 2 {
		t.Fatalf("unexpected plan after resolving legacy proposals: %+v", plan)
	}
}
