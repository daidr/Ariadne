package settings

import (
	"path/filepath"
	"strings"

	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/sdk"
)

func searchInfo(kinds []sdk.SearchProviderInfo, kind string) (sdk.SearchProviderInfo, bool) {
	for _, info := range kinds {
		if info.Kind == kind {
			return info, true
		}
	}
	return sdk.SearchProviderInfo{}, false
}

func validSearchValue(field sdk.SettingField, raw any) bool {
	switch field.Type {
	case "string":
		value, ok := raw.(string)
		return ok && len(value) <= 32768 && !strings.ContainsRune(value, 0) &&
			(!field.Directory || strings.TrimSpace(value) == "" || filepath.IsAbs(strings.TrimSpace(value))) &&
			(len(field.Options) == 0 || hasString(field.Options, value))
	case "number":
		_, ok := number(raw)
		return ok
	case "boolean":
		_, ok := raw.(bool)
		return ok
	default:
		value, ok := raw.(string)
		return ok && hasString(field.Options, value)
	}
}

func effectiveSearchValues(schema sdk.SettingsSchema, stored Object) Object {
	resolved := Object{}
	for key, field := range schema.Properties {
		if field.Default != nil {
			resolved[key] = field.Default
		}
	}
	for key := range schema.Properties {
		value, exists := stored[key]
		if exists && value != nil && value != "" {
			resolved[key] = value
		}
	}
	owned, err := clone(resolved)
	if err != nil {
		return resolved
	}
	return owned
}

func searchRequiredReady(schema sdk.SettingsSchema, values Object) bool {
	for _, key := range schema.Required {
		value, exists := values[key]
		if !exists || value == nil || value == "" {
			return false
		}
	}
	return true
}

func searchDraft(before Object, input any, kinds []sdk.SearchProviderInfo, validate func(string, Object) error) (Object, error) {
	body := object(input)
	provider := textutil.Trim(text(before["provider"]))
	if raw, exists := body["provider"]; exists {
		value, ok := raw.(string)
		if !ok {
			return nil, bad("provider")
		}
		value = textutil.Trim(value)
		if value != "" {
			if _, ok := searchInfo(kinds, value); !ok {
				return nil, bad("provider")
			}
		}
		provider = value
	}
	if raw, exists := body["services"]; exists {
		if _, ok := raw.(map[string]any); !ok {
			return nil, bad("services")
		}
	}
	services, err := clone(object(before["services"]))
	if err != nil {
		return nil, err
	}
	edits := cloneMap(object(body["services"]))
	if key, exists := body["apiKey"]; exists {
		if _, ok := searchInfo(kinds, "tavily"); ok {
			entry := cloneMap(object(edits["tavily"]))
			entry["apiKey"] = key
			edits["tavily"] = entry
		}
	}
	for _, kind := range names(edits) {
		info, ok := searchInfo(kinds, kind)
		if !ok {
			return nil, bad("provider")
		}
		entry, ok := edits[kind].(map[string]any)
		if !ok {
			return nil, bad("services")
		}
		service := cloneMap(object(services[kind]))
		for _, key := range names(entry) {
			field, exists := info.Settings.Properties[key]
			if !exists {
				return nil, bad(key)
			}
			raw := entry[key]
			if field.Secret {
				if raw == nil {
					delete(service, key)
				} else {
					value, ok := raw.(string)
					if !ok {
						return nil, bad(key)
					}
					value = textutil.Trim(value)
					if value != "" {
						if !validSearchValue(field, value) {
							return nil, bad(key)
						}
						service[key] = value
					}
				}
				continue
			}
			if raw == nil {
				delete(service, key)
				continue
			}
			if field.Type == "string" {
				value, ok := raw.(string)
				if !ok {
					return nil, bad(key)
				}
				value = textutil.Trim(value)
				if !validSearchValue(field, value) {
					return nil, bad(key)
				}
				service[key] = value
				continue
			}
			if !validSearchValue(field, raw) {
				return nil, bad(key)
			}
			service[key] = raw
		}
		if validate != nil {
			if err := validate(kind, service); err != nil {
				return nil, err
			}
		}
		services[kind] = service
	}
	return Object{"provider": provider, "services": services}, nil
}

func (store *Store) SearchDraft(input any, kinds []sdk.SearchProviderInfo, validate func(string, Object) error) (Object, error) {
	config := store.snapshot()
	return searchDraft(object(config["search"]), input, kinds, validate)
}

func (store *Store) UpdateSearch(input any, kinds []sdk.SearchProviderInfo, validate func(string, Object) error) (Object, error) {
	var result Object
	err := store.update(func(config Object) error {
		value, err := searchDraft(object(config["search"]), input, kinds, validate)
		if err != nil {
			return err
		}
		config["search"] = value
		result = value
		return nil
	})
	return result, err
}

func (store *Store) ActiveSearchProvider() string {
	return textutil.Trim(text(object(store.snapshot()["search"])["provider"]))
}

// EffectiveSearchSettings merges non-secret schema defaults with stored values
// from a search configuration or draft object.
func EffectiveSearchSettings(searchConfig Object, info sdk.SearchProviderInfo) Object {
	stored := object(object(searchConfig["services"])[info.Kind])
	return effectiveSearchValues(info.Settings, stored)
}

func (store *Store) SearchSettings(info sdk.SearchProviderInfo) Object {
	return EffectiveSearchSettings(object(store.snapshot()["search"]), info)
}

// PublicSearch keeps secret fields in the private configuration and returns
// schema-driven metadata and public values for every registered search provider.
func (store *Store) PublicSearch(kinds []sdk.SearchProviderInfo, ready func(string, Object) bool, models []string) Object {
	config := store.snapshot()
	search := object(config["search"])
	services := object(search["services"])
	storedProvider := textutil.Trim(text(search["provider"]))
	if models == nil {
		models = []string{}
	}
	listed := []any{}
	readyPlugins := []any{}
	firstReady := ""
	activeReady := false
	activeKeySet := false
	resolvedProvider := ""
	disabledProvider := ""
	if storedProvider != "" {
		if _, ok := searchInfo(kinds, storedProvider); ok {
			resolvedProvider = storedProvider
		} else {
			disabledProvider = storedProvider
		}
	}
	for _, info := range kinds {
		stored := object(services[info.Kind])
		effective := effectiveSearchValues(info.Settings, stored)
		isReady := searchRequiredReady(info.Settings, effective)
		if ready != nil {
			isReady = ready(info.Kind, effective)
		}
		values := Object{}
		secretsSet := []string{}
		for _, key := range namesOfFields(info.Settings.Properties) {
			field := info.Settings.Properties[key]
			if field.Secret {
				if truthy(stored[key]) {
					secretsSet = append(secretsSet, key)
				}
				continue
			}
			if value, exists := stored[key]; exists && value != nil {
				values[key] = value
			} else if field.Type == "boolean" && field.Default != nil {
				values[key] = field.Default
			}
		}
		if isReady {
			readyPlugins = append(readyPlugins, info.Title)
			if firstReady == "" {
				firstReady = info.Kind
			}
		}
		entry := Object{
			"kind":         info.Kind,
			"title":        info.Title,
			"settings":     info.Settings,
			"capabilities": info.Capabilities,
			"values":       values,
			"secretsSet":   secretsSet,
			"ready":        isReady,
		}
		if len(info.Description) > 0 {
			entry["description"] = info.Description
		}
		listed = append(listed, entry)
	}
	if resolvedProvider == "" {
		if firstReady != "" {
			resolvedProvider = firstReady
		} else if len(kinds) > 0 {
			resolvedProvider = kinds[0].Kind
		}
	}
	for _, raw := range listed {
		entry := object(raw)
		if text(entry["kind"]) == resolvedProvider {
			activeReady = entry["ready"] == true
			if secrets, ok := entry["secretsSet"].([]string); ok {
				activeKeySet = len(secrets) > 0
			}
			break
		}
	}
	out := Object{
		"provider": resolvedProvider,
		"ready":    activeReady,
		"keySet":   activeKeySet,
		"needed":   len(list(object(config["models"])["models"])) > 0 && len(models) == 0 && len(readyPlugins) == 0,
		"models":   models,
		"plugins":  readyPlugins,
		"services": listed,
	}
	if disabledProvider != "" {
		out["disabledProvider"] = disabledProvider
	}
	return out
}
