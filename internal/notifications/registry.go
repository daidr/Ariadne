// Package notifications delivers offline notifications over Web Push (RFC 8291/8292),
// generic signed Webhooks, and external/built-in notifier plugins.
package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sync"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/sdk"
)

// Registry owns registration, validation, and execution of notification plugins.
type Registry struct {
	mu        sync.RWMutex
	providers map[string]*sdk.NotifierProvider
	order     []string
}

func NewRegistry() *Registry {
	return &Registry{providers: map[string]*sdk.NotifierProvider{}}
}

func copyLocalizedText(value sdk.LocalizedText) sdk.LocalizedText {
	if value == nil {
		return nil
	}
	out := sdk.LocalizedText{}
	for key, text := range value {
		out[key] = text
	}
	return out
}

func copySettingsSchema(schema sdk.SettingsSchema) sdk.SettingsSchema {
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
		field.Title = copyLocalizedText(field.Title)
		field.Description = copyLocalizedText(field.Description)
		field.Options = append([]string(nil), field.Options...)
		out.Properties[key] = field
	}
	return out
}

func copyNotifierProvider(provider sdk.NotifierProvider) sdk.NotifierProvider {
	owned := provider
	owned.Title = copyLocalizedText(provider.Title)
	owned.Description = copyLocalizedText(provider.Description)
	owned.Settings = copySettingsSchema(provider.Settings)
	return owned
}

func cloneSettingsMap(values map[string]any) map[string]any {
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

func validateNotifierSchema(schema sdk.SettingsSchema) error {
	if schema.Type != "" && schema.Type != "object" {
		return errors.New("notifier provider settings schema must have type object")
	}
	for _, key := range schema.Required {
		if _, ok := schema.Properties[key]; !ok {
			return fmt.Errorf("notifier provider required setting %q has no field", key)
		}
	}
	for key, field := range schema.Properties {
		if field.Secret && (field.Type != "string" || field.Default != nil || len(field.Options) > 0) {
			return fmt.Errorf("notifier provider secret setting %q must be a plain string without default", key)
		}
	}
	return nil
}

func settingPresent(value any) bool {
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

func (registry *Registry) Register(provider sdk.NotifierProvider) (func(), error) {
	if provider.Kind == "" || provider.Send == nil {
		return nil, errors.New("notifier provider requires kind and send")
	}
	if err := validateNotifierSchema(provider.Settings); err != nil {
		return nil, err
	}
	owned := copyNotifierProvider(provider)
	if owned.Check != nil {
		owned.Capabilities.SupportsCheck = true
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.providers[owned.Kind]; exists {
		return nil, fmt.Errorf("notifier provider %q already registered", owned.Kind)
	}
	entry := &owned
	registry.providers[owned.Kind] = entry
	registry.order = append(registry.order, owned.Kind)
	return func() {
		registry.mu.Lock()
		defer registry.mu.Unlock()
		if registry.providers[owned.Kind] != entry {
			return
		}
		delete(registry.providers, owned.Kind)
		registry.order = slices.DeleteFunc(registry.order, func(kind string) bool { return kind == owned.Kind })
	}, nil
}

func (registry *Registry) Get(kind string) (sdk.NotifierProvider, bool) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	provider, ok := registry.providers[kind]
	if !ok {
		return sdk.NotifierProvider{}, false
	}
	return copyNotifierProvider(*provider), true
}

func (registry *Registry) Has(kind string) bool {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	_, ok := registry.providers[kind]
	return ok
}

func (registry *Registry) Kinds() []sdk.NotifierProviderInfo {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	out := make([]sdk.NotifierProviderInfo, 0, len(registry.order))
	for _, kind := range registry.order {
		if provider := registry.providers[kind]; provider != nil {
			out = append(out, copyNotifierProvider(*provider).Info())
		}
	}
	return out
}

func (registry *Registry) Validate(kind string, values map[string]any) error {
	provider, ok := registry.Get(kind)
	if !ok {
		return domainerr.New("error.invalidInput", map[string]any{"field": "notifier"})
	}
	if provider.ValidateSettings != nil {
		return provider.ValidateSettings(cloneSettingsMap(values))
	}
	return nil
}

func (registry *Registry) Ready(kind string, values map[string]any) bool {
	provider, ok := registry.Get(kind)
	if !ok {
		return false
	}
	if provider.Ready != nil {
		return provider.Ready(cloneSettingsMap(values))
	}
	for _, key := range provider.Settings.Required {
		if !settingPresent(values[key]) {
			return false
		}
	}
	return true
}

func (registry *Registry) Send(ctx context.Context, kind string, values map[string]any, payload sdk.NotificationPayload) error {
	provider, ok := registry.Get(kind)
	if !ok {
		return domainerr.NotFound(kind)
	}
	return provider.Send(ctx, cloneSettingsMap(values), payload)
}

func (registry *Registry) Check(ctx context.Context, kind string, values map[string]any) (sdk.NotifierCheckResult, error) {
	provider, ok := registry.Get(kind)
	if !ok {
		return sdk.NotifierCheckResult{}, domainerr.NotFound(kind)
	}
	if provider.Check == nil {
		return sdk.NotifierCheckResult{}, domainerr.New("error.invalidInput", map[string]any{"field": "notifier"})
	}
	return provider.Check(ctx, cloneSettingsMap(values))
}
