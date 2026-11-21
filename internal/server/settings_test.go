package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/settings"
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
	api, err := New(Options{Repository: repo, Token: "test-token", Settings: store})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	for _, item := range requests {
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
	}
}
