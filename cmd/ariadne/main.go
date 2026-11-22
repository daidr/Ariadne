package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/daidr/Ariadne/internal/agentruntime"
)

var version = "dev"

type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string {
	if e.err != nil {
		return e.err.Error()
	}
	return ""
}

func (e *exitError) Unwrap() error {
	return e.err
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runWithIO(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		var exitErr *exitError
		if errors.As(err, &exitErr) {
			if exitErr.err != nil {
				fmt.Fprintln(os.Stderr, exitErr.err)
			}
			os.Exit(exitErr.code)
		}
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintf(os.Stderr, "ariadne: %v\n", err)
		}
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	return runWithIO(ctx, args, os.Stdout, os.Stderr)
}

func runWithIO(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printRootHelp(stdout)
		return nil
	}
	cmd := args[0]
	rest := args[1:]
	switch cmd {
	case "-h", "--help":
		printRootHelp(stdout)
		return nil
	case "help":
		return cmdHelp(rest, stdout)
	case "-v", "--version":
		fmt.Fprintln(stdout, version)
		return nil
	case "version":
		return cmdVersion(rest, stdout, stderr)
	case "self-check":
		return selfCheck(stdout)
	case agentruntime.StdioBridgeCommand:
		if len(rest) != 1 {
			return errors.New("mcp-stdio requires the local bridge URL")
		}
		return agentruntime.RunStdioBridge(ctx, rest[0], os.Stdin, os.Stdout)
	case "serve":
		return cmdServe(ctx, rest, stdout, stderr)
	case "dashboard":
		return cmdDashboard(ctx, rest, stdout, stderr)
	case "service":
		return cmdService(ctx, rest, stdout, stderr)
	case "upgrade":
		return cmdUpgrade(ctx, rest, stdout, stderr)
	default:
		return fmt.Errorf("unknown command %q (run 'ariadne --help' for usage)", cmd)
	}
}

func cmdHelp(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		printRootHelp(stdout)
		return nil
	}
	switch args[0] {
	case "serve":
		printServeHelp(stdout)
	case "dashboard":
		printDashboardHelp(stdout)
	case "service":
		printServiceHelp(stdout)
	case "upgrade":
		printUpgradeHelp(stdout)
	case "version":
		printVersionHelp(stdout)
	default:
		return fmt.Errorf("unknown command %q (run 'ariadne --help' for usage)", args[0])
	}
	return nil
}

func cmdVersion(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printVersionHelp(stderr) }

	var (
		asJSON   bool
		showHelp bool
	)
	fs.BoolVar(&asJSON, "json", false, "Output version info as JSON")
	fs.BoolVar(&showHelp, "help", false, "Show help")
	fs.BoolVar(&showHelp, "h", false, "Show help")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if showHelp {
		printVersionHelp(stdout)
		return nil
	}
	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{
			"version": version,
			"os":      runtime.GOOS,
			"arch":    runtime.GOARCH,
			"bundled": bundledBuild,
		})
	}
	fmt.Fprintln(stdout, version)
	return nil
}

func selfCheck(stdout io.Writer) error {
	assets, err := embeddedWeb()
	if err != nil {
		return err
	}
	if assets == nil {
		return errors.New("missing embedded web assets")
	}
	page, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		return err
	}
	if !bytes.Contains(page, []byte("<script")) {
		return errors.New("embedded index.html is missing its script")
	}
	files := 0
	if err := fs.WalkDir(assets, ".", func(_ string, current fs.DirEntry, err error) error {
		if err == nil && !current.IsDir() {
			files++
		}
		return err
	}); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "ok", files)
	return nil
}
