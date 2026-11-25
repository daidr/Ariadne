package sdk

import "context"

// NotifierCapabilities describes optional features of a notification provider.
type NotifierCapabilities struct {
	SupportsCheck bool `json:"supportsCheck,omitempty"`
}

// NotifierProviderInfo is the public metadata and settings schema of a notification provider.
type NotifierProviderInfo struct {
	Kind         string               `json:"kind"`
	Title        LocalizedText        `json:"title"`
	Description  LocalizedText        `json:"description,omitempty"`
	Settings     SettingsSchema       `json:"settings"`
	Capabilities NotifierCapabilities `json:"capabilities,omitempty"`
}

// NotificationPayload is the structured event payload delivered to notification channels
// (Web Push, generic Webhooks, and external/built-in NotifierProviders).
type NotificationPayload struct {
	Event       string `json:"event"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	URL         string `json:"url"`
	ProjectID   string `json:"projectId,omitempty"`
	ProjectName string `json:"projectName,omitempty"`
	ThreadID    string `json:"threadId,omitempty"`
	ThreadTitle string `json:"threadTitle,omitempty"`
	Tag         string `json:"tag,omitempty"`
	NeedsMe     int    `json:"needsMe"`
	At          int64  `json:"at"`
}

// NotifierCheckResult reports the outcome of testing a notification provider's settings.
type NotifierCheckResult struct {
	OK      bool          `json:"ok"`
	Status  *int          `json:"status,omitempty"`
	Message LocalizedText `json:"message,omitempty"`
}

// NotifierProvider is the contract used by built-in and external notification plugins.
type NotifierProvider struct {
	Kind             string
	Title            LocalizedText
	Description      LocalizedText
	Settings         SettingsSchema
	Capabilities     NotifierCapabilities
	ValidateSettings func(map[string]any) error
	Ready            func(map[string]any) bool
	Send             func(context.Context, map[string]any, NotificationPayload) error
	Check            func(context.Context, map[string]any) (NotifierCheckResult, error)
}

func (provider NotifierProvider) Info() NotifierProviderInfo {
	return NotifierProviderInfo{
		Kind:         provider.Kind,
		Title:        provider.Title,
		Description:  provider.Description,
		Settings:     provider.Settings,
		Capabilities: provider.Capabilities,
	}
}
