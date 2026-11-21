package runner

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/core"
)

func TestCoordinatorCannotRenameProject(t *testing.T) {
	ctx := t.Context()
	repo, err := core.Open(ctx, core.Options{Path: filepath.Join(t.TempDir(), "test.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	project, err := repo.CreateProject(ctx, nil, core.ProjectStart{Message: "整理项目需求"})
	if err != nil {
		t.Fatal(err)
	}
	if !project.Unnamed {
		t.Fatal("expected an unnamed project")
	}
	coordinator := &turn{Runner: &Runner{Repository: repo}, conversation: core.ConversationView{
		ID: project.CoordinatorID, ProjectID: project.ID, Role: "coordinator",
	}}
	for _, entry := range coordinator.tools(ctx, project) {
		if entry.spec.Name == "name_project" {
			t.Fatal("the coordinator can still rename a project")
		}
	}
	if strings.Contains(joinSections(coordinator.sections(ctx, project, nil)), "name_project") {
		t.Fatal("the coordinator prompt still asks it to name the project")
	}
}

func TestCoordinatorCompletesPreviouslyPausedDiscussion(t *testing.T) {
	ctx := t.Context()
	repo, err := core.Open(ctx, core.Options{Path: filepath.Join(t.TempDir(), "test.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	project, err := repo.CreateProject(ctx, new("Fixture"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "交付"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.RecordWorkspace(ctx, thread.ID, "workspace", new("work/delivery"), new("main")); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ReportThread(ctx, thread.ID, core.ThreadReport{Kind: "ready", Summary: "已交到预发布分支", Headline: "预发布交付完成"}, nil, core.ReviewOptions{}); err != nil {
		t.Fatal(err)
	}
	coordinator := &turn{Runner: &Runner{Repository: repo}, conversation: core.ConversationView{
		ID: project.CoordinatorID, ProjectID: project.ID, Role: "coordinator",
	}}
	tools := map[string]tool{}
	for _, item := range coordinator.coordinatorTools() {
		tools[item.spec.Name] = item
	}
	input := args{"threadId": thread.ID}
	complete := tools["complete_thread"]
	if complete.run == nil {
		t.Fatal("coordinator has no completion tool")
	}
	if _, err := complete.run(ctx, input); err == nil {
		t.Fatal("completed a thread without a user request")
	}
	if _, err := tools["stop_thread"].run(ctx, args{"threadId": thread.ID, "reason": "待归档"}); err != nil {
		t.Fatal(err)
	}
	paused, err := repo.GetThread(ctx, thread.ID)
	if err != nil || paused.Stage != "stopped" {
		t.Fatalf("pause did not preserve its own stage: %+v, %v", paused, err)
	}
	if _, err := repo.SetAside(ctx, thread.ID, 1, "稍后继续", project.CoordinatorID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SetWait(ctx, thread.ID, time.Now().Add(time.Hour).UnixMilli(), "检查远程状态"); err != nil {
		t.Fatal(err)
	}
	if err := repo.RequestTurn(ctx, thread.ID); err != nil {
		t.Fatal(err)
	}
	coordinator.userSpoke = true
	result, err := complete.run(ctx, input)
	if err != nil || !strings.Contains(result, `"stage":"done"`) {
		t.Fatalf("completion result = %s, %v", result, err)
	}
	finished, err := repo.GetThread(ctx, thread.ID)
	if err != nil || finished.Stage != "done" || finished.Stopped != nil || finished.Aside != nil ||
		finished.Workspace == nil || finished.Workspace.Removed || finished.Workspace.Branch != "work/delivery" || finished.Merged != nil || finished.LastSummary != paused.LastSummary {
		t.Fatalf("completion changed the delivery or kept paused state: %+v, %v", finished, err)
	}
	conversation, err := repo.GetConversation(ctx, thread.ID)
	if err != nil || conversation.PendingSeq != 0 || conversation.Wait != nil {
		t.Fatalf("completion left pending work or a timer: %+v, %v", conversation, err)
	}
	if id, err := repo.StartScheduledTurn(ctx, thread.ID, "mock"); err != nil || id != "" {
		t.Fatalf("a stale scheduled turn could restart completed work: %s, %v", id, err)
	}
	if err := repo.CancelBeforeTurn(ctx, thread.ID, false); err != nil {
		t.Fatal(err)
	}
	finished, err = repo.GetThread(ctx, thread.ID)
	if err != nil || finished.Stage != "done" {
		t.Fatalf("a stale cancellation overwrote completion: %+v, %v", finished, err)
	}
	if err := repo.RequestTurn(ctx, thread.ID); err != nil {
		t.Fatal(err)
	}
	if id, err := repo.StartScheduledTurn(ctx, thread.ID, "mock"); err != nil || id == "" {
		t.Fatalf("new work could not start after completion: %s, %v", id, err)
	}
	if _, err := complete.run(ctx, input); err == nil {
		t.Fatal("completion interrupted an active turn")
	}
}

func TestCoordinatorLinkThreadNode(t *testing.T) {
	ctx := t.Context()
	repo, err := core.Open(ctx, core.Options{Path: filepath.Join(t.TempDir(), "link-node.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	project, err := repo.CreateProject(ctx, new("Fixture"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	otherProject, err := repo.CreateProject(ctx, new("Other"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.RevisePlan(ctx, project.ID, core.PlanRevision{Author: "user", Reason: "建立计划", Ops: []map[string]any{
		{"op": "add", "id": "nod_1", "kind": "task", "title": "实现后端", "description": "新增事件和命令", "acceptance": "测试通过"},
		{"op": "add", "id": "nod_2", "kind": "task", "title": "完善前端", "description": "实时同步视图", "acceptance": "视图一致"},
	}}); err != nil {
		t.Fatal(err)
	}
	thread, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "事后绑定测试"})
	if err != nil {
		t.Fatal(err)
	}
	otherThread, err := repo.CreateThread(ctx, otherProject.ID, core.NewThread{Title: "其他项目讨论串"})
	if err != nil {
		t.Fatal(err)
	}

	coordinator := &turn{Runner: &Runner{Repository: repo}, conversation: core.ConversationView{
		ID: project.CoordinatorID, ProjectID: project.ID, Role: "coordinator",
	}}
	tools := map[string]tool{}
	for _, item := range coordinator.tools(ctx, project) {
		tools[item.spec.Name] = item
	}
	linkTool := tools["link_thread_node"]
	if linkTool.run == nil {
		t.Fatal("coordinator missing link_thread_node tool")
	}

	// Invalid arguments return actionable guidance.
	if _, err := linkTool.run(ctx, args{"threadId": "undefined", "nodeId": "nod_1"}); err == nil || !strings.Contains(err.Error(), "list_threads") {
		t.Fatalf("expected list_threads guidance for invalid threadId, got %v", err)
	}
	if _, err := linkTool.run(ctx, args{"threadId": otherThread.ID, "nodeId": "nod_1"}); err == nil || !strings.Contains(err.Error(), "list_threads") {
		t.Fatalf("expected list_threads guidance for cross-project thread, got %v", err)
	}
	if _, err := linkTool.run(ctx, args{"threadId": thread.ID, "nodeId": "nod_missing"}); err == nil || !strings.Contains(err.Error(), "read_plan") {
		t.Fatalf("expected read_plan guidance for missing node, got %v", err)
	}
	if _, err := linkTool.run(ctx, args{"threadId": thread.ID, "nodeId": 123}); err == nil {
		t.Fatal("expected error for non-string nodeId")
	}

	// Bind to nod_1.
	result, err := linkTool.run(ctx, args{"threadId": thread.ID, "nodeId": "nod_1"})
	if err != nil || !strings.Contains(result, `"nodeId":"nod_1"`) {
		t.Fatalf("link result = %s, %v", result, err)
	}

	// list_threads and read_thread expose nodeId, and thread prompt includes the linked plan brief.
	listResult, err := tools["list_threads"].run(ctx, args{})
	if err != nil || !strings.Contains(listResult, `"nodeId":"nod_1"`) {
		t.Fatalf("list_threads = %s, %v", listResult, err)
	}
	readResult, err := tools["read_thread"].run(ctx, args{"threadId": thread.ID})
	if err != nil || !strings.Contains(readResult, `"nodeId":"nod_1"`) {
		t.Fatalf("read_thread = %s, %v", readResult, err)
	}
	worker := &turn{Runner: &Runner{Repository: repo}, conversation: core.ConversationView{
		ID: thread.ID, ProjectID: project.ID, Role: "thread",
	}}
	if promptText := joinSections(worker.sections(ctx, project, nil)); !strings.Contains(promptText, "实现后端") {
		t.Fatalf("thread prompt did not pick up linked plan node: %s", promptText)
	}

	// Rebind to nod_2.
	result, err = linkTool.run(ctx, args{"threadId": thread.ID, "nodeId": "nod_2"})
	if err != nil || !strings.Contains(result, `"nodeId":"nod_2"`) {
		t.Fatalf("rebind result = %s, %v", result, err)
	}
	if promptText := joinSections(worker.sections(ctx, project, nil)); !strings.Contains(promptText, "完善前端") || strings.Contains(promptText, "实现后端") {
		t.Fatalf("thread prompt did not update to new plan node: %s", promptText)
	}

	// Unlink with unlink=true.
	result, err = linkTool.run(ctx, args{"threadId": thread.ID, "nodeId": "nod_2", "unlink": true})
	if err != nil || !strings.Contains(result, `"nodeId":null`) {
		t.Fatalf("unlink result = %s, %v", result, err)
	}
	if promptText := joinSections(worker.sections(ctx, project, nil)); strings.Contains(promptText, "完善前端") {
		t.Fatalf("thread prompt still contains unlinked plan node: %s", promptText)
	}

	// Bind again, then unlink by omitting nodeId.
	if _, err := linkTool.run(ctx, args{"threadId": thread.ID, "nodeId": "nod_1"}); err != nil {
		t.Fatal(err)
	}
	result, err = linkTool.run(ctx, args{"threadId": thread.ID})
	if err != nil || !strings.Contains(result, `"nodeId":null`) {
		t.Fatalf("omitted nodeId unlink result = %s, %v", result, err)
	}
}
