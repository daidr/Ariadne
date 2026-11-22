package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/daidr/Ariadne/internal/service"
	"github.com/daidr/Ariadne/internal/update"
)

func cmdUpgrade(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printUpgradeHelp(stderr) }

	var (
		home           string
		checkOnly      bool
		channel        string
		targetVersion  string
		mode           string
		whenIdle       bool
		waitBackground bool
		timeout        time.Duration
		force          bool
		noRestart      bool
		showHelp       bool
	)
	fs.StringVar(&home, "home", "", "Ariadne data directory")
	fs.BoolVar(&checkOnly, "check", false, "Check for updates without installing")
	fs.StringVar(&channel, "channel", "", "Release channel: stable or preview")
	fs.StringVar(&targetVersion, "version", "", "Install a specific release version")
	fs.StringVar(&mode, "mode", service.DrainModeSafePoint, "Drain mode when restarting: safe-point or turn")
	fs.BoolVar(&whenIdle, "when-idle", false, "Replace binary now and restart automatically once idle")
	fs.BoolVar(&waitBackground, "wait-background", false, "Also wait for active background commands before restarting")
	fs.DurationVar(&timeout, "timeout", 5*time.Minute, "Max wait for active sessions")
	fs.BoolVar(&force, "force", false, "Restart immediately without waiting for active sessions")
	fs.BoolVar(&noRestart, "no-restart", false, "Replace binary without restarting the running service")
	fs.BoolVar(&showHelp, "help", false, "Show help")
	fs.BoolVar(&showHelp, "h", false, "Show help")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if showHelp {
		printUpgradeHelp(stdout)
		return nil
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q (run 'ariadne upgrade --help')", fs.Arg(0))
	}
	if !checkOnly && os.Getenv("ARIADNE_DISABLE_SELF_UPDATE") == "1" {
		return errors.New("self-update is disabled by ARIADNE_DISABLE_SELF_UPDATE=1 (update via your package manager, or run 'ariadne upgrade --check' to check for updates)")
	}

	var (
		release        *update.Release
		resolvedChan   string
		fallbackNotice bool
		err            error
	)

	if targetVersion != "" {
		fmt.Fprintf(stdout, "Looking up Ariadne %s...\n", targetVersion)
		release, err = update.FindRelease(ctx, nil, targetVersion)
		if err != nil {
			return err
		}
		if release.Prerelease {
			resolvedChan = "preview"
		} else {
			resolvedChan = "stable"
		}
	} else if channel != "" {
		fmt.Fprintf(stdout, "Checking for Ariadne updates (%s channel)...\n", channel)
		release, err = update.CheckLatest(ctx, nil, channel)
		if err != nil {
			return err
		}
		resolvedChan = channel
	} else {
		preferred := update.DefaultChannel(version)
		fmt.Fprintf(stdout, "Checking for Ariadne updates (%s channel)...\n", preferred)
		release, resolvedChan, err = update.CheckLatestWithFallback(ctx, nil, preferred)
		if err != nil {
			return err
		}
		if preferred == "stable" && resolvedChan == "preview" && release != nil {
			fallbackNotice = true
		}
	}

	if release == nil {
		fmt.Fprintf(stdout, "No releases found on the %s channel.\n", resolvedChan)
		return nil
	}
	if fallbackNotice {
		fmt.Fprintln(stdout, "No stable release is published yet; falling back to the latest preview release.")
	}

	if targetVersion == "" && update.CompareVersions(version, release.Version) >= 0 {
		fmt.Fprintf(stdout, "Ariadne %s is already up to date.\n", version)
		return nil
	}

	if checkOnly {
		fmt.Fprintf(stdout, "Update available: %s -> %s (%s)\n", version, release.Version, resolvedChan)
		if release.URL != "" {
			fmt.Fprintf(stdout, "Release notes: %s\n", release.URL)
		}
		fmt.Fprintln(stdout, "Run 'ariadne upgrade' to install.")
		return nil
	}

	exe, err := resolveExecutable()
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}

	fmt.Fprintf(stdout, "Downloading Ariadne %s...\n", release.Version)
	lastPct := -1
	err = update.InstallExecutable(ctx, nil, exe, *release, func(pct int) {
		if pct == lastPct {
			return
		}
		if pct == 100 || pct-lastPct >= 10 || lastPct < 0 {
			lastPct = pct
			fmt.Fprintf(stdout, "  %d%%\n", pct)
		}
	})
	if err != nil {
		return fmt.Errorf("install update: %w", err)
	}
	fmt.Fprintf(stdout, "Verified and installed Ariadne %s at %s.\n", release.Version, exe)

	if noRestart {
		fmt.Fprintln(stdout, "Skipped restart (--no-restart). Restart when ready with: ariadne service restart")
		return nil
	}

	cfg, err := loadConfigWithOverrides(home, "", 0, false, true, false)
	if err != nil {
		return err
	}
	_, snap, rt := discoverRunning(ctx, cfg)
	if snap == nil && rt == nil {
		fmt.Fprintln(stdout, "Ariadne service is not currently running. Start it with: ariadne service start")
		return nil
	}

	mgr := service.NewManager(cfg.Home)
	installOpts := buildStartOptions(exe, cfg, "", 0)

	restarted, err := restartRunningService(ctx, cfg, mgr, installOpts, drainControlOptions{
		Mode:           mode,
		WhenIdle:       whenIdle,
		WaitBackground: waitBackground,
		Timeout:        timeout,
		Force:          force,
	}, stdout, stderr)
	if err != nil {
		return errors.Join(err, errors.New("binary was updated on disk; restart manually with: ariadne service restart"))
	}
	if !whenIdle && restarted != nil && restarted.Version != "" && restarted.Version != release.Version {
		fmt.Fprintf(stderr, "Note: restarted instance reports version %s (expected %s).\n", restarted.Version, release.Version)
	}
	return nil
}
