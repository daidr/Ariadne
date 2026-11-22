package searches

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/daidr/Ariadne/sdk"
)

const TavilyDefaultBaseURL = "https://api.tavily.com"

func TavilyProvider(client *http.Client) sdk.SearchProvider {
	execute := func(ctx context.Context, settings map[string]any, query sdk.SearchQuery) (sdk.SearchResponse, error) {
		rows, err := postSearch(ctx, client, settingString(settings, "baseUrl"), TavilyDefaultBaseURL, settingString(settings, "apiKey"), map[string]any{
			"query":       query.Query,
			"max_results": query.Limit,
		})
		if err != nil {
			return sdk.SearchResponse{}, err
		}
		hits := []sdk.SearchResult{}
		for _, raw := range rows {
			var hit struct{ URL, Title, Content json.RawMessage }
			if json.Unmarshal(raw, &hit) != nil {
				continue
			}
			url, ok := stringField(hit.URL)
			if !ok {
				continue
			}
			title := ""
			_ = json.Unmarshal(hit.Title, &title)
			snippet := ""
			_ = json.Unmarshal(hit.Content, &snippet)
			hits = append(hits, sdk.SearchResult{URL: url, Title: title, Snippet: snippet})
		}
		return sdk.SearchResponse{Results: hits}, nil
	}
	return sdk.SearchProvider{
		Kind:             "tavily",
		Title:            sdk.LocalizedText{"zh-CN": "Tavily", "en": "Tavily"},
		Settings:         searchSchema(TavilyDefaultBaseURL),
		Capabilities:     sdk.SearchCapabilities{SupportsCheck: true},
		ValidateSettings: validateHTTPSettings,
		Execute:          execute,
		Check: func(ctx context.Context, settings map[string]any) (sdk.SearchCheckResult, error) {
			_, err := execute(ctx, settings, sdk.SearchQuery{Query: "Ariadne", Limit: 1})
			return checkSearchError(err), nil
		},
	}
}

func TavilyPlugin(client *http.Client) sdk.Plugin {
	return sdk.Plugin{
		ID:       "search-tavily",
		Name:     sdk.LocalizedText{"zh-CN": "Tavily 搜索", "en": "Tavily Search"},
		Provides: sdk.PluginProvides{Searches: []string{"tavily"}},
		Setup: func(_ context.Context, pc sdk.PluginContext) (func(context.Context) error, error) {
			if _, err := pc.RegisterSearch(TavilyProvider(client)); err != nil {
				return nil, err
			}
			return nil, nil
		},
	}
}
