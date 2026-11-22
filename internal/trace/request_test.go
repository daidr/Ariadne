package trace

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/eventstore"
	"github.com/daidr/Ariadne/sdk"
)

func TestModelInputKeepReasoningUsesCapabilitiesReasoningScope(t *testing.T) {
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(t.TempDir(), "trace.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	name := "project"
	project, err := repo.CreateProject(t.Context(), &name, core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}

	record := func(hash string, call RecordedCall) eventstore.NewEvent {
		data, err := json.Marshal(call)
		if err != nil {
			t.Fatal(err)
		}
		payload, _ := json.Marshal(map[string]any{"hash": hash, "content": string(data)})
		return eventstore.NewEvent{Type: "prompt.recorded", ProjectID: project.ID, Data: payload}
	}

	if _, err := repo.Store.Append(t.Context(), []eventstore.NewEvent{
		record("h-custom-a", RecordedCall{Provider: "custom-http", Model: "model-a", Settings: map[string]any{"baseUrl": "https://api.example.com"}}),
		record("h-custom-b", RecordedCall{Provider: "custom-http", Model: "model-b", Settings: map[string]any{"baseUrl": "https://api.example.com"}}),
		record("h-custom-diff-settings", RecordedCall{Provider: "custom-http", Model: "model-a", Settings: map[string]any{"baseUrl": "https://other.example.com"}}),
	}); err != nil {
		t.Fatal(err)
	}

	// ReasoningScopeProvider allows replaying reasoning across different models of the same provider and settings.
	providerScopeCall := &RecordedCall{
		Provider:       "custom-http",
		Model:          "model-b",
		Settings:       map[string]any{"baseUrl": "https://api.example.com"},
		ReasoningScope: sdk.ReasoningScopeProvider,
	}
	optsProvider, _ := ModelInput(t.Context(), repo, project.ID, providerScopeCall, false)
	if !optsProvider.KeepReasoning("h-custom-a") {
		t.Fatal("expected provider-scoped reasoning to keep reasoning across models of same provider")
	}
	if optsProvider.KeepReasoning("h-custom-diff-settings") {
		t.Fatal("expected provider-scoped reasoning to reject different settings")
	}

	// ReasoningScopeModel (and empty default) restricts reasoning replay to the exact same model.
	modelScopeCall := &RecordedCall{
		Provider:       "custom-http",
		Model:          "model-b",
		Settings:       map[string]any{"baseUrl": "https://api.example.com"},
		ReasoningScope: sdk.ReasoningScopeModel,
	}
	optsModel, _ := ModelInput(t.Context(), repo, project.ID, modelScopeCall, false)
	if optsModel.KeepReasoning("h-custom-a") {
		t.Fatal("expected model-scoped reasoning to reject a different model")
	}
	if !optsModel.KeepReasoning("h-custom-b") {
		t.Fatal("expected model-scoped reasoning to keep reasoning for the same model")
	}
}
