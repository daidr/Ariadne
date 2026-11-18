package leases

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRemoveWaitJoinsEveryGenerationAndRespectsCancellation(t *testing.T) {
	pool := New[*resource](Options{})
	defer closePool(t, pool)
	load := func(context.Context) (*resource, error) { return &resource{secret: "key"}, nil }
	one, err := pool.Acquire(t.Context(), "thread", "old", load)
	if err != nil {
		t.Fatal(err)
	}
	two, err := pool.Acquire(t.Context(), "thread", "new", load)
	if err != nil {
		t.Fatal(err)
	}
	if !pool.Busy("thread") {
		t.Fatal("active generations not reported busy")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if err := pool.RemoveWait(ctx, "thread"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("removal did not wait for readers", err)
	}
	one.Release()
	two.Release()
	if err := pool.RemoveWait(t.Context(), "thread"); err != nil {
		t.Fatal(err)
	}
	if one.Value.closed.Load() != 1 || two.Value.closed.Load() != 1 || pool.Busy("thread") {
		t.Fatal("removal retained a resource generation")
	}
}
