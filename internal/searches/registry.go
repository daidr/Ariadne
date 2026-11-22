// Package searches owns search provider registration, validation and execution
// for both built-in and external search plugins.
package searches

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/sdk"
)

type Registry struct {
	mu        sync.RWMutex
	providers map[string]*sdk.SearchProvider
	order     []string
}

func New() *Registry {
	return &Registry{providers: map[string]*sdk.SearchProvider{}}
}

func copyText(value sdk.LocalizedText) sdk.LocalizedText {
	if value == nil {
		return nil
	}
	out := sdk.LocalizedText{}
	for key, text := range value {
		out[key] = text
	}
	return out
}

func copySchema(schema sdk.SettingsSchema) sdk.SettingsSchema {
	out := sdk.SettingsSchema{
		Type:       schema.Type,
		Required:   append([]string(nil), schema.Required...),
		FieldOrder: append([]string(nil), schema.FieldOrder...),
		Properties: map[string]sdk.SettingField{},
	}
	if out.Type == "" {
		out.Type = "object"
	}
	for key, field := range schema.Properties {
		field.Title = copyText(field.Title)
		field.Description = copyText(field.Description)
		field.Options = append([]string(nil), field.Options...)
		out.Properties[key] = field
	}
	return out
}

func copyProvider(provider sdk.SearchProvider) sdk.SearchProvider {
	owned := provider
	owned.Title = copyText(provider.Title)
	owned.Description = copyText(provider.Description)
	owned.Settings = copySchema(provider.Settings)
	return owned
}

func cloneValues(values map[string]any) map[string]any {
	if len(values) == 0 {
		return map[string]any{}
	}
	data, err := json.Marshal(values)
	if err != nil {
		out := make(map[string]any, len(values))
		for key, value := range values {
			out[key] = value
		}
		return out
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil || out == nil {
		return map[string]any{}
	}
	return out
}

func validateSchema(schema sdk.SettingsSchema) error {
	if schema.Type != "" && schema.Type != "object" {
		return errors.New("search provider settings schema must have type object")
	}
	for _, key := range schema.Required {
		if _, ok := schema.Properties[key]; !ok {
			return fmt.Errorf("search provider required setting %q has no field", key)
		}
	}
	for key, field := range schema.Properties {
		if field.Secret && (field.Type != "string" || field.Default != nil || len(field.Options) > 0) {
			return fmt.Errorf("search provider secret setting %q must be a plain string without default", key)
		}
	}
	return nil
}

func valuePresent(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return typed != ""
	case float64:
		return !math.IsNaN(typed) && !math.IsInf(typed, 0)
	case bool:
		return true
	default:
		return true
	}
}

func (registry *Registry) Register(provider sdk.SearchProvider) (func(), error) {
	if provider.Kind == "" || (provider.Execute == nil && provider.Search == nil) {
		return nil, errors.New("search provider needs kind and search")
	}
	if err := validateSchema(provider.Settings); err != nil {
		return nil, err
	}
	owned := copyProvider(provider)
	owned.Capabilities.SupportsCheck = owned.Check != nil
	if _, err := json.Marshal(owned.Info()); err != nil {
		return nil, err
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.providers[owned.Kind] != nil {
		return nil, fmt.Errorf("search provider is already registered: %s", owned.Kind)
	}
	entry := &owned
	registry.providers[owned.Kind] = entry
	registry.order = append(registry.order, owned.Kind)
	var once sync.Once
	return func() {
		once.Do(func() {
			registry.mu.Lock()
			defer registry.mu.Unlock()
			if registry.providers[owned.Kind] == entry {
				delete(registry.providers, owned.Kind)
				registry.order = slices.DeleteFunc(registry.order, func(kind string) bool { return kind == owned.Kind })
			}
		})
	}, nil
}

func (registry *Registry) Get(kind string) (sdk.SearchProvider, bool) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	provider := registry.providers[kind]
	if provider == nil {
		return sdk.SearchProvider{}, false
	}
	return copyProvider(*provider), true
}

func (registry *Registry) Providers() []sdk.SearchProvider {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	out := []sdk.SearchProvider{}
	for _, kind := range registry.order {
		out = append(out, copyProvider(*registry.providers[kind]))
	}
	return out
}

func (registry *Registry) Kinds() []sdk.SearchProviderInfo {
	providers := registry.Providers()
	out := make([]sdk.SearchProviderInfo, 0, len(providers))
	for _, provider := range providers {
		out = append(out, provider.Info())
	}
	return out
}

func (registry *Registry) Validate(kind string, values map[string]any) error {
	provider, ok := registry.Get(kind)
	if !ok {
		return domainerr.New("error.invalidBody", map[string]any{"reason": "provider"})
	}
	if provider.ValidateSettings == nil {
		return nil
	}
	return provider.ValidateSettings(values)
}

func (registry *Registry) Ready(kind string, values map[string]any) bool {
	provider, ok := registry.Get(kind)
	if !ok {
		return false
	}
	if provider.Ready != nil {
		return provider.Ready(cloneValues(values))
	}
	for _, key := range provider.Settings.Required {
		if !valuePresent(values[key]) {
			return false
		}
	}
	return true
}

func (registry *Registry) Execute(ctx context.Context, kind string, values map[string]any, query sdk.SearchQuery) (sdk.SearchResponse, error) {
	provider, ok := registry.Get(kind)
	if !ok {
		return sdk.SearchResponse{}, errors.New("unknown search provider")
	}
	if maxRunes := provider.Capabilities.MaxQueryRunes; maxRunes > 0 && utf8.RuneCountInString(query.Query) > maxRunes {
		return sdk.SearchResponse{}, fmt.Errorf("search query must be at most %d characters", maxRunes)
	}
	if maxResults := provider.Capabilities.MaxResults; maxResults > 0 && query.Limit > maxResults {
		query.Limit = maxResults
	}
	if provider.Execute != nil {
		response, err := provider.Execute(ctx, cloneValues(values), query)
		if err != nil {
			return sdk.SearchResponse{}, err
		}
		if response.Results == nil {
			response.Results = []sdk.SearchResult{}
		}
		return response, nil
	}
	hits, err := provider.Search(ctx, query.Query, query.Limit)
	if err != nil {
		return sdk.SearchResponse{}, err
	}
	if hits == nil {
		hits = []sdk.SearchResult{}
	}
	return sdk.SearchResponse{Results: hits}, nil
}

func (registry *Registry) Check(ctx context.Context, kind string, values map[string]any) (sdk.SearchCheckResult, error) {
	provider, ok := registry.Get(kind)
	if !ok {
		return sdk.SearchCheckResult{}, domainerr.New("error.invalidBody", map[string]any{"reason": "provider"})
	}
	for _, key := range provider.Settings.Required {
		if !valuePresent(values[key]) {
			return sdk.SearchCheckResult{}, domainerr.New("error.invalidBody", map[string]any{"reason": key})
		}
	}
	if provider.Check == nil {
		return sdk.SearchCheckResult{}, domainerr.New("error.invalidBody", map[string]any{"reason": "check"})
	}
	if provider.Ready != nil && !provider.Ready(cloneValues(values)) {
		return sdk.SearchCheckResult{}, domainerr.New("error.invalidBody", map[string]any{"reason": "settings"})
	}
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return provider.Check(checkCtx, cloneValues(values))
}
