package settings

import (
	"strings"

	"github.com/daidr/Ariadne/internal/textutil"
)

const ReferenceSecret = "reference."

func ReferenceSecretPrefix(reference string) string { return ReferenceSecret + reference + "." }
func (store *Store) SourceSecrets(project string) Object {
	return cloneMap(object(object(store.snapshot()["sourceSecrets"])[project]))
}
func (store *Store) SourceSecretsSet(project string) []string {
	result := []string{}
	for _, key := range names(store.SourceSecrets(project)) {
		if !strings.HasPrefix(key, ReferenceSecret) {
			result = append(result, key)
		}
	}
	return result
}

// SplitSourceSettings receives secret field names from the context provider's
// schema, never from untrusted settings submitted by its caller.
func (store *Store) SplitSourceSettings(project, kind string, settings Object, previousKind, prefix string, secretFields []string) (Object, Object) {
	before := Object{}
	if previousKind == kind {
		before = store.SourceSecrets(project)
	}
	plain := cloneMap(settings)
	secrets := Object{}
	for _, key := range secretFields {
		value, exists := plain[key]
		delete(plain, key)
		if exists && value == nil {
			continue
		}
		if valueText, ok := value.(string); ok && textutil.Trim(valueText) != "" {
			secrets[prefix+key] = textutil.Trim(valueText)
		} else if kept := text(before[prefix+key]); kept != "" {
			secrets[prefix+key] = kept
		}
	}
	return plain, secrets
}
func (store *Store) SaveSourceSecrets(project string, secrets Object) error {
	return store.update(func(config Object) error {
		all := object(config["sourceSecrets"])
		merged := Object{}
		for key, value := range object(all[project]) {
			if strings.HasPrefix(key, ReferenceSecret) {
				merged[key] = value
			}
		}
		for key, value := range secrets {
			merged[key] = value
		}
		if len(merged) > 0 {
			all[project] = merged
		} else {
			delete(all, project)
		}
		config["sourceSecrets"] = all
		return nil
	})
}
func (store *Store) SaveReferenceSecrets(project, reference string, secrets Object) error {
	prefix := ReferenceSecretPrefix(reference)
	return store.update(func(config Object) error {
		all := object(config["sourceSecrets"])
		merged := Object{}
		for key, value := range object(all[project]) {
			if !strings.HasPrefix(key, prefix) {
				merged[key] = value
			}
		}
		for key, value := range secrets {
			merged[prefix+key] = value
		}
		if len(merged) > 0 {
			all[project] = merged
		} else {
			delete(all, project)
		}
		config["sourceSecrets"] = all
		return nil
	})
}
