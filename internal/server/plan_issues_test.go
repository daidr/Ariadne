package server

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/daidr/Ariadne/internal/legacytest"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/eventstore"
	"github.com/daidr/Ariadne/internal/sources"
	"github.com/daidr/Ariadne/internal/workspaces"
	"github.com/daidr/Ariadne/sdk"
)

func planIssueSeed() []eventstore.NewEvent {
	return append(workspaceHTTPSeed(), eventstore.NewEvent{Type: "plan.revised", ProjectID: "prj_a", Data: json.RawMessage(encoded(object{"revisionId": "rev_a", "author": "user", "reason": "plan", "ops": []any{object{"op": "add", "id": "task", "kind": "task", "title": "work", "description": "", "acceptance": ""}}}))})
}
func TestPlanIssueValidationHTTPMatchesLegacy(t *testing.T) {
	no := false
	requests := []apiRequest{{Method: "POST", Path: "/api/projects/prj_a/plan/nodes/task/issues", Body: object{"number": 1}, Auth: &no}, {Method: "POST", Path: "/api/projects/prj_a/plan/nodes/task/issues", Body: object{"number": 0}}, {Method: "POST", Path: "/api/projects/prj_a/plan/nodes/task/issues", Body: object{"number": 1.5}}, {Method: "POST", Path: "/api/projects/prj_a/plan/nodes/missing/issues", Body: object{"number": 1}}, {Method: "POST", Path: "/api/projects/prj_a/plan/nodes/task/issues", Body: object{"number": 1}}, {Method: "DELETE", Path: "/api/projects/prj_a/plan/nodes/task/issues/1"}, {Method: "DELETE", Path: "/api/projects/prj_a/plan/nodes/missing/issues/1"}}
	var expected []any
	if bun, err := exec.LookPath("bun"); err == nil {
		cmd := legacytest.Command(t, bun, "packages/server/src/testing/go-api-fixture.ts")
		cmd.Dir = filepath.Join("..", "..")
		cmd.Stdin = strings.NewReader(encoded(object{"seed": planIssueSeed(), "sql": "SELECT 1", "requests": requests}))
		data, err := cmd.CombinedOutput()
		if err != nil || json.Unmarshal(data, &expected) != nil {
			t.Fatalf("legacy %v %s", err, data)
		}
	} else if os.Getenv("ARIADNE_REQUIRE_LEGACY") == "1" {
		t.Fatal(err)
	}
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
	for i, item := range requests {
		request := httptest.NewRequest(item.Method, item.Path, strings.NewReader(encoded(item.Body)))
		if item.Auth == nil || *item.Auth {
			request.Header.Set("Authorization", "Bearer test-token")
		}
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		var body any
		json.Unmarshal(response.Body.Bytes(), &body)
		actual := object{"status": response.Code, "body": body}
		if expected != nil && encoded(canonical(actual)) != encoded(canonical(expected[i])) {
			t.Fatalf("plan issue differs\nGo %s\nTS %s", encoded(actual), encoded(expected[i]))
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
	invoke := func(method, path string, body any, auth bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(encoded(body)))
		if auth {
			r.Header.Set("Authorization", "Bearer secret")
		}
		response := httptest.NewRecorder()
		api.ServeHTTP(response, r)
		return response
	}
	for _, item := range []struct {
		path   string
		body   any
		auth   bool
		status int
	}{{"task", object{"number": 1}, false, 401}, {"missing", object{"number": 1}, true, 400}, {"task", object{"number": -1}, true, 400}} {
		response := invoke("POST", "/api/projects/prj_a/plan/nodes/"+item.path+"/issues", item.body, item.auth)
		if response.Code != item.status {
			t.Fatal(response.Code, response.Body.String())
		}
	}
	if reads.Load() != 0 {
		t.Fatal("invalid request reached tracker")
	}
	if response := invoke("POST", "/api/projects/prj_a/plan/nodes/task/issues", object{"number": 1}, true); response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	issues, err := repo.PlanIssues(t.Context(), "prj_a", "task")
	if err != nil || len(issues) != 1 || issues[0].Title != "from tracker" {
		t.Fatal("issue metadata not saved", issues, err)
	}
	if response := invoke("DELETE", "/api/projects/prj_a/plan/nodes/task/issues/01", nil, true); response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	issues, _ = repo.PlanIssues(t.Context(), "prj_a", "task")
	if len(issues) != 1 {
		t.Fatal("noncanonical issue number removed link")
	}
	if response := invoke("DELETE", "/api/projects/prj_a/plan/nodes/task/issues/1", nil, true); response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	issues, _ = repo.PlanIssues(t.Context(), "prj_a", "task")
	if len(issues) != 0 {
		t.Fatal("issue link retained")
	}
	if response := invoke("POST", "/api/projects/prj_a/plan/nodes/task/issues", object{"number": 404}, true); response.Code != 400 || !strings.Contains(response.Body.String(), "error.issue") {
		t.Fatal("tracker error hidden", response.Body.String())
	}
}
