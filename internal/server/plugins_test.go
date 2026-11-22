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
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/internal/sources"
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
