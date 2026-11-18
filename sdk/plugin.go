package sdk

import "context"

type SearchProvider struct {
	Kind   string
	Title  LocalizedText
	Search func(context.Context, string, int) ([]SearchResult, error)
}

// PluginContext registrations belong to one plugin and are rolled back together
// if Setup fails. Context lasts until unload; the Setup argument only lasts for
// startup. Lua plugins register through adapters in the host.
type PluginContext struct {
	Context           context.Context
	DataDir           string
	OnDispose         func(func(context.Context) error) error
	RegisterModel     func(ModelProvider) (func(), error)
	RegisterContext   func(ContextProvider) (func(), error)
	RegisterDashboard func(DashboardSource) (func(), error)
	RegisterSearch    func(SearchProvider) (func(), error)
	RegisterLink      func(LinkProvider) (func(), error)
}
type Plugin struct {
	ID    string
	Name  LocalizedText
	Setup func(context.Context, PluginContext) (func(context.Context) error, error)
}
