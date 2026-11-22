package modelhttp

import (
	"net/url"
	"strings"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/textutil"
)

// ValidateHTTPSettings checks baseUrl and apiKey for HTTP-based model providers.
func ValidateHTTPSettings(values map[string]any) error {
	if raw, exists := values["baseUrl"]; exists && raw != nil {
		address, ok := raw.(string)
		if !ok {
			return domainerr.New("error.invalidBody", map[string]any{"reason": "baseUrl"})
		}
		trimmed := textutil.Trim(address)
		if trimmed != "" {
			if len(trimmed) > 2048 || strings.ContainsAny(trimmed, "\x00\r\n") {
				return domainerr.New("error.invalidBody", map[string]any{"reason": "baseUrl"})
			}
			parsed, err := url.Parse(trimmed)
			if err != nil || parsed.Hostname() == "" || parsed.Opaque != "" || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
				parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
				return domainerr.New("error.invalidBody", map[string]any{"reason": "baseUrl"})
			}
		}
	}
	if raw, exists := values["apiKey"]; exists && raw != nil {
		key, ok := raw.(string)
		if !ok || len(key) > 32768 || strings.ContainsAny(key, "\x00\r\n") {
			return domainerr.New("error.invalidBody", map[string]any{"reason": "apiKey"})
		}
	}
	return nil
}
