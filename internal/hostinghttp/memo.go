package hostinghttp

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Memo retains one immutable value. Expiry releases it even if it is never read
// again. A cancelled waiter does not cancel a shared lookup for other callers.
type Memo[T any] struct {
	mu            sync.Mutex
	life          context.Context
	cancel        context.CancelFunc
	load          func(context.Context) (T, error)
	keep          func(T) bool
	ttl           time.Duration
	value         T
	valid, closed bool
	timer         *time.Timer
	pending       *memoLoad[T]
	jobs          sync.WaitGroup
	done          chan struct{}
}
type memoLoad[T any] struct {
	done  chan struct{}
	value T
	err   error
}

func safeLoad[T any](ctx context.Context, load func(context.Context) (T, error)) (value T, err error) {
	defer func() {
		if recover() != nil {
			var zero T
			value = zero
			err = errors.New("repository lookup failed")
		}
	}()
	return load(ctx)
}

func NewMemo[T any](ttl time.Duration, load func(context.Context) (T, error), keep func(T) bool) *Memo[T] {
	life, cancel := context.WithCancel(context.Background())
	return &Memo[T]{life: life, cancel: cancel, load: load, keep: keep, ttl: ttl, done: make(chan struct{})}
}
func (memo *Memo[T]) Get(ctx context.Context) (T, error) {
	memo.mu.Lock()
	var zero T
	if err := ctx.Err(); err != nil {
		memo.mu.Unlock()
		return zero, err
	}
	if memo.closed {
		memo.mu.Unlock()
		return zero, errors.New("repository metadata cache is closed")
	}
	if memo.valid {
		value := memo.value
		memo.mu.Unlock()
		return value, nil
	}
	pending := memo.pending
	if pending == nil {
		pending = &memoLoad[T]{done: make(chan struct{})}
		memo.pending = pending
		memo.jobs.Add(1)
		go func() {
			defer memo.jobs.Done()
			ctx, cancel := context.WithTimeout(memo.life, 30*time.Second)
			defer cancel()
			pending.value, pending.err = safeLoad(ctx, memo.load)
			memo.mu.Lock()
			defer memo.mu.Unlock()
			if !memo.closed && pending.err == nil && (memo.keep == nil || memo.keep(pending.value)) {
				memo.value, memo.valid = pending.value, true
				memo.timer = time.AfterFunc(memo.ttl, func() { memo.mu.Lock(); memo.value, memo.valid = zero, false; memo.timer = nil; memo.mu.Unlock() })
			}
			memo.pending = nil
			close(pending.done)
		}()
	}
	memo.mu.Unlock()
	select {
	case <-ctx.Done():
		return zero, ctx.Err()
	case <-pending.done:
		return pending.value, pending.err
	}
}
func (memo *Memo[T]) Close() error {
	memo.mu.Lock()
	if memo.closed {
		memo.mu.Unlock()
		<-memo.done
		return nil
	}
	memo.closed = true
	if memo.timer != nil {
		memo.timer.Stop()
		memo.timer = nil
	}
	var zero T
	memo.value, memo.valid = zero, false
	memo.cancel()
	memo.mu.Unlock()
	memo.jobs.Wait()
	memo.mu.Lock()
	memo.load = nil
	memo.keep = nil
	memo.mu.Unlock()
	close(memo.done)
	return nil
}
