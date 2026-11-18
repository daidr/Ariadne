package runner

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/core"
)

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
