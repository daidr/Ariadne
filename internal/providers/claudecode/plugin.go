package claudecode

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/daidr/Ariadne/internal/agentruntime"
	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/sdk"
)

const PluginID = "model-claude-code"
const ProviderKind = "claude-code"
const DefaultCommand = "claude"

func commandFor(settings map[string]any) string {
	command := agentruntime.StringSetting(settings, "command")
	if textutil.Trim(command) == "" {
		return DefaultCommand
	}
	return command
}

func findProviders(ctx sdk.FindContext) []sdk.FoundProvider {
	which := ctx.Which
	if which == nil {
		which = func(program string) bool { _, err := exec.LookPath(program); return err == nil }
	}
	if !which(DefaultCommand) {
		return nil
	}
	signedIn := ctx.Env["CLAUDE_CODE_OAUTH_TOKEN"] != ""
	if !signedIn {
		configDir := textutil.Trim(ctx.Env["CLAUDE_CONFIG_DIR"])
		var candidates []string
		if configDir != "" {
			candidates = []string{
				filepath.Join(configDir, ".credentials.json"),
				filepath.Join(configDir, ".claude.json"),
			}
		} else {
			defaultDir := filepath.Join(ctx.Home, ".claude")
			candidates = []string{
				filepath.Join(defaultDir, ".credentials.json"),
				filepath.Join(ctx.Home, ".claude.json"),
				filepath.Join(defaultDir, ".claude.json"),
			}
		}
		for _, path := range candidates {
			if _, err := os.Stat(path); err == nil {
				signedIn = true
				break
			}
		}
	}
	if signedIn {
		return []sdk.FoundProvider{{
			ID:       ProviderKind,
			From:     map[string]string{"program": DefaultCommand},
			Settings: map[string]any{},
		}}
	}
	return nil
}

func ModelProvider(sessions *agentruntime.Sessions, dataDir string) sdk.ModelProvider {
	return sdk.ModelProvider{
		Info: sdk.ModelProviderInfo{
			Kind:         ProviderKind,
			Title:        agentruntime.Localized("Claude Code", "Claude Code"),
			Program:      true,
			Capabilities: agentruntime.ProgramCapabilities(),
			Settings: agentruntime.CommandSchema(
				DefaultCommand,
				agentruntime.Localized("用它已登录的账号", "Uses the account it is signed in with"),
				false,
			),
		},
		ValidateSettings: agentruntime.ValidateCommandSettings(false),
		Stream:           sessions.Stream,
		Forget:           sessions.Forget,
		Describe: func(settings map[string]any, request sdk.Request) any {
			tools := agentruntime.ToolNames(request.Tools)
			command := agentruntime.StringSetting(settings, "command")
			argv := append(Argv(Settings{Command: command}, request.Model, request.Effort), "--system-prompt-file", "<system prompt>")
			if len(tools) > 0 {
				argv = append(argv, "--mcp-config", "<tool server>")
			}
			return map[string]any{"argv": argv, "tools": tools, "messages": len(request.Messages)}
		},
		Models: func(ctx context.Context, settings map[string]any, _ sdk.ModelListOptions) ([]sdk.ListedModel, error) {
			command := agentruntime.StringSetting(settings, "command")
			listed, err := agentruntime.WithModelTempDir(dataDir, func(directory string) ([]sdk.ListedModel, error) {
				return ListModels(ctx, Settings{Command: command}, directory)
			})
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if err == nil && len(listed) > 0 {
				return agentruntime.EnsureDefaultModel(listed), nil
			}
			return []sdk.ListedModel{{ID: "default"}, {ID: "sonnet"}, {ID: "opus"}, {ID: "haiku"}}, nil
		},
		Check: func(_ context.Context, settings map[string]any) (sdk.LocalizedText, error) {
			return agentruntime.CheckCommand(commandFor(settings))
		},
		Find: findProviders,
	}
}

func Plugin(opts agentruntime.PluginOptions) sdk.Plugin {
	return sdk.Plugin{
		ID:       PluginID,
		Name:     agentruntime.Localized("Claude Code", "Claude Code"),
		Provides: sdk.PluginProvides{Models: []string{ProviderKind}},
		Setup: func(_ context.Context, pc sdk.PluginContext) (func(context.Context) error, error) {
			dataDir := agentruntime.ResolveDataDir(opts, pc.DataDir)
			sessions, err := agentruntime.NewSessions(Kind(opts.Bridge), agentruntime.SessionOptions{
				DataDir: dataDir,
				Context: pc.Context,
				Now:     opts.Now,
			})
			if err != nil {
				return nil, err
			}
			// Register session cleanup before model registration so reverse-order
			// disposal unregisters the model first, then closes active sessions.
			if err := pc.OnDispose(sessions.Close); err != nil {
				_ = sessions.Close(context.Background())
				return nil, err
			}
			if _, err := pc.RegisterModel(ModelProvider(sessions, dataDir)); err != nil {
				return nil, err
			}
			return nil, nil
		},
	}
}
