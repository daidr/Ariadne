package server

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/dashboard"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
	"github.com/daidr/Ariadne/internal/links"
	"github.com/daidr/Ariadne/internal/models"
	"github.com/daidr/Ariadne/internal/plugins"
	"github.com/daidr/Ariadne/internal/searches"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/internal/sources"
	"github.com/daidr/Ariadne/sdk"
)

func TestLuaPluginsThroughSettingsAPI(t *testing.T) {
	dir := t.TempDir()
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(dir, "app.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	settingsPath := filepath.Join(dir, "settings.json")
	fixtures := t.TempDir()
	file := filepath.Join(fixtures, "search.lua")
	invalid := filepath.Join(fixtures, "invalid.lua")
	legacy := filepath.Join(fixtures, "legacy.ts")
	for path, data := range map[string]string{
		invalid: "return false",
		legacy:  "export default {}",
		file: `local title = {["zh-CN"] = "Lua 搜索", en = "Lua search"}
		return {version = 1, id = "fixture", name = title,
			settings = {type = "object", required = {"credential"}, properties = {
				credential = {type = "string", title = title, secret = true}
			}},
			contexts = {{kind = "test-source", title = title, requiresPluginSettings = true,
				list = function() return {} end, read = function() return "" end}},
			searches = {{
			kind = "test-search", title = title,
			search = function(query)
				assert(ariadne.settings().credential == "fixture-private-token")
				return {{title = query, url = "https://example.test"}}
			end
		}}}`,
	} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	store, err := settings.Open(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SavePluginPath(legacy, plugins.MaxPlugins); err != nil {
		t.Fatal(err)
	}

	type testEnv struct {
		host          *plugins.Host
		plugins       ariadnev1connect.PluginServiceClient
		unauthPlugins ariadnev1connect.PluginServiceClient
		settings      ariadnev1connect.SettingsServiceClient
		meta          ariadnev1connect.MetaServiceClient
		close         func()
	}

	start := func() testEnv {
		t.Helper()
		m, s, d, l := models.New(), sources.New(), dashboard.New(), links.New(links.Options{})
		store, err := settings.Open(settingsPath)
		if err != nil {
			t.Fatal(err)
		}
		host, err := plugins.New(plugins.Options{DataRoot: filepath.Join(dir, "plugins"), ManagedRoot: dir, Models: m, Sources: s, Dashboards: d, Links: l, Settings: store})
		if err != nil {
			t.Fatal(err)
		}
		api, err := New(Options{Repository: repo, Token: "fixture-token", Settings: store, Plugins: host, Models: m, Sources: s, Dashboards: d, Links: l})
		if err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewServer(api)
		auth := connect.WithInterceptors(bearerClientInterceptor("fixture-token"))
		return testEnv{
			host:          host,
			plugins:       ariadnev1connect.NewPluginServiceClient(srv.Client(), srv.URL+rpcPrefix, auth),
			unauthPlugins: ariadnev1connect.NewPluginServiceClient(srv.Client(), srv.URL+rpcPrefix),
			settings:      ariadnev1connect.NewSettingsServiceClient(srv.Client(), srv.URL+rpcPrefix, auth),
			meta:          ariadnev1connect.NewMetaServiceClient(srv.Client(), srv.URL+rpcPrefix, auth),
			close: func() {
				srv.Close()
				api.Close()
				host.Close(context.Background())
				d.Close(context.Background())
				l.Close()
			},
		}
	}

	mustStruct := func(values map[string]any) *structpb.Struct {
		t.Helper()
		out, err := structpb.NewStruct(values)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	env := start()
	t.Cleanup(env.close)

	listRes, err := env.plugins.ListPlugins(t.Context(), connect.NewRequest(&ariadnev1.ListPluginsRequest{}))
	if err != nil || len(listRes.Msg.GetPlugins()) != 1 || listRes.Msg.GetPlugins()[0].GetStatus() != "failed" {
		t.Fatalf("ListPlugins: res=%+v err=%v", listRes, err)
	}

	if _, err := env.unauthPlugins.AddPlugin(t.Context(), connect.NewRequest(&ariadnev1.AddPluginRequest{Path: file})); err == nil {
		t.Fatal("expected unauthenticated AddPlugin to fail")
	} else if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("unexpected unauth error: %v", err)
	}

	if _, err := env.plugins.AddPlugin(t.Context(), connect.NewRequest(&ariadnev1.AddPluginRequest{Path: invalid})); err == nil {
		t.Fatal("expected invalid plugin to fail")
	} else if connectErr, localized := extractLocalizedError(t, err); connectErr.Code() != connect.CodeInvalidArgument || localized == nil || localized.Key != "error.pluginLoad" {
		t.Fatalf("unexpected invalid plugin error: code=%v localized=%v", connectErr.Code(), localized)
	}

	addRes, err := env.plugins.AddPlugin(t.Context(), connect.NewRequest(&ariadnev1.AddPluginRequest{Path: file}))
	if err != nil {
		t.Fatalf("AddPlugin: %v", err)
	}
	hasLoaded := false
	for _, record := range addRes.Msg.GetPlugins() {
		if record.GetStatus() == "loaded" {
			hasLoaded = true
		}
	}
	if !hasLoaded {
		t.Fatalf("expected loaded plugin, got %+v", addRes.Msg.GetPlugins())
	}

	if _, err := env.plugins.AddPlugin(t.Context(), connect.NewRequest(&ariadnev1.AddPluginRequest{Path: file})); err == nil {
		t.Fatal("expected duplicate AddPlugin to fail")
	} else if connectErr, localized := extractLocalizedError(t, err); connectErr.Code() != connect.CodeInvalidArgument || localized == nil || localized.Key != "error.pluginExists" {
		t.Fatalf("unexpected duplicate error: code=%v localized=%v", connectErr.Code(), localized)
	}

	searchRes, err := env.settings.GetSearchSettings(t.Context(), connect.NewRequest(&ariadnev1.GetSearchSettingsRequest{}))
	if err != nil || len(searchRes.Msg.GetSearch().GetPlugins()) != 1 || searchRes.Msg.GetSearch().GetPlugins()[0].GetValues()["en"] != "Lua search" {
		t.Fatalf("GetSearchSettings: res=%+v err=%v", searchRes, err)
	}

	if _, err := env.unauthPlugins.GetPluginConfig(t.Context(), connect.NewRequest(&ariadnev1.GetPluginConfigRequest{PluginId: "fixture"})); err == nil {
		t.Fatal("expected unauthenticated GetPluginConfig to fail")
	} else if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("unexpected unauth GetPluginConfig error: %v", err)
	}

	cfgRes, err := env.plugins.GetPluginConfig(t.Context(), connect.NewRequest(&ariadnev1.GetPluginConfigRequest{PluginId: "fixture"}))
	if err != nil || cfgRes.Msg.GetConfig().GetReady() {
		t.Fatalf("GetPluginConfig: res=%+v err=%v", cfgRes, err)
	}

	kindsRes, err := env.meta.ListSourceKinds(t.Context(), connect.NewRequest(&ariadnev1.ListSourceKindsRequest{}))
	if err != nil {
		t.Fatalf("ListSourceKinds: %v", err)
	}
	findPluginReady := func(kinds []*ariadnev1.SourceKind) (bool, bool) {
		for _, kind := range kinds {
			if kind.GetPlugin() != nil && kind.GetPlugin().GetId() == "fixture" {
				return true, kind.GetPlugin().GetReady()
			}
		}
		return false, false
	}
	if found, ready := findPluginReady(kindsRes.Msg.GetKinds()); !found || ready {
		t.Fatalf("expected fixture source kind not ready, found=%v ready=%v", found, ready)
	}

	if _, err := env.plugins.SavePluginConfig(t.Context(), connect.NewRequest(&ariadnev1.SavePluginConfigRequest{
		PluginId: "fixture",
		Values:   mustStruct(map[string]any{"oops": "value"}),
	})); err == nil {
		t.Fatal("expected invalid plugin settings to fail")
	} else if connectErr, localized := extractLocalizedError(t, err); connectErr.Code() != connect.CodeInvalidArgument || localized == nil || localized.Key != "error.invalidBody" {
		t.Fatalf("unexpected invalid settings error: code=%v localized=%v", connectErr.Code(), localized)
	}

	savedCfg, err := env.plugins.SavePluginConfig(t.Context(), connect.NewRequest(&ariadnev1.SavePluginConfigRequest{
		PluginId: "fixture",
		Values:   mustStruct(map[string]any{"credential": "fixture-private-token"}),
	}))
	if err != nil || !slices.Contains(savedCfg.Msg.GetConfig().GetSecretsSet(), "credential") {
		t.Fatalf("SavePluginConfig: res=%+v err=%v", savedCfg, err)
	}
	if strings.Contains(savedCfg.Msg.String(), "fixture-private-token") {
		t.Fatal("settings response leaked a plugin secret")
	}

	keptCfg, err := env.plugins.SavePluginConfig(t.Context(), connect.NewRequest(&ariadnev1.SavePluginConfigRequest{
		PluginId: "fixture",
		Values:   mustStruct(map[string]any{"credential": ""}),
	}))
	if err != nil || !keptCfg.Msg.GetConfig().GetReady() {
		t.Fatalf("SavePluginConfig keep secret: res=%+v err=%v", keptCfg, err)
	}

	kindsRes, err = env.meta.ListSourceKinds(t.Context(), connect.NewRequest(&ariadnev1.ListSourceKindsRequest{}))
	if err != nil {
		t.Fatalf("ListSourceKinds after save: %v", err)
	}
	if found, ready := findPluginReady(kindsRes.Msg.GetKinds()); !found || !ready {
		t.Fatalf("expected fixture source kind ready, found=%v ready=%v", found, ready)
	}

	env.close()
	env = start()
	defer env.close()
	if items := env.host.SearchProviders(); len(items) != 1 {
		t.Fatal("restart did not restore saved Lua plugin", items)
	} else if got, err := items[0].Search(t.Context(), "测试", 1); len(got) != 1 || got[0].Title != "测试" || err != nil {
		t.Fatal(got, err)
	}

	clearedCfg, err := env.plugins.SavePluginConfig(t.Context(), connect.NewRequest(&ariadnev1.SavePluginConfigRequest{
		PluginId: "fixture",
		Values:   mustStruct(map[string]any{"credential": nil}),
	}))
	if err != nil || clearedCfg.Msg.GetConfig().GetReady() {
		t.Fatalf("SavePluginConfig clear secret: res=%+v err=%v", clearedCfg, err)
	}

	delRes, err := env.plugins.DeletePlugin(t.Context(), connect.NewRequest(&ariadnev1.DeletePluginRequest{Path: file}))
	if err != nil || len(delRes.Msg.GetPlugins()) != 1 || delRes.Msg.GetPlugins()[0].GetStatus() != "failed" {
		t.Fatalf("DeletePlugin file: res=%+v err=%v", delRes, err)
	}
	delLegacy, err := env.plugins.DeletePlugin(t.Context(), connect.NewRequest(&ariadnev1.DeletePluginRequest{Path: legacy}))
	if err != nil || len(delLegacy.Msg.GetPlugins()) != 0 {
		t.Fatalf("DeletePlugin legacy: res=%+v err=%v", delLegacy, err)
	}
	searchRes, err = env.settings.GetSearchSettings(t.Context(), connect.NewRequest(&ariadnev1.GetSearchSettingsRequest{}))
	if err != nil || len(searchRes.Msg.GetSearch().GetPlugins()) != 0 {
		t.Fatalf("GetSearchSettings after delete: res=%+v err=%v", searchRes, err)
	}
	config, err := settings.Open(settingsPath)
	if err != nil || len(config.PluginPaths()) != 0 {
		t.Fatal("settings retained removed paths", err)
	}
}

func TestBuiltinHTTPModelPluginsTogglePreservesSavedConfig(t *testing.T) {
	dir := t.TempDir()
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(dir, "app.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	store, err := settings.Open(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	modelReg, sourceReg, dashReg, linkReg := models.New(), sources.New(), dashboard.New(), links.New(links.Options{})
	defer dashReg.Close(context.Background())
	defer linkReg.Close()

	host, err := plugins.New(plugins.Options{
		DataRoot:    filepath.Join(dir, "plugins"),
		ManagedRoot: dir,
		Models:      modelReg,
		Sources:     sourceReg,
		Dashboards:  dashReg,
		Links:       linkReg,
		Settings:    store,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close(context.Background())
	for _, plugin := range models.HTTPBuiltinPlugins(models.HTTPOptions{}) {
		if err := host.Load(t.Context(), plugin, nil); err != nil {
			t.Fatal(err)
		}
	}
	api, err := New(Options{
		Repository: repo,
		Token:      "fixture-token",
		Settings:   store,
		Plugins:    host,
		Models:     modelReg,
		Sources:    sourceReg,
		Dashboards: dashReg,
		Links:      linkReg,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()

	srv := httptest.NewServer(api)
	defer srv.Close()
	auth := connect.WithInterceptors(bearerClientInterceptor("fixture-token"))
	pluginClient := ariadnev1connect.NewPluginServiceClient(srv.Client(), srv.URL+rpcPrefix, auth)
	settingsClient := ariadnev1connect.NewSettingsServiceClient(srv.Client(), srv.URL+rpcPrefix, auth)

	// Both built-in HTTP model plugins load enabled by default.
	listRes, err := pluginClient.ListPlugins(t.Context(), connect.NewRequest(&ariadnev1.ListPluginsRequest{}))
	if err != nil {
		t.Fatalf("ListPlugins: %v", err)
	}
	hasOpenAI, hasAnthropic := false, false
	for _, record := range listRes.Msg.Plugins {
		if record.GetId() == "model-openai" && record.GetStatus() == "loaded" {
			hasOpenAI = true
		}
		if record.GetId() == "model-anthropic" && record.GetStatus() == "loaded" {
			hasAnthropic = true
		}
	}
	if !hasOpenAI || !hasAnthropic {
		t.Fatalf("missing builtin HTTP model plugins: %+v", listRes.Msg.Plugins)
	}

	// Save an Anthropic provider and an OpenAI-compatible provider directly in settings.
	if _, err := store.UpdateModels(object{
		"providers": []any{
			object{"id": "p-anth", "kind": "anthropic", "name": "Claude", "settings": object{"baseUrl": "https://api.anthropic.com", "apiKey": "sk-anthropic-secret"}},
			object{"id": "p-oa", "kind": "openai-compatible", "name": "OpenAI", "settings": object{"baseUrl": "https://api.openai.com/v1", "apiKey": "sk-openai-secret"}},
		},
		"models": []any{
			object{"id": "m-anth", "provider": "p-anth", "model": "claude-sonnet"},
			object{"id": "m-oa", "provider": "p-oa", "model": "gpt-5"},
		},
		"roles": object{"thread": object{"model": "m-oa"}},
	}, modelReg.Kinds(), modelReg.ValidateSettings); err != nil {
		t.Fatal(err)
	}

	// Invalid HTTP baseUrl is rejected by ValidateSettings through ConnectRPC.
	badSettings, err := structpb.NewStruct(map[string]any{"baseUrl": "file:///etc/passwd"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = settingsClient.SaveModelSettings(t.Context(), connect.NewRequest(&ariadnev1.SaveModelSettingsRequest{
		Providers: []*ariadnev1.ModelProviderInput{
			{Id: "p-oa", Kind: "openai-compatible", Name: "OpenAI", Settings: badSettings},
		},
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("SaveModelSettings(invalid baseUrl): want InvalidArgument, got %v", err)
	}

	// Disable model-anthropic via ConnectRPC.
	_, err = pluginClient.SetPluginEnabled(t.Context(), connect.NewRequest(&ariadnev1.SetPluginEnabledRequest{
		PluginId: "nonexistent",
		Enabled:  false,
	}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("SetPluginEnabled(nonexistent): want NotFound, got %v", err)
	}
	toggleRes, err := pluginClient.SetPluginEnabled(t.Context(), connect.NewRequest(&ariadnev1.SetPluginEnabledRequest{
		PluginId: "model-anthropic",
		Enabled:  false,
	}))
	if err != nil {
		t.Fatalf("SetPluginEnabled(false): %v", err)
	}
	disabledFound := false
	for _, record := range toggleRes.Msg.Plugins {
		if record.GetId() == "model-anthropic" && record.GetStatus() == "disabled" {
			disabledFound = true
		}
	}
	if !disabledFound {
		t.Fatalf("expected model-anthropic disabled: %+v", toggleRes.Msg.Plugins)
	}
	if _, ok := modelReg.Get("anthropic"); ok {
		t.Fatal("anthropic provider still registered after disabling plugin")
	}

	// Editing a disabled provider's model entry and request params via ConnectRPC succeeds without probing errors
	// and preserves its stored settings and secret.
	timeoutSeconds := int32(45)
	openAISettings, err := structpb.NewStruct(map[string]any{"baseUrl": "https://api.openai.com/v1", "apiKey": ""})
	if err != nil {
		t.Fatal(err)
	}
	emptySettings, err := structpb.NewStruct(map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	threadModelID := "m-oa"
	saveDisabledRes, err := settingsClient.SaveModelSettings(t.Context(), connect.NewRequest(&ariadnev1.SaveModelSettingsRequest{
		Providers: []*ariadnev1.ModelProviderInput{
			{Id: "p-anth", Kind: "anthropic", Name: "Claude", Settings: emptySettings, TimeoutSeconds: &timeoutSeconds},
			{Id: "p-oa", Kind: "openai-compatible", Name: "OpenAI Updated", Settings: openAISettings},
		},
		Models: []*ariadnev1.ModelEntry{
			{Id: "m-anth", Provider: "p-anth", Model: "claude-opus"},
			{Id: "m-oa", Provider: "p-oa", Model: "gpt-5"},
		},
		Roles: &ariadnev1.ModelRoles{
			Thread: &ariadnev1.RoleConfig{Model: &threadModelID},
		},
	}))
	if err != nil {
		t.Fatalf("SaveModelSettings while editing disabled provider failed: %v", err)
	}
	if len(saveDisabledRes.Msg.GetProblems()) != 0 {
		t.Fatalf("unexpected probe problems for disabled provider edit: %+v", saveDisabledRes.Msg.GetProblems())
	}

	// Re-enable model-anthropic: provider returns with both the preserved baseUrl/apiKey and updated timeout/model.
	reEnableRes, err := pluginClient.SetPluginEnabled(t.Context(), connect.NewRequest(&ariadnev1.SetPluginEnabledRequest{
		PluginId: "model-anthropic",
		Enabled:  true,
	}))
	if err != nil {
		t.Fatalf("SetPluginEnabled(true): %v", err)
	}
	for _, record := range reEnableRes.Msg.Plugins {
		if record.GetStatus() == "disabled" {
			t.Fatalf("expected all loaded after re-enable: %+v", reEnableRes.Msg.Plugins)
		}
	}
	modelsRes, err := settingsClient.GetModelSettings(t.Context(), connect.NewRequest(&ariadnev1.GetModelSettingsRequest{}))
	if err != nil {
		t.Fatalf("GetModelSettings: %v", err)
	}
	recovered := false
	for _, p := range modelsRes.Msg.GetSettings().GetProviders() {
		if p.GetId() == "p-anth" && p.GetKind() == "anthropic" &&
			slices.Contains(p.GetSecretsSet(), "apiKey") &&
			p.GetSettings().AsMap()["baseUrl"] == "https://api.anthropic.com" &&
			p.GetTimeoutSeconds() == 45 {
			recovered = true
		}
	}
	if !recovered {
		t.Fatalf("re-enabled anthropic did not recover saved config: %+v", modelsRes.Msg.GetSettings().GetProviders())
	}
}

func TestPluginUsageAndDisabledModelSelection(t *testing.T) {
	dir := t.TempDir()
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(dir, "app.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	store, err := settings.Open(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	modelReg := models.New()
	sourceReg := sources.New()
	dashReg := dashboard.New()
	searchReg := searches.New()
	linkReg := links.New(links.Options{})
	host, err := plugins.New(plugins.Options{
		DataRoot:    filepath.Join(dir, "plugins"),
		ManagedRoot: dir,
		Models:      modelReg,
		Sources:     sourceReg,
		Dashboards:  dashReg,
		Searches:    searchReg,
		Links:       linkReg,
		Settings:    store,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close(context.Background())

	for _, plugin := range models.HTTPBuiltinPlugins(models.HTTPOptions{}) {
		if err := host.Load(t.Context(), plugin, nil); err != nil {
			t.Fatal(err)
		}
	}
	for _, plugin := range searches.BuiltinPlugins(nil) {
		if err := host.Load(t.Context(), plugin, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := host.Load(t.Context(), sdk.Plugin{
		ID:       "local",
		Name:     sdk.LocalizedText{"zh-CN": "本地目录", "en": "Local directory"},
		Provides: sdk.PluginProvides{Contexts: []string{"local-directory"}},
		Setup: func(_ context.Context, pluginCtx sdk.PluginContext) (func(context.Context) error, error) {
			if _, err := pluginCtx.RegisterContext(sdk.ContextProvider{
				Info: sdk.ContextProviderInfo{Kind: "local-directory", Title: sdk.LocalizedText{"zh-CN": "本地目录"}},
				Connect: func(context.Context, map[string]any) (sdk.Capabilities, error) {
					return sdk.Capabilities{}, nil
				},
			}); err != nil {
				return nil, err
			}
			return nil, nil
		},
	}, nil); err != nil {
		t.Fatal(err)
	}

	if _, err := store.UpdateModels(object{
		"providers": []any{
			object{"id": "p-anth", "kind": "anthropic", "name": "Claude", "settings": object{"baseUrl": "https://api.anthropic.com", "apiKey": "sk-anth"}},
			object{"id": "p-oa", "kind": "openai-compatible", "name": "OpenAI", "settings": object{"baseUrl": "https://api.openai.com/v1", "apiKey": "sk-oa"}},
		},
		"models": []any{
			object{"id": "m-anth", "provider": "p-anth", "model": "claude-sonnet"},
			object{"id": "m-oa", "provider": "p-oa", "model": "gpt-5"},
		},
		"roles": object{"thread": object{"model": "m-oa", "fallback": "m-anth"}},
	}, modelReg.Kinds(), modelReg.ValidateSettings); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateSearch(object{
		"provider": "tavily",
		"services": object{"tavily": object{"apiKey": "tvly-secret"}},
	}, searchReg.Kinds(), nil); err != nil {
		t.Fatal(err)
	}

	projectName := "Demo Project"
	project, err := repo.CreateProject(t.Context(), &projectName, core.ProjectStart{
		Source: map[string]any{"kind": "local-directory", "settings": map[string]any{"path": dir}},
	})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := repo.CreateThread(t.Context(), project.ID, core.NewThread{Title: "Check model"})
	if err != nil {
		t.Fatal(err)
	}

	api, err := New(Options{
		Repository: repo,
		Token:      "fixture-token",
		Settings:   store,
		Plugins:    host,
		Models:     modelReg,
		Sources:    sourceReg,
		Dashboards: dashReg,
		Searches:   searchReg,
		Links:      linkReg,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()

	srv := httptest.NewServer(api)
	defer srv.Close()
	auth := connect.WithInterceptors(bearerClientInterceptor("fixture-token"))
	pluginClient := ariadnev1connect.NewPluginServiceClient(srv.Client(), srv.URL+rpcPrefix, auth)
	threadClient := ariadnev1connect.NewThreadServiceClient(srv.Client(), srv.URL+rpcPrefix, auth)

	listRes, err := pluginClient.ListPlugins(t.Context(), connect.NewRequest(&ariadnev1.ListPluginsRequest{}))
	if err != nil {
		t.Fatalf("ListPlugins: %v", err)
	}
	byID := map[string]*ariadnev1.PluginRecord{}
	for _, record := range listRes.Msg.GetPlugins() {
		byID[record.GetId()] = record
	}
	if usage := byID["model-openai"].GetUsage(); !slices.Equal(usage.GetModelProviders(), []string{"OpenAI"}) || !slices.Equal(usage.GetModelRoles(), []string{"thread"}) {
		t.Fatalf("model-openai usage = %+v", usage)
	}
	if usage := byID["model-anthropic"].GetUsage(); !slices.Equal(usage.GetModelProviders(), []string{"Claude"}) || !slices.Equal(usage.GetModelRoles(), []string{"fallback"}) {
		t.Fatalf("model-anthropic usage = %+v", usage)
	}
	if usage := byID["search-tavily"].GetUsage(); !slices.Equal(usage.GetSearches(), []string{"tavily"}) {
		t.Fatalf("search-tavily usage = %+v", usage)
	}
	if usage := byID["local"].GetUsage(); !slices.Equal(usage.GetProjects(), []string{"Demo Project"}) {
		t.Fatalf("local usage = %+v", usage)
	}

	// Disable model-anthropic: SetThreadModel must reject m-anth while still allowing m-oa.
	if _, err := pluginClient.SetPluginEnabled(t.Context(), connect.NewRequest(&ariadnev1.SetPluginEnabledRequest{
		PluginId: "model-anthropic",
		Enabled:  false,
	})); err != nil {
		t.Fatalf("SetPluginEnabled(false): %v", err)
	}
	_, err = threadClient.SetThreadModel(t.Context(), connect.NewRequest(&ariadnev1.SetThreadModelRequest{
		ThreadId:    thread.ID,
		ModelAction: &ariadnev1.SetThreadModelRequest_Model{Model: "m-anth"},
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("SetThreadModel(disabled m-anth): want InvalidArgument, got %v", err)
	}
	if _, err := threadClient.SetThreadModel(t.Context(), connect.NewRequest(&ariadnev1.SetThreadModelRequest{
		ThreadId:    thread.ID,
		ModelAction: &ariadnev1.SetThreadModelRequest_Model{Model: "m-oa"},
	})); err != nil {
		t.Fatalf("SetThreadModel(active m-oa): %v", err)
	}
}
