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
}
type ContextProvider struct {
	Info       ContextProviderInfo
	SecretsFor func(map[string]any) (string, error)
	Connect    func(context.Context, map[string]any) (Capabilities, error)
	Browse     func(context.Context, map[string]any, string) (*DirectoryListing, error)
}
