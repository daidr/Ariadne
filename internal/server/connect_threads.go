package server

import (
	"context"
	"net/http"
	"time"

	"connectrpc.com/connect"

	"github.com/daidr/Ariadne/internal/background"
	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/domainerr"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/sdk"
)

type threadServiceHandler struct {
	api *API
}

func (api *API) registerThreadService() {
	serviceDesc := ariadnev1.File_ariadne_v1_threads_proto.Services().ByName("ThreadService")
	handler := &threadServiceHandler{api: api}
	api.mountConnect(serviceDesc, func(opts ...connect.HandlerOption) (string, http.Handler) {
		return ariadnev1connect.NewThreadServiceHandler(handler, opts...)
	})
}

func (h *threadServiceHandler) ResumeThread(
	ctx context.Context,
	req *connect.Request[ariadnev1.ResumeThreadRequest],
) (*connect.Response[ariadnev1.ResumeThreadResponse], error) {
	thread, err := h.api.options.Repository.ResumeThread(ctx, req.Msg.GetThreadId(), "user")
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.ResumeThreadResponse{
		Thread: toProtoThreadView(thread),
	}), nil
}

func (h *threadServiceHandler) CompleteThread(
	ctx context.Context,
	req *connect.Request[ariadnev1.CompleteThreadRequest],
) (*connect.Response[ariadnev1.CompleteThreadResponse], error) {
	thread, err := h.api.options.Repository.CompleteThread(ctx, req.Msg.GetThreadId())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.CompleteThreadResponse{
		Thread: toProtoThreadView(thread),
	}), nil
}

func (h *threadServiceHandler) EndAside(
	ctx context.Context,
	req *connect.Request[ariadnev1.EndAsideRequest],
) (*connect.Response[ariadnev1.EndAsideResponse], error) {
	id := req.Msg.GetThreadId()
	repo := h.api.options.Repository
	if _, err := repo.EndAside(ctx, id, "user"); err != nil {
		return nil, err
	}
	thread, err := repo.GetThread(ctx, id)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.EndAsideResponse{
		Thread: toProtoThreadView(thread),
	}), nil
}

func (h *threadServiceHandler) SetThreadModel(
	ctx context.Context,
	req *connect.Request[ariadnev1.SetThreadModelRequest],
) (*connect.Response[ariadnev1.SetThreadModelResponse], error) {
	var model *string
	var effort *string
	switch action := req.Msg.ModelAction.(type) {
	case *ariadnev1.SetThreadModelRequest_UseDefault:
		if !action.UseDefault {
			return nil, badInput("model")
		}
		if req.Msg.Effort != nil && *req.Msg.Effort != "" {
			return nil, badInput("effort")
		}
	case *ariadnev1.SetThreadModelRequest_Model:
		value := action.Model
		found := false
		if h.api.options.Settings != nil {
			var kinds []sdk.ModelProviderInfo
			if h.api.options.Models != nil {
				kinds = h.api.options.Models.Kinds()
			}
			for _, raw := range h.api.options.Settings.ModelChoices(kinds) {
				choice := protoMap(raw)
				if choice["id"] == value {
					found = true
					break
				}
			}
		}
		if !found {
			return nil, badInput("model")
		}
		model = &value
		if req.Msg.Effort != nil && *req.Msg.Effort != "" {
			e := *req.Msg.Effort
			if !settings.ValidEffort(e) {
				return nil, badInput("effort")
			}
			effort = &e
		}
	default:
		return nil, badInput("model")
	}
	thread, err := h.api.options.Repository.SetThreadModel(ctx, req.Msg.GetThreadId(), model, effort, "user")
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.SetThreadModelResponse{
		Thread: toProtoThreadView(thread),
	}), nil
}

func (h *threadServiceHandler) RemoveThreadReference(
	ctx context.Context,
	req *connect.Request[ariadnev1.RemoveThreadReferenceRequest],
) (*connect.Response[ariadnev1.RemoveThreadReferenceResponse], error) {
	thread, err := h.api.options.Repository.RemoveThreadReference(ctx, req.Msg.GetThreadId(), req.Msg.GetReferenceId())
	if err != nil {
		return nil, err
	}
	if h.api.options.Workspaces != nil {
		if err := h.api.options.Workspaces.InvalidateReference(ctx, thread.ProjectID, req.Msg.GetReferenceId()); err != nil {
			return nil, err
		}
	}
	return connect.NewResponse(&ariadnev1.RemoveThreadReferenceResponse{
		Thread: toProtoThreadView(thread),
	}), nil
}

func (h *threadServiceHandler) KeepThreadReference(
	ctx context.Context,
	req *connect.Request[ariadnev1.KeepThreadReferenceRequest],
) (*connect.Response[ariadnev1.KeepThreadReferenceResponse], error) {
	id := req.Msg.GetThreadId()
	repo := h.api.options.Repository
	if _, err := repo.KeepThreadReference(ctx, id, req.Msg.GetReferenceId()); err != nil {
		return nil, err
	}
	thread, err := repo.GetThread(ctx, id)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.KeepThreadReferenceResponse{
		Thread: toProtoThreadView(thread),
	}), nil
}

func (h *threadServiceHandler) GetCommandOutput(
	_ context.Context,
	req *connect.Request[ariadnev1.GetCommandOutputRequest],
) (*connect.Response[ariadnev1.GetCommandOutputResponse], error) {
	id := req.Msg.GetCommandId()
	if h.api.options.Background != nil {
		if output := h.api.options.Background.Peek(req.Msg.GetThreadId(), id, 0); output != nil {
			return connect.NewResponse(&ariadnev1.GetCommandOutputResponse{
				Command: toProtoBackgroundCommand(output.Command),
				Tail:    output.Tail,
			}), nil
		}
	}
	return nil, domainerr.NotFound(id)
}

func (h *threadServiceHandler) StreamCommandOutput(
	ctx context.Context,
	req *connect.Request[ariadnev1.StreamCommandOutputRequest],
	stream *connect.ServerStream[ariadnev1.StreamCommandOutputResponse],
) error {
	id := req.Msg.GetCommandId()
	if h.api.options.Background == nil {
		return domainerr.NotFound(id)
	}
	guard := h.api.streamWriteGuard(ctx)
	found, err := h.api.options.Background.WatchPeek(ctx, req.Msg.GetThreadId(), id, 0, 150*time.Millisecond, func(peek background.Peek) error {
		return guard.Send(func() error {
			return stream.Send(&ariadnev1.StreamCommandOutputResponse{
				Command: toProtoBackgroundCommand(peek.Command),
				Tail:    peek.Tail,
			})
		})
	})
	if err != nil {
		guard.ArmCloseDeadline()
		return err
	}
	if !found {
		guard.ArmCloseDeadline()
		return domainerr.NotFound(id)
	}
	return nil
}

func (h *threadServiceHandler) StopCommand(
	ctx context.Context,
	req *connect.Request[ariadnev1.StopCommandRequest],
) (*connect.Response[ariadnev1.StopCommandResponse], error) {
	id := req.Msg.GetCommandId()
	if h.api.options.Background != nil {
		command, err := h.api.options.Background.Stop(ctx, req.Msg.GetThreadId(), id)
		if err != nil {
			return nil, err
		}
		if command != nil {
			return connect.NewResponse(&ariadnev1.StopCommandResponse{
				Command: toProtoBackgroundCommand(*command),
			}), nil
		}
	}
	return nil, domainerr.NotFound(id)
}

func (h *threadServiceHandler) ListReviews(
	ctx context.Context,
	req *connect.Request[ariadnev1.ListReviewsRequest],
) (*connect.Response[ariadnev1.ListReviewsResponse], error) {
	opts := core.ReviewPageOptions{}
	if req.Msg.BeforeId != nil {
		if *req.Msg.BeforeId < 1 {
			return nil, badInput("beforeId")
		}
		v := *req.Msg.BeforeId
		opts.BeforeID = &v
	}
	if req.Msg.Limit != nil {
		if *req.Msg.Limit < 1 || *req.Msg.Limit > 200 {
			return nil, badInput("limit")
		}
		v := int(*req.Msg.Limit)
		opts.Limit = &v
	}
	page, err := h.api.options.Repository.PageReviews(ctx, req.Msg.GetThreadId(), opts)
	if err != nil {
		return nil, err
	}
	rounds := make([]*ariadnev1.ReviewRoundView, 0, len(page.Rounds))
	for _, r := range page.Rounds {
		rounds = append(rounds, toProtoReviewRound(r))
	}
	return connect.NewResponse(&ariadnev1.ListReviewsResponse{
		Rounds:       rounds,
		HasMore:      page.HasMore,
		NextBeforeId: page.NextBeforeID,
	}), nil
}

func (h *threadServiceHandler) ListLineComments(
	ctx context.Context,
	req *connect.Request[ariadnev1.ListLineCommentsRequest],
) (*connect.Response[ariadnev1.ListLineCommentsResponse], error) {
	opts := core.LineCommentPageOptions{}
	if req.Msg.BeforeMessageId != nil && *req.Msg.BeforeMessageId != "" {
		v := *req.Msg.BeforeMessageId
		opts.BeforeMessageID = &v
	}
	if req.Msg.Limit != nil {
		if *req.Msg.Limit < 1 || *req.Msg.Limit > 500 {
			return nil, badInput("limit")
		}
		v := int(*req.Msg.Limit)
		opts.Limit = &v
	}
	page, err := h.api.options.Repository.PageLineComments(ctx, req.Msg.GetThreadId(), opts)
	if err != nil {
		return nil, err
	}
	comments := make([]*ariadnev1.SentLineComment, 0, len(page.Comments))
	for _, c := range page.Comments {
		comments = append(comments, toProtoSentLineComment(c))
	}
	return connect.NewResponse(&ariadnev1.ListLineCommentsResponse{
		Comments:            comments,
		HasMore:             page.HasMore,
		NextBeforeMessageId: page.NextBeforeMessage,
	}), nil
}

func (h *threadServiceHandler) GetPullRequestChecks(
	ctx context.Context,
	req *connect.Request[ariadnev1.GetPullRequestChecksRequest],
) (*connect.Response[ariadnev1.GetPullRequestChecksResponse], error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	thread, err := h.api.options.Repository.GetThread(ctx, req.Msg.GetThreadId())
	if err != nil {
		return nil, err
	}
	pullRequest := protoMap(thread.PullRequest)
	head := protoStr(pullRequest["headSha"])
	manager := h.api.options.Workspaces
	if manager == nil || head == "" {
		return nil, domainerr.New("error.noRepo", nil)
	}
	repo, err := manager.RepoForThread(ctx, thread)
	if err != nil {
		return nil, err
	}
	if repo.Checks == nil {
		return nil, domainerr.New("error.noRepo", nil)
	}
	checks, err := repo.Checks(ctx, head)
	if err != nil {
		return nil, err
	}
	if checks == nil {
		checks = []sdk.CheckInfo{}
	}
	protoChecks := make([]*ariadnev1.PullRequestCheck, 0, len(checks))
	for _, check := range checks {
		protoChecks = append(protoChecks, &ariadnev1.PullRequestCheck{
			Id:         check.ID,
			Name:       check.Name,
			Status:     check.Status,
			Conclusion: check.Conclusion,
		})
	}
	return connect.NewResponse(&ariadnev1.GetPullRequestChecksResponse{
		HeadSha: head,
		Checks:  protoChecks,
	}), nil
}
