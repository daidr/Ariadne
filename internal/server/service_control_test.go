package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/daidr/Ariadne/internal/core"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
	"github.com/daidr/Ariadne/internal/scheduler"
	"github.com/daidr/Ariadne/internal/service"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/internal/update"
)

func TestServiceControlEndpointsAndClient(t *testing.T) {
	t.Parallel()
	repo, err := core.Open(t.Context(), core.Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	started := make(chan struct{}, 2)
	release := make(chan struct{})
	slots, reserved := 1, 0
	sched, err := scheduler.New(scheduler.Options{
		Repository:    repo,
		Slots:         &slots,
		ReservedSlots: &reserved,
		Run: func(ctx context.Context, conv core.ConversationView) error {
			turn, err := repo.StartTurn(ctx, conv.ID, "mock")
			if err != nil {
				return err
			}
			started <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
			return repo.FinishTurn(ctx, conv.ID, turn, "cancelled", nil)
		},
		OnError: func(err error) { t.Error(err) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := sched.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = sched.Stop(ctx)
	}()

	var shutdownCalls atomic.Int32
	var shutdownRestart atomic.Bool
	ctrl := service.NewController(service.ControllerOptions{
		Runtime: service.RuntimeInfo{
			PID:       12345,
			Version:   "0.2.0",
			Host:      "127.0.0.1",
			Port:      7428,
			StartedAt: 1700000000,
			ManagedBy: service.ManagedSystemd,
		},
		CanRestart: true,
		Scheduler:  sched,
		Repository: repo,
		OnShutdown: func(restart bool) {
			shutdownRestart.Store(restart)
			shutdownCalls.Add(1)
		},
	})
	defer ctrl.Close()

	api, err := New(Options{
		Repository:     repo,
		Token:          "secret-token",
		ServiceControl: ctrl,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()

	srv := httptest.NewServer(api)
	defer srv.Close()

	// Unauthenticated control requests must be rejected.
	resp, err := http.Get(srv.URL + "/api/service/status")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", resp.StatusCode)
	}

	client := service.NewClient(srv.URL, "secret-token")
	client.HTTP = srv.Client()

	if err := client.Health(t.Context()); err != nil {
		t.Fatalf("Health: %v", err)
	}

	snap, err := client.Status(t.Context())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if snap.PID != 12345 || snap.Version != "0.2.0" || snap.Draining {
		t.Fatalf("unexpected initial status: %+v", snap)
	}

	// Start an active turn so we can test drain & busy shutdown.
	name := "SvcProject"
	proj, err := repo.CreateProject(t.Context(), &name, core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PostMessage(t.Context(), proj.ID, core.MessageInput{
		ConversationID: proj.CoordinatorID,
		Author:         "user",
		Content:        "work",
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("turn did not start")
	}

	// Non-force shutdown must fail while busy.
	if err := client.Shutdown(t.Context(), service.ShutdownRequest{Restart: false, Force: false}); err == nil {
		t.Fatal("expected busy shutdown to fail without force")
	}

	// Begin safe-point drain (without autoShutdown so we control shutdown explicitly).
	drained, err := client.BeginDrain(t.Context(), service.DrainRequest{
		Mode:   service.DrainModeSafePoint,
		Action: service.ActionStop,
	})
	if err != nil {
		t.Fatalf("BeginDrain: %v", err)
	}
	if !drained.Draining || len(drained.ActiveTurns) != 1 {
		t.Fatalf("unexpected drain snapshot: %+v", drained)
	}

	// Cancel drain works.
	cancelled, err := client.CancelDrain(t.Context())
	if err != nil {
		t.Fatalf("CancelDrain: %v", err)
	}
	if cancelled.Draining {
		t.Fatalf("expected draining=false after cancel: %+v", cancelled)
	}

	// Release the active turn and wait until idle, then request clean shutdown.
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		current, err := client.Status(t.Context())
		if err == nil && current.ReadyToStop(false) {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err := client.Shutdown(t.Context(), service.ShutdownRequest{Restart: true}); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if shutdownCalls.Load() != 1 || !shutdownRestart.Load() {
		t.Fatalf("expected 1 shutdown with restart=true, got calls=%d restart=%v", shutdownCalls.Load(), shutdownRestart.Load())
	}
}

func TestUpdateServiceRestartEndpoints(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	repo, err := core.Open(t.Context(), core.Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	store, err := settings.Open(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	updates := update.New(t.Context(), store, update.Options{Version: "0.1.0"})
	defer updates.Close(context.Background())

	ctrl := service.NewController(service.ControllerOptions{
		Runtime:    service.RuntimeInfo{PID: 42, Version: "0.1.0", Host: "127.0.0.1", Port: 7428, ManagedBy: service.ManagedDirect},
		CanRestart: false,
		OnStateChange: func(canRestart, restarting bool, activeTurns int) {
			updates.SetRestartState(canRestart, restarting, activeTurns)
		},
	})
	defer ctrl.Close()

	api, err := New(Options{
		Repository:     repo,
		Token:          "test-token",
		Settings:       store,
		Updates:        updates,
		ServiceControl: ctrl,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()

	srv := httptest.NewServer(api)
	defer srv.Close()
	client := ariadnev1connect.NewUpdateServiceClient(
		srv.Client(),
		srv.URL+rpcPrefix,
		connect.WithInterceptors(bearerClientInterceptor("test-token")),
	)

	// Cannot restart when CanRestart=false.
	if _, err := client.RestartToApplyUpdate(t.Context(), connect.NewRequest(&ariadnev1.RestartToApplyUpdateRequest{})); err == nil {
		t.Fatal("expected RestartToApplyUpdate to fail when CanRestart=false")
	} else if _, localized := extractLocalizedError(t, err); localized == nil || localized.Key != "updates.error.cannotRestart" {
		t.Fatalf("expected updates.error.cannotRestart, got %v (localized=%v)", err, localized)
	}

	// CancelRestart succeeds idempotently and returns current status.
	cancelRes, err := client.CancelRestart(t.Context(), connect.NewRequest(&ariadnev1.CancelRestartRequest{}))
	if err != nil {
		t.Fatalf("CancelRestart: %v", err)
	}
	if cancelRes.Msg.GetStatus().GetRestarting() || cancelRes.Msg.GetStatus().GetCanRestart() {
		t.Fatalf("unexpected status after CancelRestart: %+v", cancelRes.Msg.GetStatus())
	}
}
