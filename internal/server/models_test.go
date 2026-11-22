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
	"github.com/daidr/Ariadne/internal/models"
	"github.com/daidr/Ariadne/internal/settings"
)

func TestModelSettingsHTTPAndRealProviderCheck(t *testing.T) {
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
	for _, provider := range models.HTTPBuiltinProviders(models.HTTPOptions{Client: service.Client()}) {
		remove, err := builtins.Registry.Register(provider)
		if err != nil {
			t.Fatal(err)
		}
		defer remove()
	}
	api, err := New(Options{Repository: repo, Token: "test-token", Settings: store, Models: builtins.Registry})
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
		if response.Code != 200 {
			t.Fatal(object{"status": response.Code, "body": body})
		}
		if strings.Contains(response.Body.String(), `"apiKey":"private"`) {
			t.Fatal("API key leaked to settings page")
		}
	}
}
