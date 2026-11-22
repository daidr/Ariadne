package sdk

import "context"

// PluginContext registrations belong to one plugin and are rolled back together
// if Setup fails. Context lasts until unload; the Setup argument only lasts for
// startup. Lua plugins register through adapters in the host.
type PluginContext struct {
	Context           context.Context
	DataDir           string
	Settings          func() map[string]any
	OnDispose         func(func(context.Context) error) error
	RegisterModel     func(ModelProvider) (func(), error)
	RegisterContext   func(ContextProvider) (func(), error)
	RegisterDashboard func(DashboardSource) (func(), error)
	RegisterSearch    func(SearchProvider) (func(), error)
	RegisterLink      func(LinkProvider) (func(), error)
}
type PluginProvides struct {
	Models     []string `json:"models,omitempty"`
	Contexts   []string `json:"contexts,omitempty"`
	Dashboards []string `json:"dashboards,omitempty"`
	Searches   []string `json:"searches,omitempty"`
	Links      []string `json:"links,omitempty"`
}

type Plugin struct {
	ID       string
	Name     LocalizedText
	Provides PluginProvides
	Settings *SettingsSchema
	// SettingsReady checks configuration that can also come from outside Ariadne.
	// When omitted, readiness is determined by the schema's required fields.
	SettingsReady    func(context.Context, map[string]any) bool
	ValidateSettings func(map[string]any) error
	Setup            func(context.Context, PluginContext) (func(context.Context) error, error)
}
