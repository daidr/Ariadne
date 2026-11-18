// Package models registers provider implementations independently of transport
// and settings persistence. Registry snapshots preserve registration order.
package models

import (
	"errors"
	"sync"

	"github.com/daidr/Ariadne/sdk"
)

type Registry struct {
	mu        sync.RWMutex
	providers map[string]*sdk.ModelProvider
	order     []string
}

func metadataCopy(info sdk.ModelProviderInfo) sdk.ModelProviderInfo {
	copyText := func(value sdk.LocalizedText) sdk.LocalizedText {
		if value == nil {
			return nil
		}
		out := sdk.LocalizedText{}
		for key, text := range value {
			out[key] = text
		}
		return out
	}
	info.Title = copyText(info.Title)
	info.Settings.Required = append([]string(nil), info.Settings.Required...)
	info.Settings.FieldOrder = append([]string(nil), info.Settings.FieldOrder...)
	if info.Settings.Properties != nil {
		fields := map[string]sdk.SettingField{}
		for key, field := range info.Settings.Properties {
			field.Title = copyText(field.Title)
			field.Description = copyText(field.Description)
			field.Options = append([]string(nil), field.Options...)
			fields[key] = field
		}
		info.Settings.Properties = fields
	}
	return info
}

func New() *Registry { return &Registry{providers: map[string]*sdk.ModelProvider{}} }
func (registry *Registry) Register(provider sdk.ModelProvider) (func(), error) {
	if provider.Info.Kind == "" || provider.Stream == nil {
		return nil, errors.New("model provider requires a kind and stream implementation")
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.providers[provider.Info.Kind] != nil {
		return nil, errors.New("model provider kind is already registered: " + provider.Info.Kind)
	}
	owned := provider
	owned.Info = metadataCopy(provider.Info)
	registry.providers[provider.Info.Kind] = &owned
	registry.order = append(registry.order, provider.Info.Kind)
	var once sync.Once
	return func() {
		once.Do(func() {
			registry.mu.Lock()
			defer registry.mu.Unlock()
			if registry.providers[provider.Info.Kind] != &owned {
				return
			}
			delete(registry.providers, provider.Info.Kind)
			for index, kind := range registry.order {
				if kind == provider.Info.Kind {
					registry.order = append(registry.order[:index], registry.order[index+1:]...)
					break
				}
			}
		})
	}, nil
}
func (registry *Registry) Get(kind string) (sdk.ModelProvider, bool) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	provider := registry.providers[kind]
	if provider == nil {
		return sdk.ModelProvider{}, false
	}
	copy := *provider
	copy.Info = metadataCopy(copy.Info)
	return copy, true
}
func (registry *Registry) Providers() []sdk.ModelProvider {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	result := []sdk.ModelProvider{}
	for _, kind := range registry.order {
		provider := *registry.providers[kind]
		provider.Info = metadataCopy(provider.Info)
		result = append(result, provider)
	}
	return result
}
func (registry *Registry) Kinds() []sdk.ModelProviderInfo {
	providers := registry.Providers()
	result := []sdk.ModelProviderInfo{}
	for _, provider := range providers {
		result = append(result, provider.Info)
	}
	return result
}
