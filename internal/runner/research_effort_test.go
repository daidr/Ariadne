package runner

import (
	"context"
	"iter"
	"path/filepath"
	"testing"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/models"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/sdk"
)

func TestResearchAndThreadEffort(t *testing.T) {
	ctx := t.Context()
	home := t.TempDir()
	repo, err := core.Open(ctx, core.Options{Path: filepath.Join(home, "effort.sqlite")})
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
