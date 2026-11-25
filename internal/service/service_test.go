package service

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/background"
	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/scheduler"
	"github.com/daidr/Ariadne/sdk"
)

func TestWriteReadAndLiveRuntime(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	info := RuntimeInfo{
		PID:        os.Getpid(),
		Version:    "0.2.0",
		Host:       "127.0.0.1",
		Port:       7428,
		StartedAt:  1700000000,
		ManagedBy:  ManagedDirect,
		Executable: "/usr/local/bin/ariadne",
	}

	cleanup, err := WriteRuntime(home, info)
	if err != nil {
		t.Fatalf("WriteRuntime: %v", err)
	}
	read, err := ReadRuntime(home)
	if err != nil || read == nil {
		t.Fatalf("ReadRuntime: %+v, err=%v", read, err)
	}
	if !reflect.DeepEqual(*read, info) {
		t.Fatalf("runtime mismatch: got %+v, want %+v", *read, info)
	}
	live, err := LiveRuntime(home)
	if err != nil || live == nil || live.PID != os.Getpid() {
		t.Fatalf("LiveRuntime for current PID failed: %+v, err=%v", live, err)
	}

	// Overwriting runtime.json with a different PID prevents the old cleanup from deleting it.
	other := info
	other.PID = os.Getpid() + 999999
	if _, err := WriteRuntime(home, other); err != nil {
		t.Fatalf("WriteRuntime overwrite: %v", err)
	}
	cleanup()
	stillThere, err := ReadRuntime(home)
	if err != nil || stillThere == nil || stillThere.PID != other.PID {
		t.Fatalf("old cleanup removed newer runtime file: %+v, err=%v", stillThere, err)
	}

	// Non-existent PID should not be reported as live.
	stale, err := LiveRuntime(home)
	if err != nil || stale != nil {
		t.Fatalf("expected nil LiveRuntime for dead PID, got %+v, err=%v", stale, err)
	}
}

func TestDashboardURLAndBuildServeArgs(t *testing.T) {
	t.Parallel()
	if got := DashboardURL("0.0.0.0", 7428, "tok+1"); got != "http://127.0.0.1:7428/#token=tok%2B1" {
		t.Fatalf("unexpected 0.0.0.0 dashboard URL: %s", got)
	}
	if got := DashboardURL("::", 8080, "abc"); got != "http://[::1]:8080/#token=abc" {
		t.Fatalf("unexpected :: dashboard URL: %s", got)
	}
	if got := (RuntimeInfo{Host: "0.0.0.0", Port: 7428}).BaseURL(); got != "http://127.0.0.1:7428" {
		t.Fatalf("unexpected 0.0.0.0 BaseURL: %s", got)
	}
	if got := (RuntimeInfo{Host: "::", Port: 8080}).BaseURL(); got != "http://[::1]:8080" {
		t.Fatalf("unexpected :: BaseURL: %s", got)
	}

	args := BuildServeArgs(InstallOptions{
		Executable: "/bin/ariadne",
		Home:       "/tmp/home",
		Host:       "0.0.0.0",
		Port:       9000,
	}, ManagedSystemd)
	want := []string{
		"serve", "--no-open", "--managed-by", ManagedSystemd,
		"--home", "/tmp/home",
		"--host", "0.0.0.0",
		"--port", "9000",
		"--log-file", LogPath("/tmp/home"),
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("BuildServeArgs mismatch:\n got: %v\nwant: %v", args, want)
	}
}

func TestRotatingLogAndTailLog(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "logs", "ariadne.log")

	writer, err := openRotatingLog(logPath, 40, 2)
	if err != nil {
		t.Fatalf("openRotatingLog: %v", err)
	}
	for i := 1; i <= 5; i++ {
		if _, err := writer.Write([]byte("line-number-" + string(rune('0'+i)) + "-padding-12345\n")); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if _, err := os.Stat(logPath + ".1"); err != nil {
		t.Fatalf("expected rotated backup .1: %v", err)
	}
	if _, err := os.Stat(logPath + ".2"); err != nil {
		t.Fatalf("expected rotated backup .2: %v", err)
	}

	// Verify TailLog returns the last N lines accurately.
	samplePath := filepath.Join(dir, "sample.log")
	if err := os.WriteFile(samplePath, []byte("a\nb\nc\nd\ne\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := TailLog(context.Background(), samplePath, 2, false, &out); err != nil {
		t.Fatalf("TailLog: %v", err)
	}
	if out.String() != "d\ne\n" {
		t.Fatalf("unexpected tail output: %q", out.String())
	}

	// Verify follow mode streams newly appended lines.
	ctx, cancel := context.WithCancel(context.Background())
	var followOut bytes.Buffer
	var mu sync.Mutex
	syncWriter := &lockedWriter{mu: &mu, buf: &followOut}
	done := make(chan error, 1)
	go func() {
		done <- TailLog(ctx, samplePath, 1, true, syncWriter)
	}()
	time.Sleep(30 * time.Millisecond)
	f, err := os.OpenFile(samplePath, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("f\n")
	_ = f.Close()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := followOut.String()
		mu.Unlock()
		if strings.Contains(got, "f\n") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("TailLog follow returned error: %v", err)
	}
	mu.Lock()
	gotFollow := followOut.String()
	mu.Unlock()
	if !strings.Contains(gotFollow, "e\n") || !strings.Contains(gotFollow, "f\n") {
		t.Fatalf("expected followed lines e and f, got %q", gotFollow)
	}
}

type lockedWriter struct {
	mu  *sync.Mutex
	buf *bytes.Buffer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func newTestRepoAndScheduler(t *testing.T, run func(context.Context, core.ConversationView) error) (*core.Repository, *scheduler.Scheduler) {
	t.Helper()
	repo, err := core.Open(t.Context(), core.Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	slots, reserved := 2, 0
	sched, err := scheduler.New(scheduler.Options{
		Repository:    repo,
		Slots:         &slots,
		ReservedSlots: &reserved,
		Run:           run,
		OnError:       func(err error) { t.Error(err) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := sched.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = sched.Stop(ctx)
	})
	return repo, sched
}

func TestControllerDrainAutoShutdownAndCancel(t *testing.T) {
	t.Parallel()
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var repoRef *core.Repository

	repo, sched := newTestRepoAndScheduler(t, func(ctx context.Context, conv core.ConversationView) error {
		turn, err := repoRef.StartTurn(ctx, conv.ID, "mock-model")
		if err != nil {
			return err
		}
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		return repoRef.FinishTurn(ctx, conv.ID, turn, "cancelled", nil)
	})
	repoRef = repo

	name := "Demo"
	proj, err := repo.CreateProject(t.Context(), &name, core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PostMessage(t.Context(), proj.ID, core.MessageInput{
		ConversationID: proj.CoordinatorID,
		Author:         "user",
		Content:        "hello",
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("coordinator turn did not start")
	}

	// Non-restartable controller rejects auto-shutdown restart (both safe-point and when-idle).
	nonRestartable := NewController(ControllerOptions{
		Runtime:    RuntimeInfo{PID: 100, Version: "0.1.0", Host: "127.0.0.1", Port: 7428, ManagedBy: ManagedDirect},
		CanRestart: false,
		Scheduler:  sched,
		Repository: repo,
	})
	defer nonRestartable.Close()
	if _, err := nonRestartable.BeginDrain(t.Context(), DrainRequest{
		Mode:         DrainModeSafePoint,
		Action:       ActionRestart,
		AutoShutdown: true,
	}); !errors.Is(err, ErrCannotRestart) {
		t.Fatalf("expected ErrCannotRestart for safe-point autoShutdown, got %v", err)
	}
	if _, err := nonRestartable.BeginDrain(t.Context(), DrainRequest{
		Mode:   DrainModeWhenIdle,
		Action: ActionRestart,
	}); !errors.Is(err, ErrCannotRestart) {
		t.Fatalf("expected ErrCannotRestart for when-idle restart, got %v", err)
	}

	// Restartable controller coordinates drain, refuses non-force shutdown while busy,
	// and fires OnShutdown(true) once the active turn finishes.
	shutdownCh := make(chan bool, 1)
	ctrl := NewController(ControllerOptions{
		Runtime:         RuntimeInfo{PID: 200, Version: "0.1.0", Host: "127.0.0.1", Port: 7428, ManagedBy: ManagedLaunchd},
		CanRestart:      true,
		Scheduler:       sched,
		Repository:      repo,
		IdleQuietWindow: 25 * time.Millisecond,
		OnShutdown: func(restart bool) {
			shutdownCh <- restart
		},
	})
	defer ctrl.Close()

	if err := ctrl.RequestShutdown(ShutdownRequest{Restart: true}); !errors.Is(err, ErrBusy) {
		t.Fatalf("expected ErrBusy while turn active, got %v", err)
	}

	snap, err := ctrl.BeginDrain(t.Context(), DrainRequest{
		Mode:         DrainModeSafePoint,
		Action:       ActionRestart,
		AutoShutdown: true,
	})
	if err != nil {
		t.Fatalf("BeginDrain: %v", err)
	}
	if !snap.Draining || snap.DrainMode != DrainModeSafePoint || !snap.AutoShutdown || len(snap.ActiveTurns) != 1 {
		t.Fatalf("unexpected drain snapshot: %+v", snap)
	}
	if snap.ActiveTurns[0].ProjectName != "Demo" || snap.ActiveTurns[0].Title != "coordinator" {
		t.Fatalf("unexpected active turn metadata: %+v", snap.ActiveTurns[0])
	}

	// Cancel and re-arm drain to ensure cancel stops the watcher cleanly.
	cancelled := ctrl.CancelDrain(t.Context())
	if cancelled.Draining || cancelled.AutoShutdown {
		t.Fatalf("expected draining=false after cancel: %+v", cancelled)
	}
	if _, err := ctrl.BeginDrain(t.Context(), DrainRequest{
		Mode:         DrainModeSafePoint,
		Action:       ActionRestart,
		AutoShutdown: true,
	}); err != nil {
		t.Fatalf("re-arm BeginDrain: %v", err)
	}

	close(release)
	select {
	case restart := <-shutdownCh:
		if !restart {
			t.Fatal("expected restart=true callback")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OnShutdown was not triggered after turn completed")
	}
}

func TestControllerWhenIdleTriggersAfterQuietWindow(t *testing.T) {
	t.Parallel()
	repo, sched := newTestRepoAndScheduler(t, func(ctx context.Context, _ core.ConversationView) error {
		return nil
	})
	shutdownCh := make(chan bool, 1)
	ctrl := NewController(ControllerOptions{
		Runtime:         RuntimeInfo{PID: 300, Version: "0.1.0", Host: "127.0.0.1", Port: 7428, ManagedBy: ManagedSystemd},
		CanRestart:      true,
		Scheduler:       sched,
		Repository:      repo,
		IdleQuietWindow: 25 * time.Millisecond,
		OnShutdown: func(restart bool) {
			shutdownCh <- restart
		},
	})
	defer ctrl.Close()

	snap, err := ctrl.BeginDrain(t.Context(), DrainRequest{
		Mode:   DrainModeWhenIdle,
		Action: ActionRestart,
	})
	if err != nil {
		t.Fatalf("BeginDrain when-idle: %v", err)
	}
	if !snap.Draining || snap.DrainMode != DrainModeWhenIdle || !snap.AutoShutdown {
		t.Fatalf("unexpected when-idle snapshot: %+v", snap)
	}

	select {
	case restart := <-shutdownCh:
		if !restart {
			t.Fatal("expected restart=true from when-idle")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("when-idle did not trigger shutdown after quiet window")
	}
}

func TestDetachedManagerLifecycleAndNote(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	note := "headless SSH fallback note"
	mgr := &detachedManager{home: home, note: note}

	info, err := mgr.Info(context.Background())
	if err != nil {
		t.Fatalf("Info before install: %v", err)
	}
	if info.Installed || info.Autostart || info.SupportsAutostart || info.ManagedBy != ManagedDetached || info.Note != note {
		t.Fatalf("unexpected initial detached info: %+v", info)
	}

	if err := mgr.Start(context.Background(), InstallOptions{}); err == nil {
		t.Fatal("expected error when starting without executable")
	}
	if _, err := os.Stat(serviceConfigPath(home)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed Start should not write service.json, stat err=%v", err)
	}
	info, err = mgr.Info(context.Background())
	if err != nil || info.Installed {
		t.Fatalf("expected Installed=false after failed Start, got %+v err=%v", info, err)
	}

	info, err = mgr.Install(context.Background(), InstallOptions{
		Executable: "/usr/local/bin/ariadne",
		Host:       "127.0.0.1",
		Port:       7428,
		Autostart:  true,
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !info.Installed || info.Autostart || info.LogPath != LogPath(home) || info.Note != note {
		t.Fatalf("unexpected installed detached info: %+v", info)
	}
	if stat, err := os.Stat(filepath.Dir(info.LogPath)); err != nil || !stat.IsDir() {
		t.Fatalf("expected log directory to be created: stat=%v err=%v", stat, err)
	}
	cfg, err := readServiceConfig(home)
	if err != nil || cfg == nil || cfg.Home != home {
		t.Fatalf("expected stored service config with defaulted home=%q, got %+v err=%v", home, cfg, err)
	}

	if err := mgr.Uninstall(context.Background()); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	info, err = mgr.Info(context.Background())
	if err != nil || info.Installed {
		t.Fatalf("expected uninstalled after Uninstall, got %+v err=%v", info, err)
	}
}

func TestControllerWhenIdleNonBlockingAndUserActivityReset(t *testing.T) {
	t.Parallel()
	started := make(chan string, 4)
	release := make(chan struct{})
	var repoRef *core.Repository

	repo, sched := newTestRepoAndScheduler(t, func(ctx context.Context, conv core.ConversationView) error {
		turn, err := repoRef.StartTurn(ctx, conv.ID, "mock-model")
		if err != nil {
			return err
		}
		started <- conv.ID
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		return repoRef.FinishTurn(ctx, conv.ID, turn, "cancelled", nil)
	})
	repoRef = repo

	name := "IdleNonBlocking"
	proj, err := repo.CreateProject(t.Context(), &name, core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}

	var nowMu sync.Mutex
	fakeNow := time.Date(2026, 11, 26, 2, 0, 0, 0, time.UTC)
	advance := func(d time.Duration) {
		nowMu.Lock()
		fakeNow = fakeNow.Add(d)
		nowMu.Unlock()
	}
	getNow := func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		return fakeNow
	}

	shutdownCh := make(chan bool, 1)
	ctrl := NewController(ControllerOptions{
		Runtime:         RuntimeInfo{PID: 400, Version: "0.1.0", Host: "127.0.0.1", Port: 7428, ManagedBy: ManagedSystemd},
		CanRestart:      true,
		Scheduler:       sched,
		Repository:      repo,
		Now:             getNow,
		IdleQuietWindow: 100 * time.Millisecond,
		ShutdownDelay:   10 * time.Millisecond,
		PollInterval:    15 * time.Millisecond,
		OnShutdown: func(restart bool) {
			shutdownCh <- restart
		},
	})
	defer ctrl.Close()

	// Start a first turn so server is busy.
	if _, err := repo.PostMessage(t.Context(), proj.ID, core.MessageInput{
		ConversationID: proj.CoordinatorID,
		Author:         "user",
		Content:        "first",
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first turn did not start")
	}

	// Schedule when-idle restart while busy: must NOT freeze scheduler.
	snap, err := ctrl.BeginDrain(t.Context(), DrainRequest{
		Mode:          DrainModeWhenIdle,
		Action:        ActionRestart,
		QuietWindowMs: 100,
	})
	if err != nil {
		t.Fatalf("BeginDrain when-idle: %v", err)
	}
	if snap.RestartPhase != RestartPhaseWaitingIdle || sched.IsDraining() {
		t.Fatalf("expected non-blocking waiting_idle, snap=%+v schedDraining=%v", snap, sched.IsDraining())
	}

	// A new thread posted while waiting_idle MUST still start immediately.
	thr, err := repo.CreateThread(t.Context(), proj.ID, core.NewThread{Title: "New Thread"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PostMessage(t.Context(), proj.ID, core.MessageInput{
		ConversationID: thr.ID,
		Author:         "user",
		Content:        "must not queue",
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-started:
		if id != thr.ID {
			t.Fatalf("expected thread %s to start, got %s", thr.ID, id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("new thread was blocked during waiting_idle")
	}

	// Finish all active turns and wait for the idle watcher to latch workIdleSince before
	// advancing the fake clock, avoiding a scheduling race on slower runners.
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for (sched.ActiveCount() > 0 || ctrl.getWorkIdleSince().IsZero()) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if ctrl.getWorkIdleSince().IsZero() {
		t.Fatal("when-idle watcher did not observe idle state after turns finished")
	}
	advance(60 * time.Millisecond)
	ctrl.RecordUserActivity()
	advance(60 * time.Millisecond)
	select {
	case <-shutdownCh:
		t.Fatal("shutdown fired despite recent user activity")
	case <-time.After(60 * time.Millisecond):
	}

	// Advance past quiet window after last user activity -> should restart.
	advance(120 * time.Millisecond)
	ctrl.notifyWatcher()
	select {
	case restart := <-shutdownCh:
		if !restart {
			t.Fatal("expected restart=true")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("when-idle did not trigger after user activity quiet window elapsed")
	}
}

func TestControllerWhenIdleImmediateIfIdle(t *testing.T) {
	t.Parallel()
	repo, sched := newTestRepoAndScheduler(t, func(ctx context.Context, _ core.ConversationView) error {
		return nil
	})
	shutdownCh := make(chan bool, 1)
	ctrl := NewController(ControllerOptions{
		Runtime:         RuntimeInfo{PID: 401, Version: "0.1.0", Host: "127.0.0.1", Port: 7428, ManagedBy: ManagedLaunchd},
		CanRestart:      true,
		Scheduler:       sched,
		Repository:      repo,
		IdleQuietWindow: 5 * time.Minute,
		ShutdownDelay:   10 * time.Millisecond,
		OnShutdown: func(restart bool) {
			shutdownCh <- restart
		},
	})
	defer ctrl.Close()

	if _, err := ctrl.BeginDrain(t.Context(), DrainRequest{
		Mode:            DrainModeWhenIdle,
		Action:          ActionRestart,
		QuietWindowMs:   int64(5 * time.Minute / time.Millisecond),
		ImmediateIfIdle: true,
	}); err != nil {
		t.Fatalf("BeginDrain immediateIfIdle: %v", err)
	}
	select {
	case restart := <-shutdownCh:
		if !restart {
			t.Fatal("expected restart=true")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected immediate shutdown when already idle")
	}
}

func TestControllerImmediateRestartNowSkipsStatefulWait(t *testing.T) {
	t.Parallel()
	started := make(chan string, 1)
	release := make(chan struct{})
	var repoRef *core.Repository
	var schedRef *scheduler.Scheduler

	repo, sched := newTestRepoAndScheduler(t, func(ctx context.Context, conv core.ConversationView) error {
		turn, err := repoRef.StartTurn(ctx, conv.ID, "claudecode/sonnet")
		if err != nil {
			return err
		}
		schedRef.SetTurnModel(conv.ID, "claudecode/sonnet", true)
		started <- conv.ID
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		return repoRef.FinishTurn(ctx, conv.ID, turn, "interrupted", nil)
	})
	repoRef = repo
	schedRef = sched
	defer close(release)

	name := "StatefulNow"
	proj, err := repo.CreateProject(t.Context(), &name, core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PostMessage(t.Context(), proj.ID, core.MessageInput{
		ConversationID: proj.CoordinatorID,
		Author:         "user",
		Content:        "busy cli",
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("stateful turn did not start")
	}

	shutdownCh := make(chan bool, 1)
	ctrl := NewController(ControllerOptions{
		Runtime:                   RuntimeInfo{PID: 402, Version: "0.1.0", Host: "127.0.0.1", Port: 7428, ManagedBy: ManagedLaunchd},
		CanRestart:                true,
		Scheduler:                 sched,
		Repository:                repo,
		ImmediateSafePointTimeout: 10 * time.Second,
		ShutdownDelay:             10 * time.Millisecond,
		OnShutdown: func(restart bool) {
			shutdownCh <- restart
		},
	})
	defer ctrl.Close()

	begin := time.Now()
	snap, err := ctrl.BeginDrain(t.Context(), DrainRequest{
		Mode:         DrainModeNow,
		Action:       ActionRestart,
		AutoShutdown: true,
	})
	if err != nil {
		t.Fatalf("BeginDrain now: %v", err)
	}
	if snap.RestartPhase != RestartPhaseRestarting {
		t.Fatalf("expected restarting phase, got %+v", snap)
	}
	select {
	case restart := <-shutdownCh:
		if !restart {
			t.Fatal("expected restart=true")
		}
		if elapsed := time.Since(begin); elapsed > 2*time.Second {
			t.Fatalf("stateful immediate restart waited too long: %s", elapsed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("immediate restart with stateful turn did not fire quickly")
	}
}

func TestControllerDrainLeaseExpiryAndRenewal(t *testing.T) {
	t.Parallel()
	repo, sched := newTestRepoAndScheduler(t, func(ctx context.Context, _ core.ConversationView) error {
		return nil
	})
	var nowMu sync.Mutex
	fakeNow := time.Date(2026, 11, 26, 3, 0, 0, 0, time.UTC)
	advance := func(d time.Duration) {
		nowMu.Lock()
		fakeNow = fakeNow.Add(d)
		nowMu.Unlock()
	}
	getNow := func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		return fakeNow
	}

	ctrl := NewController(ControllerOptions{
		Runtime:       RuntimeInfo{PID: 403, Version: "0.1.0", Host: "127.0.0.1", Port: 7428, ManagedBy: ManagedDirect},
		CanRestart:    false,
		Scheduler:     sched,
		Repository:    repo,
		Now:           getNow,
		DrainLeaseTTL: 100 * time.Millisecond,
		PollInterval:  20 * time.Millisecond,
	})
	defer ctrl.Close()

	snap, err := ctrl.BeginDrain(t.Context(), DrainRequest{
		Mode:         DrainModeSafePoint,
		Action:       ActionRestart,
		AutoShutdown: false,
		LeaseMs:      100,
	})
	if err != nil {
		t.Fatalf("BeginDrain lease: %v", err)
	}
	if !snap.Draining || snap.RestartPhase != RestartPhaseDraining || !sched.IsDraining() {
		t.Fatalf("expected active drain, snap=%+v schedDraining=%v", snap, sched.IsDraining())
	}

	// Explicit RenewDrain at t+70ms extends lease to t+170ms.
	advance(70 * time.Millisecond)
	renewed := ctrl.RenewDrain(t.Context())
	if !renewed.Draining {
		t.Fatalf("expected drain active during renewal, got %+v", renewed)
	}
	advance(70 * time.Millisecond) // t+140ms (< t+170ms)
	time.Sleep(40 * time.Millisecond)
	if !sched.IsDraining() {
		t.Fatal("expected drain still active after explicit lease renewal")
	}

	// Read-only Snapshot at t+140ms must NOT extend the lease to t+240ms.
	if cur := ctrl.Snapshot(t.Context()); !cur.Draining {
		t.Fatalf("expected drain still active before renewed TTL, got %+v", cur)
	}
	advance(50 * time.Millisecond) // t+190ms (>= t+170ms, < t+240ms)
	deadline := time.Now().Add(2 * time.Second)
	for sched.IsDraining() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if sched.IsDraining() {
		t.Fatal("expected scheduler drain to auto-cancel after lease expiry despite read-only Snapshot calls")
	}
	if st := ctrl.RestartState(); st.Phase != RestartPhaseNone {
		t.Fatalf("expected phase cleared after lease expiry, got %+v", st)
	}
}

func TestControllerWhenIdleIgnoresKeptBackgroundCommands(t *testing.T) {
	t.Parallel()
	repo, sched := newTestRepoAndScheduler(t, func(ctx context.Context, _ core.ConversationView) error {
		return nil
	})

	bg := background.New(background.Options{})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = bg.Close(ctx)
	})

	keepStarted := make(chan struct{}, 1)
	keepRelease := make(chan struct{})
	if _, err := bg.Start("thr-keep", "vite dev", func(ctx context.Context, _ sdk.ExecOptions) (sdk.ExecResult, error) {
		keepStarted <- struct{}{}
		select {
		case <-keepRelease:
			return sdk.ExecResult{ExitCode: 0}, nil
		case <-ctx.Done():
			return sdk.ExecResult{ExitCode: 130}, nil
		}
	}, background.StartOptions{Local: true, Keep: true}); err != nil {
		t.Fatalf("start keep command: %v", err)
	}
	<-keepStarted

	tempStarted := make(chan struct{}, 1)
	tempRelease := make(chan struct{})
	if _, err := bg.Start("thr-temp", "go test ./...", func(ctx context.Context, _ sdk.ExecOptions) (sdk.ExecResult, error) {
		tempStarted <- struct{}{}
		select {
		case <-tempRelease:
			return sdk.ExecResult{ExitCode: 0}, nil
		case <-ctx.Done():
			return sdk.ExecResult{ExitCode: 130}, nil
		}
	}, background.StartOptions{Local: true, Keep: false}); err != nil {
		t.Fatalf("start non-keep command: %v", err)
	}
	<-tempStarted

	shutdownCh := make(chan bool, 1)
	ctrl := NewController(ControllerOptions{
		Runtime:         RuntimeInfo{PID: 404, Version: "0.1.0", Host: "127.0.0.1", Port: 7428, ManagedBy: ManagedSystemd},
		CanRestart:      true,
		Scheduler:       sched,
		Background:      bg,
		Repository:      repo,
		IdleQuietWindow: 40 * time.Millisecond,
		ShutdownDelay:   10 * time.Millisecond,
		PollInterval:    15 * time.Millisecond,
		OnShutdown: func(restart bool) {
			shutdownCh <- restart
		},
	})
	defer ctrl.Close()

	// Only the non-keep command should count toward active background work.
	if st := ctrl.RestartState(); st.ActiveBackground != 1 {
		t.Fatalf("expected ActiveBackground=1 (excluding Keep:true), got %+v", st)
	}
	if snap := ctrl.Snapshot(t.Context()); snap.ReadyToStop(true) {
		t.Fatalf("expected ReadyToStop(true)=false while non-keep command runs, snap=%+v", snap)
	}

	if _, err := ctrl.BeginDrain(t.Context(), DrainRequest{
		Mode:           DrainModeWhenIdle,
		Action:         ActionRestart,
		WaitBackground: true,
		QuietWindowMs:  40,
	}); err != nil {
		t.Fatalf("BeginDrain when-idle: %v", err)
	}

	// Must wait while non-keep background command is still running.
	select {
	case <-shutdownCh:
		t.Fatal("when-idle fired while non-keep background command was running")
	case <-time.After(100 * time.Millisecond):
	}

	// Finish the non-keep command while Keep:true preview server stays running.
	close(tempRelease)
	deadline := time.Now().Add(3 * time.Second)
	for ctrl.RestartState().ActiveBackground > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	ctrl.NotifyBackgroundChange()

	if st := ctrl.RestartState(); st.ActiveBackground != 0 {
		t.Fatalf("expected ActiveBackground=0 with only Keep:true command running, got %+v", st)
	}
	if snap := ctrl.Snapshot(t.Context()); !snap.ReadyToStop(true) {
		t.Fatalf("expected ReadyToStop(true)=true when only Keep:true command remains, snap=%+v", snap)
	}

	select {
	case restart := <-shutdownCh:
		if !restart {
			t.Fatal("expected restart=true")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("when-idle stayed blocked by Keep:true background command")
	}
	close(keepRelease)
}

func TestControllerConcurrentBeginAndCancelDrainConsistency(t *testing.T) {
	t.Parallel()
	repo, sched := newTestRepoAndScheduler(t, func(ctx context.Context, _ core.ConversationView) error {
		return nil
	})
	ctrl := NewController(ControllerOptions{
		Runtime:       RuntimeInfo{PID: 405, Version: "0.1.0", Host: "127.0.0.1", Port: 7428, ManagedBy: ManagedSystemd},
		CanRestart:    true,
		Scheduler:     sched,
		Repository:    repo,
		DrainLeaseTTL: time.Minute,
		PollInterval:  20 * time.Millisecond,
	})
	defer ctrl.Close()

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for j := range 40 {
				switch (worker + j) % 4 {
				case 0:
					_, _ = ctrl.BeginDrain(t.Context(), DrainRequest{
						Mode:         DrainModeSafePoint,
						Action:       ActionRestart,
						AutoShutdown: false,
						LeaseMs:      60_000,
					})
				case 1:
					_, _ = ctrl.BeginDrain(t.Context(), DrainRequest{
						Mode:         DrainModeTurn,
						Action:       ActionStop,
						AutoShutdown: false,
						LeaseMs:      60_000,
					})
				case 2:
					_, _ = ctrl.BeginDrain(t.Context(), DrainRequest{
						Mode:          DrainModeWhenIdle,
						Action:        ActionRestart,
						QuietWindowMs: 60_000,
					})
				default:
					_ = ctrl.CancelDrain(t.Context())
				}
			}
		}(i)
	}
	wg.Wait()

	// Final CancelDrain must leave both Controller and Scheduler cleanly un-drained.
	snap := ctrl.CancelDrain(t.Context())
	if snap.Draining || snap.RestartPhase != RestartPhaseNone || sched.IsDraining() {
		t.Fatalf("inconsistent state after CancelDrain: snap=%+v schedDraining=%v", snap, sched.IsDraining())
	}

	// Final BeginDrain(safe-point) must leave both Controller and Scheduler draining.
	snap, err := ctrl.BeginDrain(t.Context(), DrainRequest{
		Mode:         DrainModeSafePoint,
		Action:       ActionRestart,
		AutoShutdown: false,
		LeaseMs:      60_000,
	})
	if err != nil {
		t.Fatalf("final BeginDrain: %v", err)
	}
	if !snap.Draining || snap.RestartPhase != RestartPhaseDraining || !sched.IsDraining() {
		t.Fatalf("inconsistent state after BeginDrain: snap=%+v schedDraining=%v", snap, sched.IsDraining())
	}
}

func TestControllerStaleWhenIdleRollbackDoesNotUnlockNewDrain(t *testing.T) {
	t.Parallel()
	repo, sched := newTestRepoAndScheduler(t, func(ctx context.Context, _ core.ConversationView) error {
		return nil
	})
	shutdownCh := make(chan bool, 2)
	ctrl := NewController(ControllerOptions{
		Runtime:         RuntimeInfo{PID: 406, Version: "0.1.0", Host: "127.0.0.1", Port: 7428, ManagedBy: ManagedSystemd},
		CanRestart:      true,
		Scheduler:       sched,
		Repository:      repo,
		IdleQuietWindow: 5 * time.Minute,
		ShutdownDelay:   350 * time.Millisecond,
		DrainLeaseTTL:   time.Minute,
		PollInterval:    20 * time.Millisecond,
		OnShutdown: func(restart bool) {
			shutdownCh <- restart
		},
	})
	defer ctrl.Close()

	// Start immediate-if-idle when-idle restart so gen=1 locks the scheduler and enters ShutdownDelay.
	if _, err := ctrl.BeginDrain(t.Context(), DrainRequest{
		Mode:            DrainModeWhenIdle,
		Action:          ActionRestart,
		ImmediateIfIdle: true,
	}); err != nil {
		t.Fatalf("BeginDrain when-idle: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for ctrl.RestartState().Phase != RestartPhaseRestarting && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if ctrl.RestartState().Phase != RestartPhaseRestarting {
		t.Fatalf("expected RestartPhaseRestarting during shutdown delay, got %+v", ctrl.RestartState())
	}

	// Supersede gen=1 with a new safe-point drain (gen=2) while gen=1 is waiting in ShutdownDelay.
	snap, err := ctrl.BeginDrain(t.Context(), DrainRequest{
		Mode:         DrainModeSafePoint,
		Action:       ActionStop,
		AutoShutdown: false,
		LeaseMs:      60_000,
	})
	if err != nil {
		t.Fatalf("BeginDrain safe-point: %v", err)
	}
	if snap.RestartPhase != RestartPhaseDraining || !sched.IsDraining() {
		t.Fatalf("expected active safe-point drain, snap=%+v schedDraining=%v", snap, sched.IsDraining())
	}

	// Give the cancelled gen=1 watcher time to run its rollback path; it must NOT unlock gen=2's drain or trigger shutdown.
	select {
	case restart := <-shutdownCh:
		t.Fatalf("stale gen=1 triggered shutdown (restart=%v) after being superseded", restart)
	case <-time.After(450 * time.Millisecond):
	}
	if !sched.IsDraining() {
		t.Fatal("stale gen=1 rollback unlocked scheduler owned by newer gen=2 safe-point drain")
	}
	if st := ctrl.RestartState(); st.Phase != RestartPhaseDraining {
		t.Fatalf("expected RestartPhaseDraining still owned by gen=2, got %+v", st)
	}
}

func TestControllerCancelDuringShutdownDelayPreventsShutdown(t *testing.T) {
	t.Parallel()
	repo, sched := newTestRepoAndScheduler(t, func(ctx context.Context, _ core.ConversationView) error {
		return nil
	})
	shutdownCh := make(chan bool, 1)
	ctrl := NewController(ControllerOptions{
		Runtime:         RuntimeInfo{PID: 407, Version: "0.1.0", Host: "127.0.0.1", Port: 7428, ManagedBy: ManagedSystemd},
		CanRestart:      true,
		Scheduler:       sched,
		Repository:      repo,
		ShutdownDelay:   300 * time.Millisecond,
		IdleQuietWindow: 5 * time.Minute,
		OnShutdown: func(restart bool) {
			shutdownCh <- restart
		},
	})
	defer ctrl.Close()

	if _, err := ctrl.BeginDrain(t.Context(), DrainRequest{
		Mode:            DrainModeWhenIdle,
		Action:          ActionRestart,
		ImmediateIfIdle: true,
	}); err != nil {
		t.Fatalf("BeginDrain when-idle: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for ctrl.RestartState().Phase != RestartPhaseRestarting && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if ctrl.RestartState().Phase != RestartPhaseRestarting {
		t.Fatalf("expected RestartPhaseRestarting, got %+v", ctrl.RestartState())
	}

	// Cancel during shutdown delay; shutdown must not fire and scheduler must resume.
	snap := ctrl.CancelDrain(t.Context())
	if snap.Draining || snap.RestartPhase != RestartPhaseNone || sched.IsDraining() {
		t.Fatalf("expected cancelled state, snap=%+v schedDraining=%v", snap, sched.IsDraining())
	}
	// Verify stale generation cannot trigger shutdown even if called directly after cancel.
	if ctrl.triggerShutdownGen(1, true) {
		t.Fatal("expected triggerShutdownGen to reject stale generation")
	}
	select {
	case restart := <-shutdownCh:
		t.Fatalf("cancelled restart still fired shutdown (restart=%v)", restart)
	case <-time.After(350 * time.Millisecond):
	}
}
