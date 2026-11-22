package service

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/daidr/Ariadne/internal/background"
	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/scheduler"
)

var (
	ErrBusy            = errors.New("active sessions are still running")
	ErrCannotRestart   = errors.New("this server instance cannot self-restart")
	ErrInvalidDrainReq = errors.New("invalid drain mode or action")
)

const (
	defaultIdleQuietWindow = 3 * time.Second
	defaultShutdownDelay   = 250 * time.Millisecond
)

type ControllerOptions struct {
	Runtime         RuntimeInfo
	CanRestart      bool
	Scheduler       *scheduler.Scheduler
	Background      *background.Commands
	Repository      *core.Repository
	IdleQuietWindow time.Duration
	ShutdownDelay   time.Duration
	OnShutdown      func(restart bool)
	OnStateChange   func(canRestart, restarting bool, activeTurns int)
}

// Controller coordinates graceful drain, safe-point handoff, when-idle scheduling,
// and controlled shutdown/restart for a running Ariadne server.
type Controller struct {
	mu              sync.Mutex
	runtime         RuntimeInfo
	canRestart      bool
	scheduler       *scheduler.Scheduler
	background      *background.Commands
	repository      *core.Repository
	idleQuietWindow time.Duration
	shutdownDelay   time.Duration
	onShutdown      func(restart bool)
	onStateChange   func(canRestart, restarting bool, activeTurns int)

	draining       bool
	drainMode      string
	drainAction    string
	waitBackground bool
	autoShutdown   bool
	watchCancel    context.CancelFunc
	shuttingDown   bool
}

func NewController(opts ControllerOptions) *Controller {
	quiet := opts.IdleQuietWindow
	if quiet <= 0 {
		quiet = defaultIdleQuietWindow
	}
	delay := opts.ShutdownDelay
	if delay <= 0 {
		delay = defaultShutdownDelay
	}
	c := &Controller{
		runtime:         opts.Runtime,
		canRestart:      opts.CanRestart,
		scheduler:       opts.Scheduler,
		background:      opts.Background,
		repository:      opts.Repository,
		idleQuietWindow: quiet,
		shutdownDelay:   delay,
		onShutdown:      opts.OnShutdown,
		onStateChange:   opts.OnStateChange,
	}
	if opts.Scheduler != nil {
		opts.Scheduler.SetOnActiveChange(func(count int) {
			c.emitStateChange(count)
		})
	}
	c.emitStateChange(c.activeTurnCount())
	return c
}

func (c *Controller) CanRestart() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.canRestart
}

func (c *Controller) activeTurnCount() int {
	if c.scheduler == nil {
		return 0
	}
	return c.scheduler.ActiveCount()
}

func (c *Controller) emitStateChange(activeTurns int) {
	c.mu.Lock()
	cb := c.onStateChange
	canRestart := c.canRestart
	restarting := c.draining && c.drainAction == ActionRestart && c.autoShutdown
	c.mu.Unlock()
	if cb != nil {
		cb(canRestart, restarting, activeTurns)
	}
}

func (c *Controller) Snapshot(ctx context.Context) StatusSnapshot {
	c.mu.Lock()
	snap := StatusSnapshot{
		PID:              c.runtime.PID,
		Version:          c.runtime.Version,
		Host:             c.runtime.Host,
		Port:             c.runtime.Port,
		StartedAt:        c.runtime.StartedAt,
		ManagedBy:        c.runtime.ManagedBy,
		Executable:       c.runtime.Executable,
		Draining:         c.draining,
		DrainMode:        c.drainMode,
		DrainAction:      c.drainAction,
		WaitBackground:   c.waitBackground,
		AutoShutdown:     c.autoShutdown,
		ActiveTurns:      []ActiveTurn{},
		ActiveBackground: []ActiveBackground{},
	}
	sched := c.scheduler
	bg := c.background
	repo := c.repository
	c.mu.Unlock()

	if sched != nil {
		active := sched.Active()
		projectNames := map[string]string{}
		for _, item := range active {
			entry := ActiveTurn{
				ConversationID: item.ConversationID,
				ProjectID:      item.ProjectID,
				Role:           item.Role,
				Model:          item.Model,
				Stateful:       item.Stateful,
				StartedAt:      item.StartedAt,
			}
			if repo != nil {
				if name, ok := projectNames[item.ProjectID]; ok {
					entry.ProjectName = name
				} else if proj, err := repo.GetProject(ctx, item.ProjectID); err == nil {
					projectNames[item.ProjectID] = proj.Name
					entry.ProjectName = proj.Name
				}
				if item.Role == "thread" {
					if thread, err := repo.GetThread(ctx, item.ConversationID); err == nil {
						entry.Title = thread.Title
					}
				} else {
					entry.Title = "coordinator"
				}
			}
			snap.ActiveTurns = append(snap.ActiveTurns, entry)
		}
	}
	if bg != nil {
		for _, cmd := range bg.ListRunning() {
			snap.ActiveBackground = append(snap.ActiveBackground, ActiveBackground{
				ID:        cmd.ID,
				ThreadID:  cmd.ThreadID,
				Command:   cmd.Command,
				StartedAt: cmd.StartedAt,
			})
		}
	}
	return snap
}

func (c *Controller) BeginDrain(ctx context.Context, req DrainRequest) (StatusSnapshot, error) {
	if req.Mode == "" {
		req.Mode = DrainModeSafePoint
	}
	if req.Action == "" {
		req.Action = ActionRestart
	}
	switch req.Mode {
	case DrainModeSafePoint, DrainModeTurn, DrainModeWhenIdle:
	default:
		return StatusSnapshot{}, ErrInvalidDrainReq
	}
	switch req.Action {
	case ActionStop, ActionRestart:
	default:
		return StatusSnapshot{}, ErrInvalidDrainReq
	}
	autoShutdown := req.AutoShutdown || req.Mode == DrainModeWhenIdle
	if req.Action == ActionRestart && autoShutdown && !c.CanRestart() {
		return StatusSnapshot{}, ErrCannotRestart
	}

	c.mu.Lock()
	if c.watchCancel != nil {
		c.watchCancel()
		c.watchCancel = nil
	}
	c.draining = true
	c.drainMode = req.Mode
	c.drainAction = req.Action
	c.waitBackground = req.WaitBackground
	c.autoShutdown = autoShutdown

	var watchCtx context.Context
	if c.autoShutdown {
		watchCtx, c.watchCancel = context.WithCancel(context.Background())
	}
	mode := c.drainMode
	action := c.drainAction
	waitBg := c.waitBackground
	sched := c.scheduler
	c.mu.Unlock()

	if sched != nil {
		switch mode {
		case DrainModeSafePoint:
			sched.BeginDrain(scheduler.DrainSafePoint)
		case DrainModeTurn:
			sched.BeginDrain(scheduler.DrainTurn)
		case DrainModeWhenIdle:
			// Do not pause scheduling yet; let normal work finish and watch for an idle window.
			sched.CancelDrain()
		}
	}
	c.emitStateChange(c.activeTurnCount())

	if watchCtx != nil {
		go c.watchAndShutdown(watchCtx, mode, action, waitBg)
	}
	return c.Snapshot(ctx), nil
}

func (c *Controller) CancelDrain(ctx context.Context) StatusSnapshot {
	c.mu.Lock()
	if c.watchCancel != nil {
		c.watchCancel()
		c.watchCancel = nil
	}
	c.draining = false
	c.drainMode = ""
	c.drainAction = ""
	c.waitBackground = false
	c.autoShutdown = false
	sched := c.scheduler
	c.mu.Unlock()

	if sched != nil {
		sched.CancelDrain()
	}
	c.emitStateChange(c.activeTurnCount())
	return c.Snapshot(ctx)
}

func (c *Controller) RequestShutdown(req ShutdownRequest) error {
	if !req.Force {
		if c.scheduler != nil && c.scheduler.ActiveCount() > 0 {
			return ErrBusy
		}
	}
	c.triggerShutdown(req.Restart)
	return nil
}

func (c *Controller) Close() {
	c.mu.Lock()
	if c.watchCancel != nil {
		c.watchCancel()
		c.watchCancel = nil
	}
	c.mu.Unlock()
}

func (c *Controller) triggerShutdown(restart bool) {
	c.mu.Lock()
	if c.shuttingDown {
		c.mu.Unlock()
		return
	}
	c.shuttingDown = true
	if c.watchCancel != nil {
		c.watchCancel()
		c.watchCancel = nil
	}
	cb := c.onShutdown
	c.mu.Unlock()
	if cb != nil {
		cb(restart)
	}
}

func (c *Controller) watchAndShutdown(ctx context.Context, mode, action string, waitBackground bool) {
	restart := action == ActionRestart
	if mode == DrainModeWhenIdle {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		var idleSince time.Time
		for {
			if c.isIdle(ctx, waitBackground) {
				if idleSince.IsZero() {
					idleSince = time.Now()
				}
				if time.Since(idleSince) >= c.idleQuietWindow {
					// Atomically lock drain if still idle so no turn can sneak in.
					if c.scheduler == nil || c.scheduler.TryDrainIfIdle(scheduler.DrainSafePoint) {
						if !waitBackground || c.background == nil || len(c.background.ListRunning()) == 0 {
							c.triggerShutdown(restart)
							return
						}
						if c.scheduler != nil {
							c.scheduler.CancelDrain()
						}
					}
					idleSince = time.Time{}
				}
			} else {
				idleSince = time.Time{}
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}

	// Safe-point or turn drain: wait until active turns (and optional background commands) drain to 0.
	if c.scheduler != nil {
		if err := c.scheduler.WaitIdle(ctx); err != nil {
			return
		}
	}
	if waitBackground && c.background != nil {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for len(c.background.ListRunning()) > 0 {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}
	if ctx.Err() != nil {
		return
	}
	// Give the triggering RPC/HTTP response a brief window to flush before cancelling HTTP contexts.
	if c.shutdownDelay > 0 {
		timer := time.NewTimer(c.shutdownDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
	c.triggerShutdown(restart)
}

func (c *Controller) isIdle(ctx context.Context, waitBackground bool) bool {
	if c.scheduler != nil && c.scheduler.ActiveCount() > 0 {
		return false
	}
	if waitBackground && c.background != nil && len(c.background.ListRunning()) > 0 {
		return false
	}
	if c.repository != nil {
		pending, err := c.repository.ListPendingConversations(ctx)
		if err == nil && len(pending) > 0 {
			return false
		}
	}
	return true
}
