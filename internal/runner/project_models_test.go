package runner

import (
	"context"
	"encoding/json"
	"iter"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/models"
	"github.com/daidr/Ariadne/internal/projectsettings"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/sdk"
)

func TestRunnerUsesProjectModelOverridesAndRedactsEnvFromRecordCall(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	repo, err := core.Open(ctx, core.Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	store, err := settings.Open(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}

	var seenRequests []sdk.Request
	modelRegistry := models.New()
	if _, err := modelRegistry.Register(sdk.ModelProvider{
		Info: sdk.ModelProviderInfo{
			Kind:    "claude-code",
			Title:   sdk.LocalizedText{"zh-CN": "Claude Code", "en": "Claude Code"},
			Program: true,
			Settings: sdk.SettingsSchema{
				Type: "object",
				Properties: map[string]sdk.SettingField{
					"command": {Type: "string", Default: "claude"},
				},
			},
		},
		Stream: func(_ context.Context, _ map[string]any, req sdk.Request) iter.Seq[sdk.Part] {
			seenRequests = append(seenRequests, req)
			return func(yield func(sdk.Part) bool) {
				yield(sdk.Part{Type: "text", Text: "done"})
			}
		},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := store.UpdateModels(settings.Object{
		"providers": []any{
			settings.Object{
				"id":       "p1",
				"kind":     "claude-code",
				"name":     "Claude Code",
				"settings": settings.Object{"command": "claude"},
				"env":      []any{settings.Object{"name": "ANTHROPIC_AUTH_TOKEN", "value": "top-secret-env-token"}},
			},
		},
		"models": []any{
			settings.Object{"id": "m-global", "provider": "p1", "model": "global-model", "efforts": []any{"low", "medium", "high"}},
			settings.Object{"id": "m-proj-thread", "provider": "p1", "model": "proj-thread-model", "efforts": []any{"low", "medium", "high"}},
			settings.Object{"id": "m-proj-coord", "provider": "p1", "model": "proj-coord-model", "efforts": []any{"low", "medium", "high"}},
			settings.Object{"id": "m-proj-research", "provider": "p1", "model": "proj-research-model", "efforts": []any{"low", "medium", "high"}},
		},
		"roles": settings.Object{
			"thread":      settings.Object{"model": "m-global", "effort": "high"},
			"coordinator": settings.Object{"model": nil, "effort": nil},
			"research":    settings.Object{"model": nil, "effort": nil},
		},
	}, modelRegistry.Kinds()); err != nil {
		t.Fatal(err)
	}

	project, err := repo.CreateProject(ctx, new("Special Project"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	next := project.Settings
	next.Models = projectsettings.Models{
		Thread:      projectsettings.ModelRole{Model: "m-proj-thread", Effort: "medium"},
		Coordinator: projectsettings.ModelRole{Model: "m-proj-coord", Effort: "low"},
		Research:    projectsettings.ModelRole{Model: "m-proj-research", Effort: "high"},
	}
	if _, err := repo.SetProjectSettings(ctx, project.ID, next, nil); err != nil {
		t.Fatal(err)
	}
	thread, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "T1"})
	if err != nil {
		t.Fatal(err)
	}
	threadConv, err := repo.GetConversation(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	coordConv, err := repo.GetConversation(ctx, project.CoordinatorID)
	if err != nil {
		t.Fatal(err)
	}

	r := &Runner{Repository: repo, Settings: store, Models: modelRegistry}

	resolvedThread, err := r.Resolve(ctx, threadConv)
	if err != nil || resolvedThread.Model != "proj-thread-model" || resolvedThread.Effort != "medium" {
		t.Fatalf("thread Resolve = %+v, err=%v", resolvedThread, err)
	}
	// Verify recordCall never leaks provider env secrets into recorded call metadata.
	recorded, err := json.Marshal(recordCall(resolvedThread, resolvedThread.Effort))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(recorded), "top-secret-env-token") || strings.Contains(string(recorded), `"env"`) {
		t.Fatalf("recordCall leaked provider env: %s", string(recorded))
	}

	resolvedCoord, err := r.Resolve(ctx, coordConv)
	if err != nil || resolvedCoord.Model != "proj-coord-model" || resolvedCoord.Effort != "low" {
		t.Fatalf("coordinator Resolve = %+v, err=%v", resolvedCoord, err)
	}

	// Execute research tool and verify it invokes the project's research model override.
	if _, err := repo.PostMessage(ctx, project.ID, core.MessageInput{ConversationID: thread.ID, Author: "user", Content: "check"}); err != nil {
		t.Fatal(err)
	}
	turnID, err := repo.StartScheduledTurn(ctx, thread.ID, resolvedThread.Label)
	if err != nil {
		t.Fatal(err)
	}
	active := &turn{Runner: r, conversation: threadConv, id: turnID}
	entry := active.researchTool(nil, resolvedThread, args{"modelRetries": float64(0)})
	if _, err := entry.withCallID(ctx, "call_1", args{"question": "check project model"}); err != nil {
		t.Fatal(err)
	}
	if len(seenRequests) != 1 || seenRequests[0].Model != "proj-research-model" || seenRequests[0].Effort != "high" {
		t.Fatalf("research requests = %+v", seenRequests)
	}
}
