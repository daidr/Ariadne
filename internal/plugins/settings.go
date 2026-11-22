package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/sdk"
)

type SettingsView struct {
	Name       sdk.LocalizedText   `json:"name"`
	Schema     *sdk.SettingsSchema `json:"schema"`
	FieldOrder []string            `json:"fieldOrder,omitempty"`
	Values     map[string]any      `json:"values"`
	SecretsSet []string            `json:"secretsSet"`
	Ready      bool                `json:"ready"`
}

func validValue(field sdk.SettingField, raw any) bool {
	switch field.Type {
	case "string":
		value, ok := raw.(string)
		if !ok || len(value) > 32768 || strings.ContainsRune(value, 0) {
			return false
		}
		if field.Directory && strings.TrimSpace(value) != "" && !filepath.IsAbs(strings.TrimSpace(value)) {
			return false
		}
		if field.NetworkOrigin && strings.TrimSpace(value) != "" {
			if _, ok := parseNetworkOrigin(value); !ok {
				return false
			}
		}
		return len(field.Options) == 0 || slices.Contains(field.Options, value)
	case "number":
		_, ok := raw.(float64)
		return ok
	case "boolean":
		_, ok := raw.(bool)
		return ok
	default:
		// Provider settings historically use the type "select" for enums.
		value, ok := raw.(string)
		return ok && slices.Contains(field.Options, value)
	}
}

func checkedSettingsSchema(schema *sdk.SettingsSchema) (*sdk.SettingsSchema, error) {
	if schema == nil {
		return nil, nil
	}
	raw, err := json.Marshal(schema)
	if err != nil || len(raw) > 64<<10 || schema.Type != "object" || len(schema.Properties) == 0 {
		return nil, errors.New("invalid plugin settings schema")
	}
	var owned sdk.SettingsSchema
	if err := json.Unmarshal(raw, &owned); err != nil {
		return nil, err
	}
	owned.FieldOrder = slices.Clone(schema.FieldOrder)
	for _, key := range owned.Required {
		if _, ok := owned.Properties[key]; !ok {
			return nil, errors.New("required plugin setting has no field")
		}
	}
	for _, field := range owned.Properties {
		if field.Secret && (field.Type != "string" || field.Default != nil || len(field.Options) > 0) ||
			field.NetworkOrigin && (field.Type != "string" || field.Secret || field.Directory) ||
			field.Default != nil && !validValue(field, field.Default) {
			return nil, errors.New("invalid plugin setting default or secret")
		}
	}
	return &owned, nil
}

func pluginValues(store *settings.Store, id string, schema *sdk.SettingsSchema) map[string]any {
	values := map[string]any{}
	if schema == nil {
		return values
	}
	stored := map[string]any{}
	if store != nil {
		stored = store.PluginValues(id)
	}
	for key, field := range schema.Properties {
		if value := stored[key]; validValue(field, value) {
			values[key] = value
		} else if field.Default != nil {
			values[key] = field.Default
		}
	}
	return values
}

func (entry *loaded) settingsReady(ctx context.Context, values map[string]any) bool {
	if entry.ready != nil {
		return entry.ready(ctx, values)
	}
	for _, key := range entry.schema.Required {
		if value, ok := values[key]; !ok || value == "" {
			return false
		}
	}
	return true
}

func (host *Host) settingsEntry(id string) (*loaded, *settings.Store) {
	host.mu.Lock()
	defer host.mu.Unlock()
	for _, entry := range host.loaded {
		if entry.record.ID != nil && *entry.record.ID == id && entry.schema != nil {
			return &loaded{record: entry.record, schema: entry.schema, ready: entry.ready, validate: entry.validate}, host.options.Settings
		}
	}
	return nil, nil
}

func (host *Host) Settings(ctx context.Context, id string) (SettingsView, error) {
	entry, store := host.settingsEntry(id)
	if entry == nil {
		return SettingsView{}, domainerr.NotFound(id)
	}
	schema := entry.schema
	values := pluginValues(store, id, schema)
	view := SettingsView{Name: entry.record.Name, Schema: schema, FieldOrder: schema.FieldOrder, Values: values, SecretsSet: []string{}, Ready: entry.settingsReady(ctx, values)}
	for key, field := range schema.Properties {
		if field.Secret {
			if values[key] != nil {
				view.SecretsSet = append(view.SecretsSet, key)
				delete(values, key)
			}
		}
	}
	sort.Strings(view.SecretsSet)
	return view, nil
}

func (host *Host) SettingsReady(ctx context.Context, id string) bool {
	entry, store := host.settingsEntry(id)
	return entry != nil && entry.settingsReady(ctx, pluginValues(store, id, entry.schema))
}

func (host *Host) SaveSettings(ctx context.Context, id string, changes map[string]any) (SettingsView, error) {
	if err := host.saveSettings(ctx, id, changes); err != nil {
		return SettingsView{}, err
	}
	return host.Settings(ctx, id)
}

func (host *Host) saveSettings(ctx context.Context, id string, changes map[string]any) error {
	unlock, err := host.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	entry, store := host.settingsEntry(id)
	if entry == nil || store == nil {
		return domainerr.NotFound(id)
	}
	schema := entry.schema
	for key, value := range changes {
		field, exists := schema.Properties[key]
		if !exists || value != nil && !validValue(field, value) {
			return domainerr.New("error.invalidBody", map[string]any{"reason": key})
		}
		if stringValue, ok := value.(string); ok {
			if trimmed := strings.TrimSpace(stringValue); trimmed != "" {
				changes[key] = trimmed
			} else if field.Secret {
				delete(changes, key)
			} else {
				changes[key] = nil
			}
		}
	}
	if len(changes) > 0 {
		if entry.validate != nil {
			next := pluginValues(store, id, schema)
			for key, value := range changes {
				if value == nil {
					value = schema.Properties[key].Default
				}
				next[key] = value
			}
			if err := entry.validate(next); err != nil {
				return err
			}
		}
		if err := store.SavePluginValues(id, changes); err != nil {
			return err
		}
		host.options.Sources.Invalidate(entry.record.Provides.Contexts)
	}
	return nil
}
