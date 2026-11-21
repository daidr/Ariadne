package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
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

func TestPullRequestChecksReadTrackedCommit(t *testing.T) {
	ctx := t.Context()
	repo, err := core.Open(ctx, core.Options{Path: filepath.Join(t.TempDir(), "data.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if _, err := repo.Store.Append(ctx, workspaceHTTPSeed()); err != nil {
		t.Fatal(err)
	}
	head := strings.Repeat("a", 40)
	if err := repo.SyncPullRequest(ctx, "thr_a", object{"number": 4, "state": "open", "headSha": head, "checks": object{"state": "pending"}}, nil); err != nil {
		t.Fatal(err)
	}
	registry := sources.New()
	if _, err := registry.Register(sdk.ContextProvider{
		Info: sdk.ContextProviderInfo{Kind: "fixture", Provides: []string{"repo"}},
		Connect: func(context.Context, map[string]any) (sdk.Capabilities, error) {
			return sdk.Capabilities{Repo: &sdk.RepoCapability{Checks: func(_ context.Context, sha string) ([]sdk.CheckInfo, error) {
				if sha != head {
					t.Errorf("checked %q, want %q", sha, head)
				}
				return []sdk.CheckInfo{
					{ID: "build", Name: "Build", Status: "in_progress"},
					{ID: "lint", Name: "Lint", Status: "completed", Conclusion: new("success")},
				}, nil
			}}}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	manager, err := workspaces.New(workspaces.Options{Repository: repo, Sources: registry})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	api, err := New(Options{Repository: repo, Token: "test-token", Workspaces: manager})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	request := httptest.NewRequest("GET", "/api/threads/thr_a/pull-request/checks?headSha=other", nil)
	request.Header.Set("Authorization", "Bearer test-token")
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	var result struct {
		HeadSHA string          `json:"headSha"`
		Checks  []sdk.CheckInfo `json:"checks"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil ||
		result.HeadSHA != head || len(result.Checks) != 2 || result.Checks[0].Status != "in_progress" ||
		result.Checks[1].Conclusion == nil || *result.Checks[1].Conclusion != "success" {
		t.Fatalf("checks: %d %s", response.Code, response.Body)
	}
}

func TestChangesProbeAndMergeExpectedHead(t *testing.T) {
	ctx := t.Context()
	repo, err := core.Open(ctx, core.Options{Path: filepath.Join(t.TempDir(), "data.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if _, err := repo.Store.Append(ctx, workspaceHTTPSeed()); err != nil {
		t.Fatal(err)
	}
	initial, base := strings.Repeat("a", 40), strings.Repeat("b", 40)
	revisions := sdk.BranchRevisions{Head: initial, BaseHead: base}
	diffs := 0
	registry := sources.New()
	registry.Register(sdk.ContextProvider{
		Info: sdk.ContextProviderInfo{Kind: "fixture", Provides: []string{"workspace"}},
		Connect: func(context.Context, map[string]any) (sdk.Capabilities, error) {
			return sdk.Capabilities{Workspace: &sdk.WorkspaceCapability{Isolated: true, Branches: &sdk.BranchCapability{
				Revisions: func(context.Context, sdk.ThreadBranch, string) (sdk.BranchRevisions, error) {
					return revisions, nil
				},
				Changes: func(context.Context, sdk.ThreadBranch, string) (*sdk.BranchChanges, error) {
					diffs++
					return &sdk.BranchChanges{Head: revisions.Head, BaseHead: revisions.BaseHead}, nil
				},
				Merge: func(_ context.Context, input sdk.MergeRequest) (sdk.MergeOutcome, error) {
					if input.ExpectedHead != initial {
						t.Fatalf("expected head = %q", input.ExpectedHead)
					}
					return sdk.MergeOutcome{Kind: "moved"}, nil
				},
			}}}, nil
		},
	})
	manager, err := workspaces.New(workspaces.Options{Repository: repo, Sources: registry})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	api, err := New(Options{Repository: repo, Token: "test-token", Workspaces: manager, Sources: registry})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	call := func(method, path, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, "/api/threads/thr_a"+path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer test-token")
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		return response
	}
	probe := "/changes?head=" + initial + "&baseHead=" + base
	if response := call("GET", probe, ""); response.Code != 200 || !strings.Contains(response.Body.String(), `"unchanged":true`) || diffs != 0 {
		t.Fatalf("unchanged refs: %d %s; diff calls: %d", response.Code, response.Body, diffs)
	}
	revisions.BaseHead = strings.Repeat("d", 40)
	if response := call("GET", probe, ""); response.Code != 200 || !strings.Contains(response.Body.String(), revisions.BaseHead) || diffs != 1 {
		t.Fatalf("changed base: %d %s; diff calls: %d", response.Code, response.Body, diffs)
	}
	revisions.Head = strings.Repeat("c", 40)
	if response := call("POST", "/merge", encoded(object{"method": "squash", "expectedHead": initial})); response.Code != 200 || !strings.Contains(response.Body.String(), `"kind":"moved"`) {
		t.Fatalf("merge of older comparison: %d %s", response.Code, response.Body)
	}
}

func TestWorkspaceHTTP(t *testing.T) {
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
	for _, item := range requests {
		request := httptest.NewRequest(item.Method, item.Path, strings.NewReader(encoded(item.Body)))
		if item.Auth == nil || *item.Auth {
			request.Header.Set("Authorization", "Bearer test-token")
		}
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		if response.Code >= 500 {
			t.Fatal(response.Body.String())
		}
	}
}
