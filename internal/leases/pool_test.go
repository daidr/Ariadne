package leases

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

type resource struct {
	closed atomic.Int32
	secret string
}

func (value *resource) Close() error { value.secret = ""; value.closed.Add(1); return nil }
func closePool(t *testing.T, pool *Pool[*resource]) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := pool.Close(ctx); err != nil {
		t.Error(err)
	}
}
func TestActiveResourcesDoNotQueueAtIdleLimit(t *testing.T) {
	pool := New[*resource](Options{Idle: 1, TTL: time.Hour})
	defer closePool(t, pool)
	values := []Lease[*resource]{}
	for i := 0; i < 40; i++ {
		lease, err := pool.Acquire(t.Context(), fmt.Sprint(i), "config", func(context.Context) (*resource, error) { return &resource{secret: "key"}, nil })
		if err != nil {
			t.Fatal("active resource rejected at idle capacity", err)
		}
		values = append(values, lease)
	}
	for _, lease := range values {
		if lease.Value.closed.Load() != 0 {
			t.Fatal("active resource closed")
		}
		lease.Release()
		lease.Release()
	}
	deadline := time.Now().Add(time.Second)
	for {
		closed := 0
		for _, lease := range values {
			closed += int(lease.Value.closed.Load())
		}
		if closed >= 39 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("idle resources not evicted")
		}
		time.Sleep(time.Millisecond)
	}
}
func TestSharedLookupSurvivesCancellationOfOneWaiter(t *testing.T) {
	pool := New[*resource](Options{})
	defer closePool(t, pool)
	started := make(chan struct{})
	ready := make(chan struct{})
	var calls atomic.Int32
	loader := func(ctx context.Context) (*resource, error) {
		calls.Add(1)
		close(started)
		select {
		case <-ready:
			return &resource{}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancelled := make(chan error, 1)
	go func() { _, err := pool.Acquire(ctx, "id", "config", loader); cancelled <- err }()
	<-started
	completed := make(chan Lease[*resource], 1)
	failed := make(chan error, 1)
	go func() {
		lease, err := pool.Acquire(t.Context(), "id", "config", loader)
		if err != nil {
			failed <- err
		} else {
			completed <- lease
		}
	}()
	cancel()
	if err := <-cancelled; !errors.Is(err, context.Canceled) {
		t.Fatal("waiter not cancelled", err)
	}
	close(ready)
	select {
	case lease := <-completed:
		lease.Release()
	case err := <-failed:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("shared lookup cancelled or hung")
	}
	if calls.Load() != 1 {
		t.Fatal("lookup not coalesced", calls.Load())
	}
}
func TestConfigurationChangeAndRemovalRespectActiveLeases(t *testing.T) {
	pool := New[*resource](Options{})
	defer closePool(t, pool)
	loader := func(context.Context) (*resource, error) { return &resource{}, nil }
	one, err := pool.Acquire(t.Context(), "id", "old", loader)
	if err != nil {
		t.Fatal(err)
	}
	oldCtx, stopOld := one.Context(t.Context())
	defer stopOld()
	two, err := pool.Acquire(t.Context(), "id", "new", loader)
	if err != nil {
		t.Fatal(err)
	}
	newCtx, stopNew := two.Context(t.Context())
	defer stopNew()
	if oldCtx.Err() != nil || one.Value.closed.Load() != 0 {
		t.Fatal("configuration change closed a resource in use")
	}
	pool.Remove("id")
	deadline := time.Now().Add(time.Second)
	for oldCtx.Err() == nil || newCtx.Err() == nil {
		if time.Now().After(deadline) {
			t.Fatal("project removal did not cancel every resource generation")
		}
		time.Sleep(time.Millisecond)
	}
	if one.Value.closed.Load() != 0 || two.Value.closed.Load() != 0 {
		t.Fatal("resource closed before operation release")
	}
	one.Release()
	two.Release()
}
func TestExpiryReleasesResourcesWithoutAnotherRead(t *testing.T) {
	pool := New[*resource](Options{TTL: 20 * time.Millisecond})
	defer closePool(t, pool)
	lease, err := pool.Acquire(t.Context(), "id", "config", func(context.Context) (*resource, error) { return &resource{secret: "key"}, nil })
	if err != nil {
		t.Fatal(err)
	}
	value := lease.Value
	lease.Release()
	deadline := time.Now().Add(time.Second)
	for {
		pool.mu.Lock()
		retained := len(pool.entries) != 0 || len(pool.all) != 0
		pool.mu.Unlock()
		if value.closed.Load() != 0 && !retained {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("expired resource retained")
		}
		time.Sleep(time.Millisecond)
	}
	pool.mu.Lock()
	retained := len(pool.entries) != 0 || len(pool.all) != 0
	pool.mu.Unlock()
	if retained {
		t.Fatal("expired entry retained metadata")
	}
}
func TestCloseCancelsOperationsAndWaitsForLeaseRelease(t *testing.T) {
	pool := New[*resource](Options{})
	lease, err := pool.Acquire(t.Context(), "id", "config", func(context.Context) (*resource, error) { return &resource{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := lease.Context(t.Context())
	defer stop()
	deadline, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := pool.Close(deadline); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("close did not wait for active operation", err)
	}
	if ctx.Err() == nil || lease.Value.closed.Load() != 0 {
		t.Fatal("close cancellation or lease lifetime wrong")
	}
	lease.Release()
	closePool(t, pool)
	if lease.Value.closed.Load() != 1 {
		t.Fatal("resource not closed exactly once")
	}
	if _, err := pool.Acquire(t.Context(), "id", "config", func(context.Context) (*resource, error) { t.Fatal("closed pool started loader"); return nil, nil }); err == nil {
		t.Fatal("closed pool admitted work")
	}
}
func TestFailedAndPanickingLoadersDoNotRetainResources(t *testing.T) {
	pool := New[*resource](Options{})
	if _, err := pool.Acquire(t.Context(), "failed", "config", func(context.Context) (*resource, error) { return nil, errors.New("unavailable") }); err == nil {
		t.Fatal("failed load succeeded")
	}
	if _, err := pool.Acquire(t.Context(), "panic", "config", func(context.Context) (*resource, error) { panic("private settings") }); err == nil || err.Error() != "resource loader failed" {
		t.Fatal("panic escaped", err)
	}
	closePool(t, pool)
}
