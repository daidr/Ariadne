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
	"time"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/modelcatalog"
	"github.com/daidr/Ariadne/internal/models"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/sdk"
)

func TestModelListingCatalogAndFoundSetupHTTPMatchLegacy(t *testing.T) {
	catalogJSON := `{"openai":{"models":{"big":{"cost":{"input":1,"output":2},"limit":{"context":128000,"output":4096},"modalities":{"input":["text","image"]},"tool_call":true,"reasoning":false,"release_date":"2026-11-01"},"old":{"tool_call":true,"release_date":"2025-01-01"},"embedding":{"tool_call":false,"release_date":"2026-11-02"}}}}`
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/catalog" {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, catalogJSON)
			return
		}
		if r.Header.Get("Authorization") != "Bearer private" {
			t.Error("wrong provider authorization", r.Header)
		}
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"data":[{"id":"unknown","created":5},{"id":"old","created":2},{"id":"big","created":1},{"id":"embedding","created":6},{"id":"big","created":0}]}`)
			return
		}
		if r.URL.Path == "/v1/chat/completions" {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
			return
		}
		http.NotFound(w, r)
	}))
	defer service.Close()
	home := t.TempDir()
	env := map[string]string{"OPENAI_API_KEY": "private", "OPENAI_BASE_URL": service.URL + "/v1"}
	requests := []apiRequest{
		{Method: "POST", Path: "/api/settings/model-list", Body: object{"provider": object{"kind": "openai-compatible", "settings": object{"baseUrl": service.URL + "/v1", "apiKey": "private"}}}},
		{Method: "GET", Path: "/api/settings/model-info?model=big"},
		{Method: "GET", Path: "/api/settings/model-info?model=unknown"},
		{Method: "GET", Path: "/api/settings/model-info?model="},
		{Method: "GET", Path: "/api/settings/found"},
		{Method: "POST", Path: "/api/settings/found/use", Body: object{"id": "openai-compatible:OPENAI_API_KEY"}},
		{Method: "GET", Path: "/api/settings/found"},
		{Method: "GET", Path: "/api/settings/models"},
	}
	var expected []any
	if bun, err := exec.LookPath("bun"); err == nil {
		input := object{"settings": true, "models": true, "catalog": json.RawMessage(catalogJSON), "find": object{"env": env, "home": home, "available": []string{}}, "seed": []any{}, "sql": "SELECT 1", "requests": requests}
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
	catalog := modelcatalog.New(modelcatalog.Options{URL: service.URL + "/catalog", Client: service.Client()})
	defer catalog.Close(context.Background())
	api, err := New(Options{Repository: repo, Token: "test-token", Settings: store, Models: builtins.Registry, Catalog: catalog, FindContext: &sdk.FindContext{Env: env, Home: home, Which: func(string) bool { return false }}, Now: func() time.Time { return time.UnixMilli(1800000000000) }})
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
		if response.Code >= 500 {
			t.Fatal(actual)
		}
		if strings.Contains(response.Body.String(), `"apiKey":"private"`) {
			t.Fatal("discovery leaked credentials")
		}
	}
}
