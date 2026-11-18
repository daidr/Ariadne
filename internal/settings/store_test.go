package settings

import (
	"encoding/json"
	"errors"
	"github.com/daidr/Ariadne/internal/legacytest"
	"os"
	"os/exec"
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
func TestSettingsSearchKeysAndSourceSplit(t *testing.T) {
	store := newStore(t)
	if _, err := store.UpdateSearch(jsonObject(`{"provider":"deepseek","services":{"deepseek":{"apiKey":" secret ","baseUrl":"https://example.test/v1/tools/"}}}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateSearch(jsonObject(`{"services":{"deepseek":{"apiKey":""}}}`)); err != nil {
		t.Fatal(err)
	}
	public, _ := json.Marshal(store.PublicSearch(nil, nil))
	if strings.Contains(string(public), "secret") || !strings.Contains(string(public), `"keySet":true`) {
		t.Fatal(string(public))
	}
	draft, err := store.SearchDraft(jsonObject(`{"services":{"deepseek":{"apiKey":null}}}`))
	if err != nil || text(object(object(draft["services"])["deepseek"])["apiKey"]) != "" {
		t.Fatal(draft, err)
	}
	if text(object(object(object(store.Current()["search"])["services"])["deepseek"])["apiKey"]) != "secret" {
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

func TestSettingsStoreMatchesLegacy(t *testing.T) {
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
					resolved, err = store.ResolveModel(text(value["role"]), text(value["given"]), testModelKinds())
				}
				result = stableResolved(resolved)
			case "choices":
				result = store.ModelChoices()
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
				_, err = store.UpdateSearch(action.Value)
				result = store.PublicSearch(nil, nil)
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
	bun, err := exec.LookPath("bun")
	if err != nil {
		if os.Getenv("ARIADNE_REQUIRE_LEGACY") == "1" {
			t.Fatal(err)
		}
		t.Log("native checks passed; Bun comparison unavailable")
		return
	}
	data, _ := json.Marshal(cases)
	command := legacytest.Command(t, bun, "packages/server/src/testing/go-settings-store-fixture.ts")
	command.Dir = filepath.Join("..", "..")
	command.Stdin = strings.NewReader(string(data))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("legacy %v\n%s", err, output)
	}
	var expected any
	if err := json.Unmarshal(output, &expected); err != nil {
		t.Fatalf("%v\n%s", err, output)
	}
	equalJSON(t, native, expected)
}

func stableResolved(model *ResolvedModel) any {
	if model == nil {
		return nil
	}
	data, _ := json.Marshal(model)
	var value Object
	json.Unmarshal(data, &value)
	value["provider"] = model.Provider.Kind
	if model.Fallback != nil {
		value["fallback"] = stableResolved(model.Fallback)
	}
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
	resolved, err := store.ResolveModel("thread", "", kinds)
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
