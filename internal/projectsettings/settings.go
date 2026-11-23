// Package projectsettings preserves project configuration and validation rules.
package projectsettings

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode/utf16"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/textutil"
)

const MaxInstructions = 16000
const MaxSetup = 8000
const MaxCheck = 2000
const MaxRoutines = 20
const MaxRoutinePrompt = 2000
const DefaultBranchPrefix = "ariadne/"
const MaxReviewRounds = 2

var ShellChoices = []string{"auto", "bash", "zsh", "sh", "pwsh", "powershell", "cmd"}
var prefixPattern = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
var slugPattern = regexp.MustCompile(`[^a-z0-9]+`)
var routineID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)
var clockPattern = regexp.MustCompile(`^([01][0-9]|2[0-3]):([0-5][0-9])$`)

type Delivery struct {
	Mode         string `json:"mode"`
	Instructions string `json:"instructions"`
}
type Auto struct {
	Setup bool `json:"setup"`
	Check bool `json:"check"`
}
type Workspace struct {
	Retention    string `json:"retention"`
	Auto         Auto   `json:"auto"`
	Setup        string `json:"setup"`
	Shell        string `json:"shell"`
	Check        string `json:"check"`
	BranchPrefix string `json:"branchPrefix"`
}
type Routine struct {
	ID      string `json:"id"`
	Prompt  string `json:"prompt"`
	Days    []int  `json:"days"`
	Time    string `json:"time"`
	Enabled bool   `json:"enabled"`
}
type Review struct {
	Enabled bool `json:"enabled"`
}
type Autopilot struct {
	Enabled bool   `json:"enabled"`
	Method  string `json:"method"`
}
type Settings struct {
	Instructions string    `json:"instructions"`
	Delivery     Delivery  `json:"delivery"`
	Tools        []string  `json:"tools"`
	Workspace    Workspace `json:"workspace"`
	Routines     []Routine `json:"routines"`
	Review       Review    `json:"review"`
	Autopilot    Autopilot `json:"autopilot"`
}

func Defaults() Settings {
	return Settings{
		Delivery: Delivery{Mode: "merge"}, Tools: []string{},
		Workspace: Workspace{Retention: "auto", Auto: Auto{Setup: true, Check: true}, Shell: "auto", BranchPrefix: DefaultBranchPrefix},
		Routines:  []Routine{}, Review: Review{Enabled: true}, Autopilot: Autopilot{Method: "squash"},
	}
}

func bad(reason string) error {
	return domainerr.New("error.invalidBody", map[string]any{"reason": reason})
}
func object(raw any) map[string]any {
	if value, ok := raw.(map[string]any); ok {
		return value
	}
	return map[string]any{}
}
func fallback(raw any, value any) any {
	if raw == nil {
		return value
	}
	return raw
}
func length(value string) int { return len(utf16.Encode([]rune(value))) }

// JS trim semantics include BOM but exclude U+0085; preserve the old validator.
func Trim(value string) string {
	return textutil.Trim(value)
}
func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
func number(raw any) (float64, bool) {
	switch value := raw.(type) {
	case float64:
		return value, true
	case int:
		return float64(value), true
	case int64:
		return float64(value), true
	case json.Number:
		n, err := value.Float64()
		return n, err == nil
	}
	return 0, false
}

func ValidBranchPrefix(prefix string) bool {
	if prefix == "" {
		return true
	}
	if len(prefix) > 40 || !prefixPattern.MatchString(prefix) || strings.HasPrefix(prefix, "/") || strings.HasPrefix(prefix, "-") || strings.Contains(prefix, "..") || strings.Contains(prefix, "//") || strings.HasSuffix(prefix, ".lock") || strings.HasSuffix(prefix, ".") {
		return false
	}
	parts := strings.Split(prefix, "/")
	for index, part := range parts {
		if index == len(parts)-1 && part == "" {
			continue
		}
		if strings.HasPrefix(part, ".") || strings.HasPrefix(part, "-") {
			return false
		}
	}
	return true
}

func BranchSlug(raw any) *string {
	value, ok := raw.(string)
	if !ok {
		return nil
	}
	slug := strings.Trim(slugPattern.ReplaceAllString(textutil.Lower(value), "-"), "-")
	if len(slug) > 40 {
		slug = slug[:40]
	}
	slug = strings.TrimRight(slug, "-")
	if slug == "" {
		return nil
	}
	return &slug
}

func ThreadBranch(prefix, id string, slug *string) string {
	if slug != nil && *slug != "" {
		return prefix + *slug + "-" + id[max(0, len(id)-6):]
	}
	return prefix + id[max(0, len(id)-12):]
}

func Parse(input any, allowedTools []string) (Settings, error) {
	// Native callers commonly pass settings read from the repository. Preserve
	// their fields just as a decoded JSON object would.
	if value, ok := input.(Settings); ok {
		data, err := json.Marshal(value)
		if err != nil {
			return Settings{}, err
		}
		var body map[string]any
		if err := json.Unmarshal(data, &body); err != nil {
			return Settings{}, err
		}
		input = body
	}
	body := object(input)
	out := Defaults()
	if raw, exists := body["instructions"]; exists {
		value, ok := raw.(string)
		if !ok || length(value) > MaxInstructions {
			return Settings{}, bad("instructions")
		}
		out.Instructions = Trim(value)
	}
	if raw, exists := body["delivery"]; exists {
		value := object(raw)
		mode, ok := fallback(value["mode"], "merge").(string)
		if !ok || mode != "merge" && mode != "external" {
			return Settings{}, bad("delivery.mode")
		}
		instructions, ok := fallback(value["instructions"], "").(string)
		if !ok || length(instructions) > MaxInstructions {
			return Settings{}, bad("delivery.instructions")
		}
		out.Delivery = Delivery{Mode: mode, Instructions: Trim(instructions)}
	}
	if raw, exists := body["tools"]; exists {
		values, ok := raw.([]any)
		if !ok {
			return Settings{}, bad("tools")
		}
		seen := make(map[string]bool)
		for _, raw := range values {
			name, ok := raw.(string)
			if !ok || !contains(allowedTools, name) {
				return Settings{}, bad("tools")
			}
			if !seen[name] {
				out.Tools = append(out.Tools, name)
				seen[name] = true
			}
		}
		sort.Strings(out.Tools)
	}
	if raw, exists := body["workspace"]; exists {
		value := object(raw)
		retention, ok := fallback(value["retention"], "auto").(string)
		if !ok || !contains([]string{"auto", "merged", "keep"}, retention) {
			return Settings{}, bad("workspace.retention")
		}
		setup, ok := fallback(value["setup"], "").(string)
		if !ok || length(setup) > MaxSetup {
			return Settings{}, bad("workspace.setup")
		}
		shell, ok := fallback(value["shell"], "auto").(string)
		if !ok || !contains(ShellChoices, shell) {
			return Settings{}, bad("workspace.shell")
		}
		check, ok := fallback(value["check"], "").(string)
		if !ok || length(check) > MaxCheck {
			return Settings{}, bad("workspace.check")
		}
		prefix, ok := fallback(value["branchPrefix"], DefaultBranchPrefix).(string)
		if !ok || !ValidBranchPrefix(Trim(prefix)) {
			return Settings{}, bad("workspace.branchPrefix")
		}
		auto := object(value["auto"])
		autoSetup, ok := fallback(auto["setup"], Trim(setup) == "").(bool)
		if !ok {
			return Settings{}, bad("workspace.auto.setup")
		}
		autoCheck, ok := fallback(auto["check"], Trim(check) == "").(bool)
		if !ok {
			return Settings{}, bad("workspace.auto.check")
		}
		out.Workspace = Workspace{Retention: retention, Auto: Auto{Setup: autoSetup, Check: autoCheck}, Shell: shell, BranchPrefix: Trim(prefix)}
		if !autoSetup {
			out.Workspace.Setup = Trim(setup)
		}
		if !autoCheck {
			out.Workspace.Check = Trim(check)
		}
	}
	if raw, exists := body["routines"]; exists {
		var err error
		out.Routines, err = ParseRoutines(raw)
		if err != nil {
			return Settings{}, err
		}
	}
	if raw, exists := body["review"]; exists {
		enabled, ok := fallback(object(raw)["enabled"], true).(bool)
		if !ok {
			return Settings{}, bad("review.enabled")
		}
		out.Review.Enabled = enabled
	}
	if raw, exists := body["autopilot"]; exists {
		value := object(raw)
		enabled, ok := fallback(value["enabled"], false).(bool)
		if !ok || enabled && !out.Review.Enabled {
			return Settings{}, bad("autopilot.enabled")
		}
		method, ok := fallback(value["method"], "squash").(string)
		if !ok || !contains([]string{"squash", "merge"}, method) {
			return Settings{}, bad("autopilot.method")
		}
		out.Autopilot = Autopilot{Enabled: enabled, Method: method}
	}
	return out, nil
}

func ParseRoutines(raw any) ([]Routine, error) {
	if typed, ok := raw.([]Routine); ok {
		if len(typed) > MaxRoutines {
			return nil, bad("routines")
		}
		data, err := json.Marshal(typed)
		if err != nil {
			return nil, bad("routines")
		}
		values := []any{}
		if typed != nil {
			if err := json.Unmarshal(data, &values); err != nil {
				return nil, bad("routines")
			}
		}
		raw = values
	}
	values, ok := raw.([]any)
	if !ok || len(values) > MaxRoutines {
		return nil, bad("routines")
	}
	result := make([]Routine, 0, len(values))
	seen := make(map[string]bool)
	for index, raw := range values {
		at := fmt.Sprintf("routines.%d", index)
		value := object(raw)
		id, ok := value["id"].(string)
		if !ok || !routineID.MatchString(id) || seen[id] {
			return nil, bad(at + ".id")
		}
		seen[id] = true
		prompt, ok := value["prompt"].(string)
		if !ok || Trim(prompt) == "" || length(prompt) > MaxRoutinePrompt {
			return nil, bad(at + ".prompt")
		}
		days, ok := value["days"].([]any)
		if !ok {
			return nil, bad(at + ".days")
		}
		unique := make(map[int]bool)
		for _, raw := range days {
			day, ok := number(raw)
			if !ok || math.Trunc(day) != day || day < 0 || day > 6 {
				return nil, bad(at + ".days")
			}
			unique[int(day)] = true
		}
		clock, ok := value["time"].(string)
		if !ok || !clockPattern.MatchString(clock) {
			return nil, bad(at + ".time")
		}
		ordered := make([]int, 0, len(unique))
		for day := range unique {
			ordered = append(ordered, day)
		}
		sort.Ints(ordered)
		enabled := true
		if given, ok := value["enabled"].(bool); ok && !given {
			enabled = false
		}
		result = append(result, Routine{ID: id, Prompt: Trim(prompt), Days: ordered, Time: clock, Enabled: enabled})
	}
	return result, nil
}

func Stored(raw string) (Settings, error) {
	out := Defaults()
	if raw == "" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return Settings{}, err
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		return Settings{}, err
	}
	if workspace, exists := body["workspace"]; exists {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(workspace, &fields); err != nil {
			return Settings{}, err
		}
		if auto, exists := fields["auto"]; !exists || string(auto) == "null" {
			out.Workspace.Auto = Auto{Setup: out.Workspace.Setup == "", Check: out.Workspace.Check == ""}
		}
	}
	return out, nil
}
