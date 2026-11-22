package searches

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/sdk"
)

func TestBuiltinSearchRequestAndResults(t *testing.T) {
	for _, kind := range []string{"tavily", "deepseek"} {
		t.Run(kind, func(t *testing.T) {
			called := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called++
				if r.URL.Path != "/"+kind+"/search" || r.Header.Get("Authorization") != "Bearer private" {
					t.Error("wrong request", r.URL.Path, r.Header)
				}
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				var want map[string]any
				if called == 1 {
					want = map[string]any{"query": "中文😀", "max_results": float64(70)}
					if kind == "deepseek" {
						want = map[string]any{"query": "中文😀", "count": float64(50), "need_content": false}
					}
				} else {
					want = map[string]any{"query": "Ariadne", "max_results": float64(1)}
					if kind == "deepseek" {
						want = map[string]any{"query": "Ariadne", "count": float64(1), "need_content": false}
					}
				}
				if !reflect.DeepEqual(body, want) {
					t.Errorf("body %v want %v", body, want)
				}
				_, _ = io.WriteString(w, `{"results":[{"url":"https://example.test","title":"Result","content":"Tavily","summary":"DeepSeek summary","snippet":"fallback"},null,{},1,{"url":null},{"url":"","title":1,"summary":null,"snippet":"fallback"}]}`)
			}))
			defer server.Close()

			registry := New()
			for _, provider := range BuiltinProviders(server.Client()) {
				if _, err := registry.Register(provider); err != nil {
					t.Fatal(err)
				}
			}
			settings := map[string]any{"apiKey": "private", "baseUrl": server.URL + "/" + kind + "/"}
			if err := registry.Validate(kind, settings); err != nil {
				t.Fatal(err)
			}
			if !registry.Ready(kind, settings) {
				t.Fatal("expected provider to be ready")
			}
			response, err := registry.Execute(t.Context(), kind, settings, sdk.SearchQuery{Query: "中文😀", Limit: 70})
			if err != nil {
				t.Fatal(err)
			}
			hits := response.Results
			if called != 1 || len(hits) != 2 || hits[0].Title != "Result" || hits[1].Title != "" {
				t.Fatal(hits)
			}
			want := "Tavily"
			if kind == "deepseek" {
				want = "DeepSeek summary"
				if hits[1].Snippet != "fallback" {
					t.Fatal(hits)
				}
			}
			if hits[0].Snippet != want {
				t.Fatal(hits)
			}
			check, err := registry.Check(t.Context(), kind, settings)
			if err != nil || !check.OK || check.Status != nil {
				t.Fatalf("check = %+v, err = %v", check, err)
			}
		})
	}
}

func TestBuiltinRedirectsErrorsAndShape(t *testing.T) {
	redirected := false
	status := 302
	body := `{"results":[]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/target" {
			redirected = true
		}
		if status == 302 {
			w.Header().Set("Location", "/target")
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()

	registry := New()
	for _, provider := range BuiltinProviders(server.Client()) {
		if _, err := registry.Register(provider); err != nil {
			t.Fatal(err)
		}
	}
	settings := map[string]any{"apiKey": "private", "baseUrl": server.URL}
	for _, code := range []int{302, 401, 500} {
		status = code
		_, err := registry.Execute(t.Context(), "tavily", settings, sdk.SearchQuery{Query: "query", Limit: 1})
		var httpError *HTTPError
		if !errors.As(err, &httpError) || httpError.Status != code || strings.Contains(err.Error(), "private") {
			t.Fatal(err)
		}
		check, err := registry.Check(t.Context(), "tavily", settings)
		if err != nil || check.OK || check.Status == nil || *check.Status != code {
			t.Fatalf("check status %d = %+v, err = %v", code, check, err)
		}
	}
	if redirected {
		t.Fatal("search followed a redirect")
	}
	status = 200
	for _, raw := range []string{`null`, `{}`, `{"results":null}`, `{"results":{}}`, `not JSON`} {
		body = raw
		if _, err := registry.Execute(t.Context(), "tavily", settings, sdk.SearchQuery{Query: "query", Limit: 1}); err == nil {
			t.Fatal("invalid results accepted", raw)
		}
	}
	if _, err := registry.Execute(t.Context(), "deepseek", settings, sdk.SearchQuery{Query: strings.Repeat("😀", 129), Limit: 1}); err == nil {
		t.Fatal("oversized DeepSeek query accepted")
	}
	if err := registry.Validate("tavily", map[string]any{"apiKey": "bad key"}); err == nil {
		t.Fatal("apiKey with whitespace accepted")
	}
	for _, badURL := range []string{"ftp://example.test", "https://user:pass@example.test", "https://example.test?a=1", "https://example.test#frag"} {
		if err := registry.Validate("tavily", map[string]any{"baseUrl": badURL}); err == nil {
			t.Fatalf("unsafe baseUrl %q accepted", badURL)
		}
	}
}

func TestRegistryLegacyAndCapabilities(t *testing.T) {
	registry := New()
	dispose, err := registry.Register(sdk.SearchProvider{
		Kind:         "legacy",
		Title:        sdk.LocalizedText{"zh-CN": "旧版搜索", "en": "Legacy Search"},
		Capabilities: sdk.SearchCapabilities{SupportsCheck: true},
		Search: func(_ context.Context, query string, limit int) ([]sdk.SearchResult, error) {
			return []sdk.SearchResult{{Title: query, URL: "https://example.test", Snippet: "ok"}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := registry.Get("legacy"); !ok || got.Capabilities.SupportsCheck {
		t.Fatalf("expected SupportsCheck=false when Check is nil, got %+v", got.Capabilities)
	}
	if kinds := registry.Kinds(); len(kinds) != 1 || kinds[0].Capabilities.SupportsCheck {
		t.Fatalf("expected Kinds SupportsCheck=false when Check is nil, got %+v", kinds)
	}
	if !registry.Ready("legacy", nil) {
		t.Fatal("legacy provider with no required settings should be ready")
	}
	response, err := registry.Execute(t.Context(), "legacy", nil, sdk.SearchQuery{Query: "hello", Limit: 5})
	if err != nil || len(response.Results) != 1 || response.Results[0].Title != "hello" {
		t.Fatalf("legacy response = %+v, err = %v", response, err)
	}
	if _, err := registry.Check(t.Context(), "legacy", nil); err == nil {
		t.Fatal("expected check error for provider without Check")
	}
	if _, err := registry.Register(sdk.SearchProvider{
		Kind:  "legacy",
		Title: sdk.LocalizedText{"zh-CN": "重复"},
		Search: func(context.Context, string, int) ([]sdk.SearchResult, error) {
			return nil, nil
		},
	}); err == nil {
		t.Fatal("duplicate kind accepted")
	}
	if _, err := registry.Register(sdk.SearchProvider{
		Kind:  "bad-secret",
		Title: sdk.LocalizedText{"zh-CN": "错误"},
		Settings: sdk.SettingsSchema{
			Type: "object",
			Properties: map[string]sdk.SettingField{
				"token": {Type: "string", Secret: true, Default: "nope"},
			},
		},
		Search: func(context.Context, string, int) ([]sdk.SearchResult, error) { return nil, nil },
	}); err == nil {
		t.Fatal("secret with default accepted")
	}
	dispose()
	if _, ok := registry.Get("legacy"); ok || len(registry.Kinds()) != 0 {
		t.Fatal("dispose did not remove provider")
	}
}
