package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/daidr/Ariadne/internal/agentruntime"
	"github.com/daidr/Ariadne/internal/application"
	"github.com/daidr/Ariadne/internal/config"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
	"github.com/daidr/Ariadne/internal/httpauth"
	"github.com/daidr/Ariadne/internal/process"
	"github.com/daidr/Ariadne/internal/update"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 1 {
		switch args[0] {
		case "--version", "version":
			fmt.Println(version)
			return nil
		case "self-check":
			return selfCheck()
		}
	}
	if len(args) > 0 && args[0] == agentruntime.StdioBridgeCommand {
		if len(args) != 2 {
			return errors.New("mcp-stdio requires the local bridge URL")
		}
		return agentruntime.RunStdioBridge(ctx, args[1], os.Stdin, os.Stdout)
	}
	if len(args) != 0 && (len(args) != 1 || args[0] != "serve") {
		return errors.New("usage: ariadne [serve | --version | self-check]")
	}
	cfg, err := config.Load(func(key string) (string, bool) {
		value, exists := os.LookupEnv(key)
		if key == "ARIADNE_OPEN" && !exists && bundledBuild {
			return "1", true
		}
		return value, exists
	})
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", cfg.Address())
	if err != nil {
		return err
	}
	defer listener.Close()
	return serve(ctx, cfg, listener)
}

func selfCheck() error {
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
	fmt.Println("ok", files)
	return nil
}

func serve(ctx context.Context, cfg config.Config, listener net.Listener) error {
	web, err := webAssets()
	if err != nil {
		return err
	}
	executable, executableErr := os.Executable()
	updater := &update.Options{Version: version}
	if bundledBuild && executableErr == nil && os.Getenv("ARIADNE_DISABLE_SELF_UPDATE") != "1" {
		updater.Executable = executable
	}
	app, err := application.Open(ctx, application.Options{
		Config: cfg, Web: web, Ledger: process.DefaultLedger, Update: updater,
		OnError: func(err error) { log.Printf("ariadne: %v", err) },
		StdioLauncher: func(address string) (string, []string, error) {
			return executable, []string{agentruntime.StdioBridgeCommand, address}, executableErr
		},
	})
	if err != nil {
		return err
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
		ReadHeaderTimeout: 10 * time.Second, IdleTimeout: time.Minute,
		MaxHeaderBytes: 64 << 10, BaseContext: func(net.Listener) context.Context { return httpLife },
	}
	exited := make(chan error, 1)
	go func() { exited <- server.Serve(listener) }()
	pageURL := "http://" + listener.Addr().String() + "/#" + url.Values{"token": {cfg.Token}}.Encode()
	fmt.Println("Ariadne listening at " + pageURL)
	if cfg.Open && web != nil {
		delay := 12 * time.Second
		if cfg.FirstRun {
			delay = 0
		}
		timer := time.AfterFunc(delay, func() {
			if ctx.Err() == nil && !pageConnected.Load() {
				openBrowser(pageURL)
			}
		})
		defer timer.Stop()
	}
	var serveErr error
	select {
	case <-ctx.Done():
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
	cleanup, stopCleanup := context.WithTimeout(context.Background(), 30*time.Second)
	defer stopCleanup()
	return errors.Join(serveErr, shutdownErr, app.Close(cleanup))
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
