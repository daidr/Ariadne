package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/eventstore"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
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
	srv := httptest.NewServer(api)
	defer srv.Close()

	client := ariadnev1connect.NewThreadServiceClient(srv.Client(), srv.URL+rpcPrefix, connect.WithInterceptors(bearerClientInterceptor("test-token")))
	resp, err := client.GetPullRequestChecks(ctx, connect.NewRequest(&ariadnev1.GetPullRequestChecksRequest{ThreadId: "thr_a"}))
	if err != nil ||
		resp.Msg.HeadSha != head || len(resp.Msg.Checks) != 2 || resp.Msg.Checks[0].Status != "in_progress" ||
		resp.Msg.Checks[1].Conclusion == nil || *resp.Msg.Checks[1].Conclusion != "success" {
		t.Fatalf("checks: resp=%+v err=%v", resp, err)
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
					return &sdk.BranchChanges{Head: revisions.Head, BaseHead: revisions.BaseHead, Branch: "work/thread", Base: "integration", From: revisions.BaseHead}, nil
				},
				Merge: func(_ context.Context, input sdk.MergeRequest) (sdk.MergeOutcome, error) {
					if input.ExpectedHead != "" && input.ExpectedHead != revisions.Head {
						return sdk.MergeOutcome{Kind: "moved", Head: revisions.Head}, nil
					}
					return sdk.MergeOutcome{Kind: "merged", SHA: strings.Repeat("e", 40), Head: revisions.Head}, nil
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
	srv := httptest.NewServer(api)
	defer srv.Close()

	client := ariadnev1connect.NewWorkspaceServiceClient(srv.Client(), srv.URL+rpcPrefix, connect.WithInterceptors(bearerClientInterceptor("test-token")))

	resp, err := client.GetThreadChanges(ctx, connect.NewRequest(&ariadnev1.GetThreadChangesRequest{
		ThreadId: "thr_a",
		Head:     &initial,
		BaseHead: &base,
	}))
	if err != nil || !resp.Msg.Unchanged || diffs != 0 {
		t.Fatalf("unchanged refs: resp=%+v err=%v; diff calls: %d", resp, err, diffs)
	}
	revisions.BaseHead = strings.Repeat("d", 40)
	resp, err = client.GetThreadChanges(ctx, connect.NewRequest(&ariadnev1.GetThreadChangesRequest{
		ThreadId: "thr_a",
		Head:     &initial,
		BaseHead: &base,
	}))
	if err != nil || resp.Msg.Changes == nil || resp.Msg.Changes.BaseHead == nil || *resp.Msg.Changes.BaseHead != revisions.BaseHead || diffs != 1 {
		t.Fatalf("changed base: resp=%+v err=%v; diff calls: %d", resp, err, diffs)
	}
	revisions.Head = strings.Repeat("c", 40)
	mergeResp, err := client.MergeThread(ctx, connect.NewRequest(&ariadnev1.MergeThreadRequest{
		ThreadId:     "thr_a",
		Method:       "squash",
		ExpectedHead: &initial,
	}))
	if err != nil || mergeResp.Msg.Outcome == nil || mergeResp.Msg.Outcome.Kind != "moved" {
		t.Fatalf("merge of older comparison: resp=%+v err=%v", mergeResp, err)
	}
}

func TestWorkspaceRPC(t *testing.T) {
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
			UndoMerge: func(context.Context, sdk.UndoMergeRequest) error { return nil },
			Remove:    func(context.Context, string, sdk.WorktreeRemoveOptions) (bool, error) { return true, nil },
			Worktrees: func(context.Context, bool) ([]sdk.WorktreeInfo, error) {
				return []sdk.WorktreeInfo{{ThreadID: "thr_a", Path: "/tmp/wt/thr_a"}}, nil
			},
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
	srv := httptest.NewServer(api)
	defer srv.Close()

	auth := connect.WithInterceptors(bearerClientInterceptor("test-token"))
	client := ariadnev1connect.NewWorkspaceServiceClient(srv.Client(), srv.URL+rpcPrefix, auth)
	unauth := ariadnev1connect.NewWorkspaceServiceClient(srv.Client(), srv.URL+rpcPrefix)

	changesResp, err := client.GetThreadChanges(t.Context(), connect.NewRequest(&ariadnev1.GetThreadChangesRequest{ThreadId: "thr_a"}))
	if err != nil || changesResp.Msg.Changes == nil || !changesResp.Msg.Mergeable {
		t.Fatalf("get changes failed: resp=%+v err=%v", changesResp, err)
	}

	fileResp, err := client.GetChangedFile(t.Context(), connect.NewRequest(&ariadnev1.GetChangedFileRequest{
		ThreadId: "thr_a",
		From:     strings.Repeat("a", 40),
		Head:     strings.Repeat("b", 40),
		Path:     "file.txt",
	}))
	if err != nil || fileResp.Msg.File == nil || fileResp.Msg.File.GetBefore() != "before" || fileResp.Msg.File.GetAfter() != "after" {
		t.Fatalf("get changed file failed: resp=%+v err=%v", fileResp, err)
	}

	if _, err := client.GetChangedFile(t.Context(), connect.NewRequest(&ariadnev1.GetChangedFileRequest{
		ThreadId: "thr_a", From: "invalid", Head: strings.Repeat("b", 40), Path: "file.txt",
	})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected invalid argument for bad from, got %v", err)
	}

	wtResp, err := client.ListWorktrees(t.Context(), connect.NewRequest(&ariadnev1.ListWorktreesRequest{IncludeSizes: true}))
	if err != nil || len(wtResp.Msg.Projects) != 1 || len(wtResp.Msg.Projects[0].Worktrees) != 1 || wtResp.Msg.Projects[0].Worktrees[0].Thread == nil {
		t.Fatalf("list worktrees failed: resp=%+v err=%v", wtResp, err)
	}

	if _, err := unauth.MergeThread(t.Context(), connect.NewRequest(&ariadnev1.MergeThreadRequest{ThreadId: "thr_a"})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("expected unauthenticated merge, got %v", err)
	}
	if _, err := client.MergeThread(t.Context(), connect.NewRequest(&ariadnev1.MergeThreadRequest{ThreadId: "thr_a", Method: "bad"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected invalid argument for bad method, got %v", err)
	}
	conflictResp, err := client.MergeThread(t.Context(), connect.NewRequest(&ariadnev1.MergeThreadRequest{ThreadId: "thr_a", Message: "conflict"}))
	if err != nil || conflictResp.Msg.Outcome.Kind != "conflict" {
		t.Fatalf("expected conflict outcome, got resp=%+v err=%v", conflictResp, err)
	}
	mergedResp, err := client.MergeThread(t.Context(), connect.NewRequest(&ariadnev1.MergeThreadRequest{ThreadId: "thr_a", Method: "merge"}))
	if err != nil || mergedResp.Msg.Outcome.Kind != "merged" || mergedResp.Msg.Thread.Merged == nil {
		t.Fatalf("expected merged outcome, got resp=%+v err=%v", mergedResp, err)
	}
	undoResp, err := client.UndoMerge(t.Context(), connect.NewRequest(&ariadnev1.UndoMergeRequest{ThreadId: "thr_a"}))
	if err != nil || undoResp.Msg.Thread.Merged != nil {
		t.Fatalf("undo merge failed: resp=%+v err=%v", undoResp, err)
	}

	if _, err := client.RemoveWorktree(t.Context(), connect.NewRequest(&ariadnev1.RemoveWorktreeRequest{ProjectId: "prj_a", ThreadId: "thr_a"})); err != nil {
		t.Fatalf("remove worktree failed: %v", err)
	}
}
