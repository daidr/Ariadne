// Package sources owns context provider registration. A connection's resources
// belong to its caller and are released through Capabilities.Close.
package sources

import (
	"encoding/json"
	"errors"
	"slices"
	"sync"

	"github.com/daidr/Ariadne/sdk"
)

type Registry struct {
	mu        sync.RWMutex
	providers map[string]*sdk.ContextProvider
	order     []string
	versions  map[string]uint64
	next      uint64
}

func New() *Registry {
	return &Registry{providers: map[string]*sdk.ContextProvider{}, versions: map[string]uint64{}}
}
func copyInfo(info sdk.ContextProviderInfo) sdk.ContextProviderInfo {
	data, _ := json.Marshal(info)
	var result sdk.ContextProviderInfo
	_ = json.Unmarshal(data, &result)
	return result
}
func (registry *Registry) Register(provider sdk.ContextProvider) (func(), error) {
	if provider.Info.Kind == "" || provider.Connect == nil {
		return nil, errors.New("context provider requires a kind and connect implementation")
	}
	provider.Info.Browse = provider.Browse != nil
	if _, err := json.Marshal(provider.Info); err != nil {
		return nil, err
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.providers[provider.Info.Kind] != nil {
		return nil, errors.New("context provider kind is already registered: " + provider.Info.Kind)
	}
	owned := provider
	owned.Info = copyInfo(provider.Info)
	registry.providers[provider.Info.Kind] = &owned
	registry.next++
	registry.versions[provider.Info.Kind] = registry.next
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
			delete(registry.versions, provider.Info.Kind)
			registry.order = slices.DeleteFunc(registry.order, func(kind string) bool { return kind == provider.Info.Kind })
		})
	}, nil
}
func (registry *Registry) Generation(kind string) uint64 {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	return registry.versions[kind]
}
func (registry *Registry) Get(kind string) (sdk.ContextProvider, bool) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	provider := registry.providers[kind]
	if provider == nil {
		return sdk.ContextProvider{}, false
	}
	result := *provider
	result.Info = copyInfo(result.Info)
	return result, true
}
func (registry *Registry) Kinds() []sdk.ContextProviderInfo {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	result := []sdk.ContextProviderInfo{}
	for _, kind := range registry.order {
		result = append(result, copyInfo(registry.providers[kind].Info))
	}
	return result
}
func SecretFields(info sdk.ContextProviderInfo) []string {
	result := []string{}
	for key, field := range info.Settings.Properties {
		if field.Secret {
			result = append(result, key)
		}
	}
	slices.Sort(result)
	return result
}
