package server

import (
	"net/http/httptest"
	"os"
	"path/filepath"
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
	srv := httptest.NewServer(api)
	defer srv.Close()

	auth := connect.WithInterceptors(bearerClientInterceptor("test-token"))
	threadClient := ariadnev1connect.NewThreadServiceClient(srv.Client(), srv.URL+rpcPrefix, auth)
	unauthThread := ariadnev1connect.NewThreadServiceClient(srv.Client(), srv.URL+rpcPrefix)
	convClient := ariadnev1connect.NewConversationServiceClient(srv.Client(), srv.URL+rpcPrefix, auth)
	wsClient := ariadnev1connect.NewWorkspaceServiceClient(srv.Client(), srv.URL+rpcPrefix, auth)

	// Unauthenticated SetThreadModel fails.
	_, err = unauthThread.SetThreadModel(t.Context(), connect.NewRequest(&ariadnev1.SetThreadModelRequest{
		ThreadId:    "thr_a",
		ModelAction: &ariadnev1.SetThreadModelRequest_Model{Model: "m"},
	}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("expected unauthenticated, got %v", err)
	}

	// Missing action or unknown model fails with InvalidArgument.
	_, err = threadClient.SetThreadModel(t.Context(), connect.NewRequest(&ariadnev1.SetThreadModelRequest{ThreadId: "thr_a"}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected invalid argument for empty action, got %v", err)
	}
	_, err = threadClient.SetThreadModel(t.Context(), connect.NewRequest(&ariadnev1.SetThreadModelRequest{
		ThreadId:    "thr_a",
		ModelAction: &ariadnev1.SetThreadModelRequest_Model{Model: "missing"},
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected invalid argument for unknown model, got %v", err)
	}

	// Setting valid model and resetting to default succeed.
	setResp, err := threadClient.SetThreadModel(t.Context(), connect.NewRequest(&ariadnev1.SetThreadModelRequest{
		ThreadId:    "thr_a",
		ModelAction: &ariadnev1.SetThreadModelRequest_Model{Model: "m"},
	}))
	if err != nil || setResp.Msg.Thread.Model == nil || *setResp.Msg.Thread.Model != "m" {
		t.Fatalf("set model failed: resp=%+v err=%v", setResp, err)
	}
	resetResp, err := threadClient.SetThreadModel(t.Context(), connect.NewRequest(&ariadnev1.SetThreadModelRequest{
		ThreadId:    "thr_a",
		ModelAction: &ariadnev1.SetThreadModelRequest_UseDefault{UseDefault: true},
	}))
	if err != nil || resetResp.Msg.Thread.Model != nil {
		t.Fatalf("reset model failed: resp=%+v err=%v", resetResp, err)
	}
	_, err = threadClient.SetThreadModel(t.Context(), connect.NewRequest(&ariadnev1.SetThreadModelRequest{
		ThreadId:    "missing",
		ModelAction: &ariadnev1.SetThreadModelRequest_UseDefault{UseDefault: true},
	}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("expected not found for missing thread, got %v", err)
	}

	// Undo local memory notice twice (idempotent), undo global instruction notices, and check missing project.
	for _, call := range []struct {
		project string
		notice  string
		wantErr bool
	}{
		{"prj_a", "notice_local", false},
		{"prj_a", "notice_local", false},
		{"prj_a", "notice_all", false},
		{"prj_a", "notice_all", false},
		{"prj_a", "notice_removed", false},
		{"other", "notice_removed", true},
	} {
		_, err := convClient.UndoNotice(t.Context(), connect.NewRequest(&ariadnev1.UndoNoticeRequest{
			ProjectId: call.project,
			NoticeId:  call.notice,
		}))
		if call.wantErr && err == nil {
			t.Fatalf("expected error for undo %s/%s", call.project, call.notice)
		}
		if !call.wantErr && err != nil {
			t.Fatalf("unexpected error for undo %s/%s: %v", call.project, call.notice, err)
		}
	}

	// Reference status for valid and missing project.
	statusResp, err := wsClient.GetReferenceStatus(t.Context(), connect.NewRequest(&ariadnev1.GetReferenceStatusRequest{ProjectId: "prj_a"}))
	if err != nil || len(statusResp.Msg.Failed) != 0 {
		t.Fatalf("reference status failed: resp=%+v err=%v", statusResp, err)
	}
	_, err = wsClient.GetReferenceStatus(t.Context(), connect.NewRequest(&ariadnev1.GetReferenceStatusRequest{ProjectId: "missing"}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("expected not found for missing project, got %v", err)
	}

	memories, err := repo.ListMemories(t.Context(), "prj_a")
	if err != nil || len(memories) != 0 {
		t.Fatal("memory undo did not release row", memories, err)
	}
	if store.Current()["instructions"] != "restore line" {
		t.Fatal("global instruction undo failed", store.Current()["instructions"])
	}
}
