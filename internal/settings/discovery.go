package settings

import (
	"strconv"
	"strings"
	"time"

	"github.com/daidr/Ariadne/sdk"
)

type FoundSetup struct {
	ID       string            `json:"id"`
	Kind     string            `json:"kind"`
	Title    sdk.LocalizedText `json:"title"`
	From     map[string]string `json:"from"`
	Settings Object            `json:"settings"`
}

func strictEqual(first, second any) bool {
	switch first := first.(type) {
	case nil:
		return second == nil
	case string:
		value, ok := second.(string)
		return ok && first == value
	case bool:
		value, ok := second.(bool)
		return ok && first == value
	case float64:
		value, ok := second.(float64)
		return ok && first == value
	}
	return false
}
func foundBy(provider sdk.ModelProvider, context sdk.FindContext) (found []sdk.FoundProvider) {
	defer func() {
		if recover() != nil {
			found = nil
		}
	}()
	if provider.Find == nil {
		return nil
	}
	return provider.Find(context)
}
func (store *Store) FoundSetups(context sdk.FindContext, providers []sdk.ModelProvider) []FoundSetup {
	used := list(object(store.snapshot()["models"])["providers"])
	result := []FoundSetup{}
	for _, provider := range providers {
		for _, setup := range foundBy(provider, context) {
			already := false
			for _, raw := range used {
				before := object(raw)
				if before["kind"] != provider.Info.Kind {
					continue
				}
				matches := true
				for key, value := range setup.Settings {
					if !strictEqual(object(before["settings"])[key], value) {
						matches = false
						break
					}
				}
				if matches {
					already = true
					break
				}
			}
			if already {
				continue
			}
			title := provider.Info.Title
			if setup.Name != "" {
				title = sdk.LocalizedText{"zh-CN": setup.Name, "en": setup.Name}
			}
			result = append(result, FoundSetup{ID: provider.Info.Kind + ":" + setup.ID, Kind: provider.Info.Kind, Title: title, From: setup.From, Settings: cloneMap(setup.Settings)})
		}
	}
	return result
}
func foundHint(info sdk.ModelProviderInfo, settings Object) string {
	parts := []string{info.Kind}
	secrets := secretFields(info)
	for _, key := range names(settings) {
		if value, ok := settings[key].(string); ok && !hasString(secrets, key) {
			parts = append(parts, value)
		}
	}
	return strings.Join(parts, " ")
}
func FoundProvider(setup FoundSetup, kinds []sdk.ModelProviderInfo) (DraftProvider, error) {
	info, exists := providerInfo(kinds, setup.Kind)
	if !exists {
		return DraftProvider{}, bad("id")
	}
	return DraftProvider{Info: info, Settings: effective(info, setup.Settings), Headers: Object{}, Hint: foundHint(info, setup.Settings)}, nil
}
func infoFields(info Object) Object {
	fields := Object{}
	for _, key := range []string{"contextTokens", "maxTokens", "price", "efforts"} {
		if truthy(info[key]) {
			fields[key] = info[key]
		}
	}
	if info["images"] == false {
		fields["images"] = false
	}
	return fields
}
func (store *Store) UseFound(setup FoundSetup, model string, info Object, now time.Time) (Object, error) {
	stamp := strconv.FormatInt(now.UnixMilli(), 36)
	var entry Object
	err := store.update(func(config Object) error {
		title, exists := setup.Title[text(config["locale"])]
		if !exists {
			title = setup.Kind
		}
		models := object(config["models"])
		providers := list(models["providers"])
		taken := false
		for _, raw := range providers {
			if object(raw)["name"] == title {
				taken = true
				break
			}
		}
		if env, exists := setup.From["env"]; exists && taken {
			title += " (" + env + ")"
		}
		provider := Object{"id": "p_" + stamp, "kind": setup.Kind, "name": title, "settings": setup.Settings}
		entry = Object{"id": "m_" + stamp, "provider": provider["id"], "model": model}
		for key, value := range infoFields(info) {
			entry[key] = value
		}
		models["providers"] = append(providers, provider)
		models["models"] = append(list(models["models"]), entry)
		roleConfig := object(models["roles"])
		thread := object(roleConfig["thread"])
		if thread["model"] == nil {
			thread["model"] = entry["id"]
		}
		roleConfig["thread"] = thread
		models["roles"] = roleConfig
		config["models"] = models
		return nil
	})
	return entry, err
}
