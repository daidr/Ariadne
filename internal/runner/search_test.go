package runner

import (
	"context"
	"errors"
	"iter"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/models"
	"github.com/daidr/Ariadne/internal/searches"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/sdk"
)

func TestWebSearchPluginPriorityAndModelFallback(t *testing.T) {
	ctx := t.Context()
	home := t.TempDir()
	repo, err := core.Open(ctx, core.Options{Path: filepath.Join(home, "search.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	store, err := settings.Open(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatal(err)
	}

	project, err := repo.CreateProject(ctx, new("Search Priority"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}

	var calls []string
	failTavily := false
	searchRegistry := searches.New()
	if _, err := searchRegistry.Register(sdk.SearchProvider{
		Kind:  "tavily",
		Title: sdk.LocalizedText{"zh-CN": "Tavily", "en": "Tavily"},
		Settings: sdk.SettingsSchema{
			Type: "object",
			Properties: map[string]sdk.SettingField{
				"apiKey": {Type: "string", Secret: true},
			},
			Required: []string{"apiKey"},
		},
		Execute: func(_ context.Context, values map[string]any, query sdk.SearchQuery) (sdk.SearchResponse, error) {
			calls = append(calls, "tavily:"+values["apiKey"].(string))
			if failTavily {
				return sdk.SearchResponse{}, errors.New("tavily down")
			}
			return sdk.SearchResponse{
				Answer:  "Tavily summary",
				Results: []sdk.SearchResult{{Title: "Tavily Hit", URL: "https://tavily.test", Snippet: query.Query}},
			}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := searchRegistry.Register(sdk.SearchProvider{
		Kind:  "external",
		Title: sdk.LocalizedText{"zh-CN": "外部搜索", "en": "External Search"},
		Execute: func(_ context.Context, _ map[string]any, query sdk.SearchQuery) (sdk.SearchResponse, error) {
			calls = append(calls, "external")
			return sdk.SearchResponse{
				Results: []sdk.SearchResult{{Title: "External Hit", URL: "https://external.test", Snippet: query.Query}},
			}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	modelRegistry := models.New()
	if _, err := modelRegistry.Register(sdk.ModelProvider{
		Info: sdk.ModelProviderInfo{
			Kind:  "openai-compatible",
			Title: sdk.LocalizedText{"zh-CN": "OpenAI", "en": "OpenAI"},
		},
		Stream: func(context.Context, map[string]any, sdk.Request) iter.Seq[sdk.Part] {
			return func(func(sdk.Part) bool) {}
		},
		Search: func(_ context.Context, _ map[string]any, req sdk.SearchRequest) (sdk.SearchAnswer, error) {
			calls = append(calls, "model:"+req.Model)
			return sdk.SearchAnswer{
				Text:    "Model summary",
				Sources: []sdk.SearchResult{{Title: "Model Hit", URL: "https://model.test", Snippet: req.Query}},
			}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	resolvedModel := &settings.ResolvedModel{
		Provider: sdk.ModelProviderInfo{Kind: "openai-compatible"},
		Settings: settings.Object{},
		Model:    "gpt-test",
		Label:    "OpenAI / gpt-test",
	}

	// Configure tavily as active search provider; it must run before external and model search.
	if _, err := store.UpdateSearch(map[string]any{
		"provider": "tavily",
		"services": map[string]any{
			"tavily": map[string]any{"apiKey": "tvly-key"},
		},
	}, searchRegistry.Kinds(), searchRegistry.Validate); err != nil {
		t.Fatal(err)
	}

	turnID, err := repo.StartTurn(ctx, project.CoordinatorID, "mock")
	if err != nil {
		t.Fatal(err)
	}
	searchTurn := &turn{
		Runner: &Runner{
			Repository: repo,
			Settings:   store,
			Models:     modelRegistry,
			Searches:   searchRegistry,
		},
		conversation: core.ConversationView{ID: project.CoordinatorID, ProjectID: project.ID, Role: "coordinator"},
		id:           turnID,
	}
	entry := searchTurn.webSearch(project, resolvedModel)
	if entry == nil {
		t.Fatal("expected web_search tool")
	}

	// 1. Configured active search provider runs first and includes SearchResponse.Answer.
	out, err := entry.run(ctx, args{"query": "ariadne"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Tavily summary") || !strings.Contains(out, "Tavily Hit") {
		t.Fatalf("unexpected output: %s", out)
	}
	if strings.Join(calls, ",") != "tavily:tvly-key" {
		t.Fatalf("calls = %v, want tavily only", calls)
	}

	// 2. Explicit provider selection invokes only the requested provider.
	calls = nil
	out, err = entry.run(ctx, args{"query": "ariadne", "provider": "external"})
	if err != nil || !strings.Contains(out, "External Hit") || strings.Join(calls, ",") != "external" {
		t.Fatalf("explicit provider out=%q calls=%v err=%v", out, calls, err)
	}

	// 3. If active search provider fails and no other ready search plugin exists, fall back to model search.
	onlyTavily := searches.New()
	tavily, _ := searchRegistry.Get("tavily")
	if _, err := onlyTavily.Register(tavily); err != nil {
		t.Fatal(err)
	}
	searchTurn.Searches = onlyTavily
	entry = searchTurn.webSearch(project, resolvedModel)
	calls = nil
	failTavily = true
	out, err = entry.run(ctx, args{"query": "fallback"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Model summary") || !strings.Contains(out, "Model Hit") {
		t.Fatalf("expected model fallback output, got: %s", out)
	}
	if strings.Join(calls, ",") != "tavily:tvly-key,model:gpt-test" {
		t.Fatalf("calls = %v, want tavily then model", calls)
	}
}
