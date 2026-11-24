package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/eventstore"
	"github.com/daidr/Ariadne/internal/models"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/internal/trace"
	"github.com/daidr/Ariadne/internal/transcript"
	"github.com/daidr/Ariadne/sdk"
)

func TestCoordinatorCompactionInjectsActiveTodoIntoContextAndNextTurn(t *testing.T) {
	ctx := t.Context()
	home := t.TempDir()
	repo, err := core.Open(ctx, core.Options{Path: filepath.Join(home, "coord-todo.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	project, err := repo.CreateProject(ctx, new("Todo Compaction"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	worker, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "后端优化讨论串"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PostMessage(ctx, project.ID, core.MessageInput{
		ConversationID: project.CoordinatorID, Author: "user", Content: "请跟进两个任务。",
	}); err != nil {
		t.Fatal(err)
	}
	store, err := settings.Open(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatal(err)
	}

	var seenRequests []sdk.Request
	summaries := 0
	requests := 0
	registry := models.New()
	info := sdk.ModelProviderInfo{Kind: "fixture"}
	if _, err := registry.Register(sdk.ModelProvider{Info: info, Stream: func(_ context.Context, _ map[string]any, req sdk.Request) iter.Seq[sdk.Part] {
		return func(yield func(sdk.Part) bool) {
			if req.Conversation == "" && len(req.Tools) == 0 {
				summaries++
				if yield(sdk.Part{Type: "text", Text: "摘要：已安排两个并行任务。"}) {
					yield(sdk.Part{Type: "finish", Reason: "stop"})
				}
				return
			}
			requests++
			seenRequests = append(seenRequests, req)
			switch requests {
			case 1:
				if yield(sdk.Part{
					Type:      "tool-call",
					ID:        "todo-init",
					Name:      "update_todo",
					Arguments: `{"items":[{"label":"跟进后端优化讨论串","status":"waiting","detail":"等待汇报"},{"label":"安排前端验证","status":"next"}]}`,
				}) {
					yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
				}
			case 2, 3, 4:
				// Post ~5.5k tokens per step so total crosses threshold (20_000 * 0.75 = 15_000)
				// after step 4 and the compaction cut lands past todo-init.
				if yield(sdk.Part{
					Type:      "tool-call",
					ID:        fmt.Sprintf("msg-%d", requests),
					Name:      "send_message",
					Arguments: fmt.Sprintf(`{"text":%q}`, strings.Repeat(fmt.Sprintf("update-%d-", requests), 2000)),
				}) {
					yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
				}
			default:
				yield(sdk.Part{Type: "finish", Reason: "stop"})
			}
		}
	}}); err != nil {
		t.Fatal(err)
	}

	resolved := &settings.ResolvedModel{Provider: info, Model: "fixture", Label: "fixture", ContextTokens: new(20_000.0)}
	runCoordinatorTurn := func() {
		t.Helper()
		turnID, err := repo.StartTurn(ctx, project.CoordinatorID, "fixture")
		if err != nil || turnID == "" {
			t.Fatalf("expected coordinator turn, got %q: %v", turnID, err)
		}
		conv, err := repo.GetConversation(ctx, project.CoordinatorID)
		if err != nil {
			t.Fatal(err)
		}
		active := &turn{Runner: &Runner{Repository: repo, Settings: store, Models: registry}, id: turnID, conversation: conv}
		if err := active.run(ctx, resolved); err != nil {
			t.Fatalf("coordinator turn failed: %v", err)
		}
	}

	runCoordinatorTurn()
	if summaries != 1 {
		t.Fatalf("expected exactly 1 compaction, got %d", summaries)
	}

	// Verify the post-compaction step in turn 1 received <current_todo> inside <your_notes>.
	postCompact := seenRequests[len(seenRequests)-1]
	foundSummaryTodo := false
	for _, msg := range postCompact.Messages {
		if msg.Role == "assistant" && strings.Contains(msg.Content, "<your_notes>") &&
			strings.Contains(msg.Content, "<current_todo>") &&
			strings.Contains(msg.Content, "- [waiting] 跟进后端优化讨论串 — 等待汇报") &&
			strings.Contains(msg.Content, "- [next] 安排前端验证") {
			foundSummaryTodo = true
			break
		}
	}
	if !foundSummaryTodo {
		t.Fatalf("post-compaction request missing <current_todo> in summary notes: %+v", postCompact.Messages)
	}

	// Turn 2: a discussion thread reports completion after compaction.
	if _, err := repo.ReportThread(ctx, worker.ID, core.ThreadReport{
		Kind:     "done",
		Headline: "后端优化完成",
		Summary:  "已完成并验证。",
	}, nil, core.ReviewOptions{}); err != nil {
		t.Fatal(err)
	}

	beforeTurn2 := len(seenRequests)
	runCoordinatorTurn()
	if len(seenRequests) <= beforeTurn2 {
		t.Fatalf("expected coordinator to run on thread report")
	}
	turn2Req := seenRequests[beforeTurn2]
	sawTodoInTurn2, sawReportInTurn2 := false, false
	for _, msg := range turn2Req.Messages {
		if msg.Role == "assistant" &&
			strings.Contains(msg.Content, "<current_todo>") &&
			strings.Contains(msg.Content, "- [waiting] 跟进后端优化讨论串 — 等待汇报") &&
			strings.Contains(msg.Content, "- [next] 安排前端验证") {
			sawTodoInTurn2 = true
		}
		if msg.Role == "user" && strings.Contains(msg.Content, "<thread_report") && strings.Contains(msg.Content, "后端优化完成") {
			sawReportInTurn2 = true
		}
	}
	if !sawTodoInTurn2 || !sawReportInTurn2 {
		t.Fatalf("turn 2 after compaction should see both <current_todo> (%v) and <thread_report> (%v): %+v", sawTodoInTurn2, sawReportInTurn2, turn2Req.Messages)
	}

	// Verify trace.RebuildRequest reproduces the exact <current_todo> block.
	events, err := repo.Store.Read(ctx, eventstore.ReadOptions{Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	var lastStepSeq int64
	for _, ev := range events {
		if ev.Type != "turn.step" {
			continue
		}
		var body map[string]any
		if json.Unmarshal(ev.Data, &body) == nil && body["conversationId"] == project.CoordinatorID {
			lastStepSeq = ev.Seq
		}
	}
	if lastStepSeq == 0 {
		t.Fatal("expected recorded coordinator turn.step event")
	}
	rebuilt, _, err := trace.RebuildRequest(ctx, repo, project.CoordinatorID, lastStepSeq)
	if err != nil {
		t.Fatalf("trace.RebuildRequest failed: %v", err)
	}
	foundInTrace := false
	for _, msg := range rebuilt.Messages {
		if msg.Role == "assistant" && strings.Contains(msg.Content, "<current_todo>") && strings.Contains(msg.Content, "跟进后端优化讨论串") {
			foundInTrace = true
			break
		}
	}
	if !foundInTrace {
		t.Fatalf("rebuilt trace request missing <current_todo>: %+v", rebuilt.Messages)
	}
}

func TestThreadCompactionInjectsTodoAndOmitsWhenEmpty(t *testing.T) {
	runCase := func(t *testing.T, clearBeforeCompact bool) []sdk.Message {
		t.Helper()
		ctx := t.Context()
		home := t.TempDir()
		repo, err := core.Open(ctx, core.Options{Path: filepath.Join(home, "thread-todo.sqlite")})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = repo.Close() })
		project, err := repo.CreateProject(ctx, new("Thread Compaction"), core.ProjectStart{})
		if err != nil {
			t.Fatal(err)
		}
		thread, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "线程任务"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repo.PostMessage(ctx, project.ID, core.MessageInput{
			ConversationID: thread.ID, Author: "user", Content: "请按步骤执行。",
		}); err != nil {
			t.Fatal(err)
		}
		store, err := settings.Open(filepath.Join(home, "config.json"))
		if err != nil {
			t.Fatal(err)
		}

		var postCompactMessages []sdk.Message
		summaries := 0
		requests := 0
		registry := models.New()
		info := sdk.ModelProviderInfo{Kind: "fixture"}
		if _, err := registry.Register(sdk.ModelProvider{Info: info, Stream: func(_ context.Context, _ map[string]any, req sdk.Request) iter.Seq[sdk.Part] {
			return func(yield func(sdk.Part) bool) {
				if req.Conversation == "" && len(req.Tools) == 0 {
					summaries++
					if yield(sdk.Part{Type: "text", Text: "线程阶段摘要。"}) {
						yield(sdk.Part{Type: "finish", Reason: "stop"})
					}
					return
				}
				requests++
				if summaries > 0 && postCompactMessages == nil {
					postCompactMessages = req.Messages
				}
				switch requests {
				case 1:
					args := `{"items":[{"label":"排查代码","status":"done"},{"label":"修改实现","status":"doing","detail":"处理 runner.go"},{"label":"运行测试","status":"next"}]}`
					if clearBeforeCompact {
						args = `{"items":[]}`
					}
					if yield(sdk.Part{Type: "tool-call", ID: "todo-1", Name: "update_todo", Arguments: args}) {
						yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
					}
				case 2, 3, 4:
					if yield(sdk.Part{
						Type:      "tool-call",
						ID:        fmt.Sprintf("msg-%d", requests),
						Name:      "send_message",
						Arguments: fmt.Sprintf(`{"text":%q}`, strings.Repeat(fmt.Sprintf("log-%d-", requests), 3000)),
					}) {
						yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
					}
				default:
					yield(sdk.Part{Type: "finish", Reason: "stop"})
				}
			}
		}}); err != nil {
			t.Fatal(err)
		}

		turnID, err := repo.StartTurn(ctx, thread.ID, "fixture")
		if err != nil || turnID == "" {
			t.Fatalf("expected thread turn, got %q: %v", turnID, err)
		}
		conv, err := repo.GetConversation(ctx, thread.ID)
		if err != nil {
			t.Fatal(err)
		}
		active := &turn{Runner: &Runner{Repository: repo, Settings: store, Models: registry}, id: turnID, conversation: conv}
		if err := active.run(ctx, &settings.ResolvedModel{Provider: info, Model: "fixture", Label: "fixture", ContextTokens: new(20_000.0)}); err != nil {
			t.Fatalf("thread turn failed: %v", err)
		}
		if summaries != 1 || postCompactMessages == nil {
			t.Fatalf("expected 1 compaction and captured post-compaction messages, got summaries=%d", summaries)
		}
		return postCompactMessages
	}

	t.Run("non-empty thread todo preserved across compaction", func(t *testing.T) {
		msgs := runCase(t, false)
		found := false
		for _, msg := range msgs {
			if msg.Role == "assistant" &&
				strings.Contains(msg.Content, "<current_todo>") &&
				strings.Contains(msg.Content, "- [done] 排查代码") &&
				strings.Contains(msg.Content, "- [doing] 修改实现 — 处理 runner.go") &&
				strings.Contains(msg.Content, "- [next] 运行测试") {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected thread <current_todo> after compaction: %+v", msgs)
		}
	})

	t.Run("empty todo injects nothing", func(t *testing.T) {
		msgs := runCase(t, true)
		for _, msg := range msgs {
			if strings.Contains(msg.Content, "<current_todo>") {
				t.Fatalf("empty todo must not inject <current_todo>: %+v", msg)
			}
		}
	})
}

func TestCompactionStateSkipsWhenSucceededTodoRemainsAfterCut(t *testing.T) {
	ctx := t.Context()
	repo, _, project := openTestEnv(t)
	if _, err := repo.UpdateTodo(ctx, project.ID, project.CoordinatorID, []core.TodoItem{
		{Label: "保持可见的任务", Status: "doing"},
	}); err != nil {
		t.Fatal(err)
	}
	active := &turn{
		Runner:       &Runner{Repository: repo},
		conversation: core.ConversationView{ID: project.CoordinatorID, ProjectID: project.ID, Role: "coordinator"},
	}

	entriesWithTailTodo := []transcript.Entry{
		{"kind": "step", "seq": float64(10), "sawSeq": float64(9), "toolCalls": []any{map[string]any{"id": "todo-tail", "name": "update_todo", "arguments": `{"items":[{"label":"保持可见的任务","status":"doing"}]}`}}},
		{"kind": "tool", "seq": float64(11), "toolCallId": "todo-tail", "name": "update_todo", "content": "Todo list updated.", "isError": false},
	}
	state, err := active.compactionState(ctx, entriesWithTailTodo, 5)
	if err != nil {
		t.Fatal(err)
	}
	if state != "" {
		t.Fatalf("expected empty compaction state when update_todo remains after cut, got %q", state)
	}

	entriesWithFailedTailTodo := []transcript.Entry{
		{"kind": "step", "seq": float64(10), "sawSeq": float64(9), "toolCalls": []any{map[string]any{"id": "todo-fail", "name": "update_todo", "arguments": `{"items":[]}`}}},
		{"kind": "tool", "seq": float64(11), "toolCallId": "todo-fail", "name": "update_todo", "content": "error", "isError": true},
	}
	state, err = active.compactionState(ctx, entriesWithFailedTailTodo, 5)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(state, "<current_todo>") || !strings.Contains(state, "- [doing] 保持可见的任务") {
		t.Fatalf("expected compaction state fallback when tail update_todo failed, got %q", state)
	}
}
