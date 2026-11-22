package codex

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/daidr/Ariadne/internal/agentruntime"
	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/sdk"
)

const PluginID = "model-codex"
const ProviderKind = "codex"
const DefaultCommand = "codex"

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
	home := textutil.Trim(ctx.Env["CODEX_HOME"])
	if home == "" {
		home = filepath.Join(ctx.Home, ".codex")
	}
	if _, err := os.Stat(filepath.Join(home, "auth.json")); err == nil {
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
			Title:        agentruntime.Localized("Codex", "Codex"),
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
			thread := map[string]any{
				"model":            request.Model,
				"baseInstructions": "<system prompt>",
				"dynamicTools":     tools,
			}
			if request.Effort != "" {
				thread["effort"] = request.Effort
			}
			return map[string]any{
				"argv":     Argv(Settings{Command: command}),
				"thread":   thread,
				"messages": len(request.Messages),
			}
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
			return []sdk.ListedModel{{ID: "default"}}, nil
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
		Name:     agentruntime.Localized("Codex", "Codex"),
		Provides: sdk.PluginProvides{Models: []string{ProviderKind}},
		Setup: func(_ context.Context, pc sdk.PluginContext) (func(context.Context) error, error) {
			dataDir := agentruntime.ResolveDataDir(opts, pc.DataDir)
			sessions, err := agentruntime.NewSessions(Kind(), agentruntime.SessionOptions{
				DataDir: dataDir,
				Context: pc.Context,
				Now:     opts.Now,
			})
			if err != nil {
				return nil, err
			}
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
