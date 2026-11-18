package server

import (
	"context"
	"encoding/json"
	"github.com/daidr/Ariadne/internal/legacytest"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/eventstore"
	"github.com/daidr/Ariadne/internal/sources"
	"github.com/daidr/Ariadne/internal/workspaces"
	"github.com/daidr/Ariadne/sdk"
)

func workspaceHTTPSeed() []eventstore.NewEvent {
	event := func(kind string, data any) eventstore.NewEvent {
		return eventstore.NewEvent{ProjectID: "prj_a", Type: kind, Data: json.RawMessage(encoded(data))}
	}
	return []eventstore.NewEvent{event("project.created", object{"name": "project", "coordinatorId": "coo_a"}), event("project.source.set", object{"source": object{"kind": "fixture", "settings": object{}}}), event("thread.created", object{"threadId": "thr_a", "title": "task", "nodeId": nil}), event("workspace.opened", object{"threadId": "thr_a", "label": "fixture", "branch": "work/thread", "base": "integration"})}
}
func TestWorkspaceHTTPMatchesLegacy(t *testing.T) {
	no := false
	file := "/api/threads/thr_a/changes/file?from=" + strings.Repeat("a", 40) + "&head=" + strings.Repeat("b", 40) + "&path=file.txt"
	requests := []apiRequest{
		{Method: "GET", Path: "/api/threads/thr_a/changes"}, {Method: "GET", Path: file}, {Method: "GET", Path: "/api/threads/thr_a/changes/file?from=invalid"},
		{Method: "GET", Path: "/api/worktrees"}, {Method: "POST", Path: "/api/threads/thr_a/merge", Body: object{}, Auth: &no}, {Method: "POST", Path: "/api/threads/thr_a/merge", Body: object{"method": "bad"}},
		{Method: "POST", Path: "/api/threads/thr_a/merge", Body: object{"message": "conflict"}}, {Method: "POST", Path: "/api/threads/thr_a/merge", Body: object{"method": "merge"}}, {Method: "POST", Path: "/api/threads/thr_a/merge/undo"},
		{Method: "PUT", Path: "/api/projects/prj_a/settings", Body: object{"settings": object{"delivery": object{"mode": "external", "instructions": "submit to platform"}}}},
		{Method: "GET", Path: "/api/threads/thr_a/changes"}, {Method: "POST", Path: "/api/threads/thr_a/merge", Body: object{}},
		{Method: "PUT", Path: "/api/projects/prj_a/settings", Body: object{}}, {Method: "GET", Path: "/api/projects/prj_a/settings"},
		{Method: "DELETE", Path: "/api/projects/prj_a/worktrees/thr_a"}, {Method: "POST", Path: "/api/threads/thr_a/pull-request/refresh"}, {Method: "DELETE", Path: "/api/projects/prj_a"}, {Method: "GET", Path: "/api/projects/prj_a"},
	}
	var expected []any
	if bun, err := exec.LookPath("bun"); err == nil {
		cmd := legacytest.Command(t, bun, "packages/server/src/testing/go-api-fixture.ts")
		cmd.Dir = filepath.Join("..", "..")
		cmd.Stdin = strings.NewReader(encoded(object{"workspaces": true, "seed": workspaceHTTPSeed(), "sql": "SELECT 1", "requests": requests}))
		data, err := cmd.CombinedOutput()
		if err != nil || json.Unmarshal(data, &expected) != nil {
			t.Fatalf("legacy %v\n%s", err, data)
		}
	} else if os.Getenv("ARIADNE_REQUIRE_LEGACY") == "1" {
		t.Fatal(err)
	}
	now := func() time.Time { return time.UnixMilli(1_800_000_000_000) }
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(t.TempDir(), "data.sqlite"), Now: now})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if _, err := repo.Store.Append(t.Context(), workspaceHTTPSeed()); err != nil {
		t.Fatal(err)
	}
	registry := sources.New()
	registry.Register(sdk.ContextProvider{Info: sdk.ContextProviderInfo{Kind: "fixture", Provides: []string{"workspace"}}, Connect: func(context.Context, map[string]any) (sdk.Capabilities, error) {
		return sdk.Capabilities{Workspace: &sdk.WorkspaceCapability{Isolated: true, Branches: &sdk.BranchCapability{
			Changes: func(context.Context, sdk.ThreadBranch, string) (*sdk.BranchChanges, error) {
				return &sdk.BranchChanges{Branch: "work/thread", Base: "integration", From: strings.Repeat("a", 40), Head: strings.Repeat("b", 40), Commits: []sdk.BranchCommit{}, Files: []sdk.ChangedFile{}, Uncommitted: []string{}}, nil
			},
			File: func(context.Context, sdk.FileVersionRequest) (sdk.FileVersions, error) {
				before, after := "before", "after"
				return sdk.FileVersions{Before: &before, After: &after}, nil
			},
			Merge: func(_ context.Context, input sdk.MergeRequest) (sdk.MergeOutcome, error) {
				if input.Message == "conflict" {
					return sdk.MergeOutcome{Kind: "conflict", Files: []string{"file.txt"}}, nil
				}
				return sdk.MergeOutcome{Kind: "merged", SHA: strings.Repeat("c", 40), Head: strings.Repeat("b", 40)}, nil
			},
			UndoMerge: func(context.Context, sdk.UndoMergeRequest) error { return nil }, Remove: func(context.Context, string, sdk.WorktreeRemoveOptions) (bool, error) { return true, nil }, Worktrees: func(context.Context, bool) ([]sdk.WorktreeInfo, error) { return []sdk.WorktreeInfo{}, nil },
		}}}, nil
	}})
	manager, err := workspaces.New(workspaces.Options{Repository: repo, Sources: registry})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	api, err := New(Options{Repository: repo, Token: "test-token", Now: now, Workspaces: manager, Sources: registry})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	for i, item := range requests {
		request := httptest.NewRequest(item.Method, item.Path, strings.NewReader(encoded(item.Body)))
		if item.Auth == nil || *item.Auth {
			request.Header.Set("Authorization", "Bearer test-token")
		}
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		var body any = response.Body.String()
		if response.Body.Len() > 0 {
			json.Unmarshal(response.Body.Bytes(), &body)
		}
		actual := object{"status": response.Code, "body": body}
		if expected != nil && encoded(canonical(actual)) != encoded(canonical(expected[i])) {
			t.Fatalf("%s %s\nGo %s\nTS %s", item.Method, item.Path, encoded(actual), encoded(expected[i]))
		}
		if response.Code >= 500 {
			t.Fatal(response.Body.String())
		}
	}
}
