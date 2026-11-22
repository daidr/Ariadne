package core

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/eventstore"
)

func planAdd(id, title string) object {
	return object{"op": "add", "id": id, "kind": "task", "title": title, "description": "", "acceptance": ""}
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

	func TestPlanNodeGroupAddUpdateClearAndRevert(t *testing.T) {
		ctx := t.Context()
		repo, err := Open(ctx, Options{Path: filepath.Join(t.TempDir(), "plan-groups.sqlite"), Now: func() time.Time { return time.UnixMilli(1800000000000) }})
		if err != nil {
			t.Fatal(err)
		}
		defer repo.Close()
		project, err := repo.CreateProject(ctx, new("Grouped plan"), ProjectStart{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repo.RevisePlan(ctx, project.ID, PlanRevision{
			Author: "coordinator",
			Reason: "创建带分组的节点",
			Ops: []map[string]any{
				{"op": "add", "id": "a", "kind": "task", "title": "协议定义", "description": "", "acceptance": "", "group": " Protobuf 迁移 "},
				{"op": "add", "id": "b", "kind": "task", "title": "独立任务", "description": "", "acceptance": ""},
			},
		}); err != nil {
			t.Fatal(err)
		}
		plan, err := repo.GetPlan(ctx, project.ID)
		if err != nil {
			t.Fatal(err)
		}
		nodes := plan["nodes"].([]object)
		if len(nodes) != 2 || nodes[0].str("group") != "Protobuf 迁移" || nodes[1].str("group") != "" {
			t.Fatalf("unexpected initial groups: %+v", nodes)
		}
		brief, err := repo.PlanBrief(ctx, project.ID, "a")
		if err != nil || brief != "协议定义\n\nGroup: Protobuf 迁移" {
			t.Fatalf("PlanBrief = %q, err=%v", brief, err)
		}

		// Update group and clear group, then revert the clear.
		if _, err := repo.RevisePlan(ctx, project.ID, PlanRevision{
			Author: "coordinator",
			Reason: "归入分组",
			Ops:    []map[string]any{{"op": "update", "id": "b", "group": "Protobuf 迁移"}},
		}); err != nil {
			t.Fatal(err)
		}
		clearRev, err := repo.RevisePlan(ctx, project.ID, PlanRevision{
			Author: "coordinator",
			Reason: "移出分组",
			Ops:    []map[string]any{{"op": "update", "id": "a", "group": ""}},
		})
		if err != nil {
			t.Fatal(err)
		}
		plan, err = repo.GetPlan(ctx, project.ID)
		if err != nil {
			t.Fatal(err)
		}
		nodes = plan["nodes"].([]object)
		if nodes[0].str("group") != "" || nodes[1].str("group") != "Protobuf 迁移" {
			t.Fatalf("unexpected updated groups: %+v", nodes)
		}

		if _, err := repo.RevertPlanRevision(ctx, project.ID, clearRev.str("revisionId"), "coordinator", "恢复分组"); err != nil {
			t.Fatal(err)
		}
		plan, err = repo.GetPlan(ctx, project.ID)
		if err != nil {
			t.Fatal(err)
		}
		nodes = plan["nodes"].([]object)
		if nodes[0].str("group") != "Protobuf 迁移" {
			t.Fatalf("reverted node did not restore group: %+v", nodes[0])
		}
	}
