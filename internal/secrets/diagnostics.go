package secrets

import (
	"bytes"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
)

const hidden = "[redacted]"

var (
	keySeparators = strings.NewReplacer("-", "", "_", "", ".", "")
	privateKey    = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)
	authLine      = regexp.MustCompile(`(?im)\b((?:proxy-)?authorization|set-cookie|cookie)\s*:\s*[^\r\n]+`)
	bearer        = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/-]+=*`)
	assignment    = regexp.MustCompile(`(?i)((?:\b(?:api[-_]?key|access[-_]?token|refresh[-_]?token|client[-_]?secret|auth[-_]?token|password|token)["']?\s*[:=]\s*|--(?:api[-_]?key|access[-_]?token|client[-_]?secret|password|token)\s+))(?:(?:"[^"\r\n]*")|(?:'[^'\r\n]*')|[^\s&#,;"'<>]+)`)
	token         = regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{18,}|github_pat_[A-Za-z0-9_]{18,}|sk-[A-Za-z0-9_-]{16,}|eyJ[A-Za-z0-9_-]+\.eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+)\b`)
	urlAuth       = regexp.MustCompile(`(?i)(https?://)[^\s/@]+:[^\s/@]+@`)
)

func SensitiveKey(key string) bool {
	key = keySeparators.Replace(strings.ToLower(key))
	switch key {
	case "authorization", "proxyauthorization", "cookie", "setcookie", "password", "passphrase", "apikey", "xapikey", "secret", "clientsecret", "token", "accesstoken", "refreshtoken", "idtoken", "authtoken", "sessiontoken", "privatekey", "signature", "credential", "credentials":
		return true
	}
	return false
}

// ConfiguredValues finds values that may be repeated verbatim in error output.
func ConfiguredValues(config map[string]any) []string {
	var out []string
	var collect func(any, bool)
	collect = func(value any, sensitive bool) {
		switch value := value.(type) {
		case map[string]any:
			for k, v := range value {
				collect(v, sensitive || SensitiveKey(k) || k == "headers" || k == "env")
			}
		case []any:
			for _, v := range value {
				collect(v, sensitive)
			}
		case string:
			if sensitive && len(value) >= 4 {
				out = append(out, value)
			}
		}
	}
	collect(config, false)
	return out
}

type DiagnosticRedactor struct{ configured *strings.Replacer }

func NewDiagnosticRedactor(values []string) DiagnosticRedactor {
	values = slices.Clone(values)
	slices.SortFunc(values, func(a, b string) int { return len(b) - len(a) })
	pairs := make([]string, 0, 2*len(values))
	for _, value := range values {
		if value != "" {
			pairs = append(pairs, value, hidden)
		}
	}
	return DiagnosticRedactor{configured: strings.NewReplacer(pairs...)}
}

func (r DiagnosticRedactor) text(s string) string {
	if s == "" {
		return s
	}
	s = r.configured.Replace(s)
	s = privateKey.ReplaceAllString(s, hidden)
	s = authLine.ReplaceAllString(s, "${1}: "+hidden)
	s = bearer.ReplaceAllString(s, "Bearer "+hidden)
	s = assignment.ReplaceAllString(s, "${1}"+hidden)
	s = token.ReplaceAllString(s, hidden)
	return urlAuth.ReplaceAllString(s, "${1}"+hidden+"@")
}

func (r DiagnosticRedactor) value(value any, nestedJSON bool) any {
	switch value := value.(type) {
	case map[string]any:
		redacted, _ := value["redacted"].(bool)
		for key, v := range value {
			if SensitiveKey(key) || key == "headers" || key == "env" || redacted && key == "text" {
				value[key] = hidden
			} else {
				value[key] = r.value(v, nestedJSON)
			}
		}
		return value
	case []any:
		for i, v := range value {
			value[i] = r.value(v, nestedJSON)
		}
		return value
	case string:
		trimmed := strings.TrimSpace(value)
		if nestedJSON && (strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")) && json.Valid([]byte(trimmed)) {
			if safe, err := r.json([]byte(trimmed), false); err == nil {
				return string(safe)
			}
		}
		return r.text(value)
	}
	return value
}

func (r DiagnosticRedactor) json(raw []byte, nestedJSON bool) (json.RawMessage, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return json.Marshal(r.value(value, nestedJSON))
}

func (r DiagnosticRedactor) JSON(raw []byte) (json.RawMessage, error) {
	return r.json(raw, true)
}

func (r DiagnosticRedactor) Text(text string) string {
	return r.value(text, true).(string)
}
