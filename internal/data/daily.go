package data

import (
	"context"
	"sync"
	"time"
)

type Daily struct {
	Store   *Store
	OnError func(error)
	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
}

func (daily *Daily) Start(ctx context.Context) {
	daily.mu.Lock()
	defer daily.mu.Unlock()
	if daily.cancel != nil {
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	stopStore := context.AfterFunc(daily.Store.ctx, cancel)
	daily.cancel = cancel
	daily.done = make(chan struct{})
	done := daily.done
	go func() {
		defer close(done)
		defer stopStore()
		defer cancel()
		run := func() {
			_, err := daily.Store.DailyBackup(runCtx, daily.Store.options.Now())
			if err != nil && runCtx.Err() == nil && daily.OnError != nil {
				daily.OnError(err)
			}
		}
		run()
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				run()
			}
		}
	}()
}
func (daily *Daily) Stop(ctx context.Context) error {
	daily.mu.Lock()
	cancel, done := daily.cancel, daily.done
	if cancel != nil {
		cancel()
	}
	daily.mu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
	}
	daily.mu.Lock()
	if daily.done == done {
		daily.cancel = nil
		daily.done = nil
	}
	daily.mu.Unlock()
	return nil
}
