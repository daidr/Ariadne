package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/dashboard"
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

	start := func() (*API, *plugins.Host, func()) {
		t.Helper()
		models, sources, dashboards, links := models.New(), sources.New(), dashboard.New(), links.New(links.Options{})
		store, err := settings.Open(settingsPath)
		if err != nil {
			t.Fatal(err)
		}
		host, err := plugins.New(plugins.Options{DataRoot: filepath.Join(dir, "plugins"), ManagedRoot: dir, Models: models, Sources: sources, Dashboards: dashboards, Links: links, Settings: store})
		if err != nil {
			t.Fatal(err)
		}
		api, err := New(Options{Repository: repo, Token: "fixture-token", Settings: store, Plugins: host, Models: models, Sources: sources, Dashboards: dashboards, Links: links})
		if err != nil {
			t.Fatal(err)
		}
		return api, host, func() {
			api.Close()
			host.Close(context.Background())
			dashboards.Close(context.Background())
			links.Close()
		}
	}
	request := func(api *API, method, path string, body any, authenticated bool, status int, want string) {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(encoded(body)))
		if authenticated {
			req.Header.Set("Authorization", "Bearer fixture-token")
		}
		response := httptest.NewRecorder()
		api.ServeHTTP(response, req)
		if response.Code != status || !strings.Contains(response.Body.String(), want) {
			t.Fatalf("%s %s: %d %s", method, path, response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "fixture-private-token") {
			t.Fatal("settings response leaked a plugin secret")
		}
	}
	api, _, closeAPI := start()
	t.Cleanup(closeAPI)
	request(api, http.MethodGet, "/api/plugins", nil, true, 200, `"status":"failed"`)
	request(api, http.MethodPost, "/api/plugins", object{"path": file}, false, 401, "error.unauthorized")
	request(api, http.MethodPost, "/api/plugins", object{"path": invalid}, true, 400, "error.pluginLoad")
	request(api, http.MethodPost, "/api/plugins", object{"path": file}, true, 200, `"status":"loaded"`)
	request(api, http.MethodPost, "/api/plugins", object{"path": file}, true, 400, "error.pluginExists")
	request(api, http.MethodGet, "/api/settings/search", nil, true, 200, "Lua search")
	request(api, http.MethodGet, "/api/plugins/fixture/settings", nil, false, 401, "error.unauthorized")
	request(api, http.MethodGet, "/api/plugins/fixture/settings", nil, true, 200, `"ready":false`)
	request(api, http.MethodGet, "/api/source-kinds", nil, true, 200, `"plugin":{"id":"fixture","ready":false}`)
	request(api, http.MethodPut, "/api/plugins/fixture/settings", object{"values": object{"oops": "value"}}, true, 400, "error.invalidBody")
	request(api, http.MethodPut, "/api/plugins/fixture/settings", object{"values": object{"credential": "fixture-private-token"}}, true, 200, `"secretsSet":["credential"]`)
	request(api, http.MethodPut, "/api/plugins/fixture/settings", object{"values": object{"credential": ""}}, true, 200, `"ready":true`)
	request(api, http.MethodGet, "/api/source-kinds", nil, true, 200, `"plugin":{"id":"fixture","ready":true}`)

	closeAPI()
	api, host, closeRestart := start()
	defer closeRestart()
	if items := host.SearchProviders(); len(items) != 1 {
		t.Fatal("restart did not restore saved Lua plugin", items)
	} else if got, err := items[0].Search(t.Context(), "测试", 1); len(got) != 1 || got[0].Title != "测试" || err != nil {
		t.Fatal(got, err)
	}
	request(api, http.MethodPut, "/api/plugins/fixture/settings", object{"values": object{"credential": nil}}, true, 200, `"ready":false`)
	request(api, http.MethodPost, "/api/plugins/remove", object{"path": file}, true, 200, `"status":"failed"`)
	request(api, http.MethodPost, "/api/plugins/remove", object{"path": legacy}, true, 200, `"plugins":[]`)
	request(api, http.MethodGet, "/api/settings/search", nil, true, 200, `"plugins":[]`)
	config, err := settings.Open(settingsPath)
	if err != nil || len(config.PluginPaths()) != 0 {
		t.Fatal("settings retained removed paths", err)
	}
}
