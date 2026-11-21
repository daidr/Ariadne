package plugins

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/sdk"
)

func luaFile(t *testing.T, folder, source string) string {
	t.Helper()
	path := filepath.Join(folder, "plugin.lua")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLuaProviderHTTPStreamAndLifecycle(t *testing.T) {
	host, registries := fixture(t)
	store, err := settings.Open(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	stopped := make(chan struct{})
	release := make(chan struct{})
	const example = "https://example.test/example"
	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/stream":
			if r.Header.Get("Authorization") != "Bearer mock-token" {
				t.Error("model settings did not reach the HTTP request")
			}
			io.WriteString(w, "hello from HTTP")
		case "/hold":
			close(started)
			select {
			case <-r.Context().Done():
			case <-release:
			}
			close(stopped)
		case "/redirect":
			http.Redirect(w, r, "/must-not-follow", http.StatusFound)
		default:
			t.Error("unexpected plugin HTTP destination", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer external.Close()
	defer close(release)
	folder := t.TempDir()
	file := luaFile(t, folder, fmt.Sprintf(`
		assert(os == nil and io == nil and debug == nil and require == nil and dofile == nil)
		local endpoint = %q
		local title = { ["zh-CN"] = "测试插件", en = "Fixture plugin" }
		local calls = 0
		return { version = 1, id = "fixture", name = title, network = { endpoint },
			models = {{ kind = "lua-chat", title = title,
				models = function() return {{ id = "mock" }} end,
				stream = function(settings, request, emit)
					assert(request.model == "mock" and #request.messages == 1)
					local response = ariadne.http({
						url = endpoint .. "/stream",
						headers = { Authorization = "Bearer " .. settings.key }
					}, function(chunk, status)
						assert(status == 200)
						emit({type = "text", text = chunk})
					end)
					assert(response.status == 200)
					emit({type = "usage", input = 2, output = 1})
					emit({type = "finish", reason = "stop"})
				end }},
			contexts = {{ kind = "lua-source", title = title,
				list = function(settings, path)
					assert(path == ".")
					return {{name = "binary.dat", kind = "file"}, {name = "sub", kind = "directory"}}
				end,
				read = function(settings, path) assert(path == "binary.dat"); return "\000\255" end }},
			dashboards = {{ kind = "lua-data", title = title,
				fetch = function(settings, project)
					return { rows = {{project = project}}, empty = ariadne.array(), missing = ariadne.null }
				end }},
			searches = {{ kind = "lua-search", title = title,
				search = function(query, limit)
					calls = calls + 1
					assert(calls == 1)
					if query == "hold" then ariadne.http({url = endpoint .. "/hold"}) end
					if query == "spin" then while true do end end
					if query == "forbidden" then ariadne.http({url = "http://other.test/"}) end
					if query == "redirect" then assert(ariadne.http({url = endpoint .. "/redirect"}).status == 302) end
					return {{ title = query, url = endpoint, snippet = "fixture" }}
				end }},
			links = {{ kind = "lua-link", title = title,
				match = function(url) if url == %q then return "Example" end end,
				preview = function(url) return { url = url, title = "Example" } end }}
		}
	`, external.URL, example))
	if err := host.AddPath(t.Context(), store, folder); err != nil {
		t.Fatal(err)
	}
	if err := host.AddPath(t.Context(), store, file); err == nil {
		t.Fatal("same script loaded via a different path")
	}
	record := host.Records()
	if len(record) != 1 || !reflect.DeepEqual(record[0].Provides, Provides{[]string{"lua-chat"}, []string{"lua-source"}, []string{"lua-data"}, []string{"lua-search"}, []string{"lua-link"}}) {
		t.Fatal(record)
	}
	model, ok := registries.Models.Get("lua-chat")
	if !ok {
		t.Fatal("model was not registered")
	}
	listed, err := model.Models(t.Context(), nil, sdk.ModelListOptions{})
	if err != nil || len(listed) != 1 || listed[0].ID != "mock" {
		t.Fatal(listed, err)
	}
	var reply strings.Builder
	var types []string
	for part := range model.Stream(t.Context(), map[string]any{"key": "mock-token"}, sdk.Request{Model: "mock", Messages: []sdk.Message{{Role: "user", Content: "test"}}}) {
		types = append(types, part.Type)
		reply.WriteString(part.Text)
		if part.Error != "" {
			t.Error(part.Error)
		}
	}
	if len(types) < 3 || !slices.Equal(types[len(types)-2:], []string{"usage", "finish"}) || reply.String() != "hello from HTTP" {
		t.Fatal(types, reply.String())
	}
	source, _ := registries.Sources.Get("lua-source")
	caps, err := source.Connect(t.Context(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	defer caps.Close()
	view, err := caps.Workspace.View(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if entries, err := view.List(t.Context(), ""); err != nil || len(entries) != 2 {
		t.Fatal(entries, err)
	}
	if data, err := view.ReadBytes(t.Context(), "binary.dat", 2); err != nil || !slices.Equal(data, []byte{0, 255}) {
		t.Fatal(data, err)
	}
	if _, err := view.ReadBytes(t.Context(), "binary.dat", 1); err == nil {
		t.Fatal("oversized read accepted")
	}
	data, err := registries.Dashboards.Fetch(t.Context(), "lua-data", nil, "project", time.Second)
	if err != nil || !reflect.DeepEqual(data["empty"], []any{}) || data["missing"] != nil || data["rows"] == nil {
		t.Fatal(data, err)
	}
	link := registries.Links.Preview(t.Context(), example)
	if link == nil || link.Title != "Example" || link.Kind != "lua-link" {
		t.Fatal(link)
	}
	search := host.SearchProviders()[0].Search
	for _, query := range []string{"redirect", "again"} {
		results, err := search(t.Context(), query, 1)
		if err != nil || len(results) != 1 || results[0].Title != query {
			t.Fatal(results, err)
		}
	}
	if _, err := search(t.Context(), "forbidden", 1); err == nil || !strings.Contains(err.Error(), "not declared") {
		t.Fatal("accepted undeclared HTTP origin", err)
	}
	short, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if _, err := search(short, "spin", 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("did not cancel Lua loop", err)
	}
	finished := make(chan error, 1)
	go func() {
		_, err := search(t.Context(), "hold", 1)
		finished <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("search never reached HTTP endpoint")
	}
	if err := host.RemovePath(t.Context(), store, folder); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("unload did not cancel the active call", err)
		}
	case <-time.After(time.Second):
		t.Fatal("unload did not wait for the active call")
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("unload did not close the HTTP request")
	}
	if _, err := search(t.Context(), "stale", 1); err == nil {
		t.Fatal("stale callback could access the unloaded plugin")
	}
	if len(host.Records()) != 0 || len(store.PluginPaths()) != 0 || len(registries.Models.Kinds()) != 0 ||
		len(registries.Sources.Kinds()) != 0 || registries.Dashboards.Get("lua-data") != nil || len(host.SearchProviders()) != 0 ||
		registries.Links.Preview(t.Context(), example) != nil {
		t.Fatal("unload retained a provider")
	}
}

func TestLuaPluginRejectsManagedPathsAndRollsBackUnsavedAdds(t *testing.T) {
	host, registries := fixture(t)
	brokenSettings := filepath.Join(t.TempDir(), "missing", "settings.json")
	store, err := settings.Open(brokenSettings)
	if err != nil {
		t.Fatal(err)
	}
	const script = `return {version=1,id="sample",name={["zh-CN"]="示例"},searches={{
		kind="sample",title={["zh-CN"]="示例"},search=function() return ariadne.array() end
	}}}`
	valid := luaFile(t, t.TempDir(), script)
	if err := host.AddPath(t.Context(), store, valid); err == nil || len(host.Records()) != 0 || len(host.SearchProviders()) != 0 {
		t.Fatal("failed settings write retained plugin", err)
	}
	if len(registries.Models.Kinds()) != 0 || len(store.PluginPaths()) != 0 {
		t.Fatal("failed settings write changed configuration")
	}
	worktree := filepath.Join(t.TempDir(), ".ariadne-worktrees", "thread")
	if err := os.MkdirAll(worktree, 0700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{luaFile(t, worktree, script), luaFile(t, registries.DataRoot, script)} {
		if _, _, err := pluginFile(path, registries.DataRoot); !errors.Is(err, ErrWorkspacePlugin) {
			t.Fatal("accepted managed code", path, err)
		}
	}
	if link := filepath.Join(t.TempDir(), "alias.lua"); os.Symlink(filepath.Join(worktree, "plugin.lua"), link) == nil {
		if _, _, err := pluginFile(link, registries.DataRoot); !errors.Is(err, ErrWorkspacePlugin) {
			t.Fatal("accepted symlink into managed worktree", err)
		}
	}
}

func TestLuaSearchSettingsCheckAndResponse(t *testing.T) {
	host, registries := fixture(t)
	store, err := settings.Open(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	script := `return {
		version = 1,
		id = "custom-search",
		name = { ["zh-CN"] = "自定义搜索", en = "Custom Search" },
		searches = {{
			kind = "custom-search",
			title = { ["zh-CN"] = "自定义搜索", en = "Custom Search" },
			description = { ["zh-CN"] = "支持摘要与检测", en = "Supports answers and checks" },
			settings = {
				type = "object",
				properties = {
					apiKey = { type = "string", title = { ["zh-CN"] = "密钥", en = "Key" }, secret = true },
					region = { type = "string", title = { ["zh-CN"] = "地区", en = "Region" }, default = "cn" },
				},
				required = { "apiKey" },
			},
			search = function(query, limit, cfg)
				return {
					answer = "summary:" .. cfg.region .. ":" .. cfg.apiKey,
					results = {
						{ title = query, url = "https://example.test", snippet = "limit=" .. tostring(limit) },
					},
				}
			end,
			check = function(cfg)
				if cfg.apiKey == "good" then
					return { ok = true }
				end
				return { ok = false, status = 403, message = { ["zh-CN"] = "密钥无效", en = "Invalid key" } }
			end,
		}},
	}`
	file := luaFile(t, t.TempDir(), script)
	if err := host.AddPath(t.Context(), store, file); err != nil {
		t.Fatal(err)
	}
	searchRegistry := registries.Searches
	if searchRegistry == nil {
		searchRegistry = host.Searches()
	}
	provider, ok := searchRegistry.Get("custom-search")
	if !ok || !provider.Capabilities.SupportsCheck || provider.Description["en"] != "Supports answers and checks" {
		t.Fatalf("provider = %+v, ok = %v", provider, ok)
	}
	if searchRegistry.Ready("custom-search", map[string]any{}) {
		t.Fatal("provider missing required apiKey should not be ready")
	}
	if !searchRegistry.Ready("custom-search", map[string]any{"apiKey": "good"}) {
		t.Fatal("provider with required apiKey should be ready")
	}
	res, err := searchRegistry.Execute(t.Context(), "custom-search", map[string]any{"apiKey": "good", "region": "us"}, sdk.SearchQuery{Query: "hello", Limit: 3})
	if err != nil || res.Answer != "summary:us:good" || len(res.Results) != 1 || res.Results[0].Snippet != "limit=3" {
		t.Fatalf("execute = %+v, err = %v", res, err)
	}
	okCheck, err := searchRegistry.Check(t.Context(), "custom-search", map[string]any{"apiKey": "good"})
	if err != nil || !okCheck.OK {
		t.Fatalf("ok check = %+v, err = %v", okCheck, err)
	}
	badCheck, err := searchRegistry.Check(t.Context(), "custom-search", map[string]any{"apiKey": "bad"})
	if err != nil || badCheck.OK || badCheck.Status == nil || *badCheck.Status != 403 || badCheck.Message["en"] != "Invalid key" {
		t.Fatalf("bad check = %+v, err = %v", badCheck, err)
	}
}
