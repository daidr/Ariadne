package server

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"

	"github.com/daidr/Ariadne/internal/core"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/internal/update"
)

func TestUpdateServiceConnectEndToEnd(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	repo, err := core.Open(t.Context(), core.Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	store, err := settings.Open(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	updates := update.New(t.Context(), store, update.Options{
		Version: "0.1.0",
	})
	defer updates.Close(context.Background())

	api, err := New(Options{
		Repository: repo,
		Token:      "test-token",
		Settings:   store,
		Updates:    updates,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()

	server := httptest.NewServer(api)
	defer server.Close()
	client := ariadnev1connect.NewUpdateServiceClient(
		server.Client(),
		server.URL+rpcPrefix,
		connect.WithInterceptors(bearerClientInterceptor("test-token")),
	)

	statusRes, err := client.GetUpdateStatus(t.Context(), connect.NewRequest(&ariadnev1.GetUpdateStatusRequest{}))
	if err != nil {
		t.Fatalf("GetUpdateStatus: %v", err)
	}
	if statusRes.Msg.GetStatus().GetVersion() != "0.1.0" {
		t.Fatalf("unexpected version: %+v", statusRes.Msg.GetStatus())
	}

	saveRes, err := client.SaveUpdatePreferences(t.Context(), connect.NewRequest(&ariadnev1.SaveUpdatePreferencesRequest{
		Channel:   "preview",
		Automatic: false,
	}))
	if err != nil {
		t.Fatalf("SaveUpdatePreferences: %v", err)
	}
	if saveRes.Msg.GetStatus().GetPreferences().GetChannel() != "preview" || saveRes.Msg.GetStatus().GetPreferences().GetAutomatic() {
		t.Fatalf("unexpected saved preferences: %+v", saveRes.Msg.GetStatus().GetPreferences())
	}

	if _, err := client.SaveUpdatePreferences(t.Context(), connect.NewRequest(&ariadnev1.SaveUpdatePreferencesRequest{
		Channel:   "invalid",
		Automatic: true,
	})); err == nil {
		t.Fatal("expected invalid channel to fail")
	} else if connectErr, localized := extractLocalizedError(t, err); connectErr.Code() != connect.CodeInvalidArgument || localized == nil || localized.Key != "error.invalidBody" {
		t.Fatalf("unexpected error for invalid channel: code=%v localized=%v", connectErr.Code(), localized)
	}

	if _, err := client.InstallUpdate(t.Context(), connect.NewRequest(&ariadnev1.InstallUpdateRequest{
		Version: "9.9.9",
	})); err == nil {
		t.Fatal("expected unavailable install to fail")
	} else if connectErr, localized := extractLocalizedError(t, err); connectErr.Code() != connect.CodeAborted || localized == nil || localized.Key != "updates.error.unavailable" {
		t.Fatalf("unexpected error for unavailable install: code=%v localized=%v", connectErr.Code(), localized)
	}

	if _, err := client.InstallUpdate(t.Context(), connect.NewRequest(&ariadnev1.InstallUpdateRequest{})); err == nil {
		t.Fatal("expected empty version install to fail")
	} else if connectErr, _ := extractLocalizedError(t, err); connectErr.Code() != connect.CodeInvalidArgument {
		t.Fatalf("expected CodeInvalidArgument for empty version, got %v", connectErr.Code())
	}
}
