package server

import (
	"context"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"

	"github.com/daidr/Ariadne/internal/core"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
	"github.com/daidr/Ariadne/internal/links"
	"github.com/daidr/Ariadne/sdk"
)

func TestLinkRPCAuthenticatesBeforeCallbacks(t *testing.T) {
	over := make([]string, 201)
	for i := range over {
		over[i] = "https://x.test/pull/1"
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
		return &sdk.PullRequestPreview{
			Kind: "pull-request", Repository: "acme/app", Number: 1, URL: "https://x.test/pull/1",
			Title: "标题", State: "open", Draft: true, Additions: &add, Deletions: &del, ChangedFiles: &files, At: "2026-11-01",
		}, nil
	}})
	api, err := New(Options{Repository: repo, Token: "test-token", Links: resolver})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	server := httptest.NewServer(api)
	defer server.Close()

	authClient := ariadnev1connect.NewMetaServiceClient(
		server.Client(),
		server.URL+rpcPrefix,
		connect.WithInterceptors(bearerClientInterceptor("test-token")),
	)
	unauthClient := ariadnev1connect.NewMetaServiceClient(server.Client(), server.URL+rpcPrefix)

	sumRes, err := authClient.SummarizeLinks(t.Context(), connect.NewRequest(&ariadnev1.SummarizeLinksRequest{
		Urls: []string{"https://x.test/pull/1", "https://other.test/", "http://x.test/pull/1"},
	}))
	if err != nil {
		t.Fatalf("SummarizeLinks: %v", err)
	}
	if sumRes.Msg.GetLinks()["https://x.test/pull/1"].GetSummary().GetLabel() != "acme/app#1" {
		t.Fatalf("unexpected summary map: %+v", sumRes.Msg.GetLinks())
	}
	if sumRes.Msg.GetLinks()["https://other.test/"].GetSummary() != nil {
		t.Fatalf("expected unmatched link to have nil summary")
	}

	previewRes, err := authClient.PreviewLink(t.Context(), connect.NewRequest(&ariadnev1.PreviewLinkRequest{
		Url: "https://x.test/pull/1",
	}))
	if err != nil || previewRes.Msg.GetPreview().GetTitle() != "标题" || previewRes.Msg.GetPreview().GetAdditions() != 4 {
		t.Fatalf("PreviewLink: %+v %v", previewRes, err)
	}
	otherRes, err := authClient.PreviewLink(t.Context(), connect.NewRequest(&ariadnev1.PreviewLinkRequest{
		Url: "https://other.test/",
	}))
	if err != nil || otherRes.Msg.GetPreview() != nil {
		t.Fatalf("expected nil preview for unknown URL: %+v %v", otherRes, err)
	}
	if _, err := authClient.SummarizeLinks(t.Context(), connect.NewRequest(&ariadnev1.SummarizeLinksRequest{})); err != nil {
		t.Fatalf("empty SummarizeLinks failed: %v", err)
	}
	if _, err := authClient.SummarizeLinks(t.Context(), connect.NewRequest(&ariadnev1.SummarizeLinksRequest{Urls: over})); err == nil {
		t.Fatal("expected >200 URLs to fail")
	} else if connectErr, _ := extractLocalizedError(t, err); connectErr.Code() != connect.CodeInvalidArgument {
		t.Fatalf("expected CodeInvalidArgument, got %v", connectErr.Code())
	}
	if _, err := authClient.PreviewLink(t.Context(), connect.NewRequest(&ariadnev1.PreviewLinkRequest{})); err == nil {
		t.Fatal("expected empty PreviewLink to fail")
	} else if connectErr, _ := extractLocalizedError(t, err); connectErr.Code() != connect.CodeInvalidArgument {
		t.Fatalf("expected CodeInvalidArgument, got %v", connectErr.Code())
	}

	beforeMatches, beforePreviews := matches, previews
	if _, err := unauthClient.SummarizeLinks(t.Context(), connect.NewRequest(&ariadnev1.SummarizeLinksRequest{Urls: []string{"https://x.test/pull/1"}})); err == nil {
		t.Fatal("expected unauth SummarizeLinks to fail")
	}
	if _, err := unauthClient.PreviewLink(t.Context(), connect.NewRequest(&ariadnev1.PreviewLinkRequest{Url: "https://x.test/pull/1"})); err == nil {
		t.Fatal("expected unauth PreviewLink to fail")
	}
	if beforeMatches != matches || beforePreviews != previews {
		t.Fatal("unauthenticated request reached link provider")
	}
	if previews != 1 {
		t.Fatal("unexpected remote reads", previews)
	}
}
