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
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/dashboard"
	"github.com/daidr/Ariadne/sdk"
)

// A Go source returns data sets as a map. Compare dashboard JSON semantically;
// object member order in its stored JSON string is not part of the wire contract.
func dashboardJSON(value any) any {
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			if key == "content" {
				if text, ok := child.(string); ok {
					var content map[string]any
					if json.Unmarshal([]byte(text), &content) == nil && content["dashboard"] != nil {
						value[key] = content
						continue
					}
				}
			}
			value[key] = dashboardJSON(child)
		}
	case []any:
		for i, child := range value {
			value[i] = dashboardJSON(child)
		}
	}
	return value
}
func TestDashboardRefreshHTTPMatchesLegacy(t *testing.T) {
	content := func(source any) string {
		value := object{"version": 1, "dashboard": object{"version": 1, "widgets": []any{}}, "data": object{"keep": []any{1}}}
		if source != nil {
			value["source"] = source
		}
		return encoded(value)
	}
	requests := []apiRequest{}
	add := func(name, kind, body string) {
		requests = append(requests, apiRequest{Method: "POST", Path: "/api/projects/prj_a/artifacts", Save: name, Body: object{"kind": kind, "title": "Dashboard", "content": body}}, apiRequest{Method: "POST", Path: "/api/artifacts/@" + name + "@/refresh"})
	}
	add("live", "dashboard", content(object{"kind": "fixture", "settings": object{"query": "open"}}))
	requests = append(requests, apiRequest{Method: "GET", Path: "/api/artifacts/@live@"})
	add("static", "dashboard", content(nil))
	add("unknown", "dashboard", content(object{"kind": "missing", "settings": object{}}))
	add("bad", "dashboard", content(object{"kind": "fixture", "settings": object{"query": 42}}))
	add("fail", "dashboard", content(object{"kind": "fixture", "settings": object{"query": "fail"}}))
	add("document", "document", "# Text")
	requests = append(requests, apiRequest{Method: "POST", Path: "/api/artifacts/missing/refresh"})
	var expected []any
	if bun, err := exec.LookPath("bun"); err == nil {
		cmd := legacytest.Command(t, bun, "packages/server/src/testing/go-api-fixture.ts")
		cmd.Dir = filepath.Join("..", "..")
		cmd.Stdin = strings.NewReader(encoded(object{"seed": traceSeed()[:1], "sql": "SELECT 1", "dashboards": true, "requests": requests}))
		output, err := cmd.CombinedOutput()
		if err != nil || json.Unmarshal(output, &expected) != nil {
			t.Fatal(err, string(output))
		}
	} else if os.Getenv("ARIADNE_REQUIRE_LEGACY") == "1" {
		t.Fatal(err)
	}
	dir := t.TempDir()
	now := func() time.Time { return time.UnixMilli(1800000000000) }
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(dir, "dashboard.sqlite"), Now: now})
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
	aliases := map[string]string{}
	for i, item := range requests {
		path := item.Path
		for key, value := range aliases {
			path = strings.ReplaceAll(path, "@"+key+"@", value)
		}
		request := httptest.NewRequest(item.Method, path, strings.NewReader(encoded(item.Body)))
		request.Header.Set("Authorization", "Bearer test-token")
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		var body any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err, response.Body.String())
		}
		if item.Save != "" {
			aliases[item.Save] = body.(map[string]any)["artifact"].(map[string]any)["id"].(string)
		}
		actual := object{"status": response.Code, "body": body}
		if expected != nil && encoded(canonical(dashboardJSON(actual))) != encoded(canonical(dashboardJSON(expected[i]))) {
			t.Fatalf("%s\nGo %s\nTS %s", path, encoded(actual), encoded(expected[i]))
		}
		if response.Code >= 500 {
			t.Fatal(response.Body.String())
		}
	}
}
