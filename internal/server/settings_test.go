package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/searches"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/sdk"
)

func TestSettingsHTTP(t *testing.T) {
	searchServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer draft-secret" {
			t.Error("missing search auth")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"results":[]}`)
	}))
	defer searchServer.Close()
	requests := []apiRequest{
		{Method: "GET", Path: "/api/settings/search"},
		{Method: "POST", Path: "/api/settings/search/check", Body: object{"provider": "deepseek", "services": object{"deepseek": object{"apiKey": "draft-secret", "baseUrl": searchServer.URL}}}},
		{Method: "GET", Path: "/api/settings/search"},
		{Method: "PUT", Path: "/api/settings/search", Body: object{"provider": "deepseek", "services": object{"deepseek": object{"apiKey": "saved-private", "baseUrl": "https://example.test/v1/tools/"}}}},
		{Method: "PUT", Path: "/api/settings/search", Body: object{"services": object{"deepseek": object{"apiKey": ""}}}},
		{Method: "PUT", Path: "/api/settings/search", Body: object{"services": object{"deepseek": object{"apiKey": nil}}}},
		{Method: "POST", Path: "/api/settings/search/check", Body: object{}},
		{Method: "GET", Path: "/api/settings/work"}, {Method: "GET", Path: "/api/settings/input"}, {Method: "GET", Path: "/api/settings/concurrency"},
		{Method: "PUT", Path: "/api/settings/work", Body: object{"instructions": " plain words ", "rules": []any{object{"pattern": "git push*", "action": "ask"}}, "limits": object{"commandTimeout": float64(60)}}},
		{Method: "PUT", Path: "/api/settings/work", Body: object{"rules": []any{object{"pattern": "", "action": "ask"}}}},
		{Method: "PUT", Path: "/api/settings/input", Body: object{"submitOn": "mod-enter", "snippets": []any{object{"id": "a", "name": " 常用 ", "text": "prompt"}}, "keys": object{"focus": "alt+f"}}},
		{Method: "PUT", Path: "/api/settings/concurrency", Body: object{"slots": float64(5), "reserved": float64(0)}},
		{Method: "PUT", Path: "/api/settings/concurrency", Body: object{"slots": float64(0), "reserved": float64(0)}},
		{Method: "GET", Path: "/api/settings/work"}, {Method: "GET", Path: "/api/settings/input"}, {Method: "GET", Path: "/api/settings/concurrency"},
	}
	directory := t.TempDir()
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(directory, "data.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	store, err := settings.Open(filepath.Join(directory, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	searchRegistry := searches.New()
	for _, provider := range searches.BuiltinProviders(nil) {
		if _, err := searchRegistry.Register(provider); err != nil {
			t.Fatal(err)
		}
	}
	api, err := New(Options{Repository: repo, Token: "test-token", Settings: store, Searches: searchRegistry})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	for idx, item := range requests {
		request := httptest.NewRequest(item.Method, "http://test.invalid"+item.Path, strings.NewReader(encoded(item.Body)))
		request.Header.Set("Authorization", "Bearer test-token")
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		var body any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if response.Code >= 500 {
			t.Fatal(object{"status": response.Code, "body": body})
		}
		if strings.Contains(response.Body.String(), "saved-private") || strings.Contains(response.Body.String(), "draft-secret") {
			t.Fatalf("request %d leaked secret: %s", idx, response.Body.String())
		}
		if idx == 1 {
			result, _ := body.(map[string]any)
			if response.Code != 200 || result["ok"] != true {
				t.Fatalf("draft search check = %d %v", response.Code, body)
			}
		}
		if idx == 3 {
			result, _ := body.(map[string]any)
			if response.Code != 200 || result["provider"] != "deepseek" || result["ready"] != true {
				t.Fatalf("saved search view = %d %v", response.Code, body)
			}
		}
	}
	// Verify dynamic external search provider appears automatically in GET /api/settings/search.
	if _, err := searchRegistry.Register(sdk.SearchProvider{
		Kind:  "custom-ext",
		Title: sdk.LocalizedText{"zh-CN": "扩展搜索", "en": "Extension Search"},
		Search: func(_ context.Context, _ string, _ int) ([]sdk.SearchResult, error) {
			return []sdk.SearchResult{}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "http://test.invalid/api/settings/search", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"kind":"custom-ext"`) {
		t.Fatalf("dynamic search provider missing: %d %s", rec.Code, rec.Body.String())
	}
}
