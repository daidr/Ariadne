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

func TestWorkspaceBatchFilesRPC(t *testing.T) {
	now := func() time.Time { return time.UnixMilli(1_800_000_000_000) }
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(t.TempDir(), "data.sqlite"), Now: now})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if _, err := repo.Store.Append(t.Context(), workspaceHTTPSeed()); err != nil {
		t.Fatal(err)
	}
	batchCalls := 0
	registry := sources.New()
	registry.Register(sdk.ContextProvider{Info: sdk.ContextProviderInfo{Kind: "fixture", Provides: []string{"workspace"}}, Connect: func(context.Context, map[string]any) (sdk.Capabilities, error) {
		return sdk.Capabilities{Workspace: &sdk.WorkspaceCapability{Isolated: true, Branches: &sdk.BranchCapability{
			Files: func(_ context.Context, requests []sdk.FileVersionRequest) ([]sdk.FileVersions, error) {
				batchCalls++
				out := make([]sdk.FileVersions, len(requests))
				for i, req := range requests {
					after := "content:" + req.Path
					if req.PreviousPath != nil {
						after += "<-" + *req.PreviousPath
					}
					out[i] = sdk.FileVersions{After: &after}
				}
				return out, nil
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

	from, head := strings.Repeat("a", 40), strings.Repeat("b", 40)
	prev := "old-b.go"
	resp, err := client.GetChangedFiles(t.Context(), connect.NewRequest(&ariadnev1.GetChangedFilesRequest{
		ThreadId: "thr_a",
		From:     from,
		Head:     head,
		Files: []*ariadnev1.ChangedFileRef{
			{Path: "a.go"},
			{Path: "b.go", PreviousPath: &prev},
		},
	}))
	if err != nil || batchCalls != 1 || len(resp.Msg.Files) != 2 || resp.Msg.Files[0].GetAfter() != "content:a.go" || resp.Msg.Files[1].GetAfter() != "content:b.go<-old-b.go" {
		t.Fatalf("batch response: calls=%d resp=%+v err=%v", batchCalls, resp, err)
	}
	if _, err := client.GetChangedFiles(t.Context(), connect.NewRequest(&ariadnev1.GetChangedFilesRequest{
		ThreadId: "thr_a",
		From:     "invalid",
		Head:     head,
		Files:    []*ariadnev1.ChangedFileRef{{Path: "a.go"}},
	})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected invalid argument for bad from, got %v", err)
	}
	if _, err := client.GetChangedFiles(t.Context(), connect.NewRequest(&ariadnev1.GetChangedFilesRequest{
		ThreadId: "thr_a",
		From:     from,
		Head:     head,
	})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected invalid argument for empty files, got %v", err)
	}
}

func TestWorkspaceMultiRepoRPC(t *testing.T) {
	now := func() time.Time { return time.UnixMilli(1_800_000_000_000) }
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(t.TempDir(), "data.sqlite"), Now: now})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if _, err := repo.Store.Append(t.Context(), workspaceHTTPSeed()); err != nil {
		t.Fatal(err)
	}
	from, head := strings.Repeat("a", 40), strings.Repeat("b", 40)
	added := int64(2)
	var gotRepoPath string
	registry := sources.New()
	registry.Register(sdk.ContextProvider{Info: sdk.ContextProviderInfo{Kind: "fixture", Provides: []string{"workspace"}}, Connect: func(context.Context, map[string]any) (sdk.Capabilities, error) {
		return sdk.Capabilities{Workspace: &sdk.WorkspaceCapability{Isolated: true, Branches: &sdk.BranchCapability{
			Changes: func(context.Context, sdk.ThreadBranch, string) (*sdk.BranchChanges, error) {
				return &sdk.BranchChanges{
					Branch: "work/thread", Base: "integration", From: from, Head: head,
					Files: []sdk.ChangedFile{{Path: "root.go", Status: "modified", Added: &added}},
				}, nil
			},
			EmbeddedChanges: func(context.Context, string, map[string]sdk.RepoBaseline) ([]sdk.RepoChanges, error) {
				return []sdk.RepoChanges{{
					RepoPath: "packages/sub",
					Embedded: true,
					Changes: &sdk.BranchChanges{
						Branch: "feat/sub", Base: "main", From: from, Head: head,
						Files: []sdk.ChangedFile{{Path: "sub.go", Status: "added", Added: &added}},
					},
				}}, nil
			},
			File: func(_ context.Context, req sdk.FileVersionRequest) (sdk.FileVersions, error) {
				gotRepoPath = req.RepoPath
				after := "repo:" + req.RepoPath + ":" + req.Path
				return sdk.FileVersions{After: &after}, nil
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

	changesResp, err := client.GetThreadChanges(t.Context(), connect.NewRequest(&ariadnev1.GetThreadChangesRequest{ThreadId: "thr_a"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(changesResp.Msg.Repos) != 2 {
		t.Fatalf("expected 2 repos in GetThreadChangesResponse, got %+v", changesResp.Msg.Repos)
	}
	if changesResp.Msg.Repos[1].GetRepoPath() != "packages/sub" || !changesResp.Msg.Repos[1].GetEmbedded() || changesResp.Msg.Repos[1].GetMergeable() {
		t.Fatalf("unexpected embedded repo entry: %+v", changesResp.Msg.Repos[1])
	}

	subPath := "packages/sub"
	fileResp, err := client.GetChangedFile(t.Context(), connect.NewRequest(&ariadnev1.GetChangedFileRequest{
		ThreadId: "thr_a",
		From:     from,
		Head:     head,
		Path:     "sub.go",
		RepoPath: &subPath,
	}))
	if err != nil || gotRepoPath != "packages/sub" || fileResp.Msg.File.GetAfter() != "repo:packages/sub:sub.go" {
		t.Fatalf("unexpected embedded GetChangedFile response: repoPath=%q resp=%+v err=%v", gotRepoPath, fileResp, err)
	}

	badPath := "../escape"
	if _, err := client.GetChangedFile(t.Context(), connect.NewRequest(&ariadnev1.GetChangedFileRequest{
		ThreadId: "thr_a",
		From:     from,
		Head:     head,
		Path:     "sub.go",
		RepoPath: &badPath,
	})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected invalid argument for traversal repoPath, got %v", err)
	}
}
