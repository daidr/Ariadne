package runner

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/settings"
)

func TestCoordinatorUpdatesAndRevertsPlanDirectly(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	home := t.TempDir()
	repo, err := core.Open(ctx, core.Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	store, err := settings.Open(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	project, err := repo.CreateProject(ctx, new("Fixture"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	turn := &turn{Runner: &Runner{Repository: repo, Settings: store},
		conversation: core.ConversationView{ID: project.CoordinatorID, ProjectID: project.ID, Role: "coordinator"}}
	decodeReply := func(raw string) map[string]any {
		t.Helper()
		var out map[string]any
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatalf("decode tool reply %q: %v", raw, err)
		}
		return out
	}

	add := func(title string) args {
		return args{"op": "add", "ref": "work", "title": title, "group": "核心改造"}
	}
	initialReply, err := turn.updatePlan(ctx, args{"reason": "提出计划", "ops": []any{
		args{"op": "add", "title": "验收", "group": "核心改造", "dependsOn": []any{"work"}}, add("实现接口"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	initial := decodeReply(initialReply)
	workID := initial["ids"].(map[string]any)["work"].(string)
	plan, err := repo.GetPlan(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := repo.ModelPlan(ctx, project.ID)
	if err != nil || !strings.Contains(mustJSON(t, nodes), "实现接口") || !strings.Contains(mustJSON(t, nodes), "核心改造") {
		t.Fatal("model read did not include the implementation node or group", plan, err)
	}
	if _, err := turn.updatePlan(ctx, args{"reason": "重复", "ops": []any{add("实 现接口！")}}); err == nil {
		t.Fatal("created a duplicate title")
	}
	_, refs, err := resolvePlanOps([]any{add("另一个任务")})
	if err != nil || refs["work"] == "" {
		t.Fatal("missing temporary node reference", refs, err)
	}

	// Adding a milestone, changing acceptance and removing a node apply immediately without approval.
	milestoneReply, err := turn.updatePlan(ctx, args{"reason": "增加里程碑", "ops": []any{args{"op": "add", "ref": "ms", "title": "上线前验收", "kind": "milestone"}}})
	if err != nil {
		t.Fatal(err)
	}
	milestone := decodeReply(milestoneReply)
	if milestone["status"] != "applied" || milestone["revisionId"] == "" {
		t.Fatalf("milestone add did not apply directly: %s", milestoneReply)
	}
	acceptanceReply, err := turn.updatePlan(ctx, args{"reason": "明确验收标准", "ops": []any{args{"op": "update", "id": workID, "acceptance": "单测通过"}}})
	if err != nil || decodeReply(acceptanceReply)["status"] != "applied" {
		t.Fatalf("acceptance update did not apply directly: %s, %v", acceptanceReply, err)
	}
	removeReply, err := turn.updatePlan(ctx, args{"reason": "删除节点", "ops": []any{args{"op": "remove", "id": workID}}})
	if err != nil {
		t.Fatal(err)
	}
	removed := decodeReply(removeReply)
	if removed["status"] != "applied" || removed["revisionId"] == "" {
		t.Fatalf("node removal did not apply directly: %s", removeReply)
	}
	revertReply, err := turn.revertPlan(ctx, args{"revisionId": removed["revisionId"], "reason": "恢复节点"})
	if err != nil {
		t.Fatal(err)
	}
	reverted := decodeReply(revertReply)
	if reverted["status"] != "reverted" || reverted["revisionId"] == "" {
		t.Fatalf("plan revert did not apply directly: %s", revertReply)
	}

	attentions, err := repo.ListOpenAttentions(ctx, project.ID)
	if err != nil || len(attentions) != 0 {
		t.Fatalf("plan updates created unexpected approval attentions: %+v, %v", attentions, err)
	}
	nodes, err = repo.ModelPlan(ctx, project.ID)
	if err != nil || !strings.Contains(mustJSON(t, nodes), "上线前验收") || !strings.Contains(mustJSON(t, nodes), "单测通过") {
		t.Fatalf("model plan missed directly applied changes: %+v, %v", nodes, err)
	}

	block := args{"type": "confirm", "title": "需要生成截图吗？"}
	result, err := turn.postUI(ctx, args{"blocks": []any{block}})
	if err != nil || !strings.Contains(result, "attentionId") {
		t.Fatal("interactive card did not reference the pending answer", result, err)
	}
	for _, item := range turn.tools(ctx, project) {
		if item.spec.Name == "set_delivery" {
			if _, err := item.run(ctx, args{"mode": "external"}); err == nil {
				t.Fatal("saved delivery from a turn without user input")
			}
			return
		}
	}
	t.Fatal("coordinator lost the delivery configuration tool")
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	result, err := reply(value, nil)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
