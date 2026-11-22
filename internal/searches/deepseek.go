package searches

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"unicode/utf8"

	"github.com/daidr/Ariadne/sdk"
)

const DeepSeekDefaultBaseURL = "https://api.deepseek.com/v1/tools"

func DeepSeekProvider(client *http.Client) sdk.SearchProvider {
	execute := func(ctx context.Context, settings map[string]any, query sdk.SearchQuery) (sdk.SearchResponse, error) {
		if utf8.RuneCountInString(query.Query) > 128 {
			return sdk.SearchResponse{}, errors.New("DeepSeek search query must be at most 128 characters")
		}
		rows, err := postSearch(ctx, client, settingString(settings, "baseUrl"), DeepSeekDefaultBaseURL, settingString(settings, "apiKey"), map[string]any{
			"query":        query.Query,
			"count":        min(50, query.Limit),
			"need_content": false,
		})
		if err != nil {
			return sdk.SearchResponse{}, err
		}
		hits := []sdk.SearchResult{}
		for _, raw := range rows {
			var hit struct{ URL, Title, Snippet, Summary json.RawMessage }
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
			if value, ok := stringField(hit.Summary); ok {
				snippet = value
			} else {
				snippet, _ = stringField(hit.Snippet)
			}
			hits = append(hits, sdk.SearchResult{URL: url, Title: title, Snippet: snippet})
		}
		return sdk.SearchResponse{Results: hits}, nil
	}
	return sdk.SearchProvider{
		Kind:             "deepseek",
		Title:            sdk.LocalizedText{"zh-CN": "DeepSeek Search API", "en": "DeepSeek Search API"},
		Settings:         searchSchema(DeepSeekDefaultBaseURL),
		Capabilities:     sdk.SearchCapabilities{MaxQueryRunes: 128, MaxResults: 50, SupportsCheck: true},
		ValidateSettings: validateHTTPSettings,
		Execute:          execute,
		Check: func(ctx context.Context, settings map[string]any) (sdk.SearchCheckResult, error) {
			_, err := execute(ctx, settings, sdk.SearchQuery{Query: "Ariadne", Limit: 1})
			return checkSearchError(err), nil
		},
	}
}

func DeepSeekPlugin(client *http.Client) sdk.Plugin {
	return sdk.Plugin{
		ID:       "search-deepseek",
		Name:     sdk.LocalizedText{"zh-CN": "DeepSeek 搜索", "en": "DeepSeek Search"},
		Provides: sdk.PluginProvides{Searches: []string{"deepseek"}},
		Setup: func(_ context.Context, pc sdk.PluginContext) (func(context.Context) error, error) {
			if _, err := pc.RegisterSearch(DeepSeekProvider(client)); err != nil {
				return nil, err
			}
			return nil, nil
		},
	}
}
