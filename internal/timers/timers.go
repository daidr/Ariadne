// Package timers wakes due conversations and routines using durable schedules.
// One timer and two cursors replace a growing map of per-routine timestamps.
package timers

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/projectsettings"
)

const RoutineGrace = 5 * time.Minute

type Options struct {
	Repository *core.Repository
	Now        func() time.Time
	Location   *time.Location
	OnError    func(error)
}
type Timers struct {
	options Options
	mu      sync.Mutex
	serial  chan struct{}
	life    context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	from    time.Time
	seq     int64
}

func New(options Options) (*Timers, error) {
	if options.Repository == nil {
		return nil, errors.New("timers repository is required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &Timers{options: options, serial: make(chan struct{}, 1)}, nil
}
func (timers *Timers) report(err error) {
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	if timers.options.OnError != nil {
		func() { defer func() { recover() }(); timers.options.OnError(err) }()
	}
}
func (timers *Timers) Start(ctx context.Context) error {
	timers.mu.Lock()
	defer timers.mu.Unlock()
	if timers.cancel != nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	watch, unwatch := timers.options.Repository.Store.Watch()
	timers.life, timers.cancel = context.WithCancel(ctx)
	timers.from = timers.options.Now()
	timers.seq = 0
	timers.done = make(chan struct{})
	life, done := timers.life, timers.done
	go func() {
		defer close(done)
		defer unwatch()
		for life.Err() == nil {
			next, err := timers.Check(life)
			timers.report(err)
			delay := time.Hour
			if next != nil {
				delay = min(delay, max(time.Duration(0), next.Sub(timers.options.Now())))
			}
			if err != nil {
				delay = time.Second
			}
			timer := time.NewTimer(delay)
			select {
			case <-life.Done():
				timer.Stop()
				return
			case _, ok := <-watch:
				timer.Stop()
				if !ok {
					return
				}
			case <-timer.C:
			}
		}
	}()
	return nil
}
func (timers *Timers) Stop(ctx context.Context) error {
	timers.mu.Lock()
	cancel, done := timers.cancel, timers.done
	if cancel != nil {
		cancel()
	}
	timers.mu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case timers.serial <- struct{}{}:
	}
	defer func() { <-timers.serial }()
	timers.mu.Lock()
	if timers.done == done {
		timers.life = nil
		timers.cancel = nil
		timers.done = nil
		timers.from = time.Time{}
		timers.seq = 0
	}
	timers.mu.Unlock()
	return nil
}

// Check is serialized with automatic wakeups. It is also useful for clock-driven
// tests. A restart starts routines from now; overdue waits still wake immediately.
func (timers *Timers) Check(ctx context.Context) (*time.Time, error) {
	timers.mu.Lock()
	life := timers.life
	timers.mu.Unlock()
	if life == nil {
		return nil, nil
	}
	if err := life.Err(); err != nil {
		return nil, err
	}
	linked, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(life, cancel)
	defer stop()
	defer cancel()
	select {
	case <-linked.Done():
		return nil, linked.Err()
	case timers.serial <- struct{}{}:
	}
	defer func() { <-timers.serial }()
	if err := linked.Err(); err != nil {
		return nil, err
	}
	state, err := timers.options.Repository.ReadTimers(linked)
	if err != nil {
		return nil, err
	}
	timers.mu.Lock()
	from, seq := timers.from, timers.seq
	timers.mu.Unlock()
	now := timers.options.Now()
	var next *time.Time
	later := func(at time.Time) {
		if next == nil || at.Before(*next) {
			next = new(at)
		}
	}
	for _, wait := range state.Waits {
		if wait.Until <= now.UnixMilli() {
			_, err := timers.options.Repository.EndWait(linked, wait.ConversationID, "due", "time", &wait.WaitID)
			timers.report(err)
		} else {
			later(time.UnixMilli(wait.Until))
		}
	}
	for _, aside := range state.Asides {
		if aside.Until <= now.UnixMilli() {
			timers.report(timers.options.Repository.AsideDueIfCurrent(linked, aside.ThreadID, aside.Until))
		} else {
			later(time.UnixMilli(aside.Until))
		}
	}
	for _, project := range state.Projects {
		start := from
		if project.SettingsSeq > seq {
			start = now
		}
		for _, routine := range project.Routines {
			due := projectsettings.NextRun(routine, start, timers.options.Location)
			if due == nil {
				continue
			}
			if !due.After(now) {
				if now.Sub(*due) <= RoutineGrace {
					_, err := timers.options.Repository.RunRoutineIfCurrent(linked, project.ID, routine.ID, due.UnixMilli(), project.SettingsSeq)
					timers.report(err)
				}
				due = projectsettings.NextRun(routine, now, timers.options.Location)
			}
			if due != nil {
				later(*due)
			}
		}
	}
	if err := linked.Err(); err != nil {
		return nil, err
	}
	timers.mu.Lock()
	if timers.life != life {
		timers.mu.Unlock()
		return nil, context.Canceled
	}
	timers.from = now
	timers.seq = state.Seq
	timers.mu.Unlock()
	return next, nil
}
