package sdk

import "context"

type DashboardSourceInfo struct {
	Kind        string         `json:"kind"`
	Title       LocalizedText  `json:"title"`
	Description string         `json:"description"`
	Settings    SettingsSchema `json:"settings"`
}
type DashboardSource struct {
	Info  DashboardSourceInfo
	Fetch func(context.Context, map[string]any, string) (map[string]any, error)
}
