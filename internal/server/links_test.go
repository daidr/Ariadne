package server

import (
	"context"
	"encoding/json"
	"github.com/daidr/Ariadne/internal/legacytest"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/links"
	"github.com/daidr/Ariadne/sdk"
)

func TestLinkHTTPMatchesLegacyAndAuthenticatesBeforeCallbacks(t *testing.T) {
	no := false
	over := []string{}
	for i := 0; i < 201; i++ {
		over = append(over, "https://x.test/pull/1")
	}
	requests := []apiRequest{{Method: "POST", Path: "/api/links", Body: object{"urls": []string{"https://x.test/pull/1", "https://other.test/", "http://x.test/pull/1"}}}, {Method: "GET", Path: "/api/links/preview?url=" + url.QueryEscape("https://x.test/pull/1")}, {Method: "GET", Path: "/api/links/preview?url=" + url.QueryEscape("https://other.test/")}, {Method: "POST", Path: "/api/links", Body: object{"urls": []string{}}}, {Method: "POST", Path: "/api/links", Body: object{"urls": []any{1}}}, {Method: "POST", Path: "/api/links", Body: object{"urls": nil}}, {Method: "POST", Path: "/api/links", Body: object{"urls": over}}, {Method: "GET", Path: "/api/links/preview"}, {Method: "POST", Path: "/api/links", Body: object{"urls": []string{"https://x.test/pull/1"}}, Auth: &no}, {Method: "GET", Path: "/api/links/preview?url=https://x.test/pull/1", Auth: &no}}
	var expected []any
	if bun, err := exec.LookPath("bun"); err == nil {
		cmd := legacytest.Command(t, bun, "packages/server/src/testing/go-api-fixture.ts")
		cmd.Dir = filepath.Join("..", "..")
		cmd.Stdin = strings.NewReader(encoded(object{"links": true, "seed": []any{}, "sql": "SELECT 1", "requests": requests}))
		data, err := cmd.CombinedOutput()
		if err != nil || json.Unmarshal(data, &expected) != nil {
			t.Fatalf("legacy %v\n%s", err, data)
		}
	} else if os.Getenv("ARIADNE_REQUIRE_LEGACY") == "1" {
		t.Fatal(err)
	}
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(t.TempDir(), "data.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	resolver := links.New(links.Options{})
	defer resolver.Close()
	var matches, previews int
	resolver.Register(sdk.LinkProvider{Kind: "fixture", Match: func(address *url.URL) *sdk.LinkMatch {
		matches++
		if address.Hostname() == "x.test" && address.Path == "/pull/1" {
			return &sdk.LinkMatch{Kind: "pull-request", Label: "acme/app#1"}
		}
		return nil
	}, Preview: func(context.Context, *url.URL) (*sdk.PullRequestPreview, error) {
		previews++
		add, del, files := 4, 1, 2
		return &sdk.PullRequestPreview{Kind: "pull-request", Repository: "acme/app", Number: 1, URL: "https://x.test/pull/1", Title: "标题", State: "open", Draft: true, Additions: &add, Deletions: &del, ChangedFiles: &files, At: "2026-11-01"}, nil
	}})
	api, err := New(Options{Repository: repo, Token: "test-token", Links: resolver})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	for i, item := range requests {
		beforeMatches, beforePreviews := matches, previews
		request := httptest.NewRequest(item.Method, item.Path, strings.NewReader(encoded(item.Body)))
		if item.Auth == nil || *item.Auth {
			request.Header.Set("Authorization", "Bearer test-token")
		}
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		var body any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		actual := object{"status": response.Code, "body": body}
		if expected != nil && encoded(actual) != encoded(expected[i]) {
			t.Fatalf("request %s differs\nGo %s\nTS %s", item.Path, encoded(actual), encoded(expected[i]))
		}
		if item.Auth != nil && !*item.Auth && (beforeMatches != matches || beforePreviews != previews) {
			t.Fatal("unauthenticated request reached link provider")
		}
	}
	if previews != 1 {
		t.Fatal("unexpected remote reads", previews)
	}
}
