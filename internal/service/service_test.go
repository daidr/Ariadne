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

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/scheduler"
)

func TestWriteReadAndLiveRuntime(t *testing.T) {
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
	time.Sleep(150 * time.Millisecond)
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
		time.Sleep(50 * time.Millisecond)
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
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(t.TempDir(), "ctrl.sqlite")})
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
		IdleQuietWindow: 80 * time.Millisecond,
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
	repo, sched := newTestRepoAndScheduler(t, func(ctx context.Context, _ core.ConversationView) error {
		return nil
	})
	shutdownCh := make(chan bool, 1)
	ctrl := NewController(ControllerOptions{
		Runtime:         RuntimeInfo{PID: 300, Version: "0.1.0", Host: "127.0.0.1", Port: 7428, ManagedBy: ManagedSystemd},
		CanRestart:      true,
		Scheduler:       sched,
		Repository:      repo,
		IdleQuietWindow: 100 * time.Millisecond,
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
