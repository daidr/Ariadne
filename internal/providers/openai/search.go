package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/daidr/Ariadne/internal/modelhttp"
	"github.com/daidr/Ariadne/sdk"
)

type SearchRefused = modelhttp.SearchRefused

func SearchAvailable(settings Settings) bool {
	address, err := url.Parse(settings.BaseURL)
	return err == nil && strings.EqualFold(address.Hostname(), "api.openai.com")
}

func SearchBody(request sdk.SearchRequest) map[string]any {
	return map[string]any{
		"model": request.Model, "tools": []any{map[string]any{"type": "web_search"}},
		"include": []string{"web_search_call.action.sources"}, "max_output_tokens": 8192,
		"input": sdk.SearchAsk(request.Query),
	}
}

type searchResponse struct {
	Status string `json:"status"`
	Output []struct {
		Type   string `json:"type"`
		Action struct {
			Sources []struct {
				URL string `json:"url"`
			} `json:"sources"`
		} `json:"action"`
		Content []struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			Annotations []struct {
				Type  string `json:"type"`
				URL   string `json:"url"`
				Title string `json:"title"`
			} `json:"annotations"`
		} `json:"content"`
	} `json:"output"`
	Usage *struct {
		Input   int64 `json:"input_tokens"`
		Output  int64 `json:"output_tokens"`
		Details struct {
			Cached int64 `json:"cached_tokens"`
		} `json:"input_tokens_details"`
	} `json:"usage"`
}

func ReadSearch(data []byte, limit int) (sdk.SearchAnswer, error) {
	var response searchResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return sdk.SearchAnswer{}, err
	}
	answer := sdk.SearchAnswer{Sources: make([]sdk.SearchResult, 0)}
	seen := make(map[string]bool)
	read := make([]string, 0)
	for _, item := range response.Output {
		if item.Type == "web_search_call" {
			for _, source := range item.Action.Sources {
				if source.URL != "" {
					read = append(read, source.URL)
				}
			}
		}
		if item.Type != "message" {
			continue
		}
		for _, part := range item.Content {
			if part.Type != "output_text" {
				continue
			}
			answer.Text += part.Text
			for _, cite := range part.Annotations {
				if cite.Type == "url_citation" && cite.URL != "" && !seen[cite.URL] {
					seen[cite.URL] = true
					answer.Sources = append(answer.Sources, sdk.SearchResult{Title: cite.Title, URL: cite.URL})
				}
			}
		}
	}
	answer.Text = strings.TrimSpace(answer.Text)
	if answer.Text == "" && response.Status == "incomplete" {
		return sdk.SearchAnswer{}, errors.New("the search ended early")
	}
	for _, address := range read {
		if !seen[address] {
			seen[address] = true
			answer.Sources = append(answer.Sources, sdk.SearchResult{URL: address})
		}
	}
	answer.Sources = answer.Sources[:min(max(limit, 0), len(answer.Sources))]
	if response.Usage != nil {
		answer.Usage = &sdk.SearchUsage{Input: response.Usage.Input, Output: response.Usage.Output, Cached: response.Usage.Details.Cached}
	}
	return answer, nil
}

func (provider Provider) Search(ctx context.Context, settings Settings, request sdk.SearchRequest) (sdk.SearchAnswer, error) {
	body, err := json.Marshal(SearchBody(request))
	if err != nil {
		return sdk.SearchAnswer{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(settings.BaseURL, "/")+"/responses", bytes.NewReader(body))
	if err != nil {
		return sdk.SearchAnswer{}, err
	}
	for key, value := range request.Headers {
		httpRequest.Header.Set(key, value)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	if settings.APIKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+settings.APIKey)
	}
	response, err := provider.client().Do(httpRequest)
	if err != nil {
		return sdk.SearchAnswer{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if response.StatusCode >= 400 && response.StatusCode < 500 && response.StatusCode != 408 && response.StatusCode != 429 {
			return sdk.SearchAnswer{}, &SearchRefused{Status: response.StatusCode}
		}
		return sdk.SearchAnswer{}, fmt.Errorf("HTTP %d", response.StatusCode)
	}
	data, err := readResponse(response.Body)
	if err != nil {
		return sdk.SearchAnswer{}, err
	}
	return ReadSearch(data, request.Limit)
}

func readResponse(reader io.Reader) ([]byte, error) {
	return modelhttp.ReadResponse(reader)
}

func (provider Provider) Models(ctx context.Context, settings Settings, headers map[string]string) ([]string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(settings.BaseURL, "/")+"/models", nil)
	if err != nil {
		return nil, err
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	if settings.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+settings.APIKey)
	}
	response, err := provider.client().Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := readResponse(response.Body)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, errors.New(modelhttp.RefusalText(response.StatusCode, data))
	}
	var body struct {
		Data []struct {
			ID      json.RawMessage `json:"id"`
			Created json.RawMessage `json:"created"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, err
	}
	type model struct {
		ID      string
		Created float64
	}
	models := make([]model, 0, len(body.Data))
	for _, item := range body.Data {
		var id string
		if len(item.ID) == 0 || item.ID[0] != '"' || json.Unmarshal(item.ID, &id) != nil {
			continue
		}
		var created float64
		_ = json.Unmarshal(item.Created, &created)
		models = append(models, model{ID: id, Created: created})
	}
	sort.SliceStable(models, func(a, b int) bool { return models[a].Created > models[b].Created })
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	return ids, nil
}
