package sdk

import "context"

type ContextProviderInfo struct {
	Kind     string         `json:"kind"`
	Title    LocalizedText  `json:"title"`
	Settings SettingsSchema `json:"settings"`
	Provides []string       `json:"provides"`
	Remote   bool           `json:"remote"`
	Hosting  bool           `json:"hosting"`
	Browse   bool           `json:"browse"`
	Summary  *string        `json:"summary"`
	Plugin   *SourcePlugin  `json:"plugin,omitempty"`
	Defaults map[string]any `json:"defaults,omitempty"`
}
type SourcePlugin struct {
	ID    string `json:"id"`
	Ready bool   `json:"ready"`
}
type ContextProvider struct {
	Info                ContextProviderInfo
	NeedsPluginSettings bool
	// Hidden project fields remain recognized for existing source records.
	PluginFields      []string
	ValidateSettings  func(values, previous map[string]any) error
	NewSourceDefaults func() map[string]any
	SecretsFor        func(map[string]any) (string, error)
	Connect           func(context.Context, map[string]any) (Capabilities, error)
	Browse            func(context.Context, map[string]any, string) (*DirectoryListing, error)
}
