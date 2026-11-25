package settings

import (
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/sdk"
	"github.com/google/uuid"
)

const maxWebhooks = 32

var webhookIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type VAPIDConfig struct {
	PublicKey  string `json:"publicKey"`
	PrivateKey string `json:"privateKey"`
	Subject    string `json:"subject"`
}

type WebhookConfig struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	URL     string            `json:"url"`
	Enabled bool              `json:"enabled"`
	Secret  string            `json:"secret,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

type NotifierConfig struct {
	Enabled bool   `json:"enabled"`
	Values  Object `json:"values,omitempty"`
}

type NotificationConfig struct {
	Enabled        bool                      `json:"enabled"`
	WebPushEnabled bool                      `json:"webPushEnabled"`
	VAPID          VAPIDConfig               `json:"vapid"`
	Webhooks       []WebhookConfig           `json:"webhooks"`
	Notifiers      map[string]NotifierConfig `json:"notifiers"`
}

type PublicWebhookView struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	URL       string   `json:"url"`
	Enabled   bool     `json:"enabled"`
	SecretSet bool     `json:"secretSet"`
	Headers   []string `json:"headers"`
}

type PublicNotifierServiceView struct {
	Info       sdk.NotifierProviderInfo
	Enabled    bool
	Values     Object
	SecretsSet []string
	Ready      bool
}

type PublicNotificationView struct {
	Enabled        bool
	WebPushEnabled bool
	VAPIDPublicKey string
	Webhooks       []PublicWebhookView
	Notifiers      []PublicNotifierServiceView
}

func notifierInfo(kinds []sdk.NotifierProviderInfo, kind string) (sdk.NotifierProviderInfo, bool) {
	for _, info := range kinds {
		if info.Kind == kind {
			return info, true
		}
	}
	return sdk.NotifierProviderInfo{}, false
}

func notificationBool(obj Object, key string, fallbackValue bool) bool {
	if raw, exists := obj[key]; exists {
		if value, ok := raw.(bool); ok {
			return value
		}
	}
	return fallbackValue
}

func decodeWebhookObject(entry Object) WebhookConfig {
	headers := map[string]string{}
	for key, raw := range object(entry["headers"]) {
		if value, ok := raw.(string); ok {
			headers[key] = value
		}
	}
	if len(headers) == 0 {
		headers = nil
	}
	return WebhookConfig{
		ID:      text(entry["id"]),
		Name:    text(entry["name"]),
		URL:     text(entry["url"]),
		Enabled: notificationBool(entry, "enabled", true),
		Secret:  text(entry["secret"]),
		Headers: headers,
	}
}

func encodeWebhookObject(item WebhookConfig) Object {
	out := Object{
		"id":      item.ID,
		"name":    item.Name,
		"url":     item.URL,
		"enabled": item.Enabled,
	}
	if item.Secret != "" {
		out["secret"] = item.Secret
	}
	if len(item.Headers) > 0 {
		headers := Object{}
		for key, value := range item.Headers {
			headers[key] = value
		}
		out["headers"] = headers
	}
	return out
}

func decodeNotifications(config Object) NotificationConfig {
	section := object(config["notifications"])
	vapidObj := object(section["vapid"])
	out := NotificationConfig{
		Enabled:        notificationBool(section, "enabled", true),
		WebPushEnabled: notificationBool(section, "webPushEnabled", true),
		VAPID: VAPIDConfig{
			PublicKey:  text(vapidObj["publicKey"]),
			PrivateKey: text(vapidObj["privateKey"]),
			Subject:    text(vapidObj["subject"]),
		},
		Webhooks:  []WebhookConfig{},
		Notifiers: map[string]NotifierConfig{},
	}
	for _, raw := range list(section["webhooks"]) {
		item := decodeWebhookObject(object(raw))
		if item.ID != "" && item.URL != "" {
			out.Webhooks = append(out.Webhooks, item)
		}
	}
	for kind, raw := range object(section["notifiers"]) {
		entry := object(raw)
		out.Notifiers[kind] = NotifierConfig{
			Enabled: notificationBool(entry, "enabled", false),
			Values:  cloneMap(object(entry["values"])),
		}
	}
	return out
}

func (store *Store) Notifications() NotificationConfig {
	return decodeNotifications(store.snapshot())
}

// EnsureVAPID returns the persisted VAPID keypair, generating and storing one once when absent.
func (store *Store) EnsureVAPID(generate func() (VAPIDConfig, error)) (VAPIDConfig, error) {
	current := store.Notifications().VAPID
	if current.PublicKey != "" && current.PrivateKey != "" {
		return current, nil
	}
	var result VAPIDConfig
	err := store.update(func(config Object) error {
		existing := decodeNotifications(config).VAPID
		if existing.PublicKey != "" && existing.PrivateKey != "" {
			result = existing
			return nil
		}
		generated, err := generate()
		if err != nil {
			return err
		}
		section := cloneMap(object(config["notifications"]))
		section["vapid"] = Object{
			"publicKey":  generated.PublicKey,
			"privateKey": generated.PrivateKey,
			"subject":    generated.Subject,
		}
		config["notifications"] = section
		result = generated
		return nil
	})
	return result, err
}

func normalizeWebhookInput(entry Object, previous Object, at string, generateID bool) (WebhookConfig, error) {
	id := textutil.Trim(text(entry["id"]))
	if id == "" {
		if !generateID {
			id = "wh_draft"
		} else {
			id = "wh_" + uuid.NewString()
		}
	}
	if !webhookIDPattern.MatchString(id) {
		return WebhookConfig{}, bad(at + ".id")
	}
	address := textutil.Trim(text(entry["url"]))
	parsed, err := url.Parse(address)
	if err != nil || len(address) > 2048 || strings.ContainsRune(address, 0) ||
		parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return WebhookConfig{}, bad(at + ".url")
	}
	name := textutil.Trim(text(entry["name"]))
	if len(name) > 120 || strings.ContainsRune(name, 0) {
		return WebhookConfig{}, bad(at + ".name")
	}
	if name == "" {
		name = parsed.Host
	}
	enabled := notificationBool(entry, "enabled", true)

	secret := text(previous["secret"])
	if clear, ok := entry["clearSecret"].(bool); ok && clear {
		secret = ""
	}
	if rawSecret, exists := entry["secret"]; exists && rawSecret != nil {
		secretText, ok := rawSecret.(string)
		if !ok {
			return WebhookConfig{}, bad(at + ".secret")
		}
		secretText = textutil.Trim(secretText)
		if len(secretText) > 4096 || strings.ContainsRune(secretText, 0) {
			return WebhookConfig{}, bad(at + ".secret")
		}
		if secretText != "" {
			secret = secretText
		}
	}

	var headers map[string]string
	if rawHeaders, exists := entry["headers"]; exists {
		merged, err := secretPairs(rawHeaders, headerName.MatchString, object(previous["headers"]), at+".headers")
		if err != nil {
			return WebhookConfig{}, err
		}
		if len(merged) > 0 {
			headers = make(map[string]string, len(merged))
			for key, rawValue := range merged {
				if value, ok := rawValue.(string); ok {
					headers[key] = value
				}
			}
		}
	} else {
		for key, rawValue := range object(previous["headers"]) {
			if value, ok := rawValue.(string); ok {
				if headers == nil {
					headers = map[string]string{}
				}
				headers[key] = value
			}
		}
	}

	return WebhookConfig{
		ID:      id,
		Name:    name,
		URL:     address,
		Enabled: enabled,
		Secret:  secret,
		Headers: headers,
	}, nil
}

func mergeNotifierValues(before Object, edits Object, info sdk.NotifierProviderInfo, validate func(string, Object) error) (Object, error) {
	service := cloneMap(before)
	for _, key := range names(edits) {
		field, exists := info.Settings.Properties[key]
		if !exists {
			return nil, bad(key)
		}
		raw := edits[key]
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
		if err := validate(info.Kind, effectiveSearchValues(info.Settings, service)); err != nil {
			return nil, err
		}
	}
	return service, nil
}

func (store *Store) UpdateNotifications(input Object, kinds []sdk.NotifierProviderInfo, validate func(string, Object) error) error {
	return store.update(func(config Object) error {
		before := object(config["notifications"])
		next := cloneMap(before)
		next["enabled"] = notificationBool(input, "enabled", notificationBool(before, "enabled", true))
		next["webPushEnabled"] = notificationBool(input, "webPushEnabled", notificationBool(before, "webPushEnabled", true))

		if rawWebhooks, exists := input["webhooks"]; exists {
			items, ok := rawWebhooks.([]any)
			if !ok || len(items) > maxWebhooks {
				return bad("webhooks")
			}
			prevByID := map[string]Object{}
			for _, raw := range list(before["webhooks"]) {
				entry := object(raw)
				if id := text(entry["id"]); id != "" {
					prevByID[id] = entry
				}
			}
			seen := map[string]bool{}
			encoded := make([]any, 0, len(items))
			for index, raw := range items {
				entry := object(raw)
				at := "webhooks[" + strconv.Itoa(index) + "]"
				prev := prevByID[textutil.Trim(text(entry["id"]))]
				normalized, err := normalizeWebhookInput(entry, prev, at, true)
				if err != nil {
					return err
				}
				if seen[normalized.ID] {
					return bad(at + ".id")
				}
				seen[normalized.ID] = true
				encoded = append(encoded, encodeWebhookObject(normalized))
			}
			next["webhooks"] = encoded
		}

		if rawNotifiers, exists := input["notifiers"]; exists {
			edits, ok := rawNotifiers.(map[string]any)
			if !ok {
				return bad("notifiers")
			}
			storedNotifiers, err := clone(object(before["notifiers"]))
			if err != nil {
				return err
			}
			for _, kind := range names(edits) {
				info, ok := notifierInfo(kinds, kind)
				if !ok {
					return bad("notifiers." + kind)
				}
				entry, ok := edits[kind].(map[string]any)
				if !ok {
					return bad("notifiers." + kind)
				}
				prevEntry := object(storedNotifiers[kind])
				enabled := notificationBool(entry, "enabled", notificationBool(prevEntry, "enabled", false))
				values := cloneMap(object(prevEntry["values"]))
				if rawValues, hasValues := entry["values"]; hasValues && rawValues != nil {
					valueMap, ok := rawValues.(map[string]any)
					if !ok {
						return bad("notifiers." + kind + ".values")
					}
					merged, err := mergeNotifierValues(values, valueMap, info, validate)
					if err != nil {
						return err
					}
					values = merged
				}
				storedNotifiers[kind] = Object{
					"enabled": enabled,
					"values":  values,
				}
			}
			next["notifiers"] = storedNotifiers
		}

		config["notifications"] = next
		return nil
	})
}

// WebhookDraft resolves an unsaved webhook draft against any stored secrets for the same id.
func (store *Store) WebhookDraft(input Object) (WebhookConfig, error) {
	before := object(store.snapshot()["notifications"])
	var prev Object
	if id := textutil.Trim(text(input["id"])); id != "" {
		for _, raw := range list(before["webhooks"]) {
			entry := object(raw)
			if text(entry["id"]) == id {
				prev = entry
				break
			}
		}
	}
	return normalizeWebhookInput(input, prev, "webhook", false)
}

// NotifierDraft resolves an unsaved notifier draft against stored secrets and schema defaults.
func (store *Store) NotifierDraft(kind string, input Object, kinds []sdk.NotifierProviderInfo, validate func(string, Object) error) (Object, error) {
	info, ok := notifierInfo(kinds, kind)
	if !ok {
		return nil, bad("notifier")
	}
	before := object(object(object(store.snapshot()["notifications"])["notifiers"])[kind])
	storedValues := cloneMap(object(before["values"]))
	edits := object(input["values"])
	merged, err := mergeNotifierValues(storedValues, edits, info, validate)
	if err != nil {
		return nil, err
	}
	return effectiveSearchValues(info.Settings, merged), nil
}

// EffectiveNotifierSettings returns whether a notifier is enabled and its resolved settings.
func (store *Store) EffectiveNotifierSettings(info sdk.NotifierProviderInfo) (bool, Object) {
	entry := object(object(object(store.snapshot()["notifications"])["notifiers"])[info.Kind])
	enabled := notificationBool(entry, "enabled", false)
	return enabled, effectiveSearchValues(info.Settings, object(entry["values"]))
}

// ActiveNotifierKinds returns the sorted kinds of notification providers currently enabled.
func (store *Store) ActiveNotifierKinds() []string {
	cfg := store.Notifications()
	out := []string{}
	for kind, entry := range cfg.Notifiers {
		if entry.Enabled {
			out = append(out, kind)
		}
	}
	slices.Sort(out)
	return out
}

// PublicNotifications returns the browser-safe notification settings view without any secrets.
func (store *Store) PublicNotifications(kinds []sdk.NotifierProviderInfo, ready func(string, Object) bool) PublicNotificationView {
	cfg := store.Notifications()
	webhooks := make([]PublicWebhookView, 0, len(cfg.Webhooks))
	for _, hook := range cfg.Webhooks {
		headerNames := make([]string, 0, len(hook.Headers))
		for key := range hook.Headers {
			headerNames = append(headerNames, key)
		}
		slices.Sort(headerNames)
		webhooks = append(webhooks, PublicWebhookView{
			ID:        hook.ID,
			Name:      hook.Name,
			URL:       hook.URL,
			Enabled:   hook.Enabled,
			SecretSet: hook.Secret != "",
			Headers:   headerNames,
		})
	}
	notifiers := make([]PublicNotifierServiceView, 0, len(kinds))
	for _, info := range kinds {
		storedEntry := cfg.Notifiers[info.Kind]
		stored := storedEntry.Values
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
		notifiers = append(notifiers, PublicNotifierServiceView{
			Info:       info,
			Enabled:    storedEntry.Enabled,
			Values:     values,
			SecretsSet: secretsSet,
			Ready:      isReady,
		})
	}
	return PublicNotificationView{
		Enabled:        cfg.Enabled,
		WebPushEnabled: cfg.WebPushEnabled,
		VAPIDPublicKey: cfg.VAPID.PublicKey,
		Webhooks:       webhooks,
		Notifiers:      notifiers,
	}
}
