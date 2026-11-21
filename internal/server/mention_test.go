package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/local"
	"github.com/daidr/Ariadne/internal/sources"
	"github.com/daidr/Ariadne/internal/workspaces"
)

func TestMentionFilesAndReferenceStatusHTTP(t *testing.T) {
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
	requests := []apiRequest{
		{Method: "GET", Path: "/api/projects/prj_a/mention-files"},
		{Method: "GET", Path: "/api/projects/prj_a/mention-files?q=file"},
		{Method: "GET", Path: "/api/projects/prj_a/mention-files?q=not-found&conversationId=coo_a"},
		{Method: "GET", Path: "/api/projects/prj_a/mention-files?conversationId=missing"},
		{Method: "GET", Path: "/api/projects/prj_a/references/status"},
	}
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
	for _, item := range requests {
		request := httptest.NewRequest(item.Method, item.Path, nil)
		request.Header.Set("Authorization", "Bearer test-token")
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		var body any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if response.Code >= 500 {
			t.Fatal(response.Body.String())
		}
	}
}
