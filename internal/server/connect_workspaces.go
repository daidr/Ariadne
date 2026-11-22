package server

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"runtime"
	"sync"
	"time"

	"connectrpc.com/connect"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/domainerr"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/sdk"
)

var commitID = regexp.MustCompile(`^[0-9a-f]{40,64}$`)
var threadDirectoryID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

type workspaceServiceHandler struct {
	api *API
}

func (api *API) registerWorkspaceService() {
	serviceDesc := ariadnev1.File_ariadne_v1_workspaces_proto.Services().ByName("WorkspaceService")
	handler := &workspaceServiceHandler{api: api}
	api.mountConnect(serviceDesc, func(opts ...connect.HandlerOption) (string, http.Handler) {
		return ariadnev1connect.NewWorkspaceServiceHandler(handler, opts...)
	})
}

func (h *workspaceServiceHandler) GetThreadChanges(
	ctx context.Context,
	req *connect.Request[ariadnev1.GetThreadChangesRequest],
) (*connect.Response[ariadnev1.GetThreadChangesResponse], error) {
	manager := h.api.options.Workspaces
	if manager == nil {
		return connect.NewResponse(&ariadnev1.GetThreadChangesResponse{
			Mergeable: false,
		}), nil
	}
	var known *sdk.BranchRevisions
	if req.Msg.Head != nil || req.Msg.BaseHead != nil {
		head, baseHead := req.Msg.GetHead(), req.Msg.GetBaseHead()
		if !commitID.MatchString(head) || !commitID.MatchString(baseHead) {
			return nil, badInput("head or baseHead")
		}
		known = &sdk.BranchRevisions{Head: head, BaseHead: baseHead}
	}
	value, err := manager.ThreadChangesSince(ctx, req.Msg.GetThreadId(), known)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.GetThreadChangesResponse{
		Changes:   toProtoBranchChanges(value.Changes),
		Mergeable: value.Mergeable,
		Unchanged: value.Unchanged,
	}), nil
}

func (h *workspaceServiceHandler) GetChangedFile(
	ctx context.Context,
	req *connect.Request[ariadnev1.GetChangedFileRequest],
) (*connect.Response[ariadnev1.GetChangedFileResponse], error) {
	manager := h.api.options.Workspaces
	if manager == nil {
		return nil, domainerr.New("error.noBranch", nil)
	}
	if req.Msg.GetPath() == "" {
		return nil, badInput("path")
	}
	if !commitID.MatchString(req.Msg.GetFrom()) {
		return nil, badInput("from")
	}
	if !commitID.MatchString(req.Msg.GetHead()) {
		return nil, badInput("head")
	}
	var previous *string
	if req.Msg.PreviousPath != nil && *req.Msg.PreviousPath != "" {
		v := *req.Msg.PreviousPath
		previous = &v
	}
	file, err := manager.ThreadFile(ctx, req.Msg.GetThreadId(), sdk.FileVersionRequest{
		From:         req.Msg.GetFrom(),
		Head:         req.Msg.GetHead(),
		Path:         req.Msg.GetPath(),
		PreviousPath: previous,
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.GetChangedFileResponse{
		File: &ariadnev1.FileVersions{
			Before:  file.Before,
			After:   file.After,
			Skipped: file.Skipped,
		},
	}), nil
}

func (h *workspaceServiceHandler) MergeThread(
	ctx context.Context,
	req *connect.Request[ariadnev1.MergeThreadRequest],
) (*connect.Response[ariadnev1.MergeThreadResponse], error) {
	manager := h.api.options.Workspaces
	if manager == nil {
		return nil, domainerr.New("error.merge.unsupported", nil)
	}
	method := req.Msg.GetMethod()
	if method == "" {
		method = "squash"
	}
	if method != "squash" && method != "merge" {
		return nil, badInput("method")
	}
	expectedHead := ""
	if req.Msg.ExpectedHead != nil {
		expectedHead = *req.Msg.ExpectedHead
		if !commitID.MatchString(expectedHead) {
			return nil, badInput("expectedHead")
		}
	}
	value, err := manager.MergeThread(ctx, req.Msg.GetThreadId(), method, req.Msg.GetMessage(), expectedHead)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.MergeThreadResponse{
		Outcome: toProtoMergeOutcome(value.Outcome),
		Thread:  toProtoThreadView(value.Thread),
	}), nil
}

func (h *workspaceServiceHandler) UndoMerge(
	ctx context.Context,
	req *connect.Request[ariadnev1.UndoMergeRequest],
) (*connect.Response[ariadnev1.UndoMergeResponse], error) {
	manager := h.api.options.Workspaces
	if manager == nil {
		return nil, domainerr.New("error.merge.unsupported", nil)
	}
	thread, err := manager.UndoMerge(ctx, req.Msg.GetThreadId())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.UndoMergeResponse{
		Thread: toProtoThreadView(thread),
	}), nil
}

func (h *workspaceServiceHandler) ListWorktrees(
	ctx context.Context,
	req *connect.Request[ariadnev1.ListWorktreesRequest],
) (*connect.Response[ariadnev1.ListWorktreesResponse], error) {
	manager := h.api.options.Workspaces
	if manager == nil {
		return connect.NewResponse(&ariadnev1.ListWorktreesResponse{
			Windows: runtime.GOOS == "windows",
		}), nil
	}
	projects, err := manager.ListWorktrees(ctx, req.Msg.GetIncludeSizes())
	if err != nil {
		return nil, err
	}
	out := make([]*ariadnev1.ProjectWorktrees, 0, len(projects))
	for _, project := range projects {
		out = append(out, toProtoProjectWorktrees(project))
	}
	return connect.NewResponse(&ariadnev1.ListWorktreesResponse{
		Projects: out,
		Windows:  runtime.GOOS == "windows",
	}), nil
}

func (h *workspaceServiceHandler) RemoveWorktree(
	ctx context.Context,
	req *connect.Request[ariadnev1.RemoveWorktreeRequest],
) (*connect.Response[ariadnev1.RemoveWorktreeResponse], error) {
	project, thread := req.Msg.GetProjectId(), req.Msg.GetThreadId()
	if _, err := h.api.options.Repository.GetProject(ctx, project); err != nil {
		return nil, err
	}
	if !threadDirectoryID.MatchString(thread) {
		return nil, badInput("threadId")
	}
	source := req.Msg.GetSourceId()
	if source != "" && !core.ValidFileSourceID(source) {
		return nil, badInput("sourceId")
	}
	removed := "dirty"
	var err error
	manager := h.api.options.Workspaces
	if manager != nil {
		if req.Msg.GetOrphaned() {
			removed, err = manager.RemoveOrphanedWorktree(ctx, project, thread, source)
		} else {
			removed, err = manager.RemoveSourceWorktree(ctx, project, thread, source, "user")
		}
		if err != nil {
			return nil, err
		}
	}
	if removed == "busy" {
		return nil, domainerr.New("error.merge.working", nil)
	}
	if removed == "dirty" {
		return nil, domainerr.New("error.worktreeDirty", nil)
	}
	if removed == "orphan-changed" {
		return nil, domainerr.New("error.worktreeOrphanChanged", nil)
	}
	if h.api.options.Data != nil {
		h.api.options.Data.InvalidateUsage()
	}
	return connect.NewResponse(&ariadnev1.RemoveWorktreeResponse{}), nil
}

func (h *workspaceServiceHandler) ListMentionFiles(
	ctx context.Context,
	req *connect.Request[ariadnev1.ListMentionFilesRequest],
) (*connect.Response[ariadnev1.ListMentionFilesResponse], error) {
	repo := h.api.options.Repository
	project := req.Msg.GetProjectId()
	if _, err := repo.GetProject(ctx, project); err != nil {
		return nil, err
	}
	manager := h.api.options.Workspaces
	if manager == nil || !manager.Available(ctx, project) {
		return connect.NewResponse(&ariadnev1.ListMentionFilesResponse{}), nil
	}
	thread := ""
	if id := req.Msg.GetConversationId(); id != "" {
		conversation, err := repo.GetConversation(ctx, id)
		if err != nil {
			return nil, err
		}
		if conversation.ProjectID != project {
			return nil, badInput("conversationId")
		}
		if conversation.Role == "thread" {
			thread = conversation.ID
		}
	}
	files, err := manager.MentionFiles(ctx, project, thread, textutil.HeadUnits(req.Msg.GetQuery(), 200))
	if err != nil {
		files = []string{}
	}
	return connect.NewResponse(&ariadnev1.ListMentionFilesResponse{Files: files}), nil
}

func (h *workspaceServiceHandler) GetReferenceStatus(
	ctx context.Context,
	req *connect.Request[ariadnev1.GetReferenceStatusRequest],
) (*connect.Response[ariadnev1.GetReferenceStatusResponse], error) {
	project, err := h.api.options.Repository.GetProject(ctx, req.Msg.GetProjectId())
	if err != nil {
		return nil, err
	}
	manager := h.api.options.Workspaces
	if manager == nil {
		return connect.NewResponse(&ariadnev1.GetReferenceStatusResponse{}), nil
	}
	references, _ := project.References.([]any)
	failures := make([]*domainerr.Error, len(references))
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	jobs := make(chan int)
	var group sync.WaitGroup
	for range min(16, len(references)) {
		group.Go(func() {
			for index := range jobs {
				if checkCtx.Err() != nil {
					continue
				}
				ref, _ := references[index].(map[string]any)
				view, err := manager.ReferenceView(checkCtx, project.ID, text(ref, "name"), "")
				if err == nil {
					_, err = view.List(checkCtx, ".")
				}
				if err == nil || checkCtx.Err() != nil {
					continue
				}
				var known *domainerr.Error
				if !errors.As(err, &known) {
					known = domainerr.New("error.workspace", object{"detail": err.Error()})
				}
				failures[index] = known
			}
		})
	}
	for index := range references {
		select {
		case jobs <- index:
		case <-checkCtx.Done():
		}
	}
	close(jobs)
	group.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var failed []*ariadnev1.FailedReference
	for index, failure := range failures {
		if failure != nil {
			ref, _ := references[index].(map[string]any)
			failed = append(failed, &ariadnev1.FailedReference{
				Id:    protoStr(ref["id"]),
				Error: toProtoLocalizedError(failure),
			})
		}
	}
	return connect.NewResponse(&ariadnev1.GetReferenceStatusResponse{Failed: failed}), nil
}
