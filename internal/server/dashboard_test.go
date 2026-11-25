package server

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/dashboard"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
	"github.com/daidr/Ariadne/sdk"
)

func TestDashboardRefreshConnect(t *testing.T) {
	t.Parallel()
	content := func(source any) string {
		value := object{"version": 1, "dashboard": object{"version": 1, "widgets": []any{}}, "data": object{"keep": []any{1}}}
		if source != nil {
			value["source"] = source
		}
		return encoded(value)
	}
	now := func() time.Time { return time.UnixMilli(1800000000000) }
	repo, err := core.Open(t.Context(), core.Options{Path: ":memory:", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if _, err := repo.Store.Append(t.Context(), traceSeed()[:1]); err != nil {
		t.Fatal(err)
	}
	registry := dashboard.New()
	defer registry.Close(t.Context())
	_, err = registry.Register(sdk.DashboardSource{Info: sdk.DashboardSourceInfo{Kind: "fixture", Settings: sdk.SettingsSchema{Type: "object", Properties: map[string]sdk.SettingField{"query": {Type: "string"}}, Required: []string{"query"}}}, Fetch: func(_ context.Context, settings map[string]any, project string) (map[string]any, error) {
		if project != "prj_a" {
			t.Error("wrong project context", project)
		}
		if settings["query"] == "fail" {
			return nil, errors.New("source failed")
		}
		return map[string]any{"rows": []any{map[string]any{"value": 5}}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	api, err := New(Options{Repository: repo, Token: "test-token", Now: now, Dashboards: registry})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	srv := httptest.NewServer(api)
	defer srv.Close()
	client := ariadnev1connect.NewArtifactServiceClient(
		srv.Client(),
		srv.URL+rpcPrefix,
		connect.WithInterceptors(bearerClientInterceptor("test-token")),
	)

	makeArtifact := func(kind, body string) string {
		t.Helper()
		art, err := repo.CreateArtifact(t.Context(), "prj_a", core.NewArtifact{
			Kind:    kind,
			Title:   "Dashboard",
			Author:  "user",
			Content: body,
		})
		if err != nil {
			t.Fatal(err)
		}
		return art.ID
	}

	liveID := makeArtifact("dashboard", content(object{"kind": "fixture", "settings": object{"query": "open"}}))
	refreshed, err := client.RefreshDashboard(t.Context(), connect.NewRequest(&ariadnev1.RefreshDashboardRequest{ArtifactId: liveID}))
	if err != nil {
		t.Fatalf("live RefreshDashboard failed: %v", err)
	}
	if !strings.Contains(refreshed.Msg.GetArtifact().GetContent(), `"value":5`) {
		t.Fatalf("refreshed content missing fetched rows: %s", refreshed.Msg.GetArtifact().GetContent())
	}
	got, err := client.GetArtifact(t.Context(), connect.NewRequest(&ariadnev1.GetArtifactRequest{ArtifactId: liveID}))
	if err != nil || !strings.Contains(got.Msg.GetArtifact().GetContent(), `"value":5`) {
		t.Fatalf("GetArtifact after refresh failed: %v %+v", err, got)
	}

	for _, tc := range []struct {
		name     string
		id       string
		wantCode connect.Code
	}{
		{"static", makeArtifact("dashboard", content(nil)), connect.CodeInvalidArgument},
		{"unknown", makeArtifact("dashboard", content(object{"kind": "missing", "settings": object{}})), connect.CodeInvalidArgument},
		{"bad", makeArtifact("dashboard", content(object{"kind": "fixture", "settings": object{"query": 42}})), connect.CodeInvalidArgument},
		{"fail", makeArtifact("dashboard", content(object{"kind": "fixture", "settings": object{"query": "fail"}})), connect.CodeInvalidArgument},
		{"document", makeArtifact("document", "# Text"), connect.CodeInvalidArgument},
		{"missing", "missing", connect.CodeNotFound},
	} {
		_, err := client.RefreshDashboard(t.Context(), connect.NewRequest(&ariadnev1.RefreshDashboardRequest{ArtifactId: tc.id}))
		if connect.CodeOf(err) != tc.wantCode {
			t.Fatalf("%s: RefreshDashboard code = %v (err=%v), want %v", tc.name, connect.CodeOf(err), err, tc.wantCode)
		}
	}
}
