package searches

import (
	"net/http"

	"github.com/daidr/Ariadne/sdk"
)

// BuiltinPlugins returns the pre-installed search plugins loaded through plugins.Host.
func BuiltinPlugins(client *http.Client) []sdk.Plugin {
	return []sdk.Plugin{
		TavilyPlugin(client),
		DeepSeekPlugin(client),
	}
}

// BuiltinProviders returns the built-in search providers for direct registry setup in tests.
func BuiltinProviders(client *http.Client) []sdk.SearchProvider {
	return []sdk.SearchProvider{
		TavilyProvider(client),
		DeepSeekProvider(client),
	}
}
