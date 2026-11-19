// Package update checks the official release feed and installs verified programs.
package update

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/daidr/Ariadne/internal/settings"
	"golang.org/x/mod/semver"
)

var ErrBusy = errors.New("an update is in progress")
var ErrUnavailable = errors.New("that update is unavailable")

type Options struct {
	Version    string
	Executable string // Empty for development builds and externally managed installations.
	Client     *http.Client
	Interval   time.Duration
	OnError    func(error)
}

type View struct {
	Version         string                     `json:"version"`
	Preferences     settings.UpdatePreferences `json:"preferences"`
	CanInstall      bool                       `json:"canInstall"`
	State           string                     `json:"state"`
	Release         *Release                   `json:"release"`
	Failure         string                     `json:"failure,omitempty"`
	DownloadPercent int                        `json:"downloadPercent"`
}

func (view View) updating() bool {
	return view.State == "downloading" || view.State == "installing"
}

type Service struct {
	mu          sync.Mutex
	store       *settings.Store
	view        View
	lastCheck   time.Time
	checkClient *http.Client
	assetClient *http.Client
	executable  string
	original    os.FileInfo
	wake        chan struct{}
	life        context.Context
	cancel      context.CancelFunc
	done        chan struct{}
	jobs        sync.WaitGroup
	closed      bool
	onError     func(error)
}

func New(ctx context.Context, store *settings.Store, options Options) *Service {
	life, cancel := context.WithCancel(ctx)
	interval := options.Interval
	if interval <= 0 {
		interval = 6 * time.Hour
	}
	tag := versionTag(options.Version)
	channel := "stable"
	if tag == "" || semver.Prerelease(tag) != "" {
		channel = "preview"
	}
	service := &Service{
		store: store, view: View{Version: options.Version, Preferences: store.UpdatePreferences(channel), State: "idle"},
		checkClient: releaseClient(options.Client, false), assetClient: releaseClient(options.Client, true),
		wake: make(chan struct{}, 1), life: life, cancel: cancel, done: make(chan struct{}), onError: options.OnError,
	}
	if executable := options.Executable; tag != "" && executable != "" {
		if path, err := filepath.EvalSymlinks(executable); err == nil {
			if stat, err := os.Stat(path); err == nil && stat.Mode().IsRegular() {
				service.executable, service.original, service.view.CanInstall = path, stat, true
				cleanPrevious(path)
			}
		}
	}
	go service.run(interval)
	return service
}

func (service *Service) View() View {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.view
}

func (service *Service) Save(value settings.UpdatePreferences) (View, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.view.updating() || service.view.State == "checking" {
		return service.view, ErrBusy
	}
	if service.view.Preferences == value {
		return service.view, nil
	}
	if err := service.store.SaveUpdatePreferences(value); err != nil {
		return service.view, err
	}
	if service.view.Preferences.Channel != value.Channel && service.view.State != "ready" {
		service.view.State, service.view.Failure, service.view.Release = "idle", "", nil
		service.lastCheck = time.Time{}
	}
	service.view.Preferences = value
	service.notify()
	return service.view, nil
}

func (service *Service) notify() {
	select {
	case service.wake <- struct{}{}:
	default:
	}
}

func (service *Service) Check(ctx context.Context) View {
	return service.check(ctx, true)
}

func (service *Service) check(ctx context.Context, wakeAutomatic bool) View {
	service.mu.Lock()
	before := service.view
	if service.closed || before.State == "checking" || before.updating() || before.State == "ready" ||
		time.Since(service.lastCheck) < 5*time.Second {
		service.mu.Unlock()
		return before
	}
	service.view.State = "checking"
	service.jobs.Add(1)
	service.mu.Unlock()
	defer service.jobs.Done()

	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	stop := context.AfterFunc(service.life, cancel)
	defer stop()
	release, err := readReleases(ctx, service.checkClient, before.Preferences.Channel)
	if err != nil && ctx.Err() == nil && service.onError != nil {
		service.onError(err)
	}

	service.mu.Lock()
	defer service.mu.Unlock()
	if err != nil {
		service.view.State, service.view.Failure = before.State, "check"
		// A failed recheck leaves the last verified metadata available for display.
	} else {
		service.view.Release, service.view.Failure = release, ""
		switch {
		case release == nil:
			service.view.State = "no-release"
		case semver.Compare(versionTag(release.Version), versionTag(service.view.Version)) <= 0:
			service.view.State = "current"
		default:
			service.view.State = "available"
			if wakeAutomatic && service.view.CanInstall && service.view.Preferences.Automatic && release.Installable {
				service.notify()
			}
		}
	}
	service.lastCheck = time.Now()
	return service.view
}

func (service *Service) Install(version string) (View, error) {
	return service.startInstall(version, false)
}

func (service *Service) startInstall(version string, automatic bool) (View, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	view := service.view
	if view.updating() {
		return view, nil
	}
	if service.closed || view.State != "available" || !view.CanInstall || view.Release == nil ||
		!view.Release.Installable || view.Release.Version != version || (automatic && !view.Preferences.Automatic) {
		return view, ErrUnavailable
	}
	service.view.State, service.view.Failure, service.view.DownloadPercent = "downloading", "", 0
	service.jobs.Go(func() {
		ctx, cancel := context.WithTimeout(service.life, 5*time.Minute)
		defer cancel()
		err := install(ctx, service.assetClient, service.executable, service.original, *view.Release, service.reportProgress)
		if err != nil && service.onError != nil {
			service.onError(err)
		}
		service.mu.Lock()
		defer service.mu.Unlock()
		service.view.DownloadPercent = 0
		if err != nil {
			service.view.State, service.view.Failure = "available", "install"
			switch {
			case errors.Is(err, errDownload) && errors.Is(err, context.DeadlineExceeded):
				service.view.Failure = "downloadTimeout"
			case errors.Is(err, errDownload):
				service.view.Failure = "download"
			case errors.Is(err, errVerification):
				service.view.Failure = "verification"
			case errors.Is(err, os.ErrPermission):
				service.view.Failure = "permission"
			}
			return
		}
		service.view.State = "ready"
	})
	return service.view, nil
}

func (service *Service) reportProgress(percent int) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.view.DownloadPercent = percent
	if percent == 100 {
		service.view.State = "installing"
	}
}

func (service *Service) run(interval time.Duration) {
	defer close(service.done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if view := service.View(); view.CanInstall && view.Preferences.Automatic {
			view = service.check(service.life, false)
			if view.State == "available" && view.Failure != "check" && view.Release.Installable {
				_, _ = service.startInstall(view.Release.Version, true)
			}
		}
		select {
		case <-service.life.Done():
			return
		case <-ticker.C:
		case <-service.wake:
		}
	}
}

func (service *Service) Close(ctx context.Context) error {
	service.mu.Lock()
	service.closed = true
	service.mu.Unlock()
	service.cancel()
	select {
	case <-service.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	joined := make(chan struct{})
	go func() { service.jobs.Wait(); close(joined) }()
	select {
	case <-joined:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
