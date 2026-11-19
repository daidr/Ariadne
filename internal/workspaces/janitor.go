package workspaces

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/eventstore"
	"github.com/daidr/Ariadne/sdk"
)

type JanitorOptions struct {
	Interval, Delay, Idle time.Duration
	Now                   func() time.Time
	OnError               func(error)
}
type Janitor struct {
	manager *Manager
	options JanitorOptions
	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	serial  threadLocks
}

func NewJanitor(manager *Manager, options JanitorOptions) *Janitor {
	if options.Interval <= 0 {
		options.Interval = 10 * time.Minute
	}
	if options.Delay <= 0 {
		options.Delay = 2 * time.Second
	}
	if options.Idle <= 0 {
		options.Idle = 24 * time.Hour
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &Janitor{manager: manager, options: options}
}
func (janitor *Janitor) Start(ctx context.Context) error {
	janitor.mu.Lock()
	defer janitor.mu.Unlock()
	if janitor.cancel != nil {
		return nil
	}
	ctx, done, err := janitor.manager.begin(ctx)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	watch, unwatch := janitor.manager.options.Repository.Store.Watch()
	seq, err := janitor.manager.options.Repository.Store.LastSeq(ctx)
	if err != nil {
		unwatch()
		done()
		cancel()
		return err
	}
	janitor.cancel, janitor.done = cancel, make(chan struct{})
	go func() {
		defer close(janitor.done)
		defer done()
		defer unwatch()
		defer cancel()
		janitor.run(ctx, watch, seq)
	}()
	return nil
}
func (janitor *Janitor) Stop(ctx context.Context) error {
	janitor.mu.Lock()
	cancel, done := janitor.cancel, janitor.done
	if cancel != nil {
		cancel()
	}
	janitor.mu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
	}
	janitor.mu.Lock()
	if janitor.done == done {
		janitor.cancel, janitor.done = nil, nil
	}
	janitor.mu.Unlock()
	return nil
}
func (janitor *Janitor) failed(err error) {
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	if janitor.options.OnError != nil {
		func() { defer func() { recover() }(); janitor.options.OnError(err) }()
	}
}
func (janitor *Janitor) run(ctx context.Context, watch <-chan struct{}, seq int64) {
	// Coalesced wakeups and a fixed-size due map keep event bursts from growing
	// a chain of closures or per-thread timers. The durable log is the backlog.
	ticker := time.NewTicker(min(janitor.options.Delay, janitor.options.Interval))
	defer ticker.Stop()
	pending := map[string]time.Time{}
	nextSweep := janitor.options.Now()
	backlog := false
	for ctx.Err() == nil {
		now := janitor.options.Now()
		if !now.Before(nextSweep) {
			janitor.failed(janitor.Sweep(ctx))
			nextSweep = now.Add(janitor.options.Interval)
		}
		for thread, due := range pending {
			if !now.Before(due) {
				delete(pending, thread)
				janitor.failed(janitor.Check(ctx, thread))
			}
		}
		if !backlog {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-watch:
				if !ok {
					return
				}
			case <-ticker.C:
			}
		}
		backlog = false
		// Check due work between batches even while new events keep arriving.
		for batch := 0; batch < 8 && ctx.Err() == nil; batch++ {
			events, err := janitor.manager.options.Repository.Store.Read(ctx, eventstore.ReadOptions{AfterSeq: seq, Limit: 128})
			if err != nil {
				backlog = false
				janitor.failed(err)
				break
			}
			if len(events) == 0 {
				backlog = false
				break
			}
			for _, event := range events {
				seq = event.Seq
				if event.Type == "project.settings.set" {
					nextSweep = janitor.options.Now()
					continue
				}
				key := ""
				switch event.Type {
				case "branch.merged", "pull_request.closed", "thread.reported", "thread.completed":
					key = "threadId"
				case "turn.finished":
					key = "conversationId"
				}
				if key == "" {
					continue
				}
				var data map[string]any
				if json.Unmarshal(event.Data, &data) != nil {
					continue
				}
				thread, _ := data[key].(string)
				if thread == "" || len(thread) > 512 {
					continue
				}
				if _, known := pending[thread]; known {
					continue
				}
				if len(pending) >= 4096 {
					nextSweep = janitor.options.Now()
					continue
				}
				pending[thread] = janitor.options.Now().Add(janitor.options.Delay)
			}
			if len(events) < 128 {
				backlog = false
				break
			}
			backlog = true
		}
	}
}
func (janitor *Janitor) Sweep(ctx context.Context) error {
	ctx, done, err := janitor.manager.begin(ctx)
	if err != nil {
		return err
	}
	defer done()
	projects, err := janitor.manager.options.Repository.ListProjects(ctx)
	if err != nil {
		return err
	}
	for _, project := range projects {
		threads, err := janitor.manager.options.Repository.ListThreads(ctx, project.ID)
		if err != nil {
			return err
		}
		for _, thread := range threads {
			if candidate(thread) || parked(thread) {
				if err := janitor.Check(ctx, thread.ID); err != nil {
					janitor.failed(err)
				}
			}
			if err := ctx.Err(); err != nil {
				return err
			}
		}
	}
	return nil
}
func candidate(thread core.ThreadView) bool {
	return thread.Workspace != nil && optionalString(thread.Workspace.Branch) != nil && !thread.Workspace.Removed && (thread.Stage == "review" || thread.Stage == "done") && (thread.PullRequest != nil || optionalString(thread.Workspace.Base) != nil)
}
func parked(thread core.ThreadView) bool {
	return thread.Workspace != nil && optionalString(thread.Workspace.Branch) != nil && thread.Workspace.Parked
}
func (janitor *Janitor) landed(ctx context.Context, thread core.ThreadView) (bool, error) {
	if pr, ok := thread.PullRequest.(map[string]any); ok {
		return pr["state"] != "open", nil
	}
	if thread.Workspace == nil {
		return false, nil
	}
	base, branch := optionalString(thread.Workspace.Base), optionalString(thread.Workspace.Branch)
	if base == nil || branch == nil || *base == "" || *branch == "" {
		return false, nil
	}
	branches, err := janitor.manager.BranchesForThread(ctx, thread)
	if err != nil || branches == nil {
		return false, err
	}
	own := sdk.ThreadBranch{ThreadID: thread.ID, Branch: *branch}
	if merge, ok := thread.Merged.(map[string]any); ok && branches.Changes != nil {
		if base, ok := merge["base"].(string); ok {
			changes, err := branches.Changes(ctx, own, base)
			if err != nil {
				return false, err
			}
			if changes != nil && changes.Head == merge["head"] {
				return true, nil
			}
		}
	}
	if branches.Merged == nil {
		return false, nil
	}
	return branches.Merged(ctx, own, *base)
}
func (janitor *Janitor) Check(ctx context.Context, threadID string) error {
	ctx, done, err := janitor.manager.begin(ctx)
	if err != nil {
		return err
	}
	defer done()
	unlock, err := janitor.serial.lock(ctx, threadID)
	if err != nil {
		return err
	}
	defer unlock()
	repo := janitor.manager.options.Repository
	thread, err := repo.GetThread(ctx, threadID)
	if err != nil {
		return nil
	}
	project, err := repo.GetProject(ctx, thread.ProjectID)
	if err != nil {
		return err
	}
	retention := project.Settings.Workspace.Retention
	if retention == "keep" || (!candidate(thread) && !parked(thread)) {
		return nil
	}
	landed, err := janitor.landed(ctx, thread)
	if err != nil {
		return err
	}
	current, err := repo.GetThread(ctx, threadID)
	if err != nil {
		return nil
	}
	project, err = repo.GetProject(ctx, thread.ProjectID)
	if err != nil {
		return err
	}
	if project.Settings.Workspace.Retention != retention {
		return nil
	}
	if parked(thread) {
		if landed && parked(current) {
			return repo.RecordWorktreeRemoved(ctx, threadID, "merged")
		}
		return nil
	}
	if !candidate(current) {
		return nil
	}
	reason := ""
	if landed {
		reason = "merged"
	} else if retention == "auto" && janitor.options.Now().UnixMilli()-current.ActiveAt >= janitor.options.Idle.Milliseconds() {
		reason = "idle"
	}
	if reason == "" {
		return nil
	}
	_, err = janitor.manager.RemoveWorktree(ctx, thread.ProjectID, threadID, reason)
	return err
}
