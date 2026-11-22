package server

import (
	"context"
	"math"
	"net/http"

	"connectrpc.com/connect"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/domainerr"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
)

type planService struct {
	api *API
}

func (api *API) registerPlanRPC() {
	svc := &planService{api: api}
	api.mountConnect(
		ariadnev1.File_ariadne_v1_plans_proto.Services().ByName("PlanService"),
		func(opts ...connect.HandlerOption) (string, http.Handler) {
			return ariadnev1connect.NewPlanServiceHandler(svc, opts...)
		},
	)
}

func (s *planService) GetPlan(
	ctx context.Context,
	req *connect.Request[ariadnev1.GetPlanRequest],
) (*connect.Response[ariadnev1.GetPlanResponse], error) {
	page, err := s.api.options.Repository.GetPlanPage(
		ctx,
		req.Msg.ProjectId,
		int(req.Msg.RevisionPageSize),
		req.Msg.RevisionPageToken,
	)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.GetPlanResponse{
		Plan:                  toProtoPlanView(page.Plan),
		NextRevisionPageToken: page.NextRevisionPageToken,
		HasMoreRevisions:      page.HasMoreRevisions,
	}), nil
}

func (s *planService) ListPlanRevisions(
	ctx context.Context,
	req *connect.Request[ariadnev1.ListPlanRevisionsRequest],
) (*connect.Response[ariadnev1.ListPlanRevisionsResponse], error) {
	page, err := s.api.options.Repository.ListPlanRevisionsPage(
		ctx,
		req.Msg.ProjectId,
		int(req.Msg.PageSize),
		req.Msg.PageToken,
	)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.ListPlanRevisionsResponse{
		Revisions:     toProtoPlanRevisions(page.Revisions),
		NextPageToken: page.NextPageToken,
		HasMore:       page.HasMore,
	}), nil
}

func (s *planService) RevisePlan(
	ctx context.Context,
	req *connect.Request[ariadnev1.RevisePlanRequest],
) (*connect.Response[ariadnev1.RevisePlanResponse], error) {
	rawOps := fromProtoPlanOps(req.Msg.Ops)
	ops, err := core.ParsePlanOps(rawOps)
	if err != nil {
		return nil, err
	}
	id := req.Msg.ProjectId
	repo := s.api.options.Repository
	change, err := repo.RevisePlan(ctx, id, core.PlanRevision{
		Author: "user",
		Reason: req.Msg.Reason,
		Ops:    ops,
	})
	if err != nil {
		return nil, err
	}
	plan, err := repo.GetPlan(ctx, id)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.RevisePlanResponse{
		Change: &ariadnev1.PlanChangeResult{
			Status:     anyString(change["status"]),
			RevisionId: anyString(change["revisionId"]),
		},
		Plan: toProtoPlanView(plan),
	}), nil
}

func (s *planService) DismissPlanProposal(
	ctx context.Context,
	req *connect.Request[ariadnev1.DismissPlanProposalRequest],
) (*connect.Response[ariadnev1.DismissPlanProposalResponse], error) {
	repo := s.api.options.Repository
	if err := repo.DismissPlanProposal(ctx, req.Msg.ProjectId, req.Msg.ProposalId); err != nil {
		return nil, err
	}
	plan, err := repo.GetPlan(ctx, req.Msg.ProjectId)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.DismissPlanProposalResponse{
		Plan: toProtoPlanView(plan),
	}), nil
}

func (s *planService) PreviewPlanUndo(
	ctx context.Context,
	req *connect.Request[ariadnev1.PreviewPlanUndoRequest],
) (*connect.Response[ariadnev1.PreviewPlanUndoResponse], error) {
	preview, err := s.api.options.Repository.PreviewPlanUndo(ctx, req.Msg.ProjectId, req.Msg.RevisionId)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.PreviewPlanUndoResponse{
		Preview: toProtoPlanUndoPreview(preview),
	}), nil
}

func (s *planService) RevertPlanRevision(
	ctx context.Context,
	req *connect.Request[ariadnev1.RevertPlanRevisionRequest],
) (*connect.Response[ariadnev1.RevertPlanRevisionResponse], error) {
	repo := s.api.options.Repository
	if _, err := repo.RevertPlanRevision(ctx, req.Msg.ProjectId, req.Msg.RevisionId, "user", req.Msg.Reason); err != nil {
		return nil, err
	}
	plan, err := repo.GetPlan(ctx, req.Msg.ProjectId)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.RevertPlanRevisionResponse{
		Plan: toProtoPlanView(plan),
	}), nil
}

func (s *planService) LinkPlanNodeIssue(
	ctx context.Context,
	req *connect.Request[ariadnev1.LinkPlanNodeIssueRequest],
) (*connect.Response[ariadnev1.LinkPlanNodeIssueResponse], error) {
	number := req.Msg.Number
	if number < 1 || number >= int64(math.MaxInt) {
		return nil, badInput("number")
	}
	project, node := req.Msg.ProjectId, req.Msg.NodeId
	repo := s.api.options.Repository
	if err := repo.RequirePlanNode(ctx, project, node); err != nil {
		return nil, err
	}
	manager := s.api.options.Workspaces
	if manager == nil || !manager.HasTracker(ctx, project) {
		return nil, domainerr.New("error.noTracker", nil)
	}
	tracker, err := manager.Tracker(ctx, project)
	if err != nil {
		return nil, err
	}
	issue, err := tracker.Issue(ctx, int(number))
	if err != nil {
		return nil, domainerr.New("error.issue", object{"detail": err.Error()})
	}
	plan, err := repo.LinkIssue(ctx, project, node, core.LinkedIssue{
		Tracker: tracker.Name,
		Number:  float64(number),
		Title:   issue.Title,
		URL:     issue.URL,
	}, "user")
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.LinkPlanNodeIssueResponse{
		Plan: toProtoPlanView(plan),
	}), nil
}

func (s *planService) UnlinkPlanNodeIssue(
	ctx context.Context,
	req *connect.Request[ariadnev1.UnlinkPlanNodeIssueRequest],
) (*connect.Response[ariadnev1.UnlinkPlanNodeIssueResponse], error) {
	project, node := req.Msg.ProjectId, req.Msg.NodeId
	repo := s.api.options.Repository
	plan, err := repo.GetPlan(ctx, project)
	if err != nil {
		return nil, err
	}
	issues, err := repo.PlanIssues(ctx, project, node)
	if err != nil {
		return nil, err
	}
	for _, issue := range issues {
		if int64(issue.Number) != req.Msg.Number {
			continue
		}
		plan, err = repo.UnlinkIssue(ctx, project, node, issue.Tracker, issue.Number, "user")
		if err != nil {
			return nil, err
		}
		break
	}
	return connect.NewResponse(&ariadnev1.UnlinkPlanNodeIssueResponse{
		Plan: toProtoPlanView(plan),
	}), nil
}

// ---- Plan proto conversion helpers ----

func toProtoLocalizedMessage(raw any) *ariadnev1.LocalizedError {
	if errVal, ok := raw.(*domainerr.Error); ok {
		if errVal == nil {
			return nil
		}
		return &ariadnev1.LocalizedError{
			Key:    errVal.Key,
			Params: encodeErrorParams(errVal.Params),
		}
	}
	value := asMap(raw)
	if value == nil {
		return nil
	}
	return &ariadnev1.LocalizedError{
		Key:    anyString(value["key"]),
		Params: encodeErrorParams(asMap(value["params"])),
	}
}

func toProtoPlanOp(raw any) *ariadnev1.PlanOp {
	m := asMap(raw)
	if m == nil {
		return nil
	}
	switch anyString(m["op"]) {
	case "add":
		var group *string
		if g := anyString(m["group"]); g != "" {
			group = &g
		}
		return &ariadnev1.PlanOp{
			Op: &ariadnev1.PlanOp_Add{
				Add: &ariadnev1.PlanAddNodeOp{
					Id:          anyString(m["id"]),
					Kind:        anyString(m["kind"]),
					Title:       anyString(m["title"]),
					Description: anyString(m["description"]),
					Acceptance:  anyString(m["acceptance"]),
					Group:       group,
				},
			},
		}
	case "update":
		upd := &ariadnev1.PlanUpdateNodeOp{
			Id:          anyString(m["id"]),
			Title:       anyStringPtr(m["title"]),
			Description: anyStringPtr(m["description"]),
			Acceptance:  anyStringPtr(m["acceptance"]),
			Group:       anyStringPtr(m["group"]),
		}
		if expect := asMap(m["expect"]); expect != nil {
			upd.Expect = &ariadnev1.PlanNodeExpect{
				Title:       anyStringPtr(expect["title"]),
				Description: anyStringPtr(expect["description"]),
				Acceptance:  anyStringPtr(expect["acceptance"]),
				Group:       anyStringPtr(expect["group"]),
			}
		}
		return &ariadnev1.PlanOp{Op: &ariadnev1.PlanOp_Update{Update: upd}}
	case "remove":
		return &ariadnev1.PlanOp{
			Op: &ariadnev1.PlanOp_Remove{
				Remove: &ariadnev1.PlanRemoveNodeOp{Id: anyString(m["id"])},
			},
		}
	case "link":
		return &ariadnev1.PlanOp{
			Op: &ariadnev1.PlanOp_Link{
				Link: &ariadnev1.PlanLinkNodesOp{
					From: anyString(m["from"]),
					To:   anyString(m["to"]),
				},
			},
		}
	case "unlink":
		return &ariadnev1.PlanOp{
			Op: &ariadnev1.PlanOp_Unlink{
				Unlink: &ariadnev1.PlanUnlinkNodesOp{
					From: anyString(m["from"]),
					To:   anyString(m["to"]),
				},
			},
		}
	default:
		return nil
	}
}

func fromProtoPlanOps(ops []*ariadnev1.PlanOp) []any {
	out := make([]any, 0, len(ops))
	for _, op := range ops {
		if op == nil {
			out = append(out, nil)
			continue
		}
		switch value := op.Op.(type) {
		case *ariadnev1.PlanOp_Add:
			entry := object{
				"op":          "add",
				"id":          value.Add.Id,
				"kind":        value.Add.Kind,
				"title":       value.Add.Title,
				"description": value.Add.Description,
				"acceptance":  value.Add.Acceptance,
			}
			if value.Add.Group != nil {
				entry["group"] = *value.Add.Group
			}
			out = append(out, entry)
		case *ariadnev1.PlanOp_Update:
			entry := object{
				"op": "update",
				"id": value.Update.Id,
			}
			if value.Update.Title != nil {
				entry["title"] = *value.Update.Title
			}
			if value.Update.Description != nil {
				entry["description"] = *value.Update.Description
			}
			if value.Update.Acceptance != nil {
				entry["acceptance"] = *value.Update.Acceptance
			}
			if value.Update.Group != nil {
				entry["group"] = *value.Update.Group
			}
			if value.Update.Expect != nil {
				expect := object{}
				if value.Update.Expect.Title != nil {
					expect["title"] = *value.Update.Expect.Title
				}
				if value.Update.Expect.Description != nil {
					expect["description"] = *value.Update.Expect.Description
				}
				if value.Update.Expect.Acceptance != nil {
					expect["acceptance"] = *value.Update.Expect.Acceptance
				}
				if value.Update.Expect.Group != nil {
					expect["group"] = *value.Update.Expect.Group
				}
				entry["expect"] = expect
			}
			out = append(out, entry)
		case *ariadnev1.PlanOp_Remove:
			out = append(out, object{"op": "remove", "id": value.Remove.Id})
		case *ariadnev1.PlanOp_Link:
			out = append(out, object{"op": "link", "from": value.Link.From, "to": value.Link.To})
		case *ariadnev1.PlanOp_Unlink:
			out = append(out, object{"op": "unlink", "from": value.Unlink.From, "to": value.Unlink.To})
		default:
			out = append(out, object{})
		}
	}
	return out
}

func toProtoPlanOps(raw any) []*ariadnev1.PlanOp {
	items := asMapSlice(raw)
	out := make([]*ariadnev1.PlanOp, 0, len(items))
	for _, item := range items {
		if op := toProtoPlanOp(item); op != nil {
			out = append(out, op)
		}
	}
	return out
}

func toProtoPlanRevisions(raw any) []*ariadnev1.PlanRevisionView {
	list := asMapSlice(raw)
	out := make([]*ariadnev1.PlanRevisionView, 0, len(list))
	for _, rev := range list {
		out = append(out, &ariadnev1.PlanRevisionView{
			Id:         anyString(rev["id"]),
			Author:     anyString(rev["author"]),
			Reason:     anyString(rev["reason"]),
			Ops:        toProtoPlanOps(rev["ops"]),
			RevertOf:   anyStringPtr(rev["revertOf"]),
			RevertedBy: anyStringPtr(rev["revertedBy"]),
			CreatedAt:  anyInt64(rev["createdAt"]),
		})
	}
	return out
}

func toProtoPlanPreview(raw any) *ariadnev1.PlanPreview {
	m := asMap(raw)
	if m == nil {
		return nil
	}
	nodes := []*ariadnev1.PlanPreviewNode{}
	for _, n := range asMapSlice(m["nodes"]) {
		var group *string
		if g := anyString(n["group"]); g != "" {
			group = &g
		}
		nodes = append(nodes, &ariadnev1.PlanPreviewNode{
			Id:          anyString(n["id"]),
			Kind:        anyString(n["kind"]),
			Title:       anyString(n["title"]),
			Description: anyString(n["description"]),
			Acceptance:  anyString(n["acceptance"]),
			Mark:        anyStringPtr(n["mark"]),
			DependsOn:   anyStringSlice(n["dependsOn"]),
			Group:       group,
		})
	}
	edges := []*ariadnev1.PlanPreviewEdge{}
	for _, e := range asMapSlice(m["edges"]) {
		edges = append(edges, &ariadnev1.PlanPreviewEdge{
			From: anyString(e["from"]),
			To:   anyString(e["to"]),
			Mark: anyStringPtr(e["mark"]),
		})
	}
	return &ariadnev1.PlanPreview{Nodes: nodes, Edges: edges}
}

func toProtoPlanView(input any) *ariadnev1.PlanView {
	raw := asMap(input)
	if raw == nil {
		return &ariadnev1.PlanView{}
	}
	nodes := []*ariadnev1.PlanNodeView{}
	for _, n := range asMapSlice(raw["nodes"]) {
		threads := []*ariadnev1.PlanNodeThread{}
		for _, t := range asMapSlice(n["threads"]) {
			var pr *ariadnev1.PlanNodePullRequest
			if rawPR := asMap(t["pullRequest"]); rawPR != nil {
				checks := &ariadnev1.PlanPullRequestChecks{}
				if c := asMap(rawPR["checks"]); c != nil {
					checks.State = anyString(c["state"])
					checks.Failed = anyStringSlice(c["failed"])
				}
				pr = &ariadnev1.PlanNodePullRequest{
					Number:     anyInt64(rawPR["number"]),
					Reference:  anyStringPtr(rawPR["reference"]),
					Url:        anyString(rawPR["url"]),
					State:      anyString(rawPR["state"]),
					Conflicted: anyBool(rawPR["conflicted"]),
					Checks:     checks,
				}
			}
			threads = append(threads, &ariadnev1.PlanNodeThread{
				Id:           anyString(t["id"]),
				Title:        anyString(t["title"]),
				Stage:        anyString(t["stage"]),
				StoppedBy:    anyStringPtr(t["stoppedBy"]),
				LastHeadline: anyStringPtr(t["lastHeadline"]),
				PullRequest:  pr,
			})
		}
		issues := []*ariadnev1.PlanLinkedIssue{}
		for _, issue := range asMapSlice(n["issues"]) {
			issues = append(issues, &ariadnev1.PlanLinkedIssue{
				Tracker:  anyString(issue["tracker"]),
				Number:   anyInt64(issue["number"]),
				Title:    anyString(issue["title"]),
				Url:      anyString(issue["url"]),
				LinkedBy: anyString(issue["linkedBy"]),
			})
		}
		var group *string
		if g := anyString(n["group"]); g != "" {
			group = &g
		}
		nodes = append(nodes, &ariadnev1.PlanNodeView{
			Id:          anyString(n["id"]),
			Kind:        anyString(n["kind"]),
			Title:       anyString(n["title"]),
			Description: anyString(n["description"]),
			Acceptance:  anyString(n["acceptance"]),
			DependsOn:   anyStringSlice(n["dependsOn"]),
			Status:      anyString(n["status"]),
			Threads:     threads,
			ChangedIn:   anyStringPtr(n["changedIn"]),
			Issues:      issues,
			Gate:        anyStringPtr(n["gate"]),
			Group:       group,
		})
	}
	proposals := []*ariadnev1.PlanProposalView{}
	for _, p := range asMapSlice(raw["proposals"]) {
		changes := []*ariadnev1.LocalizedError{}
		for _, c := range asMapSlice(p["changes"]) {
			if msg := toProtoLocalizedMessage(c); msg != nil {
				changes = append(changes, msg)
			}
		}
		proposals = append(proposals, &ariadnev1.PlanProposalView{
			Id:          anyString(p["id"]),
			AttentionId: anyString(p["attentionId"]),
			Reason:      anyString(p["reason"]),
			Ops:         toProtoPlanOps(p["ops"]),
			Changes:     changes,
			Preview:     toProtoPlanPreview(p["preview"]),
			Status:      anyString(p["status"]),
			StaleReason: anyStringPtr(p["staleReason"]),
			CreatedAt:   anyInt64(p["createdAt"]),
		})
	}
	return &ariadnev1.PlanView{
		ProjectId: anyString(raw["projectId"]),
		Nodes:     nodes,
		Revisions: toProtoPlanRevisions(raw["revisions"]),
		Proposals: proposals,
	}
}

func toProtoPlanUndoPreview(input any) *ariadnev1.PlanUndoPreview {
	raw := asMap(input)
	if raw == nil {
		return &ariadnev1.PlanUndoPreview{}
	}
	changes := []*ariadnev1.LocalizedError{}
	for _, item := range asMapSlice(raw["changes"]) {
		if msg := toProtoLocalizedMessage(item); msg != nil {
			changes = append(changes, msg)
		}
	}
	threads := []*ariadnev1.PlanUndoThread{}
	for _, item := range asMapSlice(raw["threads"]) {
		threads = append(threads, &ariadnev1.PlanUndoThread{
			Id:    anyString(item["id"]),
			Title: anyString(item["title"]),
		})
	}
	return &ariadnev1.PlanUndoPreview{
		Changes: changes,
		Threads: threads,
		Blocked: toProtoLocalizedMessage(raw["blocked"]),
	}
}
