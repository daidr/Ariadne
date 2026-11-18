package server

import (
	"encoding/json"
	"math"
	"net/http"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/domainerr"
)

func (api *API) registerPlans() {
	repo := api.options.Repository
	api.add("POST", "/api/projects/:projectId/plan/nodes/:nodeId/issues", func(r *http.Request) (any, int, error) {
		body, err := readJSON(r)
		if err != nil {
			return nil, 0, err
		}
		number, ok := body["number"].(float64)
		if !ok || number < 1 || math.Trunc(number) != number {
			return nil, 0, badInput("number")
		}
		project, node := r.PathValue("projectId"), r.PathValue("nodeId")
		if err := repo.RequirePlanNode(r.Context(), project, node); err != nil {
			return nil, 0, err
		}
		manager := api.options.Workspaces
		if manager == nil || !manager.HasTracker(r.Context(), project) {
			return nil, 0, domainerr.New("error.noTracker", nil)
		}
		tracker, err := manager.Tracker(r.Context(), project)
		if err != nil {
			return nil, 0, err
		}
		if number >= float64(math.MaxInt) {
			return nil, 0, badInput("number")
		}
		issue, err := tracker.Issue(r.Context(), int(number))
		if err != nil {
			return nil, 0, domainerr.New("error.issue", object{"detail": err.Error()})
		}
		plan, err := repo.LinkIssue(r.Context(), project, node, core.LinkedIssue{Tracker: tracker.Name, Number: number, Title: issue.Title, URL: issue.URL}, "user")
		return object{"plan": plan}, 200, err
	})
	api.add("DELETE", "/api/projects/:projectId/plan/nodes/:nodeId/issues/:number", func(r *http.Request) (any, int, error) {
		project, node := r.PathValue("projectId"), r.PathValue("nodeId")
		plan, err := repo.GetPlan(r.Context(), project)
		if err != nil {
			return nil, 0, err
		}
		issues, err := repo.PlanIssues(r.Context(), project, node)
		if err != nil {
			return nil, 0, err
		}
		for _, issue := range issues {
			encoded, _ := json.Marshal(issue.Number)
			if string(encoded) != r.PathValue("number") {
				continue
			}
			plan, err = repo.UnlinkIssue(r.Context(), project, node, issue.Tracker, issue.Number, "user")
			break
		}
		return object{"plan": plan}, 200, err
	})
	plan := func(r *http.Request) (any, int, error) {
		value, err := repo.GetPlan(r.Context(), r.PathValue("projectId"))
		return object{"plan": value}, 200, err
	}
	api.add("GET", "/api/projects/:projectId/plan", plan)
	api.add("POST", "/api/projects/:projectId/plan/proposals/:proposalId/dismiss", func(r *http.Request) (any, int, error) {
		if err := repo.DismissPlanProposal(r.Context(), r.PathValue("projectId"), r.PathValue("proposalId")); err != nil {
			return nil, 0, err
		}
		return plan(r)
	})
	api.add("POST", "/api/projects/:projectId/plan/revise", func(r *http.Request) (any, int, error) {
		body, err := readJSON(r)
		if err != nil {
			return nil, 0, err
		}
		ops, err := core.ParsePlanOps(body["ops"])
		if err != nil {
			return nil, 0, err
		}
		id := r.PathValue("projectId")
		change, err := repo.RevisePlan(r.Context(), id, core.PlanRevision{Author: "user", Reason: text(body, "reason"), Ops: ops})
		if err != nil {
			return nil, 0, err
		}
		value, err := repo.GetPlan(r.Context(), id)
		return object{"change": change, "plan": value}, 200, err
	})
	api.add("GET", "/api/projects/:projectId/plan/revisions/:revisionId/revert", func(r *http.Request) (any, int, error) {
		value, err := repo.PreviewPlanUndo(r.Context(), r.PathValue("projectId"), r.PathValue("revisionId"))
		return object{"preview": value}, 200, err
	})
	api.add("POST", "/api/projects/:projectId/plan/revisions/:revisionId/revert", func(r *http.Request) (any, int, error) {
		body, err := readJSON(r)
		if err != nil {
			return nil, 0, err
		}
		if _, err := repo.RevertPlanRevision(r.Context(), r.PathValue("projectId"), r.PathValue("revisionId"), "user", text(body, "reason"), nil); err != nil {
			return nil, 0, err
		}
		return plan(r)
	})
}
