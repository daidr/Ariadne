// Package application owns the lifetime of the native backend's services.
package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/daidr/Ariadne/internal/agentruntime"
	"github.com/daidr/Ariadne/internal/autopilot"
	"github.com/daidr/Ariadne/internal/browser"
	"github.com/daidr/Ariadne/internal/collab"
	"github.com/daidr/Ariadne/internal/commandpolicy"
	"github.com/daidr/Ariadne/internal/config"
	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/dashboard"
	"github.com/daidr/Ariadne/internal/data"
	"github.com/daidr/Ariadne/internal/hosting/github"
	"github.com/daidr/Ariadne/internal/hosting/gitlab"
	"github.com/daidr/Ariadne/internal/links"
	"github.com/daidr/Ariadne/internal/local"
	"github.com/daidr/Ariadne/internal/mcp"
	"github.com/daidr/Ariadne/internal/models"
	"github.com/daidr/Ariadne/internal/notifications"
	"github.com/daidr/Ariadne/internal/plugins"
	"github.com/daidr/Ariadne/internal/process"
	"github.com/daidr/Ariadne/internal/projectsettings"
	"github.com/daidr/Ariadne/internal/providers"
	"github.com/daidr/Ariadne/internal/realtime"
	"github.com/daidr/Ariadne/internal/repowatch"
	"github.com/daidr/Ariadne/internal/runner"
	"github.com/daidr/Ariadne/internal/scheduler"
	"github.com/daidr/Ariadne/internal/searches"
	"github.com/daidr/Ariadne/internal/server"
	"github.com/daidr/Ariadne/internal/service"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/internal/sources"
	"github.com/daidr/Ariadne/internal/timers"
	"github.com/daidr/Ariadne/internal/update"
	"github.com/daidr/Ariadne/internal/workspaces"
	"github.com/daidr/Ariadne/sdk"
)

type Options struct {
	Config         config.Config
	Web            fs.FS
	Client         *http.Client
	OnError        func(error)
	Ledger         *process.Ledger
	StdioLauncher  agentruntime.StdioLauncher
	Update         *update.Options
	ServiceRuntime *service.RuntimeInfo
	CanRestart     bool
	OnShutdown     func(restart bool)
}

type Application struct {
	Handler        http.Handler
	Repository     *core.Repository
	ServiceControl *service.Controller
	cleanups       []func(context.Context) error
	cancel         context.CancelFunc
	closeOnce      sync.Once
	done           chan struct{}
	closeErr       error
}

func Open(ctx context.Context, options Options) (_ *Application, err error) {
	cfg := options.Config
	if cfg.Home == "" || cfg.DatabasePath == "" || cfg.Token == "" {
		return nil, errors.New("application home, database and token are required")
	}
	home, err := filepath.Abs(cfg.Home)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(home, 0700); err != nil {
		return nil, err
	}
	life, cancel := context.WithCancel(context.Background())
	app := &Application{cancel: cancel, done: make(chan struct{})}
	defer func() {
		if err != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			err = errors.Join(err, app.Close(cleanup))
		}
	}()
	onError := options.OnError
	if onError == nil {
		onError = func(error) {}
	}
	add := func(close func(context.Context) error) { app.cleanups = append(app.cleanups, close) }
	addSimple := func(close func() error) { add(func(context.Context) error { return close() }) }

	if options.Ledger != nil {
		left, err := options.Ledger.Open(ctx, filepath.Join(home, "processes.json"))
		if err != nil {
			return nil, err
		}
		if left.OwnerAlive {
			return nil, errors.New("another backend already uses this Ariadne home")
		}
		addSimple(options.Ledger.Close)
	}
	repo, err := core.Open(ctx, core.Options{Path: cfg.DatabasePath})
	if err != nil {
		return nil, err
	}
	app.Repository = repo
	addSimple(repo.Close)
	user, err := settings.Open(filepath.Join(home, "config.json"))
	if err != nil {
		return nil, err
	}
	eventHub := realtime.NewHub()
	addSimple(func() error { eventHub.Close(); return nil })
	state := realtime.NewState(eventHub)
	addSimple(func() error { state.Close(); return nil })
	var serviceControlRef atomic.Pointer[service.Controller]
	var updates *update.Service
	version := ""
	if options.Update != nil {
		supplied := *options.Update
		version = supplied.Version
		supplied.OnError = onError
		supplied.OnChange = func(view update.View) {
			_ = state.Set(realtime.Update{Kind: "update", Value: view}, true)
		}
		supplied.OnAutoReady = func(_ update.View) {
			ctrl := serviceControlRef.Load()
			if ctrl == nil || !ctrl.CanRestart() {
				return
			}
			if ctrl.RestartState().Phase != service.RestartPhaseNone {
				return
			}
			_, _ = ctrl.BeginDrain(life, service.DrainRequest{
				Mode:            service.DrainModeWhenIdle,
				Action:          service.ActionRestart,
				WaitBackground:  true,
				AutoShutdown:    true,
				QuietWindowMs:   int64(service.DefaultAutoIdleQuietWindow / time.Millisecond),
				ImmediateIfIdle: false,
			})
		}
		updates = update.New(life, user, supplied)
		add(updates.Close)
	}
	drafts := realtime.NewDrafts(state, realtime.LiveOptions{})
	addSimple(func() error { drafts.Close(); return nil })
	live := realtime.NewLive(state, realtime.LiveOptions{})
	addSimple(func() error { live.Close(); return nil })
	modelRegistry := models.New()
	registry := sources.New()
	dashboards := dashboard.New()
	add(dashboards.Close)
	searchRegistry := searches.New()
	notifierRegistry := notifications.NewRegistry()
	resolver := links.New(links.Options{})
	addSimple(func() error { resolver.Close(); return nil })
	agentBridge := agentruntime.NewToolBridge()
	add(agentBridge.Close)
	host, err := plugins.New(plugins.Options{
		DataRoot: filepath.Join(home, "plugins"), ManagedRoot: home, Models: modelRegistry, Sources: registry, Dashboards: dashboards, Searches: searchRegistry, Links: resolver, Notifiers: notifierRegistry, OnError: onError,
		Settings: user,
	})
	if err != nil {
		return nil, err
	}
	add(host.Close)
	if err := installSources(ctx, host, home, options.Client, addSimple); err != nil {
		return nil, err
	}
	if err := installSearches(ctx, host, options.Client); err != nil {
		return nil, err
	}
	if err := installModels(ctx, host, providers.BuiltinOptions{
		Client:        options.Client,
		Bridge:        agentBridge,
		AgentDataDir:  filepath.Join(home, "plugins", "agents"),
		StdioLauncher: options.StdioLauncher,
	}); err != nil {
		return nil, err
	}
	env := process.Environ()
	mcpHub, err := mcp.NewHub(mcp.HubOptions{Servers: user.MCPServers, Env: func() map[string]string { return process.ThreadEnv(env) }, Context: life, Client: options.Client})
	if err != nil {
		return nil, err
	}
	add(mcpHub.Close)
	manager, err := workspaces.New(workspaces.Options{
		Repository: repo, Sources: registry, Secrets: user.SourceSecrets, State: state,
		Env: func(project string) map[string]string { return stringMap(user.TurnSettings(project)["env"]) },
		CommandRules: func(project string) []commandpolicy.Rule {
			var rules []commandpolicy.Rule
			encoded, _ := json.Marshal(user.TurnSettings(project)["commandRules"])
			_ = json.Unmarshal(encoded, &rules)
			return rules
		},
		SetupTimeout: func(project string) time.Duration {
			limits, _ := user.TurnSettings(project)["limits"].(map[string]any)
			duration, _ := limits["commandTimeoutMax"].(float64)
			return time.Duration(duration * float64(time.Second))
		},
		OnBackgroundChange: func() {
			if ctrl := serviceControlRef.Load(); ctrl != nil {
				ctrl.NotifyBackgroundChange()
			}
		},
	})
	if err != nil {
		return nil, err
	}
	add(manager.Close)
	storage, err := data.New(data.Options{Events: repo.Store, Home: home, DatabasePath: cfg.DatabasePath})
	if err != nil {
		return nil, err
	}
	addSimple(func() error { storage.Close(); return nil })

	userHome, _ := os.UserHomeDir()
	skillsDirs := []string{filepath.Join(home, "skills")}
	if userHome != "" {
		skillsDirs = append(skillsDirs, filepath.Join(userHome, ".agents", "skills"))
	}
	rooms := collab.NewRooms(repo, onError)
	var trackBrowser func(int, bool, string) (func(), error)
	if options.Ledger != nil {
		trackBrowser = options.Ledger.Track
	}
	browsers := browser.New(life, filepath.Join(home, "browser"), trackBrowser)
	add(browsers.Close)
	worker := &runner.Runner{
		Repository: repo, Settings: user, Models: modelRegistry, Drafts: drafts, Live: live,
		Workspaces: manager, MCP: mcpHub, SkillsDirs: skillsDirs, Searches: searchRegistry, HTTPClient: options.Client,
		Rooms: rooms, Dashboards: dashboards, Browser: browsers, Sources: registry, Home: home,
	}
	concurrency := user.Concurrency()
	number := func(key string, fallback int) *int {
		if n, ok := concurrency[key].(float64); ok && n >= 0 && n <= 16 {
			return new(int(n))
		}
		return &fallback
	}
	schedule, err := scheduler.New(scheduler.Options{
		Repository: repo, Slots: number("slots", 3), ReservedSlots: number("reserved", 1), Run: worker.Run, OnError: onError,
		OnThreadStopped: func(thread string) {
			manager.Background.CancelThread(thread, false)
			browsers.DropThread(thread)
		},
		OnThreadFinished: func(thread string, keep bool) {
			manager.Background.CancelThread(thread, keep)
			browsers.DropThread(thread)
		},
	})
	if err != nil {
		return nil, err
	}
	worker.ShouldSuspend = schedule.ShouldSuspendStep
	worker.OnModelResolved = schedule.SetTurnModel
	runtimeMeta := service.RuntimeInfo{
		PID:       os.Getpid(),
		Version:   version,
		Host:      cfg.Host,
		Port:      cfg.Port,
		StartedAt: time.Now().UnixMilli(),
		ManagedBy: service.ManagedDirect,
	}
	if options.ServiceRuntime != nil {
		runtimeMeta = *options.ServiceRuntime
	}
	serviceControl := service.NewController(service.ControllerOptions{
		Runtime:    runtimeMeta,
		CanRestart: options.CanRestart,
		Scheduler:  schedule,
		Background: manager.Background,
		Repository: repo,
		OnShutdown: options.OnShutdown,
		OnStateChange: func(status service.RestartState) {
			if updates != nil {
				updates.SetRestartState(update.RestartStatus{
					CanRestart:       status.CanRestart,
					Phase:            status.Phase,
					ActiveTurns:      status.ActiveTurns,
					ActiveBackground: status.ActiveBackground,
				})
			}
		},
	})
	serviceControlRef.Store(serviceControl)
	if updates != nil {
		_ = state.Set(realtime.Update{Kind: "update", Value: updates.View()}, true)
	}
	rooms.SetOnUserEdit(serviceControl.RecordUserActivity)
	app.ServiceControl = serviceControl
	addSimple(func() error { serviceControl.Close(); return nil })
	daily := &data.Daily{Store: storage, OnError: onError}
	alarms, err := timers.New(timers.Options{Repository: repo, OnError: onError})
	if err != nil {
		return nil, err
	}
	notifierService, err := notifications.NewService(notifications.Options{
		Repository: repo,
		Settings:   user,
		Notifiers:  notifierRegistry,
		HTTPClient: options.Client,
		OnError:    onError,
	})
	if err != nil {
		return nil, err
	}
	janitor := workspaces.NewJanitor(manager, workspaces.JanitorOptions{OnError: onError})
	api, err := server.New(server.Options{
		Repository: repo, Token: cfg.Token, Version: version, Web: options.Web, Scheduler: schedule, State: state,
		Data: storage, Settings: user, Models: modelRegistry, Sources: registry,
		Updates: updates, ServiceControl: serviceControl,
		Links: resolver, MCP: mcpHub, Workspaces: manager, SkillsDirs: skillsDirs, Dashboards: dashboards,
		Searches: searchRegistry, Notifiers: notifierRegistry, Notifications: notifierService, Plugins: host, HTTPClient: options.Client, OnError: onError,
		Rooms:       rooms,
		FindContext: &sdk.FindContext{Env: env, Home: userHome, Which: func(name string) bool { _, err := exec.LookPath(name); return err == nil }},
		ModelOf: func(r *http.Request, conversation core.ConversationView) (any, error) {
			model := ""
			effort := ""
			if conversation.Role == "thread" {
				thread, err := repo.GetThread(r.Context(), conversation.ID)
				if err != nil {
					return nil, err
				}
				model, _ = thread.Model.(string)
				effort, _ = thread.Effort.(string)
			}
			var overrides *projectsettings.Models
			if project, err := repo.GetProject(r.Context(), conversation.ProjectID); err == nil {
				overrides = &project.Settings.Models
			}
			return user.ConversationModelForProject(conversation.Role, model, effort, overrides), nil
		},
		OnProjectDeleted: func(id string) {
			drafts.DropProject(id)
			live.DropProject(id)
			browsers.DropProject(id)
		},
	})
	if err != nil {
		return nil, err
	}
	app.Handler = api
	addSimple(func() error { api.Close(); return nil })
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := schedule.Start(life); err != nil {
		return nil, err
	}
	add(schedule.Stop)
	feedbackDone := make(chan struct{})
	go func() {
		defer close(feedbackDone)
		rooms.RunFeedback(life)
	}()
	add(func(ctx context.Context) error {
		select {
		case <-feedbackDone:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	if err := alarms.Start(life); err != nil {
		return nil, err
	}
	add(alarms.Stop)
	if err := notifierService.Start(life); err != nil {
		return nil, err
	}
	add(notifierService.Stop)
	if err := janitor.Start(life); err != nil {
		return nil, err
	}
	add(janitor.Stop)
	daily.Start(life)
	add(daily.Stop)
	pulsePruner := &core.PulsePruner{Repository: repo, OnError: onError}
	pulsePruner.Start(life)
	add(pulsePruner.Stop)
	watcher := repowatch.Watcher{Repository: repo, ThreadRepo: manager.RepoForThread, OnError: onError}
	watchDone := make(chan struct{})
	interval := time.Minute
	if ms, err := strconv.Atoi(os.Getenv("ARIADNE_REPO_POLL_MS")); err == nil && ms > 0 && ms <= 24*60*60*1000 {
		interval = time.Duration(ms) * time.Millisecond
	}
	go func() {
		defer close(watchDone)
		watcher.Run(life, interval)
	}()
	add(func(ctx context.Context) error {
		select {
		case <-watchDone:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	drive := &autopilot.Service{
		Repository: repo, Workspaces: manager, Locale: user.Locale, OnError: onError,
		Slots: func() int {
			slots, ok := user.Concurrency()["slots"].(float64)
			if ok && slots >= 0 && slots <= 16 {
				return int(slots)
			}
			return 3
		},
	}
	driveDone := make(chan struct{})
	go func() {
		defer close(driveDone)
		drive.Run(life)
	}()
	add(func(ctx context.Context) error {
		select {
		case <-driveDone:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	return app, nil
}

func installSources(ctx context.Context, host *plugins.Host, home string, client *http.Client, add func(func() error)) error {
	dataDir := filepath.Join(home, "plugins")
	gitlabOptions := gitlab.Options{DataDir: filepath.Join(dataDir, "gitlab"), Client: client, Instances: gitlab.NewInstances(nil)}
	add(gitlabOptions.Instances.Close)
	githubOptions := github.Options{DataDir: filepath.Join(dataDir, "github"), Client: client}
	githubReady, closeGitHubReady := github.PluginReadiness(githubOptions)
	add(closeGitHubReady)
	localProvider := local.DirectoryProvider(filepath.Join(dataDir, "local"), nil, false)
	entries := []struct {
		id        string
		name      sdk.LocalizedText
		provides  sdk.PluginProvides
		settings  *sdk.SettingsSchema
		ready     func(context.Context, map[string]any) bool
		validate  func(map[string]any) error
		source    func() sdk.ContextProvider
		link      func() sdk.LinkProvider
		configure func(func() map[string]any)
	}{
		{id: "local", name: localProvider.Info.Title, provides: sdk.PluginProvides{Contexts: []string{localProvider.Info.Kind}}, source: func() sdk.ContextProvider { return localProvider }},
		{id: "github", name: sdk.LocalizedText{"zh-CN": "GitHub", "en": "GitHub"}, provides: sdk.PluginProvides{Contexts: []string{"github"}, Links: []string{"github"}}, settings: github.PluginSchema(),
			ready:  githubReady,
			source: func() sdk.ContextProvider { return github.Provider(githubOptions) }, link: func() sdk.LinkProvider { return github.Links(githubOptions) },
			configure: func(values func() map[string]any) { githubOptions.PluginSettings = values }},
		{id: "gitlab", name: sdk.LocalizedText{"zh-CN": "GitLab", "en": "GitLab"}, provides: sdk.PluginProvides{Contexts: []string{"gitlab"}, Links: []string{"gitlab"}}, settings: gitlab.PluginSchema(),
			validate: gitlab.ValidatePluginSettings,
			source:   func() sdk.ContextProvider { return gitlab.Provider(gitlabOptions) }, link: func() sdk.LinkProvider { return gitlab.Links(gitlabOptions) },
			configure: func(values func() map[string]any) { gitlabOptions.PluginSettings = values }},
	}
	for _, entry := range entries {
		plugin := sdk.Plugin{ID: entry.id, Name: entry.name, Provides: entry.provides, Settings: entry.settings, SettingsReady: entry.ready, ValidateSettings: entry.validate, Setup: func(_ context.Context, pc sdk.PluginContext) (func(context.Context) error, error) {
			if entry.configure != nil {
				entry.configure(pc.Settings)
			}
			if _, err := pc.RegisterContext(entry.source()); err != nil {
				return nil, err
			}
			if entry.link != nil {
				link := entry.link()
				if _, err := pc.RegisterLink(link); err != nil {
					if link.Close != nil {
						_ = link.Close()
					}
					return nil, err
				}
			}
			return nil, nil
		}}
		if err := host.Load(ctx, plugin, nil); err != nil {
			return fmt.Errorf("load %s source: %w", entry.id, err)
		}
	}
	return nil
}

func installSearches(ctx context.Context, host *plugins.Host, client *http.Client) error {
	for _, plugin := range searches.BuiltinPlugins(client) {
		if err := host.Load(ctx, plugin, nil); err != nil {
			return fmt.Errorf("load %s search: %w", plugin.ID, err)
		}
	}
	return nil
}

func installModels(ctx context.Context, host *plugins.Host, opts providers.BuiltinOptions) error {
	for _, plugin := range providers.BuiltinPlugins(opts) {
		if err := host.Load(ctx, plugin, nil); err != nil {
			return fmt.Errorf("load %s model: %w", plugin.ID, err)
		}
	}
	return nil
}

func stringMap(value any) map[string]string {
	table, _ := value.(map[string]any)
	out := map[string]string{}
	for key, value := range table {
		if text, ok := value.(string); ok {
			out[key] = text
		}
	}
	return out
}

func (app *Application) Close(ctx context.Context) error {
	app.closeOnce.Do(func() {
		app.cancel()
		go func() {
			defer close(app.done)
			for i := len(app.cleanups) - 1; i >= 0; i-- {
				cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				app.closeErr = errors.Join(app.closeErr, app.cleanups[i](cleanup))
				cancel()
			}
			app.cleanups = nil
		}()
	})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-app.done:
		return app.closeErr
	}
}
