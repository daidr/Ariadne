// Package searchservice implements independent Tavily and DeepSeek search APIs.
package searchservice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/daidr/Ariadne/internal/jsonutil"
	"github.com/daidr/Ariadne/internal/modelhttp"
	"github.com/daidr/Ariadne/sdk"
)

type HTTPError struct{ Status int }

func (err *HTTPError) Error() string { return fmt.Sprintf("HTTP %d", err.Status) }

type Provider struct {
	Kind, APIKey, BaseURL string
	Client                *http.Client
}

func stringField(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || raw[0] != '"' {
		return "", false
	}
	var value string
	err := json.Unmarshal(raw, &value)
	return value, err == nil
}

func (provider Provider) Search(ctx context.Context, query string, limit int) ([]sdk.SearchResult, error) {
	kind := provider.Kind
	if kind == "" {
		kind = "tavily"
	}
	if kind != "tavily" && kind != "deepseek" {
		return nil, errors.New("unknown search provider")
	}
	if kind == "deepseek" && utf8.RuneCountInString(query) > 128 {
		return nil, errors.New("DeepSeek search query must be at most 128 characters")
	}
	address := provider.BaseURL
	if address == "" {
		address = "https://api.tavily.com"
		if kind == "deepseek" {
			address = "https://api.deepseek.com/v1/tools"
		}
	}
	body := map[string]any{"query": query, "max_results": limit}
	if kind == "deepseek" {
		body = map[string]any{"query": query, "count": min(50, limit), "need_content": false}
	}
	data, err := jsonutil.Encode(body)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(address, "/")+"/search", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+provider.APIKey)
	client := http.Client{}
	if provider.Client != nil {
		client = *provider.Client
	}
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, &HTTPError{Status: response.StatusCode}
	}
	data, err = modelhttp.ReadResponse(response.Body)
	if err != nil {
		return nil, err
	}
	var result struct {
		Results *[]json.RawMessage `json:"results"`
	}
	if json.Unmarshal(data, &result) != nil || result.Results == nil {
		return nil, errors.New("Invalid search response")
	}
	hits := []sdk.SearchResult{}
	for _, raw := range *result.Results {
		var hit struct{ URL, Title, Content, Snippet, Summary json.RawMessage }
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
		if kind == "deepseek" {
			if value, ok := stringField(hit.Summary); ok {
				snippet = value
			} else {
				snippet, _ = stringField(hit.Snippet)
			}
		} else {
			_ = json.Unmarshal(hit.Content, &snippet)
		}
		hits = append(hits, sdk.SearchResult{URL: url, Title: title, Snippet: snippet})
	}
	return hits, nil
}
