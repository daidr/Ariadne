package settings

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/sdk"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}
func jsonObject(raw string) Object {
	var value Object
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		panic(err)
	}
	return value
}
func equalJSON(t *testing.T, got, want any) {
	t.Helper()
	a, _ := json.Marshal(got)
	b, _ := json.Marshal(want)
	var av, bv any
	json.Unmarshal(a, &av)
	json.Unmarshal(b, &bv)
	if !reflect.DeepEqual(av, bv) {
		t.Fatalf("got %s\nwant %s", a, b)
	}
}
func TestSettingsPersistenceIsolationAndRollback(t *testing.T) {
	store := newStore(t)
	value, err := store.UpdateConcurrency(jsonObject(`{"slots":5,"reserved":0}`))
	if err != nil {
		t.Fatal(err)
	}
	value["slots"] = 100.
	current := store.Current()
	object(current["concurrency"])["slots"] = 100.
	if object(store.Current()["concurrency"])["slots"] != 5. {
		t.Fatal("caller mutated stored settings")
	}
	reopened, err := Open(store.path)
	if err != nil {
		t.Fatal(err)
	}
	equalJSON(t, store.Current(), reopened.Current())
	if runtime.GOOS != "windows" {
		info, err := os.Stat(store.path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("settings permissions", info, err)
		}
	}
	before := store.Current()
	if _, err := store.UpdateConcurrency(jsonObject(`{"slots":0,"reserved":0}`)); err == nil {
		t.Fatal("invalid slots accepted")
	}
	equalJSON(t, store.Current(), before)
	if err := os.Remove(store.path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(store.path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateConcurrency(jsonObject(`{"slots":6,"reserved":1}`)); err == nil {
		t.Fatal("failed rename unexpectedly saved")
	}
	equalJSON(t, store.Current(), before)
	entries, err := os.ReadDir(filepath.Dir(store.path))
	if err != nil || len(entries) != 1 {
		t.Fatal("failed write retained temporary files", entries, err)
	}
}
func TestSettingsConcurrentWritersAndSecretRelease(t *testing.T) {
	store := newStore(t)
	var group sync.WaitGroup
	for i := range 20 {
		group.Add(1)
		go func() {
			defer group.Done()
			if err := store.SaveSourceSecrets(string(rune('a'+i)), Object{"token": "secret"}); err != nil {
				t.Error(err)
			}
		}()
	}
	group.Wait()
	if len(object(store.Current()["sourceSecrets"])) != 20 {
		t.Fatal("concurrent settings updates lost")
	}
	if _, err := store.UpdateProjectEnv("a", []any{Object{"name": "TOKEN", "value": "private"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateProjectEnv("a", []any{Object{"name": "TOKEN", "value": ""}}); err != nil {
		t.Fatal(err)
	}
	if store.TurnSettings("a")["env"].(Object)["TOKEN"] != "private" {
		t.Fatal("empty secret did not retain stored value")
	}
	for _, name := range []string{"ARIADNE_TOKEN", "ariadne_home", "GIT_CONFIG_COUNT", "Git_Configanything", "1BAD"} {
		if _, err := store.UpdateProjectEnv("a", []any{Object{"name": name, "value": "x"}}); err == nil {
			t.Fatal("reserved environment accepted", name)
		}
	}
	if err := store.ForgetProject("a"); err != nil {
		t.Fatal(err)
	}
	if len(store.SourceSecrets("a")) != 0 || len(store.ProjectEnvNames("a")) != 0 {
		t.Fatal("deleted project retained secrets")
	}
}
func testSearchKinds() []sdk.SearchProviderInfo {
	schema := func(defaultURL string) sdk.SettingsSchema {
		return sdk.SettingsSchema{
			Type: "object",
			Properties: map[string]sdk.SettingField{
				"baseUrl": {Type: "string", Title: sdk.LocalizedText{"zh-CN": "接口地址", "en": "Base URL"}, Default: defaultURL},
				"apiKey":  {Type: "string", Title: sdk.LocalizedText{"zh-CN": "API Key", "en": "API key"}, Secret: true},
			},
			Required:   []string{"apiKey"},
			FieldOrder: []string{"baseUrl", "apiKey"},
		}
	}
	return []sdk.SearchProviderInfo{
		{Kind: "tavily", Title: sdk.LocalizedText{"zh-CN": "Tavily", "en": "Tavily"}, Settings: schema("https://api.tavily.com"), Capabilities: sdk.SearchCapabilities{SupportsCheck: true}},
		{Kind: "deepseek", Title: sdk.LocalizedText{"zh-CN": "DeepSeek Search API", "en": "DeepSeek Search API"}, Settings: schema("https://api.deepseek.com/v1/tools"), Capabilities: sdk.SearchCapabilities{MaxQueryRunes: 128, MaxResults: 50, SupportsCheck: true}},
	}
}

func testSearchValidate(_ string, values Object) error {
	if raw, ok := values["apiKey"].(string); ok && strings.ContainsAny(raw, " \t\n\r") {
		return bad("apiKey")
	}
	return nil
}

func TestSettingsSearchKeysAndSourceSplit(t *testing.T) {
	store := newStore(t)
	searchKinds := testSearchKinds()
	if _, err := store.UpdateSearch(jsonObject(`{"provider":"deepseek","services":{"deepseek":{"apiKey":" private-token ","baseUrl":"https://example.test/v1/tools/"}}}`), searchKinds, testSearchValidate); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateSearch(jsonObject(`{"services":{"deepseek":{"apiKey":""}}}`), searchKinds, testSearchValidate); err != nil {
		t.Fatal(err)
	}
	public, _ := json.Marshal(store.PublicSearch(searchKinds, nil, nil))
	if strings.Contains(string(public), "private-token") || !strings.Contains(string(public), `"keySet":true`) || !strings.Contains(string(public), `"ready":true`) || !strings.Contains(string(public), `"secretsSet":["apiKey"]`) {
		t.Fatal(string(public))
	}
	draft, err := store.SearchDraft(jsonObject(`{"services":{"deepseek":{"apiKey":null}}}`), searchKinds, testSearchValidate)
	if err != nil || text(object(object(draft["services"])["deepseek"])["apiKey"]) != "" {
		t.Fatal(draft, err)
	}
	if text(object(object(object(store.Current()["search"])["services"])["deepseek"])["apiKey"]) != "private-token" {
		t.Fatal("checking draft saved it")
	}
	if err := store.SaveSourceSecrets("p", Object{"token": "old", "host.token": "host"}); err != nil {
		t.Fatal(err)
	}
	plain, secrets := store.SplitSourceSettings("p", "git", Object{"token": "", "path": "repo"}, "git", "", []string{"token"})
	equalJSON(t, plain, Object{"path": "repo"})
	equalJSON(t, secrets, Object{"token": "old"})
	_, secrets = store.SplitSourceSettings("p", "new", Object{"token": ""}, "git", "", []string{"token"})
	if len(secrets) != 0 {
		t.Fatal("changed source inherited secret")
	}
	if err := store.SaveReferenceSecrets("p", "r", Object{"token": "reference"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSourceSecrets("p", Object{"token": "new"}); err != nil {
		t.Fatal(err)
	}
	if store.SourceSecrets("p")["reference.r.token"] != "reference" || !reflect.DeepEqual(store.SourceSecretsSet("p"), []string{"token"}) {
		t.Fatal(store.SourceSecrets("p"))
	}
}

func TestMCPSecretsAndPrivateDirectoryBoundary(t *testing.T) {
	store := newStore(t)
	for _, cwd := range []string{filepath.Dir(store.path), filepath.Join(filepath.Dir(store.path), "new-child")} {
		if err := store.UpdateMCP(Object{"servers": []any{Object{"id": "local", "transport": "stdio", "command": "tool", "cwd": cwd}}}); err == nil {
			t.Fatal("MCP can start inside private settings directory", cwd)
		}
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(filepath.Dir(store.path), alias); err == nil {
		if !insideDir(filepath.Join(alias, "future-child"), filepath.Dir(store.path)) {
			t.Fatal("missing directory under symlink bypassed private boundary")
		}
	}
	if err := store.UpdateMCP(jsonObject(`{"servers":[{"id":"remote","transport":"http","url":"https://example.test/mcp","headers":[{"name":"Authorization","value":"private"}]}]}`)); err != nil {
		t.Fatal(err)
	}
	public, _ := json.Marshal(store.PublicMCP())
	if strings.Contains(string(public), "private") || !strings.Contains(string(public), `"headers":["Authorization"]`) {
		t.Fatal(string(public))
	}
	for _, url := range []string{"https://user:secret@example.test", "https://:secret@example.test", "file:///tmp/mcp"} {
		if err := store.UpdateMCP(Object{"servers": []any{Object{"id": "bad", "transport": "http", "url": url}}}); err == nil {
			t.Fatal("MCP accepted unsafe endpoint", url)
		}
	}
}

func TestSettingsStoreLifecycle(t *testing.T) {
	type action struct {
		Op        string `json:"op"`
		Value     any    `json:"value,omitempty"`
		Project   string `json:"project,omitempty"`
		Reference string `json:"reference,omitempty"`
	}
	type testCase struct {
		Initial Object   `json:"initial,omitempty"`
		Actions []action `json:"actions"`
	}
	cases := []testCase{
		{Actions: []action{{Op: "concurrency", Value: jsonObject(`{"slots":5,"reserved":0}`)}, {Op: "concurrency", Value: jsonObject(`{"slots":0,"reserved":1}`)}, {Op: "input", Value: jsonObject(`{"submitOn":"mod-enter","snippets":[{"id":"a","name":"  中文  ","text":" prompt "}],"keys":{"focus":" ALT+F "}}`)}, {Op: "input", Value: jsonObject(`{"submitOn":"enter","snippets":[],"keys":{"focus":"mod+k"}}`)}, {Op: "work", Value: jsonObject(`{"instructions":"  plain words  ","rules":[{"pattern":" git push* ","action":"ask"}],"limits":{"commandTimeout":300,"commandTimeoutMax":600,"compactShare":0.7}}`)}, {Op: "work", Value: jsonObject(`{"limits":{"commandTimeout":700,"commandTimeoutMax":600}}`)}, {Op: "add", Value: "another\n  line\u0085text"}, {Op: "add", Value: "another line\u0085text"}, {Op: "remove", Value: "not there"}, {Op: "remove", Value: "plain words"}}},
		{Initial: jsonObject(`{"locale":"en","extra":"retained","instructions":false,"models":{"extra":1,"roles":{"thread":{"model":"m","effort":"turbo","fallback":"f"}}},"input":{"snippets":[{"id":"a","name":" a ","text":" t "},{"id":"a","name":"dup","text":"dup"},{"id":"?","name":"bad","text":"bad"}],"keys":{"search":"invalid","focus":" SHIFT+F "}},"commands":{"rules":[{"pattern":" ok* ","action":"allow"},{"pattern":"","action":"deny"}]},"limits":{"commandTimeout":700,"commandTimeoutMax":10,"modelRetries":1.5},"search":{"apiKey":"old-tavily"}}`), Actions: []action{{Op: "work", Value: Object{}}, {Op: "search", Value: jsonObject(`{"provider":"deepseek","services":{"deepseek":{"apiKey":" key ","baseUrl":"https://example.test/v1/"}}}`)}, {Op: "search", Value: jsonObject(`{"services":{"deepseek":{"apiKey":""}}}`)}, {Op: "search", Value: jsonObject(`{"services":{"deepseek":{"apiKey":null}}}`)}, {Op: "search", Value: jsonObject(`{"services":{"deepseek":{"apiKey":"bad key"}}}`)}, {Op: "search", Value: jsonObject(`{"provider":"wrong"}`)}}},
		{Actions: []action{{Op: "env", Project: "p", Value: []any{Object{"name": "TOKEN", "value": "private"}}}, {Op: "env", Project: "p", Value: []any{Object{"name": "TOKEN", "value": ""}}}, {Op: "env", Project: "p", Value: []any{Object{"name": "ARIADNE_TOKEN", "value": "x"}}}, {Op: "source", Project: "p", Value: Object{"token": "main"}}, {Op: "reference", Project: "p", Reference: "r", Value: Object{"token": "ref"}}, {Op: "source", Project: "p", Value: Object{"token": "updated"}}, {Op: "reference", Project: "p", Reference: "r", Value: Object{}}, {Op: "forget", Project: "p"}}},
	}
	cases = append(cases, testCase{Actions: []action{
		{Op: "mcp", Value: jsonObject(`{"servers":[{"id":"local","name":" Local ","transport":"stdio","command":" tool ","args":["--flag"],"env":[{"name":"TOKEN","value":"private"}],"tools":{"read":"read","default":"ask","blocked":"off"}}]}`)},
		{Op: "mcp", Value: jsonObject(`{"servers":[{"id":"local","transport":"stdio","command":"tool","env":[{"name":"TOKEN","value":""}]}]}`)},
		{Op: "mcp", Value: jsonObject(`{"servers":[{"id":"remote","transport":"http","url":"https://example.test/mcp","headers":[{"name":"Authorization","value":"Bearer private"}]}]}`)},
		{Op: "mcp", Value: jsonObject(`{"servers":[{"id":"remote","transport":"http","url":"https://example.test/mcp","headers":[{"name":"Authorization","value":""}]}]}`)},
		{Op: "mcp", Value: jsonObject(`{"servers":[{"id":"bad id","transport":"http","url":"https://example.test"}]}`)},
		{Op: "mcp", Value: jsonObject(`{"servers":[{"id":"remote","transport":"http","url":"file:///etc/passwd"}]}`)},
	}})
	cases = append(cases, testCase{Actions: []action{
		{Op: "models", Value: jsonObject(`{"providers":[{"id":"p","kind":"anthropic","name":" Main ","settings":{"baseUrl":"","apiKey":"secret"},"headers":[{"name":"X-Key","value":"header-secret"}],"extraBody":{"temperature":0.2},"timeoutSeconds":120}],"models":[{"id":"m","provider":"p","model":" model ","contextTokens":32000,"maxTokens":4096,"note":" note ","efforts":["max","low","low"],"price":{"currency":"USD","input":1,"output":2,"cacheRead":0}}],"roles":{"thread":{"model":"m","effort":"high","fallback":"m"},"research":{"fallback":"nonexistent"}},"showCost":true}`)},
		{Op: "resolve", Value: Object{"role": "thread"}}, {Op: "resolve", Value: Object{"role": "coordinator"}}, {Op: "resolve", Value: Object{"role": "triage"}}, {Op: "resolve", Value: Object{"role": "research"}}, {Op: "choices"},
		{Op: "models", Value: jsonObject(`{"providers":[{"id":"p","kind":"anthropic","settings":{"baseUrl":"https://example.test","apiKey":""},"headers":[{"name":"X-Key","value":""}]}],"models":[{"id":"m","provider":"p","model":"model"}],"roles":{"thread":{"model":"m"}}}`)},
		{Op: "models", Value: jsonObject(`{"providers":[{"id":"p","kind":"anthropic","settings":{"apiKey":null}}],"models":[{"id":"m","provider":"p","model":"model"}]}`)},
		{Op: "models", Value: jsonObject(`{"providers":[{"id":"p","kind":"missing","settings":{}}],"models":[]}`)},
		{Op: "models", Value: jsonObject(`{"providers":[{"id":"p","kind":"anthropic","settings":{},"extraBody":[]}],"models":[]}`)},
		{Op: "models", Value: jsonObject(`{"providers":[],"models":[],"roles":{"thread":{"model":"missing"}}}`)},
	}})
	cases = append(cases, testCase{Actions: []action{
		{Op: "models", Value: jsonObject(`{"providers":[{"id":"p","kind":"anthropic","settings":{"apiKey":"k"}}],"models":[{"id":"main","provider":"p","model":"big"},{"id":"quick","provider":"p","model":"fast","contextTokens":32000,"efforts":[]},{"id":"backup","provider":"p","model":"backup"}],"roles":{"thread":{"model":"main","effort":"max","fallback":"backup"},"triage":{"model":"quick"}}}`)},
		{Op: "resolve", Value: Object{"role": "thread"}}, {Op: "resolve", Value: Object{"role": "thread", "given": "quick"}}, {Op: "resolve", Value: Object{"role": "coordinator"}}, {Op: "resolve", Value: Object{"role": "triage"}}, {Op: "resolve", Value: Object{"triage": true, "contextTokens": float64(1000)}}, {Op: "resolve", Value: Object{"triage": true, "contextTokens": float64(20000)}}, {Op: "choices"},
	}})
	cases = append(cases, testCase{Actions: []action{
		{Op: "useFound", Value: jsonObject(`{"setup":{"id":"anthropic:ENV_KEY","kind":"anthropic","title":{"zh-CN":"发现","en":"Found"},"from":{"env":"ENV_KEY"},"settings":{"apiKey":"key"}},"model":"new-model","info":{"contextTokens":32000,"images":false,"efforts":[],"price":{"currency":"USD","input":1,"output":2}}}`)},
		{Op: "resolve", Value: Object{"role": "coordinator"}},
	}})
	native := []any{}
	for _, test := range cases {
		path := filepath.Join(t.TempDir(), "config.json")
		if test.Initial != nil {
			data, _ := json.Marshal(test.Initial)
			os.WriteFile(path, data, 0600)
		}
		store, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		steps := []any{Object{"config": store.Current()}}
		for _, action := range test.Actions {
			var result any
			var err error
			switch action.Op {
			case "models":
				result, err = store.UpdateModels(action.Value, testModelKinds())
			case "resolve":
				value := object(action.Value)
				var resolved *ResolvedModel
				if size, exists := value["contextTokens"]; exists {
					resolved, err = store.ResolveCoordinator(value["triage"] == true, size.(float64), testModelKinds())
				} else {
					resolved, err = store.ResolveModel(text(value["role"]), text(value["given"]), text(value["effort"]), testModelKinds())
				}
				result = stableResolved(resolved)
			case "choices":
				result = store.ModelChoices(nil)
			case "useFound":
				value := object(action.Value)
				data, _ := json.Marshal(value["setup"])
				var setup FoundSetup
				json.Unmarshal(data, &setup)
				result, err = store.UseFound(setup, text(value["model"]), object(value["info"]), time.UnixMilli(1800000000000))
			case "concurrency":
				result, err = store.UpdateConcurrency(action.Value)
			case "input":
				result, err = store.UpdateInput(action.Value)
			case "work":
				result, err = store.UpdateWork(action.Value)
			case "add":
				result, err = store.AddInstruction(text(action.Value))
			case "remove":
				result, err = store.RemoveInstruction(text(action.Value))
			case "search":
				_, err = store.UpdateSearch(action.Value, testSearchKinds(), testSearchValidate)
				result = store.PublicSearch(testSearchKinds(), nil, nil)
			case "env":
				result, err = store.UpdateProjectEnv(action.Project, action.Value)
			case "mcp":
				err = store.UpdateMCP(action.Value)
			case "source":
				err = store.SaveSourceSecrets(action.Project, object(action.Value))
			case "reference":
				err = store.SaveReferenceSecrets(action.Project, action.Reference, object(action.Value))
			case "forget":
				err = store.ForgetProject(action.Project)
			}
			step := Object{"config": store.Current()}
			if err != nil {
				var localized *domainerr.Error
				if !errors.As(err, &localized) {
					t.Fatal(err)
				}
				step["error"] = localized
			} else {
				step["result"] = result
			}
			steps = append(steps, step)
		}
		native = append(native, steps)
	}
	if len(native) != len(cases) {
		t.Fatalf("executed cases = %d, want %d", len(native), len(cases))
	}
}

func stableResolved(model *ResolvedModel) any {
	if model == nil {
		return nil
	}
	data, _ := json.Marshal(model)
	var value Object
	json.Unmarshal(data, &value)
	value["provider"] = model.Provider.Kind
	return value
}

func testModelKinds() []sdk.ModelProviderInfo {
	return []sdk.ModelProviderInfo{{Kind: "anthropic", Title: sdk.LocalizedText{"zh-CN": "Anthropic"}, Settings: sdk.SettingsSchema{Type: "object", Properties: map[string]sdk.SettingField{"baseUrl": {Type: "string", Default: "https://api.anthropic.com"}, "apiKey": {Type: "string", Secret: true}}, Required: []string{"baseUrl", "apiKey"}}}}
}

func TestModelProviderSecretsDefaultsAndDraft(t *testing.T) {
	store := newStore(t)
	kinds := testModelKinds()
	input := jsonObject(`{"providers":[{"id":"p","kind":"anthropic","settings":{"baseUrl":"","apiKey":"secret"},"headers":[{"name":"Authorization","value":"header-secret"}]}],"models":[{"id":"m","provider":"p","model":"model"}],"roles":{"thread":{"model":"m"}}}`)
	if _, err := store.UpdateModels(input, kinds); err != nil {
		t.Fatal(err)
	}
	public, _ := json.Marshal(store.PublicModels(kinds))
	if strings.Contains(string(public), "header-secret") || strings.Contains(string(public), `"apiKey":"secret"`) {
		t.Fatal("model credentials leaked", string(public))
	}
	draft, err := store.DraftProvider(Object{"id": "p", "kind": "anthropic", "settings": Object{"apiKey": ""}}, kinds)
	if err != nil {
		t.Fatal(err)
	}
	if draft.Settings["baseUrl"] != "https://api.anthropic.com" || draft.Settings["apiKey"] != "secret" || draft.Headers["Authorization"] != "header-secret" || strings.Contains(draft.Hint, "secret") {
		t.Fatal(draft)
	}
	unknown, _ := json.Marshal(store.PublicModels(nil))
	if strings.Contains(string(unknown), "header-secret") || strings.Contains(string(unknown), `"apiKey":"secret"`) {
		t.Fatal("removed provider exposed credentials", string(unknown))
	}
	if _, err := store.DraftProvider(Object{"kind": "anthropic", "settings": Object{}}, kinds); err == nil {
		t.Fatal("draft missing required API key accepted")
	}
}

func TestResolvedSettingsCannotMutatePrivateConfiguration(t *testing.T) {
	store := newStore(t)
	kinds := testModelKinds()
	input := jsonObject(`{"providers":[{"id":"p","kind":"anthropic","settings":{"apiKey":"secret","nested":{"key":"value"}},"headers":[{"name":"X-Key","value":"header"}],"extraBody":{"nested":{"temperature":1}}}],"models":[{"id":"m","provider":"p","model":"model","price":{"currency":"USD","input":1,"output":2}}],"roles":{"thread":{"model":"m"}}}`)
	if _, err := store.UpdateModels(input, kinds); err != nil {
		t.Fatal(err)
	}
	before := store.Current()
	resolved, err := store.ResolveModel("thread", "", "", kinds)
	if err != nil {
		t.Fatal(err)
	}
	object(resolved.Settings["nested"])["key"] = "changed"
	object(resolved.Request["headers"])["X-Key"] = "changed"
	object(object(resolved.Request["extraBody"])["nested"])["temperature"] = 2.
	resolved.Price["input"] = 99.
	equalJSON(t, store.Current(), before)
	turn := store.TurnSettings("project")
	object(turn["limits"])["commandTimeout"] = 1.
	equalJSON(t, store.Current(), before)
}

func TestSearchConfigMigrationAndDynamicPlugins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	legacy := `{"search":{"apiKey":"legacy-tavily-token"}}`
	if err := os.WriteFile(path, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	kinds := append(testSearchKinds(), sdk.SearchProviderInfo{
		Kind:        "custom-lua",
		Title:       sdk.LocalizedText{"zh-CN": "自定义搜索", "en": "Custom Search"},
		Description: sdk.LocalizedText{"zh-CN": "外部 Lua 搜索插件", "en": "External Lua search"},
		Settings: sdk.SettingsSchema{
			Type: "object",
			Properties: map[string]sdk.SettingField{
				"endpoint": {Type: "string", Title: sdk.LocalizedText{"zh-CN": "地址"}, Default: "https://custom.test"},
				"token":    {Type: "string", Title: sdk.LocalizedText{"zh-CN": "密钥"}, Secret: true},
				"maxAge":   {Type: "number", Title: sdk.LocalizedText{"zh-CN": "天数"}, Default: float64(7)},
				"strict":   {Type: "boolean", Title: sdk.LocalizedText{"zh-CN": "严格模式"}, Default: true},
				"region":   {Type: "enum", Title: sdk.LocalizedText{"zh-CN": "地区"}, Options: []string{"cn", "us"}, Default: "cn"},
			},
			Required:   []string{"token"},
			FieldOrder: []string{"endpoint", "token", "maxAge", "strict", "region"},
		},
		Capabilities: sdk.SearchCapabilities{ReturnsAnswer: true, SupportsCheck: true},
	})

	// Legacy top-level apiKey migrates cleanly to tavily, and unset boolean defaults are surfaced.
	public := store.PublicSearch(kinds, nil, nil)
	if text(public["provider"]) != "tavily" || public["ready"] != true || public["keySet"] != true {
		t.Fatalf("migrated public search = %#v", public)
	}
	services := list(public["services"])
	if len(services) != 3 || object(object(services[2])["values"])["strict"] != true {
		t.Fatalf("expected unset boolean default true in PublicSearch, got %#v", services)
	}
	effectiveTavily := store.SearchSettings(kinds[0])
	if text(effectiveTavily["apiKey"]) != "legacy-tavily-token" || text(effectiveTavily["baseUrl"]) != "https://api.tavily.com" {
		t.Fatalf("effective tavily = %#v", effectiveTavily)
	}

	// Dynamic external plugin with custom field types persists cleanly and hides secrets.
	if _, err := store.UpdateSearch(jsonObject(`{
		"provider":"custom-lua",
		"services":{
			"custom-lua":{"endpoint":"https://override.test","token":"top-secret-val","maxAge":14,"strict":false,"region":"us"}
		}
	}`), kinds, nil); err != nil {
		t.Fatal(err)
	}
	updatedPublic := store.PublicSearch(kinds, nil, []string{"OpenAI / gpt-5"})
	updatedServices := list(updatedPublic["services"])
	if len(updatedServices) != 3 || object(object(updatedServices[2])["values"])["strict"] != false {
		t.Fatalf("expected saved boolean false in PublicSearch, got %#v", updatedServices)
	}
	publicJSON, _ := json.Marshal(updatedPublic)
	if strings.Contains(string(publicJSON), "top-secret-val") || strings.Contains(string(publicJSON), "legacy-tavily-token") {
		t.Fatalf("secret leaked in PublicSearch: %s", string(publicJSON))
	}
	effectiveCustom := store.SearchSettings(kinds[2])
	if text(effectiveCustom["endpoint"]) != "https://override.test" || text(effectiveCustom["token"]) != "top-secret-val" ||
		effectiveCustom["maxAge"] != float64(14) || effectiveCustom["strict"] != false || text(effectiveCustom["region"]) != "us" {
		t.Fatalf("effective custom = %#v", effectiveCustom)
	}
}

func TestModelChoicesFilterDisabledKindsAndUsageSummary(t *testing.T) {
	store := newStore(t)
	allKinds := []sdk.ModelProviderInfo{
		testModelKinds()[0],
		{
			Kind:  "openai-compatible",
			Title: sdk.LocalizedText{"zh-CN": "OpenAI"},
			Settings: sdk.SettingsSchema{
				Type: "object",
				Properties: map[string]sdk.SettingField{
					"baseUrl": {Type: "string", Default: "https://api.openai.com/v1"},
					"apiKey":  {Type: "string", Secret: true},
				},
			},
		},
	}
	input := jsonObject(`{
		"providers":[
			{"id":"p-anth","kind":"anthropic","name":"Claude","settings":{"apiKey":"s1"}},
			{"id":"p-oa","kind":"openai-compatible","name":"OpenAI","settings":{"apiKey":"s2"}}
		],
		"models":[
			{"id":"m-anth","provider":"p-anth","model":"claude-sonnet"},
			{"id":"m-oa","provider":"p-oa","model":"gpt-5"}
		],
		"roles":{
			"thread":{"model":"m-oa","fallback":"m-anth"},
			"coordinator":{"model":"m-anth"}
		}
	}`)
	if _, err := store.UpdateModels(input, allKinds); err != nil {
		t.Fatal(err)
	}

	// Filtering by only openai-compatible hides anthropic model from ModelChoices.
	filtered := store.ModelChoices(allKinds[1:])
	if len(filtered) != 1 || text(object(filtered[0])["id"]) != "m-oa" {
		t.Fatalf("expected only m-oa when anthropic is disabled, got %#v", filtered)
	}

	// ModelUsageByKinds reports provider names and active roles (fallback is ignored).
	providers, usedRoles := store.ModelUsageByKinds([]string{"anthropic"})
	if len(providers) != 1 || providers[0] != "Claude" {
		t.Fatalf("anthropic providers = %#v", providers)
	}
	if len(usedRoles) != 1 || usedRoles[0] != "coordinator" {
		t.Fatalf("anthropic roles = %#v", usedRoles)
	}
}

func TestLegacyFallbackIgnoredAndEffortResolution(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	legacy := `{
		"models": {
			"providers": [{"id": "p", "kind": "anthropic", "name": "Claude", "settings": {"apiKey": "k"}}],
			"models": [
				{"id": "m-main", "provider": "p", "model": "claude-opus", "efforts": ["low", "medium", "high", "max"]},
				{"id": "m-fast", "provider": "p", "model": "claude-haiku", "efforts": ["low", "medium"]},
				{"id": "m-plain", "provider": "p", "model": "claude-plain", "efforts": []}
			],
			"roles": {
				"thread": {"model": "m-main", "effort": "high", "fallback": "m-fast"},
				"coordinator": {"model": null, "effort": "low", "fallback": "m-fast"},
				"research": {"model": null, "effort": "low", "fallback": "m-fast"}
			}
		}
	}`
	if err := os.WriteFile(path, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open legacy config with fallback failed: %v", err)
	}
	kinds := testModelKinds()
	rolesCfg := object(object(store.Current()["models"])["roles"])
	for _, role := range roles {
		if _, hasFallback := object(rolesCfg[role])["fallback"]; hasFallback {
			t.Fatalf("role %s unexpectedly retained fallback: %#v", role, rolesCfg[role])
		}
	}

	// Default thread uses roles.thread.model and roles.thread.effort.
	parent, err := store.ResolveModel("thread", "", "", kinds)
	if err != nil || parent == nil || parent.Model != "claude-opus" || parent.Effort != "high" {
		t.Fatalf("default thread resolved = %+v, err=%v", parent, err)
	}

	// Explicit thread model + effort overrides both; explicit model + empty effort uses model default.
	overrideHigh, err := store.ResolveModel("thread", "m-fast", "max", kinds)
	if err != nil || overrideHigh == nil || overrideHigh.Model != "claude-haiku" || overrideHigh.Effort != "medium" {
		t.Fatalf("clamped thread override = %+v, err=%v", overrideHigh, err)
	}
	overrideDefaultEffort, err := store.ResolveModel("thread", "m-main", "", kinds)
	if err != nil || overrideDefaultEffort == nil || overrideDefaultEffort.Model != "claude-opus" || overrideDefaultEffort.Effort != "" {
		t.Fatalf("explicit model with default effort = %+v, err=%v", overrideDefaultEffort, err)
	}

	// ConversationModel returns the resolved effective effort.
	if conv := store.ConversationModel("thread", "", ""); conv["id"] != "m-main" || conv["effort"] != "high" {
		t.Fatalf("default ConversationModel = %#v", conv)
	}
	if conv := store.ConversationModel("thread", "m-main", ""); conv["id"] != "m-main" || conv["effort"] != nil {
		t.Fatalf("explicit model default effort ConversationModel = %#v", conv)
	}
	if conv := store.ConversationModel("thread", "m-fast", "max"); conv["id"] != "m-fast" || conv["effort"] != "medium" {
		t.Fatalf("override ConversationModel = %#v", conv)
	}

	// A thread override referencing a deleted model falls back to both the default thread model and default thread effort.
	deletedResolved, err := store.ResolveModel("thread", "m-deleted", "low", kinds)
	if err != nil || deletedResolved == nil || deletedResolved.Model != "claude-opus" || deletedResolved.Effort != "high" {
		t.Fatalf("deleted thread override resolved = %+v, err=%v", deletedResolved, err)
	}
	if conv := store.ConversationModel("thread", "m-deleted", "low"); conv["id"] != "m-main" || conv["effort"] != "high" {
		t.Fatalf("deleted thread override ConversationModel = %#v", conv)
	}

	// ResolveResearch with model=null inherits parent model but applies roles.research.effort without mutating parent.
	researchModel, err := store.ResolveResearch(parent, kinds)
	if err != nil || researchModel == nil || researchModel.Model != "claude-opus" || researchModel.Effort != "low" {
		t.Fatalf("ResolveResearch(same as asking + low) = %+v, err=%v", researchModel, err)
	}
	if parent.Effort != "high" {
		t.Fatalf("ResolveResearch mutated parent effort: %q", parent.Effort)
	}

	// ResolveResearch with explicit research model + effort uses that model and effort.
	if _, err := store.UpdateModels(jsonObject(`{
		"providers": [{"id": "p", "kind": "anthropic", "name": "Claude", "settings": {"apiKey": "k"}}],
		"models": [
			{"id": "m-main", "provider": "p", "model": "claude-opus", "efforts": ["low", "medium", "high", "max"]},
			{"id": "m-fast", "provider": "p", "model": "claude-haiku", "efforts": ["low", "medium"]}
		],
		"roles": {
			"thread": {"model": "m-main", "effort": "high"},
			"research": {"model": "m-fast", "effort": "high"}
		}
	}`), kinds); err != nil {
		t.Fatal(err)
	}
	researchExplicit, err := store.ResolveResearch(parent, kinds)
	if err != nil || researchExplicit == nil || researchExplicit.Model != "claude-haiku" || researchExplicit.Effort != "medium" {
		t.Fatalf("ResolveResearch(explicit model) = %+v, err=%v", researchExplicit, err)
	}
}
