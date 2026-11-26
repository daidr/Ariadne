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
	"github.com/daidr/Ariadne/internal/models"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/sdk"
)

type recordedToolResult struct {
	name    string
	content string
	isError bool
}

func runToolTurn(
	t *testing.T,
	repo *core.Repository,
	store *settings.Store,
	conv core.ConversationView,
	external []tool,
	stream func(req sdk.Request) iter.Seq[sdk.Part],
) []recordedToolResult {
	t.Helper()
	ctx := t.Context()
	beforeSeq, err := repo.Store.LastSeq(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PostMessage(ctx, conv.ProjectID, core.MessageInput{
		ConversationID: conv.ID, Author: "user", Content: "请处理",
	}); err != nil {
		t.Fatal(err)
	}
	turnID, err := repo.StartScheduledTurn(ctx, conv.ID, "mock")
	if err != nil || turnID == "" {
		t.Fatalf("StartScheduledTurn: id=%q err=%v", turnID, err)
	}

	const providerName = "mock"
	info := sdk.ModelProviderInfo{Kind: providerName, Program: true}
	registry := models.New()
	if _, err := registry.Register(sdk.ModelProvider{
		Info: info,
		Stream: func(_ context.Context, _ map[string]any, req sdk.Request) iter.Seq[sdk.Part] {
			return stream(req)
		},
	}); err != nil {
		t.Fatal(err)
	}

	active := &turn{
		Runner:       &Runner{Repository: repo, Settings: store, Models: registry},
		id:           turnID,
		external:     external,
		conversation: conv,
	}
	model := &settings.ResolvedModel{Provider: info, Model: providerName, Label: providerName}
	if err := active.run(ctx, model); err != nil {
		t.Fatalf("turn.run failed: %v", err)
	}

	entries, err := repo.ListTranscript(ctx, conv.ID, core.TranscriptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var out []recordedToolResult
	for _, entry := range entries {
		if entry["kind"] != "tool" || asNumber(entry["seq"]) <= float64(beforeSeq) {
			continue
		}
		isErr, _ := entry["isError"].(bool)
		out = append(out, recordedToolResult{
			name:    text(entry, "name"),
			content: text(entry, "content"),
			isError: isErr,
		})
	}
	return out
}

func openTestEnv(t *testing.T) (*core.Repository, *settings.Store, core.ProjectView) {
	t.Helper()
	ctx := t.Context()
	home := t.TempDir()
	repo, err := core.Open(ctx, core.Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	store, err := settings.Open(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	project, err := repo.CreateProject(ctx, new("Test Project"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	return repo, store, project
}

func makeCall(id, name string, input args) sdk.Part {
	data, err := json.Marshal(input)
	if err != nil {
		panic(err)
	}
	return sdk.Part{Type: "tool-call", ID: id, Name: name, Arguments: string(data)}
}

func TestCoordinatorLooksHintAndRefuse(t *testing.T) {
	t.Parallel()
	repo, store, project := openTestEnv(t)
	conv := core.ConversationView{ID: project.CoordinatorID, ProjectID: project.ID, Role: "coordinator"}

	totalSteps := coordinatorLookLimit + 3
	step := 0
	results := runToolTurn(t, repo, store, conv, nil, func(_ sdk.Request) iter.Seq[sdk.Part] {
		current := step
		step++
		return func(yield func(sdk.Part) bool) {
			if current < totalSteps {
				if !yield(makeCall(fmt.Sprintf("c%d", current), "fetch_url", args{"url": fmt.Sprintf("http://127.0.0.1/%d", current)})) {
					return
				}
				yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
				return
			}
			yield(sdk.Part{Type: "finish", Reason: "stop"})
		}
	})

	if len(results) != totalSteps {
		t.Fatalf("got %d tool results, want %d", len(results), totalSteps)
	}
	var hintedAt, refusedAt []int
	for i, res := range results {
		if strings.Contains(res.content, coordinatorLookHintText) {
			hintedAt = append(hintedAt, i)
		}
		if strings.Contains(res.content, coordinatorLookRefusedText) {
			if !res.isError {
				t.Errorf("refused result at index %d should have isError=true", i)
			}
			refusedAt = append(refusedAt, i)
		}
	}
	if len(hintedAt) != 1 || hintedAt[0] != coordinatorLookHint-1 {
		t.Fatalf("hinted indices = %v, want [%d]", hintedAt, coordinatorLookHint-1)
	}
	wantRefused := []int{coordinatorLookLimit, coordinatorLookLimit + 1, coordinatorLookLimit + 2}
	if len(refusedAt) != len(wantRefused) || refusedAt[0] != wantRefused[0] || refusedAt[2] != wantRefused[2] {
		t.Fatalf("refused indices = %v, want %v", refusedAt, wantRefused)
	}
}

func TestCoordinatorLooksIncludesBrowser(t *testing.T) {
	t.Parallel()
	repo, store, project := openTestEnv(t)
	conv := core.ConversationView{ID: project.CoordinatorID, ProjectID: project.ID, Role: "coordinator"}

	browserRuns := 0
	fakeBrowser := tool{
		spec: sdk.Tool{Name: "browser"},
		run: func(_ context.Context, input args) (string, error) {
			browserRuns++
			return fmt.Sprintf("browser ok %v", input["url"]), nil
		},
	}

	totalSteps := coordinatorLookLimit + 2
	step := 0
	results := runToolTurn(t, repo, store, conv, []tool{fakeBrowser}, func(_ sdk.Request) iter.Seq[sdk.Part] {
		current := step
		step++
		return func(yield func(sdk.Part) bool) {
			if current < totalSteps {
				if !yield(makeCall(fmt.Sprintf("b%d", current), "browser", args{"action": "open", "url": fmt.Sprintf("https://example.com/%d", current)})) {
					return
				}
				yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
				return
			}
			yield(sdk.Part{Type: "finish", Reason: "stop"})
		}
	})

	if browserRuns != coordinatorLookLimit {
		t.Fatalf("browser ran %d times, want %d (refused calls must not execute)", browserRuns, coordinatorLookLimit)
	}
	if !strings.Contains(results[coordinatorLookHint-1].content, coordinatorLookHintText) {
		t.Fatalf("step %d did not receive soft hint: %q", coordinatorLookHint, results[coordinatorLookHint-1].content)
	}
	for i := coordinatorLookLimit; i < totalSteps; i++ {
		if !results[i].isError || results[i].content != coordinatorLookRefusedText {
			t.Fatalf("step %d = (%q, err=%v), want refused error", i+1, results[i].content, results[i].isError)
		}
	}
}

func TestCoordinatorLooksResetOnHandover(t *testing.T) {
	t.Parallel()
	for _, handover := range []string{"create_thread", "message_thread"} {
		t.Run(handover, func(t *testing.T) {
			repo, store, project := openTestEnv(t)
			conv := core.ConversationView{ID: project.CoordinatorID, ProjectID: project.ID, Role: "coordinator"}
			var existingThreadID string
			if handover == "message_thread" {
				thread, err := repo.CreateThread(t.Context(), project.ID, core.NewThread{Title: "既有讨论串"})
				if err != nil {
					t.Fatal(err)
				}
				existingThreadID = thread.ID
			}

			// 11 looks (triggers hint at #6), then handover (resets count & hint flag), then 7 looks (triggers hint again at #6 after handover, no refusal).
			handoverStep := coordinatorLookLimit - 1
			totalSteps := handoverStep + 1 + coordinatorLookHint + 1
			step := 0
			results := runToolTurn(t, repo, store, conv, nil, func(_ sdk.Request) iter.Seq[sdk.Part] {
				current := step
				step++
				return func(yield func(sdk.Part) bool) {
					if current >= totalSteps {
						yield(sdk.Part{Type: "finish", Reason: "stop"})
						return
					}
					if current == handoverStep {
						if handover == "create_thread" {
							yield(makeCall("handover", "create_thread", args{"title": "调研", "brief": "继续排查"}))
						} else {
							yield(makeCall("handover", "message_thread", args{"threadId": existingThreadID, "content": "补充上下文"}))
						}
					} else {
						yield(makeCall(fmt.Sprintf("c%d", current), "fetch_url", args{"url": fmt.Sprintf("http://127.0.0.1/%d", current)}))
					}
					yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
				}
			})

			var hintedAt []int
			for i, res := range results {
				if strings.Contains(res.content, coordinatorLookRefusedText) {
					t.Fatalf("unexpected refusal at index %d: %q", i, res.content)
				}
				if strings.Contains(res.content, coordinatorLookHintText) {
					hintedAt = append(hintedAt, i)
				}
			}
			wantHints := []int{coordinatorLookHint - 1, handoverStep + coordinatorLookHint}
			if len(hintedAt) != 2 || hintedAt[0] != wantHints[0] || hintedAt[1] != wantHints[1] {
				t.Fatalf("hinted indices = %v, want %v", hintedAt, wantHints)
			}
			threads, err := repo.ListThreads(t.Context(), project.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(threads) != 1 {
				t.Fatalf("threads count = %d, want 1", len(threads))
			}
		})
	}
}

func TestCoordinatorLooksResetInSameStepAsHandover(t *testing.T) {
	t.Parallel()
	repo, store, project := openTestEnv(t)
	conv := core.ConversationView{ID: project.CoordinatorID, ProjectID: project.ID, Role: "coordinator"}

	step := 0
	results := runToolTurn(t, repo, store, conv, nil, func(_ sdk.Request) iter.Seq[sdk.Part] {
		current := step
		step++
		return func(yield func(sdk.Part) bool) {
			switch {
			case current < coordinatorLookLimit:
				yield(makeCall(fmt.Sprintf("c%d", current), "fetch_url", args{"url": fmt.Sprintf("http://127.0.0.1/%d", current)}))
				yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
			case current == coordinatorLookLimit:
				// Same step hands over and looks for the next task; the look must not be refused.
				yield(makeCall("look_same_step", "fetch_url", args{"url": "http://127.0.0.1/after"}))
				yield(makeCall("handover_same_step", "create_thread", args{"title": "新任务", "brief": "去查"}))
				yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
			default:
				yield(sdk.Part{Type: "finish", Reason: "stop"})
			}
		}
	})

	for i, res := range results {
		if strings.Contains(res.content, coordinatorLookRefusedText) {
			t.Fatalf("unexpected refusal at index %d: %q", i, res.content)
		}
	}
}

func TestResearchToolRegisteredOnlyForThreads(t *testing.T) {
	t.Parallel()
	hasTool := func(tools []sdk.Tool, name string) bool {
		for _, item := range tools {
			if item.Name == name {
				return true
			}
		}
		return false
	}

	t.Run("coordinator does not register research and rejects calls to it", func(t *testing.T) {
		repo, store, project := openTestEnv(t)
		conv := core.ConversationView{ID: project.CoordinatorID, ProjectID: project.ID, Role: "coordinator"}

		var sawTools []sdk.Tool
		researchCalls := 0
		step := 0
		results := runToolTurn(t, repo, store, conv, nil, func(req sdk.Request) iter.Seq[sdk.Part] {
			if strings.Contains(req.Conversation, "/research/") {
				researchCalls++
				return func(yield func(sdk.Part) bool) {
					yield(sdk.Part{Type: "text", Text: "unexpected"})
					yield(sdk.Part{Type: "finish", Reason: "stop"})
				}
			}
			if step == 0 {
				sawTools = append(sawTools, req.Tools...)
			}
			current := step
			step++
			return func(yield func(sdk.Part) bool) {
				if current == 0 {
					yield(makeCall("r0", "research", args{"question": "Check repository"}))
					yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
					return
				}
				yield(sdk.Part{Type: "finish", Reason: "stop"})
			}
		})

		if hasTool(sawTools, "research") {
			t.Fatalf("coordinator tools should not include research")
		}
		if researchCalls != 0 {
			t.Fatalf("research provider ran %d times for coordinator, want 0", researchCalls)
		}
		if len(results) != 1 || !results[0].isError || !strings.Contains(results[0].content, `tool "research" is unavailable`) {
			t.Fatalf("coordinator research call result = %+v, want unavailable error", results)
		}
	})

	t.Run("thread keeps research unless disabled in project settings", func(t *testing.T) {
		repo, store, project := openTestEnv(t)
		thread, err := repo.CreateThread(t.Context(), project.ID, core.NewThread{Title: "调研讨论串"})
		if err != nil {
			t.Fatal(err)
		}
		conv := core.ConversationView{ID: thread.ID, ProjectID: project.ID, Role: "thread"}

		var sawEnabledTools []sdk.Tool
		researchCalls := 0
		step := 0
		results := runToolTurn(t, repo, store, conv, nil, func(req sdk.Request) iter.Seq[sdk.Part] {
			if strings.Contains(req.Conversation, "/research/") {
				researchCalls++
				return func(yield func(sdk.Part) bool) {
					yield(sdk.Part{Type: "text", Text: "Thread conclusion."})
					yield(sdk.Part{Type: "finish", Reason: "stop"})
				}
			}
			if step == 0 {
				sawEnabledTools = append(sawEnabledTools, req.Tools...)
			}
			current := step
			step++
			return func(yield func(sdk.Part) bool) {
				if current == 0 {
					yield(makeCall("r0", "research", args{"question": "Inspect details"}))
					yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
					return
				}
				yield(sdk.Part{Type: "finish", Reason: "stop"})
			}
		})

		if !hasTool(sawEnabledTools, "research") {
			t.Fatalf("thread tools should include research when enabled")
		}
		if researchCalls != 1 {
			t.Fatalf("research provider ran %d times for thread, want 1", researchCalls)
		}
		if len(results) != 1 || results[0].isError || !strings.Contains(results[0].content, "Thread conclusion.") {
			t.Fatalf("thread research call result = %+v, want success conclusion", results)
		}

		if _, err := repo.SetProjectSettings(t.Context(), project.ID, map[string]any{"tools": []any{"research"}}, []string{"fetch", "search", "commands", "research", "issueWrites"}); err != nil {
			t.Fatal(err)
		}
		var sawDisabledTools []sdk.Tool
		runToolTurn(t, repo, store, conv, nil, func(req sdk.Request) iter.Seq[sdk.Part] {
			sawDisabledTools = append(sawDisabledTools, req.Tools...)
			return func(yield func(sdk.Part) bool) {
				yield(sdk.Part{Type: "finish", Reason: "stop"})
			}
		})
		if hasTool(sawDisabledTools, "research") {
			t.Fatalf("thread tools should omit research when disabled in project settings")
		}
	})
}

func TestThreadNotLimitedByCoordinatorLooks(t *testing.T) {
	t.Parallel()
	repo, store, project := openTestEnv(t)
	thread, err := repo.CreateThread(t.Context(), project.ID, core.NewThread{Title: "调研讨论串"})
	if err != nil {
		t.Fatal(err)
	}
	conv := core.ConversationView{ID: thread.ID, ProjectID: project.ID, Role: "thread"}

	totalSteps := coordinatorLookLimit + 3
	step := 0
	results := runToolTurn(t, repo, store, conv, nil, func(_ sdk.Request) iter.Seq[sdk.Part] {
		current := step
		step++
		return func(yield func(sdk.Part) bool) {
			if current < totalSteps {
				yield(makeCall(fmt.Sprintf("c%d", current), "fetch_url", args{"url": fmt.Sprintf("http://127.0.0.1/%d", current)}))
				yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
				return
			}
			yield(sdk.Part{Type: "finish", Reason: "stop"})
		}
	})

	for i, res := range results {
		if strings.Contains(res.content, coordinatorLookHintText) || strings.Contains(res.content, coordinatorLookRefusedText) {
			t.Fatalf("thread should not receive coordinator look hints/refusals at index %d: %q", i, res.content)
		}
	}
}
