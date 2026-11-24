package antigravity

import (
	"context"
	"os/exec"

	"github.com/daidr/Ariadne/internal/agentruntime"
	"github.com/daidr/Ariadne/internal/process"
	"github.com/daidr/Ariadne/internal/providers/acp"
	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/sdk"
)

const PluginID = "model-antigravity"
const ProviderKind = "antigravity"
const programName = "agy"

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
	if !which(programName) {
		return nil
	}
	return []sdk.FoundProvider{{
		ID:       ProviderKind,
		From:     map[string]string{"program": programName},
		Settings: map[string]any{},
	}}
}

func ModelProvider(sessions *agentruntime.Sessions, dataDir string) sdk.ModelProvider {
	return sdk.ModelProvider{
		Info: sdk.ModelProviderInfo{
			Kind:         ProviderKind,
			Title:        agentruntime.Localized("Antigravity CLI", "Antigravity CLI"),
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
			command := commandFor(settings)
			servers := []string{}
			if len(tools) > 0 {
				servers = append(servers, agentruntime.BridgeServerName)
			}
			session := map[string]any{"model": request.Model, "mcpServers": servers}
			if request.Effort != "" {
				session["thoughtLevel"] = request.Effort
			}
			return map[string]any{
				"argv":     process.SplitCommand(command),
				"session":  session,
				"tools":    tools,
				"messages": len(request.Messages),
			}
		},
		Models: func(ctx context.Context, settings map[string]any, _ sdk.ModelListOptions) ([]sdk.ListedModel, error) {
			command := commandFor(settings)
			listed, err := agentruntime.WithModelTempDir(dataDir, func(directory string) ([]sdk.ListedModel, error) {
				return acp.ListModelsWithOptions(ctx, acp.Settings{Command: command}, directory, acp.ListOptions{ProbeThoughtLevels: true})
			})
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if err != nil {
				return nil, err
			}
			if len(listed) > 0 {
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
		Name:     agentruntime.Localized("Antigravity CLI", "Antigravity CLI"),
		Provides: sdk.PluginProvides{Models: []string{ProviderKind}},
		Setup: func(_ context.Context, pc sdk.PluginContext) (func(context.Context) error, error) {
			dataDir := agentruntime.ResolveDataDir(opts, pc.DataDir)
			sessions, err := agentruntime.NewSessions(Kind(opts.Bridge, opts.StdioLauncher), agentruntime.SessionOptions{
				DataDir:      dataDir,
				Context:      pc.Context,
				Now:          opts.Now,
				DisableQuiet: true,
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
