package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/daidr/Ariadne/internal/modelhttp"
	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/sdk"
)

func SearchAvailable(settings Settings) bool {
	address, err := url.Parse(settings.BaseURL)
	return err == nil && strings.EqualFold(address.Hostname(), "api.anthropic.com")
}
func SearchBody(request sdk.SearchRequest) map[string]any {
	return block{"model": request.Model, "max_tokens": 4096, "tools": []block{{"type": "web_search_20250305", "name": "web_search", "max_uses": 2}}, "messages": []block{{"role": "user", "content": sdk.SearchAsk(request.Query)}}}
}

type searchResponse struct {
	Stop    string `json:"stop_reason"`
	Content []struct {
		Type      string `json:"type"`
		Text      string `json:"text"`
		Citations []struct {
			URL   string `json:"url"`
			Title string `json:"title"`
			Text  string `json:"cited_text"`
		} `json:"citations"`
		Content json.RawMessage `json:"content"`
	} `json:"content"`
	Usage *usage `json:"usage"`
}

func ReadSearch(data []byte, limit int) (sdk.SearchAnswer, error) {
	var response searchResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return sdk.SearchAnswer{}, err
	}
	answer := sdk.SearchAnswer{Sources: []sdk.SearchResult{}}
	indices := map[string]int{}
	found := []sdk.SearchResult{}
	failed := ""
	for _, part := range response.Content {
		switch part.Type {
		case "text":
			answer.Text += part.Text
			for _, cite := range part.Citations {
				if cite.URL == "" {
					continue
				}
				quoted := textutil.Trim(cite.Text)
				if index, ok := indices[cite.URL]; !ok {
					indices[cite.URL] = len(answer.Sources)
					answer.Sources = append(answer.Sources, sdk.SearchResult{URL: cite.URL, Title: cite.Title, Snippet: quoted})
				} else if quoted != "" && !strings.Contains(answer.Sources[index].Snippet, quoted) {
					if answer.Sources[index].Snippet != "" {
						answer.Sources[index].Snippet += " … "
					}
					answer.Sources[index].Snippet += quoted
				}
			}
		case "web_search_tool_result":
			var pages []struct {
				URL   string `json:"url"`
				Title string `json:"title"`
			}
			if len(part.Content) > 0 && part.Content[0] == '[' && json.Unmarshal(part.Content, &pages) == nil {
				for _, page := range pages {
					if page.URL != "" {
						found = append(found, sdk.SearchResult{URL: page.URL, Title: page.Title})
					}
				}
			} else {
				var failure struct {
					Code *string `json:"error_code"`
				}
				failed = "unavailable"
				if json.Unmarshal(part.Content, &failure) == nil && failure.Code != nil {
					failed = *failure.Code
				}
			}
		}
	}
	for _, page := range found {
		if _, ok := indices[page.URL]; !ok {
			indices[page.URL] = len(answer.Sources)
			answer.Sources = append(answer.Sources, page)
		}
	}
	if len(answer.Sources) == 0 && failed != "" {
		return sdk.SearchAnswer{}, fmt.Errorf("web search failed: %s", failed)
	}
	if response.Stop == "refusal" {
		return sdk.SearchAnswer{}, errors.New("the model declined to search")
	}
	answer.Text = textutil.Trim(answer.Text)
	if answer.Text == "" && (response.Stop == "max_tokens" || response.Stop == "pause_turn") {
		return sdk.SearchAnswer{}, fmt.Errorf("the search ended early: %s", response.Stop)
	}
	answer.Sources = answer.Sources[:min(max(limit, 0), len(answer.Sources))]
	if usage := response.Usage; usage != nil {
		answer.Usage = &sdk.SearchUsage{Input: usage.Input + usage.CacheWrite + usage.Cached, Output: usage.Output, Cached: usage.Cached, CacheWrite: usage.CacheWrite}
	}
	return answer, nil
}
func (provider Provider) Search(ctx context.Context, settings Settings, request sdk.SearchRequest) (sdk.SearchAnswer, error) {
	body, err := json.Marshal(SearchBody(request))
	if err != nil {
		return sdk.SearchAnswer{}, err
	}
	httpRequest, err := provider.newRequest(ctx, settings, http.MethodPost, "/v1/messages", body, request.Headers)
	if err != nil {
		return sdk.SearchAnswer{}, err
	}
	response, err := provider.client().Do(httpRequest)
	if err != nil {
		return sdk.SearchAnswer{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return sdk.SearchAnswer{}, modelhttp.SearchHTTPError(response.StatusCode)
	}
	data, err := modelhttp.ReadResponse(response.Body)
	if err != nil {
		return sdk.SearchAnswer{}, err
	}
	return ReadSearch(data, request.Limit)
}

var effortOrder = []string{"low", "medium", "high", "xhigh", "max"}

func Listed(model map[string]any) sdk.ListedModel {
	id, _ := model["id"].(string)
	result := sdk.ListedModel{ID: id}
	if title, ok := model["display_name"].(string); ok {
		title = textutil.Trim(title)
		if title != id {
			result.Title = title
		}
	}
	capabilities, _ := model["capabilities"].(map[string]any)
	effort, _ := capabilities["effort"].(map[string]any)
	levels := []string{}
	for _, level := range effortOrder {
		item, _ := effort[level].(map[string]any)
		if item["supported"] == true {
			levels = append(levels, level)
		}
	}
	if len(levels) > 0 || effort["supported"] == false {
		result.Efforts = &levels
	}
	return result
}
func (provider Provider) Models(ctx context.Context, settings Settings, headers map[string]string) ([]sdk.ListedModel, error) {
	request, err := provider.newRequest(ctx, settings, http.MethodGet, "/v1/models?limit=1000", nil, headers)
	if err != nil {
		return nil, err
	}
	response, err := provider.client().Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := modelhttp.ReadResponse(response.Body)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, errors.New(modelhttp.RefusalText(response.StatusCode, data))
	}
	var body struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, err
	}
	models := []sdk.ListedModel{}
	for _, model := range body.Data {
		if _, ok := model["id"].(string); ok {
			models = append(models, Listed(model))
		}
	}
	return models, nil
}
func Find(env map[string]string) map[string]any {
	key := textutil.Trim(env["ANTHROPIC_API_KEY"])
	if key == "" {
		return nil
	}
	base := textutil.Trim(env["ANTHROPIC_BASE_URL"])
	if base == "" {
		base = "https://api.anthropic.com"
	}
	return block{"id": "ANTHROPIC_API_KEY", "from": block{"env": "ANTHROPIC_API_KEY"}, "settings": block{"baseUrl": base, "apiKey": key}}
}
