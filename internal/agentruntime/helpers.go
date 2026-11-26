package agentruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/fsutil"
	"github.com/daidr/Ariadne/internal/process"
	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/sdk"
)

// ClientInfo identifies Ariadne to local agent CLI protocols (Codex, ACP, etc.).
var ClientInfo = map[string]any{"name": "ariadne", "title": "Ariadne", "version": "1.0.0"}

// PluginOptions configures shared runtime dependencies for built-in agent CLI plugins.
type PluginOptions struct {
	Bridge        *ToolBridge
	DataDir       string
	Now           func() time.Time
	StdioLauncher StdioLauncher
}

// ResolveDataDir returns the shared agent runtime directory when configured,
// falling back to the plugin's own data directory for isolated tests.
func ResolveDataDir(opts PluginOptions, fallback string) string {
	if textutil.Trim(opts.DataDir) != "" {
		return opts.DataDir
	}
	return fallback
}

func Localized(zh, en string) sdk.LocalizedText {
	return sdk.LocalizedText{"zh-CN": zh, "en": en}
}

func StringSetting(settings map[string]any, key string) string {
	value, _ := settings[key].(string)
	return value
}

// EnvSetting extracts custom provider environment variables from settings["env"].
func EnvSetting(settings map[string]any) map[string]string {
	if settings == nil {
		return nil
	}
	raw, exists := settings["env"]
	if !exists || raw == nil {
		return nil
	}
	out := map[string]string{}
	add := func(key, value string) {
		key = textutil.Trim(key)
		if key == "" || strings.HasPrefix(strings.ToUpper(key), "ARIADNE_") {
			return
		}
		out[key] = value
	}
	switch entries := raw.(type) {
	case map[string]string:
		for key, value := range entries {
			add(key, value)
		}
	case map[string]any:
		for key, value := range entries {
			if str, ok := value.(string); ok {
				add(key, str)
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// MergeEnv layers overrides on top of defaults, normalizing case-insensitive
// key collisions on Windows so overrides deterministically win.
func MergeEnv(defaults, overrides map[string]string) map[string]string {
	if len(defaults) == 0 && len(overrides) == 0 {
		return nil
	}
	out := make(map[string]string, len(defaults)+len(overrides))
	for key, value := range defaults {
		out[key] = value
	}
	for key, value := range overrides {
		if runtime.GOOS == "windows" {
			for existing := range out {
				if strings.EqualFold(existing, key) {
					delete(out, existing)
				}
			}
		}
		out[key] = value
	}
	return out
}

func TrimmedJSON(raw json.RawMessage) string {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return textutil.Trim(value)
}

func ProgramCapabilities() sdk.ModelProviderCapabilities {
	return sdk.ModelProviderCapabilities{
		ReasoningScope:     sdk.ReasoningScopeModel,
		StatefulSessions:   true,
		PreserveModelOrder: true,
		PreferDefaultModel: true,
	}
}

func CommandSchema(defaultCommand string, description sdk.LocalizedText, required bool) sdk.SettingsSchema {
	field := sdk.SettingField{
		Type:        "string",
		Title:       Localized("命令", "Command"),
		Description: description,
	}
	var req []string
	if defaultCommand != "" {
		field.Default = defaultCommand
	}
	if required {
		req = []string{"command"}
	}
	return sdk.SettingsSchema{
		Type:       "object",
		Properties: map[string]sdk.SettingField{"command": field},
		Required:   req,
	}
}

// ValidateCommandSettings validates the "command" field of an agent CLI model provider.
func ValidateCommandSettings(required bool) func(map[string]any) error {
	return func(values map[string]any) error {
		raw, exists := values["command"]
		if !exists || raw == nil {
			if required {
				return domainerr.New("error.invalidBody", map[string]any{"reason": "command"})
			}
			return nil
		}
		command, ok := raw.(string)
		if !ok || len(command) > 4096 || strings.ContainsAny(command, "\x00\r\n") {
			return domainerr.New("error.invalidBody", map[string]any{"reason": "command"})
		}
		trimmed := textutil.Trim(command)
		if trimmed == "" {
			if required {
				return domainerr.New("error.invalidBody", map[string]any{"reason": "command"})
			}
			return nil
		}
		if len(process.SplitCommand(trimmed)) == 0 {
			return domainerr.New("error.invalidBody", map[string]any{"reason": "command"})
		}
		return nil
	}
}

// CheckCommand verifies that the first token of command is resolvable in PATH.
func CheckCommand(command string) (sdk.LocalizedText, error) {
	return CheckCommandWithEnv(command, nil)
}

// CheckCommandWithEnv verifies that the first token of command is resolvable
// in the agent process environment with optional provider overrides applied.
func CheckCommandWithEnv(command string, env map[string]string) (sdk.LocalizedText, error) {
	words := process.SplitCommand(command)
	if len(words) == 0 {
		return nil, nil
	}
	if _, err := process.LookPath(words[0], process.AgentEnv(env)); err != nil {
		return Localized("找不到命令 "+words[0], "command not found: "+words[0]), nil
	}
	return nil, nil
}

// WithModelTempDir allocates a temporary working directory under <dataDir>/sessions
// for model discovery and cleans it up afterwards.
func WithModelTempDir(dataDir string, fn func(directory string) ([]sdk.ListedModel, error)) ([]sdk.ListedModel, error) {
	root := filepath.Join(dataDir, "sessions")
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	directory, err := os.MkdirTemp(root, "models-")
	if err != nil {
		return nil, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		_ = fsutil.RemoveTree(cleanup, root, directory)
	}()
	return fn(directory)
}

// EnsureDefaultModel prepends a "default" entry if not already present.
func EnsureDefaultModel(listed []sdk.ListedModel) []sdk.ListedModel {
	for _, model := range listed {
		if model.ID == "default" {
			return listed
		}
	}
	return append([]sdk.ListedModel{{ID: "default"}}, listed...)
}

// ToolNames returns the names of the provided tools in order.
func ToolNames(tools []sdk.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}
