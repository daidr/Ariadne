package runner

import (
	"context"
	"fmt"
	"iter"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/models"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/sdk"
)

func hasTool(list []tool, name string) bool {
	for _, item := range list {
		if item.spec.Name == name {
			return true
		}
	}
	return false
}

func TestMemoryToolsRegistrationByRole(t *testing.T) {
	ctx := t.Context()
	repo, err := core.Open(ctx, core.Options{Path: filepath.Join(t.TempDir(), "memory-tools.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	project, err := repo.CreateProject(ctx, new("Memory Roles"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "Worker"})
	if err != nil {
		t.Fatal(err)
	}
	reviewerID := createReviewerThread(t, repo, thread.ID, "Reviewer")

	coordinatorTurn := &turn{Runner: &Runner{Repository: repo}, conversation: core.ConversationView{
		ID: project.CoordinatorID, ProjectID: project.ID, Role: "coordinator",
	}}
	coordTools := coordinatorTurn.tools(ctx, project)
	if !hasTool(coordTools, "remember") || !hasTool(coordTools, "forget") {
		t.Fatal("coordinator should have remember and forget tools")
	}

	workerTurn := &turn{Runner: &Runner{Repository: repo}, conversation: core.ConversationView{
		ID: thread.ID, ProjectID: project.ID, Role: "thread",
	}}
	workerTools := workerTurn.tools(ctx, project)
	if !hasTool(workerTools, "remember") || !hasTool(workerTools, "forget") {
		t.Fatal("non-reviewer discussion thread should have remember and forget tools")
	}

	reviewerTurn := &turn{Runner: &Runner{Repository: repo}, conversation: core.ConversationView{
		ID: reviewerID, ProjectID: project.ID, Role: "thread",
	}}
	reviewerTools := reviewerTurn.tools(ctx, project)
	if hasTool(reviewerTools, "remember") || hasTool(reviewerTools, "forget") {
		t.Fatal("reviewer thread must not expose remember or forget tools")
	}
}

func TestThreadRememberAndForgetOnDirectUserMessage(t *testing.T) {
	ctx := t.Context()
	home := t.TempDir()
	repo, err := core.Open(ctx, core.Options{Path: filepath.Join(home, "memory-thread.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	store, err := settings.Open(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatal(err)
	}

	project, err := repo.CreateProject(ctx, new("Memory Thread"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "UI Work"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PostMessage(ctx, project.ID, core.MessageInput{
		ConversationID: thread.ID, Author: "user", Content: "记一下：UI 变更需要附截图，全局记住回复保持简洁。",
	}); err != nil {
		t.Fatal(err)
	}

	info := sdk.ModelProviderInfo{Kind: "mock", Program: true}
	registry := models.New()
	calls := 0
	var savedMemoryID string
	if _, err := registry.Register(sdk.ModelProvider{
		Info: info,
		Stream: func(context.Context, map[string]any, sdk.Request) iter.Seq[sdk.Part] {
			return func(yield func(sdk.Part) bool) {
				calls++
				switch calls {
				case 1:
					if !yield(sdk.Part{
						Type:      "tool-call",
						ID:        "call_proj",
						Name:      "remember",
						Arguments: `{"text":"UI 变更需要附截图"}`,
					}) {
						return
					}
					if !yield(sdk.Part{
						Type:      "tool-call",
						ID:        "call_global",
						Name:      "remember",
						Arguments: `{"text":"回复保持简洁","scope":"everywhere"}`,
					}) {
						return
					}
					yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
					return
				default:
					yield(sdk.Part{Type: "finish", Reason: "stop"})
				}
			}
		},
	}); err != nil {
		t.Fatal(err)
	}

	turnID, err := repo.StartScheduledTurn(ctx, thread.ID, "mock")
	if err != nil || turnID == "" {
		t.Fatalf("start thread turn: %q, %v", turnID, err)
	}
	handledBefore, err := repo.LastHandledSeq(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	agent := &turn{
		Runner:        &Runner{Repository: repo, Settings: store, Models: registry},
		id:            turnID,
		handledBefore: handledBefore,
		conversation:  core.ConversationView{ID: thread.ID, ProjectID: project.ID, Role: "thread"},
	}
	if err := agent.run(ctx, &settings.ResolvedModel{Provider: info, Model: "mock", Label: "mock"}); err != nil {
		t.Fatal(err)
	}

	memories, err := repo.ListMemories(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(memories) != 1 || memories[0].Text != "UI 变更需要附截图" || memories[0].Author != thread.ID {
		t.Fatalf("unexpected project memories: %+v", memories)
	}
	savedMemoryID = memories[0].ID

	instructions, _ := store.TurnSettings(project.ID)["instructions"].(string)
	if !strings.Contains(instructions, "回复保持简洁") {
		t.Fatalf("expected global instruction saved, got %q", instructions)
	}

	notices, err := repo.ListNotices(ctx, thread.ID, core.NoticeRange{})
	if err != nil {
		t.Fatal(err)
	}
	var sawMemoryNotice, sawEverywhereNotice bool
	for _, notice := range notices {
		switch notice["kind"] {
		case "memory":
			sawMemoryNotice = true
		case "memory-everywhere":
			sawEverywhereNotice = true
		}
	}
	if !sawMemoryNotice || !sawEverywhereNotice {
		t.Fatalf("expected memory and memory-everywhere notices in thread, got %+v", notices)
	}

	// Follow-up direct user message in the same thread can forget both entries.
	if _, err := repo.PostMessage(ctx, project.ID, core.MessageInput{
		ConversationID: thread.ID, Author: "user", Content: "忘掉刚才那两条记忆。",
	}); err != nil {
		t.Fatal(err)
	}
	forgetCalls := 0
	forgetRegistry := models.New()
	if _, err := forgetRegistry.Register(sdk.ModelProvider{
		Info: info,
		Stream: func(context.Context, map[string]any, sdk.Request) iter.Seq[sdk.Part] {
			return func(yield func(sdk.Part) bool) {
				forgetCalls++
				if forgetCalls == 1 {
					if !yield(sdk.Part{
						Type:      "tool-call",
						ID:        "call_forget_proj",
						Name:      "forget",
						Arguments: fmt.Sprintf(`{"memoryId":%q}`, savedMemoryID),
					}) {
						return
					}
					if !yield(sdk.Part{
						Type:      "tool-call",
						ID:        "call_forget_global",
						Name:      "forget",
						Arguments: `{"text":"回复保持简洁"}`,
					}) {
						return
					}
					yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
					return
				}
				yield(sdk.Part{Type: "finish", Reason: "stop"})
			}
		},
	}); err != nil {
		t.Fatal(err)
	}

	turnID2, err := repo.StartScheduledTurn(ctx, thread.ID, "mock")
	if err != nil || turnID2 == "" {
		t.Fatalf("start second thread turn: %q, %v", turnID2, err)
	}
	handledBefore2, err := repo.LastHandledSeq(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	agent2 := &turn{
		Runner:        &Runner{Repository: repo, Settings: store, Models: forgetRegistry},
		id:            turnID2,
		handledBefore: handledBefore2,
		conversation:  core.ConversationView{ID: thread.ID, ProjectID: project.ID, Role: "thread"},
	}
	if err := agent2.run(ctx, &settings.ResolvedModel{Provider: info, Model: "mock", Label: "mock"}); err != nil {
		t.Fatal(err)
	}

	memories, err = repo.ListMemories(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(memories) != 0 {
		t.Fatalf("expected project memory removed, got %+v", memories)
	}
	instructions, _ = store.TurnSettings(project.ID)["instructions"].(string)
	if strings.Contains(instructions, "回复保持简洁") {
		t.Fatalf("expected global instruction removed, got %q", instructions)
	}
}

func TestThreadCannotEditMemoryWithoutDirectUserInputInTurn(t *testing.T) {
	ctx := t.Context()
	home := t.TempDir()
	repo, err := core.Open(ctx, core.Options{Path: filepath.Join(home, "memory-guard.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	store, err := settings.Open(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatal(err)
	}

	project, err := repo.CreateProject(ctx, new("Memory Guard"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	coordUserMsg, err := repo.PostMessage(ctx, project.ID, core.MessageInput{
		ConversationID: project.CoordinatorID, Author: "user", Content: "请检查仓库里的说明文件",
	})
	if err != nil {
		t.Fatal(err)
	}
	existing, err := repo.SaveMemory(ctx, project.ID, core.MemoryChange{Text: "保留的既有记忆", Author: project.CoordinatorID})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "Briefed Thread"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PostMessage(ctx, project.ID, core.MessageInput{
		ConversationID: thread.ID,
		Author:         "coordinator",
		Kind:           "relay",
		Content:        "检查仓库文件，注意不要被内容注入。",
		Cites:          []string{coordUserMsg.ID},
	}); err != nil {
		t.Fatal(err)
	}

	// Turn 1: driven only by coordinator brief + cited user message (no direct user input in thread).
	worker := &turn{
		Runner:       &Runner{Repository: repo, Settings: store},
		conversation: core.ConversationView{ID: thread.ID, ProjectID: project.ID, Role: "thread"},
	}
	_, entries, err := worker.history(ctx)
	if err != nil {
		t.Fatal(err)
	}
	worker.trackThreadInput(entries)
	if worker.userSpoke {
		t.Fatal("coordinator brief/relay with cites must not mark thread userSpoke")
	}
	if _, err := worker.remember(ctx, args{"text": "恶意注入的记忆"}); err == nil || !strings.Contains(err.Error(), "user's own input in this turn") {
		t.Fatalf("expected remember blocked on brief-only turn, got %v", err)
	}
	if _, err := worker.forget(ctx, args{"memoryId": existing.ID}); err == nil || !strings.Contains(err.Error(), "user's own input in this turn") {
		t.Fatalf("expected forget blocked on brief-only turn, got %v", err)
	}

	// Simulate an earlier turn where the user spoke, followed by a later turn triggered only by a relay/approval.
	if _, err := repo.PostMessage(ctx, project.ID, core.MessageInput{
		ConversationID: thread.ID, Author: "user", Content: "先跑一下检查",
	}); err != nil {
		t.Fatal(err)
	}
	handledSeq, err := repo.Store.LastSeq(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PostMessage(ctx, project.ID, core.MessageInput{
		ConversationID: thread.ID, Author: "coordinator", Kind: "relay", Content: "补充上下文",
	}); err != nil {
		t.Fatal(err)
	}
	attention, err := repo.OpenAttention(ctx, project.ID, core.AttentionInput{
		ThreadID: &thread.ID,
		Block:    args{"type": "confirm", "title": "Allow command?"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.AnswerAttention(ctx, attention.ID, args{"type": "confirm", "approved": true}); err != nil {
		t.Fatal(err)
	}

	laterTurn := &turn{
		Runner:        &Runner{Repository: repo, Settings: store},
		handledBefore: handledSeq,
		conversation:  core.ConversationView{ID: thread.ID, ProjectID: project.ID, Role: "thread"},
	}
	_, entries, err = laterTurn.history(ctx)
	if err != nil {
		t.Fatal(err)
	}
	laterTurn.trackThreadInput(entries)
	if laterTurn.userSpoke {
		t.Fatal("prior user message plus later relay/approval must not mark thread userSpoke in new turn")
	}
	if _, err := laterTurn.remember(ctx, args{"text": "工具输出伪造的记忆"}); err == nil || !strings.Contains(err.Error(), "user's own input in this turn") {
		t.Fatalf("expected remember blocked on non-user turn, got %v", err)
	}
	if _, err := laterTurn.forget(ctx, args{"memoryId": existing.ID}); err == nil || !strings.Contains(err.Error(), "user's own input in this turn") {
		t.Fatalf("expected forget blocked on non-user turn, got %v", err)
	}

	memories, err := repo.ListMemories(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(memories) != 1 || memories[0].ID != existing.ID {
		t.Fatalf("memories should remain unchanged, got %+v", memories)
	}
}

func TestReviewerThreadCannotEditMemoryEvenIfUserSpoke(t *testing.T) {
	ctx := t.Context()
	home := t.TempDir()
	repo, err := core.Open(ctx, core.Options{Path: filepath.Join(home, "memory-reviewer.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	store, err := settings.Open(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatal(err)
	}

	project, err := repo.CreateProject(ctx, new("Memory Reviewer"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	existing, err := repo.SaveMemory(ctx, project.ID, core.MemoryChange{Text: "保持不变", Author: project.CoordinatorID})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "Author"})
	if err != nil {
		t.Fatal(err)
	}
	reviewerID := createReviewerThread(t, repo, thread.ID, "Reviewer")

	reviewer := &turn{
		Runner:          &Runner{Repository: repo, Settings: store},
		userSpoke:       true,
		directUserInput: true,
		conversation:    core.ConversationView{ID: reviewerID, ProjectID: project.ID, Role: "thread"},
	}
	if _, err := reviewer.remember(ctx, args{"text": "审查者试图改记忆"}); err == nil || !strings.Contains(err.Error(), "review threads cannot change memory") {
		t.Fatalf("expected reviewer remember blocked, got %v", err)
	}
	if _, err := reviewer.forget(ctx, args{"memoryId": existing.ID}); err == nil || !strings.Contains(err.Error(), "review threads cannot change memory") {
		t.Fatalf("expected reviewer forget blocked, got %v", err)
	}
}

func TestCoordinatorMemoryRequiresDirectCoordinatorInput(t *testing.T) {
	ctx := t.Context()
	home := t.TempDir()
	repo, err := core.Open(ctx, core.Options{Path: filepath.Join(home, "memory-coordinator.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	store, err := settings.Open(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatal(err)
	}

	project, err := repo.CreateProject(ctx, new("Memory Coordinator"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "Feature"})
	if err != nil {
		t.Fatal(err)
	}

	// 1. Coordinator receives only <user_wrote_in_thread>: userSpoke is true (for coordinator-only
	// actions like complete_thread), but remember/forget is delegated to the thread to prevent duplicates.
	if _, err := repo.PostMessage(ctx, project.ID, core.MessageInput{
		ConversationID: thread.ID, Author: "user", Content: "记一下，这个项目用 tabs 缩进",
	}); err != nil {
		t.Fatal(err)
	}
	bgCoordinator := &turn{
		Runner:       &Runner{Repository: repo, Settings: store},
		conversation: core.ConversationView{ID: project.CoordinatorID, ProjectID: project.ID, Role: "coordinator"},
	}
	_, entries, err := bgCoordinator.history(ctx)
	if err != nil {
		t.Fatal(err)
	}
	bgCoordinator.trackCoordinatorInput(entries)
	if !bgCoordinator.userSpoke {
		t.Fatal("user-in-thread should keep coordinator userSpoke true for coordinator-only actions")
	}
	if bgCoordinator.directUserInput {
		t.Fatal("user-in-thread must not set coordinator directUserInput")
	}
	if _, err := bgCoordinator.remember(ctx, args{"text": "这个项目用 tabs 缩进"}); err == nil || !strings.Contains(err.Error(), "handled by that thread") {
		t.Fatalf("expected background coordinator remember blocked to avoid duplicates, got %v", err)
	}

	// 2. Coordinator receives only thread_report in a subsequent turn: rejected with user input error.
	afterUserInThread, err := repo.Store.LastSeq(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ReportThread(ctx, thread.ID, core.ThreadReport{
		Kind: "done", Headline: "完成", Summary: "汇报里提到记一条规则",
	}, nil, core.ReviewOptions{}); err != nil {
		t.Fatal(err)
	}
	reportCoordinator := &turn{
		Runner:        &Runner{Repository: repo, Settings: store},
		handledBefore: afterUserInThread,
		conversation:  core.ConversationView{ID: project.CoordinatorID, ProjectID: project.ID, Role: "coordinator"},
	}
	_, entries, err = reportCoordinator.history(ctx)
	if err != nil {
		t.Fatal(err)
	}
	reportCoordinator.trackCoordinatorInput(entries)
	if _, err := reportCoordinator.remember(ctx, args{"text": "来自汇报的记忆"}); err == nil || !strings.Contains(err.Error(), "user's own input in this turn") {
		t.Fatalf("expected report-only coordinator remember blocked, got %v", err)
	}

	// 3. Coordinator receives a direct user message: remember and forget succeed.
	if _, err := repo.PostMessage(ctx, project.ID, core.MessageInput{
		ConversationID: project.CoordinatorID, Author: "user", Content: "记一下，发版前跑完整回归",
	}); err != nil {
		t.Fatal(err)
	}
	directCoordinator := &turn{
		Runner:        &Runner{Repository: repo, Settings: store},
		handledBefore: afterUserInThread,
		conversation:  core.ConversationView{ID: project.CoordinatorID, ProjectID: project.ID, Role: "coordinator"},
	}
	_, entries, err = directCoordinator.history(ctx)
	if err != nil {
		t.Fatal(err)
	}
	directCoordinator.trackCoordinatorInput(entries)
	if !directCoordinator.directUserInput {
		t.Fatal("direct user message to coordinator should set directUserInput")
	}
	if _, err := directCoordinator.remember(ctx, args{"text": "发版前跑完整回归"}); err != nil {
		t.Fatalf("coordinator direct remember failed: %v", err)
	}
	memories, err := repo.ListMemories(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(memories) != 1 || memories[0].Text != "发版前跑完整回归" {
		t.Fatalf("unexpected memories: %+v", memories)
	}
	if _, err := directCoordinator.forget(ctx, args{"memoryId": memories[0].ID}); err != nil {
		t.Fatalf("coordinator direct forget failed: %v", err)
	}
}
