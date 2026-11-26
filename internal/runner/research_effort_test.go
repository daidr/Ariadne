package runner

import (
	"context"
	"fmt"
	"iter"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/models"
	"github.com/daidr/Ariadne/internal/projectsettings"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/internal/transcript"
	"github.com/daidr/Ariadne/sdk"
)

func TestResearchAndThreadEffort(t *testing.T) {
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

	var seenRequests []sdk.Request
	modelRegistry := models.New()
	if _, err := modelRegistry.Register(sdk.ModelProvider{
		Info: sdk.ModelProviderInfo{
			Kind:  "anthropic",
			Title: sdk.LocalizedText{"zh-CN": "Anthropic", "en": "Anthropic"},
		},
		Stream: func(_ context.Context, _ map[string]any, req sdk.Request) iter.Seq[sdk.Part] {
			seenRequests = append(seenRequests, req)
			return func(yield func(sdk.Part) bool) {
				yield(sdk.Part{Type: "text", Text: "Verified conclusion."})
			}
		},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := store.UpdateModels(settings.Object{
		"providers": []any{
			settings.Object{"id": "p1", "kind": "anthropic", "name": "Claude", "settings": settings.Object{}},
		},
		"models": []any{
			settings.Object{"id": "m-opus", "provider": "p1", "model": "claude-opus", "efforts": []any{"low", "medium", "high", "max"}},
			settings.Object{"id": "m-haiku", "provider": "p1", "model": "claude-haiku", "efforts": []any{"low", "medium"}},
		},
		"roles": settings.Object{
			"thread":   settings.Object{"model": "m-opus", "effort": "high"},
			"research": settings.Object{"model": nil, "effort": "low"},
		},
	}, modelRegistry.Kinds()); err != nil {
		t.Fatal(err)
	}

	project, err := repo.CreateProject(ctx, new("Effort Test"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "Check effort"})
	if err != nil {
		t.Fatal(err)
	}
	conv, err := repo.GetConversation(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}

	r := &Runner{Repository: repo, Settings: store, Models: modelRegistry}

	// Default thread inherits roles.thread.model and roles.thread.effort.
	resolved, err := r.Resolve(ctx, conv)
	if err != nil || resolved.Model != "claude-opus" || resolved.Effort != "high" {
		t.Fatalf("default Resolve = %+v, err=%v", resolved, err)
	}

	// Thread-level model + effort override applies to Runner.Resolve.
	modelID := "m-haiku"
	effort := "medium"
	if _, err := repo.SetThreadModel(ctx, thread.ID, &modelID, &effort, "user"); err != nil {
		t.Fatal(err)
	}
	resolved, err = r.Resolve(ctx, conv)
	if err != nil || resolved.Model != "claude-haiku" || resolved.Effort != "medium" {
		t.Fatalf("override Resolve = %+v, err=%v", resolved, err)
	}

	// Thread-level model with empty effort uses model default rather than roles.thread.effort.
	opusID := "m-opus"
	if _, err := repo.SetThreadModel(ctx, thread.ID, &opusID, nil, "user"); err != nil {
		t.Fatal(err)
	}
	resolved, err = r.Resolve(ctx, conv)
	if err != nil || resolved.Model != "claude-opus" || resolved.Effort != "" {
		t.Fatalf("explicit model default effort Resolve = %+v, err=%v", resolved, err)
	}

	// Reset to default restores roles.thread.effort.
	if _, err := repo.SetThreadModel(ctx, thread.ID, nil, nil, "user"); err != nil {
		t.Fatal(err)
	}
	parent, err := r.Resolve(ctx, conv)
	if err != nil || parent.Model != "claude-opus" || parent.Effort != "high" {
		t.Fatalf("reset Resolve = %+v, err=%v", parent, err)
	}

	if _, err := repo.PostMessage(ctx, project.ID, core.MessageInput{ConversationID: thread.ID, Author: "user", Content: "check"}); err != nil {
		t.Fatal(err)
	}
	turnID, err := repo.StartScheduledTurn(ctx, thread.ID, parent.Label)
	if err != nil || turnID == "" {
		t.Fatalf("StartScheduledTurn: id=%q err=%v", turnID, err)
	}
	activeTurn := &turn{Runner: r, conversation: conv, id: turnID}
	entry := activeTurn.researchTool(nil, parent, args{"modelRetries": float64(0)})

	// Research with model=null ("same as asking") still applies roles.research.effort ("low").
	if _, err := entry.withCallID(ctx, "call_1", args{"question": "Check effort"}); err != nil {
		t.Fatal(err)
	}
	if len(seenRequests) != 1 || seenRequests[0].Model != "claude-opus" || seenRequests[0].Effort != "low" {
		t.Fatalf("research request (inherit model) = %+v", seenRequests)
	}

	// Research with explicit model + effort uses that model and effort.
	if _, err := store.UpdateModels(settings.Object{
		"providers": []any{
			settings.Object{"id": "p1", "kind": "anthropic", "name": "Claude", "settings": settings.Object{}},
		},
		"models": []any{
			settings.Object{"id": "m-opus", "provider": "p1", "model": "claude-opus", "efforts": []any{"low", "medium", "high", "max"}},
			settings.Object{"id": "m-haiku", "provider": "p1", "model": "claude-haiku", "efforts": []any{"low", "medium"}},
		},
		"roles": settings.Object{
			"thread":   settings.Object{"model": "m-opus", "effort": "high"},
			"research": settings.Object{"model": "m-haiku", "effort": "medium"},
		},
	}, modelRegistry.Kinds()); err != nil {
		t.Fatal(err)
	}
	if _, err := entry.withCallID(ctx, "call_2", args{"question": "Check explicit model"}); err != nil {
		t.Fatal(err)
	}
	if len(seenRequests) != 2 || seenRequests[1].Model != "claude-haiku" || seenRequests[1].Effort != "medium" {
		t.Fatalf("research request (explicit model) = %+v", seenRequests)
	}
}

func TestResearchRoleLimitsAndDescription(t *testing.T) {
	t.Parallel()
	setup := func(t *testing.T, role string, stream func(req sdk.Request) iter.Seq[sdk.Part], available []tool) (tool, *[]sdk.Request) {
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
		var requests []sdk.Request
		info := sdk.ModelProviderInfo{Kind: "mock", Program: true}
		registry := models.New()
		if _, err := registry.Register(sdk.ModelProvider{
			Info: info,
			Stream: func(_ context.Context, _ map[string]any, req sdk.Request) iter.Seq[sdk.Part] {
				captured := req
				captured.Messages = slices.Clone(req.Messages)
				captured.Tools = slices.Clone(req.Tools)
				requests = append(requests, captured)
				return stream(req)
			},
		}); err != nil {
			t.Fatal(err)
		}
		project, err := repo.CreateProject(ctx, new("Research Limits"), core.ProjectStart{})
		if err != nil {
			t.Fatal(err)
		}
		convID := project.CoordinatorID
		if role == "thread" {
			thread, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "Deep Dive"})
			if err != nil {
				t.Fatal(err)
			}
			convID = thread.ID
		}
		if _, err := repo.PostMessage(ctx, project.ID, core.MessageInput{ConversationID: convID, Author: "user", Content: "check"}); err != nil {
			t.Fatal(err)
		}
		turnID, err := repo.StartScheduledTurn(ctx, convID, "mock")
		if err != nil || turnID == "" {
			t.Fatalf("StartScheduledTurn: id=%q err=%v", turnID, err)
		}
		conv := core.ConversationView{ID: convID, ProjectID: project.ID, Role: role}
		parent := &settings.ResolvedModel{Provider: info, Model: "mock", Label: "mock"}
		active := &turn{Runner: &Runner{Repository: repo, Settings: store, Models: registry}, conversation: conv, id: turnID}
		entry := active.researchTool(available, parent, args{"modelRetries": float64(0)})
		return entry, &requests
	}

	t.Run("step caps: coordinator 8 vs thread 24", func(t *testing.T) {
		for _, tc := range []struct {
			role      string
			wantSteps int
		}{
			{role: "coordinator", wantSteps: coordinatorResearchMaxSteps},
			{role: "thread", wantSteps: threadResearchMaxSteps},
		} {
			t.Run(tc.role, func(t *testing.T) {
				readStub := specification("read_file", "read", args{"path": field("string")}, nil, func(_ context.Context, input args) (string, error) {
					return "snippet " + text(input, "path"), nil
				})
				callNum := 0
				entry, requests := setup(t, tc.role, func(req sdk.Request) iter.Seq[sdk.Part] {
					return func(yield func(sdk.Part) bool) {
						if len(req.Tools) == 0 {
							yield(sdk.Part{Type: "text", Text: "Final conclusion."})
							yield(sdk.Part{Type: "finish", Reason: "stop"})
							return
						}
						callNum++
						yield(makeCall(fmt.Sprintf("rc%d", callNum), "read_file", args{"path": fmt.Sprintf("f%d.go", callNum)}))
						yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
					}
				}, []tool{readStub})

				if tc.role == "coordinator" {
					if !strings.Contains(entry.spec.Description, "create_thread") || !strings.Contains(entry.spec.Description, "single-point") {
						t.Errorf("coordinator research description should guide lightweight single-point usage: %q", entry.spec.Description)
					}
				} else if strings.Contains(entry.spec.Description, "create_thread") {
					t.Errorf("thread research description should not mention coordinator create_thread guidance: %q", entry.spec.Description)
				}

				out, err := entry.withCallID(t.Context(), "call_steps", args{"question": "Check cap"})
				if err != nil {
					t.Fatalf("research failed: %v", err)
				}
				if !strings.Contains(out, fmt.Sprintf("[Research: %d requests", tc.wantSteps)) {
					t.Fatalf("output = %q, want %d requests", out, tc.wantSteps)
				}
				if len(*requests) != tc.wantSteps {
					t.Fatalf("saw %d requests, want %d", len(*requests), tc.wantSteps)
				}
				for i := 0; i < tc.wantSteps-1; i++ {
					if len((*requests)[i].Tools) == 0 {
						t.Fatalf("step %d unexpectedly had empty tools", i+1)
					}
				}
				lastReq := (*requests)[tc.wantSteps-1]
				if len(lastReq.Tools) != 0 {
					t.Fatalf("final step %d should have empty tools, got %d", tc.wantSteps, len(lastReq.Tools))
				}
				lastMsg := lastReq.Messages[len(lastReq.Messages)-1]
				if lastMsg.Role != "user" || lastMsg.Content != transcript.FinalResearchPrompt {
					t.Fatalf("final step prompt = %+v, want FinalResearchPrompt", lastMsg)
				}
			})
		}
	})

	t.Run("retained buffer cap: coordinator 512 KiB vs thread 2 MiB", func(t *testing.T) {
		chunk := strings.Repeat("a", 20_000)
		readStub := specification("read_file", "read", args{"path": field("string")}, nil, func(_ context.Context, _ args) (string, error) {
			return chunk, nil
		})

		// 28 calls x 20,000 bytes = 560,000 bytes (> 512 KiB, < 2 MiB).
		runOnce := func(role string) []sdk.Request {
			step := 0
			entry, requests := setup(t, role, func(req sdk.Request) iter.Seq[sdk.Part] {
				step++
				return func(yield func(sdk.Part) bool) {
					if step == 1 {
						for i := 0; i < 28; i++ {
							if !yield(makeCall(fmt.Sprintf("big_%d", i), "read_file", args{"path": fmt.Sprintf("big%d.txt", i)})) {
								return
							}
						}
						yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
						return
					}
					yield(sdk.Part{Type: "text", Text: "Wrapped up."})
					yield(sdk.Part{Type: "finish", Reason: "stop"})
				}
			}, []tool{readStub})
			if _, err := entry.withCallID(t.Context(), "call_buf", args{"question": "Check buffer cap"}); err != nil {
				t.Fatalf("%s research failed: %v", role, err)
			}
			return *requests
		}

		coordReqs := runOnce("coordinator")
		if len(coordReqs) != 2 {
			t.Fatalf("coordinator requests = %d, want 2", len(coordReqs))
		}
		if len(coordReqs[1].Tools) != 0 {
			t.Fatalf("coordinator step 2 should hit 512 KiB cap and clear tools, got %d tools", len(coordReqs[1].Tools))
		}
		if last := coordReqs[1].Messages[len(coordReqs[1].Messages)-1]; last.Content != transcript.FinalResearchPrompt {
			t.Fatalf("coordinator step 2 should append FinalResearchPrompt, got %q", last.Content)
		}

		threadReqs := runOnce("thread")
		if len(threadReqs) != 2 {
			t.Fatalf("thread requests = %d, want 2", len(threadReqs))
		}
		if len(threadReqs[1].Tools) == 0 {
			t.Fatalf("thread step 2 should still have tools under 2 MiB cap")
		}
	})
}

func TestRunnerResolvesProjectModelOverrides(t *testing.T) {
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

	var seenRequests []sdk.Request
	modelRegistry := models.New()
	if _, err := modelRegistry.Register(sdk.ModelProvider{
		Info: sdk.ModelProviderInfo{
			Kind:  "anthropic",
			Title: sdk.LocalizedText{"en": "Anthropic"},
		},
		Stream: func(_ context.Context, _ map[string]any, req sdk.Request) iter.Seq[sdk.Part] {
			seenRequests = append(seenRequests, req)
			return func(yield func(sdk.Part) bool) {
				yield(sdk.Part{Type: "text", Text: "Done."})
				yield(sdk.Part{Type: "finish", Reason: "stop"})
			}
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateModels(settings.Object{
		"providers": []any{
			settings.Object{"id": "p1", "kind": "anthropic", "name": "Claude", "settings": settings.Object{}},
		},
		"models": []any{
			settings.Object{"id": "m-opus", "provider": "p1", "model": "claude-opus", "efforts": []any{"low", "medium", "high", "max"}},
			settings.Object{"id": "m-sonnet", "provider": "p1", "model": "claude-sonnet", "efforts": []any{"low", "medium", "high"}},
			settings.Object{"id": "m-haiku", "provider": "p1", "model": "claude-haiku", "efforts": []any{"low", "medium"}},
		},
		"roles": settings.Object{
			"thread":      settings.Object{"model": "m-sonnet", "effort": "medium"},
			"coordinator": settings.Object{"model": nil, "effort": nil},
			"research":    settings.Object{"model": "m-haiku", "effort": "low"},
		},
	}, modelRegistry.Kinds()); err != nil {
		t.Fatal(err)
	}

	project, err := repo.CreateProject(ctx, new("Specialized Project"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	strPtr := func(v string) *string { return &v }
	nextSettings := project.Settings
	nextSettings.Models = projectsettings.Models{
		Thread:      &projectsettings.ModelRole{Model: strPtr("m-opus"), Effort: strPtr("max")},
		Coordinator: nil, // Inherits project thread (m-opus) with coordinator default low effort.
		Research:    &projectsettings.ModelRole{Model: nil, Effort: strPtr("medium")},
	}
	if _, err := repo.UpdateProject(ctx, project.ID, core.ProjectPatch{
		Settings:    nextSettings,
		HasSettings: true,
	}, nil); err != nil {
		t.Fatal(err)
	}

	thread, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "Scoped task"})
	if err != nil {
		t.Fatal(err)
	}
	r := &Runner{Repository: repo, Settings: store, Models: modelRegistry}

	// Thread uses project thread override.
	threadConv := core.ConversationView{ID: thread.ID, ProjectID: project.ID, Role: "thread"}
	resolvedThread, err := r.Resolve(ctx, threadConv)
	if err != nil || resolvedThread == nil || resolvedThread.Model != "claude-opus" || resolvedThread.Effort != "max" {
		t.Fatalf("Resolve(thread) = %+v, err=%v", resolvedThread, err)
	}

	// Coordinator inherits project thread override with low effort.
	coordConv := core.ConversationView{ID: project.CoordinatorID, ProjectID: project.ID, Role: "coordinator"}
	resolvedCoord, err := r.Resolve(ctx, coordConv)
	if err != nil || resolvedCoord == nil || resolvedCoord.Model != "claude-opus" || resolvedCoord.Effort != "low" {
		t.Fatalf("Resolve(coordinator) = %+v, err=%v", resolvedCoord, err)
	}

	// Research uses project research override (same as asking conversation with medium effort).
	if _, err := repo.PostMessage(ctx, project.ID, core.MessageInput{ConversationID: thread.ID, Author: "user", Content: "check"}); err != nil {
		t.Fatal(err)
	}
	turnID, err := repo.StartScheduledTurn(ctx, thread.ID, resolvedThread.Label)
	if err != nil {
		t.Fatal(err)
	}
	activeTurn := &turn{Runner: r, conversation: threadConv, id: turnID}
	entry := activeTurn.researchTool(nil, resolvedThread, args{"modelRetries": float64(0)})
	if _, err := entry.withCallID(ctx, "call_proj_res", args{"question": "Check project research override"}); err != nil {
		t.Fatal(err)
	}
	if len(seenRequests) != 1 || seenRequests[0].Model != "claude-opus" || seenRequests[0].Effort != "medium" {
		t.Fatalf("project research request = %+v", seenRequests)
	}
}
