package data

import (
	"context"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/fsutil"
)

func TestStorageSharesScanAndInvalidatesLastMeasurement(t *testing.T) {
	store, _ := testStore(t, nil)
	var calls atomic.Int64
	entered, release := make(chan struct{}), make(chan struct{})
	store.options.Measure = func(ctx context.Context, _ string, paths []string) fsutil.StorageSizes {
		if !slices.Equal(paths, []string{"a", "b"}) {
			t.Errorf("unexpected scopes: %v", paths)
		}
		if calls.Add(1) == 1 {
			close(entered)
		}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return fsutil.StorageSizes{Managed: new(int64(123))}
	}
	done := make(chan *StorageSnapshot, 2)
	scan := func(paths []string) {
		result, err := store.MeasureStorage(t.Context(), paths)
		if err != nil {
			t.Error(err)
		}
		done <- result
	}
	go scan([]string{"b", "a", "b"})
	select {
	case <-entered:
	case <-t.Context().Done():
		t.Fatal("scan did not start")
	}
	go scan([]string{"a", "b"})
	close(release)
	for range 2 {
		if result := <-done; result == nil || result.Managed == nil || *result.Managed != 123 {
			t.Fatalf("unexpected result: %+v", result)
		}
	}
	if calls.Load() != 1 || store.LatestStorage() == nil {
		t.Fatal("identical requests did not reuse the scan")
	}
	store.InvalidateUsage()
	if store.LatestStorage() != nil {
		t.Fatal("invalidated snapshot retained")
	}
}

func TestStorageInvalidationStopsMeasuringRemovedPaths(t *testing.T) {
	store, _ := testStore(t, nil)
	started := make(chan struct{})
	var calls atomic.Int64
	store.options.Measure = func(ctx context.Context, _ string, paths []string) fsutil.StorageSizes {
		if calls.Add(1) == 1 {
			close(started)
			<-ctx.Done()
			return fsutil.StorageSizes{Total: new(int64(123))}
		}
		if len(paths) != 0 {
			t.Errorf("rescanned removed paths: %v", paths)
		}
		return fsutil.StorageSizes{Total: new(int64(0))}
	}
	previous := make(chan *StorageSnapshot, 1)
	go func() {
		result, err := store.MeasureStorage(t.Context(), []string{"removed"})
		if err != nil {
			t.Error(err)
		}
		previous <- result
	}()
	select {
	case <-started:
	case <-t.Context().Done():
		t.Fatal("scan did not start")
	}
	store.InvalidateUsage()
	if result := <-previous; result != nil || store.LatestStorage() != nil || calls.Load() != 1 {
		t.Fatalf("removed paths became cached: %+v", result)
	}
	result, err := store.MeasureStorage(t.Context(), nil)
	if err != nil || result.Total == nil || *result.Total != 0 {
		t.Fatalf("unexpected replacement snapshot: %+v, %v", result, err)
	}
}

func TestStorageKeepsPreviousMeasurementUntilRefresh(t *testing.T) {
	store, _ := testStore(t, nil)
	now := time.Unix(100, 0)
	store.options.Now = func() time.Time { return now }
	calls := int64(0)
	store.options.Measure = func(context.Context, string, []string) fsutil.StorageSizes {
		calls++
		return fsutil.StorageSizes{Total: new(calls)}
	}
	first, err := store.MeasureStorage(t.Context(), []string{"a"})
	if err != nil || first == nil {
		t.Fatal(first, err)
	}
	now = now.Add(store.options.ScanTTL + time.Second)
	if last := store.LatestStorage(); last != first {
		t.Fatal("expired measurement was unavailable while waiting for a refresh")
	}
	refreshed, err := store.MeasureStorage(t.Context(), []string{"a"})
	if err != nil || refreshed == nil || *refreshed.Total != 2 || refreshed.MeasuredAt <= first.MeasuredAt {
		t.Fatal(refreshed, err)
	}
}
