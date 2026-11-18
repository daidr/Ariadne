package models

import "github.com/daidr/Ariadne/internal/settings"

func (registry *Registry) SearchModels(store *settings.Store) []*settings.ResolvedModel {
	result := []*settings.ResolvedModel{}
	for _, model := range store.ResolvedModels("research", registry.Kinds()) {
		provider, exists := registry.Get(model.Provider.Kind)
		if exists && provider.Search != nil && (provider.SearchAvailable == nil || provider.SearchAvailable(model.Settings)) {
			result = append(result, model)
		}
	}
	return result
}
