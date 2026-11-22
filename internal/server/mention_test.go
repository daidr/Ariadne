package server

import (
	"context"
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
	"github.com/daidr/Ariadne/internal/local"
	"github.com/daidr/Ariadne/internal/sources"
	"github.com/daidr/Ariadne/internal/workspaces"
)

func TestMentionFilesAndReferenceStatusRPC(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	if err := os.MkdirAll(filepath.Join(source, "src"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"a.txt", "profile.txt", "src/file.ts"} {
		if err := os.WriteFile(filepath.Join(source, filepath.FromSlash(file)), []byte("text"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	definition := object{"kind": "local-directory", "settings": object{"path": source}}
	refs := []any{object{"id": "ref_a", "name": "reference", "kind": "local-directory", "settings": object{"path": source}}}
	sql := `UPDATE projects SET source='` + strings.ReplaceAll(encoded(definition), "'", "''") + `',reference_sources='` + strings.ReplaceAll(encoded(refs), "'", "''") + `' WHERE id='prj_a';`
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(dir, "files.sqlite")})
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
	registry := sources.New()
	registry.Register(local.DirectoryProvider(filepath.Join(dir, "workspaces"), nil, true))
	manager, err := workspaces.New(workspaces.Options{Repository: repo, Sources: registry})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.Close(ctx); err != nil {
			t.Error(err)
		}
	}()
	api, err := New(Options{Repository: repo, Token: "test-token", Sources: registry, Workspaces: manager})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	srv := httptest.NewServer(api)
	defer srv.Close()

	client := ariadnev1connect.NewWorkspaceServiceClient(
		srv.Client(),
		srv.URL+rpcPrefix,
		connect.WithInterceptors(bearerClientInterceptor("test-token")),
	)

	allResp, err := client.ListMentionFiles(t.Context(), connect.NewRequest(&ariadnev1.ListMentionFilesRequest{
		ProjectId: "prj_a",
	}))
	if err != nil || len(allResp.Msg.GetFiles()) != 3 {
		t.Fatalf("unexpected mention files: %+v err=%v", allResp, err)
	}
	filteredResp, err := client.ListMentionFiles(t.Context(), connect.NewRequest(&ariadnev1.ListMentionFilesRequest{
		ProjectId: "prj_a",
		Query:     "file",
	}))
	if err != nil || len(filteredResp.Msg.GetFiles()) != 2 {
		t.Fatalf("unexpected filtered mention files: %+v err=%v", filteredResp, err)
	}
	cooID := "coo_a"
	noneResp, err := client.ListMentionFiles(t.Context(), connect.NewRequest(&ariadnev1.ListMentionFilesRequest{
		ProjectId:      "prj_a",
		ConversationId: &cooID,
		Query:          "not-found",
	}))
	if err != nil || len(noneResp.Msg.GetFiles()) != 0 {
		t.Fatalf("expected 0 files, got %+v err=%v", noneResp, err)
	}
	missingID := "missing"
	if _, err := client.ListMentionFiles(t.Context(), connect.NewRequest(&ariadnev1.ListMentionFilesRequest{
		ProjectId:      "prj_a",
		ConversationId: &missingID,
	})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("expected not found for missing conversation, got %v", err)
	}
	statusResp, err := client.GetReferenceStatus(t.Context(), connect.NewRequest(&ariadnev1.GetReferenceStatusRequest{
		ProjectId: "prj_a",
	}))
	if err != nil || len(statusResp.Msg.GetFailed()) != 0 {
		t.Fatalf("unexpected reference status: %+v err=%v", statusResp, err)
	}
}
