package server

import (
	"context"
	"encoding/json"
	"github.com/daidr/Ariadne/internal/legacytest"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/eventstore"
	"github.com/daidr/Ariadne/internal/mcp"
	"github.com/daidr/Ariadne/internal/settings"
)

func TestMCPSettingsCheckAndProjectSelectionHTTPMatchLegacy(t *testing.T) {
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.WriteHeader(405)
			return
		}
		if r.Method == "DELETE" {
			return
		}
		if r.URL.Path == "/bad" {
			w.WriteHeader(503)
			return
		}
		if r.Header.Get("Authorization") != "Bearer private" {
			t.Error("missing MCP credentials")
		}
		var message struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&message)
		response := func(result any) {
			json.NewEncoder(w).Encode(object{"jsonrpc": "2.0", "id": message.ID, "result": result})
		}
		switch message.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "session")
			response(object{"protocolVersion": "2025-06-18", "serverInfo": object{"name": "server", "version": "1"}})
		case "tools/list":
			response(object{"tools": []any{object{"name": "read", "inputSchema": object{"type": "string"}, "annotations": object{}}}})
		default:
			w.WriteHeader(202)
		}
	}))
	defer service.Close()
	config := func(url, mark string) object {
		return object{"servers": []any{object{"id": "server", "name": "Server", "transport": "http", "url": url, "headers": []any{object{"name": "Authorization", "value": "Bearer private"}}, "tools": object{"read": mark}}}}
	}
	seed := []eventstore.NewEvent{{Type: "project.created", ProjectID: "prj_a", Data: json.RawMessage(`{"name":"Project","coordinatorId":"crd_a"}`)}}
	requests := []apiRequest{{Method: "GET", Path: "/api/settings/mcp"}, {Method: "PUT", Path: "/api/settings/mcp", Body: config(service.URL, "read")}, {Method: "POST", Path: "/api/settings/mcp/server/check"}, {Method: "GET", Path: "/api/settings/mcp"}, {Method: "PUT", Path: "/api/projects/prj_a/mcp", Body: object{"servers": []any{"server"}}}, {Method: "PUT", Path: "/api/projects/prj_a/mcp", Body: object{"servers": []any{"unknown"}}}, {Method: "PUT", Path: "/api/settings/mcp", Body: config(service.URL, "off")}, {Method: "PUT", Path: "/api/settings/mcp", Body: config(service.URL+"/bad", "read")}, {Method: "POST", Path: "/api/settings/mcp/server/check"}, {Method: "PUT", Path: "/api/settings/mcp", Body: object{"servers": []any{}}}, {Method: "POST", Path: "/api/settings/mcp/unknown/check"}}
	var expected []any
	if bun, err := exec.LookPath("bun"); err == nil {
		command := legacytest.Command(t, bun, "packages/server/src/testing/go-api-fixture.ts")
		command.Dir = filepath.Join("..", "..")
		command.Stdin = strings.NewReader(encoded(object{"settings": true, "mcp": true, "seed": seed, "sql": "SELECT 1", "requests": requests}))
		data, err := command.CombinedOutput()
		if err != nil || json.Unmarshal(data, &expected) != nil {
			t.Fatalf("legacy %v\n%s", err, data)
		}
	} else if os.Getenv("ARIADNE_REQUIRE_LEGACY") == "1" {
		t.Fatal(err)
	}
	directory := t.TempDir()
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(directory, "data.sqlite"), Now: func() time.Time { return time.UnixMilli(1800000000000) }})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if _, err := repo.Store.Append(t.Context(), seed); err != nil {
		t.Fatal(err)
	}
	store, err := settings.Open(filepath.Join(directory, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	hub, err := mcp.NewHub(mcp.HubOptions{Servers: store.MCPServers})
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close(context.Background())
	api, err := New(Options{Repository: repo, Token: "test-token", Settings: store, MCP: hub})
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
		if response.Code >= 500 || strings.Contains(response.Body.String(), "Bearer private") {
			t.Fatal(actual)
		}
	}
}
