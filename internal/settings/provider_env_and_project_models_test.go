package settings

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/internal/projectsettings"
	"github.com/daidr/Ariadne/internal/secrets"
	"github.com/daidr/Ariadne/sdk"
)

func testProgramKinds() []sdk.ModelProviderInfo {
	return []sdk.ModelProviderInfo{
		{
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
		{
			Kind:  "anthropic",
			Title: sdk.LocalizedText{"zh-CN": "Anthropic", "en": "Anthropic"},
			Settings: sdk.SettingsSchema{
				Type: "object",
				Properties: map[string]sdk.SettingField{
					"apiKey": {Type: "string", Secret: true},
				},
			},
		},
	}
}

func TestProviderEnvPersistenceMaskingRedactionAndDraft(t *testing.T) {
	t.Parallel()
	store, err := Open(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	kinds := testProgramKinds()

	// Reject ARIADNE_* env variable names.
	if _, err := store.UpdateModels(Object{
		"providers": []any{
			Object{
				"id":       "p-cc",
				"kind":     "claude-code",
				"name":     "Claude Code",
				"settings": Object{},
				"env":      []any{Object{"name": "ARIADNE_TOKEN", "value": "bad"}},
			},
		},
		"models": []any{},
		"roles":  Object{},
	}, kinds); err == nil {
		t.Fatal("expected ARIADNE_* provider env to be rejected")
	}

	changed, err := store.UpdateModels(Object{
		"providers": []any{
			Object{
				"id":       "p-cc",
				"kind":     "claude-code",
				"name":     "Claude Code",
				"settings": Object{"command": "claude"},
				"env": []any{
					Object{"name": "ANTHROPIC_BASE_URL", "value": "https://mirror.example"},
					Object{"name": "ANTHROPIC_AUTH_TOKEN", "value": "sk-secret-value-12345"},
				},
			},
		},
		"models": []any{
			Object{"id": "m-cc", "provider": "p-cc", "model": "default"},
		},
		"roles": Object{
			"thread": Object{"model": "m-cc", "effort": nil},
		},
	}, kinds)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 {
		t.Fatalf("expected 1 changed model on initial save, got %d", len(changed))
	}

	// PublicModels exposes only sorted env variable names, never values.
	public := store.PublicModels(kinds)
	providers := list(public["providers"])
	envNames, _ := object(providers[0])["env"].([]string)
	if !slices.Equal(envNames, []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL"}) {
		t.Fatalf("public env names = %#v", envNames)
	}

	// Diagnostic redaction picks up provider env values automatically.
	configured := secrets.ConfiguredValues(store.Current())
	if !slices.Contains(configured, "sk-secret-value-12345") {
		t.Fatalf("provider env secret missing from ConfiguredValues: %#v", configured)
	}

	// ResolveModel injects env into ResolvedModel.Settings["env"].
	resolved, err := store.ResolveModel("thread", "", "", kinds)
	if err != nil || resolved == nil {
		t.Fatalf("ResolveModel = %+v, err=%v", resolved, err)
	}
	envMap, ok := resolved.Settings["env"].(map[string]any)
	if !ok || envMap["ANTHROPIC_BASE_URL"] != "https://mirror.example" || envMap["ANTHROPIC_AUTH_TOKEN"] != "sk-secret-value-12345" {
		t.Fatalf("resolved env = %#v", resolved.Settings["env"])
	}

	// Empty value keeps stored value and does not mark provider changed.
	changed, err = store.UpdateModels(Object{
		"providers": []any{
			Object{
				"id":       "p-cc",
				"kind":     "claude-code",
				"name":     "Claude Code",
				"settings": Object{"command": "claude"},
				"env": []any{
					Object{"name": "ANTHROPIC_BASE_URL", "value": ""},
					Object{"name": "ANTHROPIC_AUTH_TOKEN", "value": ""},
				},
			},
		},
		"models": []any{
			Object{"id": "m-cc", "provider": "p-cc", "model": "default"},
		},
		"roles": Object{
			"thread": Object{"model": "m-cc", "effort": nil},
		},
	}, kinds)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 0 {
		t.Fatalf("expected 0 changed models when env preserved, got %d", len(changed))
	}

	// Changing an env value marks the provider's model as changed for re-checking.
	changed, err = store.UpdateModels(Object{
		"providers": []any{
			Object{
				"id":       "p-cc",
				"kind":     "claude-code",
				"name":     "Claude Code",
				"settings": Object{"command": "claude"},
				"env": []any{
					Object{"name": "ANTHROPIC_BASE_URL", "value": "https://mirror2.example"},
					Object{"name": "ANTHROPIC_AUTH_TOKEN", "value": ""},
				},
			},
		},
		"models": []any{
			Object{"id": "m-cc", "provider": "p-cc", "model": "default"},
		},
		"roles": Object{
			"thread": Object{"model": "m-cc", "effort": nil},
		},
	}, kinds)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 {
		t.Fatalf("expected 1 changed model after env edit, got %d", len(changed))
	}

	// DraftProvider without env inherits saved env and never leaks values into Hint.
	draft, err := store.DraftProvider(Object{"id": "p-cc", "kind": "claude-code", "settings": Object{}}, kinds)
	if err != nil {
		t.Fatal(err)
	}
	draftEnv, _ := draft.Settings["env"].(map[string]any)
	if draftEnv["ANTHROPIC_BASE_URL"] != "https://mirror2.example" || draftEnv["ANTHROPIC_AUTH_TOKEN"] != "sk-secret-value-12345" {
		t.Fatalf("draft inherited env = %#v", draftEnv)
	}
	if strings.Contains(draft.Hint, "sk-secret-value-12345") || strings.Contains(draft.Hint, "mirror2") {
		t.Fatalf("draft.Hint must not contain env values: %q", draft.Hint)
	}

	// DraftProvider with unsaved draft env merges stored values + new values.
	draft, err = store.DraftProvider(Object{
		"id":       "p-cc",
		"kind":     "claude-code",
		"settings": Object{},
		"env": []any{
			Object{"name": "ANTHROPIC_AUTH_TOKEN", "value": ""},
			Object{"name": "EXTRA_FLAG", "value": "unsaved"},
		},
	}, kinds)
	if err != nil {
		t.Fatal(err)
	}
	draftEnv, _ = draft.Settings["env"].(map[string]any)
	if len(draftEnv) != 2 || draftEnv["ANTHROPIC_AUTH_TOKEN"] != "sk-secret-value-12345" || draftEnv["EXTRA_FLAG"] != "unsaved" {
		t.Fatalf("draft merged env = %#v", draftEnv)
	}

	// DraftProvider with explicit empty env clears inherited env for listing.
	draft, err = store.DraftProvider(Object{
		"id":       "p-cc",
		"kind":     "claude-code",
		"settings": Object{},
		"env":      []any{},
	}, kinds)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := draft.Settings["env"]; exists {
		t.Fatalf("expected explicit empty draft env to clear inherited env, got %#v", draft.Settings["env"])
	}
}

func TestProviderEnvDisabledPluginPreservedAndKindSwitchIsolated(t *testing.T) {
	t.Parallel()
	store, err := Open(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	codexKind := sdk.ModelProviderInfo{
		Kind:    "codex",
		Title:   sdk.LocalizedText{"zh-CN": "Codex", "en": "Codex"},
		Program: true,
		Settings: sdk.SettingsSchema{
			Type: "object",
			Properties: map[string]sdk.SettingField{
				"command": {Type: "string", Default: "codex"},
			},
		},
	}
	allKinds := append(testProgramKinds(), codexKind)

	// Initial save with a program provider that has secret env variables.
	if _, err := store.UpdateModels(Object{
		"providers": []any{
			Object{
				"id":       "p-cc",
				"kind":     "claude-code",
				"name":     "Claude Code",
				"settings": Object{"command": "claude"},
				"env": []any{
					Object{"name": "ANTHROPIC_AUTH_TOKEN", "value": "sk-claude-secret"},
					Object{"name": "ANTHROPIC_BASE_URL", "value": "https://claude.example"},
				},
			},
		},
		"models": []any{
			Object{"id": "m-cc", "provider": "p-cc", "model": "default"},
		},
		"roles": Object{
			"thread": Object{"model": "m-cc", "effort": nil},
		},
	}, allKinds); err != nil {
		t.Fatal(err)
	}

	// 1. Saving while claude-code plugin is disabled preserves stored env even if request sends env=[].
	disabledKinds := []sdk.ModelProviderInfo{testProgramKinds()[1], codexKind}
	if _, err := store.UpdateModels(Object{
		"providers": []any{
			Object{
				"id":       "p-cc",
				"kind":     "claude-code",
				"name":     "Claude Code",
				"settings": Object{},
				"env":      []any{},
			},
		},
		"models": []any{
			Object{"id": "m-cc", "provider": "p-cc", "model": "default"},
		},
		"roles": Object{
			"thread": Object{"model": "m-cc", "effort": nil},
		},
	}, disabledKinds); err != nil {
		t.Fatalf("save with disabled plugin failed: %v", err)
	}

	// Re-enabling the plugin must still have all stored env secrets intact.
	resolved, err := store.ResolveModel("thread", "", "", allKinds)
	if err != nil || resolved == nil {
		t.Fatalf("ResolveModel after re-enable = %+v, err=%v", resolved, err)
	}
	envMap, _ := resolved.Settings["env"].(map[string]any)
	if envMap["ANTHROPIC_AUTH_TOKEN"] != "sk-claude-secret" || envMap["ANTHROPIC_BASE_URL"] != "https://claude.example" {
		t.Fatalf("disabled plugin env lost: %#v", envMap)
	}

	// 2. Switching provider kind from claude-code to codex must NOT inherit old claude-code secrets.
	if _, err := store.UpdateModels(Object{
		"providers": []any{
			Object{
				"id":       "p-cc",
				"kind":     "codex",
				"name":     "Codex",
				"settings": Object{"command": "codex"},
				"env": []any{
					Object{"name": "ANTHROPIC_AUTH_TOKEN", "value": ""},
				},
			},
		},
		"models": []any{
			Object{"id": "m-cc", "provider": "p-cc", "model": "default"},
		},
		"roles": Object{
			"thread": Object{"model": "m-cc", "effort": nil},
		},
	}, allKinds); err == nil {
		t.Fatal("expected switching provider kind with empty stored env to be rejected rather than inheriting old secrets")
	}

	// Switching kind with new env values succeeds and drops old kind's secrets completely.
	if _, err := store.UpdateModels(Object{
		"providers": []any{
			Object{
				"id":       "p-cc",
				"kind":     "codex",
				"name":     "Codex",
				"settings": Object{"command": "codex"},
				"env": []any{
					Object{"name": "OPENAI_API_KEY", "value": "sk-codex-new"},
				},
			},
		},
		"models": []any{
			Object{"id": "m-cc", "provider": "p-cc", "model": "default"},
		},
		"roles": Object{
			"thread": Object{"model": "m-cc", "effort": nil},
		},
	}, allKinds); err != nil {
		t.Fatalf("kind switch with fresh env failed: %v", err)
	}
	resolved, err = store.ResolveModel("thread", "", "", allKinds)
	if err != nil || resolved == nil {
		t.Fatalf("ResolveModel after kind switch = %+v, err=%v", resolved, err)
	}
	envMap, _ = resolved.Settings["env"].(map[string]any)
	if len(envMap) != 1 || envMap["OPENAI_API_KEY"] != "sk-codex-new" {
		t.Fatalf("expected only codex env after kind switch, got %#v", envMap)
	}

	// 3. Switching from program kind (codex) to non-program kind (anthropic) drops env even if env is sent.
	if _, err := store.UpdateModels(Object{
		"providers": []any{
			Object{
				"id":       "p-cc",
				"kind":     "anthropic",
				"name":     "Anthropic",
				"settings": Object{"apiKey": "sk-anth"},
				"env": []any{
					Object{"name": "SHOULD_NOT_KEEP", "value": "secret-should-drop"},
				},
			},
		},
		"models": []any{
			Object{"id": "m-cc", "provider": "p-cc", "model": "claude-sonnet"},
		},
		"roles": Object{
			"thread": Object{"model": "m-cc", "effort": nil},
		},
	}, allKinds); err != nil {
		t.Fatalf("switch to non-program kind failed: %v", err)
	}
	public := store.PublicModels(allKinds)
	publicEnv, _ := object(list(public["providers"])[0])["env"].([]string)
	if len(publicEnv) != 0 {
		t.Fatalf("expected non-program provider to drop env, got %#v", publicEnv)
	}
	configured := secrets.ConfiguredValues(store.Current())
	if slices.Contains(configured, "sk-codex-new") || slices.Contains(configured, "secret-should-drop") {
		t.Fatalf("non-program provider must not retain env secrets: %#v", configured)
	}
}

func TestProjectModelOverridesResolutionAndFallback(t *testing.T) {
	t.Parallel()
	store, err := Open(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	kinds := testProgramKinds()
	if _, err := store.UpdateModels(Object{
		"providers": []any{
			Object{"id": "p1", "kind": "anthropic", "name": "Anthropic", "settings": Object{"apiKey": "k"}},
		},
		"models": []any{
			Object{"id": "m-global-thread", "provider": "p1", "model": "global-thread", "efforts": []any{"low", "medium", "high"}},
			Object{"id": "m-global-coord", "provider": "p1", "model": "global-coord", "efforts": []any{"low", "medium", "high"}},
			Object{"id": "m-proj-thread", "provider": "p1", "model": "proj-thread", "efforts": []any{"low", "medium", "high"}},
			Object{"id": "m-proj-coord", "provider": "p1", "model": "proj-coord", "efforts": []any{"low", "medium", "high"}},
			Object{"id": "m-proj-research", "provider": "p1", "model": "proj-research", "efforts": []any{"low", "medium"}},
			Object{"id": "m-explicit-thread", "provider": "p1", "model": "explicit-thread", "efforts": []any{"low", "medium", "high"}},
		},
		"roles": Object{
			"thread":      Object{"model": "m-global-thread", "effort": "high"},
			"coordinator": Object{"model": nil, "effort": nil},
			"triage":      Object{"model": "m-global-coord", "effort": "low"},
			"research":    Object{"model": nil, "effort": "low"},
		},
	}, kinds); err != nil {
		t.Fatal(err)
	}

	// 1. Project overrides all three roles.
	allOverrides := &projectsettings.Models{
		Thread:      projectsettings.ModelRole{Model: "m-proj-thread", Effort: "medium"},
		Coordinator: projectsettings.ModelRole{Model: "m-proj-coord", Effort: ""},
		Research:    projectsettings.ModelRole{Model: "m-proj-research", Effort: "high"},
	}

	// Thread default uses project thread override; thread explicit choice still wins.
	got, err := store.ResolveModelForProject("thread", "", "", allOverrides, kinds)
	if err != nil || got.Model != "proj-thread" || got.Effort != "medium" {
		t.Fatalf("project thread = %+v, err=%v", got, err)
	}
	got, err = store.ResolveModelForProject("thread", "m-explicit-thread", "low", allOverrides, kinds)
	if err != nil || got.Model != "explicit-thread" || got.Effort != "low" {
		t.Fatalf("explicit thread over project = %+v, err=%v", got, err)
	}

	// Coordinator uses project coordinator override (empty effort = model default, not forced low),
	// and triage does not bypass project coordinator override.
	got, err = store.ResolveCoordinatorForProject(false, 0, allOverrides, kinds)
	if err != nil || got.Model != "proj-coord" || got.Effort != "" {
		t.Fatalf("project coordinator = %+v, err=%v", got, err)
	}
	got, err = store.ResolveCoordinatorForProject(true, 0, allOverrides, kinds)
	if err != nil || got.Model != "proj-coord" || got.Effort != "" {
		t.Fatalf("project coordinator triage = %+v, err=%v", got, err)
	}

	// Research uses project research override (effort clamped to supported max "medium").
	parent, _ := store.ResolveModelForProject("thread", "", "", allOverrides, kinds)
	got, err = store.ResolveResearchForProject(parent, allOverrides, kinds)
	if err != nil || got.Model != "proj-research" || got.Effort != "medium" {
		t.Fatalf("project research = %+v, err=%v", got, err)
	}

	// ConversationModelForProject matches runtime resolution.
	conv := store.ConversationModelForProject("thread", "", "", allOverrides)
	if conv["id"] != "m-proj-thread" || conv["effort"] != "medium" {
		t.Fatalf("ConversationModelForProject(thread) = %#v", conv)
	}
	conv = store.ConversationModelForProject("coordinator", "", "", allOverrides)
	if conv["id"] != "m-proj-coord" || conv["effort"] != nil {
		t.Fatalf("ConversationModelForProject(coordinator) = %#v", conv)
	}

	// 2. When global coordinator is "same as threads" and only project thread is overridden,
	// coordinator inherits project's thread model with coordinator's default low effort.
	threadOnly := &projectsettings.Models{
		Thread: projectsettings.ModelRole{Model: "m-proj-thread", Effort: "high"},
	}
	got, err = store.ResolveModelForProject("coordinator", "", "", threadOnly, kinds)
	if err != nil || got.Model != "proj-thread" || got.Effort != "low" {
		t.Fatalf("coordinator following project thread = %+v, err=%v", got, err)
	}
	conv = store.ConversationModelForProject("coordinator", "", "", threadOnly)
	if conv["id"] != "m-proj-thread" || conv["effort"] != "low" {
		t.Fatalf("ConversationModelForProject(coordinator follow project thread) = %#v", conv)
	}

	// 3. When project overrides point to deleted models, cleanly fall back to global config.
	deleted := &projectsettings.Models{
		Thread:      projectsettings.ModelRole{Model: "m-gone", Effort: "low"},
		Coordinator: projectsettings.ModelRole{Model: "m-gone", Effort: "high"},
		Research:    projectsettings.ModelRole{Model: "m-gone", Effort: "high"},
	}
	got, err = store.ResolveModelForProject("thread", "", "", deleted, kinds)
	if err != nil || got.Model != "global-thread" || got.Effort != "high" {
		t.Fatalf("deleted thread fallback = %+v, err=%v", got, err)
	}
	got, err = store.ResolveModelForProject("coordinator", "", "", deleted, kinds)
	if err != nil || got.Model != "global-thread" || got.Effort != "low" {
		t.Fatalf("deleted coordinator fallback = %+v, err=%v", got, err)
	}
	got, err = store.ResolveResearchForProject(parent, deleted, kinds)
	if err != nil || got.Model != "proj-thread" || got.Effort != "low" {
		t.Fatalf("deleted research fallback = %+v, err=%v", got, err)
	}
}
