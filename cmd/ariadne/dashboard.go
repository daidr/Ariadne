package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/daidr/Ariadne/internal/service"
)

func cmdDashboard(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("dashboard", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printDashboardHelp(stderr) }

	var (
		home     string
		openFlag bool
		asJSON   bool
		showHelp bool
	)
	fs.StringVar(&home, "home", "", "Ariadne data directory")
	fs.BoolVar(&openFlag, "open", false, "Open the dashboard in the default browser")
	fs.BoolVar(&asJSON, "json", false, "Output JSON")
	fs.BoolVar(&showHelp, "help", false, "Show help")
	fs.BoolVar(&showHelp, "h", false, "Show help")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if showHelp {
		printDashboardHelp(stdout)
		return nil
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q (run 'ariadne dashboard --help')", fs.Arg(0))
	}

	cfg, err := loadConfigWithOverrides(home, "", 0, false, true, false)
	if err != nil {
		return err
	}

	host := cfg.Host
	port := cfg.Port
	running := false
	pid := 0

	if rt, err := service.LiveRuntime(cfg.Home); err == nil && rt != nil {
		client := service.NewClient(rt.BaseURL(), cfg.Token)
		if err := client.Health(ctx); err == nil {
			host = rt.Host
			port = rt.Port
			running = true
			pid = rt.PID
		}
	}
	if !running {
		client := service.NewClient("http://"+cfg.Address(), cfg.Token)
		if snap, err := client.Status(ctx); err == nil && snap != nil {
			host = snap.Host
			port = snap.Port
			running = true
			pid = snap.PID
		}
	}

	url := service.DashboardURL(host, port, cfg.Token)
	if asJSON {
		payload := map[string]any{
			"url":     url,
			"running": running,
			"host":    host,
			"port":    port,
			"pid":     pid,
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		if err := enc.Encode(payload); err != nil {
			return err
		}
	} else {
		fmt.Fprintln(stdout, url)
		if !running {
			fmt.Fprintln(stderr, "Note: Ariadne service is not currently running. Start it with: ariadne service start")
		}
	}

	if openFlag {
		openBrowser(url)
	}
	return nil
}
