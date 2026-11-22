package server

import (
	"context"
	"math"
	"net/http"
	"slices"

	"connectrpc.com/connect"

	"github.com/daidr/Ariadne/internal/domainerr"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
	"github.com/daidr/Ariadne/sdk"
)

type issueService struct {
	api *API
}

func (api *API) registerIssueRPC() {
	svc := &issueService{api: api}
	api.mountConnect(
		ariadnev1.File_ariadne_v1_issues_proto.Services().ByName("IssueService"),
		func(opts ...connect.HandlerOption) (string, http.Handler) {
			return ariadnev1connect.NewIssueServiceHandler(svc, opts...)
		},
	)
}

func (s *issueService) resolveTracker(ctx context.Context, projectID string) (*sdk.TrackerCapability, error) {
	if _, err := s.api.options.Repository.GetProject(ctx, projectID); err != nil {
		return nil, err
	}
	manager := s.api.options.Workspaces
	if manager == nil || !manager.HasTracker(ctx, projectID) {
		return nil, domainerr.New("error.noTracker", nil)
	}
	return manager.Tracker(ctx, projectID)
}

func (s *issueService) ListIssues(
	ctx context.Context,
	req *connect.Request[ariadnev1.ListIssuesRequest],
) (*connect.Response[ariadnev1.ListIssuesResponse], error) {
	if req.Msg.Limit < 0 {
		return nil, badInput("limit")
	}
	tracker, err := s.resolveTracker(ctx, req.Msg.ProjectId)
	if err != nil {
		return nil, err
	}
	if tracker.List == nil {
		return nil, domainerr.New("error.noTracker", nil)
	}
	issues, err := tracker.List(ctx, sdk.IssueQuery{
		State:  req.Msg.State,
		Labels: slices.Clone(req.Msg.Labels),
		Search: req.Msg.Search,
		Limit:  int(req.Msg.Limit),
	})
	if err != nil {
		return nil, domainerr.New("error.issue", object{"detail": err.Error()})
	}
	out := make([]*ariadnev1.IssueSummary, 0, len(issues))
	for _, issue := range issues {
		out = append(out, toProtoIssueSummary(issue))
	}
	return connect.NewResponse(&ariadnev1.ListIssuesResponse{
		Tracker: tracker.Name,
		WebUrl:  tracker.WebURL,
		Issues:  out,
	}), nil
}

func (s *issueService) GetIssue(
	ctx context.Context,
	req *connect.Request[ariadnev1.GetIssueRequest],
) (*connect.Response[ariadnev1.GetIssueResponse], error) {
	number := req.Msg.Number
	if number < 1 || number >= int64(math.MaxInt) {
		return nil, badInput("number")
	}
	tracker, err := s.resolveTracker(ctx, req.Msg.ProjectId)
	if err != nil {
		return nil, err
	}
	if tracker.Issue == nil {
		return nil, domainerr.New("error.noTracker", nil)
	}
	detail, err := tracker.Issue(ctx, int(number))
	if err != nil {
		return nil, domainerr.New("error.issue", object{"detail": err.Error()})
	}
	recent := make([]*ariadnev1.IssueComment, 0, len(detail.Recent))
	for _, comment := range detail.Recent {
		recent = append(recent, &ariadnev1.IssueComment{
			Id:        comment.ID,
			Author:    comment.Author,
			Body:      comment.Body,
			CreatedAt: comment.CreatedAt,
			Url:       comment.URL,
		})
	}
	return connect.NewResponse(&ariadnev1.GetIssueResponse{
		Tracker: tracker.Name,
		WebUrl:  tracker.WebURL,
		Issue: &ariadnev1.IssueDetail{
			Summary:   toProtoIssueSummary(detail.IssueSummary),
			Body:      detail.Body,
			CreatedAt: detail.CreatedAt,
			Recent:    recent,
		},
	}), nil
}

func toProtoIssueSummary(issue sdk.IssueSummary) *ariadnev1.IssueSummary {
	return &ariadnev1.IssueSummary{
		Number:    int64(issue.Number),
		Url:       issue.URL,
		Title:     issue.Title,
		State:     issue.State,
		Author:    issue.Author,
		Labels:    slices.Clone(issue.Labels),
		Comments:  int32(issue.Comments),
		UpdatedAt: issue.UpdatedAt,
	}
}
