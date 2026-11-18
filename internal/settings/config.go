// Package settings stores private application configuration independently of
// the event log. Public views explicitly remove provider and project secrets.
package settings

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/textutil"
)

type Object = map[string]any

const MaxConfigBytes = 16 << 20

type Rule struct {
	Pattern string `json:"pattern"`
	Action  string `json:"action"`
}

var ShortcutActions = []string{"search", "focus", "previousThread", "nextThread", "stop", "sheet"}
var defaultKeys = Object{"search": "mod+k", "focus": "/", "previousThread": "mod+up", "nextThread": "mod+down", "stop": "escape", "sheet": "?"}
var defaultLimits = Object{"commandTimeout": float64(120), "commandTimeoutMax": float64(600), "commandOutput": float64(30000), "compactShare": .6, "modelRetries": float64(4), "loopRepeats": float64(4)}
var limitRanges = map[string][2]float64{"commandTimeout": {5, 3600}, "commandTimeoutMax": {5, 7200}, "commandOutput": {2000, 200000}, "compactShare": {.3, .9}, "modelRetries": {0, 10}, "loopRepeats": {2, 20}}
var limitKeys = []string{"commandTimeout", "commandTimeoutMax", "commandOutput", "compactShare", "modelRetries", "loopRepeats"}
var roles = []string{"thread", "coordinator", "triage", "research"}
var efforts = []string{"low", "medium", "high", "xhigh", "max"}
var snippetID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var headerName = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+.^_`|~-]+$")
var hotkey = regexp.MustCompile("^((mod|meta|ctrl|alt|shift)\\+){0,4}([a-z0-9]|f([1-9]|1[0-2])|escape|enter|tab|space|backspace|delete|up|down|left|right|home|end|pageup|pagedown|[`~!@#$%^&*()\\-_=+\\[\\]{}\\\\|;:'\",.<>/?])$")

func bad(reason string) error { return domainerr.New("error.invalidBody", Object{"reason": reason}) }
func object(raw any) Object {
	if value, ok := raw.(map[string]any); ok {
		return value
	}
	return Object{}
}
func list(raw any) []any {
	if value, ok := raw.([]any); ok {
		return value
	}
	return []any{}
}
func text(raw any) string     { value, _ := raw.(string); return value }
func length(value string) int { return len(utf16.Encode([]rune(value))) }
func hasString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
func cloneMap(value Object) Object {
	result := Object{}
	for key, item := range value {
		result[key] = item
	}
	return result
}
func clone(value Object) (Object, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var result Object
	err = json.Unmarshal(data, &result)
	return result, err
}
func fallback(value, otherwise any) any {
	if value == nil {
		return otherwise
	}
	return value
}
func number(raw any) (float64, bool) {
	value, ok := raw.(float64)
	return value, ok && !math.IsNaN(value) && !math.IsInf(value, 0)
}
func DefaultRules() []any {
	rules := []any{}
	for _, pattern := range []string{"git push*", "npm publish*", "pnpm publish*", "yarn publish*", "bun publish*", "cargo publish*", "gh pr merge*", "gh release create*"} {
		rules = append(rules, Object{"pattern": pattern, "action": "ask"})
	}
	return rules
}
func Defaults() Object {
	roleConfig := Object{}
	for _, role := range roles {
		roleConfig[role] = Object{"model": nil, "effort": nil, "fallback": nil}
	}
	return Object{"locale": "zh-CN", "plugins": []any{}, "models": Object{"providers": []any{}, "models": []any{}, "roles": roleConfig, "showCost": false}, "concurrency": Object{"slots": float64(3), "reserved": float64(1)}, "sourceSecrets": Object{}, "mcp": Object{"servers": []any{}}, "input": Object{"submitOn": "enter", "snippets": []any{}, "keys": cloneMap(defaultKeys)}, "instructions": "", "commands": Object{"rules": DefaultRules()}, "limits": cloneMap(defaultLimits), "projectEnv": Object{}, "search": Object{"provider": "tavily", "services": Object{"tavily": Object{"apiKey": "", "baseUrl": ""}, "deepseek": Object{"apiKey": "", "baseUrl": ""}}}}
}
func parseKeys(raw any, strict bool) (Object, error) {
	given := object(raw)
	keys := cloneMap(defaultKeys)
	for _, action := range ShortcutActions {
		value, exists := given[action]
		if !exists {
			continue
		}
		key, ok := value.(string)
		key = textutil.Lower(textutil.Trim(key))
		if ok && hotkey.MatchString(key) {
			keys[action] = key
		} else if strict {
			return nil, bad("keys." + action)
		}
	}
	if strict {
		seen := map[string]bool{}
		for _, action := range ShortcutActions {
			key := keys[action].(string)
			if seen[key] {
				return nil, bad("keys")
			}
			seen[key] = true
		}
	}
	return keys, nil
}
func storedSnippets(raw any) []any {
	out := []any{}
	seen := map[string]bool{}
	for _, value := range list(raw) {
		item := object(value)
		id, idOK := item["id"].(string)
		name, nameOK := item["name"].(string)
		text, textOK := item["text"].(string)
		if !idOK || !snippetID.MatchString(id) || seen[id] || !nameOK || !textOK || textutil.Trim(name) == "" || textutil.Trim(text) == "" {
			continue
		}
		seen[id] = true
		out = append(out, Object{"id": id, "name": name, "text": text})
	}
	return out
}
func parseRules(raw any, strict bool) ([]any, error) {
	items, ok := raw.([]any)
	if !ok {
		if strict {
			return nil, bad("rules")
		}
		return []any{}, nil
	}
	if strict && len(items) > 200 {
		return nil, bad("rules")
	}
	rules := []any{}
	for index, value := range items {
		item := object(value)
		pattern := textutil.Trim(text(item["pattern"]))
		action := text(item["action"])
		if pattern == "" || length(pattern) > 500 || !hasString([]string{"allow", "ask", "deny"}, action) {
			if strict {
				return nil, bad("rules[" + strconv.Itoa(index) + "]")
			}
			continue
		}
		rules = append(rules, Object{"pattern": pattern, "action": action})
	}
	return rules, nil
}
func parseLimits(raw any, strict bool) (Object, error) {
	given := object(raw)
	limits := cloneMap(defaultLimits)
	for _, key := range limitKeys {
		raw := given[key]
		if raw == nil {
			continue
		}
		value, ok := number(raw)
		bounds := limitRanges[key]
		if ok && value >= bounds[0] && value <= bounds[1] && (key == "compactShare" || value == math.Trunc(value)) {
			limits[key] = value
		} else if strict {
			return nil, bad("limits." + key)
		}
	}
	if limits["commandTimeout"].(float64) > limits["commandTimeoutMax"].(float64) {
		if strict {
			return nil, bad("limits.commandTimeout")
		}
		limits["commandTimeout"] = limits["commandTimeoutMax"]
	}
	return limits, nil
}
func storedSearch(raw any) Object {
	given := object(raw)
	services := object(given["services"])
	out := Object{}
	for _, kind := range []string{"tavily", "deepseek"} {
		service := object(services[kind])
		key, keyOK := service["apiKey"].(string)
		if !keyOK && kind == "tavily" {
			key = text(given["apiKey"])
		}
		out[kind] = Object{"apiKey": key, "baseUrl": text(service["baseUrl"])}
	}
	provider := "tavily"
	if given["provider"] == "deepseek" {
		provider = "deepseek"
	}
	return Object{"provider": provider, "services": out}
}
func Normalize(stored Object) Object {
	config := Defaults()
	for key, value := range stored {
		config[key] = value
	}
	models := object(stored["models"])
	roleConfig := object(models["roles"])
	normalizedRoles := Object{}
	for _, role := range roles {
		given := object(roleConfig[role])
		value := Object{"model": nil, "effort": nil, "fallback": nil}
		for _, field := range []string{"model", "fallback"} {
			if text, ok := given[field].(string); ok {
				value[field] = text
			}
		}
		if effort, ok := given["effort"].(string); ok && hasString(efforts, effort) {
			value["effort"] = effort
		}
		normalizedRoles[role] = value
	}
	config["models"] = Object{"providers": fallback(models["providers"], []any{}), "models": fallback(models["models"], []any{}), "roles": normalizedRoles, "showCost": models["showCost"] == true}
	concurrency := Object{"slots": float64(3), "reserved": float64(1)}
	for key, value := range object(stored["concurrency"]) {
		concurrency[key] = value
	}
	config["concurrency"] = concurrency
	config["sourceSecrets"] = cloneMap(object(stored["sourceSecrets"]))
	config["projectEnv"] = cloneMap(object(stored["projectEnv"]))
	config["mcp"] = Object{"servers": list(object(stored["mcp"])["servers"])}
	input := object(stored["input"])
	keys, _ := parseKeys(input["keys"], false)
	submit := "enter"
	if input["submitOn"] == "mod-enter" {
		submit = "mod-enter"
	}
	config["input"] = Object{"submitOn": submit, "snippets": storedSnippets(input["snippets"]), "keys": keys}
	config["instructions"] = text(stored["instructions"])
	commands := object(stored["commands"])
	rules := DefaultRules()
	if value, exists := commands["rules"]; exists {
		rules, _ = parseRules(value, false)
	}
	config["commands"] = Object{"rules": rules}
	config["limits"], _ = parseLimits(stored["limits"], false)
	config["search"] = storedSearch(stored["search"])
	return config
}
func Load(path string) (Object, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return Defaults(), nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxConfigBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxConfigBytes {
		return nil, errors.New("application settings exceed 16 MiB")
	}
	var stored Object
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, err
	}
	if stored == nil {
		return nil, errors.New("application settings must be an object")
	}
	return Normalize(stored), nil
}
func validProjectEnv(name string) bool {
	upper := strings.ToUpper(name)
	return envName.MatchString(name) && !strings.HasPrefix(upper, "GIT_CONFIG") && !strings.HasPrefix(upper, "ARIADNE_")
}
func secretPairs(raw any, valid func(string) bool, before Object, at string) (Object, error) {
	if raw == nil {
		return Object{}, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, bad(at)
	}
	out := Object{}
	for _, value := range items {
		pair := object(value)
		field := textutil.Trim(text(pair["name"]))
		if _, exists := out[field]; !valid(field) || exists {
			return nil, bad(at)
		}
		value := text(pair["value"])
		if value == "" {
			value = text(before[field])
		}
		if value == "" {
			return nil, bad(at + "." + field)
		}
		out[field] = value
	}
	return out, nil
}
