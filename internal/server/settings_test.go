package server

import (
	"encoding/json"
	"github.com/daidr/Ariadne/internal/legacytest"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/settings"
)

func TestSettingsHTTPMatchesLegacy(t *testing.T) {
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
	var expected []any
	if bun, err := exec.LookPath("bun"); err == nil {
		input := object{"settings": true, "seed": []any{}, "sql": "SELECT 1", "requests": requests}
		command := legacytest.Command(t, bun, "packages/server/src/testing/go-api-fixture.ts")
		command.Dir = filepath.Join("..", "..")
		command.Stdin = strings.NewReader(encoded(input))
		data, err := command.CombinedOutput()
		if err != nil || json.Unmarshal(data, &expected) != nil {
			t.Fatalf("legacy: %v\n%s", err, data)
		}
	} else if os.Getenv("ARIADNE_REQUIRE_LEGACY") == "1" {
		t.Fatal(err)
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
	for index, item := range requests {
		request := httptest.NewRequest(item.Method, "http://test.invalid"+item.Path, strings.NewReader(encoded(item.Body)))
		request.Header.Set("Authorization", "Bearer test-token")
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		var body any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		actual := object{"status": response.Code, "body": body}
		if expected != nil {
			if encoded(canonical(actual)) != encoded(canonical(expected[index])) {
				t.Fatalf("%s %s\ngot %s\nwant %s", item.Method, item.Path, encoded(actual), encoded(expected[index]))
			}
		}
		if response.Code >= 500 {
			t.Fatal(actual)
		}
	}
}
