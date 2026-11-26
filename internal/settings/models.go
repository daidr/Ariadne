package settings

import (
	"math"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/daidr/Ariadne/internal/jsonutil"
	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/sdk"
)

func providerInfo(kinds []sdk.ModelProviderInfo, kind string) (sdk.ModelProviderInfo, bool) {
	for _, info := range kinds {
		if info.Kind == kind {
			return info, true
		}
	}
	return sdk.ModelProviderInfo{}, false
}
func secretFields(info sdk.ModelProviderInfo) []string {
	fields := []string{}
	for _, key := range namesOfFields(info.Settings.Properties) {
		field := info.Settings.Properties[key]
		if field.Type == "string" && field.Secret {
			fields = append(fields, key)
		}
	}
	return fields
}
func namesOfFields(fields map[string]sdk.SettingField) []string {
	value := Object{}
	for key := range fields {
		value[key] = true
	}
	return names(value)
}
func storedProviderSecrets(kind string, typed any, before Object, info sdk.ModelProviderInfo) Object {
	settings := cloneMap(object(typed))
	for _, key := range secretFields(info) {
		value, exists := settings[key]
		if exists && value == nil {
			delete(settings, key)
		} else if !truthy(value) && before["kind"] == kind && truthy(object(before["settings"])[key]) {
			settings[key] = object(before["settings"])[key]
		}
	}
	return settings
}
func truthy(value any) bool {
	switch value := value.(type) {
	case nil:
		return false
	case bool:
		return value
	case string:
		return value != ""
	case float64:
		return value != 0 && !math.IsNaN(value)
	}
	return true
}
func parsePrice(raw any, at string) (Object, error) {
	if raw == nil {
		return nil, nil
	}
	given := object(raw)
	currency := text(given["currency"])
	if currency != "USD" && currency != "CNY" {
		return nil, bad(at + ".currency")
	}
	price := Object{"currency": currency}
	// Preserve the legacy validation order, including optional cache prices.
	for _, field := range []string{"cacheRead", "cacheWrite", "input", "output"} {
		raw := given[field]
		optional := field == "cacheRead" || field == "cacheWrite"
		if raw == nil && optional {
			continue
		}
		value, ok := number(raw)
		if !ok || value < 0 || value > 10000 {
			return nil, bad(at + "." + field)
		}
		price[field] = value
	}
	return price, nil
}
func findByID(items []any, id string) Object {
	if id == "" {
		return nil
	}
	for _, raw := range items {
		item := object(raw)
		if item["id"] == id {
			return item
		}
	}
	return nil
}
func (store *Store) UpdateModels(input any, kinds []sdk.ModelProviderInfo, validate ...func(string, Object) error) ([]any, error) {
	var validateSettings func(string, Object) error
	if len(validate) > 0 {
		validateSettings = validate[0]
	}
	body := object(input)
	var changedModels []any
	err := store.update(func(config Object) error {
		old := object(config["models"])
		previousProviders := list(old["providers"])
		rawProviders, ok := body["providers"].([]any)
		if !ok {
			return bad("providers")
		}
		providers := []any{}
		for index, raw := range rawProviders {
			given := object(raw)
			at := "providers[" + strconv.Itoa(index) + "]"
			id := text(given["id"])
			if id == "" {
				return bad(at + ".id")
			}
			before := findByID(previousProviders, id)
			kind := text(given["kind"])
			info, ok := providerInfo(kinds, kind)
			var settings Object
			var env Object
			if !ok {
				if before == nil || before["kind"] != kind {
					return bad(at + ".kind")
				}
				settings = cloneMap(object(before["settings"]))
				env = cloneMap(object(before["env"]))
			} else {
				settings = storedProviderSecrets(kind, given["settings"], before, info)
				if validateSettings != nil {
					if err := validateSettings(kind, cloneMap(settings)); err != nil {
						return err
					}
				}
				if info.Program {
					var beforeEnv Object
					if before != nil && before["kind"] == kind {
						beforeEnv = object(before["env"])
					}
					var err error
					env, err = secretPairs(given["env"], validProviderEnv, beforeEnv, at+".env")
					if err != nil {
						return err
					}
				}
			}
			headers, err := secretPairs(given["headers"], headerName.MatchString, object(object(before["request"])["headers"]), at+".headers")
			if err != nil {
				return err
			}
			extra, ok := fallback(given["extraBody"], Object{}).(map[string]any)
			if !ok {
				return bad(at + ".extraBody")
			}
			encoded, err := jsonutil.Encode(extra)
			if err != nil || length(string(encoded)) > 10000 {
				return bad(at + ".extraBody")
			}
			timeout := given["timeoutSeconds"]
			if timeout != nil {
				value, ok := number(timeout)
				if !ok || math.Trunc(value) != value || value < 10 || value > 3600 {
					return bad(at + ".timeoutSeconds")
				}
			}
			request := Object{}
			if len(headers) > 0 {
				request["headers"] = headers
			}
			if len(extra) > 0 {
				request["extraBody"] = extra
			}
			if timeout != nil {
				request["timeoutSeconds"] = timeout
			}
			name := textutil.Trim(text(given["name"]))
			if name == "" {
				name = kind
			}
			provider := Object{"id": id, "kind": kind, "name": name, "settings": settings}
			if len(request) > 0 {
				provider["request"] = request
			}
			if len(env) > 0 {
				provider["env"] = env
			}
			providers = append(providers, provider)
		}
		rawModels, ok := body["models"].([]any)
		if !ok {
			return bad("models")
		}
		models := []any{}
		ids := map[string]bool{}
		for index, raw := range rawModels {
			given := object(raw)
			at := "models[" + strconv.Itoa(index) + "]"
			id := text(given["id"])
			if id == "" || ids[id] {
				return bad(at + ".id")
			}
			ids[id] = true
			provider := text(given["provider"])
			if findByID(providers, provider) == nil {
				return bad(at + ".provider")
			}
			model := textutil.Trim(text(given["model"]))
			if model == "" {
				return bad(at + ".model")
			}
			size := given["contextTokens"]
			if size != nil {
				value, ok := number(size)
				if !ok || math.Trunc(value) != value || value < 4096 {
					return bad(at + ".contextTokens")
				}
			}
			note := textutil.Trim(text(given["note"]))
			units := utf16.Encode([]rune(note))
			if len(units) > 200 {
				note = string(utf16.Decode(units[:200]))
			}
			most := given["maxTokens"]
			if most != nil {
				value, ok := number(most)
				if !ok || math.Trunc(value) != value || value < 256 || value > 1000000 {
					return bad(at + ".maxTokens")
				}
			}
			price, err := parsePrice(given["price"], at+".price")
			if err != nil {
				return err
			}
			var accepted []any
			if raw := given["efforts"]; raw != nil {
				var ok bool
				accepted, ok = raw.([]any)
				if !ok {
					return bad(at + ".efforts")
				}
				for _, raw := range accepted {
					value, ok := raw.(string)
					if !ok || !hasString(efforts, value) {
						return bad(at + ".efforts")
					}
				}
			}
			entry := Object{"id": id, "provider": provider, "model": model}
			if price != nil {
				entry["price"] = price
			}
			if size != nil {
				entry["contextTokens"] = size
			}
			if note != "" {
				entry["note"] = note
			}
			if given["images"] == false {
				entry["images"] = false
			}
			if most != nil {
				entry["maxTokens"] = most
			}
			if _, ok := given["efforts"].([]any); ok {
				ordered := []any{}
				for _, effort := range efforts {
					for _, item := range accepted {
						if item == effort {
							ordered = append(ordered, effort)
							break
						}
					}
				}
				entry["efforts"] = ordered
			}
			models = append(models, entry)
		}
		roleConfig := Object{}
		givenRoles := object(body["roles"])
		for _, role := range roles {
			given := object(givenRoles[role])
			entry := Object{"model": nil, "effort": nil}
			for _, field := range []string{"model", "effort"} {
				raw := given[field]
				if raw != nil {
					value, ok := raw.(string)
					if !ok || field == "effort" && !validRoleEffort(role, value) || field != "effort" && !ids[value] {
						return bad("roles." + role + "." + field)
					}
					entry[field] = value
				}
			}
			if entry["model"] != nil && entry["effort"] == defaultEffort {
				entry["effort"] = nil
			}
			roleConfig[role] = entry
		}
		config["models"] = Object{"providers": providers, "models": models, "roles": roleConfig, "showCost": body["showCost"] == true}
		changed := map[string]bool{}
		registered := map[string]bool{}
		for _, raw := range providers {
			provider := object(raw)
			id := text(provider["id"])
			if _, known := providerInfo(kinds, text(provider["kind"])); known {
				registered[id] = true
			}
			before := findByID(previousProviders, id)
			beforeData, _ := jsonutil.Encode([]any{before["kind"], before["settings"], before["request"], before["env"]})
			afterData, _ := jsonutil.Encode([]any{provider["kind"], provider["settings"], provider["request"], provider["env"]})
			if string(beforeData) != string(afterData) {
				changed[id] = true
			}
		}
		changedModels = []any{}
		for _, raw := range models {
			model := object(raw)
			exists := false
			for _, oldRaw := range list(old["models"]) {
				before := object(oldRaw)
				if before["provider"] == model["provider"] && before["model"] == model["model"] {
					exists = true
					break
				}
			}
			id := text(model["provider"])
			if !registered[id] {
				continue
			}
			if !exists || changed[id] {
				delete(changed, id)
				changedModels = append(changedModels, model)
			}
		}
		return nil
	})
	return changedModels, err
}
func (store *Store) PublicModels(kinds []sdk.ModelProviderInfo) Object {
	models, _ := clone(object(store.snapshot()["models"]))
	providers := []any{}
	for _, raw := range list(models["providers"]) {
		provider := cloneMap(object(raw))
		info, known := providerInfo(kinds, text(provider["kind"]))
		settings := cloneMap(object(provider["settings"]))
		set := []string{}
		for _, key := range secretFields(info) {
			if truthy(settings[key]) {
				set = append(set, key)
			}
			delete(settings, key)
		}
		if !known {
			settings = Object{}
		} // A removed plugin cannot tell us which fields are secret.
		request := object(provider["request"])
		delete(provider, "request")
		provider["settings"] = settings
		provider["secretsSet"] = set
		provider["headers"] = names(object(request["headers"]))
		provider["env"] = names(object(provider["env"]))
		provider["extraBody"] = fallback(request["extraBody"], Object{})
		provider["timeoutSeconds"] = request["timeoutSeconds"]
		providers = append(providers, provider)
	}
	return Object{"kinds": kinds, "providers": providers, "models": models["models"], "roles": models["roles"], "showCost": models["showCost"]}
}
func effective(info sdk.ModelProviderInfo, stored Object) Object {
	settings := Object{}
	for key, field := range info.Settings.Properties {
		if field.Default != nil {
			settings[key] = field.Default
		}
	}
	for key, value := range stored {
		if value != nil && value != "" {
			settings[key] = value
		}
	}
	return settings
}

type DraftProvider struct {
	Info     sdk.ModelProviderInfo
	Settings Object
	Headers  Object
	Hint     string
}

func (store *Store) DraftProvider(input any, kinds []sdk.ModelProviderInfo, validate ...func(string, Object) error) (DraftProvider, error) {
	var validateSettings func(string, Object) error
	if len(validate) > 0 {
		validateSettings = validate[0]
	}
	config := store.snapshot()
	given := object(input)
	kind := text(given["kind"])
	info, ok := providerInfo(kinds, kind)
	if !ok {
		return DraftProvider{}, bad("provider.kind")
	}
	before := findByID(list(object(config["models"])["providers"]), text(given["id"]))
	settings, _ := clone(effective(info, storedProviderSecrets(kind, given["settings"], before, info)))
	for _, key := range info.Settings.Required {
		if _, exists := settings[key]; !exists {
			return DraftProvider{}, bad("provider.settings." + key)
		}
	}
	if validateSettings != nil {
		if err := validateSettings(kind, cloneMap(settings)); err != nil {
			return DraftProvider{}, err
		}
	}
	headers := Object{}
	if before["kind"] == kind {
		headers = cloneMap(object(object(before["request"])["headers"]))
	}
	var env Object
	if info.Program {
		if rawEnv, exists := given["env"]; exists && rawEnv != nil {
			var beforeEnv Object
			if before != nil && before["kind"] == kind {
				beforeEnv = object(before["env"])
			}
			var err error
			env, err = secretPairs(rawEnv, validProviderEnv, beforeEnv, "provider.env")
			if err != nil {
				return DraftProvider{}, err
			}
		} else if before != nil && before["kind"] == kind {
			env = cloneMap(object(before["env"]))
		}
	}
	hint := []string{kind}
	secrets := secretFields(info)
	for _, key := range names(settings) {
		if value, ok := settings[key].(string); ok && !hasString(secrets, key) {
			hint = append(hint, value)
		}
	}
	if info.Program {
		if len(env) > 0 {
			settings["env"] = env
		} else if _, inSchema := info.Settings.Properties["env"]; !inSchema {
			delete(settings, "env")
		}
	}
	return DraftProvider{Info: info, Settings: settings, Headers: headers, Hint: strings.Join(hint, " ")}, nil
}
