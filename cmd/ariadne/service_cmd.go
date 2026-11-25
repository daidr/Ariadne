package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/daidr/Ariadne/internal/config"
	"github.com/daidr/Ariadne/internal/service"
)

type drainControlOptions struct {
	Mode           string
	WhenIdle       bool
	WaitBackground bool
	Timeout        time.Duration
	Force          bool
}

func resolveExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return exe, nil
}

func discoverRunning(ctx context.Context, cfg config.Config) (*service.Client, *service.StatusSnapshot, *service.RuntimeInfo) {
	if rt, err := service.LiveRuntime(cfg.Home); err == nil && rt != nil {
		client := service.NewClient(rt.BaseURL(), cfg.Token)
		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		snap, statusErr := client.Status(probeCtx)
		cancel()
		if statusErr == nil && snap != nil {
			return client, snap, rt
		}
		return client, nil, rt
	}
	client := service.NewClient("http://"+cfg.Address(), cfg.Token)
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	snap, err := client.Status(probeCtx)
	cancel()
	if err == nil && snap != nil {
		return client, snap, nil
	}
	return nil, nil, nil
}

func waitForProcessExit(ctx context.Context, pid int, timeout time.Duration) bool {
	if pid <= 0 {
		return true
	}
	deadline := time.Now().Add(timeout)
	for {
		if !service.ProcessAlive(pid) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return !service.ProcessAlive(pid)
		case <-time.After(150 * time.Millisecond):
		}
	}
}

func waitForServerReady(ctx context.Context, cfg config.Config, avoidPID int, timeout time.Duration) (*service.StatusSnapshot, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		_, snap, _ := discoverRunning(ctx, cfg)
		if snap != nil && snap.PID > 0 && (avoidPID == 0 || snap.PID != avoidPID) {
			return snap, nil
		}
		if time.Now().After(deadline) {
			if lastErr != nil {
				return nil, fmt.Errorf("timed out waiting for Ariadne to start: %w", lastErr)
			}
			return nil, errors.New("timed out waiting for Ariadne to start (check logs with: ariadne service logs)")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func formatSessionLine(t service.ActiveTurn) string {
	kind := "HTTP"
	if t.Stateful {
		kind = "Agent CLI"
	}
	proj := t.ProjectName
	if proj == "" {
		proj = t.ProjectID
	}
	title := t.Title
	if title == "" {
		title = t.ConversationID
	}
	if t.Model != "" {
		return fmt.Sprintf("  - [%s] %s / %s (%s)", kind, proj, title, t.Model)
	}
	return fmt.Sprintf("  - [%s] %s / %s", kind, proj, title)
}

func printActiveWork(w io.Writer, snap *service.StatusSnapshot) {
	if snap == nil {
		return
	}
	if len(snap.ActiveTurns) > 0 {
		fmt.Fprintf(w, "Active AI sessions (%d):\n", len(snap.ActiveTurns))
		for _, t := range snap.ActiveTurns {
			fmt.Fprintln(w, formatSessionLine(t))
		}
	}
	if len(snap.ActiveBackground) > 0 {
		fmt.Fprintf(w, "Active background commands (%d):\n", len(snap.ActiveBackground))
		for _, bg := range snap.ActiveBackground {
			cmd := strings.TrimSpace(bg.Command)
			if len(cmd) > 80 {
				cmd = cmd[:77] + "..."
			}
			fmt.Fprintf(w, "  - [%s] %s\n", bg.ThreadID, cmd)
		}
	}
}

func cmdService(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printServiceHelp(stdout)
		return nil
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "-h", "--help", "help":
		printServiceHelp(stdout)
		return nil
	case "install":
		return cmdServiceInstall(ctx, rest, stdout, stderr)
	case "uninstall":
		return cmdServiceUninstall(ctx, rest, stdout, stderr)
	case "start":
		return cmdServiceStart(ctx, rest, stdout, stderr)
	case "stop":
		return cmdServiceStop(ctx, rest, stdout, stderr)
	case "restart":
		return cmdServiceRestart(ctx, rest, stdout, stderr)
	case "status":
		return cmdServiceStatus(ctx, rest, stdout, stderr)
	case "logs":
		return cmdServiceLogs(ctx, rest, stdout, stderr)
	default:
		return fmt.Errorf("unknown service subcommand %q (run 'ariadne service --help')", sub)
	}
}

func cmdServiceInstall(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("service install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printServiceHelp(stderr) }

	var (
		home        string
		host        string
		port        int
		autostart   bool
		noAutostart bool
		startNow    bool
		showHelp    bool
	)
	fs.StringVar(&home, "home", "", "Ariadne data directory")
	fs.StringVar(&host, "host", "", "Address to bind")
	fs.IntVar(&port, "port", 0, "Port to listen on")
	fs.BoolVar(&autostart, "autostart", true, "Enable start at login")
	fs.BoolVar(&noAutostart, "no-autostart", false, "Disable start at login")
	fs.BoolVar(&startNow, "start", false, "Start the service immediately after installing")
	fs.BoolVar(&showHelp, "help", false, "Show help")
	fs.BoolVar(&showHelp, "h", false, "Show help")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if showHelp {
		printServiceHelp(stdout)
		return nil
	}
	if noAutostart {
		autostart = false
	}

	cfg, err := loadConfigWithOverrides(home, host, port, false, true, false)
	if err != nil {
		return err
	}
	exe, err := resolveExecutable()
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}

	mgr := service.NewManager(cfg.Home)
	opts := service.InstallOptions{
		Executable: exe,
		Home:       cfg.Home,
		Host:       cfg.Host,
		Port:       cfg.Port,
		Autostart:  autostart,
		LogPath:    service.LogPath(cfg.Home),
	}
	info, err := mgr.Install(ctx, opts)
	if err != nil {
		return fmt.Errorf("install Ariadne service: %w", err)
	}

	fmt.Fprintf(stdout, "Installed Ariadne service (%s)\n", info.ManagedBy)
	if info.ConfigPath != "" {
		fmt.Fprintf(stdout, "  Config:    %s\n", info.ConfigPath)
	}
	fmt.Fprintf(stdout, "  Log file:  %s\n", info.LogPath)
	fmt.Fprintf(stdout, "  Autostart: %v\n", info.Autostart)
	if info.Note != "" {
		fmt.Fprintf(stdout, "  Note:      %s\n", info.Note)
	}

	if startNow {
		return startServiceIfStopped(ctx, cfg, mgr, opts, stdout)
	}
	return nil
}

func cmdServiceUninstall(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("service uninstall", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printServiceHelp(stderr) }

	var (
		home     string
		force    bool
		timeout  time.Duration
		showHelp bool
	)
	fs.StringVar(&home, "home", "", "Ariadne data directory")
	fs.BoolVar(&force, "force", false, "Force stop without waiting for active sessions")
	fs.DurationVar(&timeout, "timeout", 5*time.Minute, "Max wait for active sessions")
	fs.BoolVar(&showHelp, "help", false, "Show help")
	fs.BoolVar(&showHelp, "h", false, "Show help")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if showHelp {
		printServiceHelp(stdout)
		return nil
	}

	cfg, err := loadConfigWithOverrides(home, "", 0, false, true, false)
	if err != nil {
		return err
	}
	mgr := service.NewManager(cfg.Home)

	if _, snap, rt := discoverRunning(ctx, cfg); snap != nil || rt != nil {
		if err := stopRunningService(ctx, cfg, mgr, drainControlOptions{
			Mode:    service.DrainModeSafePoint,
			Timeout: timeout,
			Force:   force,
		}, stdout, stderr); err != nil {
			return err
		}
	}

	if err := mgr.Uninstall(ctx); err != nil {
		return fmt.Errorf("uninstall Ariadne service: %w", err)
	}
	fmt.Fprintln(stdout, "Uninstalled Ariadne background service.")
	return nil
}

func startServiceIfStopped(ctx context.Context, cfg config.Config, mgr service.Manager, opts service.InstallOptions, stdout io.Writer) error {
	if _, snap, _ := discoverRunning(ctx, cfg); snap != nil {
		url := service.DashboardURL(snap.Host, snap.Port, cfg.Token)
		fmt.Fprintf(stdout, "Ariadne is already running (PID %d, %s)\n", snap.PID, snap.Version)
		fmt.Fprintf(stdout, "Dashboard: %s\n", url)
		return nil
	}
	if err := mgr.Start(ctx, opts); err != nil {
		return fmt.Errorf("start Ariadne service: %w", err)
	}
	snap, err := waitForServerReady(ctx, cfg, 0, 15*time.Second)
	if err != nil {
		return err
	}
	url := service.DashboardURL(snap.Host, snap.Port, cfg.Token)
	fmt.Fprintf(stdout, "Ariadne started (PID %d, %s, managed by %s)\n", snap.PID, snap.Version, snap.ManagedBy)
	fmt.Fprintf(stdout, "Dashboard: %s\n", url)
	return nil
}

func buildStartOptions(exe string, cfg config.Config, explicitHost string, explicitPort int) service.InstallOptions {
	opts := service.InstallOptions{
		Executable: exe,
		Home:       cfg.Home,
		Autostart:  true,
	}
	if explicitHost != "" || os.Getenv("ARIADNE_HOST") != "" {
		opts.Host = cfg.Host
	}
	if explicitPort > 0 || os.Getenv("ARIADNE_PORT") != "" {
		opts.Port = cfg.Port
	}
	return opts
}

func cmdServiceStart(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("service start", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printServiceHelp(stderr) }

	var (
		home     string
		host     string
		port     int
		openFlag bool
		showHelp bool
	)
	fs.StringVar(&home, "home", "", "Ariadne data directory")
	fs.StringVar(&host, "host", "", "Address to bind")
	fs.IntVar(&port, "port", 0, "Port to listen on")
	fs.BoolVar(&openFlag, "open", false, "Open the dashboard in the default browser")
	fs.BoolVar(&showHelp, "help", false, "Show help")
	fs.BoolVar(&showHelp, "h", false, "Show help")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if showHelp {
		printServiceHelp(stdout)
		return nil
	}

	cfg, err := loadConfigWithOverrides(home, host, port, false, true, false)
	if err != nil {
		return err
	}
	exe, err := resolveExecutable()
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}
	mgr := service.NewManager(cfg.Home)
	opts := buildStartOptions(exe, cfg, host, port)
	if err := startServiceIfStopped(ctx, cfg, mgr, opts, stdout); err != nil {
		return err
	}
	if openFlag {
		_, snap, _ := discoverRunning(ctx, cfg)
		h, p := cfg.Host, cfg.Port
		if snap != nil {
			h, p = snap.Host, snap.Port
		}
		openBrowser(service.DashboardURL(h, p, cfg.Token))
	}
	return nil
}

func drainBeforeShutdown(ctx context.Context, client *service.Client, action string, opts drainControlOptions, stdout, stderr io.Writer) (*service.StatusSnapshot, error) {
	mode := opts.Mode
	if mode == "" {
		mode = service.DrainModeSafePoint
	}
	if mode != service.DrainModeSafePoint && mode != service.DrainModeTurn {
		return nil, fmt.Errorf("invalid drain mode %q (expected safe-point or turn)", mode)
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}

	snap, err := client.BeginDrain(ctx, service.DrainRequest{
		Mode:           mode,
		Action:         action,
		WaitBackground: opts.WaitBackground,
		AutoShutdown:   false,
		LeaseMs:        int64(30 * time.Second / time.Millisecond),
	})
	if err != nil {
		return nil, err
	}

	if snap.ReadyToStop(opts.WaitBackground) {
		if len(snap.ActiveBackground) > 0 && !opts.WaitBackground {
			fmt.Fprintf(stderr, "Note: %d active background command(s) will be stopped during %s (use --wait-background to wait for them).\n", len(snap.ActiveBackground), action)
		}
		return snap, nil
	}

	fmt.Fprintf(stdout, "Draining Ariadne before %s (mode: %s)...\n", action, mode)
	printActiveWork(stdout, snap)
	statefulCount := 0
	for _, turn := range snap.ActiveTurns {
		if turn.Stateful {
			statefulCount++
		}
	}
	if statefulCount > 0 {
		fmt.Fprintf(stdout, "Note: %d active local CLI session(s) cannot pause at mid-turn checkpoints and must finish their current turn (use --when-idle to keep scheduling new work, or --force to %s immediately).\n", statefulCount, action)
	}
	fmt.Fprintln(stdout, "Waiting for active sessions to reach a safe checkpoint (press Ctrl+C to cancel and resume)...")

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	lastCount := len(snap.ActiveTurns)
	lastBg := len(snap.ActiveBackground)

	cancelDrain := func() {
		cancelCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = client.CancelDrain(cancelCtx)
	}

	for {
		select {
		case <-ctx.Done():
			cancelDrain()
			fmt.Fprintln(stderr, "\nCancelled; normal Ariadne scheduling has resumed.")
			return nil, errors.New("cancelled by user")
		case <-ticker.C:
			if time.Now().After(deadline) {
				cancelDrain()
				return nil, fmt.Errorf("timed out after %s waiting for active sessions to drain; scheduling resumed (use --when-idle or --force)", timeout)
			}
			cur, err := client.Status(ctx)
			if err != nil {
				// Server may have exited externally.
				return nil, fmt.Errorf("lost connection to Ariadne while draining: %w", err)
			}
			snap = cur
			if !snap.Draining {
				return nil, errors.New("drain was cancelled externally; normal Ariadne scheduling has resumed")
			}
			if snap.ReadyToStop(opts.WaitBackground) {
				fmt.Fprintln(stdout, "All active sessions reached a safe checkpoint.")
				return snap, nil
			}
			if len(snap.ActiveTurns) != lastCount || len(snap.ActiveBackground) != lastBg {
				lastCount = len(snap.ActiveTurns)
				lastBg = len(snap.ActiveBackground)
				fmt.Fprintf(stdout, "Still draining: %d active session(s), %d background command(s)...\n", lastCount, lastBg)
			}
		}
	}
}

func stopRunningService(ctx context.Context, cfg config.Config, mgr service.Manager, opts drainControlOptions, stdout, stderr io.Writer) error {
	client, snap, rt := discoverRunning(ctx, cfg)
	if snap == nil && rt == nil {
		fmt.Fprintln(stdout, "Ariadne is not running.")
		return nil
	}
	if snap == nil && rt != nil {
		// Process exists in runtime.json but did not answer HTTP control API.
		if err := mgr.Stop(ctx); err != nil {
			return err
		}
		if waitForProcessExit(ctx, rt.PID, 10*time.Second) {
			fmt.Fprintf(stdout, "Stopped Ariadne (PID %d).\n", rt.PID)
			return nil
		}
		return fmt.Errorf("process %d did not exit after stop signal", rt.PID)
	}

	if opts.WhenIdle {
		updated, err := client.BeginDrain(ctx, service.DrainRequest{
			Mode:           service.DrainModeWhenIdle,
			Action:         service.ActionStop,
			WaitBackground: opts.WaitBackground,
			AutoShutdown:   true,
		})
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, "Scheduled Ariadne to stop automatically once all sessions are idle.")
		printActiveWork(stdout, updated)
		return nil
	}

	if !opts.Force {
		if _, err := drainBeforeShutdown(ctx, client, service.ActionStop, opts, stdout, stderr); err != nil {
			return err
		}
	}

	oldPID := snap.PID
	if err := client.Shutdown(ctx, service.ShutdownRequest{Restart: false, Force: opts.Force}); err != nil {
		return err
	}
	if !waitForProcessExit(ctx, oldPID, 15*time.Second) {
		if err := mgr.Stop(ctx); err != nil {
			return err
		}
		if !waitForProcessExit(ctx, oldPID, 10*time.Second) {
			return fmt.Errorf("Ariadne (PID %d) did not exit within timeout", oldPID)
		}
	}
	fmt.Fprintf(stdout, "Ariadne stopped (was PID %d).\n", oldPID)
	return nil
}

func restartRunningService(ctx context.Context, cfg config.Config, mgr service.Manager, installOpts service.InstallOptions, opts drainControlOptions, stdout, stderr io.Writer) (*service.StatusSnapshot, error) {
	client, snap, rt := discoverRunning(ctx, cfg)
	if snap == nil && rt == nil {
		fmt.Fprintln(stdout, "Ariadne is not currently running; starting service...")
		if err := startServiceIfStopped(ctx, cfg, mgr, installOpts, stdout); err != nil {
			return nil, err
		}
		_, started, _ := discoverRunning(ctx, cfg)
		return started, nil
	}
	if snap == nil && rt != nil {
		// Unresponsive process: stop then start.
		_ = mgr.Stop(ctx)
		_ = waitForProcessExit(ctx, rt.PID, 10*time.Second)
		if err := mgr.Start(ctx, installOpts); err != nil {
			return nil, err
		}
		started, err := waitForServerReady(ctx, cfg, rt.PID, 15*time.Second)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(stdout, "Ariadne restarted (PID %d, %s)\n", started.PID, started.Version)
		fmt.Fprintf(stdout, "Dashboard: %s\n", service.DashboardURL(started.Host, started.Port, cfg.Token))
		return started, nil
	}

	if opts.WhenIdle {
		updated, err := client.BeginDrain(ctx, service.DrainRequest{
			Mode:           service.DrainModeWhenIdle,
			Action:         service.ActionRestart,
			WaitBackground: opts.WaitBackground,
			AutoShutdown:   true,
		})
		if err != nil {
			if strings.Contains(err.Error(), "cannotRestart") {
				return nil, errors.New("current Ariadne process was started directly in the foreground and cannot self-restart when idle; stop it and start via 'ariadne service start'")
			}
			return nil, err
		}
		fmt.Fprintln(stdout, "Scheduled Ariadne to restart automatically once all sessions are idle.")
		printActiveWork(stdout, updated)
		return updated, nil
	}

	if !opts.Force {
		if _, err := drainBeforeShutdown(ctx, client, service.ActionRestart, opts, stdout, stderr); err != nil {
			return nil, err
		}
	}

	oldPID := snap.PID
	selfManaged := snap.ManagedBy != "" && snap.ManagedBy != service.ManagedDirect

	if err := client.Shutdown(ctx, service.ShutdownRequest{Restart: selfManaged, Force: opts.Force}); err != nil {
		return nil, err
	}
	if !waitForProcessExit(ctx, oldPID, 15*time.Second) {
		return nil, fmt.Errorf("previous Ariadne process (PID %d) did not exit in time", oldPID)
	}

	if !selfManaged {
		if err := mgr.Start(ctx, installOpts); err != nil {
			return nil, err
		}
		started, err := waitForServerReady(ctx, cfg, oldPID, 15*time.Second)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(stdout, "Ariadne restarted (PID %d, %s)\n", started.PID, started.Version)
		fmt.Fprintf(stdout, "Dashboard: %s\n", service.DashboardURL(started.Host, started.Port, cfg.Token))
		return started, nil
	}

	// Wait briefly for supervisor / self-respawn; fall back to manager.Start if it hasn't come up.
	started, err := waitForServerReady(ctx, cfg, oldPID, 5*time.Second)
	if err != nil {
		if startErr := mgr.Start(ctx, installOpts); startErr != nil {
			return nil, fmt.Errorf("restart failed: %w (fallback start: %v)", err, startErr)
		}
		started, err = waitForServerReady(ctx, cfg, oldPID, 15*time.Second)
		if err != nil {
			return nil, err
		}
	}
	fmt.Fprintf(stdout, "Ariadne restarted (PID %d, %s)\n", started.PID, started.Version)
	fmt.Fprintf(stdout, "Dashboard: %s\n", service.DashboardURL(started.Host, started.Port, cfg.Token))
	return started, nil
}

func cmdServiceStop(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("service stop", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printServiceHelp(stderr) }

	var (
		home           string
		mode           string
		whenIdle       bool
		waitBackground bool
		timeout        time.Duration
		force          bool
		showHelp       bool
	)
	fs.StringVar(&home, "home", "", "Ariadne data directory")
	fs.StringVar(&mode, "mode", service.DrainModeSafePoint, "Drain mode: safe-point or turn")
	fs.BoolVar(&whenIdle, "when-idle", false, "Schedule stop automatically once idle")
	fs.BoolVar(&waitBackground, "wait-background", false, "Also wait for active background commands")
	fs.DurationVar(&timeout, "timeout", 5*time.Minute, "Max wait for active sessions")
	fs.BoolVar(&force, "force", false, "Stop immediately without draining")
	fs.BoolVar(&showHelp, "help", false, "Show help")
	fs.BoolVar(&showHelp, "h", false, "Show help")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if showHelp {
		printServiceHelp(stdout)
		return nil
	}

	cfg, err := loadConfigWithOverrides(home, "", 0, false, true, false)
	if err != nil {
		return err
	}
	mgr := service.NewManager(cfg.Home)
	return stopRunningService(ctx, cfg, mgr, drainControlOptions{
		Mode:           mode,
		WhenIdle:       whenIdle,
		WaitBackground: waitBackground,
		Timeout:        timeout,
		Force:          force,
	}, stdout, stderr)
}

func cmdServiceRestart(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("service restart", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printServiceHelp(stderr) }

	var (
		home           string
		mode           string
		whenIdle       bool
		waitBackground bool
		timeout        time.Duration
		force          bool
		showHelp       bool
	)
	fs.StringVar(&home, "home", "", "Ariadne data directory")
	fs.StringVar(&mode, "mode", service.DrainModeSafePoint, "Drain mode: safe-point or turn")
	fs.BoolVar(&whenIdle, "when-idle", false, "Schedule restart automatically once idle")
	fs.BoolVar(&waitBackground, "wait-background", false, "Also wait for active background commands")
	fs.DurationVar(&timeout, "timeout", 5*time.Minute, "Max wait for active sessions")
	fs.BoolVar(&force, "force", false, "Restart immediately without draining")
	fs.BoolVar(&showHelp, "help", false, "Show help")
	fs.BoolVar(&showHelp, "h", false, "Show help")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if showHelp {
		printServiceHelp(stdout)
		return nil
	}

	cfg, err := loadConfigWithOverrides(home, "", 0, false, true, false)
	if err != nil {
		return err
	}
	exe, err := resolveExecutable()
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}
	mgr := service.NewManager(cfg.Home)
	installOpts := buildStartOptions(exe, cfg, "", 0)
	_, err = restartRunningService(ctx, cfg, mgr, installOpts, drainControlOptions{
		Mode:           mode,
		WhenIdle:       whenIdle,
		WaitBackground: waitBackground,
		Timeout:        timeout,
		Force:          force,
	}, stdout, stderr)
	return err
}

func cmdServiceStatus(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("service status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printServiceHelp(stderr) }

	var (
		home     string
		asJSON   bool
		showHelp bool
	)
	fs.StringVar(&home, "home", "", "Ariadne data directory")
	fs.BoolVar(&asJSON, "json", false, "Output JSON")
	fs.BoolVar(&showHelp, "help", false, "Show help")
	fs.BoolVar(&showHelp, "h", false, "Show help")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if showHelp {
		printServiceHelp(stdout)
		return nil
	}

	cfg, err := loadConfigWithOverrides(home, "", 0, false, true, false)
	if err != nil {
		return err
	}
	mgr := service.NewManager(cfg.Home)
	svcInfo, err := mgr.Info(ctx)
	if err != nil {
		return err
	}
	_, snap, _ := discoverRunning(ctx, cfg)
	running := snap != nil

	if asJSON {
		payload := map[string]any{
			"running": running,
			"service": svcInfo,
			"runtime": snap,
		}
		if running {
			payload["dashboardUrl"] = service.DashboardURL(snap.Host, snap.Port, cfg.Token)
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		return enc.Encode(payload)
	}

	if running {
		fmt.Fprintf(stdout, "Status:       running\n")
		fmt.Fprintf(stdout, "Version:      %s\n", snap.Version)
		fmt.Fprintf(stdout, "PID:          %d\n", snap.PID)
		fmt.Fprintf(stdout, "Address:      http://%s:%d\n", snap.Host, snap.Port)
		fmt.Fprintf(stdout, "Managed by:   %s\n", snap.ManagedBy)
		if snap.StartedAt > 0 {
			uptime := time.Since(time.UnixMilli(snap.StartedAt)).Truncate(time.Second)
			fmt.Fprintf(stdout, "Uptime:       %s\n", uptime)
		}
		if snap.Draining {
			fmt.Fprintf(stdout, "Draining:     yes (mode=%s, action=%s, phase=%s)\n", snap.DrainMode, snap.DrainAction, snap.RestartPhase)
		}
	} else {
		fmt.Fprintf(stdout, "Status:       stopped\n")
	}

	fmt.Fprintf(stdout, "Service:      installed=%v, manager=%s, autostart=%v\n", svcInfo.Installed, svcInfo.ManagedBy, svcInfo.Autostart)
	if svcInfo.ConfigPath != "" {
		fmt.Fprintf(stdout, "Config:       %s\n", svcInfo.ConfigPath)
	}
	if svcInfo.LogPath != "" {
		fmt.Fprintf(stdout, "Log file:     %s\n", svcInfo.LogPath)
	}
	if svcInfo.Note != "" {
		fmt.Fprintf(stdout, "Note:         %s\n", svcInfo.Note)
	}

	if running {
		if len(snap.ActiveTurns) == 0 && len(snap.ActiveBackground) == 0 {
			fmt.Fprintln(stdout, "Active work:  idle")
		} else {
			printActiveWork(stdout, snap)
		}
	}
	return nil
}

func cmdServiceLogs(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("service logs", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printServiceHelp(stderr) }

	var (
		home     string
		follow   bool
		lines    int
		showHelp bool
	)
	fs.StringVar(&home, "home", "", "Ariadne data directory")
	fs.BoolVar(&follow, "follow", false, "Follow log output")
	fs.BoolVar(&follow, "f", false, "Follow log output")
	fs.IntVar(&lines, "lines", 100, "Number of lines to show")
	fs.IntVar(&lines, "n", 100, "Number of lines to show")
	fs.BoolVar(&showHelp, "help", false, "Show help")
	fs.BoolVar(&showHelp, "h", false, "Show help")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if showHelp {
		printServiceHelp(stdout)
		return nil
	}

	cfg, err := loadConfigWithOverrides(home, "", 0, false, true, false)
	if err != nil {
		return err
	}
	logPath := service.LogPath(cfg.Home)
	if mgr := service.NewManager(cfg.Home); mgr != nil {
		if info, err := mgr.Info(ctx); err == nil && info.LogPath != "" {
			logPath = info.LogPath
		}
	}
	return service.TailLog(ctx, logPath, lines, follow, stdout)
}
