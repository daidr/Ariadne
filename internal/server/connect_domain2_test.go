package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/daidr/Ariadne/internal/core"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/internal/sources"
	"github.com/daidr/Ariadne/internal/workspaces"
	"github.com/daidr/Ariadne/sdk"
)

func newDomain2TestServer(t *testing.T) (
	*httptest.Server,
	ariadnev1connect.ProjectServiceClient,
	ariadnev1connect.PlanServiceClient,
	ariadnev1connect.ArtifactServiceClient,
	ariadnev1connect.IssueServiceClient,
	func(),
) {
	t.Helper()
	home := t.TempDir()
	nowMs := int64(1_800_000_000_000)
	now := func() time.Time {
		nowMs += 1000
		return time.UnixMilli(nowMs)
	}
	repo, err := core.Open(t.Context(), core.Options{
		Path: ":memory:",
		Now:  now,
	})
	if err != nil {
		t.Fatal(err)
	}
	settingsStore, err := settings.Open(filepath.Join(home, "settings.json"))
	if err != nil {
		repo.Close()
		t.Fatal(err)
	}
	if err := settingsStore.UpdateMCP(object{
		"servers": []any{
			object{
				"id":        "srv-alpha",
				"name":      "Alpha MCP",
				"transport": "http",
				"url":       "http://127.0.0.1:9999/mcp",
				"env":       []any{},
				"headers":   []any{},
				"tools":     object{},
			},
			object{
				"id":        "srv-beta",
				"name":      "Beta MCP",
				"transport": "http",
				"url":       "http://127.0.0.1:9998/mcp",
				"env":       []any{},
				"headers":   []any{},
				"tools":     object{},
			},
		},
	}); err != nil {
		repo.Close()
		t.Fatal(err)
	}

	author := "alice"
	registry := sources.New()
	registry.Register(sdk.ContextProvider{
		Info: sdk.ContextProviderInfo{
			Kind:     "fixture",
			Title:    sdk.LocalizedText{"zh-CN": "测试源", "en": "Fixture"},
			Settings: sdk.SettingsSchema{Type: "object", Properties: map[string]sdk.SettingField{}},
			Provides: []string{"tracker"},
		},
		Connect: func(context.Context, map[string]any) (sdk.Capabilities, error) {
			return sdk.Capabilities{
				Tracker: &sdk.TrackerCapability{
					Name:   "fixture-tracker",
					WebURL: "https://tracker.example.test",
					List: func(_ context.Context, q sdk.IssueQuery) ([]sdk.IssueSummary, error) {
						items := []sdk.IssueSummary{
							{
								Number:    101,
								URL:       "https://tracker.example.test/issues/101",
								Title:     "First issue",
								State:     "open",
								Author:    &author,
								Labels:    []string{"bug", "p1"},
								Comments:  2,
								UpdatedAt: "2026-11-20T00:00:00Z",
							},
							{
								Number:    102,
								URL:       "https://tracker.example.test/issues/102",
								Title:     "Second issue",
								State:     "closed",
								Labels:    []string{"chore"},
								Comments:  0,
								UpdatedAt: "2026-11-21T00:00:00Z",
							},
						}
						if q.State != "" {
							filtered := []sdk.IssueSummary{}
							for _, item := range items {
								if item.State == q.State {
									filtered = append(filtered, item)
								}
							}
							items = filtered
						}
						if q.Limit > 0 && len(items) > q.Limit {
							items = items[:q.Limit]
						}
						return items, nil
					},
					Issue: func(_ context.Context, number int) (sdk.IssueDetail, error) {
						return sdk.IssueDetail{
							IssueSummary: sdk.IssueSummary{
								Number:    number,
								URL:       "https://tracker.example.test/issues/101",
								Title:     "First issue",
								State:     "open",
								Author:    &author,
								Labels:    []string{"bug"},
								Comments:  1,
								UpdatedAt: "2026-11-20T00:00:00Z",
							},
							Body:      "Issue body details",
							CreatedAt: "2026-11-19T00:00:00Z",
							Recent: []sdk.IssueComment{
								{
									ID:        "ic_1",
									Author:    &author,
									Body:      "Looks reproducible",
									CreatedAt: "2026-11-20T01:00:00Z",
									URL:       "https://tracker.example.test/issues/101#c1",
								},
							},
						}, nil
					},
				},
			}, nil
		},
	})

	manager, err := workspaces.New(workspaces.Options{
		Repository: repo,
		Sources:    registry,
	})
	if err != nil {
		repo.Close()
		t.Fatal(err)
	}

	api, err := New(Options{
		Repository: repo,
		Settings:   settingsStore,
		Sources:    registry,
		Workspaces: manager,
		Token:      "domain2-token",
		Now:        now,
	})
	if err != nil {
		_ = manager.Close(context.Background())
		repo.Close()
		t.Fatal(err)
	}

	srv := httptest.NewServer(api)
	clientOpts := []connect.ClientOption{
		connect.WithInterceptors(bearerClientInterceptor("domain2-token")),
	}
	baseURL := srv.URL + rpcPrefix
	projects := ariadnev1connect.NewProjectServiceClient(srv.Client(), baseURL, clientOpts...)
	plans := ariadnev1connect.NewPlanServiceClient(srv.Client(), baseURL, clientOpts...)
	artifacts := ariadnev1connect.NewArtifactServiceClient(srv.Client(), baseURL, clientOpts...)
	issues := ariadnev1connect.NewIssueServiceClient(srv.Client(), baseURL, clientOpts...)

	cleanup := func() {
		srv.Close()
		api.Close()
		_ = manager.Close(context.Background())
		repo.Close()
	}
	return srv, projects, plans, artifacts, issues, cleanup
}

func TestProjectServiceAtomicUpdateAndCRUD(t *testing.T) {
	t.Parallel()
	srv, projects, _, _, _, cleanup := newDomain2TestServer(t)
	defer cleanup()

	// Unauthenticated request should fail with Unauthenticated.
	anonClient := ariadnev1connect.NewProjectServiceClient(srv.Client(), srv.URL+rpcPrefix)
	if _, err := anonClient.ListProjects(t.Context(), connect.NewRequest(&ariadnev1.ListProjectsRequest{})); err == nil {
		t.Fatal("expected unauthenticated ListProjects to fail")
	} else if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("unexpected auth error code: %v", connect.CodeOf(err))
	}

	// Missing project returns NotFound with LocalizedError detail.
	if _, err := projects.GetProject(t.Context(), connect.NewRequest(&ariadnev1.GetProjectRequest{ProjectId: "prj_missing"})); err == nil {
		t.Fatal("expected missing GetProject to fail")
	} else {
		cerr, localized := extractLocalizedError(t, err)
		if cerr.Code() != connect.CodeNotFound || localized == nil || localized.Key != "error.notFound" {
			t.Fatalf("unexpected notFound error: code=%v localized=%v", cerr.Code(), localized)
		}
	}

	name := "Initial Project"
	created, err := projects.CreateProject(t.Context(), connect.NewRequest(&ariadnev1.CreateProjectRequest{
		Name: &name,
	}))
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	projectID := created.Msg.Project.Id
	if created.Msg.Project.Name != "Initial Project" {
		t.Fatalf("unexpected project name: %q", created.Msg.Project.Name)
	}

	// D-4: Atomic UpdateProject combines name + settings + env + slots + mcp + autopilotPaused in one RPC.
	renamed := "Unified Project"
	paused := true
	updated, err := projects.UpdateProject(t.Context(), connect.NewRequest(&ariadnev1.UpdateProjectRequest{
		ProjectId: projectID,
		Name:      &renamed,
		Settings: &ariadnev1.ProjectSettings{
			Instructions: "Follow Go conventions",
			Delivery:     &ariadnev1.ProjectDeliverySettings{Mode: "merge", Instructions: ""},
			Tools:        []string{"fetch"},
			Workspace: &ariadnev1.ProjectWorkspaceSettings{
				Retention: "auto",
				Auto:      &ariadnev1.ProjectWorkspaceAutoSettings{Setup: true, Check: false},
				Shell:     "auto",
			},
			Review:    &ariadnev1.ProjectReviewSettings{Enabled: true},
			Autopilot: &ariadnev1.ProjectAutopilotSettings{Enabled: true, Method: "squash"},
		},
		Env: &ariadnev1.ProjectEnvVarList{
			Entries: []*ariadnev1.ProjectEnvVar{{Name: "API_MODE", Value: "staging"}},
		},
		SlotsAction:     &ariadnev1.UpdateProjectRequest_SetSlots{SetSlots: 4},
		AutopilotPaused: &paused,
		McpServers:      &ariadnev1.ProjectStringList{Values: []string{"srv-alpha", "srv-beta"}},
	}))
	if err != nil {
		t.Fatalf("UpdateProject atomic: %v", err)
	}
	proj := updated.Msg.Project
	if proj.Name != "Unified Project" {
		t.Fatalf("name = %q, want Unified Project", proj.Name)
	}
	if proj.Slots == nil || *proj.Slots != 4 {
		t.Fatalf("slots = %v, want 4", proj.Slots)
	}
	if len(proj.Mcp) != 2 || proj.Mcp[0] != "srv-alpha" || proj.Mcp[1] != "srv-beta" {
		t.Fatalf("mcp = %v, want [srv-alpha srv-beta]", proj.Mcp)
	}
	if !proj.AutopilotPaused || !proj.Settings.Autopilot.Enabled || proj.Settings.Autopilot.Method != "squash" {
		t.Fatalf("unexpected autopilot state: paused=%v settings=%+v", proj.AutopilotPaused, proj.Settings.Autopilot)
	}
	if len(updated.Msg.SettingsView.Env) != 1 || updated.Msg.SettingsView.Env[0] != "API_MODE" {
		t.Fatalf("env = %v, want [API_MODE]", updated.Msg.SettingsView.Env)
	}

	// D-4 atomicity check: invalid slots must reject the entire update and leave name unchanged.
	badName := "Should Not Commit"
	if _, err := projects.UpdateProject(t.Context(), connect.NewRequest(&ariadnev1.UpdateProjectRequest{
		ProjectId:   projectID,
		Name:        &badName,
		SlotsAction: &ariadnev1.UpdateProjectRequest_SetSlots{SetSlots: 99},
	})); err == nil {
		t.Fatal("expected invalid slots UpdateProject to fail")
	}
	detail, err := projects.GetProject(t.Context(), connect.NewRequest(&ariadnev1.GetProjectRequest{
		ProjectId: projectID,
	}))
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if detail.Msg.Project.Name != "Unified Project" {
		t.Fatalf("atomic rollback failed, name changed to %q", detail.Msg.Project.Name)
	}

	// Clear slots via oneof clear_slots.
	cleared, err := projects.UpdateProject(t.Context(), connect.NewRequest(&ariadnev1.UpdateProjectRequest{
		ProjectId:   projectID,
		SlotsAction: &ariadnev1.UpdateProjectRequest_ClearSlots{ClearSlots: true},
	}))
	if err != nil {
		t.Fatalf("clear slots: %v", err)
	}
	if cleared.Msg.Project.Slots != nil {
		t.Fatalf("expected cleared slots to be nil, got %v", *cleared.Msg.Project.Slots)
	}

	// ListProjects, GetProjectActivity, SearchProject, DeleteProject.
	listed, err := projects.ListProjects(t.Context(), connect.NewRequest(&ariadnev1.ListProjectsRequest{}))
	if err != nil || len(listed.Msg.Projects) != 1 {
		t.Fatalf("ListProjects: err=%v count=%d", err, len(listed.Msg.Projects))
	}
	activity, err := projects.GetProjectActivity(t.Context(), connect.NewRequest(&ariadnev1.GetProjectActivityRequest{ProjectId: projectID}))
	if err != nil || activity.Msg.Coordinator.Status == "" {
		t.Fatalf("GetProjectActivity: err=%v resp=%+v", err, activity)
	}
	searched, err := projects.SearchProject(t.Context(), connect.NewRequest(&ariadnev1.SearchProjectRequest{
		ProjectId: projectID,
		Query:     "Unified",
	}))
	if err != nil || len(searched.Msg.Terms) == 0 {
		t.Fatalf("SearchProject: err=%v terms=%v", err, searched.Msg.Terms)
	}
	if _, err := projects.DeleteProject(t.Context(), connect.NewRequest(&ariadnev1.DeleteProjectRequest{
		ProjectId: projectID,
	})); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	afterDelete, err := projects.ListProjects(t.Context(), connect.NewRequest(&ariadnev1.ListProjectsRequest{}))
	if err != nil || len(afterDelete.Msg.Projects) != 0 {
		t.Fatalf("ListProjects after delete: err=%v count=%d", err, len(afterDelete.Msg.Projects))
	}
}

func TestArtifactServicePaginationAndSplitComments(t *testing.T) {
	t.Parallel()
	_, projects, _, artifacts, _, cleanup := newDomain2TestServer(t)
	defer cleanup()

	name := "Artifact Project"
	createdProj, err := projects.CreateProject(t.Context(), connect.NewRequest(&ariadnev1.CreateProjectRequest{Name: &name}))
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	projectID := createdProj.Msg.Project.Id

	// Create 3 artifacts for P-6 list pagination.
	var firstID string
	for i, title := range []string{"Doc A", "Doc B", "Doc C"} {
		res, err := artifacts.CreateArtifact(t.Context(), connect.NewRequest(&ariadnev1.CreateArtifactRequest{
			ProjectId: projectID,
			Kind:      "document",
			Title:     title,
		}))
		if err != nil {
			t.Fatalf("CreateArtifact %s: %v", title, err)
		}
		if i == 0 {
			firstID = res.Msg.Artifact.Id
		}
	}

	// P-6: ListArtifacts cursor pagination (pageSize=2 -> 2 items + 1 item).
	page1, err := artifacts.ListArtifacts(t.Context(), connect.NewRequest(&ariadnev1.ListArtifactsRequest{
		ProjectId: projectID,
		PageSize:  2,
	}))
	if err != nil {
		t.Fatalf("ListArtifacts page 1: %v", err)
	}
	if len(page1.Msg.Artifacts) != 2 || !page1.Msg.HasMore || page1.Msg.NextPageToken == "" {
		t.Fatalf("unexpected page 1: len=%d hasMore=%v token=%q", len(page1.Msg.Artifacts), page1.Msg.HasMore, page1.Msg.NextPageToken)
	}
	page2, err := artifacts.ListArtifacts(t.Context(), connect.NewRequest(&ariadnev1.ListArtifactsRequest{
		ProjectId: projectID,
		PageSize:  2,
		PageToken: page1.Msg.NextPageToken,
	}))
	if err != nil {
		t.Fatalf("ListArtifacts page 2: %v", err)
	}
	if len(page2.Msg.Artifacts) != 1 || page2.Msg.HasMore {
		t.Fatalf("unexpected page 2: len=%d hasMore=%v", len(page2.Msg.Artifacts), page2.Msg.HasMore)
	}

	// Create multiple versions on firstID to test P-6 version pagination & include_versions=false.
	for v := int64(1); v <= 3; v++ {
		content := "version content " + string(rune('0'+v))
		saved, err := artifacts.SaveArtifact(t.Context(), connect.NewRequest(&ariadnev1.SaveArtifactRequest{
			ArtifactId:      firstID,
			Content:         &content,
			BaseVersion:     v,
			Reason:          "save step",
			IncludeVersions: false,
		}))
		if err != nil {
			t.Fatalf("SaveArtifact v%d: %v", v, err)
		}
		if len(saved.Msg.Versions) != 0 {
			t.Fatalf("expected empty versions when include_versions=false, got %d", len(saved.Msg.Versions))
		}
	}

	// GetArtifact with include_versions=false vs paginated versions.
	noVers, err := artifacts.GetArtifact(t.Context(), connect.NewRequest(&ariadnev1.GetArtifactRequest{
		ArtifactId:      firstID,
		IncludeVersions: false,
	}))
	if err != nil || len(noVers.Msg.Versions) != 0 || noVers.Msg.Artifact.Version != 4 {
		t.Fatalf("GetArtifact without versions: err=%v ver=%d len=%d", err, noVers.Msg.Artifact.Version, len(noVers.Msg.Versions))
	}

	verPage1, err := artifacts.ListArtifactVersions(t.Context(), connect.NewRequest(&ariadnev1.ListArtifactVersionsRequest{
		ArtifactId: firstID,
		PageSize:   2,
	}))
	if err != nil {
		t.Fatalf("ListArtifactVersions page 1: %v", err)
	}
	if len(verPage1.Msg.Versions) != 2 || !verPage1.Msg.HasMore || verPage1.Msg.NextPageToken == "" {
		t.Fatalf("unexpected version page 1: len=%d hasMore=%v", len(verPage1.Msg.Versions), verPage1.Msg.HasMore)
	}
	if verPage1.Msg.Versions[0].Version != 4 || verPage1.Msg.Versions[1].Version != 3 {
		t.Fatalf("unexpected version order on page 1: %d, %d", verPage1.Msg.Versions[0].Version, verPage1.Msg.Versions[1].Version)
	}
	verPage2, err := artifacts.ListArtifactVersions(t.Context(), connect.NewRequest(&ariadnev1.ListArtifactVersionsRequest{
		ArtifactId: firstID,
		PageSize:   2,
		PageToken:  verPage1.Msg.NextPageToken,
	}))
	if err != nil {
		t.Fatalf("ListArtifactVersions page 2: %v", err)
	}
	if len(verPage2.Msg.Versions) != 2 || verPage2.Msg.HasMore {
		t.Fatalf("unexpected version page 2: len=%d hasMore=%v", len(verPage2.Msg.Versions), verPage2.Msg.HasMore)
	}
	if verPage2.Msg.Versions[0].Version != 2 || verPage2.Msg.Versions[1].Version != 1 {
		t.Fatalf("unexpected version order on page 2: %d, %d", verPage2.Msg.Versions[0].Version, verPage2.Msg.Versions[1].Version)
	}

	// D-5: Test all 7 dedicated comment lifecycle RPCs.
	c1, err := artifacts.CreateArtifactComment(t.Context(), connect.NewRequest(&ariadnev1.CreateArtifactCommentRequest{
		ArtifactId: firstID,
		ThreadId:   "ct_1",
		CommentId:  "cm_1",
		Quote:      "version content 3",
		Body:       "Initial comment",
	}))
	if err != nil || len(c1.Msg.Comments) != 1 || len(c1.Msg.Comments[0].Comments) != 1 {
		t.Fatalf("CreateArtifactComment: err=%v comments=%+v", err, c1)
	}

	c2, err := artifacts.ReplyArtifactComment(t.Context(), connect.NewRequest(&ariadnev1.ReplyArtifactCommentRequest{
		ArtifactId: firstID,
		ThreadId:   "ct_1",
		CommentId:  "cm_2",
		Body:       "Reply comment",
	}))
	if err != nil || len(c2.Msg.Comments[0].Comments) != 2 {
		t.Fatalf("ReplyArtifactComment: err=%v comments=%+v", err, c2)
	}

	c3, err := artifacts.EditArtifactComment(t.Context(), connect.NewRequest(&ariadnev1.EditArtifactCommentRequest{
		ArtifactId: firstID,
		ThreadId:   "ct_1",
		CommentId:  "cm_2",
		Body:       "Edited reply",
	}))
	if err != nil || c3.Msg.Comments[0].Comments[1].Body != "Edited reply" || c3.Msg.Comments[0].Comments[1].EditedAt == nil {
		t.Fatalf("EditArtifactComment: err=%v comment=%+v", err, c3.Msg.Comments[0].Comments[1])
	}

	c4, err := artifacts.ResolveArtifactCommentThread(t.Context(), connect.NewRequest(&ariadnev1.ResolveArtifactCommentThreadRequest{
		ArtifactId: firstID,
		ThreadId:   "ct_1",
	}))
	if err != nil || c4.Msg.Comments[0].Resolved == nil {
		t.Fatalf("ResolveArtifactCommentThread: err=%v resolved=%+v", err, c4.Msg.Comments[0].Resolved)
	}

	c5, err := artifacts.ReopenArtifactCommentThread(t.Context(), connect.NewRequest(&ariadnev1.ReopenArtifactCommentThreadRequest{
		ArtifactId: firstID,
		ThreadId:   "ct_1",
	}))
	if err != nil || c5.Msg.Comments[0].Resolved != nil {
		t.Fatalf("ReopenArtifactCommentThread: err=%v resolved=%+v", err, c5.Msg.Comments[0].Resolved)
	}

	c6, err := artifacts.DeleteArtifactComment(t.Context(), connect.NewRequest(&ariadnev1.DeleteArtifactCommentRequest{
		ArtifactId: firstID,
		ThreadId:   "ct_1",
		CommentId:  "cm_2",
	}))
	if err != nil || len(c6.Msg.Comments[0].Comments) != 1 {
		t.Fatalf("DeleteArtifactComment: err=%v comments=%+v", err, c6.Msg.Comments[0].Comments)
	}

	c7, err := artifacts.RemoveArtifactCommentThread(t.Context(), connect.NewRequest(&ariadnev1.RemoveArtifactCommentThreadRequest{
		ArtifactId: firstID,
		ThreadId:   "ct_1",
	}))
	if err != nil || len(c7.Msg.Comments) != 0 {
		t.Fatalf("RemoveArtifactCommentThread: err=%v len=%d", err, len(c7.Msg.Comments))
	}
}

func TestPlanServiceAndIssueServiceEndToEnd(t *testing.T) {
	t.Parallel()
	_, projects, plans, _, issues, cleanup := newDomain2TestServer(t)
	defer cleanup()

	name := "Plan & Issue Project"
	emptyStruct, _ := structpb.NewStruct(map[string]any{})
	createdProj, err := projects.CreateProject(t.Context(), connect.NewRequest(&ariadnev1.CreateProjectRequest{
		Name: &name,
		Source: &ariadnev1.ProjectSource{
			Kind:     "fixture",
			Settings: emptyStruct,
		},
	}))
	if err != nil {
		t.Fatalf("CreateProject with fixture tracker: %v", err)
	}
	projectID := createdProj.Msg.Project.Id

	// Check IssueService ListIssues & GetIssue.
	listedIssues, err := issues.ListIssues(t.Context(), connect.NewRequest(&ariadnev1.ListIssuesRequest{
		ProjectId: projectID,
		State:     "open",
		Limit:     10,
	}))
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	if listedIssues.Msg.Tracker != "fixture-tracker" || len(listedIssues.Msg.Issues) != 1 || listedIssues.Msg.Issues[0].Number != 101 {
		t.Fatalf("unexpected ListIssues response: %+v", listedIssues.Msg)
	}

	issueDetail, err := issues.GetIssue(t.Context(), connect.NewRequest(&ariadnev1.GetIssueRequest{
		ProjectId: projectID,
		Number:    101,
	}))
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if issueDetail.Msg.Issue.Body != "Issue body details" || len(issueDetail.Msg.Issue.Recent) != 1 {
		t.Fatalf("unexpected GetIssue response: %+v", issueDetail.Msg)
	}

	// Create 3 plan revisions to test PlanService CRUD + P-6 revision pagination + Undo + Link/Unlink issue.
	groupAlpha := "Phase 1"
	rev1, err := plans.RevisePlan(t.Context(), connect.NewRequest(&ariadnev1.RevisePlanRequest{
		ProjectId: projectID,
		Reason:    "add node A",
		Ops: []*ariadnev1.PlanOp{{
			Op: &ariadnev1.PlanOp_Add{Add: &ariadnev1.PlanAddNodeOp{
				Id:          "node_a",
				Kind:        "task",
				Title:       "Task A",
				Description: "First task",
				Acceptance:  "Done A",
				Group:       &groupAlpha,
			}},
		}},
	}))
	if err != nil || len(rev1.Msg.Plan.Nodes) != 1 || rev1.Msg.Plan.Nodes[0].Group == nil || *rev1.Msg.Plan.Nodes[0].Group != "Phase 1" {
		t.Fatalf("RevisePlan 1: err=%v nodes=%+v", err, rev1)
	}

	rev2, err := plans.RevisePlan(t.Context(), connect.NewRequest(&ariadnev1.RevisePlanRequest{
		ProjectId: projectID,
		Reason:    "add node B",
		Ops: []*ariadnev1.PlanOp{
			{Op: &ariadnev1.PlanOp_Add{Add: &ariadnev1.PlanAddNodeOp{
				Id:          "node_b",
				Kind:        "task",
				Title:       "Task B",
				Description: "Second task",
				Acceptance:  "Done B",
			}}},
			{Op: &ariadnev1.PlanOp_Link{Link: &ariadnev1.PlanLinkNodesOp{From: "node_a", To: "node_b"}}},
		},
	}))
	if err != nil || len(rev2.Msg.Plan.Nodes) != 2 {
		t.Fatalf("RevisePlan 2: err=%v", err)
	}

	updatedTitle := "Task B Updated"
	groupBeta := "Phase 2"
	rev3, err := plans.RevisePlan(t.Context(), connect.NewRequest(&ariadnev1.RevisePlanRequest{
		ProjectId: projectID,
		Reason:    "rename node B",
		Ops: []*ariadnev1.PlanOp{{
			Op: &ariadnev1.PlanOp_Update{Update: &ariadnev1.PlanUpdateNodeOp{
				Id:    "node_b",
				Title: &updatedTitle,
				Group: &groupBeta,
			}},
		}},
	}))
	if err != nil || rev3.Msg.Plan.Nodes[1].Group == nil || *rev3.Msg.Plan.Nodes[1].Group != "Phase 2" {
		t.Fatalf("RevisePlan 3: err=%v nodes=%+v", err, rev3)
	}
	lastRevID := rev3.Msg.Change.RevisionId

	// P-6: GetPlan with revision_page_size=2 + ListPlanRevisions with next token.
	planPage, err := plans.GetPlan(t.Context(), connect.NewRequest(&ariadnev1.GetPlanRequest{
		ProjectId:        projectID,
		RevisionPageSize: 2,
	}))
	if err != nil {
		t.Fatalf("GetPlan paginated: %v", err)
	}
	if len(planPage.Msg.Plan.Revisions) != 2 || !planPage.Msg.HasMoreRevisions || planPage.Msg.NextRevisionPageToken == "" {
		t.Fatalf("unexpected GetPlan revision page: len=%d hasMore=%v token=%q",
			len(planPage.Msg.Plan.Revisions), planPage.Msg.HasMoreRevisions, planPage.Msg.NextRevisionPageToken)
	}
	moreRevs, err := plans.ListPlanRevisions(t.Context(), connect.NewRequest(&ariadnev1.ListPlanRevisionsRequest{
		ProjectId: projectID,
		PageSize:  2,
		PageToken: planPage.Msg.NextRevisionPageToken,
	}))
	if err != nil {
		t.Fatalf("ListPlanRevisions page 2: %v", err)
	}
	if len(moreRevs.Msg.Revisions) != 1 || moreRevs.Msg.HasMore {
		t.Fatalf("unexpected ListPlanRevisions page 2: len=%d hasMore=%v", len(moreRevs.Msg.Revisions), moreRevs.Msg.HasMore)
	}

	// Link and unlink issue on plan node.
	linked, err := plans.LinkPlanNodeIssue(t.Context(), connect.NewRequest(&ariadnev1.LinkPlanNodeIssueRequest{
		ProjectId: projectID,
		NodeId:    "node_a",
		Number:    101,
	}))
	if err != nil || len(linked.Msg.Plan.Nodes[0].Issues) != 1 || linked.Msg.Plan.Nodes[0].Issues[0].Number != 101 {
		t.Fatalf("LinkPlanNodeIssue: err=%v issues=%+v", err, linked.Msg.Plan.Nodes[0].Issues)
	}
	unlinked, err := plans.UnlinkPlanNodeIssue(t.Context(), connect.NewRequest(&ariadnev1.UnlinkPlanNodeIssueRequest{
		ProjectId: projectID,
		NodeId:    "node_a",
		Number:    101,
	}))
	if err != nil || len(unlinked.Msg.Plan.Nodes[0].Issues) != 0 {
		t.Fatalf("UnlinkPlanNodeIssue: err=%v issues=%+v", err, unlinked.Msg.Plan.Nodes[0].Issues)
	}

	// PreviewPlanUndo & RevertPlanRevision.
	undoPreview, err := plans.PreviewPlanUndo(t.Context(), connect.NewRequest(&ariadnev1.PreviewPlanUndoRequest{
		ProjectId:  projectID,
		RevisionId: lastRevID,
	}))
	if err != nil || undoPreview.Msg.Preview.Blocked != nil || len(undoPreview.Msg.Preview.Changes) == 0 {
		t.Fatalf("PreviewPlanUndo: err=%v preview=%+v", err, undoPreview.Msg.Preview)
	}
	reverted, err := plans.RevertPlanRevision(t.Context(), connect.NewRequest(&ariadnev1.RevertPlanRevisionRequest{
		ProjectId:  projectID,
		RevisionId: lastRevID,
		Reason:     "undo rename",
	}))
	if err != nil {
		t.Fatalf("RevertPlanRevision: %v", err)
	}
	if reverted.Msg.Plan.Nodes[1].Title != "Task B" {
		t.Fatalf("expected node_b title reverted to 'Task B', got %q", reverted.Msg.Plan.Nodes[1].Title)
	}

	// Verify X-Ariadne-Server-Time header on ConnectRPC responses.
	if serverTime := planPage.Header().Get("X-Ariadne-Server-Time"); serverTime == "" {
		t.Fatal("missing X-Ariadne-Server-Time response header")
	}
	if cc := planPage.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", cc)
	}

	// Verify invalid cursor returns InvalidArgument.
	if _, err := plans.ListPlanRevisions(t.Context(), connect.NewRequest(&ariadnev1.ListPlanRevisionsRequest{
		ProjectId: projectID,
		PageToken: "!!!not-base64!!!",
	})); err == nil {
		t.Fatal("expected invalid page token to fail")
	} else {
		cerr, _ := extractLocalizedError(t, err)
		if cerr.Code() != connect.CodeInvalidArgument {
			t.Fatalf("invalid page token code = %v, want InvalidArgument", cerr.Code())
		}
	}

	// Ensure HTTP client helper stays unused-free.
	_ = http.StatusOK
}

func TestProjectServiceBrowseSourceSecretIsolation(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	now := func() time.Time { return time.UnixMilli(1_800_000_000_000) }
	repo, err := core.Open(t.Context(), core.Options{
		Path: ":memory:",
		Now:  now,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	settingsStore, err := settings.Open(filepath.Join(home, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}

	var observedSecrets []string
	registry := sources.New()
	registry.Register(sdk.ContextProvider{
		Info: sdk.ContextProviderInfo{
			Kind:  "remote",
			Title: sdk.LocalizedText{"zh-CN": "远端源", "en": "Remote"},
			Settings: sdk.SettingsSchema{
				Type: "object",
				Properties: map[string]sdk.SettingField{
					"host":  {Type: "string"},
					"token": {Type: "string", Secret: true},
				},
			},
			Provides: []string{"files"},
		},
		Connect: func(context.Context, map[string]any) (sdk.Capabilities, error) {
			return sdk.Capabilities{}, nil
		},
		Browse: func(_ context.Context, values map[string]any, path string) (*sdk.DirectoryListing, error) {
			token, _ := values["token"].(string)
			observedSecrets = append(observedSecrets, token)
			return &sdk.DirectoryListing{Path: path, Directories: []sdk.BrowsableDirectory{}}, nil
		},
	})

	api, err := New(Options{
		Repository: repo,
		Settings:   settingsStore,
		Sources:    registry,
		Token:      "browse-token",
		Now:        now,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()

	srv := httptest.NewServer(api)
	defer srv.Close()

	projects := ariadnev1connect.NewProjectServiceClient(
		srv.Client(),
		srv.URL+rpcPrefix,
		connect.WithInterceptors(bearerClientInterceptor("browse-token")),
	)
	meta := ariadnev1connect.NewMetaServiceClient(
		srv.Client(),
		srv.URL+rpcPrefix,
		connect.WithInterceptors(bearerClientInterceptor("browse-token")),
	)

	name := "Secret Isolation Project"
	primarySettings, err := structpb.NewStruct(map[string]any{
		"host":  "primary.example.test",
		"token": "primary-secret-value",
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := projects.CreateProject(t.Context(), connect.NewRequest(&ariadnev1.CreateProjectRequest{
		Name: &name,
		Source: &ariadnev1.ProjectSource{
			Kind:     "remote",
			Settings: primarySettings,
		},
	}))
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	projectID := created.Msg.Project.Id

	newSourceSettings, err := structpb.NewStruct(map[string]any{
		"host": "new.example.test",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Explicit empty source_id signals a brand new source and must NOT inject primary source secrets.
	emptySourceID := ""
	if _, err := meta.BrowseSource(t.Context(), connect.NewRequest(&ariadnev1.BrowseSourceRequest{
		Kind:      "remote",
		ProjectId: &projectID,
		SourceId:  &emptySourceID,
		Settings:  newSourceSettings,
		Path:      "/",
	})); err != nil {
		t.Fatalf("BrowseSource with empty sourceId: %v", err)
	}

	// Omitted source_id falls back to the primary source and injects its stored secret.
	if _, err := meta.BrowseSource(t.Context(), connect.NewRequest(&ariadnev1.BrowseSourceRequest{
		Kind:      "remote",
		ProjectId: &projectID,
		Settings:  newSourceSettings,
		Path:      "/",
	})); err != nil {
		t.Fatalf("BrowseSource with omitted sourceId: %v", err)
	}

	if len(observedSecrets) != 2 {
		t.Fatalf("expected 2 browse calls, got %d", len(observedSecrets))
	}
	if observedSecrets[0] != "" {
		t.Fatalf("explicit empty source_id leaked primary secret: got %q, want empty", observedSecrets[0])
	}
	if observedSecrets[1] != "primary-secret-value" {
		t.Fatalf("omitted source_id did not reuse primary secret: got %q, want %q", observedSecrets[1], "primary-secret-value")
	}
}

func TestProjectServiceListAllNeedsMeAndGlobalSearch(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	home := t.TempDir()
	nowMs := int64(1_800_000_000_000)
	now := func() time.Time {
		nowMs += 1_000
		return time.UnixMilli(nowMs)
	}
	repo, err := core.Open(ctx, core.Options{Path: ":memory:", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	settingsStore, err := settings.Open(filepath.Join(home, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	api, err := New(Options{
		Repository: repo,
		Settings:   settingsStore,
		Sources:    sources.New(),
		Token:      "cross-token",
		Now:        now,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	srv := httptest.NewServer(api)
	defer srv.Close()

	projects := ariadnev1connect.NewProjectServiceClient(
		srv.Client(),
		srv.URL+rpcPrefix,
		connect.WithInterceptors(bearerClientInterceptor("cross-token")),
	)

	nameA, nameB := "Project Alpha", "Project Beta"
	pA, err := projects.CreateProject(ctx, connect.NewRequest(&ariadnev1.CreateProjectRequest{Name: &nameA}))
	if err != nil {
		t.Fatal(err)
	}
	pB, err := projects.CreateProject(ctx, connect.NewRequest(&ariadnev1.CreateProjectRequest{Name: &nameB}))
	if err != nil {
		t.Fatal(err)
	}
	idA, idB := pA.Msg.Project.Id, pB.Msg.Project.Id

	thrA, err := repo.CreateThread(ctx, idA, core.NewThread{Title: "全局检索测试 Alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ReportThread(ctx, thrA.ID, core.ThreadReport{Kind: "ready", Headline: "Alpha 交付完成", Summary: "详情"}, nil, core.ReviewOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PostMessage(ctx, idA, core.MessageInput{ConversationID: pA.Msg.Project.CoordinatorId, Author: "user", Content: "rpc_search_needle in Alpha"}); err != nil {
		t.Fatal(err)
	}

	thrB, err := repo.CreateThread(ctx, idB, core.NewThread{Title: "全局检索测试 Beta"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.OpenAttention(ctx, idB, core.AttentionInput{
		ThreadID: &thrB.ID,
		Block: map[string]any{
			"type":  "confirm",
			"title": "确认 Beta 发布？",
			"tone":  "warning",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PostMessage(ctx, idB, core.MessageInput{ConversationID: thrB.ID, Author: "assistant", Content: "rpc_search_needle in Beta"}); err != nil {
		t.Fatal(err)
	}

	allNeeds, err := projects.ListAllNeedsMe(ctx, connect.NewRequest(&ariadnev1.ListAllNeedsMeRequest{}))
	if err != nil {
		t.Fatalf("ListAllNeedsMe: %v", err)
	}
	if len(allNeeds.Msg.Items) != 2 {
		t.Fatalf("ListAllNeedsMe items = %d, want 2", len(allNeeds.Msg.Items))
	}
	if len(allNeeds.Msg.Threads) != 2 {
		t.Fatalf("ListAllNeedsMe threads = %d, want 2", len(allNeeds.Msg.Threads))
	}
	// Beta attention is newer than Alpha report.
	if allNeeds.Msg.Items[0].ProjectId != idB || allNeeds.Msg.Items[0].ProjectName != "Project Beta" {
		t.Fatalf("items[0] = %+v, want Beta", allNeeds.Msg.Items[0])
	}
	if allNeeds.Msg.Items[1].ProjectId != idA || allNeeds.Msg.Items[1].ProjectName != "Project Alpha" {
		t.Fatalf("items[1] = %+v, want Alpha", allNeeds.Msg.Items[1])
	}

	// Global search with prefer_project_id = idA prioritizes Alpha ahead of newer Beta.
	searchResp, err := projects.SearchProject(ctx, connect.NewRequest(&ariadnev1.SearchProjectRequest{
		ProjectId:       "",
		Query:           "全局检索测试",
		PreferProjectId: &idA,
	}))
	if err != nil {
		t.Fatalf("SearchProject global threads: %v", err)
	}
	if len(searchResp.Msg.Threads) != 2 {
		t.Fatalf("global thread hits = %d, want 2", len(searchResp.Msg.Threads))
	}
	if searchResp.Msg.Threads[0].Id != thrA.ID || searchResp.Msg.Threads[0].ProjectId != idA || searchResp.Msg.Threads[0].ProjectName != "Project Alpha" || searchResp.Msg.Threads[0].Stage != "review" {
		t.Fatalf("threads[0] = %+v, want Alpha review", searchResp.Msg.Threads[0])
	}
	if searchResp.Msg.Threads[1].Id != thrB.ID || searchResp.Msg.Threads[1].ProjectId != idB || searchResp.Msg.Threads[1].ProjectName != "Project Beta" || searchResp.Msg.Threads[1].Stage != "answer" {
		t.Fatalf("threads[1] = %+v, want Beta answer", searchResp.Msg.Threads[1])
	}

	msgResp, err := projects.SearchProject(ctx, connect.NewRequest(&ariadnev1.SearchProjectRequest{
		ProjectId:       "",
		Query:           "rpc_search_needle",
		PreferProjectId: &idA,
	}))
	if err != nil {
		t.Fatalf("SearchProject global messages: %v", err)
	}
	if len(msgResp.Msg.Messages) != 2 {
		t.Fatalf("global message hits = %d, want 2", len(msgResp.Msg.Messages))
	}
	if msgResp.Msg.Messages[0].ProjectId != idA || msgResp.Msg.Messages[0].ProjectName != "Project Alpha" {
		t.Fatalf("messages[0] = %+v, want Alpha", msgResp.Msg.Messages[0])
	}
	if msgResp.Msg.Messages[1].ProjectId != idB || msgResp.Msg.Messages[1].ProjectName != "Project Beta" || msgResp.Msg.Messages[1].Where == nil || *msgResp.Msg.Messages[1].Where != "全局检索测试 Beta" {
		t.Fatalf("messages[1] = %+v, want Beta thread message", msgResp.Msg.Messages[1])
	}
}

func TestProjectSettingsModelsRPCAndConversationModel(t *testing.T) {
	ctx := t.Context()
	home := t.TempDir()
	repo, err := core.Open(ctx, core.Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	settingsStore, err := settings.Open(filepath.Join(home, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	kinds := []sdk.ModelProviderInfo{{Kind: "anthropic", Title: sdk.LocalizedText{"en": "Anthropic"}}}
	if _, err := settingsStore.UpdateModels(settings.Object{
		"providers": []any{
			settings.Object{"id": "p1", "kind": "anthropic", "name": "Claude", "settings": settings.Object{}},
		},
		"models": []any{
			settings.Object{"id": "m-opus", "provider": "p1", "model": "claude-opus", "contextTokens": float64(200000), "efforts": []any{"low", "medium", "high", "max"}},
			settings.Object{"id": "m-sonnet", "provider": "p1", "model": "claude-sonnet", "contextTokens": float64(128000), "efforts": []any{"low", "medium", "high"}},
			settings.Object{"id": "m-haiku", "provider": "p1", "model": "claude-haiku", "contextTokens": float64(100000), "efforts": []any{"low", "medium"}},
		},
		"roles": settings.Object{
			"thread":      settings.Object{"model": "m-sonnet", "effort": "medium"},
			"coordinator": settings.Object{"model": nil, "effort": nil},
			"research":    settings.Object{"model": "m-haiku", "effort": "low"},
		},
	}, kinds); err != nil {
		t.Fatal(err)
	}

	api, err := New(Options{
		Repository: repo,
		Settings:   settingsStore,
		Token:      "test-token",
		Version:    "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(api)
	defer func() {
		ts.Close()
		api.Close()
	}()

	clientOpts := []connect.ClientOption{
		connect.WithInterceptors(bearerClientInterceptor("test-token")),
	}
	baseURL := ts.URL + rpcPrefix
	projects := ariadnev1connect.NewProjectServiceClient(ts.Client(), baseURL, clientOpts...)
	conversations := ariadnev1connect.NewConversationServiceClient(ts.Client(), baseURL, clientOpts...)

	name := "Model Override Project"
	created, err := projects.CreateProject(ctx, connect.NewRequest(&ariadnev1.CreateProjectRequest{Name: &name}))
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	projectID := created.Msg.Project.Id
	coordinatorID := created.Msg.Project.CoordinatorId
	thread, err := repo.CreateThread(ctx, projectID, core.NewThread{Title: "Thread A"})
	if err != nil {
		t.Fatal(err)
	}

	strPtr := func(v string) *string { return &v }

	// Unknown model ID is rejected.
	if _, err := projects.UpdateProject(ctx, connect.NewRequest(&ariadnev1.UpdateProjectRequest{
		ProjectId: projectID,
		Settings: &ariadnev1.ProjectSettings{
			Models: &ariadnev1.ProjectModelSettings{
				Thread: &ariadnev1.ProjectModelRoleSettings{Model: strPtr("m-missing"), Effort: strPtr("high")},
			},
		},
	})); err == nil {
		t.Fatal("expected unknown model ID in project settings to be rejected")
	}

	// Valid overrides round-trip via UpdateProject and GetProjectSettings.
	updated, err := projects.UpdateProject(ctx, connect.NewRequest(&ariadnev1.UpdateProjectRequest{
		ProjectId: projectID,
		Settings: &ariadnev1.ProjectSettings{
			Models: &ariadnev1.ProjectModelSettings{
				Thread:   &ariadnev1.ProjectModelRoleSettings{Model: strPtr("m-opus"), Effort: strPtr("max")},
				Research: &ariadnev1.ProjectModelRoleSettings{Effort: strPtr("low")},
			},
		},
	}))
	if err != nil {
		t.Fatalf("UpdateProject with models: %v", err)
	}
	gotModels := updated.Msg.SettingsView.Settings.Models
	if gotModels == nil || gotModels.Thread == nil || gotModels.Thread.GetModel() != "m-opus" || gotModels.Thread.GetEffort() != "max" {
		t.Fatalf("unexpected thread override in response: %+v", gotModels)
	}
	if gotModels.Coordinator != nil {
		t.Fatalf("expected coordinator override to remain nil, got %+v", gotModels.Coordinator)
	}
	if gotModels.Research == nil || gotModels.Research.Model != nil || gotModels.Research.GetEffort() != "low" {
		t.Fatalf("unexpected research override in response: %+v", gotModels.Research)
	}

	// GetConversation reflects project overrides for both thread and coordinator (inheriting project thread with low effort).
	threadConv, err := conversations.GetConversation(ctx, connect.NewRequest(&ariadnev1.GetConversationRequest{
		ConversationId: thread.ID,
	}))
	if err != nil {
		t.Fatalf("GetConversation(thread): %v", err)
	}
	if threadConv.Msg.Model == nil || threadConv.Msg.Model.Id != "m-opus" || threadConv.Msg.Model.GetEffort() != "max" {
		t.Fatalf("thread conversation model = %+v, want m-opus/max", threadConv.Msg.Model)
	}
	coordConv, err := conversations.GetConversation(ctx, connect.NewRequest(&ariadnev1.GetConversationRequest{
		ConversationId: coordinatorID,
	}))
	if err != nil {
		t.Fatalf("GetConversation(coordinator): %v", err)
	}
	if coordConv.Msg.Model == nil || coordConv.Msg.Model.Id != "m-opus" || coordConv.Msg.Model.GetEffort() != "low" {
		t.Fatalf("coordinator conversation model = %+v, want m-opus/low", coordConv.Msg.Model)
	}

	// Clearing models reverts GetConversation to global settings.
	cleared, err := projects.UpdateProject(ctx, connect.NewRequest(&ariadnev1.UpdateProjectRequest{
		ProjectId: projectID,
		Settings:  &ariadnev1.ProjectSettings{},
	}))
	if err != nil {
		t.Fatalf("UpdateProject clear models: %v", err)
	}
	if cleared.Msg.SettingsView.Settings.Models != nil {
		t.Fatalf("expected cleared models to be nil, got %+v", cleared.Msg.SettingsView.Settings.Models)
	}
	threadConvAfter, err := conversations.GetConversation(ctx, connect.NewRequest(&ariadnev1.GetConversationRequest{
		ConversationId: thread.ID,
	}))
	if err != nil {
		t.Fatalf("GetConversation(thread after clear): %v", err)
	}
	if threadConvAfter.Msg.Model == nil || threadConvAfter.Msg.Model.Id != "m-sonnet" || threadConvAfter.Msg.Model.GetEffort() != "medium" {
		t.Fatalf("thread conversation model after clear = %+v, want m-sonnet/medium", threadConvAfter.Msg.Model)
	}
}
