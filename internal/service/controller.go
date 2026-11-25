package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
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
	DefaultManualIdleQuietWindow     = 15 * time.Second
	DefaultAutoIdleQuietWindow       = 2 * time.Minute
	defaultIdleQuietWindow           = DefaultManualIdleQuietWindow
	defaultShutdownDelay             = 250 * time.Millisecond
	defaultImmediateSafePointTimeout = 5 * time.Second
	defaultDrainLeaseTTL             = 30 * time.Second
	defaultPollInterval              = 500 * time.Millisecond
)

type ControllerOptions struct {
	Runtime                   RuntimeInfo
	CanRestart                bool
	Scheduler                 *scheduler.Scheduler
	Background                *background.Commands
	Repository                *core.Repository
	Now                       func() time.Time
	IdleQuietWindow           time.Duration
	ShutdownDelay             time.Duration
	ImmediateSafePointTimeout time.Duration
	DrainLeaseTTL             time.Duration
	PollInterval              time.Duration
	OnShutdown                func(restart bool)
	OnStateChange             func(RestartState)
}

// Controller coordinates non-blocking idle-watch restart, immediate restart,
// leased external drain, and controlled shutdown/restart for a running Ariadne server.
type Controller struct {
	mu                        sync.Mutex
	runtime                   RuntimeInfo
	canRestart                bool
	scheduler                 *scheduler.Scheduler
	background                *background.Commands
	repository                *core.Repository
	nowFn                     func() time.Time
	idleQuietWindow           time.Duration
	shutdownDelay             time.Duration
	immediateSafePointTimeout time.Duration
	drainLeaseTTL             time.Duration
	pollInterval              time.Duration
	onShutdown                func(restart bool)
	onStateChange             func(RestartState)

	lastUserActivity atomic.Int64
	workIdleSince    atomic.Int64
	wakeCh           chan struct{}

	draining       bool
	drainMode      string
	drainAction    string
	waitBackground bool
	autoShutdown   bool
	watchGen       uint64
	watchCancel    context.CancelFunc
	leaseTTL       time.Duration
	leaseExpiresAt time.Time
	shuttingDown   bool
}

func NewController(opts ControllerOptions) *Controller {
	nowFn := opts.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	quiet := opts.IdleQuietWindow
	if quiet <= 0 {
		quiet = defaultIdleQuietWindow
	}
	delay := opts.ShutdownDelay
	if delay <= 0 {
		delay = defaultShutdownDelay
	}
	immediateTimeout := opts.ImmediateSafePointTimeout
	if immediateTimeout <= 0 {
		immediateTimeout = defaultImmediateSafePointTimeout
	}
	leaseTTL := opts.DrainLeaseTTL
	if leaseTTL <= 0 {
		leaseTTL = defaultDrainLeaseTTL
	}
	poll := opts.PollInterval
	if poll <= 0 {
		poll = defaultPollInterval
	}
	c := &Controller{
		runtime:                   opts.Runtime,
		canRestart:                opts.CanRestart,
		scheduler:                 opts.Scheduler,
		background:                opts.Background,
		repository:                opts.Repository,
		nowFn:                     nowFn,
		idleQuietWindow:           quiet,
		shutdownDelay:             delay,
		immediateSafePointTimeout: immediateTimeout,
		drainLeaseTTL:             leaseTTL,
		pollInterval:              poll,
		onShutdown:                opts.OnShutdown,
		onStateChange:             opts.OnStateChange,
		wakeCh:                    make(chan struct{}, 1),
	}
	if opts.Scheduler != nil {
		opts.Scheduler.SetOnActiveChange(func(count int) {
			c.emitStateChange(count)
			if count == 0 {
				c.notifyWatcher()
			}
		})
	}
	c.emitStateChange(c.activeTurnCount())
	return c
}

func (c *Controller) now() time.Time {
	return c.nowFn()
}

func (c *Controller) CanRestart() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.canRestart
}

// RecordUserActivity marks recent user write or document editing activity so
// automatic idle-watch restarts wait until the user finishes interacting.
func (c *Controller) RecordUserActivity() {
	c.lastUserActivity.Store(c.now().UnixNano())
}

func (c *Controller) LastUserActivity() time.Time {
	ns := c.lastUserActivity.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

// NotifyBackgroundChange pushes updated background command counts to observers.
func (c *Controller) NotifyBackgroundChange() {
	c.emitStateChange(c.activeTurnCount())
	c.notifyWatcher()
}

func (c *Controller) notifyWatcher() {
	if c.wakeCh == nil {
		return
	}
	select {
	case c.wakeCh <- struct{}{}:
	default:
	}
}

func (c *Controller) activeTurnCount() int {
	if c.scheduler == nil {
		return 0
	}
	return c.scheduler.ActiveCount()
}

func (c *Controller) activeBackgroundCount() int {
	if c.background == nil {
		return 0
	}
	return len(c.background.ListRunning())
}

func (c *Controller) phaseLocked() string {
	if c.shuttingDown {
		return RestartPhaseRestarting
	}
	if !c.draining {
		return RestartPhaseNone
	}
	switch c.drainMode {
	case DrainModeWhenIdle:
		return RestartPhaseWaitingIdle
	case DrainModeNow:
		return RestartPhaseRestarting
	default:
		return RestartPhaseDraining
	}
}

func (c *Controller) RestartState() RestartState {
	activeTurns := c.activeTurnCount()
	activeBg := c.activeBackgroundCount()
	c.mu.Lock()
	state := RestartState{
		CanRestart:       c.canRestart,
		Phase:            c.phaseLocked(),
		ActiveTurns:      activeTurns,
		ActiveBackground: activeBg,
	}
	c.mu.Unlock()
	return state
}

func (c *Controller) emitStateChange(activeTurns int) {
	activeBg := c.activeBackgroundCount()
	c.mu.Lock()
	cb := c.onStateChange
	state := RestartState{
		CanRestart:       c.canRestart,
		Phase:            c.phaseLocked(),
		ActiveTurns:      activeTurns,
		ActiveBackground: activeBg,
	}
	c.mu.Unlock()
	if cb != nil {
		cb(state)
	}
}

func (c *Controller) Snapshot(ctx context.Context) StatusSnapshot {
	c.mu.Lock()
	if c.draining && !c.autoShutdown && c.leaseTTL > 0 {
		c.leaseExpiresAt = c.now().Add(c.leaseTTL)
	}
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
		RestartPhase:     c.phaseLocked(),
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
	case DrainModeSafePoint, DrainModeTurn, DrainModeWhenIdle, DrainModeNow:
	default:
		return StatusSnapshot{}, ErrInvalidDrainReq
	}
	switch req.Action {
	case ActionStop, ActionRestart:
	default:
		return StatusSnapshot{}, ErrInvalidDrainReq
	}
	autoShutdown := req.AutoShutdown || req.Mode == DrainModeWhenIdle || req.Mode == DrainModeNow
	if req.Action == ActionRestart && autoShutdown && !c.CanRestart() {
		return StatusSnapshot{}, ErrCannotRestart
	}

	quietWindow := c.idleQuietWindow
	if req.QuietWindowMs > 0 {
		quietWindow = time.Duration(req.QuietWindowMs) * time.Millisecond
	}
	leaseTTL := c.drainLeaseTTL
	if req.LeaseMs > 0 {
		leaseTTL = time.Duration(req.LeaseMs) * time.Millisecond
	}

	c.mu.Lock()
	if c.shuttingDown {
		c.mu.Unlock()
		return c.Snapshot(ctx), nil
	}
	if c.watchCancel != nil {
		c.watchCancel()
		c.watchCancel = nil
	}
	c.watchGen++
	gen := c.watchGen
	c.draining = true
	c.drainMode = req.Mode
	c.drainAction = req.Action
	c.waitBackground = req.WaitBackground
	c.autoShutdown = autoShutdown
	c.leaseTTL = 0
	c.leaseExpiresAt = time.Time{}

	var watchCtx context.Context
	if c.autoShutdown {
		watchCtx, c.watchCancel = context.WithCancel(context.Background())
	} else if leaseTTL > 0 {
		c.leaseTTL = leaseTTL
		c.leaseExpiresAt = c.now().Add(leaseTTL)
		watchCtx, c.watchCancel = context.WithCancel(context.Background())
	}
	mode := c.drainMode
	action := c.drainAction
	waitBg := c.waitBackground
	sched := c.scheduler
	c.mu.Unlock()

	if sched != nil {
		switch mode {
		case DrainModeSafePoint, DrainModeNow:
			sched.BeginDrain(scheduler.DrainSafePoint)
		case DrainModeTurn:
			sched.BeginDrain(scheduler.DrainTurn)
		case DrainModeWhenIdle:
			// Never freeze scheduling for non-blocking idle-watch.
			sched.CancelDrain()
		}
	}
	c.emitStateChange(c.activeTurnCount())

	if watchCtx != nil {
		if autoShutdown {
			go c.watchAndShutdown(watchCtx, gen, mode, action, waitBg, quietWindow, req.ImmediateIfIdle)
		} else {
			go c.watchDrainLease(watchCtx, gen)
		}
	}
	return c.Snapshot(ctx), nil
}

func (c *Controller) CancelDrain(ctx context.Context) StatusSnapshot {
	c.cancelDrainGen(0)
	return c.Snapshot(ctx)
}

func (c *Controller) cancelDrainGen(expectedGen uint64) bool {
	c.mu.Lock()
	if expectedGen != 0 && c.watchGen != expectedGen {
		c.mu.Unlock()
		return false
	}
	if c.watchCancel != nil {
		c.watchCancel()
		c.watchCancel = nil
	}
	c.watchGen++
	c.draining = false
	c.drainMode = ""
	c.drainAction = ""
	c.waitBackground = false
	c.autoShutdown = false
	c.leaseTTL = 0
	c.leaseExpiresAt = time.Time{}
	sched := c.scheduler
	c.mu.Unlock()

	if sched != nil {
		sched.CancelDrain()
	}
	c.emitStateChange(c.activeTurnCount())
	return true
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

func (c *Controller) markRestarting(gen uint64) bool {
	c.mu.Lock()
	if c.watchGen != gen || c.shuttingDown {
		c.mu.Unlock()
		return false
	}
	c.drainMode = DrainModeNow
	c.mu.Unlock()
	c.emitStateChange(c.activeTurnCount())
	return true
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
	c.emitStateChange(c.activeTurnCount())
	if cb != nil {
		cb(restart)
	}
}

func (c *Controller) watchDrainLease(ctx context.Context, gen uint64) {
	interval := c.pollInterval / 2
	if interval < 10*time.Millisecond {
		interval = 10 * time.Millisecond
	}
	if interval > 500*time.Millisecond {
		interval = 500 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	lastBg := c.activeBackgroundCount()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if bg := c.activeBackgroundCount(); bg != lastBg {
				lastBg = bg
				c.emitStateChange(c.activeTurnCount())
			}
			c.mu.Lock()
			expired := c.watchGen == gen && c.draining && !c.autoShutdown && !c.leaseExpiresAt.IsZero() && !c.now().Before(c.leaseExpiresAt)
			c.mu.Unlock()
			if expired {
				c.cancelDrainGen(gen)
				return
			}
		}
	}
}

func (c *Controller) waitShutdownDelay(ctx context.Context) bool {
	if c.shutdownDelay <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(c.shutdownDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (c *Controller) watchAndShutdown(
	ctx context.Context,
	gen uint64,
	mode, action string,
	waitBackground bool,
	quietWindow time.Duration,
	immediateIfIdle bool,
) {
	restart := action == ActionRestart

	if mode == DrainModeNow {
		if c.scheduler != nil {
			active := c.scheduler.Active()
			if len(active) > 0 {
				hasStateful := false
				for _, item := range active {
					if item.Stateful {
						hasStateful = true
						break
					}
				}
				// Stateless HTTP sessions can finish their current tool step cleanly within a short cap.
				// Stateful local CLI sessions skip draining and resume via interrupted recovery after restart.
				if !hasStateful {
					shortCtx, cancel := context.WithTimeout(ctx, c.immediateSafePointTimeout)
					_ = c.scheduler.WaitIdle(shortCtx)
					cancel()
					if ctx.Err() != nil {
						return
					}
				}
			}
		}
		if !c.waitShutdownDelay(ctx) {
			return
		}
		c.triggerShutdown(restart)
		return
	}

	if mode == DrainModeWhenIdle {
		tryLockAndRestart := func(checkUserActivity bool) bool {
			if c.scheduler != nil && !c.scheduler.TryDrainIfIdle(scheduler.DrainSafePoint) {
				return false
			}
			if !c.isWorkIdle(ctx, waitBackground) {
				if c.scheduler != nil {
					c.scheduler.CancelDrain()
				}
				return false
			}
			if checkUserActivity {
				if lastAct := c.LastUserActivity(); !lastAct.IsZero() && c.now().Sub(lastAct) < quietWindow {
					if c.scheduler != nil {
						c.scheduler.CancelDrain()
					}
					return false
				}
			}
			if !c.markRestarting(gen) {
				if c.scheduler != nil {
					c.scheduler.CancelDrain()
				}
				return false
			}
			if !c.waitShutdownDelay(ctx) {
				if c.scheduler != nil {
					c.scheduler.CancelDrain()
				}
				return false
			}
			c.triggerShutdown(restart)
			return true
		}

		if immediateIfIdle && c.isWorkIdle(ctx, waitBackground) {
			if tryLockAndRestart(false) {
				return
			}
		}

		poll := c.pollInterval
		if quietWindow > 0 && quietWindow/2 < poll {
			poll = max(quietWindow/2, 5*time.Millisecond)
		}
		ticker := time.NewTicker(poll)
		defer ticker.Stop()
		defer c.workIdleSince.Store(0)
		c.workIdleSince.Store(0)
		var workIdleSince time.Time
		lastBg := c.activeBackgroundCount()
		for {
			if bg := c.activeBackgroundCount(); bg != lastBg {
				lastBg = bg
				c.emitStateChange(c.activeTurnCount())
			}
			if c.isWorkIdle(ctx, waitBackground) {
				now := c.now()
				if workIdleSince.IsZero() {
					workIdleSince = now
					c.workIdleSince.Store(now.UnixNano())
				}
				effectiveStart := workIdleSince
				if lastAct := c.LastUserActivity(); !lastAct.IsZero() && lastAct.After(effectiveStart) {
					effectiveStart = lastAct
				}
				if now.Sub(effectiveStart) >= quietWindow {
					if tryLockAndRestart(true) {
						return
					}
					workIdleSince = time.Time{}
					c.workIdleSince.Store(0)
				}
			} else {
				workIdleSince = time.Time{}
				c.workIdleSince.Store(0)
			}
			select {
			case <-ctx.Done():
				return
			case <-c.wakeCh:
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
		ticker := time.NewTicker(c.pollInterval)
		defer ticker.Stop()
		lastBg := c.activeBackgroundCount()
		for len(c.background.ListRunning()) > 0 {
			if bg := c.activeBackgroundCount(); bg != lastBg {
				lastBg = bg
				c.emitStateChange(c.activeTurnCount())
			}
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
	if !c.markRestarting(gen) {
		return
	}
	if !c.waitShutdownDelay(ctx) {
		return
	}
	c.triggerShutdown(restart)
}

func (c *Controller) isWorkIdle(ctx context.Context, waitBackground bool) bool {
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
