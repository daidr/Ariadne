// Package dashboard fetches versioned dashboard data from registered sources.
package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/daidr/Ariadne/internal/jsonutil"
	"github.com/daidr/Ariadne/sdk"
)

const MaxFetchedUnits = 2 << 20

func copyInfo(info sdk.DashboardSourceInfo) sdk.DashboardSourceInfo {
	data, _ := json.Marshal(info)
	var copy sdk.DashboardSourceInfo
	json.Unmarshal(data, &copy)
	return copy
}
func fieldOrder(schema sdk.SettingsSchema) []string {
	keys := slices.Clone(schema.FieldOrder)
	seen := map[string]bool{}
	out := []string{}
	for _, key := range keys {
		if _, ok := schema.Properties[key]; ok && !seen[key] {
			seen[key] = true
			out = append(out, key)
		}
	}
	keys = nil
	for key := range schema.Properties {
		if !seen[key] {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	return append(out, keys...)
}
func CheckSettings(source sdk.DashboardSourceInfo, value any) (map[string]any, error) {
	input := map[string]any{}
	if value != nil {
		var ok bool
		input, ok = value.(map[string]any)
		if !ok {
			return nil, errors.New(`"source.settings" must be an object`)
		}
	}
	keys := []string{}
	for key := range input {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	settings := map[string]any{}
	for _, key := range keys {
		given := input[key]
		field, known := source.Settings.Properties[key]
		if !known {
			suffix := ""
			if names := fieldOrder(source.Settings); len(names) > 0 {
				suffix = "; it takes: " + strings.Join(names, ", ")
			}
			return nil, fmt.Errorf(`"source.settings.%s" is not a setting of %s%s`, key, source.Kind, suffix)
		}
		if field.Type == "string" && field.Secret {
			return nil, fmt.Errorf(`"source.settings.%s" is a secret and cannot be set here`, key)
		}
		if given == nil {
			continue
		}
		typeName := field.Type
		if typeName == "enum" {
			typeName = "string"
		}
		valid := false
		switch typeName {
		case "string":
			_, valid = given.(string)
		case "boolean":
			_, valid = given.(bool)
		case "number":
			switch number := given.(type) {
			case float64:
				valid = !math.IsNaN(number) && !math.IsInf(number, 0)
			case int, int64:
				valid = true
			}
		}
		if !valid {
			return nil, fmt.Errorf(`"source.settings.%s" must be a %s`, key, typeName)
		}
		if field.Type == "enum" && !slices.Contains(field.Options, given.(string)) {
			return nil, fmt.Errorf(`"source.settings.%s" must be one of: %s`, key, strings.Join(field.Options, ", "))
		}
		settings[key] = given
	}
	for _, key := range fieldOrder(source.Settings) {
		field := source.Settings.Properties[key]
		if settings[key] == nil && field.Default != nil {
			if field.Type == "string" && field.Secret {
				return nil, fmt.Errorf(`"source.settings.%s" is a secret and cannot be set here`, key)
			}
			settings[key] = field.Default
		}
	}
	for _, key := range source.Settings.Required {
		if settings[key] == nil || settings[key] == "" {
			return nil, fmt.Errorf(`"source.settings.%s" is required`, key)
		}
	}
	return settings, nil
}

type registration struct {
	source sdk.DashboardSource
	life   context.Context
	cancel context.CancelFunc
}
type Registry struct {
	mu      sync.Mutex
	sources map[string]*registration
	order   []string
	jobs    sync.WaitGroup
	closed  bool
	done    chan struct{}
}

func New() *Registry {
	return &Registry{sources: map[string]*registration{}, done: make(chan struct{})}
}
func (registry *Registry) Register(source sdk.DashboardSource) (func(), error) {
	if source.Info.Kind == "" || source.Fetch == nil {
		return nil, errors.New("dashboard source requires kind and fetch")
	}
	if _, err := json.Marshal(source.Info); err != nil {
		return nil, fmt.Errorf("invalid dashboard source metadata: %w", err)
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.closed {
		return nil, errors.New("dashboard sources are closed")
	}
	if registry.sources[source.Info.Kind] != nil {
		return nil, fmt.Errorf("dashboard source kind is already registered: %s", source.Info.Kind)
	}
	source.Info = copyInfo(source.Info)
	life, cancel := context.WithCancel(context.Background())
	entry := &registration{source, life, cancel}
	registry.sources[source.Info.Kind] = entry
	registry.order = append(registry.order, source.Info.Kind)
	var once sync.Once
	return func() {
		once.Do(func() {
			registry.mu.Lock()
			defer registry.mu.Unlock()
			if registry.sources[source.Info.Kind] == entry {
				delete(registry.sources, source.Info.Kind)
				registry.order = slices.DeleteFunc(registry.order, func(kind string) bool { return kind == source.Info.Kind })
			}
			cancel()
		})
	}, nil
}
func (registry *Registry) Describe() []sdk.DashboardSourceInfo {
	out := []sdk.DashboardSourceInfo{}
	if registry == nil {
		return out
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	for _, kind := range registry.order {
		out = append(out, copyInfo(registry.sources[kind].source.Info))
	}
	return out
}
func (registry *Registry) Info(kind string) *sdk.DashboardSourceInfo {
	if registry == nil {
		return nil
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if entry := registry.sources[kind]; entry != nil {
		info := copyInfo(entry.source.Info)
		return &info
	}
	return nil
}
func (registry *Registry) Fetch(ctx context.Context, kind string, settings map[string]any, project string, timeout time.Duration) (data map[string]any, err error) {
	if registry == nil {
		return nil, errors.New("dashboard source is not registered")
	}
	registry.mu.Lock()
	entry := registry.sources[kind]
	registry.mu.Unlock()
	return registry.fetch(ctx, entry, settings, project, timeout)
}

// Get binds validation and fetching to the same registration. A replacement of
// the same kind cannot silently consume settings validated against its predecessor.
func (registry *Registry) Get(kind string) *sdk.DashboardSource {
	if registry == nil {
		return nil
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	entry := registry.sources[kind]
	if registry.closed || entry == nil {
		return nil
	}
	return &sdk.DashboardSource{Info: copyInfo(entry.source.Info), Fetch: func(ctx context.Context, settings map[string]any, project string) (map[string]any, error) {
		return registry.fetch(ctx, entry, settings, project, 0)
	}}
}
func (registry *Registry) fetch(ctx context.Context, entry *registration, settings map[string]any, project string, timeout time.Duration) (data map[string]any, err error) {
	registry.mu.Lock()
	if registry.closed || entry == nil || registry.sources[entry.source.Info.Kind] != entry {
		registry.mu.Unlock()
		return nil, errors.New("dashboard source is not registered")
	}
	registry.jobs.Add(1)
	registry.mu.Unlock()
	defer registry.jobs.Done()
	if timeout <= 0 || timeout > 30*time.Second {
		timeout = 30 * time.Second
	}
	active, cancel := context.WithTimeout(ctx, timeout)
	stop := context.AfterFunc(entry.life, cancel)
	defer stop()
	defer cancel()
	if ctx.Err() != nil || entry.life.Err() != nil {
		return nil, errors.New("stopped")
	}
	defer func() {
		if cause := recover(); cause != nil {
			data = nil
			err = fmt.Errorf("%v", cause)
		}
	}()
	fetched, err := entry.source.Fetch(active, settings, project)
	if active.Err() != nil {
		if errors.Is(active.Err(), context.DeadlineExceeded) {
			return nil, errors.New("the source did not answer in time")
		}
		return nil, errors.New("stopped")
	}
	if err != nil {
		return nil, err
	}
	if fetched == nil {
		return nil, errors.New("the source returned no data sets")
	}
	encoded, err := jsonutil.Encode(fetched)
	if err != nil {
		return nil, err
	}
	units := 0
	for _, r := range string(encoded) {
		units += utf16.RuneLen(r)
		if units > MaxFetchedUnits {
			return nil, errors.New("the data is too large")
		}
	}
	if err := json.Unmarshal(encoded, &data); err != nil {
		return nil, err
	}
	return data, nil
}
func (registry *Registry) Close(ctx context.Context) error {
	registry.mu.Lock()
	if !registry.closed {
		registry.closed = true
		for _, entry := range registry.sources {
			entry.cancel()
		}
		clear(registry.sources)
		registry.order = nil
		go func() { registry.jobs.Wait(); close(registry.done) }()
	}
	registry.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-registry.done:
		return nil
	}
}
