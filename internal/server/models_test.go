package server

import (
	"context"
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
	"github.com/daidr/Ariadne/internal/models"
	"github.com/daidr/Ariadne/internal/settings"
)

func TestModelSettingsHTTPAndRealProviderCheckMatchLegacy(t *testing.T) {
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer private" {
			t.Error("wrong model check request", r.URL.Path, r.Header)
		}
		var body object
		json.NewDecoder(r.Body).Decode(&body)
		if body["model"] == "refused" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(401)
			io.WriteString(w, `{"error":{"message":"unauthorized"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n")
	}))
	defer service.Close()
	provider := object{"id": "p", "kind": "openai-compatible", "name": "主力", "settings": object{"baseUrl": service.URL + "/v1", "apiKey": "private"}}
	body := func(model string) object {
		return object{"providers": []any{provider}, "models": []any{object{"id": "m", "provider": "p", "model": model}}, "roles": object{"thread": object{"model": "m"}}}
	}
	requests := []apiRequest{{Method: "GET", Path: "/api/settings/models"}, {Method: "PUT", Path: "/api/settings/models", Body: body("working")}, {Method: "GET", Path: "/api/settings/models"}, {Method: "PUT", Path: "/api/settings/models", Body: body("refused")}, {Method: "GET", Path: "/api/settings/models"}}
	var expected []any
	if bun, err := exec.LookPath("bun"); err == nil {
		input := object{"settings": true, "models": true, "seed": []any{}, "sql": "SELECT 1", "requests": requests}
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
	builtins, err := models.NewBuiltins(models.BuiltinOptions{DataDir: filepath.Join(directory, "providers"), Client: service.Client()})
	if err != nil {
		t.Fatal(err)
	}
	defer builtins.Close(context.Background())
	api, err := New(Options{Repository: repo, Token: "test-token", Settings: store, Models: builtins.Registry})
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
		if expected != nil && encoded(canonical(actual)) != encoded(canonical(expected[index])) {
			t.Fatalf("%s %s\ngot %s\nwant %s", item.Method, item.Path, encoded(actual), encoded(expected[index]))
		}
		if response.Code != 200 {
			t.Fatal(actual)
		}
		if strings.Contains(response.Body.String(), `"apiKey":"private"`) {
			t.Fatal("API key leaked to settings page")
		}
	}
}
