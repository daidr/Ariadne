package models

import (
	"context"
	"encoding/json"
	"github.com/daidr/Ariadne/internal/legacytest"
	"iter"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/sdk"
)

func TestBuiltinProviderMetadataDiscoveryAndTracingMatchLegacy(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, ".codex"), 0700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte("{}"), 0600)
	env := map[string]string{"OPENAI_API_KEY": " first ", "OPENAI_BASE_URL": " https://example.test/v1 ", "DEEPSEEK_API_KEY": "second", "OPENROUTER_API_KEY": "third", "ANTHROPIC_API_KEY": "fourth", "CLAUDE_CODE_OAUTH_TOKEN": "oauth"}
	request := sdk.Request{Model: "model", Effort: "high", Messages: []sdk.Message{{Role: "system", Content: "instructions"}, {Role: "user", Content: "hello"}}, Tools: []sdk.Tool{{Name: "read", Description: "Read", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}}}
	config := map[string]any{"baseUrl": "https://example.test/v1", "apiKey": "key"}
	builtins, err := NewBuiltins(BuiltinOptions{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer builtins.Close(context.Background())
	native := []any{}
	for _, provider := range builtins.Registry.Providers() {
		if provider.Info.Kind == "antigravity" {
			continue // This provider was added after the legacy oracle snapshot.
		}
		found := []sdk.FoundProvider{}
		if provider.Find != nil {
			found = append(found, provider.Find(sdk.FindContext{Env: env, Home: home, Which: func(string) bool { return true }})...)
		}
		describe := provider.Describe(config, request)
		if provider.Info.Kind == "codex" {
			trace, _ := describe.(map[string]any)
			argv, _ := trace["argv"].([]string)
			// Check the flags added since the frozen oracle, then compare its original flags.
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
				argv = slices.Delete(argv, index-1, index+1)
			}
			trace["argv"] = argv
		}
		native = append(native, map[string]any{"info": provider.Info, "found": found, "describe": describe})
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		if os.Getenv("ARIADNE_REQUIRE_LEGACY") == "1" {
			t.Fatal(err)
		}
		if len(native) != 5 {
			t.Fatal("missing builtins")
		}
		return
	}
	data, _ := json.Marshal(map[string]any{"env": env, "home": home, "available": []string{"claude", "codex"}, "settings": config, "request": request})
	command := legacytest.Command(t, bun, "packages/server/src/testing/go-model-host-fixture.ts")
	command.Dir = filepath.Join("..", "..")
	command.Stdin = strings.NewReader(string(data))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("legacy %v\n%s", err, output)
	}
	got, _ := json.Marshal(native)
	var actual, expected any
	if json.Unmarshal(got, &actual) != nil || json.Unmarshal(output, &expected) != nil || !reflect.DeepEqual(actual, expected) {
		t.Fatalf("Go %s\nTS %s", got, output)
	}
}

func TestAntigravityBuiltinDiscoveryAndDefaultCommand(t *testing.T) {
	ctx := t.Context()
	builtins, err := NewBuiltins(BuiltinOptions{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = builtins.Close(context.Background()) })
	provider, ok := builtins.Registry.Get("antigravity")
	if !ok || !provider.Info.Program {
		t.Fatal("Antigravity program provider missing")
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
		t.Fatal("missing CLI check", details, err)
	}
}
func TestRegistryLifecycleAndModelCheck(t *testing.T) {
	registry := New()
	closed := false
	provider := sdk.ModelProvider{Info: sdk.ModelProviderInfo{Kind: "test"}, Stream: func(ctx context.Context, config map[string]any, request sdk.Request) iter.Seq[sdk.Part] {
		return func(yield func(sdk.Part) bool) {
			defer func() { closed = true }()
			if request.Cache == nil || *request.Cache || len(request.Tools) != 0 || request.Messages[0].Content != "Hi" {
				t.Error("wrong check request", request)
			}
			yield(sdk.Part{Type: "alive"})
			if !yield(sdk.Part{Type: "text", Text: "hi"}) {
				return
			}
			t.Error("check continued after first answer")
		}
	}}
	remove, err := registry.Register(provider)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Register(provider); err == nil {
		t.Fatal("duplicate provider registered")
	}
	model := &settings.ResolvedModel{Provider: provider.Info, Model: "model"}
	problem, err := registry.CheckModel(t.Context(), model, "en")
	if err != nil || problem != nil || !closed {
		t.Fatal(problem, err, closed)
	}
	remove()
	remove()
	if len(registry.Providers()) != 0 {
		t.Fatal("removed provider retained")
	}
	provider.Stream = func(context.Context, map[string]any, sdk.Request) iter.Seq[sdk.Part] {
		return func(yield func(sdk.Part) bool) { yield(sdk.Part{Type: "finish", Reason: "error", Error: "refused"}) }
	}
	remove, err = registry.Register(provider)
	if err != nil {
		t.Fatal(err)
	}
	problem, err = registry.CheckModel(t.Context(), model, "en")
	if err != nil || problem == nil || problem.Params["detail"] != "refused" {
		t.Fatal(problem, err)
	}
	remove()
	provider.Check = func(context.Context, map[string]any) (sdk.LocalizedText, error) {
		return sdk.LocalizedText{"zh-CN": "中文错误"}, nil
	}
	remove, err = registry.Register(provider)
	if err != nil {
		t.Fatal(err)
	}
	problem, err = registry.CheckModel(t.Context(), model, "en")
	if err != nil || problem == nil || problem.Params["detail"] != "中文错误" {
		t.Fatal(problem, err)
	}
	remove()
}
func TestBuiltinCloseReleasesProgramsAndRegistry(t *testing.T) {
	builtins, err := NewBuiltins(BuiltinOptions{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = builtins.Close(context.Background()) })
	if len(builtins.Registry.Kinds()) != 6 {
		t.Fatal("missing builtins")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := builtins.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if len(builtins.Registry.Kinds()) != 0 || builtins.bridge.Count() != 0 {
		t.Fatal("closed builtins retained registrations")
	}
	for _, sessions := range builtins.sessions {
		if sessions.Count() != 0 {
			t.Fatal("closed builtins retained sessions")
		}
	}
}

func TestRegistryMetadataOwnershipAndFoundSetupFiltering(t *testing.T) {
	builtins, err := NewBuiltins(BuiltinOptions{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer builtins.Close(context.Background())
	providers := builtins.Registry.Providers()
	providers[0].Info.Title["zh-CN"] = "changed"
	field := providers[0].Info.Settings.Properties["apiKey"]
	field.Secret = false
	providers[0].Info.Settings.Properties["apiKey"] = field
	provider, _ := builtins.Registry.Get("openai-compatible")
	if provider.Info.Title["zh-CN"] == "changed" || !provider.Info.Settings.Properties["apiKey"].Secret {
		t.Fatal("caller changed registered provider metadata")
	}
	store, err := settings.Open(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	context := sdk.FindContext{Env: map[string]string{"OPENAI_API_KEY": "key"}, Home: t.TempDir(), Which: func(string) bool { return false }}
	found := store.FoundSetups(context, builtins.Registry.Providers())
	if len(found) != 1 || found[0].ID != "openai-compatible:OPENAI_API_KEY" {
		t.Fatal(found)
	}
	if _, err := store.UseFound(found[0], "model", nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if found := store.FoundSetups(context, builtins.Registry.Providers()); len(found) != 0 {
		t.Fatal("already used provider offered again", found)
	}
	panicProvider := sdk.ModelProvider{Info: sdk.ModelProviderInfo{Kind: "broken"}, Find: func(sdk.FindContext) []sdk.FoundProvider { panic("broken plugin") }}
	if found := store.FoundSetups(context, []sdk.ModelProvider{panicProvider}); len(found) != 0 {
		t.Fatal(found)
	}
}
