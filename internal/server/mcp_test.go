package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/eventstore"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
	"github.com/daidr/Ariadne/internal/mcp"
	"github.com/daidr/Ariadne/internal/settings"
)

func TestMCPSettingsCheckAndProjectSelectionConnect(t *testing.T) {
	t.Parallel()
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
		_ = json.NewDecoder(r.Body).Decode(&message)
		response := func(result any) {
			_ = json.NewEncoder(w).Encode(object{"jsonrpc": "2.0", "id": message.ID, "result": result})
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

	seed := []eventstore.NewEvent{{Type: "project.created", ProjectID: "prj_a", Data: json.RawMessage(`{"name":"Project","coordinatorId":"crd_a"}`)}}
	directory := t.TempDir()
	repo, err := core.Open(t.Context(), core.Options{Path: ":memory:", Now: func() time.Time { return time.UnixMilli(1800000000000) }})
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
	server := httptest.NewServer(api)
	defer server.Close()

	client := ariadnev1connect.NewSettingsServiceClient(
		server.Client(),
		server.URL+rpcPrefix,
		connect.WithInterceptors(bearerClientInterceptor("test-token")),
	)
	checkNoLeak := func(msg proto.Message) {
		t.Helper()
		raw, err := proto.Marshal(msg)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "Bearer private") {
			t.Fatal("MCP secret leaked in response")
		}
	}
	makeServer := func(address, mark string) *ariadnev1.McpServerInput {
		return &ariadnev1.McpServerInput{
			Id:        "server",
			Name:      "Server",
			Transport: "http",
			Url:       &address,
			Headers:   []*ariadnev1.SecretPair{{Name: "Authorization", Value: "Bearer private"}},
			Tools:     map[string]string{"read": mark},
		}
	}

	initial, err := client.GetMcpSettings(t.Context(), connect.NewRequest(&ariadnev1.GetMcpSettingsRequest{}))
	if err != nil || len(initial.Msg.GetServers()) != 0 {
		t.Fatalf("GetMcpSettings: %+v %v", initial, err)
	}

	saved, err := client.SaveMcpSettings(t.Context(), connect.NewRequest(&ariadnev1.SaveMcpSettingsRequest{
		Servers: []*ariadnev1.McpServerInput{makeServer(service.URL, "read")},
	}))
	if err != nil || len(saved.Msg.GetServers()) != 1 {
		t.Fatalf("SaveMcpSettings: %+v %v", saved, err)
	}
	checkNoLeak(saved.Msg)

	checked, err := client.CheckMcpServer(t.Context(), connect.NewRequest(&ariadnev1.CheckMcpServerRequest{ServerId: "server"}))
	if err != nil || checked.Msg.GetStatus().GetState() != "ready" {
		t.Fatalf("CheckMcpServer: %+v %v", checked, err)
	}
	checkNoLeak(checked.Msg)

	projectClient := ariadnev1connect.NewProjectServiceClient(
		server.Client(),
		server.URL+rpcPrefix,
		connect.WithInterceptors(bearerClientInterceptor("test-token")),
	)
	putProjectMCP := func(servers []string, wantCode connect.Code) {
		t.Helper()
		_, err := projectClient.UpdateProject(t.Context(), connect.NewRequest(&ariadnev1.UpdateProjectRequest{
			ProjectId:  "prj_a",
			McpServers: &ariadnev1.ProjectStringList{Values: servers},
		}))
		if wantCode == 0 {
			if err != nil {
				t.Fatalf("UpdateProject(mcp) failed: %v", err)
			}
			return
		}
		if connect.CodeOf(err) != wantCode {
			t.Fatalf("UpdateProject(mcp) code = %v (err=%v), want %v", connect.CodeOf(err), err, wantCode)
		}
	}
	putProjectMCP([]string{"server"}, 0)
	putProjectMCP([]string{"unknown"}, connect.CodeInvalidArgument)

	if _, err := client.SaveMcpSettings(t.Context(), connect.NewRequest(&ariadnev1.SaveMcpSettingsRequest{
		Servers: []*ariadnev1.McpServerInput{makeServer(service.URL+"/bad", "read")},
	})); err != nil {
		t.Fatalf("SaveMcpSettings(bad): %v", err)
	}
	badChecked, err := client.CheckMcpServer(t.Context(), connect.NewRequest(&ariadnev1.CheckMcpServerRequest{ServerId: "server"}))
	if err != nil || badChecked.Msg.GetStatus().GetState() != "failed" {
		t.Fatalf("expected failed state for /bad MCP server, got %+v %v", badChecked, err)
	}

	if _, err := client.SaveMcpSettings(t.Context(), connect.NewRequest(&ariadnev1.SaveMcpSettingsRequest{})); err != nil {
		t.Fatalf("SaveMcpSettings(clear): %v", err)
	}
	if _, err := client.CheckMcpServer(t.Context(), connect.NewRequest(&ariadnev1.CheckMcpServerRequest{ServerId: "unknown"})); err == nil {
		t.Fatal("expected unknown MCP check to fail")
	} else if connectErr, _ := extractLocalizedError(t, err); connectErr.Code() != connect.CodeNotFound {
		t.Fatalf("expected CodeNotFound, got %v", connectErr.Code())
	}
}
