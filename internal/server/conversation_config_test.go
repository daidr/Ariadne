package server

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/daidr/Ariadne/internal/core"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
	"github.com/daidr/Ariadne/internal/settings"
)

func TestModelSelectionMemoryUndoAndReferenceStatus(t *testing.T) {
	config := object{"instructions": "keep line", "models": object{"providers": []any{object{"id": "p", "kind": "fixture", "settings": object{}}}, "models": []any{object{"id": "m", "provider": "p", "model": "fixture"}}, "roles": object{}}}
	sql := `INSERT INTO conversations(id,project_id,role) VALUES('thr_a','prj_a','thread');
 INSERT INTO threads(id,project_id,title,created_at,updated_at) VALUES('thr_a','prj_a','work',0,0);
 INSERT INTO memories(id,project_id,text,author,created_at,updated_at) VALUES('mem_a','prj_a','local memory','coo_a',0,0);
 INSERT INTO notices(id,project_id,conversation_id,kind,payload,created_at) VALUES
 ('notice_local','prj_a','coo_a','memory','{"memoryId":"mem_a","action":"saved","previous":null}',0),
 ('notice_all','prj_a','coo_a','memory-everywhere','{"action":"saved","text":"keep line"}',0),
 ('notice_removed','prj_a','coo_a','memory-everywhere','{"action":"removed","text":"restore line"}',0);`
	no := false
	beforeRestore := []apiRequest{
		{Method: "PUT", Path: "/api/threads/thr_a/model", Body: object{"model": "m"}, Auth: &no},
		{Method: "PUT", Path: "/api/threads/thr_a/model", Body: object{}},
		{Method: "PUT", Path: "/api/threads/thr_a/model", Body: object{"model": 42}},
		{Method: "PUT", Path: "/api/threads/thr_a/model", Body: object{"model": "missing"}},
		{Method: "PUT", Path: "/api/threads/thr_a/model", Body: object{"model": "m"}},
		{Method: "PUT", Path: "/api/threads/thr_a/model", Body: object{"model": nil}},
		{Method: "PUT", Path: "/api/threads/missing/model", Body: object{"model": nil}},
		{Method: "POST", Path: "/api/projects/prj_a/notices/notice_local/undo"},
		{Method: "POST", Path: "/api/projects/prj_a/notices/notice_local/undo"},
		{Method: "POST", Path: "/api/projects/prj_a/notices/notice_all/undo"},
		{Method: "POST", Path: "/api/projects/prj_a/notices/notice_all/undo"},
	}
	afterRestore := []apiRequest{
		{Method: "POST", Path: "/api/projects/prj_a/notices/notice_removed/undo"},
		{Method: "POST", Path: "/api/projects/other/notices/notice_removed/undo"},
		{Method: "GET", Path: "/api/projects/prj_a/references/status"},
		{Method: "GET", Path: "/api/projects/missing/references/status"},
	}
	dir := t.TempDir()
	now := func() time.Time { return time.UnixMilli(1800000000000) }
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(dir, "config.sqlite"), Now: now})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if _, err := repo.Store.Append(t.Context(), traceSeed()[:1]); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Store.DB().ExecContext(t.Context(), sql); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(encoded(config)), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := settings.Open(configPath)
	if err != nil {
		t.Fatal(err)
	}
	api, err := New(Options{Repository: repo, Token: "test-token", Settings: store, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	server := httptest.NewServer(api)
	defer server.Close()
	settingsClient := ariadnev1connect.NewSettingsServiceClient(
		server.Client(),
		server.URL+rpcPrefix,
		connect.WithInterceptors(bearerClientInterceptor("test-token")),
	)
	run := func(items []apiRequest) {
		for _, item := range items {
			request := httptest.NewRequest(item.Method, item.Path, strings.NewReader(encoded(item.Body)))
			if item.Auth == nil || *item.Auth {
				request.Header.Set("Authorization", "Bearer test-token")
			}
			response := httptest.NewRecorder()
			api.ServeHTTP(response, request)
			var body any = response.Body.String()
			_ = json.Unmarshal(response.Body.Bytes(), &body)
			if response.Code >= 500 {
				t.Fatal(response.Body.String())
			}
		}
	}
	run(beforeRestore)
	workBefore, err := settingsClient.GetWorkSettings(t.Context(), connect.NewRequest(&ariadnev1.GetWorkSettingsRequest{}))
	if err != nil || workBefore.Msg.GetWork().GetInstructions() != "" {
		t.Fatalf("unexpected work settings after removing global memory: %+v %v", workBefore, err)
	}
	run(afterRestore)
	workAfter, err := settingsClient.GetWorkSettings(t.Context(), connect.NewRequest(&ariadnev1.GetWorkSettingsRequest{}))
	if err != nil || workAfter.Msg.GetWork().GetInstructions() != "restore line" {
		t.Fatalf("unexpected work settings after restoring global memory: %+v %v", workAfter, err)
	}
	memories, err := repo.ListMemories(t.Context(), "prj_a")
	if err != nil || len(memories) != 0 {
		t.Fatal("memory undo did not release row", memories, err)
	}
	if store.Current()["instructions"] != "restore line" {
		t.Fatal("global instruction undo failed", store.Current()["instructions"])
	}
}
