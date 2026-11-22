package server

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"

	"github.com/daidr/Ariadne/internal/core"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
	"github.com/daidr/Ariadne/internal/local"
	"github.com/daidr/Ariadne/internal/sources"
)

func TestDirectoryPickerConnect(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a10", "a2", "A1", ".hidden", "notes"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "file"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(t.TempDir(), "data.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	registry := sources.New()
	_, err = registry.Register(local.DirectoryProvider(t.TempDir(), nil, true))
	if err != nil {
		t.Fatal(err)
	}
	api, err := New(Options{Repository: repo, Token: "test-token", Sources: registry})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	server := httptest.NewServer(api)
	defer server.Close()
	client := ariadnev1connect.NewMetaServiceClient(
		server.Client(),
		server.URL+rpcPrefix,
		connect.WithInterceptors(bearerClientInterceptor("test-token")),
	)

	kindsRes, err := client.ListSourceKinds(t.Context(), connect.NewRequest(&ariadnev1.ListSourceKindsRequest{}))
	if err != nil {
		t.Fatalf("ListSourceKinds: %v", err)
	}
	if len(kindsRes.Msg.GetKinds()) != 1 || kindsRes.Msg.GetKinds()[0].GetKind() != "local-directory" {
		t.Fatalf("unexpected source kinds: %+v", kindsRes.Msg.GetKinds())
	}

	paths := []string{
		root,
		filepath.Join(root, "a"),
		root + string(filepath.Separator) + ".",
		filepath.Join(root, "not-a-directory") + string(filepath.Separator),
		filepath.Join(root, "note"),
	}
	for _, path := range paths {
		localRes, err := client.BrowseLocal(t.Context(), connect.NewRequest(&ariadnev1.BrowseLocalRequest{Path: path}))
		if err != nil {
			t.Fatalf("BrowseLocal(%q): %v", path, err)
		}
		sourceRes, err := client.BrowseSource(t.Context(), connect.NewRequest(&ariadnev1.BrowseSourceRequest{
			Kind: "local-directory",
			Path: path,
		}))
		if err != nil {
			t.Fatalf("BrowseSource(%q): %v", path, err)
		}
		if path == root {
			if localRes.Msg.GetListing() == nil || len(localRes.Msg.GetListing().GetDirectories()) == 0 {
				t.Fatalf("expected non-empty listing for root: %+v", localRes.Msg)
			}
			if sourceRes.Msg.GetListing() == nil || len(sourceRes.Msg.GetListing().GetDirectories()) == 0 {
				t.Fatalf("expected non-empty source listing for root: %+v", sourceRes.Msg)
			}
		}
	}
}
