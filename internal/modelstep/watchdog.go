package modelstep

import (
	"context"
	"sync"
	"time"
)

type watchdog struct {
	mu       sync.Mutex
	timer    *time.Timer
	deadline time.Time
	timeout  time.Duration
	closed   bool
}

func newWatchdog(timeout time.Duration, cancel context.CancelCauseFunc) *watchdog {
	watch := &watchdog{timeout: timeout}
	if timeout <= 0 {
		return watch
	}
	watch.mu.Lock()
	watch.deadline = time.Now().Add(timeout)
	watch.timer = time.AfterFunc(timeout, func() {
		watch.mu.Lock()
		defer watch.mu.Unlock()
		if watch.closed {
			return
		}
		if remaining := time.Until(watch.deadline); remaining > 0 {
			watch.timer.Reset(remaining)
			return
		}
		watch.closed = true
		cancel(errSilent)
	})
	watch.mu.Unlock()
	return watch
}
func (watch *watchdog) touch() {
	if watch.timeout <= 0 {
		return
	}
	watch.mu.Lock()
	defer watch.mu.Unlock()
	if !watch.closed {
		watch.deadline = time.Now().Add(watch.timeout)
	}
}
func (watch *watchdog) close() {
	watch.mu.Lock()
	defer watch.mu.Unlock()
	watch.closed = true
	if watch.timer != nil {
		watch.timer.Stop()
		watch.timer = nil
	}
}
