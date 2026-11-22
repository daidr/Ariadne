package plugins

import (
	"context"
	"errors"
	"iter"
	"net/url"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/dashboard"
	"github.com/daidr/Ariadne/internal/links"
	"github.com/daidr/Ariadne/internal/models"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/internal/sources"
	"github.com/daidr/Ariadne/sdk"
)

func fixture(t *testing.T) (*Host, Options) {
	t.Helper()
	options := Options{DataRoot: filepath.Join(t.TempDir(), "plugins"), Models: models.New(), Sources: sources.New(), Dashboards: dashboard.New(), Links: links.New(links.Options{})}
	host, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := host.Close(ctx); err != nil {
			t.Error(err)
		}
		options.Dashboards.Close(ctx)
		options.Links.Close()
	})
	return host, options
}
func model(kind string) sdk.ModelProvider {
	return sdk.ModelProvider{Info: sdk.ModelProviderInfo{Kind: kind}, Stream: func(context.Context, map[string]any, sdk.Request) iter.Seq[sdk.Part] {
		return func(func(sdk.Part) bool) {}
	}}
}

func TestPluginRegistrationRollbackAndPanicsLeaveNothingBehind(t *testing.T) {
	for _, panicSetup := range []bool{false, true} {
		host, options := fixture(t)
		cleaned := 0
		plugin := sdk.Plugin{ID: "fixture", Name: sdk.LocalizedText{"zh-CN": "测试"}, Setup: func(_ context.Context, ctx sdk.PluginContext) (func(context.Context) error, error) {
			if err := ctx.OnDispose(func(context.Context) error { cleaned++; return nil }); err != nil {
				return nil, err
			}
			if _, err := ctx.RegisterModel(model("fixture")); err != nil {
				return nil, err
			}
			if panicSetup {
				panic("setup failed")
			}
			return nil, errors.New("setup failed")
		}}
		if err := host.Load(t.Context(), plugin, nil); err == nil {
			t.Fatal("failed setup accepted")
		}
		if _, exists := options.Models.Get("fixture"); exists || len(host.Records()) != 0 || cleaned != 1 {
			t.Fatal("partial provider registration survived", host.Records(), cleaned)
		}
	}
}
func TestPluginDuplicateKindsRollbackAndIgnoredRegistrationErrorsFailStartup(t *testing.T) {
	host, options := fixture(t)
	remove, err := options.Models.Register(model("existing"))
	if err != nil {
		t.Fatal(err)
	}
	defer remove()
	plugin := sdk.Plugin{ID: "fixture", Setup: func(_ context.Context, ctx sdk.PluginContext) (func(context.Context) error, error) {
		ctx.RegisterModel(model("new"))
		ctx.RegisterModel(model("existing"))
		return nil, nil
	}}
	if err := host.Load(t.Context(), plugin, nil); err == nil {
		t.Fatal("ignored duplicate registration allowed startup")
	}
	if _, exists := options.Models.Get("new"); exists {
		t.Fatal("new provider survived rollback")
	}
	if _, exists := options.Models.Get("existing"); !exists {
		t.Fatal("rollback removed another plugin's provider")
	}
}
func TestPluginUnloadCancelsLifetimeDisposesInReverseAndReleasesRecords(t *testing.T) {
	host, options := fixture(t)
	order := []int{}
	var life context.Context
	var retained sdk.PluginContext
	plugin := sdk.Plugin{ID: "fixture", Name: sdk.LocalizedText{"zh-CN": "测试"}, Setup: func(_ context.Context, ctx sdk.PluginContext) (func(context.Context) error, error) {
		life = ctx.Context
		retained = ctx
		ctx.OnDispose(func(context.Context) error { order = append(order, 1); return nil })
		ctx.OnDispose(func(context.Context) error { order = append(order, 2); panic("cleanup failed") })
		if _, err := ctx.RegisterModel(model("fixture")); err != nil {
			return nil, err
		}
		if _, err := ctx.RegisterContext(sdk.ContextProvider{Info: sdk.ContextProviderInfo{Kind: "fixture"}, Connect: func(context.Context, map[string]any) (sdk.Capabilities, error) { return sdk.Capabilities{}, nil }}); err != nil {
			return nil, err
		}
		if _, err := ctx.RegisterDashboard(sdk.DashboardSource{Info: sdk.DashboardSourceInfo{Kind: "fixture"}, Fetch: func(context.Context, map[string]any, string) (map[string]any, error) { return map[string]any{}, nil }}); err != nil {
			return nil, err
		}
		if _, err := ctx.RegisterSearch(sdk.SearchProvider{Kind: "fixture", Title: sdk.LocalizedText{"zh-CN": "搜索"}, Search: func(context.Context, string, int) ([]sdk.SearchResult, error) { return []sdk.SearchResult{}, nil }}); err != nil {
			return nil, err
		}
		if _, err := ctx.RegisterLink(sdk.LinkProvider{Kind: "fixture", Match: func(*url.URL) *sdk.LinkMatch { return nil }, Preview: func(context.Context, *url.URL) (*sdk.PullRequestPreview, error) { return nil, nil }}); err != nil {
			return nil, err
		}
		return func(context.Context) error { order = append(order, 3); return nil }, nil
	}}
	path := filepath.Join(t.TempDir(), "plugin.json")
	if err := host.Load(t.Context(), plugin, &path); err != nil {
		t.Fatal(err)
	}
	plugin.Name["zh-CN"] = "mutated"
	records := host.Records()
	if len(records) != 1 || records[0].Source != "path" || records[0].Name["zh-CN"] != "测试" {
		t.Fatal("wrong plugin record", records)
	}
	records[0].Provides.Models[0] = "mutated"
	if host.Records()[0].Provides.Models[0] != "fixture" {
		t.Fatal("records alias internal state")
	}
	if _, err := retained.RegisterModel(model("late")); err == nil {
		t.Fatal("registration allowed after setup")
	}
	if err := host.UnloadPath(t.Context(), path); err == nil {
		t.Fatal("cleanup panic was not reported")
	}
	if life.Err() == nil || !reflect.DeepEqual(order, []int{3, 2, 1}) {
		t.Fatal("plugin lifetime or disposal order", order)
	}
	if _, exists := options.Models.Get("fixture"); exists {
		t.Fatal("model survived unload")
	}
	if _, exists := options.Sources.Get("fixture"); exists {
		t.Fatal("source survived unload")
	}
	if options.Dashboards.Info("fixture") != nil || len(host.SearchProviders()) != 0 || len(host.Records()) != 0 {
		t.Fatal("unloaded registries retained plugin")
	}
	if err := host.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if host.options.Models != nil || host.options.OnError != nil {
		t.Fatal("closed host retains registry owners")
	}
}
func TestPluginSafeIDsAndBoundedFailureRecords(t *testing.T) {
	host, _ := fixture(t)
	for _, id := range []string{"..", "../escape", "/outside", `C:\outside`, "", "name/child"} {
		if err := host.Load(t.Context(), sdk.Plugin{ID: id, Setup: func(context.Context, sdk.PluginContext) (func(context.Context) error, error) {
			t.Error("unsafe plugin ran")
			return nil, nil
		}}, nil); err == nil {
			t.Fatal("unsafe plugin id accepted", id)
		}
	}
	for i := range MaxPlugins + 10 {
		host.rememberFailure(string(rune(i+1000)), errors.New("failed"))
	}
	if len(host.Records()) != MaxPlugins {
		t.Fatal("failed plugin records unbounded")
	}
}

func TestBuiltinPluginEnableDisableLifecycleAndRestart(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	store, err := settings.Open(configPath)
	if err != nil {
		t.Fatal(err)
	}
	setupCalls := 0
	disposeCalls := 0
	builtin := sdk.Plugin{
		ID:       "model-fixture",
		Name:     sdk.LocalizedText{"zh-CN": "测试模型", "en": "Test model"},
		Provides: sdk.PluginProvides{Models: []string{"fixture-model"}},
		Setup: func(_ context.Context, ctx sdk.PluginContext) (func(context.Context) error, error) {
			setupCalls++
			if _, err := ctx.RegisterModel(model("fixture-model")); err != nil {
				return nil, err
			}
			return func(context.Context) error {
				disposeCalls++
				return nil
			}, nil
		},
	}

	makeHost := func(s *settings.Store) (*Host, Options) {
		options := Options{
			DataRoot:   filepath.Join(dir, "plugins"),
			Models:     models.New(),
			Sources:    sources.New(),
			Dashboards: dashboard.New(),
			Links:      links.New(links.Options{}),
			Settings:   s,
		}
		h, err := New(options)
		if err != nil {
			t.Fatal(err)
		}
		return h, options
	}

	host, options := makeHost(store)
	if err := host.Load(t.Context(), builtin, nil); err != nil {
		t.Fatal(err)
	}
	if setupCalls != 1 {
		t.Fatalf("expected 1 setup call, got %d", setupCalls)
	}
	if _, ok := options.Models.Get("fixture-model"); !ok {
		t.Fatal("builtin model not registered on initial load")
	}
	records := host.Records()
	if len(records) != 1 || records[0].Status != "loaded" || records[0].Source != "builtin" {
		t.Fatalf("unexpected initial records: %+v", records)
	}

	// Disable the built-in plugin: provider unregisters, disposer runs, config records disabledPlugins.
	if err := host.SetBuiltinEnabled(t.Context(), store, "model-fixture", false); err != nil {
		t.Fatal(err)
	}
	if disposeCalls != 1 {
		t.Fatalf("expected 1 dispose call, got %d", disposeCalls)
	}
	if _, ok := options.Models.Get("fixture-model"); ok {
		t.Fatal("disabled builtin model remained registered")
	}
	if !store.IsPluginDisabled("model-fixture") {
		t.Fatal("disabled plugin not persisted in settings")
	}
	records = host.Records()
	if len(records) != 1 || records[0].Status != "disabled" || !reflect.DeepEqual(records[0].Provides.Models, []string{"fixture-model"}) {
		t.Fatalf("unexpected disabled records: %+v", records)
	}
	if err := host.SetBuiltinEnabled(t.Context(), store, "unknown-plugin", false); err == nil {
		t.Fatal("expected error when toggling unknown plugin")
	}
	_ = host.Close(t.Context())
	options.Dashboards.Close(t.Context())
	options.Links.Close()

	// Restart with persisted config.json: built-in starts disabled without running Setup, preserving Provides.
	restartedStore, err := settings.Open(configPath)
	if err != nil {
		t.Fatal(err)
	}
	host2, options2 := makeHost(restartedStore)
	defer func() {
		_ = host2.Close(t.Context())
		options2.Dashboards.Close(t.Context())
		options2.Links.Close()
	}()
	if err := host2.Load(t.Context(), builtin, nil); err != nil {
		t.Fatal(err)
	}
	if setupCalls != 1 {
		t.Fatalf("disabled builtin ran Setup on restart: setupCalls=%d", setupCalls)
	}
	if _, ok := options2.Models.Get("fixture-model"); ok {
		t.Fatal("disabled builtin registered provider on restart")
	}
	records = host2.Records()
	if len(records) != 1 || records[0].Status != "disabled" || !reflect.DeepEqual(records[0].Provides.Models, []string{"fixture-model"}) {
		t.Fatalf("unexpected restarted disabled records: %+v", records)
	}

	// Re-enable the built-in plugin: Setup runs again and provider becomes available.
	if err := host2.SetBuiltinEnabled(t.Context(), restartedStore, "model-fixture", true); err != nil {
		t.Fatal(err)
	}
	if setupCalls != 2 {
		t.Fatalf("expected 2 setup calls after re-enable, got %d", setupCalls)
	}
	if _, ok := options2.Models.Get("fixture-model"); !ok {
		t.Fatal("re-enabled builtin model not registered")
	}
	if restartedStore.IsPluginDisabled("model-fixture") {
		t.Fatal("re-enabled plugin remained in disabledPlugins")
	}
	records = host2.Records()
	if len(records) != 1 || records[0].Status != "loaded" {
		t.Fatalf("unexpected re-enabled records: %+v", records)
	}
}
