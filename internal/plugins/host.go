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
	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/links"
	"github.com/daidr/Ariadne/internal/models"
	"github.com/daidr/Ariadne/internal/searches"
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
	Searches    *searches.Registry
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
type builtinEntry struct {
	plugin   sdk.Plugin
	provides Provides
	loaded   *loaded
}
type Host struct {
	options   Options
	mu        sync.Mutex
	serial    chan struct{}
	life      context.Context
	cancel    context.CancelFunc
	builtins  []*builtinEntry
	loaded    []*loaded
	failed    []Record
	closing   bool
	done      chan struct{}
	closeOnce sync.Once
}

func New(options Options) (*Host, error) {
	if options.DataRoot == "" || !filepath.IsAbs(options.DataRoot) || options.Models == nil || options.Sources == nil || options.Dashboards == nil || options.Links == nil {
		return nil, errors.New("plugin host needs an absolute data root and provider registries")
	}
	if options.Searches == nil {
		options.Searches = searches.New()
	}
	if err := os.MkdirAll(options.DataRoot, 0700); err != nil {
		return nil, err
	}
	life, cancel := context.WithCancel(context.Background())
	return &Host{options: options, serial: make(chan struct{}, 1), life: life, cancel: cancel, done: make(chan struct{})}, nil
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
func cloneLocalized(value sdk.LocalizedText) sdk.LocalizedText {
	if value == nil {
		return nil
	}
	out := make(sdk.LocalizedText, len(value))
	for key, text := range value {
		out[key] = text
	}
	return out
}
func copyStrings(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	return slices.Clone(values)
}
func copyProvides(provides Provides) Provides {
	return Provides{
		Models:     copyStrings(provides.Models),
		Contexts:   copyStrings(provides.Contexts),
		Dashboards: copyStrings(provides.Dashboards),
		Searches:   copyStrings(provides.Searches),
		Links:      copyStrings(provides.Links),
	}
}
func providesFromPlugin(provides sdk.PluginProvides) Provides {
	return Provides{
		Models:     copyStrings(provides.Models),
		Contexts:   copyStrings(provides.Contexts),
		Dashboards: copyStrings(provides.Dashboards),
		Searches:   copyStrings(provides.Searches),
		Links:      copyStrings(provides.Links),
	}
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
	for _, entry := range host.builtins {
		if entry.loaded != nil {
			out = append(out, copyRecord(entry.loaded.record))
		} else {
			out = append(out, copyRecord(Record{
				ID:       new(entry.plugin.ID),
				Name:     cloneLocalized(entry.plugin.Name),
				Source:   "builtin",
				Status:   "disabled",
				Provides: copyProvides(entry.provides),
			}))
		}
	}
	for _, entry := range host.loaded {
		if entry.record.Source == "builtin" {
			continue
		}
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
	for _, entry := range host.loaded {
		if entry.record.ID != nil && *entry.record.ID == plugin.ID {
			host.mu.Unlock()
			return fmt.Errorf("plugin %s is already loaded", plugin.ID)
		}
	}
	for _, entry := range host.builtins {
		if entry.plugin.ID == plugin.ID {
			host.mu.Unlock()
			return fmt.Errorf("plugin %s is already loaded", plugin.ID)
		}
	}
	host.mu.Unlock()

	if path == nil {
		owned := plugin
		owned.Name = cloneLocalized(plugin.Name)
		owned.Settings = schema
		if host.options.Settings != nil && host.options.Settings.IsPluginDisabled(plugin.ID) {
			host.mu.Lock()
			defer host.mu.Unlock()
			if host.closing {
				return errors.New("plugin host is closed")
			}
			host.builtins = append(host.builtins, &builtinEntry{plugin: owned, provides: providesFromPlugin(owned.Provides)})
			return nil
		}
		entry, err := host.start(ctx, owned, schema, nil)
		if err != nil {
			return err
		}
		host.mu.Lock()
		if host.closing {
			host.mu.Unlock()
			host.disposeLoaded(entry)
			return errors.New("plugin host is closed")
		}
		host.loaded = append(host.loaded, entry)
		host.builtins = append(host.builtins, &builtinEntry{plugin: owned, provides: copyProvides(entry.record.Provides), loaded: entry})
		host.mu.Unlock()
		return nil
	}

	entry, err := host.start(ctx, plugin, schema, path)
	if err != nil {
		return err
	}
	host.mu.Lock()
	if host.closing {
		host.mu.Unlock()
		host.disposeLoaded(entry)
		return errors.New("plugin host is closed")
	}
	host.loaded = append(host.loaded, entry)
	host.mu.Unlock()
	return nil
}

func (host *Host) disposeLoaded(entry *loaded) error {
	if entry == nil {
		return nil
	}
	entry.cancel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := dispose(ctx, entry.dispose)
	entry.dispose = nil
	entry.record = Record{}
	return err
}

func (host *Host) start(ctx context.Context, plugin sdk.Plugin, schema *sdk.SettingsSchema, path *string) (entry *loaded, err error) {
	host.mu.Lock()
	if len(host.loaded) >= MaxPlugins {
		host.mu.Unlock()
		return nil, errors.New("too many loaded plugins")
	}
	host.mu.Unlock()
	life, cancel := context.WithCancel(host.life)
	state := &setupState{provides: emptyProvides()}
	complete := false
	defer func() {
		if cause := recover(); cause != nil {
			err = fmt.Errorf("plugin startup failed: %v", cause)
			entry = nil
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
		return nil, err
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
		return register(state, provider, provider.Kind, &state.provides.Searches, host.options.Searches.Register)
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
		return nil, err
	}
	if err := startup.Err(); err != nil {
		return nil, err
	}
	record := Record{ID: new(plugin.ID), Name: cloneLocalized(plugin.Name), Source: "builtin", Status: "loaded", Provides: state.provides, Configurable: schema != nil}
	if path != nil {
		record.Source = "path"
		record.Path = new(*path)
	}
	entry = &loaded{record: copyRecord(record), dispose: state.disposers, cancel: cancel, schema: schema, ready: plugin.SettingsReady, validate: plugin.ValidateSettings}
	state.disposers = nil
	complete = true
	return entry, nil
}

func (host *Host) SetBuiltinEnabled(ctx context.Context, store *settings.Store, id string, enabled bool) error {
	if !pluginID.MatchString(id) {
		return domainerr.NotFound(id)
	}
	unlock, err := host.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	host.mu.Lock()
	var target *builtinEntry
	for _, entry := range host.builtins {
		if entry.plugin.ID == id {
			target = entry
			break
		}
	}
	host.mu.Unlock()
	if target == nil {
		return domainerr.NotFound(id)
	}
	if enabled {
		if target.loaded != nil {
			if store != nil {
				return store.SetPluginDisabled(id, false)
			}
			return nil
		}
		started, err := host.start(ctx, target.plugin, target.plugin.Settings, nil)
		if err != nil {
			var localized *domainerr.Error
			if errors.As(err, &localized) {
				return localized
			}
			return domainerr.New("error.pluginLoad", map[string]any{"detail": strings.Clone(textutil.HeadUnits(err.Error(), 1024))})
		}
		if err := ctx.Err(); err != nil {
			host.report(host.disposeLoaded(started))
			return err
		}
		if store != nil {
			if err := store.SetPluginDisabled(id, false); err != nil {
				host.report(host.disposeLoaded(started))
				return err
			}
		}
		host.mu.Lock()
		if host.closing {
			host.mu.Unlock()
			host.report(host.disposeLoaded(started))
			return errors.New("plugin host is closed")
		}
		target.loaded = started
		target.provides = copyProvides(started.record.Provides)
		host.loaded = append(host.loaded, started)
		host.mu.Unlock()
		return nil
	}
	if target.loaded == nil {
		if store != nil {
			return store.SetPluginDisabled(id, true)
		}
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if store != nil {
		if err := store.SetPluginDisabled(id, true); err != nil {
			return err
		}
	}
	host.mu.Lock()
	removed := target.loaded
	target.loaded = nil
	host.loaded = slices.DeleteFunc(host.loaded, func(item *loaded) bool { return item == removed })
	host.mu.Unlock()
	host.report(host.disposeLoaded(removed))
	return nil
}
func (host *Host) Searches() *searches.Registry {
	host.mu.Lock()
	defer host.mu.Unlock()
	return host.options.Searches
}
func (host *Host) SearchProviders() []sdk.SearchProvider {
	registry := host.Searches()
	if registry == nil {
		return []sdk.SearchProvider{}
	}
	return registry.Providers()
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
			host.builtins = nil
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
