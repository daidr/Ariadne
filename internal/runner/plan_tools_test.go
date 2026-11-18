package runner

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/settings"
)

func TestCoordinatorAddsPlanNodesAndRequestsApprovalForAcceptance(t *testing.T) {
	ctx := t.Context()
	home := t.TempDir()
	repo, err := core.Open(ctx, core.Options{Path: filepath.Join(home, "test.sqlite")})
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
	add := func(title string) args { return args{"op": "add", "ref": "work", "title": title} }
	_, err = turn.updatePlan(ctx, args{"reason": "提出计划", "ops": []any{
		args{"op": "add", "title": "验收", "dependsOn": []any{"work"}}, add("实现接口"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := repo.GetPlan(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := repo.ModelPlan(ctx, project.ID)
	if err != nil || !strings.Contains(mustJSON(t, nodes), "实现接口") {
		t.Fatal("model read did not include the implementation node", plan, err)
	}
	if _, err := turn.updatePlan(ctx, args{"reason": "重复", "ops": []any{add("实 现接口！")}}); err == nil {
		t.Fatal("created a duplicate title")
	}
	_, refs, err := resolvePlanOps([]any{add("另一个任务")})
	if err != nil || refs["work"] == "" {
		t.Fatal("missing temporary node reference", refs, err)
	}
	result, err := turn.updatePlan(ctx, args{"reason": "增加里程碑", "ops": []any{args{"op": "add", "title": "上线前验收", "kind": "milestone"}}})
	if err != nil || !strings.Contains(result, "waiting for the user") {
		t.Fatal(result, err)
	}
	attentions, err := repo.ListOpenAttentions(ctx, project.ID)
	if err != nil || len(attentions) != 1 || !strings.Contains(mustJSON(t, attentions[0]), "新增里程碑「上线前验收」") {
		t.Fatal("approval card missed its change details", attentions, err)
	}
	block := args{"type": "confirm", "title": "需要生成截图吗？"}
	result, err = turn.postUI(ctx, args{"blocks": []any{block}})
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
