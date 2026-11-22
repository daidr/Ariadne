package providers

import (
	"net/http"
	"time"

	"github.com/daidr/Ariadne/internal/agentruntime"
	"github.com/daidr/Ariadne/internal/providers/acp"
	"github.com/daidr/Ariadne/internal/providers/anthropic"
	"github.com/daidr/Ariadne/internal/providers/antigravity"
	"github.com/daidr/Ariadne/internal/providers/claudecode"
	"github.com/daidr/Ariadne/internal/providers/codex"
	"github.com/daidr/Ariadne/internal/providers/openai"
	"github.com/daidr/Ariadne/sdk"
)

// BuiltinOptions configures the built-in model provider plugins loaded through plugins.Host.
type BuiltinOptions struct {
	Client        *http.Client
	Bridge        *agentruntime.ToolBridge
	AgentDataDir  string
	Now           func() time.Time
	StdioLauncher agentruntime.StdioLauncher
}

// HTTPPlugins returns the built-in HTTP model provider plugins.
func HTTPPlugins(client *http.Client) []sdk.Plugin {
	return httpPlugins(client, nil)
}

func httpPlugins(client *http.Client, now func() time.Time) []sdk.Plugin {
	return []sdk.Plugin{
		openai.Plugin(client, now),
		anthropic.Plugin(client, now),
	}
}

// HTTPProviders returns the built-in HTTP model providers for direct registry setup in tests and tools.
func HTTPProviders(client *http.Client) []sdk.ModelProvider {
	return []sdk.ModelProvider{
		openai.ModelProvider(client, nil),
		anthropic.ModelProvider(client, nil),
	}
}

// AgentPlugins returns the built-in local agent CLI model provider plugins.
func AgentPlugins(opts agentruntime.PluginOptions) []sdk.Plugin {
	return []sdk.Plugin{
		claudecode.Plugin(opts),
		codex.Plugin(opts),
		acp.Plugin(opts),
		antigravity.Plugin(opts),
	}
}

// BuiltinPlugins returns all pre-installed model provider plugins loaded through plugins.Host.
func BuiltinPlugins(opts BuiltinOptions) []sdk.Plugin {
	agentOpts := agentruntime.PluginOptions{
		Bridge:        opts.Bridge,
		DataDir:       opts.AgentDataDir,
		Now:           opts.Now,
		StdioLauncher: opts.StdioLauncher,
	}
	return append(httpPlugins(opts.Client, opts.Now), AgentPlugins(agentOpts)...)
}
