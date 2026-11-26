package acp

import (
	"context"

	"github.com/daidr/Ariadne/internal/agentruntime"
	"github.com/daidr/Ariadne/internal/process"
	"github.com/daidr/Ariadne/sdk"
)

const PluginID = "model-acp"
const ProviderKind = "acp"

func ModelProvider(sessions *agentruntime.Sessions, dataDir string) sdk.ModelProvider {
	return sdk.ModelProvider{
		Info: sdk.ModelProviderInfo{
			Kind:         ProviderKind,
			Title:        agentruntime.Localized("ACP agent", "ACP agent"),
			Program:      true,
			Capabilities: agentruntime.ProgramCapabilities(),
			Settings: agentruntime.CommandSchema(
				"",
				agentruntime.Localized("启动它的 ACP 模式的命令", "Starts the agent in its ACP mode"),
				true,
			),
		},
		ValidateSettings: agentruntime.ValidateCommandSettings(true),
		Stream:           sessions.Stream,
		Forget:           sessions.Forget,
		Describe: func(settings map[string]any, request sdk.Request) any {
			tools := agentruntime.ToolNames(request.Tools)
			command := agentruntime.StringSetting(settings, "command")
			servers := []string{}
			if len(tools) > 0 {
				servers = append(servers, agentruntime.BridgeServerName)
			}
			session := map[string]any{"model": request.Model, "mcpServers": servers}
			return map[string]any{
				"argv":     process.SplitCommand(command),
				"session":  session,
				"tools":    tools,
				"messages": len(request.Messages),
			}
		},
		Models: func(ctx context.Context, settings map[string]any, _ sdk.ModelListOptions) ([]sdk.ListedModel, error) {
			command := agentruntime.StringSetting(settings, "command")
			env := agentruntime.EnvSetting(settings)
			listed, err := agentruntime.WithModelTempDir(dataDir, func(directory string) ([]sdk.ListedModel, error) {
				return ListModels(ctx, Settings{Command: command, Env: env}, directory)
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
			return agentruntime.CheckCommandWithEnv(agentruntime.StringSetting(settings, "command"), agentruntime.EnvSetting(settings))
		},
	}
}

func Plugin(opts agentruntime.PluginOptions) sdk.Plugin {
	return sdk.Plugin{
		ID:       PluginID,
		Name:     agentruntime.Localized("ACP agent", "ACP agent"),
		Provides: sdk.PluginProvides{Models: []string{ProviderKind}},
		Setup: func(_ context.Context, pc sdk.PluginContext) (func(context.Context) error, error) {
			dataDir := agentruntime.ResolveDataDir(opts, pc.DataDir)
			sessions, err := agentruntime.NewSessions(Kind(opts.Bridge, opts.StdioLauncher), agentruntime.SessionOptions{
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
