// Package scheduler starts at most one turn per conversation. Only workers
// consume global or project slots; coordinators have independent capacity.
package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/eventstore"
)

const (
	DrainSafePoint = "safe-point"
	DrainTurn      = "turn"
)

type ActiveTurn struct {
	ConversationID string
	ProjectID      string
	Role           string
	Model          string
	Stateful       bool
	StartedAt      int64
}

var ErrCancelled = errors.New("cancelled")
var ErrInterrupted = errors.New("interrupted")

type Options struct {
	Repository *core.Repository
	// Run resolves the model and executes a turn. An error means the runner could
	// not record the failure itself; the scheduler then closes the turn as failed.
	Run              func(context.Context, core.ConversationView) error
	Slots            *int
	ReservedSlots    *int
	OnError          func(error)
	OnThreadStopped  func(string)
	OnThreadFinished func(threadID string, keepAsked bool)
}
type running struct {
	conversationID  string
	projectID, role string
	model           string
	stateful        bool
	startedAt       int64
	reserved        bool
	cancel          context.CancelCauseFunc
	done            chan struct{}
}
type Scheduler struct {
	options              Options
	mu                   sync.Mutex
	running              map[string]*running
	held                 map[string]int
	slots, reservedSlots int
	started, stopped     bool
	drainMode            string
	changed              chan struct{}
	onActiveChange       func(int)
	ctx                  context.Context
	cancel               context.CancelCauseFunc
	wake                 chan struct{}
	done                 chan struct{}
	turns                sync.WaitGroup
}

func New(options Options) (*Scheduler, error) {
	if options.Repository == nil || options.Run == nil {
		return nil, errors.New("scheduler repository and runner are required")
	}
	slots, reserved := 3, 1
	if options.Slots != nil {
		slots = *options.Slots
	}
	if options.ReservedSlots != nil {
		reserved = *options.ReservedSlots
	}
	if slots < 0 || reserved < 0 {
		return nil, errors.New("scheduler slots must not be negative")
	}
	return &Scheduler{options: options, running: map[string]*running{}, held: map[string]int{}, slots: slots, reservedSlots: reserved, changed: make(chan struct{}), wake: make(chan struct{}, 1), done: make(chan struct{})}, nil
}
func (scheduler *Scheduler) broadcastChangeLocked() (func(int), int) {
	close(scheduler.changed)
	scheduler.changed = make(chan struct{})
	return scheduler.onActiveChange, len(scheduler.running)
}
func (scheduler *Scheduler) SetOnActiveChange(fn func(int)) {
	scheduler.mu.Lock()
	scheduler.onActiveChange = fn
	scheduler.mu.Unlock()
}
func (scheduler *Scheduler) report(err error) {
	if err != nil && scheduler.options.OnError != nil {
		scheduler.options.OnError(err)
	}
}
func (scheduler *Scheduler) signal() {
	select {
	case scheduler.wake <- struct{}{}:
	default:
	}
}
func (scheduler *Scheduler) SetSlots(slots, reserved int) error {
	if slots < 0 || reserved < 0 {
		return errors.New("scheduler slots must not be negative")
	}
	scheduler.mu.Lock()
	scheduler.slots, scheduler.reservedSlots = slots, reserved
	scheduler.mu.Unlock()
	scheduler.signal()
	return nil
}
func (scheduler *Scheduler) Start(ctx context.Context) error {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	if scheduler.stopped {
		return errors.New("scheduler is stopped")
	}
	if scheduler.started {
		return nil
	}
	repo := scheduler.options.Repository
	wake, unsubscribe := repo.Store.Watch()
	seq, err := repo.Store.LastSeq(ctx)
	if err != nil {
		unsubscribe()
		return err
	}
	conversations, err := repo.ListRunningConversations(ctx)
	if err != nil {
		unsubscribe()
		return err
	}
	for _, conversation := range conversations {
		if conversation.Turn != nil {
			if err := repo.FinishTurn(ctx, conversation.ID, conversation.Turn.ID, "interrupted", domainerr.New("error.interrupted", nil)); err != nil {
				unsubscribe()
				return err
			}
		}
	}
	scheduler.ctx, scheduler.cancel = context.WithCancelCause(ctx)
	scheduler.started = true
	go scheduler.loop(wake, unsubscribe, seq)
	return nil
}
func (scheduler *Scheduler) loop(events <-chan struct{}, unsubscribe func(), seq int64) {
	defer func() {
		unsubscribe()
		scheduler.mu.Lock()
		scheduler.stopped = true
		clear(scheduler.held)
		for _, run := range scheduler.running {
			run.cancel(ErrInterrupted)
		}
		scheduler.mu.Unlock()
		scheduler.turns.Wait()
		close(scheduler.done)
	}()
	for {
		var err error
		seq, err = scheduler.consume(seq)
		if err != nil {
			if scheduler.ctx.Err() == nil {
				scheduler.report(err)
			}
			return
		}
		if err := scheduler.fill(); err != nil {
			if scheduler.ctx.Err() == nil {
				scheduler.report(err)
			}
			return
		}
		select {
		case <-scheduler.ctx.Done():
			return
		case _, ok := <-events:
			if !ok {
				return
			}
		case <-scheduler.wake:
		}
	}
}
func (scheduler *Scheduler) consume(seq int64) (int64, error) {
	for {
		events, err := scheduler.options.Repository.Store.Read(scheduler.ctx, eventstore.ReadOptions{AfterSeq: seq, Limit: 256})
		if err != nil {
			return seq, err
		}
		for _, event := range events {
			var data struct {
				ThreadID string `json:"threadId"`
				Report   string `json:"report"`
			}
			if err := json.Unmarshal(event.Data, &data); err != nil {
				return seq, err
			}
			if event.Type == "thread.stopped" {
				scheduler.Cancel(data.ThreadID)
				if scheduler.options.OnThreadStopped != nil {
					scheduler.options.OnThreadStopped(data.ThreadID)
				}
			}
			finish := event.Type == "branch.merged" || event.Type == "pull_request.closed" || event.Type == "thread.reviewed" || event.Type == "thread.completed"
			if event.Type == "thread.reported" {
				finish = data.Report == "ready" || data.Report == "done" || data.Report == "failed" || data.Report == "blocked"
			}
			if finish && scheduler.options.OnThreadFinished != nil {
				scheduler.options.OnThreadFinished(data.ThreadID, event.Type == "thread.reported" && data.Report == "ready")
			}
			seq = event.Seq
		}
		if len(events) < 256 {
			return seq, nil
		}
		// Keep scheduling responsive even while another writer continually adds
		// events. The next batch uses the durable cursor and one bounded wakeup.
		scheduler.signal()
		return seq, nil
	}
}
func (scheduler *Scheduler) fill() error {
	repo := scheduler.options.Repository
	// A running turn may finish between the pending query and dispatch. The
	// query might see a pending message before that turn starts or settles it.
	// Skip every turn that owned its slot before the snapshot. Finishing the
	// run wakes the scheduler, which can then read the current pending state.
	scheduler.mu.Lock()
	if scheduler.drainMode != "" {
		scheduler.mu.Unlock()
		return nil
	}
	active := make(map[string]bool, len(scheduler.running))
	for id := range scheduler.running {
		active[id] = true
	}
	scheduler.mu.Unlock()
	pending, err := repo.ListPendingConversations(scheduler.ctx)
	if err != nil {
		return err
	}
	caps := map[string]*int{}
	for _, conversation := range pending {
		if active[conversation.ID] {
			continue
		}
		if conversation.Role != "coordinator" {
			if _, known := caps[conversation.ProjectID]; !known {
				project, err := repo.GetProject(scheduler.ctx, conversation.ProjectID)
				if err != nil {
					var domain *domainerr.Error
					if !errors.As(err, &domain) || domain.Key != "error.notFound" {
						return err
					}
					continue
				}
				var cap *int
				if project.Slots != nil {
					if value, ok := project.Slots.(int64); ok {
						n := int(value)
						cap = &n
					} else if value, ok := project.Slots.(float64); ok {
						n := int(value)
						cap = &n
					}
				}
				caps[conversation.ProjectID] = cap
			}
		}
		user := false
		if conversation.Role != "coordinator" {
			user, err = repo.PendingIsUserMessage(scheduler.ctx, conversation.ID)
			if err != nil {
				return err
			}
		}
		scheduler.mu.Lock()
		if scheduler.stopped || scheduler.drainMode != "" || scheduler.ctx.Err() != nil {
			scheduler.mu.Unlock()
			return nil
		}
		if scheduler.running[conversation.ID] != nil || scheduler.held[conversation.ProjectID] > 0 {
			scheduler.mu.Unlock()
			continue
		}
		reserved := false
		if conversation.Role != "coordinator" {
			general, extra, projectUsed := 0, 0, 0
			for _, run := range scheduler.running {
				if run.role == "coordinator" {
					continue
				}
				if run.reserved {
					extra++
				} else {
					general++
					if run.projectID == conversation.ProjectID {
						projectUsed++
					}
				}
			}
			full := caps[conversation.ProjectID] != nil && projectUsed >= *caps[conversation.ProjectID]
			if general >= scheduler.slots || full {
				if extra >= scheduler.reservedSlots || !user {
					scheduler.mu.Unlock()
					continue
				}
				reserved = true
			}
		}
		runCtx, cancel := context.WithCancelCause(scheduler.ctx)
		run := &running{conversationID: conversation.ID, projectID: conversation.ProjectID, role: conversation.Role, startedAt: time.Now().UnixMilli(), reserved: reserved, cancel: cancel, done: make(chan struct{})}
		scheduler.running[conversation.ID] = run
		scheduler.turns.Add(1)
		cb, count := scheduler.broadcastChangeLocked()
		scheduler.mu.Unlock()
		if cb != nil {
			cb(count)
		}
		go scheduler.launch(runCtx, conversation, run)
	}
	return nil
}
func (scheduler *Scheduler) launch(ctx context.Context, conversation core.ConversationView, run *running) {
	defer func() {
		run.cancel(nil)
		scheduler.mu.Lock()
		delete(scheduler.running, conversation.ID)
		close(run.done)
		cb, count := scheduler.broadcastChangeLocked()
		scheduler.mu.Unlock()
		if cb != nil {
			cb(count)
		}
		scheduler.signal()
		scheduler.turns.Done()
	}()
	err := func() (err error) {
		defer func() {
			if cause := recover(); cause != nil {
				err = fmt.Errorf("turn panicked: %v", cause)
			}
		}()
		return scheduler.options.Run(ctx, conversation)
	}()
	if err == nil {
		return
	}
	if ctx.Err() == nil {
		scheduler.report(err)
	}
	// Cancellation still has to be recorded after the invocation's context ends.
	storeCtx := context.WithoutCancel(ctx)
	current, readErr := scheduler.options.Repository.GetConversation(storeCtx, conversation.ID)
	if readErr != nil {
		scheduler.report(readErr)
		return
	}
	outcome := "failed"
	failure := domainerr.New("error.modelRequest", map[string]any{"detail": err.Error()})
	var domain *domainerr.Error
	if errors.As(err, &domain) {
		failure = domain
	}
	if ctx.Err() != nil {
		outcome = "cancelled"
		failure = nil
		if errors.Is(context.Cause(ctx), ErrInterrupted) || scheduler.ctx.Err() != nil {
			outcome = "interrupted"
			failure = domainerr.New("error.interrupted", nil)
		}
	}
	if current.Turn != nil {
		scheduler.report(scheduler.options.Repository.FinishTurn(storeCtx, current.ID, current.Turn.ID, outcome, failure))
	} else if ctx.Err() != nil {
		scheduler.report(scheduler.options.Repository.CancelBeforeTurn(storeCtx, current.ID, outcome == "interrupted"))
	} else {
		scheduler.report(scheduler.options.Repository.FailBeforeTurn(storeCtx, current.ID, failure))
	}
}
func (scheduler *Scheduler) IsRunning(id string) bool {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	return scheduler.running[id] != nil
}
func (scheduler *Scheduler) SetTurnModel(conversationID, model string, stateful bool) {
	scheduler.mu.Lock()
	if run := scheduler.running[conversationID]; run != nil {
		run.model = model
		run.stateful = stateful
	}
	scheduler.mu.Unlock()
}
func (scheduler *Scheduler) BeginDrain(mode string) {
	if mode != DrainTurn {
		mode = DrainSafePoint
	}
	scheduler.mu.Lock()
	scheduler.drainMode = mode
	cb, count := scheduler.broadcastChangeLocked()
	scheduler.mu.Unlock()
	if cb != nil {
		cb(count)
	}
}
func (scheduler *Scheduler) TryDrainIfIdle(mode string) bool {
	if mode != DrainTurn {
		mode = DrainSafePoint
	}
	scheduler.mu.Lock()
	if len(scheduler.running) > 0 {
		scheduler.mu.Unlock()
		return false
	}
	scheduler.drainMode = mode
	cb, count := scheduler.broadcastChangeLocked()
	scheduler.mu.Unlock()
	if cb != nil {
		cb(count)
	}
	return true
}
func (scheduler *Scheduler) CancelDrain() {
	scheduler.mu.Lock()
	was := scheduler.drainMode != ""
	scheduler.drainMode = ""
	var cb func(int)
	var count int
	if was {
		cb, count = scheduler.broadcastChangeLocked()
	}
	scheduler.mu.Unlock()
	if was {
		if cb != nil {
			cb(count)
		}
		scheduler.signal()
	}
}
func (scheduler *Scheduler) DrainMode() string {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	return scheduler.drainMode
}
func (scheduler *Scheduler) IsDraining() bool {
	return scheduler.DrainMode() != ""
}
func (scheduler *Scheduler) ShouldSuspendStep() bool {
	return scheduler.DrainMode() == DrainSafePoint
}
func (scheduler *Scheduler) ActiveCount() int {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	return len(scheduler.running)
}
func (scheduler *Scheduler) Active() []ActiveTurn {
	scheduler.mu.Lock()
	out := make([]ActiveTurn, 0, len(scheduler.running))
	for _, run := range scheduler.running {
		out = append(out, ActiveTurn{
			ConversationID: run.conversationID,
			ProjectID:      run.projectID,
			Role:           run.role,
			Model:          run.model,
			Stateful:       run.stateful,
			StartedAt:      run.startedAt,
		})
	}
	scheduler.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartedAt != out[j].StartedAt {
			return out[i].StartedAt < out[j].StartedAt
		}
		return out[i].ConversationID < out[j].ConversationID
	})
	return out
}
func (scheduler *Scheduler) WaitIdle(ctx context.Context) error {
	for {
		scheduler.mu.Lock()
		if len(scheduler.running) == 0 {
			scheduler.mu.Unlock()
			return nil
		}
		ch := scheduler.changed
		scheduler.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ch:
		}
	}
}
func (scheduler *Scheduler) Cancel(id string) bool {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	if run := scheduler.running[id]; run != nil {
		run.cancel(ErrCancelled)
		return true
	}
	return false
}
func (scheduler *Scheduler) HoldProject(ctx context.Context, id string) (func(), error) {
	scheduler.mu.Lock()
	scheduler.held[id]++
	ending := []<-chan struct{}{}
	for _, run := range scheduler.running {
		if run.projectID == id {
			run.cancel(ErrInterrupted)
			ending = append(ending, run.done)
		}
	}
	scheduler.mu.Unlock()
	var once sync.Once
	release := func() {
		once.Do(func() {
			scheduler.mu.Lock()
			if scheduler.held[id] > 1 {
				scheduler.held[id]--
			} else {
				delete(scheduler.held, id)
			}
			scheduler.mu.Unlock()
			scheduler.signal()
		})
	}
	for _, done := range ending {
		select {
		case <-ctx.Done():
			release()
			return nil, ctx.Err()
		case <-done:
		}
	}
	return release, nil
}
func (scheduler *Scheduler) Stop(ctx context.Context) error {
	scheduler.mu.Lock()
	if !scheduler.started {
		scheduler.stopped = true
		scheduler.mu.Unlock()
		return nil
	}
	scheduler.stopped = true
	scheduler.cancel(ErrInterrupted)
	for _, run := range scheduler.running {
		run.cancel(ErrInterrupted)
	}
	scheduler.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-scheduler.done:
		return nil
	}
}
