package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/daidr/Ariadne/internal/agentruntime"
	"github.com/daidr/Ariadne/internal/application"
	"github.com/daidr/Ariadne/internal/config"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
	"github.com/daidr/Ariadne/internal/httpauth"
	"github.com/daidr/Ariadne/internal/process"
	"github.com/daidr/Ariadne/internal/service"
	"github.com/daidr/Ariadne/internal/update"
)

type serveOptions struct {
	ManagedBy string
	LogFile   string
	Stdout    io.Writer
}

func loadConfigWithOverrides(home, host string, port int, openFlag, noOpenFlag bool, defaultBundledOpen bool) (config.Config, error) {
	cfg, err := config.Load(func(key string) (string, bool) {
		switch key {
		case "ARIADNE_HOME":
			if home != "" {
				return home, true
			}
		case "ARIADNE_HOST":
			if host != "" {
				return host, true
			}
		case "ARIADNE_PORT":
			if port > 0 {
				return strconv.Itoa(port), true
			}
		case "ARIADNE_OPEN":
			if noOpenFlag {
				return "0", true
			}
			if openFlag {
				return "1", true
			}
			value, exists := os.LookupEnv(key)
			if !exists && defaultBundledOpen && bundledBuild {
				return "1", true
			}
			return value, exists
		}
		return os.LookupEnv(key)
	})
	return cfg, err
}

func cmdServe(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printServeHelp(stderr) }

	var (
		home      string
		host      string
		port      int
		openFlag  bool
		noOpen    bool
		logFile   string
		managedBy string
		showHelp  bool
	)
	fs.StringVar(&home, "home", "", "Ariadne data directory")
	fs.StringVar(&host, "host", "", "Address to bind")
	fs.IntVar(&port, "port", 0, "Port to listen on")
	fs.BoolVar(&openFlag, "open", false, "Open browser after startup")
	fs.BoolVar(&noOpen, "no-open", false, "Do not open browser after startup")
	fs.StringVar(&logFile, "log-file", "", "Rotating log file path")
	fs.StringVar(&managedBy, "managed-by", service.ManagedDirect, "Service manager identifier")
	fs.BoolVar(&showHelp, "help", false, "Show help")
	fs.BoolVar(&showHelp, "h", false, "Show help")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if showHelp {
		printServeHelp(stdout)
		return nil
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q (run 'ariadne serve --help')", fs.Arg(0))
	}
	if openFlag && noOpen {
		return errors.New("--open and --no-open cannot be used together")
	}

	defaultBundledOpen := managedBy == service.ManagedDirect
	cfg, err := loadConfigWithOverrides(home, host, port, openFlag, noOpen, defaultBundledOpen)
	if err != nil {
		return err
	}

	listener, err := net.Listen("tcp", cfg.Address())
	if err != nil {
		return err
	}
	defer listener.Close()

	return serveWithOptions(ctx, cfg, listener, serveOptions{
		ManagedBy: managedBy,
		LogFile:   logFile,
		Stdout:    stdout,
	})
}

func serve(ctx context.Context, cfg config.Config, listener net.Listener) error {
	return serveWithOptions(ctx, cfg, listener, serveOptions{
		ManagedBy: service.ManagedDirect,
		Stdout:    os.Stdout,
	})
}

func serveWithOptions(ctx context.Context, cfg config.Config, listener net.Listener, opts serveOptions) error {
	if opts.ManagedBy == "" {
		opts.ManagedBy = service.ManagedDirect
	}
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}

	var rotatingLog *service.RotatingFile
	prevLogOutput := log.Writer()
	if opts.LogFile != "" {
		var err error
		rotatingLog, err = service.OpenRotatingLog(opts.LogFile)
		if err != nil {
			return fmt.Errorf("open log file %s: %w", opts.LogFile, err)
		}
		log.SetOutput(rotatingLog)
		defer func() {
			log.SetOutput(prevLogOutput)
			_ = rotatingLog.Close()
		}()
	}

	web, err := webAssets()
	if err != nil {
		return err
	}

	// Capture executable path at startup before any in-place self-update renames/replaces it.
	executable, executableErr := os.Executable()
	if executableErr == nil {
		if resolved, err := filepath.EvalSymlinks(executable); err == nil {
			executable = resolved
		}
	}

	updater := &update.Options{Version: version}
	if bundledBuild && executableErr == nil && os.Getenv("ARIADNE_DISABLE_SELF_UPDATE") != "1" {
		updater.Executable = executable
	}

	actualHost := cfg.Host
	actualPort := cfg.Port
	if tcpAddr, ok := listener.Addr().(*net.TCPAddr); ok {
		if tcpAddr.IP != nil {
			actualHost = tcpAddr.IP.String()
		}
		if tcpAddr.Port > 0 {
			actualPort = tcpAddr.Port
		}
	}

	runtimeInfo := service.RuntimeInfo{
		PID:        os.Getpid(),
		Version:    version,
		Host:       actualHost,
		Port:       actualPort,
		StartedAt:  time.Now().UnixMilli(),
		ManagedBy:  opts.ManagedBy,
		Executable: executable,
	}

	serveCtx, cancelServe := context.WithCancel(ctx)
	defer cancelServe()

	var restartRequested atomic.Bool
	canRestart := opts.ManagedBy != service.ManagedDirect && executableErr == nil && executable != ""

	app, err := application.Open(serveCtx, application.Options{
		Config:         cfg,
		Web:            web,
		Ledger:         process.DefaultLedger,
		Update:         updater,
		ServiceRuntime: &runtimeInfo,
		CanRestart:     canRestart,
		OnShutdown: func(restart bool) {
			if restart {
				restartRequested.Store(true)
			}
			cancelServe()
		},
		OnError: func(err error) { log.Printf("ariadne: %v", err) },
		StdioLauncher: func(address string) (string, []string, error) {
			return executable, []string{agentruntime.StdioBridgeCommand, address}, executableErr
		},
	})
	if err != nil {
		return err
	}

	cleanupRuntime, runtimeErr := service.WriteRuntime(cfg.Home, runtimeInfo)
	if runtimeErr != nil {
		log.Printf("ariadne: write runtime.json: %v", runtimeErr)
	}

	httpLife, cancelHTTP := context.WithCancel(context.Background())
	defer cancelHTTP()
	var pageConnected atomic.Bool
	auth := httpauth.Auth{Token: cfg.Token}
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && r.URL.Path == "/api/rpc"+ariadnev1connect.EventServiceStreamEventsProcedure && auth.ValidBearerHeader(r.Header) {
				pageConnected.Store(true)
			}
			app.Handler.ServeHTTP(w, r)
		}),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       time.Minute,
		MaxHeaderBytes:    64 << 10,
		BaseContext:       func(net.Listener) context.Context { return httpLife },
	}
	exited := make(chan error, 1)
	go func() { exited <- server.Serve(listener) }()

	pageURL := service.DashboardURL(actualHost, actualPort, cfg.Token)
	if rotatingLog != nil {
		log.Printf("Ariadne %s listening at http://%s (managedBy=%s, pid=%d)", version, listener.Addr().String(), opts.ManagedBy, os.Getpid())
	} else {
		fmt.Fprintln(opts.Stdout, "Ariadne listening at "+pageURL)
	}

	if cfg.Open && web != nil {
		delay := 12 * time.Second
		if cfg.FirstRun {
			delay = 0
		}
		timer := time.AfterFunc(delay, func() {
			if serveCtx.Err() == nil && !pageConnected.Load() {
				openBrowser(pageURL)
			}
		})
		defer timer.Stop()
	}

	var serveErr error
	select {
	case <-serveCtx.Done():
	case serveErr = <-exited:
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
	}

	cancelHTTP()
	drain, stopDrain := context.WithTimeout(context.Background(), 5*time.Second)
	shutdownErr := server.Shutdown(drain)
	stopDrain()
	if shutdownErr != nil {
		shutdownErr = errors.Join(shutdownErr, server.Close())
	}
	_ = listener.Close()

	cleanup, stopCleanup := context.WithTimeout(context.Background(), 30*time.Second)
	closeErr := app.Close(cleanup)
	stopCleanup()

	if cleanupRuntime != nil {
		cleanupRuntime()
	}

	totalErr := errors.Join(serveErr, shutdownErr, closeErr)
	if !restartRequested.Load() {
		return totalErr
	}

	// Self-restart path: release log handle first before spawning or exiting for supervisor relaunch.
	if rotatingLog != nil {
		log.Printf("Ariadne exiting for restart (managedBy=%s)", opts.ManagedBy)
		log.SetOutput(prevLogOutput)
		_ = rotatingLog.Close()
		rotatingLog = nil
	}

	switch opts.ManagedBy {
	case service.ManagedLaunchd, service.ManagedSystemd:
		// Supervisors configured with KeepAlive.SuccessfulExit=false / Restart=on-failure
		// relaunch Ariadne immediately upon this dedicated non-zero exit code.
		return &exitError{code: service.ExitCodeRestart}
	case service.ManagedWindowsTask, service.ManagedDetached:
		if executableErr == nil && executable != "" {
			respawnArgs := service.BuildServeArgs(service.InstallOptions{
				Executable: executable,
				Home:       cfg.Home,
				Host:       cfg.Host,
				Port:       cfg.Port,
				LogPath:    opts.LogFile,
			}, opts.ManagedBy)
			if _, err := service.SpawnDetached(executable, respawnArgs, os.Environ()); err != nil {
				return errors.Join(totalErr, fmt.Errorf("respawn ariadne: %w", err))
			}
		}
		return totalErr
	default:
		return totalErr
	}
}

func webAssets() (fs.FS, error) {
	path := os.Getenv("ARIADNE_WEB")
	if path == "" {
		assets, err := embeddedWeb()
		if err != nil || assets != nil {
			return assets, err
		}
		path = filepath.Join("packages", "web", "dist")
		info, err := os.Stat(filepath.Join(path, "index.html"))
		if errors.Is(err, fs.ErrNotExist) || (err == nil && info.IsDir()) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
	}
	assets := os.DirFS(path)
	index, err := fs.Stat(assets, "index.html")
	if err != nil {
		return nil, fmt.Errorf("web assets: %w", err)
	}
	if !index.Mode().IsRegular() {
		return nil, errors.New("web assets need an index.html file")
	}
	return assets, nil
}
