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
	if len(record) != 1 || !reflect.DeepEqual(record[0].Provides, Provides{[]string{"lua-chat"}, []string{"lua-source"}, []string{"lua-data"}, []string{"lua-search"}, []string{"lua-link"}, []string{}}) {
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
				if cfg.apiKey == "malformed" then
					return { ok = "yes" }
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
	malformedCheck, err := searchRegistry.Check(t.Context(), "custom-search", map[string]any{"apiKey": "malformed"})
	if err != nil || malformedCheck.OK {
		t.Fatalf("malformed check = %+v, err = %v", malformedCheck, err)
	}
	expiredCtx, cancelExpired := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancelExpired()
	timeoutCheck, err := searchRegistry.Check(expiredCtx, "custom-search", map[string]any{"apiKey": "good"})
	if err != nil || timeoutCheck.OK {
		t.Fatalf("timeout check = %+v, err = %v", timeoutCheck, err)
	}
	canceledCtx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := searchRegistry.Check(canceledCtx, "custom-search", map[string]any{"apiKey": "good"}); err == nil {
		t.Fatal("expected canceled check to return error")
	}
}

func TestLuaModelPluginNetworkOriginAndHTTPSSE(t *testing.T) {
	allowedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, ": keepalive\r\n\r\n")
			_, _ = io.WriteString(w, "event: delta\r\ndata: {\"text\":\"你好\"}\r\n\r\n")
			_, _ = io.WriteString(w, "data: {\"text\":\"世界\"}\ndata: second-line\n\n")
			_, _ = io.WriteString(w, "event: stop\ndata: {\"stop\":true}\n\n")
			_, _ = io.WriteString(w, "data: {\"text\":\"ignored-after-stop\"}\n\n")
		case "/v1/error":
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":"rate_limited"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer allowedServer.Close()

	blockedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "should never be reached")
	}))
	defer blockedServer.Close()

	host, registries := fixture(t)
	store, err := settings.Open(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	script := `return {
		version = 1,
		id = "lua-sse-model",
		name = { ["zh-CN"] = "Lua SSE 模型", en = "Lua SSE model" },
		models = {{
			kind = "lua-sse",
			title = { ["zh-CN"] = "Lua SSE 模型", en = "Lua SSE model" },
			capabilities = { reasoningScope = "provider" },
			settings = {
				type = "object",
				properties = {
					baseUrl = {
						type = "string",
						title = { ["zh-CN"] = "接口地址", en = "Base URL" },
						networkOrigin = true,
					},
					apiKey = {
						type = "string",
						title = { ["zh-CN"] = "密钥", en = "API key" },
						secret = true,
					},
				},
				required = { "baseUrl" },
			},
			stream = function(cfg, req, emit)
				if req.model == "blocked" then
					ariadne.http_sse({ url = "` + blockedServer.URL + `/v1/stream", method = "POST" }, function() end)
					return
				end
				local path = req.model == "error" and "/error" or "/stream"
				local res = ariadne.http_sse({
					url = cfg.baseUrl .. path,
					method = "POST",
					headers = { ["Authorization"] = "Bearer " .. (cfg.apiKey or "") },
					body = ariadne.json_encode({ model = req.model }),
				}, function(ev, status)
					assert(status == 200)
					if ev.event == "stop" then
						emit({ type = "finish", reason = "stop" })
						return false
					end
					if ev.event == "delta" then
						local parsed = ariadne.json_decode(ev.data)
						emit({ type = "text", text = parsed.text })
					else
						emit({ type = "text", text = ev.data })
					end
				end)
				if res.status ~= 200 then
					emit({
						type = "finish",
						reason = "error",
						error = "HTTP " .. tostring(res.status) .. ":" .. res.headers["retry-after"] .. ":" .. res.body,
						retryable = res.status == 429,
					})
				end
			end,
		}},
	}`
	file := luaFile(t, t.TempDir(), script)
	if err := host.AddPath(t.Context(), store, file); err != nil {
		t.Fatal(err)
	}
	provider, ok := registries.Models.Get("lua-sse")
	if !ok {
		t.Fatal("lua-sse model provider missing")
	}
	if provider.Info.Capabilities.ReasoningScope != sdk.ReasoningScopeProvider {
		t.Fatalf("expected provider reasoning scope, got %q", provider.Info.Capabilities.ReasoningScope)
	}
	if !provider.Info.Settings.Properties["baseUrl"].NetworkOrigin {
		t.Fatal("expected baseUrl field to expose NetworkOrigin")
	}
	if provider.ValidateSettings == nil {
		t.Fatal("expected ValidateSettings for networkOrigin field")
	}
	if err := provider.ValidateSettings(map[string]any{"baseUrl": "file:///etc/passwd"}); err == nil {
		t.Fatal("expected invalid networkOrigin URL to be rejected")
	}
	if err := provider.ValidateSettings(map[string]any{"baseUrl": allowedServer.URL + "/v1"}); err != nil {
		t.Fatalf("expected valid networkOrigin URL to pass, got %v", err)
	}

	// 1. Dynamic networkOrigin allows streaming SSE from user-supplied baseUrl and supports early stop.
	cfg := map[string]any{"baseUrl": allowedServer.URL + "/v1", "apiKey": "secret"}
	var parts []sdk.Part
	for part := range provider.Stream(t.Context(), cfg, sdk.Request{Model: "ok"}) {
		parts = append(parts, part)
	}
	if len(parts) != 3 || parts[0].Text != "你好" || parts[1].Text != "{\"text\":\"世界\"}\nsecond-line" || parts[2].Type != "finish" || parts[2].Reason != "stop" {
		t.Fatalf("unexpected SSE parts: %+v", parts)
	}

	// 2. Non-2xx response returns {status, headers, body} without invoking onEvent.
	parts = nil
	for part := range provider.Stream(t.Context(), cfg, sdk.Request{Model: "error"}) {
		parts = append(parts, part)
	}
	if len(parts) != 1 || parts[0].Type != "finish" || parts[0].Reason != "error" || !parts[0].Retryable || !strings.Contains(parts[0].Error, `HTTP 429:5:{"error":"rate_limited"}`) {
		t.Fatalf("unexpected error parts: %+v", parts)
	}

	// 3. Undeclared origin is still blocked even when baseUrl is allowed.
	parts = nil
	for part := range provider.Stream(t.Context(), cfg, sdk.Request{Model: "blocked"}) {
		parts = append(parts, part)
	}
	if len(parts) != 1 || parts[0].Type != "finish" || parts[0].Reason != "error" || !strings.Contains(parts[0].Error, "origin") {
		t.Fatalf("expected blocked origin error, got %+v", parts)
	}

	// 4. Context providers with networkOrigin also validate settings on save.
	ctxPluginFile := luaFile(t, t.TempDir(), `
local title = {["zh-CN"] = "远程文件源", en = "Remote source"}
return {
	version = 1,
	id = "lua-context-origin",
	name = title,
	contexts = {{
		kind = "lua-remote-ctx",
		title = title,
		settings = {
			type = "object",
			properties = {
				baseUrl = {type = "string", title = title, networkOrigin = true},
			},
		},
		list = function() return {} end,
		read = function() return "" end,
	}},
}
`)
	if err := host.AddPath(t.Context(), store, ctxPluginFile); err != nil {
		t.Fatalf("AddPath(context networkOrigin): %v", err)
	}
	ctxProvider, ok := registries.Sources.Get("lua-remote-ctx")
	if !ok {
		t.Fatal("lua-remote-ctx not registered")
	}
	if ctxProvider.ValidateSettings == nil {
		t.Fatal("expected ValidateSettings on Lua context provider with networkOrigin")
	}
	if err := ctxProvider.ValidateSettings(map[string]any{"baseUrl": "file:///etc/passwd"}, nil); err == nil {
		t.Fatal("expected invalid file:// origin to be rejected by context ValidateSettings")
	}
	if err := ctxProvider.ValidateSettings(map[string]any{"baseUrl": "http://:8080/v1"}, nil); err == nil {
		t.Fatal("expected empty-host origin to be rejected by context ValidateSettings")
	}
	if err := ctxProvider.ValidateSettings(map[string]any{"baseUrl": "https://files.example.com/api"}, nil); err != nil {
		t.Fatalf("expected valid context origin to pass, got %v", err)
	}

	// 5. Schema validation rejects networkOrigin on secret or non-string fields.
	badScripts := []string{
		`return {version=1,id="bad-secret-origin",name={["zh-CN"]="坏"},models={{kind="bad1",title={["zh-CN"]="坏"},settings={type="object",properties={u={type="string",title={["zh-CN"]="u"},secret=true,networkOrigin=true}}},stream=function() end}}}`,
		`return {version=1,id="bad-num-origin",name={["zh-CN"]="坏"},models={{kind="bad2",title={["zh-CN"]="坏"},settings={type="object",properties={u={type="number",title={["zh-CN"]="u"},networkOrigin=true}}},stream=function() end}}}`,
		`return {version=1,id="bad-scope",name={["zh-CN"]="坏"},models={{kind="bad3",title={["zh-CN"]="坏"},capabilities={reasoningScope="invalid"},stream=function() end}}}`,
	}
	for _, bad := range badScripts {
		badFile := luaFile(t, t.TempDir(), bad)
		if err := host.AddPath(t.Context(), store, badFile); err == nil {
			t.Fatalf("expected invalid Lua plugin to fail compilation: %s", bad)
		}
	}
}

func TestLuaNotifierSendCheckAndNetworkOrigin(t *testing.T) {
	host, _ := fixture(t)
	store, err := settings.Open(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}

	var receivedBody string
	var receivedAuth string
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		data, _ := io.ReadAll(r.Body)
		receivedBody = string(data)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer remote.Close()

	pluginPath := luaFile(t, t.TempDir(), `
local title = {["zh-CN"] = "自定义推送", en = "Custom Push"}
return {
	version = 1,
	id = "lua-notifier-fixture",
	name = title,
	notifiers = {{
		kind = "custom-push",
		title = title,
		description = {["zh-CN"] = "通过 HTTP 发送自定义通知"},
		settings = {
			type = "object",
			properties = {
				endpoint = {type = "string", title = {["zh-CN"] = "接口地址"}, networkOrigin = true},
				token = {type = "string", title = {["zh-CN"] = "令牌"}, secret = true},
			},
			required = {"endpoint", "token"},
		},
		send = function(cfg, payload)
			local res = ariadne.http({
				url = cfg.endpoint,
				method = "POST",
				headers = {
					["Content-Type"] = "application/json",
					["Authorization"] = "Bearer " .. cfg.token,
				},
				body = ariadne.json_encode({
					event = payload.event,
					title = payload.title,
					body = payload.body,
					needsMe = payload.needsMe,
				}),
			})
			assert(res.status == 200, "unexpected status " .. res.status)
		end,
		check = function(cfg)
			local res = ariadne.http({
				url = cfg.endpoint,
				method = "POST",
				headers = { ["Authorization"] = "Bearer " .. cfg.token },
				body = ariadne.json_encode({ ping = true }),
			})
			return { ok = res.status == 200, status = res.status, message = { ["zh-CN"] = "连通正常", en = "Connected" } }
		end,
	}},
}
`)
	if err := host.AddPath(t.Context(), store, pluginPath); err != nil {
		t.Fatalf("AddPath(lua notifier): %v", err)
	}
	records := host.Records()
	if len(records) != 1 || !slices.Equal(records[0].Provides.Notifiers, []string{"custom-push"}) {
		t.Fatalf("unexpected plugin records: %+v", records)
	}

	registry := host.Notifiers()
	if err := registry.Validate("custom-push", map[string]any{"endpoint": "ftp://bad.example.com"}); err == nil {
		t.Fatal("expected invalid networkOrigin to fail validation")
	}

	cfg := map[string]any{"endpoint": remote.URL + "/notify", "token": "secret-123"}
	checkRes, err := registry.Check(t.Context(), "custom-push", cfg)
	if err != nil || !checkRes.OK || checkRes.Status == nil || *checkRes.Status != 200 || checkRes.Message["zh-CN"] != "连通正常" {
		t.Fatalf("Check failed: res=%+v err=%v", checkRes, err)
	}
	if receivedAuth != "Bearer secret-123" {
		t.Fatalf("auth header = %q", receivedAuth)
	}

	err = registry.Send(t.Context(), "custom-push", cfg, sdk.NotificationPayload{
		Event:   "attention.opened",
		Title:   "等你回答 · Ariadne",
		Body:    "确认合并？",
		NeedsMe: 2,
	})
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}
	if !strings.Contains(receivedBody, "确认合并？") || !strings.Contains(receivedBody, "attention.opened") {
		t.Fatalf("unexpected received body: %s", receivedBody)
	}

	// Unloading removes the notifier cleanly.
	if err := host.RemovePath(t.Context(), store, pluginPath); err != nil {
		t.Fatalf("RemovePath: %v", err)
	}
	if registry.Has("custom-push") {
		t.Fatal("notifier remained registered after plugin removal")
	}
}
