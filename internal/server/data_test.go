package server

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/data"
	"github.com/daidr/Ariadne/internal/domainerr"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
)

func bearerClientInterceptor(token string) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if token != "" {
				req.Header().Set("Authorization", "Bearer "+token)
			}
			return next(ctx, req)
		}
	}
}

func extractLocalizedError(t *testing.T, err error) (*connect.Error, *ariadnev1.LocalizedError) {
	t.Helper()
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) {
		t.Fatalf("expected *connect.Error, got %T: %v", err, err)
	}
	for _, detail := range connectErr.Details() {
		value, valueErr := detail.Value()
		if valueErr != nil {
			t.Fatalf("decode error detail: %v", valueErr)
		}
		if localized, ok := value.(*ariadnev1.LocalizedError); ok {
			return connectErr, localized
		}
	}
	return connectErr, nil
}

func TestDataServiceConnectEndToEnd(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "ariadne.db")
	now := time.UnixMilli(1_800_000_000_000)
	repo, err := core.Open(t.Context(), core.Options{Path: path, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if err := os.Mkdir(filepath.Join(home, "plugins"), 0700); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	storage, err := data.New(data.Options{
		Events: repo.Store, Home: home, DatabasePath: path,
		Now: func() time.Time { return now },
		Scan: func(ctx context.Context, _ string) *int64 {
			close(entered)
			select {
			case <-ctx.Done():
				return nil
			case <-release:
				return new(int64(123))
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	api, err := New(Options{
		Repository: repo,
		Data:       storage,
		Token:      "test-token",
		Now:        func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()

	srv := httptest.NewServer(api)
	defer srv.Close()

	client := ariadnev1connect.NewDataServiceClient(
		srv.Client(),
		srv.URL+rpcPrefix,
		connect.WithInterceptors(bearerClientInterceptor("test-token")),
	)
	unauthClient := ariadnev1connect.NewDataServiceClient(srv.Client(), srv.URL+rpcPrefix)

	// Unauthenticated request is rejected before service execution.
	_, err = unauthClient.GetDataUsage(t.Context(), connect.NewRequest(&ariadnev1.GetDataUsageRequest{}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("expected unauthenticated error, got %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	type usageResult struct {
		resp *connect.Response[ariadnev1.GetDataUsageResponse]
		err  error
	}
	measured := make(chan usageResult, 1)
	go func() {
		resp, callErr := client.GetDataUsage(ctx, connect.NewRequest(&ariadnev1.GetDataUsageRequest{IncludeSizes: true}))
		measured <- usageResult{resp: resp, err: callErr}
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("disk scan did not start")
	}

	// Summary succeeds immediately while full size scan is blocked.
	summaryResp, err := client.GetDataUsage(ctx, connect.NewRequest(&ariadnev1.GetDataUsageRequest{IncludeSizes: false}))
	if err != nil {
		t.Fatalf("summary failed while scan was blocked: %v", err)
	}
	if summaryResp.Msg.Home != home || summaryResp.Msg.Database == 0 || summaryResp.Msg.Plugins != nil {
		t.Fatalf("unexpected summary payload: %+v", summaryResp.Msg)
	}
	if got := summaryResp.Header().Get("X-Ariadne-Server-Time"); got != strconv.FormatInt(now.UnixMilli(), 10) {
		t.Fatalf("unexpected X-Ariadne-Server-Time: %q", got)
	}
	if got := summaryResp.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("unexpected Cache-Control: %q", got)
	}
	if len(summaryResp.Header().Values("Set-Cookie")) != 1 {
		t.Fatalf("expected file session Set-Cookie header, got %v", summaryResp.Header().Values("Set-Cookie"))
	}

	close(release)
	full := <-measured
	if full.err != nil || full.resp.Msg.Plugins == nil || *full.resp.Msg.Plugins != 123 {
		t.Fatalf("full measurement failed: resp=%+v err=%v", full.resp, full.err)
	}

	// Measure storage snapshot and verify cached read matches.
	combined, err := client.GetStorageSnapshot(ctx, connect.NewRequest(&ariadnev1.GetStorageSnapshotRequest{CachedOnly: false}))
	if err != nil || combined.Msg.Storage == nil || combined.Msg.Storage.MeasuredAt == 0 {
		t.Fatalf("storage snapshot failed: resp=%+v err=%v", combined, err)
	}
	cached, err := client.GetStorageSnapshot(ctx, connect.NewRequest(&ariadnev1.GetStorageSnapshotRequest{CachedOnly: true}))
	if err != nil || !proto.Equal(cached.Msg.Storage, combined.Msg.Storage) {
		t.Fatalf("cached snapshot differed: got=%+v want=%+v err=%v", cached, combined, err)
	}

	// Create and delete a database backup via write RPCs.
	created, err := client.CreateBackup(ctx, connect.NewRequest(&ariadnev1.CreateBackupRequest{}))
	if err != nil || created.Msg.Backup == nil || created.Msg.Backup.Name == "" || created.Msg.Backup.Size == 0 {
		t.Fatalf("create backup failed: resp=%+v err=%v", created, err)
	}
	if _, err := client.DeleteBackup(ctx, connect.NewRequest(&ariadnev1.DeleteBackupRequest{Name: created.Msg.Backup.Name})); err != nil {
		t.Fatalf("delete backup failed: %v", err)
	}

	// Invalid backup name maps domainerr into Connect InvalidArgument + LocalizedError detail.
	_, err = client.DeleteBackup(ctx, connect.NewRequest(&ariadnev1.DeleteBackupRequest{Name: "../bad"}))
	connectErr, detail := extractLocalizedError(t, err)
	if connectErr.Code() != connect.CodeInvalidArgument || detail == nil || detail.Key == "" {
		t.Fatalf("expected invalid argument with localized detail, got code=%v detail=%+v", connectErr.Code(), detail)
	}

	// Invalid worktree path returns error.invalidBody with reason=path.
	_, err = client.MeasureWorktreeReclaimable(ctx, connect.NewRequest(&ariadnev1.MeasureWorktreeReclaimableRequest{Path: "relative/path"}))
	connectErr, detail = extractLocalizedError(t, err)
	if connectErr.Code() != connect.CodeInvalidArgument || detail == nil || detail.Key != "error.invalidBody" || detail.Params["reason"].GetStringValue() != "path" {
		t.Fatalf("unexpected reclaimable error: code=%v detail=%+v", connectErr.Code(), detail)
	}
}

func TestConnectErrorMappingAndDirectAuthInterceptor(t *testing.T) {
	var reported []error
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(t.TempDir(), "rpc.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	api, err := New(Options{
		Repository: repo,
		Token:      "secret",
		OnError:    func(err error) { reported = append(reported, err) },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()

	// Verify connectAuthInterceptor protects direct rpcMux calls even without httpauth.Middleware.
	directSrv := httptest.NewServer(api.rpcMux)
	defer directSrv.Close()
	directClient := ariadnev1connect.NewDataServiceClient(directSrv.Client(), directSrv.URL)
	_, err = directClient.GetDataUsage(t.Context(), connect.NewRequest(&ariadnev1.GetDataUsageRequest{}))
	connectErr, detail := extractLocalizedError(t, err)
	if connectErr.Code() != connect.CodeUnauthenticated || detail == nil || detail.Key != "error.unauthorized" {
		t.Fatalf("direct auth interceptor failed: code=%v detail=%+v", connectErr.Code(), detail)
	}

	// Verify domainerr -> Connect code mappings.
	cases := []struct {
		name     string
		err      error
		wantCode connect.Code
		wantKey  string
	}{
		{
			name:     "notFound",
			err:      domainerr.NotFound("item"),
			wantCode: connect.CodeNotFound,
			wantKey:  "error.notFound",
		},
		{
			name:     "conflict",
			err:      domainerr.New("error.artifactConflict", object{"currentVersion": 3}),
			wantCode: connect.CodeAborted,
			wantKey:  "error.artifactConflict",
		},
		{
			name:     "internal",
			err:      errors.New("boom"),
			wantCode: connect.CodeInternal,
			wantKey:  "error.invalidBody",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mapped := api.toConnectError(tc.err)
			ce, det := extractLocalizedError(t, mapped)
			if ce.Code() != tc.wantCode || det == nil || det.Key != tc.wantKey {
				t.Fatalf("got code=%v detail=%+v, want code=%v key=%s", ce.Code(), det, tc.wantCode, tc.wantKey)
			}
		})
	}
	if len(reported) != 1 || reported[0].Error() != "boom" {
		t.Fatalf("expected unexpected error to be reported once, got %v", reported)
	}
}
