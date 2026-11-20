// Package plugins owns provider registration and native/Lua plugin
// lifetimes. A failed startup leaves no providers behind.
package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/daidr/Ariadne/internal/dashboard"
	"github.com/daidr/Ariadne/internal/links"
	"github.com/daidr/Ariadne/internal/models"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/internal/sources"
	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/sdk"
)

const MaxPlugins = 128

var pluginID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type Provides struct {
	Models     []string `json:"models"`
	Contexts   []string `json:"contexts"`
	Dashboards []string `json:"dashboards"`
	Searches   []string `json:"searches"`
	Links      []string `json:"links"`
}

func emptyProvides() Provides {
	return Provides{[]string{}, []string{}, []string{}, []string{}, []string{}}
}

type Record struct {
	ID           *string           `json:"id"`
	Name         sdk.LocalizedText `json:"name"`
	Source       string            `json:"source"`
	Path         *string           `json:"path"`
	Status       string            `json:"status"`
	Error        *string           `json:"error"`
	Provides     Provides          `json:"provides"`
	Configurable bool              `json:"configurable,omitempty"`
}
type Options struct {
	DataRoot    string
	ManagedRoot string
	Models      *models.Registry
	Sources     *sources.Registry
	Dashboards  *dashboard.Registry
	Links       *links.Resolver
	OnError     func(error)
	Settings    *settings.Store
}
type loaded struct {
	record   Record
	file     string
	dispose  []func(context.Context) error
	cancel   context.CancelFunc
	schema   *sdk.SettingsSchema
	ready    func(context.Context, map[string]any) bool
	validate func(map[string]any) error
}
type Host struct {
	options     Options
	mu          sync.Mutex
	serial      chan struct{}
	life        context.Context
	cancel      context.CancelFunc
	loaded      []*loaded
	failed      []Record
	searches    map[string]*sdk.SearchProvider
	searchOrder []string
	closing     bool
	done        chan struct{}
	closeOnce   sync.Once
}

func New(options Options) (*Host, error) {
	if options.DataRoot == "" || !filepath.IsAbs(options.DataRoot) || options.Models == nil || options.Sources == nil || options.Dashboards == nil || options.Links == nil {
		return nil, errors.New("plugin host needs an absolute data root and provider registries")
	}
	if err := os.MkdirAll(options.DataRoot, 0700); err != nil {
		return nil, err
	}
	life, cancel := context.WithCancel(context.Background())
	return &Host{options: options, serial: make(chan struct{}, 1), life: life, cancel: cancel, searches: map[string]*sdk.SearchProvider{}, done: make(chan struct{})}, nil
}
func (host *Host) lock(ctx context.Context) (func(), error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case host.serial <- struct{}{}:
	}
	host.mu.Lock()
	closed := host.closing
	host.mu.Unlock()
	if closed {
		<-host.serial
		return nil, errors.New("plugin host is closed")
	}
	return func() { <-host.serial }, nil
}
func copyRecord(record Record) Record {
	data, _ := json.Marshal(record)
	var out Record
	json.Unmarshal(data, &out)
	return out
}
func (host *Host) Records() []Record {
	host.mu.Lock()
	defer host.mu.Unlock()
	out := []Record{}
	for _, entry := range host.loaded {
		out = append(out, copyRecord(entry.record))
	}
	for _, entry := range host.failed {
		out = append(out, copyRecord(entry))
	}
	return out
}
func (host *Host) report(err error) {
	if err != nil && host.options.OnError != nil {
		func() { defer func() { recover() }(); host.options.OnError(err) }()
	}
}
func dispose(ctx context.Context, functions []func(context.Context) error) error {
	errs := []error{}
	for i := len(functions) - 1; i >= 0; i-- {
		fn := functions[i]
		functions[i] = nil
		if fn == nil {
			continue
		}
		func() {
			defer func() {
				if cause := recover(); cause != nil {
					errs = append(errs, fmt.Errorf("plugin cleanup failed: %v", cause))
				}
			}()
			if err := fn(ctx); err != nil {
				errs = append(errs, err)
			}
		}()
	}
	return errors.Join(errs...)
}

type setupState struct {
	mu         sync.Mutex
	closed     bool
	disposers  []func(context.Context) error
	firstError error
	provides   Provides
}

func register[T any](state *setupState, provider T, kind string, names *[]string, install func(T) (func(), error)) (func(), error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed {
		return nil, errors.New("plugin setup has ended")
	}
	if state.firstError != nil {
		return nil, state.firstError
	}
	if len(*names) >= 128 {
		state.firstError = errors.New("too many providers registered by one plugin")
		return nil, state.firstError
	}
	remove, err := install(provider)
	if err != nil {
		if state.firstError == nil {
			state.firstError = err
		}
		return nil, err
	}
	*names = append(*names, kind)
	state.disposers = append(state.disposers, func(context.Context) error { remove(); return nil })
	return remove, nil
}
func (host *Host) Load(ctx context.Context, plugin sdk.Plugin, path *string) (err error) {
	unlock, err := host.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	return host.load(ctx, plugin, path)
}
func (host *Host) load(ctx context.Context, plugin sdk.Plugin, path *string) (err error) {
	if !pluginID.MatchString(plugin.ID) || plugin.Setup == nil {
		return errors.New("plugin requires a safe id and setup")
	}
	schema, err := checkedSettingsSchema(plugin.Settings)
	if err != nil {
		return err
	}
	if data, err := json.Marshal(plugin.Name); err != nil {
		return err
	} else if len(data) > 16384 {
		return errors.New("plugin name metadata is too large")
	}
	host.mu.Lock()
	if len(host.loaded) >= MaxPlugins {
		host.mu.Unlock()
		return errors.New("too many loaded plugins")
	}
	for _, entry := range host.loaded {
		if entry.record.ID != nil && *entry.record.ID == plugin.ID {
			host.mu.Unlock()
			return fmt.Errorf("plugin %s is already loaded", plugin.ID)
		}
	}
	host.mu.Unlock()
	life, cancel := context.WithCancel(host.life)
	state := &setupState{provides: emptyProvides()}
	complete := false
	defer func() {
		if cause := recover(); cause != nil {
			err = fmt.Errorf("plugin startup failed: %v", cause)
		}
		if !complete {
			cancel()
			state.mu.Lock()
			state.closed = true
			functions := state.disposers
			state.disposers = nil
			state.mu.Unlock()
			cleanup, cancelCleanup := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancelCleanup()
			host.report(dispose(cleanup, functions))
		}
	}()
	dataDir := filepath.Join(host.options.DataRoot, plugin.ID)
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return err
	}
	pc := sdk.PluginContext{Context: life, DataDir: dataDir}
	store := host.options.Settings
	pc.Settings = func() map[string]any { return pluginValues(store, plugin.ID, schema) }
	pc.OnDispose = func(cleanup func(context.Context) error) error {
		state.mu.Lock()
		defer state.mu.Unlock()
		if state.closed {
			return errors.New("plugin setup has ended")
		}
		if cleanup == nil {
			return errors.New("plugin cleanup is required")
		}
		if len(state.disposers) >= 1024 {
			state.firstError = errors.New("too many plugin resources")
			return state.firstError
		}
		state.disposers = append(state.disposers, cleanup)
		return nil
	}
	pc.RegisterModel = func(provider sdk.ModelProvider) (func(), error) {
		return register(state, provider, provider.Info.Kind, &state.provides.Models, host.options.Models.Register)
	}
	pc.RegisterContext = func(provider sdk.ContextProvider) (func(), error) {
		if provider.NeedsPluginSettings {
			if schema == nil || (len(schema.Required) == 0 && plugin.SettingsReady == nil) {
				return nil, errors.New("a source that requires plugin settings needs required fields or a readiness check")
			}
			provider.Info.Plugin = &sdk.SourcePlugin{ID: plugin.ID}
		}
		return register(state, provider, provider.Info.Kind, &state.provides.Contexts, host.options.Sources.Register)
	}
	pc.RegisterDashboard = func(provider sdk.DashboardSource) (func(), error) {
		return register(state, provider, provider.Info.Kind, &state.provides.Dashboards, host.options.Dashboards.Register)
	}
	pc.RegisterSearch = func(provider sdk.SearchProvider) (func(), error) {
		return register(state, provider, provider.Kind, &state.provides.Searches, host.registerSearch)
	}
	pc.RegisterLink = func(provider sdk.LinkProvider) (func(), error) {
		return register(state, provider, provider.Kind, &state.provides.Links, host.options.Links.Register)
	}
	startup, cancelStartup := context.WithTimeout(ctx, 30*time.Second)
	stop := context.AfterFunc(life, cancelStartup)
	defer stop()
	defer cancelStartup()
	cleanup, err := plugin.Setup(startup, pc)
	state.mu.Lock()
	state.closed = true
	if cleanup != nil {
		state.disposers = append(state.disposers, cleanup)
	}
	if err == nil {
		err = state.firstError
	}
	state.mu.Unlock()
	if err != nil {
		return err
	}
	if err := startup.Err(); err != nil {
		return err
	}
	record := Record{ID: new(plugin.ID), Name: plugin.Name, Source: "builtin", Status: "loaded", Provides: state.provides, Configurable: schema != nil}
	if path != nil {
		record.Source = "path"
		record.Path = new(*path)
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.closing {
		return errors.New("plugin host is closed")
	}
	host.loaded = append(host.loaded, &loaded{record: copyRecord(record), dispose: state.disposers, cancel: cancel, schema: schema, ready: plugin.SettingsReady, validate: plugin.ValidateSettings})
	state.disposers = nil
	complete = true
	return nil
}
func (host *Host) registerSearch(provider sdk.SearchProvider) (func(), error) {
	if provider.Kind == "" || provider.Search == nil {
		return nil, errors.New("search provider needs kind and search")
	}
	data, err := json.Marshal(provider.Title)
	if err != nil {
		return nil, err
	}
	json.Unmarshal(data, &provider.Title)
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.searches[provider.Kind] != nil {
		return nil, fmt.Errorf("search provider is already registered: %s", provider.Kind)
	}
	entry := &provider
	host.searches[provider.Kind] = entry
	host.searchOrder = append(host.searchOrder, provider.Kind)
	var once sync.Once
	return func() {
		once.Do(func() {
			host.mu.Lock()
			defer host.mu.Unlock()
			if host.searches[provider.Kind] == entry {
				delete(host.searches, provider.Kind)
				host.searchOrder = slices.DeleteFunc(host.searchOrder, func(kind string) bool { return kind == provider.Kind })
			}
		})
	}, nil
}
func (host *Host) SearchProviders() []sdk.SearchProvider {
	host.mu.Lock()
	defer host.mu.Unlock()
	out := []sdk.SearchProvider{}
	for _, kind := range host.searchOrder {
		provider := *host.searches[kind]
		provider.Title = map[string]string{}
		for key, value := range host.searches[kind].Title {
			provider.Title[key] = value
		}
		out = append(out, provider)
	}
	return out
}
func (host *Host) UnloadPath(ctx context.Context, path string) error {
	unlock, err := host.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	return host.unloadPath(path)
}

func (host *Host) unloadPath(path string) error {
	host.mu.Lock()
	host.failed = slices.DeleteFunc(host.failed, func(record Record) bool { return record.Path != nil && *record.Path == path })
	var removed *loaded
	for i, entry := range host.loaded {
		if entry.record.Path != nil && *entry.record.Path == path {
			removed = entry
			host.loaded = slices.Delete(host.loaded, i, i+1)
			break
		}
	}
	host.mu.Unlock()
	if removed == nil {
		return nil
	}
	removed.cancel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := dispose(ctx, removed.dispose)
	removed.dispose = nil
	removed.record = Record{}
	return err
}
func (host *Host) Close(ctx context.Context) error {
	host.closeOnce.Do(func() {
		host.mu.Lock()
		host.closing = true
		host.cancel()
		host.mu.Unlock()
		go func() {
			host.serial <- struct{}{}
			defer func() { <-host.serial; close(host.done) }()
			host.mu.Lock()
			loaded := host.loaded
			host.loaded = nil
			host.failed = nil
			host.mu.Unlock()
			for i := len(loaded) - 1; i >= 0; i-- {
				entry := loaded[i]
				entry.cancel()
				cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				host.report(dispose(cleanup, entry.dispose))
				cancel()
				entry.dispose = nil
				entry.record = Record{}
			}
			host.mu.Lock()
			clear(host.searches)
			host.searchOrder = nil
			host.options = Options{}
			host.mu.Unlock()
		}()
	})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-host.done:
		return nil
	}
}
func (host *Host) rememberFailure(path string, err error) {
	message := strings.Clone(textutil.HeadUnits(err.Error(), 1024))
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.closing {
		return
	}
	host.failed = slices.DeleteFunc(host.failed, func(record Record) bool { return record.Path != nil && *record.Path == path })
	if len(host.failed) >= MaxPlugins {
		host.failed = slices.Delete(host.failed, 0, 1)
	}
	host.failed = append(host.failed, Record{Source: "path", Path: new(strings.Clone(textutil.HeadUnits(path, 32768))), Status: "failed", Error: &message, Provides: emptyProvides()})
}
