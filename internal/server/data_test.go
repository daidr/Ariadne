package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/data"
)

func TestDataSummaryDoesNotWaitForWorkspaceScan(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "ariadne.db")
	repo, err := core.Open(t.Context(), core.Options{Path: path})
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
	api, err := New(Options{Repository: repo, Data: storage, Token: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	get := func(ctx context.Context, path string) *httptest.ResponseRecorder {
		request := httptest.NewRequestWithContext(ctx, http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer test-token")
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		return response
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	measured := make(chan *httptest.ResponseRecorder, 1)
	go func() { measured <- get(ctx, "/api/settings/data") }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("disk scan did not start")
	}

	summary := get(ctx, "/api/settings/data?sizes=0")
	var usage data.Usage
	if err := json.Unmarshal(summary.Body.Bytes(), &usage); err != nil ||
		summary.Code != http.StatusOK || usage.Home != home || usage.Database == 0 || usage.Plugins != nil {
		t.Fatalf("summary failed while scan was blocked: %d %s", summary.Code, summary.Body)
	}

	close(release)
	response := <-measured
	if err := json.Unmarshal(response.Body.Bytes(), &usage); err != nil ||
		response.Code != http.StatusOK || usage.Plugins == nil || *usage.Plugins != 123 {
		t.Fatalf("measurement failed: %d %s", response.Code, response.Body)
	}

	combined := get(ctx, "/api/settings/data?sizes=storage")
	var result struct {
		Storage *data.StorageSnapshot
	}
	if err := json.Unmarshal(combined.Body.Bytes(), &result); err != nil ||
		combined.Code != http.StatusOK || result.Storage == nil || result.Storage.MeasuredAt == 0 {
		t.Fatalf("combined measurement failed: %d %s", combined.Code, combined.Body)
	}
	cached := get(ctx, "/api/settings/data?sizes=cached")
	if cached.Code != http.StatusOK || cached.Body.String() != combined.Body.String() {
		t.Fatalf("cached measurement differed: %d %s", cached.Code, cached.Body)
	}
}
