package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
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

func planIssueSeed() []eventstore.NewEvent {
	return append(workspaceHTTPSeed(), eventstore.NewEvent{Type: "plan.revised", ProjectID: "prj_a", Data: json.RawMessage(encoded(object{"revisionId": "rev_a", "author": "user", "reason": "plan", "ops": []any{object{"op": "add", "id": "task", "kind": "task", "title": "work", "description": "", "acceptance": ""}}}))})
}

func TestPlanIssueValidationConnect(t *testing.T) {
	now := func() time.Time { return time.UnixMilli(1_800_000_000_000) }
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(t.TempDir(), "plan.sqlite"), Now: now})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if _, err := repo.Store.Append(t.Context(), planIssueSeed()); err != nil {
		t.Fatal(err)
	}
	api, err := New(Options{Repository: repo, Token: "test-token", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	srv := httptest.NewServer(api)
	defer srv.Close()

	unauth := ariadnev1connect.NewPlanServiceClient(srv.Client(), srv.URL+rpcPrefix)
	if _, err := unauth.LinkPlanNodeIssue(t.Context(), connect.NewRequest(&ariadnev1.LinkPlanNodeIssueRequest{
		ProjectId: "prj_a", NodeId: "task", Number: 1,
	})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("unauthenticated LinkPlanNodeIssue code = %v, want CodeUnauthenticated", connect.CodeOf(err))
	}

	client := ariadnev1connect.NewPlanServiceClient(
		srv.Client(),
		srv.URL+rpcPrefix,
		connect.WithInterceptors(bearerClientInterceptor("test-token")),
	)
	for _, tc := range []struct {
		nodeID string
		number int64
	}{
		{"task", 0},
		{"task", -1},
		{"missing", 1},
		{"task", 1}, // no tracker configured
	} {
		_, err := client.LinkPlanNodeIssue(t.Context(), connect.NewRequest(&ariadnev1.LinkPlanNodeIssueRequest{
			ProjectId: "prj_a", NodeId: tc.nodeID, Number: tc.number,
		}))
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("LinkPlanNodeIssue(%s, %d) code = %v (err=%v), want CodeInvalidArgument", tc.nodeID, tc.number, connect.CodeOf(err), err)
		}
	}
	for _, nodeID := range []string{"task", "missing"} {
		if _, err := client.UnlinkPlanNodeIssue(t.Context(), connect.NewRequest(&ariadnev1.UnlinkPlanNodeIssueRequest{
			ProjectId: "prj_a", NodeId: nodeID, Number: 1,
		})); err != nil {
			t.Fatalf("UnlinkPlanNodeIssue(%s) failed: %v", nodeID, err)
		}
	}
}

func TestPlanIssueFetchOnlyAfterAuthenticationAndNodeValidation(t *testing.T) {
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(t.TempDir(), "issues.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if _, err := repo.Store.Append(t.Context(), planIssueSeed()); err != nil {
		t.Fatal(err)
	}
	var reads atomic.Int32
	registry := sources.New()
	registry.Register(sdk.ContextProvider{Info: sdk.ContextProviderInfo{Kind: "fixture", Provides: []string{"tracker"}}, Connect: func(context.Context, map[string]any) (sdk.Capabilities, error) {
		return sdk.Capabilities{Tracker: &sdk.TrackerCapability{Name: "fixture", Issue: func(ctx context.Context, number int) (sdk.IssueDetail, error) {
			reads.Add(1)
			if number == 404 {
				return sdk.IssueDetail{}, errors.New("missing issue")
			}
			return sdk.IssueDetail{IssueSummary: sdk.IssueSummary{Number: number, Title: "from tracker", URL: "https://example.test/issues/1"}}, nil
		}}}, nil
	}})
	manager, err := workspaces.New(workspaces.Options{Repository: repo, Sources: registry})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	api, err := New(Options{Repository: repo, Token: "secret", Workspaces: manager})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	srv := httptest.NewServer(api)
	defer srv.Close()

	unauth := ariadnev1connect.NewPlanServiceClient(srv.Client(), srv.URL+rpcPrefix)
	client := ariadnev1connect.NewPlanServiceClient(
		srv.Client(),
		srv.URL+rpcPrefix,
		connect.WithInterceptors(bearerClientInterceptor("secret")),
	)

	if _, err := unauth.LinkPlanNodeIssue(t.Context(), connect.NewRequest(&ariadnev1.LinkPlanNodeIssueRequest{
		ProjectId: "prj_a", NodeId: "task", Number: 1,
	})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("expected CodeUnauthenticated, got %v", err)
	}
	if _, err := client.LinkPlanNodeIssue(t.Context(), connect.NewRequest(&ariadnev1.LinkPlanNodeIssueRequest{
		ProjectId: "prj_a", NodeId: "missing", Number: 1,
	})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected CodeInvalidArgument for missing node, got %v", err)
	}
	if _, err := client.LinkPlanNodeIssue(t.Context(), connect.NewRequest(&ariadnev1.LinkPlanNodeIssueRequest{
		ProjectId: "prj_a", NodeId: "task", Number: -1,
	})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected CodeInvalidArgument for negative issue number, got %v", err)
	}
	if reads.Load() != 0 {
		t.Fatal("invalid request reached tracker")
	}

	if _, err := client.LinkPlanNodeIssue(t.Context(), connect.NewRequest(&ariadnev1.LinkPlanNodeIssueRequest{
		ProjectId: "prj_a", NodeId: "task", Number: 1,
	})); err != nil {
		t.Fatalf("LinkPlanNodeIssue(1) failed: %v", err)
	}
	issues, err := repo.PlanIssues(t.Context(), "prj_a", "task")
	if err != nil || len(issues) != 1 || issues[0].Title != "from tracker" {
		t.Fatal("issue metadata not saved", issues, err)
	}
	if _, err := client.UnlinkPlanNodeIssue(t.Context(), connect.NewRequest(&ariadnev1.UnlinkPlanNodeIssueRequest{
		ProjectId: "prj_a", NodeId: "task", Number: 1,
	})); err != nil {
		t.Fatalf("UnlinkPlanNodeIssue(1) failed: %v", err)
	}
	issues, _ = repo.PlanIssues(t.Context(), "prj_a", "task")
	if len(issues) != 0 {
		t.Fatal("issue link retained")
	}
	if _, err := client.LinkPlanNodeIssue(t.Context(), connect.NewRequest(&ariadnev1.LinkPlanNodeIssueRequest{
		ProjectId: "prj_a", NodeId: "task", Number: 404,
	})); err == nil {
		t.Fatal("expected tracker error for 404 issue")
	} else {
		connectErr, loc := extractLocalizedError(t, err)
		if connectErr.Code() != connect.CodeInvalidArgument || loc == nil || loc.GetKey() != "error.issue" {
			t.Fatalf("tracker error hidden: code=%v loc=%+v", connectErr.Code(), loc)
		}
	}
}
