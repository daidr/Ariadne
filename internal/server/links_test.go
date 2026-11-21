package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/links"
	"github.com/daidr/Ariadne/sdk"
)

func TestLinkHTTPAuthenticatesBeforeCallbacks(t *testing.T) {
	no := false
	over := []string{}
	for i := 0; i < 201; i++ {
		over = append(over, "https://x.test/pull/1")
	}
	requests := []apiRequest{{Method: "POST", Path: "/api/links", Body: object{"urls": []string{"https://x.test/pull/1", "https://other.test/", "http://x.test/pull/1"}}}, {Method: "GET", Path: "/api/links/preview?url=" + url.QueryEscape("https://x.test/pull/1")}, {Method: "GET", Path: "/api/links/preview?url=" + url.QueryEscape("https://other.test/")}, {Method: "POST", Path: "/api/links", Body: object{"urls": []string{}}}, {Method: "POST", Path: "/api/links", Body: object{"urls": []any{1}}}, {Method: "POST", Path: "/api/links", Body: object{"urls": nil}}, {Method: "POST", Path: "/api/links", Body: object{"urls": over}}, {Method: "GET", Path: "/api/links/preview"}, {Method: "POST", Path: "/api/links", Body: object{"urls": []string{"https://x.test/pull/1"}}, Auth: &no}, {Method: "GET", Path: "/api/links/preview?url=https://x.test/pull/1", Auth: &no}}
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
	for _, item := range requests {
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
		if response.Code >= 500 {
			t.Fatalf("request %s failed with %d: %s", item.Path, response.Code, response.Body.String())
		}
		if item.Auth != nil && !*item.Auth && (beforeMatches != matches || beforePreviews != previews) {
			t.Fatal("unauthenticated request reached link provider")
		}
	}
	if previews != 1 {
		t.Fatal("unexpected remote reads", previews)
	}
}
