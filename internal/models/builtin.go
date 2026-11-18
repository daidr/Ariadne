package models

import (
	"context"
	"errors"
	"iter"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/daidr/Ariadne/internal/fsutil"
	"github.com/daidr/Ariadne/internal/process"
	"github.com/daidr/Ariadne/internal/providers/agents"
	"github.com/daidr/Ariadne/internal/providers/anthropic"
	"github.com/daidr/Ariadne/internal/providers/openai"
	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/sdk"
)

type BuiltinOptions struct {
	DataDir       string
	Context       context.Context
	Client        *http.Client
	Now           func() time.Time
	StdioLauncher agents.StdioLauncher
}
type Builtins struct {
	Registry  *Registry
	bridge    *agents.ToolBridge
	sessions  []*agents.Sessions
	remove    []func()
	closeOnce sync.Once
	closeDone chan struct{}
}

func localized(zh, en string) sdk.LocalizedText { return sdk.LocalizedText{"zh-CN": zh, "en": en} }
func httpInfo(kind, zh, en, address string, requiresKey bool) sdk.ModelProviderInfo {
	properties := map[string]sdk.SettingField{"baseUrl": {Type: "string", Title: localized("接口地址", "Base URL"), Default: address}, "apiKey": {Type: "string", Title: localized("API Key", "API key"), Secret: true}}
	required := []string{"baseUrl"}
	if requiresKey {
		required = append(required, "apiKey")
	} else {
		field := properties["baseUrl"]
		field.Description = localized("以 /v1 结尾的地址", "Usually ends with /v1")
		properties["baseUrl"] = field
	}
	return sdk.ModelProviderInfo{Kind: kind, Title: localized(zh, en), Settings: sdk.SettingsSchema{Type: "object", Properties: properties, Required: required, FieldOrder: []string{"baseUrl", "apiKey"}}}
}
func stringSetting(settings map[string]any, key string) string {
	value, _ := settings[key].(string)
	return value
}
func NewBuiltins(options BuiltinOptions) (*Builtins, error) {
	if options.DataDir == "" {
		return nil, errors.New("builtin model providers require a data directory")
	}
	registry := New()
	builtins := &Builtins{Registry: registry, bridge: agents.NewToolBridge(), closeDone: make(chan struct{})}
	add := func(provider sdk.ModelProvider) error {
		remove, err := registry.Register(provider)
		if err == nil {
			builtins.remove = append(builtins.remove, remove)
		}
		return err
	}
	openAI := openai.Provider{Client: options.Client, Now: options.Now}
	asOpenAI := func(settings map[string]any) openai.Settings {
		return openai.Settings{BaseURL: stringSetting(settings, "baseUrl"), APIKey: stringSetting(settings, "apiKey")}
	}
	if err := add(sdk.ModelProvider{
		Info: httpInfo("openai-compatible", "OpenAI 兼容接口", "OpenAI-compatible API", "https://api.openai.com/v1", false),
		Stream: func(ctx context.Context, settings map[string]any, request sdk.Request) iter.Seq[sdk.Part] {
			return openAI.Stream(ctx, asOpenAI(settings), request)
		},
		Models: func(ctx context.Context, settings map[string]any, options sdk.ModelListOptions) ([]sdk.ListedModel, error) {
			names, err := openAI.Models(ctx, asOpenAI(settings), options.Headers)
			listed := []sdk.ListedModel{}
			for _, name := range names {
				listed = append(listed, sdk.ListedModel{ID: name})
			}
			return listed, err
		},
		Describe: func(settings map[string]any, request sdk.Request) any {
			return openai.RequestBody(asOpenAI(settings), request)
		},
		SearchAvailable: func(settings map[string]any) bool { return openai.SearchAvailable(asOpenAI(settings)) },
		Search: func(ctx context.Context, settings map[string]any, request sdk.SearchRequest) (sdk.SearchAnswer, error) {
			return openAI.Search(ctx, asOpenAI(settings), request)
		}, Find: findOpenAI,
	}); err != nil {
		return nil, err
	}
	anthropicAPI := anthropic.Provider{Client: options.Client, Now: options.Now}
	asAnthropic := func(settings map[string]any) anthropic.Settings {
		return anthropic.Settings{BaseURL: stringSetting(settings, "baseUrl"), APIKey: stringSetting(settings, "apiKey")}
	}
	if err := add(sdk.ModelProvider{Info: httpInfo("anthropic", "Anthropic 接口", "Anthropic API", "https://api.anthropic.com", true),
		Stream: func(ctx context.Context, settings map[string]any, request sdk.Request) iter.Seq[sdk.Part] {
			return anthropicAPI.Stream(ctx, asAnthropic(settings), request)
		},
		Models: func(ctx context.Context, settings map[string]any, options sdk.ModelListOptions) ([]sdk.ListedModel, error) {
			return anthropicAPI.Models(ctx, asAnthropic(settings), options.Headers)
		},
		Describe: func(settings map[string]any, request sdk.Request) any {
			return anthropic.RequestBody(asAnthropic(settings), request)
		},
		SearchAvailable: func(settings map[string]any) bool { return anthropic.SearchAvailable(asAnthropic(settings)) },
		Search: func(ctx context.Context, settings map[string]any, request sdk.SearchRequest) (sdk.SearchAnswer, error) {
			return anthropicAPI.Search(ctx, asAnthropic(settings), request)
		},
		Find: func(context sdk.FindContext) []sdk.FoundProvider {
			found := anthropic.Find(context.Env)
			if found == nil {
				return nil
			}
			return []sdk.FoundProvider{{ID: "ANTHROPIC_API_KEY", From: map[string]string{"env": "ANTHROPIC_API_KEY"}, Settings: found["settings"].(map[string]any)}}
		},
	}); err != nil {
		return nil, err
	}
	for _, kind := range []agents.Kind{agents.ClaudeKind(builtins.bridge), agents.CodexKind(), agents.ACPKind(builtins.bridge, options.StdioLauncher)} {
		sessions, err := agents.NewSessions(kind, agents.SessionOptions{DataDir: filepath.Join(options.DataDir, "agents"), Context: options.Context, Now: options.Now})
		if err != nil {
			_ = builtins.Close(context.Background())
			return nil, err
		}
		builtins.sessions = append(builtins.sessions, sessions)
		provider := agentProvider(kind.Name, sessions, options.DataDir)
		if err := add(provider); err != nil {
			_ = builtins.Close(context.Background())
			return nil, err
		}
	}
	return builtins, nil
}
func (builtins *Builtins) Close(ctx context.Context) error {
	builtins.closeOnce.Do(func() {
		for _, remove := range builtins.remove {
			remove()
		}
		go func() {
			defer close(builtins.closeDone)
			var group sync.WaitGroup
			for _, session := range builtins.sessions {
				group.Add(1)
				go func() { defer group.Done(); _ = session.Close(context.Background()) }()
			}
			group.Wait()
			_ = builtins.bridge.Close(context.Background())
		}()
	})
	select {
	case <-builtins.closeDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (builtins *Builtins) Forget(ctx context.Context, conversation string) error {
	var combined error
	for _, sessions := range builtins.sessions {
		combined = errors.Join(combined, sessions.Forget(ctx, conversation))
	}
	return combined
}
func agentProvider(kind string, sessions *agents.Sessions, dataDir string) sdk.ModelProvider {
	title, program := "ACP agent", ""
	if kind == "claude-code" {
		title, program = "Claude Code", "claude"
	}
	if kind == "codex" {
		title, program = "Codex", "codex"
	}
	field := sdk.SettingField{Type: "string", Title: localized("命令", "Command")}
	required := []string(nil)
	if program != "" {
		field.Default = program
		field.Description = localized("用它已登录的账号", "Uses the account it is signed in with")
	} else {
		field.Description = localized("启动它的 ACP 模式的命令", "Starts the agent in its ACP mode")
		required = []string{"command"}
	}
	info := sdk.ModelProviderInfo{Kind: kind, Title: localized(title, title), Program: true, Settings: sdk.SettingsSchema{Type: "object", Properties: map[string]sdk.SettingField{"command": field}, Required: required}}
	return sdk.ModelProvider{Info: info, Stream: sessions.Stream,
		Describe: func(settings map[string]any, request sdk.Request) any {
			tools := []string{}
			for _, tool := range request.Tools {
				tools = append(tools, tool.Name)
			}
			command := stringSetting(settings, "command")
			switch kind {
			case "codex":
				thread := map[string]any{"model": request.Model, "baseInstructions": "<system prompt>", "dynamicTools": tools}
				if request.Effort != "" {
					thread["effort"] = request.Effort
				}
				return map[string]any{"argv": agents.CodexArgv(agents.CodexSettings{Command: command}), "thread": thread, "messages": len(request.Messages)}
			case "claude-code":
				argv := append(agents.ClaudeArgv(agents.ClaudeSettings{Command: command}, request.Model, request.Effort), "--system-prompt-file", "<system prompt>")
				if len(tools) > 0 {
					argv = append(argv, "--mcp-config", "<tool server>")
				}
				return map[string]any{"argv": argv, "tools": tools, "messages": len(request.Messages)}
			default:
				servers := []string{}
				if len(tools) > 0 {
					servers = append(servers, agents.BridgeServerName)
				}
				return map[string]any{"argv": process.SplitCommand(command), "session": map[string]any{"model": request.Model, "mcpServers": servers}, "tools": tools, "messages": len(request.Messages)}
			}
		},
		Models: func(ctx context.Context, settings map[string]any, options sdk.ModelListOptions) ([]sdk.ListedModel, error) {
			root := filepath.Join(dataDir, "agents", "sessions")
			if err := os.MkdirAll(root, 0700); err != nil {
				return nil, err
			}
			directory, err := os.MkdirTemp(root, "models-")
			if err != nil {
				return nil, err
			}
			defer func() {
				cleanup, cancel := context.WithTimeout(context.Background(), 4*time.Second)
				defer cancel()
				_ = fsutil.RemoveTree(cleanup, root, directory)
			}()
			command := stringSetting(settings, "command")
			var listed []sdk.ListedModel
			switch kind {
			case "codex":
				listed, err = agents.ListCodexModels(ctx, agents.CodexSettings{Command: command}, directory)
			case "claude-code":
				listed, err = agents.ListClaudeModels(ctx, agents.ClaudeSettings{Command: command}, directory)
			default:
				listed, err = agents.ListACPModels(ctx, agents.ACPSettings{Command: command}, directory)
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if err == nil && len(listed) > 0 {
				hasDefault := false
				for _, model := range listed {
					hasDefault = hasDefault || model.ID == "default"
				}
				if !hasDefault {
					listed = append([]sdk.ListedModel{{ID: "default"}}, listed...)
				}
				return listed, nil
			}
			fixed := []sdk.ListedModel{{ID: "default"}}
			if kind == "claude-code" {
				for _, name := range []string{"sonnet", "opus", "haiku"} {
					fixed = append(fixed, sdk.ListedModel{ID: name})
				}
			}
			return fixed, nil
		},
		Check: func(ctx context.Context, settings map[string]any) (sdk.LocalizedText, error) {
			words := process.SplitCommand(stringSetting(settings, "command"))
			if len(words) == 0 {
				return nil, nil
			}
			if _, err := exec.LookPath(words[0]); err != nil {
				return localized("找不到命令 "+words[0], "command not found: "+words[0]), nil
			}
			return nil, nil
		},
		Find: func(context sdk.FindContext) []sdk.FoundProvider {
			which := context.Which
			if which == nil {
				which = func(program string) bool { _, err := exec.LookPath(program); return err == nil }
			}
			signedIn := false
			if kind == "codex" && which("codex") {
				home := textutil.Trim(context.Env["CODEX_HOME"])
				if home == "" {
					home = filepath.Join(context.Home, ".codex")
				}
				_, err := os.Stat(filepath.Join(home, "auth.json"))
				signedIn = err == nil
			}
			if kind == "claude-code" && which("claude") {
				home := textutil.Trim(context.Env["CLAUDE_CONFIG_DIR"])
				if home == "" {
					home = filepath.Join(context.Home, ".claude")
				}
				_, credentials := os.Stat(filepath.Join(home, ".credentials.json"))
				_, config := os.Stat(filepath.Join(context.Home, ".claude.json"))
				signedIn = context.Env["CLAUDE_CODE_OAUTH_TOKEN"] != "" || credentials == nil || config == nil
			}
			if signedIn {
				return []sdk.FoundProvider{{ID: kind, From: map[string]string{"program": program}, Settings: map[string]any{}}}
			}
			return nil
		},
	}
}
func findOpenAI(context sdk.FindContext) []sdk.FoundProvider {
	result := []sdk.FoundProvider{}
	for _, service := range []struct{ key, name, address, base string }{{"OPENAI_API_KEY", "OpenAI", "OPENAI_BASE_URL", "https://api.openai.com/v1"}, {"DEEPSEEK_API_KEY", "DeepSeek", "", "https://api.deepseek.com/v1"}, {"OPENROUTER_API_KEY", "OpenRouter", "", "https://openrouter.ai/api/v1"}} {
		key := textutil.Trim(context.Env[service.key])
		if key == "" {
			continue
		}
		base := textutil.Trim(context.Env[service.address])
		if base == "" {
			base = service.base
		}
		result = append(result, sdk.FoundProvider{ID: service.key, Name: service.name, From: map[string]string{"env": service.key}, Settings: map[string]any{"baseUrl": base, "apiKey": key}})
	}
	return result
}
