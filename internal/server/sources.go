package server

import (
	"context"
	"slices"

	"github.com/daidr/Ariadne/internal/sources"
	"github.com/daidr/Ariadne/sdk"
)

func (api *API) sourceKinds(ctx context.Context) []sdk.ContextProviderInfo {
	if api.options.Sources == nil {
		return []sdk.ContextProviderInfo{}
	}
	kinds := api.options.Sources.Kinds()
	for i := range kinds {
		info := &kinds[i]
		provider, _ := api.options.Sources.Get(info.Kind)
		info.Defaults = sources.Defaults(provider)
		if info.Plugin == nil {
			continue
		}
		info.Plugin.Ready = api.options.Plugins != nil && api.options.Plugins.SettingsReady(ctx, info.Plugin.ID)
		for _, key := range provider.PluginFields {
			delete(info.Settings.Properties, key)
		}
		info.Settings.Required = slices.DeleteFunc(info.Settings.Required, func(key string) bool {
			return slices.Contains(provider.PluginFields, key)
		})
	}
	return kinds
}
