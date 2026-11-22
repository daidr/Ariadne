package sdk

import "context"

// SearchCapabilities describes optional features and limits of a search provider.
type SearchCapabilities struct {
	MaxQueryRunes int  `json:"maxQueryRunes,omitempty"`
	MaxResults    int  `json:"maxResults,omitempty"`
	ReturnsAnswer bool `json:"returnsAnswer,omitempty"`
	SupportsCheck bool `json:"supportsCheck,omitempty"`
}

// SearchProviderInfo is the public metadata and settings schema of a search provider.
type SearchProviderInfo struct {
	Kind         string             `json:"kind"`
	Title        LocalizedText      `json:"title"`
	Description  LocalizedText      `json:"description,omitempty"`
	Settings     SettingsSchema     `json:"settings"`
	Capabilities SearchCapabilities `json:"capabilities,omitempty"`
}

// SearchQuery is the input passed to SearchProvider.Execute.
type SearchQuery struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

// SearchResponse is the structured result returned by SearchProvider.Execute.
type SearchResponse struct {
	Answer  string         `json:"answer,omitempty"`
	Results []SearchResult `json:"results"`
}

// SearchCheckResult reports the outcome of testing a search provider's settings.
type SearchCheckResult struct {
	OK      bool          `json:"ok"`
	Status  *int          `json:"status"`
	Message LocalizedText `json:"message,omitempty"`
}

// SearchProvider is the contract used by built-in and external search providers.
type SearchProvider struct {
	Kind             string
	Title            LocalizedText
	Description      LocalizedText
	Settings         SettingsSchema
	Capabilities     SearchCapabilities
	ValidateSettings func(map[string]any) error
	Ready            func(map[string]any) bool
	Execute          func(context.Context, map[string]any, SearchQuery) (SearchResponse, error)
	Check            func(context.Context, map[string]any) (SearchCheckResult, error)
	// Search is kept for compatibility with simple and legacy search plugins.
	Search func(context.Context, string, int) ([]SearchResult, error)
}

func (provider SearchProvider) Info() SearchProviderInfo {
	return SearchProviderInfo{
		Kind:         provider.Kind,
		Title:        provider.Title,
		Description:  provider.Description,
		Settings:     provider.Settings,
		Capabilities: provider.Capabilities,
	}
}
