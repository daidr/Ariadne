package providers_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/agentruntime"
	"github.com/daidr/Ariadne/internal/dashboard"
	"github.com/daidr/Ariadne/internal/links"
	"github.com/daidr/Ariadne/internal/models"
	"github.com/daidr/Ariadne/internal/plugins"
	"github.com/daidr/Ariadne/internal/providers"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/internal/sources"
	"github.com/daidr/Ariadne/sdk"
)

func loadBuiltinModelHost(t *testing.T) (*plugins.Host, *models.Registry, *agentruntime.ToolBridge) {
	t.Helper()
	dir := t.TempDir()
	reg := models.New()
	sourceReg := sources.New()
	dashReg := dashboard.New()
	t.Cleanup(func() { _ = dashReg.Close(context.Background()) })
	linkReg := links.New(links.Options{})
	t.Cleanup(func() { _ = linkReg.Close() })
	bridge := agentruntime.NewToolBridge()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = bridge.Close(ctx)
	})
	host, err := plugins.New(plugins.Options{
		DataRoot:    filepath.Join(dir, "plugins"),
		ManagedRoot: dir,
		Models:      reg,
		Sources:     sourceReg,
		Dashboards:  dashReg,
		Links:       linkReg,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = host.Close(ctx)
	})
	for _, plugin := range providers.BuiltinPlugins(providers.BuiltinOptions{
		Bridge:       bridge,
		AgentDataDir: filepath.Join(dir, "plugins", "agents"),
	}) {
		if err := host.Load(t.Context(), plugin, nil); err != nil {
			t.Fatalf("load %s: %v", plugin.ID, err)
		}
	}
	return host, reg, bridge
}

func TestBuiltinProviderMetadataDiscoveryAndTracing(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, ".codex"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"OPENAI_API_KEY":          " first ",
		"OPENAI_BASE_URL":         " https://example.test/v1 ",
		"DEEPSEEK_API_KEY":        "second",
		"OPENROUTER_API_KEY":      "third",
		"ANTHROPIC_API_KEY":       "fourth",
		"CLAUDE_CODE_OAUTH_TOKEN": "oauth",
	}
	request := sdk.Request{
		Model:    "model",
		Effort:   "high",
		Messages: []sdk.Message{{Role: "system", Content: "instructions"}, {Role: "user", Content: "hello"}},
		Tools:    []sdk.Tool{{Name: "read", Description: "Read", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}},
	}
	config := map[string]any{"baseUrl": "https://example.test/v1", "apiKey": "key"}
	_, reg, _ := loadBuiltinModelHost(t)

	native := []any{}
	for _, provider := range reg.Providers() {
		found := []sdk.FoundProvider{}
		if provider.Find != nil {
			found = append(found, provider.Find(sdk.FindContext{Env: env, Home: home, Which: func(string) bool { return true }})...)
		}
		describe := provider.Describe(config, request)
		if provider.Info.Kind == "codex" {
			trace, _ := describe.(map[string]any)
			argv, _ := trace["argv"].([]string)
			for _, setting := range []string{
				"features.hooks=false", "tools.experimental_request_user_input.enabled=false",
				"tools.update_plan.enabled=false", "features.multi_agent_v2=false",
				"features.code_mode=false", "features.code_mode_only=false", "features.deferred_executor=false",
				"features.request_permissions_tool=false", "features.send_message_to_user_async=false",
				"features.token_budget=false", "features.current_time_reminder=false",
			} {
				index := slices.Index(argv, setting)
				if index < 1 || argv[index-1] != "-c" {
					t.Fatalf("Codex launch missing %s: %v", setting, argv)
				}
			}
		}
		native = append(native, map[string]any{"info": provider.Info, "found": found, "describe": describe})
	}
	if len(native) != 6 {
		t.Fatalf("expected 6 built-in model providers, got %d", len(native))
	}
}

func TestAntigravityBuiltinDiscoveryAndDefaultCommand(t *testing.T) {
	ctx := t.Context()
	_, reg, _ := loadBuiltinModelHost(t)
	provider, ok := reg.Get("antigravity")
	if !ok || !provider.Info.Program || !provider.Info.HasStatefulSessions() || !provider.Info.PreservesModelOrder() || !provider.Info.PrefersDefaultModel() {
		t.Fatalf("Antigravity program provider missing or capabilities incomplete: %+v", provider.Info)
	}
	command := provider.Info.Settings.Properties["command"].Default
	if command != "agy acp --process-per-session --disable-telemetry" || len(provider.Info.Settings.Required) != 0 {
		t.Fatal("unexpected Antigravity default command", command)
	}
	request := sdk.Request{Model: "fixture", Effort: "high"}
	trace := provider.Describe(nil, request).(map[string]any)
	if !slices.Equal(trace["argv"].([]string), strings.Fields(command.(string))) {
		t.Fatal("the preset and runtime command diverged", trace)
	}
	override := provider.Describe(map[string]any{"command": "my-agy acp"}, request).(map[string]any)
	if !slices.Equal(override["argv"].([]string), []string{"my-agy", "acp"}) {
		t.Fatal("the command override was ignored", override)
	}
	found := provider.Find(sdk.FindContext{Which: func(command string) bool { return command == "agy" }})
	if len(found) != 1 || found[0].From["program"] != "agy" {
		t.Fatal("installed CLI was not discovered", found)
	}
	if found := provider.Find(sdk.FindContext{Which: func(string) bool { return false }}); len(found) > 0 {
		t.Fatal("missing CLI was offered", found)
	}
	if details, err := provider.Check(ctx, map[string]any{"command": "missing-agy-fixture acp"}); err != nil || details["en"] != "command not found: missing-agy-fixture" {
		t.Fatal("missing command check failed", details, err)
	}
}

func TestAgentCLIValidateSettingsAndForgetContract(t *testing.T) {
	_, reg, _ := loadBuiltinModelHost(t)
	for _, kind := range []string{"claude-code", "codex", "acp", "antigravity"} {
		provider, ok := reg.Get(kind)
		if !ok {
			t.Fatalf("missing provider %s", kind)
		}
		if !provider.Info.HasStatefulSessions() || !provider.Info.PreservesModelOrder() || !provider.Info.PrefersDefaultModel() {
			t.Fatalf("provider %s missing expected capabilities: %+v", kind, provider.Info)
		}
		if provider.Forget == nil {
			t.Fatalf("provider %s missing Forget hook", kind)
		}
		if provider.ValidateSettings == nil {
			t.Fatalf("provider %s missing ValidateSettings hook", kind)
		}
		if err := reg.ValidateSettings(kind, map[string]any{"command": "bad\ncommand"}); err == nil {
			t.Fatalf("provider %s accepted newline in command", kind)
		}
		if kind == "acp" {
			if err := reg.ValidateSettings(kind, map[string]any{}); err == nil {
				t.Fatal("acp accepted missing required command")
			}
			if err := reg.ValidateSettings(kind, map[string]any{"command": "   "}); err == nil {
				t.Fatal("acp accepted blank required command")
			}
		} else {
			if err := reg.ValidateSettings(kind, map[string]any{}); err != nil {
				t.Fatalf("provider %s rejected empty optional command: %v", kind, err)
			}
		}
		if err := reg.ValidateSettings(kind, map[string]any{"command": "my-agent --acp"}); err != nil {
			t.Fatalf("provider %s rejected valid command: %v", kind, err)
		}
	}
	if err := reg.Forget(t.Context(), "conv_nonexistent"); err != nil {
		t.Fatalf("Registry.Forget returned unexpected error: %v", err)
	}

	// Verify Check resolves each provider's default command when settings["command"] is omitted.
	t.Setenv("PATH", "")
	for kind, wantProgram := range map[string]string{
		"claude-code": "claude",
		"codex":       "codex",
		"antigravity": "agy",
	} {
		provider, _ := reg.Get(kind)
		details, err := provider.Check(t.Context(), map[string]any{})
		if err != nil {
			t.Fatalf("%s Check error: %v", kind, err)
		}
		if details["en"] != "command not found: "+wantProgram {
			t.Fatalf("%s Check(empty settings) = %v, want command not found: %s", kind, details, wantProgram)
		}
	}
}

func TestBuiltinHostCloseReleasesProgramsAndRegistry(t *testing.T) {
	host, reg, bridge := loadBuiltinModelHost(t)
	if len(reg.Kinds()) != 6 {
		t.Fatalf("expected 6 built-in model kinds, got %d", len(reg.Kinds()))
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := host.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if len(reg.Kinds()) != 0 || bridge.Count() != 0 {
		t.Fatalf("closed host retained registrations: kinds=%d bridge=%d", len(reg.Kinds()), bridge.Count())
	}
}

func TestRegistryMetadataOwnershipAndFoundSetupFiltering(t *testing.T) {
	_, reg, _ := loadBuiltinModelHost(t)
	listed := reg.Providers()
	openAIIndex := -1
	for i, p := range listed {
		if p.Info.Kind == "openai-compatible" {
			openAIIndex = i
			break
		}
	}
	if openAIIndex < 0 {
		t.Fatal("missing openai-compatible provider")
	}
	listed[openAIIndex].Info.Title["zh-CN"] = "changed"
	field := listed[openAIIndex].Info.Settings.Properties["apiKey"]
	field.Secret = false
	listed[openAIIndex].Info.Settings.Properties["apiKey"] = field
	provider, _ := reg.Get("openai-compatible")
	if provider.Info.Title["zh-CN"] == "changed" || !provider.Info.Settings.Properties["apiKey"].Secret {
		t.Fatal("caller changed registered provider metadata")
	}
	store, err := settings.Open(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	findCtx := sdk.FindContext{Env: map[string]string{"OPENAI_API_KEY": "key"}, Home: t.TempDir(), Which: func(string) bool { return false }}
	found := store.FoundSetups(findCtx, reg.Providers())
	if len(found) != 1 || found[0].ID != "openai-compatible:OPENAI_API_KEY" {
		t.Fatal(found)
	}
	if _, err := store.UseFound(found[0], "model", nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if found := store.FoundSetups(findCtx, reg.Providers()); len(found) != 0 {
		t.Fatal("already used provider offered again", found)
	}
	panicProvider := sdk.ModelProvider{Info: sdk.ModelProviderInfo{Kind: "broken"}, Find: func(sdk.FindContext) []sdk.FoundProvider { panic("broken plugin") }}
	if found := store.FoundSetups(findCtx, []sdk.ModelProvider{panicProvider}); len(found) != 0 {
		t.Fatal(found)
	}
}
