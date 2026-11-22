package antigravity

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/daidr/Ariadne/internal/agentruntime"
	"github.com/daidr/Ariadne/internal/providers/acp"
	"github.com/daidr/Ariadne/internal/rpc"
	"github.com/daidr/Ariadne/internal/textutil"
)

const DefaultCommand = "agy acp --process-per-session --disable-telemetry"
const toolProfileName = "ariadne-provider"
const maxWarningBytes = 4096

var retryingPattern = regexp.MustCompile(`(?i)\bretrying\b`)
var rateLimitPattern = regexp.MustCompile(`(?i)^(?:rate limit(?:s)?\s+(?:exceeded|reached|hit)\b|too many requests\b)`)

const toolProfileMarkdown = `---
name: ` + toolProfileName + `
description: Agent for conversations managed by Ariadne.
mainAgent: true
subagent: false
tools: []
---
Use the Ariadne tools provided for this conversation.
`

func warningBody(text string) (string, bool) {
	trimmed := strings.TrimLeft(text, " \t\r\n")
	rest, ok := strings.CutPrefix(trimmed, "⚠ ")
	if !ok {
		return "", false
	}
	return textutil.Trim(rest), true
}

func executorPrefix(body string) bool {
	return strings.HasPrefix(body, "failed to construct executor: ") || strings.HasPrefix(body, "agent executor error: ")
}

func couldBeWarning(text string) bool {
	trimmed := strings.TrimLeft(text, " \t\r\n")
	if trimmed == "" || trimmed == "⚠" {
		return true
	}
	rest, ok := strings.CutPrefix(trimmed, "⚠ ")
	if !ok {
		return false
	}
	body := textutil.Trim(rest)
	if executorPrefix(body) {
		return true
	}
	return !strings.Contains(body, "\n")
}

func retryingNotice(text string) bool {
	body, ok := warningBody(text)
	return ok && !strings.Contains(body, "\n") && retryingPattern.MatchString(body)
}

// agy sends fatal executor and subscription quota failures as warning chat chunks
// followed by end_turn rather than JSON-RPC errors.
func parseFailure(text string) (string, bool) {
	body, ok := warningBody(text)
	if !ok || body == "" {
		return "", false
	}
	if detail, ok := strings.CutPrefix(body, "failed to construct executor: "); ok && textutil.Trim(detail) != "" {
		return "failed to construct executor: " + textutil.Trim(detail), true
	}
	if detail, ok := strings.CutPrefix(body, "agent executor error: "); ok && textutil.Trim(detail) != "" {
		return textutil.Trim(detail), true
	}
	if strings.Contains(body, "\n") || retryingPattern.MatchString(body) {
		return "", false
	}
	if rpc.QuotaText(body) || rateLimitPattern.MatchString(body) {
		return body, true
	}
	return "", false
}

func usesToolProfile(argv []string) bool {
	if len(argv) < 2 || argv[1] != "acp" || strings.TrimSuffix(strings.ToLower(filepath.Base(argv[0])), ".exe") != "agy" {
		return false
	}
	return !slices.ContainsFunc(argv[2:], func(arg string) bool {
		return arg == "--agent" || strings.HasPrefix(arg, "--agent=")
	})
}

func resolveToolProfile(argv []string) string {
	if usesToolProfile(argv) {
		return toolProfileName
	}
	return ""
}

func prepareToolProfile(argv []string, directory string, profile string) ([]string, error) {
	directory, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	agentsDir := filepath.Join(directory, ".agents", "agents")
	if err := os.MkdirAll(agentsDir, 0700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(agentsDir, profile+".md"), []byte(toolProfileMarkdown), 0600); err != nil {
		return nil, err
	}
	return append(argv, "--agent", profile, "--add-dir", directory), nil
}

// agy reports uncached input separately. Its total includes cached
// input and output, and thinking is already part of output.
func adjustInputTokens(u acp.TurnUsage) int64 {
	input := u.Input
	if u.Total == nil || *u.Total-u.Output != u.Input {
		input += u.Cached + u.Written
	}
	return input
}

type turnFilter struct {
	warning     strings.Builder
	stepHasText bool
}

func newTurnFilter() acp.TurnFilter {
	return &turnFilter{}
}

func (f *turnFilter) Reset() {
	f.warning.Reset()
	f.stepHasText = false
}

func (f *turnFilter) OnNonMessageUpdate() []string {
	f.stepHasText = false
	if f.warning.Len() == 0 {
		return nil
	}
	text := f.warning.String()
	f.warning.Reset()
	return []string{text}
}

func (f *turnFilter) OnMessageChunk(text string) []string {
	if f.warning.Len() > 0 {
		if retryingNotice(f.warning.String()) && strings.HasPrefix(strings.TrimLeft(text, " \t\r\n"), "⚠") && len(text) <= maxWarningBytes && couldBeWarning(text) {
			prev := f.warning.String()
			f.warning.Reset()
			f.warning.WriteString(text)
			return []string{prev}
		}
		if len(text) <= maxWarningBytes-f.warning.Len() {
			f.warning.WriteString(text)
			if couldBeWarning(f.warning.String()) {
				return nil
			}
			flushed := f.warning.String()
			f.warning.Reset()
			f.stepHasText = true
			return []string{flushed}
		}
		flushed := f.warning.String()
		f.warning.Reset()
		f.stepHasText = true
		return []string{flushed, text}
	}
	if !f.stepHasText && len(text) <= maxWarningBytes && couldBeWarning(text) {
		f.warning.WriteString(text)
		return nil
	}
	f.stepHasText = true
	return []string{text}
}

func (f *turnFilter) OnTurnComplete(rpcErr error) (string, string) {
	pending := f.warning.String()
	f.warning.Reset()
	f.stepHasText = false
	if rpcErr != nil || pending == "" {
		return "", ""
	}
	if parsed, ok := parseFailure(pending); ok {
		return "", parsed
	}
	return pending, ""
}

// agy's --process-per-session workers share the antigravity-cli conversations
// directory. Under concurrent workers, session/new can hit a transient SQLite
// trajectory initialization race ("its database was initialized by another
// opener") and return JSON-RPC -32603 "Failed to start Antigravity conversation".
func retryableStartError(operation string, err error) bool {
	if operation != "session/new" {
		return false
	}
	var remote *rpc.Error
	if !errors.As(err, &remote) || remote.Code != -32603 {
		return false
	}
	return strings.Contains(remote.Message, "Failed to start Antigravity conversation")
}

func Kind(bridge *agentruntime.ToolBridge, shim agentruntime.StdioLauncher) agentruntime.Kind {
	return acp.NewKind(ProviderKind, bridge, shim, acp.Hooks{
		DefaultCommand:      DefaultCommand,
		ResolveProfile:      resolveToolProfile,
		PrepareProfile:      prepareToolProfile,
		NewTurnFilter:       newTurnFilter,
		AdjustInputTokens:   adjustInputTokens,
		RetryableStartError: retryableStartError,
	})
}
