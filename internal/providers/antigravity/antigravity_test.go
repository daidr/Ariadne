package antigravity

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/agentruntime"
	"github.com/daidr/Ariadne/internal/modelstep"
	"github.com/daidr/Ariadne/sdk"
)

const testACPName = "Antigravity ACP Server"

func newTestSessions(t *testing.T, kind agentruntime.Kind, options agentruntime.SessionOptions) *agentruntime.Sessions {
	t.Helper()
	if options.DataDir == "" {
		options.DataDir = t.TempDir()
	}
	if options.Quiet == 0 {
		options.Quiet = 5 * time.Millisecond
	}
	m, err := agentruntime.NewSessions(kind, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := m.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return m
}

func newTestBridge(t *testing.T) *agentruntime.ToolBridge {
	t.Helper()
	bridge := agentruntime.NewToolBridge()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := bridge.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return bridge
}

func collectParts(t *testing.T, m *agentruntime.Sessions, settings map[string]any, request sdk.Request) []sdk.Part {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	result := []sdk.Part{}
	for part := range m.Stream(ctx, settings, request) {
		result = append(result, part)
	}
	if ctx.Err() != nil {
		t.Fatal("Antigravity stream timed out", result)
	}
	return result
}

func lastPart(t *testing.T, items []sdk.Part, reason string) {
	t.Helper()
	if len(items) == 0 || items[len(items)-1].Reason != reason {
		t.Fatalf("wanted finish %s, got %#v", reason, items)
	}
}

func textParts(parts []sdk.Part) string {
	var text strings.Builder
	for _, part := range parts {
		if part.Type == "text" {
			text.WriteString(part.Text)
		}
	}
	return text.String()
}

func tokenValue(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func user(text string) sdk.Message      { return sdk.Message{Role: "user", Content: text} }
func assistant(text string) sdk.Message { return sdk.Message{Role: "assistant", Content: text} }

func agyChildCommand(t *testing.T, scenario string) (string, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "agy.jsonl")
	return fmt.Sprintf(`"%s" -test.run=^TestAntigravityChild$ -- "%s" "%s"`, exe, log, scenario), log
}

func TestAntigravityToolProfile(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	argv := []string{filepath.Join(directory, "AGY.EXE"), "acp", "--process-per-session"}
	if !usesToolProfile(argv) {
		t.Fatal("the Antigravity ACP command should use the Ariadne profile")
	}
	for _, override := range [][]string{
		{"agy", "acp", "--agent", "custom"},
		{"agy", "acp", "--agent=custom"},
		{"wrapper", "acp"},
		{"agy", "models"},
	} {
		if usesToolProfile(override) {
			t.Fatalf("unexpected managed profile for %v", override)
		}
	}
	prepared, err := prepareToolProfile(argv, directory, toolProfileName)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(prepared, append(argv, "--agent", toolProfileName, "--add-dir", directory)) {
		t.Fatalf("unexpected Antigravity command: %v", prepared)
	}
	profile, err := os.ReadFile(filepath.Join(directory, ".agents", "agents", toolProfileName+".md"))
	if err != nil || !strings.Contains(string(profile), "\ntools: []\n") {
		t.Fatalf("Antigravity tool profile is missing: %s, %v", profile, err)
	}
}

func TestAntigravityReplaysHistoryAfterToolProfileChange(t *testing.T) {
	t.Parallel()
	driver, err := Kind(nil, nil).Create(agentruntime.DriverContext{Resume: json.RawMessage(`{"sessionId":"old"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.Start(t.Context()); err == nil || !strings.Contains(err.Error(), "agent profile changed") {
		t.Fatalf("an old ACP session would retain its original tools: %v", err)
	}
	if err := driver.Close(t.Context(), true); err != nil {
		t.Fatal(err)
	}
}

func TestAntigravityExecutorError(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		scenario, reason, detail string
		wantText                 string
		busy, retryable          bool
		expectEmptyText          bool
	}{
		{scenario: "agy-error", reason: "error", detail: "synthetic Gemini request rejected", expectEmptyText: true},
		{scenario: "agy-executor-start", reason: "error", detail: "plan model not specified", expectEmptyText: true},
		{scenario: "agy-quota", reason: "error", detail: "Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 1h17m22s.", busy: true, retryable: false, expectEmptyText: true},
		{scenario: "agy-quota-bare", reason: "error", detail: "Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 1h17m22s.", busy: true, retryable: false, expectEmptyText: true},
		{scenario: "agy-quota-split", reason: "error", detail: "Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 1h17m22s.", busy: true, retryable: false, expectEmptyText: true},
		{scenario: "agy-quota-split-reset", reason: "error", detail: "Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 1h17m22s.", busy: true, retryable: false, expectEmptyText: true},
		{scenario: "agy-quota-429", reason: "error", detail: "Error 429: Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 1h17m22s.", busy: true, retryable: false, expectEmptyText: true},
		{scenario: "agy-quota-429-split", reason: "error", detail: "Error 429: Individual quota reached. Resets in 1h17m22s.", busy: true, retryable: false, expectEmptyText: true},
		{scenario: "agy-retrying-then-quota", reason: "error", detail: "Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 1h17m22s.", busy: true, retryable: false},
		{scenario: "agy-tool-then-quota", reason: "error", detail: "Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 1h17m22s.", busy: true, retryable: false},
		{scenario: "agy-rate-limit", reason: "error", detail: "Rate limit exceeded. Please try again later.", busy: true, retryable: true, expectEmptyText: true},
		{scenario: "agy-recovered", reason: "stop", wantText: "answer"},
		{scenario: "agy-retrying-split", reason: "stop", wantText: "answer"},
		{scenario: "agy-markdown-warning-emoji", reason: "stop", wantText: "⚠️ Temporary workaround\n⚠️ 500 lines changed\n⚠️ Timeout 设置过小"},
		{scenario: "agy-plain-warning-text", reason: "stop", wantText: "⚠ 500 lines changed"},
		{scenario: "agy-multiline-quota-prose", reason: "stop", wantText: "Here is the root cause and fix."},
		{scenario: "agy-prose-then-warning", reason: "stop", wantText: "Done."},
		{scenario: "agy-warning-before-tool-then-done", reason: "stop", wantText: "final answer"},
		{scenario: "agy-warning-before-thought-then-done", reason: "stop", wantText: "final answer"},
	} {
		t.Run(test.scenario, func(t *testing.T) {
			t.Parallel()
			command, _ := agyChildCommand(t, test.scenario)
			m := newTestSessions(t, Kind(nil, nil), agentruntime.SessionOptions{})
			request := sdk.Request{Conversation: "errors", Messages: []sdk.Message{user("hello")}}
			parts := collectParts(t, m, map[string]any{"command": command}, request)
			lastPart(t, parts, test.reason)
			last := parts[len(parts)-1]
			if test.reason == "error" {
				if (test.expectEmptyText && textParts(parts) != "") ||
					!strings.Contains(last.Error, test.detail) ||
					last.Busy != test.busy ||
					last.Retryable != test.retryable {
					t.Fatalf("unexpected executor failure: %+v", parts)
				}
				if last.AgentError == nil || last.AgentError.Operation != "session/update" || last.AgentError.StopReason != "end_turn" {
					t.Fatalf("missing ACP update diagnostics: %+v", last.AgentError)
				}
			} else if test.wantText != "" && !strings.Contains(textParts(parts), test.wantText) {
				t.Fatalf("expected text containing %q, got %q", test.wantText, textParts(parts))
			}
		})
	}
}

func TestAntigravityQuotaDoesNotRetryAndAllowsFallback(t *testing.T) {
	t.Parallel()
	command, _ := agyChildCommand(t, "agy-quota")
	m := newTestSessions(t, Kind(nil, nil), agentruntime.SessionOptions{})
	provider := sdk.ModelProvider{Stream: m.Stream}
	retries := 0
	var recorded []modelstep.AttemptFailure
	result, err := modelstep.Request(t.Context(), provider, map[string]any{"command": command}, sdk.Request{
		Conversation: "quota-step",
		Messages:     []sdk.Message{user("hello")},
	}, modelstep.Options{
		Retries: new(3),
		OnRetry: func(modelstep.Retry) error {
			retries++
			return nil
		},
		OnFailure: func(attempt modelstep.AttemptFailure) error {
			recorded = append(recorded, attempt)
			return nil
		},
	})
	var failure *modelstep.Failure
	if result != nil || !errors.As(err, &failure) {
		t.Fatalf("expected modelstep failure, got result=%+v err=%v", result, err)
	}
	if retries != 0 {
		t.Fatalf("quota failure should not retry immediately, got %d retries", retries)
	}
	if len(recorded) != 1 || recorded[0].Retryable || recorded[0].WillRetry || !strings.Contains(recorded[0].Detail, "Resets in 1h17m22s.") {
		t.Fatalf("unexpected recorded attempt failure: %+v", recorded)
	}
	if failure.Exhausted == nil || !strings.Contains(*failure.Exhausted, "Individual quota reached") || !strings.Contains(*failure.Exhausted, "Resets in 1h17m22s.") {
		t.Fatalf("quota failure should preserve detail and allow fallback: %+v", failure)
	}
	if failure.Localized == nil || failure.Localized.Key != "error.modelRequest" {
		t.Fatalf("unexpected localized error: %+v", failure.Localized)
	}
}

func TestAntigravityUsageSeparatesTotalsFromContext(t *testing.T) {
	t.Parallel()
	command, _ := agyChildCommand(t, "agy-usage")
	settings := map[string]any{"command": command}
	opts := agentruntime.SessionOptions{DataDir: t.TempDir()}
	m := newTestSessions(t, Kind(nil, nil), opts)
	request := sdk.Request{Conversation: "coordinator", Messages: []sdk.Message{user("first")}}
	first := collectParts(t, m, settings, request)
	check := func(parts []sdk.Part) {
		t.Helper()
		lastPart(t, parts, "stop")
		for _, part := range parts {
			if part.Type != "usage" {
				continue
			}
			if tokenValue(part.Input) != 170000 || tokenValue(part.Cached) != 167000 ||
				tokenValue(part.Output) != 47 || tokenValue(part.Reasoning) != 7 ||
				tokenValue(part.ContextInput) != 90034 {
				t.Fatalf("incorrect Antigravity usage: %+v", part)
			}
			return
		}
		t.Fatal("no Antigravity usage reported")
	}
	check(first)
	if err := m.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	m = newTestSessions(t, Kind(nil, nil), opts)
	request.Messages = append(request.Messages, assistant(textParts(first)), user("second"))
	check(collectParts(t, m, settings, request))
}

func TestAntigravityMidTurnUsageBeforeFirstMessage(t *testing.T) {
	t.Parallel()
	command, _ := agyChildCommand(t, "agy-midturn-usage")
	bridge := newTestBridge(t)
	m := newTestSessions(t, Kind(bridge, nil), agentruntime.SessionOptions{})
	settings := map[string]any{"command": command}
	tools := []sdk.Tool{{Name: "read_file", Description: "Read a file", Parameters: map[string]any{"properties": map[string]any{"path": map[string]any{"type": "string"}}}}}
	request := sdk.Request{Conversation: "thread-midturn", Messages: []sdk.Message{user("inspect repo")}, Tools: tools}

	first := collectParts(t, m, settings, request)
	lastPart(t, first, "tool-calls")
	var call *sdk.Part
	var firstUsage *sdk.Part
	for i := range first {
		switch first[i].Type {
		case "tool-call":
			call = &first[i]
		case "usage":
			firstUsage = &first[i]
		}
	}
	if call == nil || call.Name != "read_file" {
		t.Fatalf("expected tool-call read_file before first message, got %+v", first)
	}
	if firstUsage == nil || tokenValue(firstUsage.ContextInput) != 42000 || tokenValue(firstUsage.Input) != 0 || tokenValue(firstUsage.Output) != 0 {
		t.Fatalf("expected mid-turn context usage on tool-call step, got %+v", firstUsage)
	}

	request.Messages = append(request.Messages,
		sdk.Message{Role: "assistant", Content: textParts(first), ToolCalls: []sdk.ToolCall{{ID: call.ID, Name: call.Name, Arguments: call.Arguments}}},
		sdk.Message{Role: "tool", ToolCallID: call.ID, Content: "file body"},
	)
	second := collectParts(t, m, settings, request)
	lastPart(t, second, "stop")
	var finalUsage *sdk.Part
	for i := range second {
		if second[i].Type == "usage" {
			finalUsage = &second[i]
		}
	}
	if finalUsage == nil || tokenValue(finalUsage.Input) != 170000 || tokenValue(finalUsage.Cached) != 167000 ||
		tokenValue(finalUsage.Output) != 47 || tokenValue(finalUsage.Reasoning) != 7 ||
		tokenValue(finalUsage.ContextInput) != 55000 {
		t.Fatalf("unexpected final Antigravity usage after mid-turn update: %+v", finalUsage)
	}
}

func TestAntigravitySessionNewRaceRetriesAndSucceeds(t *testing.T) {
	t.Parallel()
	command, _ := agyChildCommand(t, "agy-session-new-race-once")
	m := newTestSessions(t, Kind(nil, nil), agentruntime.SessionOptions{})
	provider := sdk.ModelProvider{Stream: m.Stream}
	retries := 0
	var recorded []modelstep.AttemptFailure
	result, err := modelstep.Request(t.Context(), provider, map[string]any{"command": command}, sdk.Request{
		Conversation: "race-step",
		Messages:     []sdk.Message{user("hello")},
	}, modelstep.Options{
		Retries:   new(2),
		BaseDelay: new(time.Duration(0)),
		OnRetry: func(modelstep.Retry) error {
			retries++
			return nil
		},
		OnFailure: func(attempt modelstep.AttemptFailure) error {
			recorded = append(recorded, attempt)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("expected retry to recover from transient session/new failure, got err=%v", err)
	}
	if result == nil || result.Text != "answer" {
		t.Fatalf("unexpected result after retry: %+v", result)
	}
	if retries != 1 {
		t.Fatalf("expected 1 retry, got %d", retries)
	}
	if len(recorded) != 1 || !recorded[0].Retryable || !recorded[0].WillRetry ||
		recorded[0].AgentError == nil || recorded[0].AgentError.Operation != "session/new" ||
		recorded[0].AgentError.RPCCode == nil || *recorded[0].AgentError.RPCCode != -32603 {
		t.Fatalf("unexpected recorded start failure: %+v", recorded)
	}
}

func TestAntigravitySessionNewOtherErrorsDoNotRetry(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"agy-session-new-auth-error", "agy-session-new-other-32603"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			command, _ := agyChildCommand(t, scenario)
			m := newTestSessions(t, Kind(nil, nil), agentruntime.SessionOptions{})
			provider := sdk.ModelProvider{Stream: m.Stream}
			retries := 0
			var recorded []modelstep.AttemptFailure
			result, err := modelstep.Request(t.Context(), provider, map[string]any{"command": command}, sdk.Request{
				Conversation: "non-retryable-start",
				Messages:     []sdk.Message{user("hello")},
			}, modelstep.Options{
				Retries:   new(2),
				BaseDelay: new(time.Duration(0)),
				OnRetry: func(modelstep.Retry) error {
					retries++
					return nil
				},
				OnFailure: func(attempt modelstep.AttemptFailure) error {
					recorded = append(recorded, attempt)
					return nil
				},
			})
			if result != nil || err == nil {
				t.Fatalf("expected start failure, got result=%+v err=%v", result, err)
			}
			if retries != 0 || len(recorded) != 1 || recorded[0].Retryable || recorded[0].WillRetry {
				t.Fatalf("unexpected retry for %s: retries=%d recorded=%+v", scenario, retries, recorded)
			}
		})
	}
}

func TestAntigravityChild(t *testing.T) {
	args := os.Args
	if len(args) < 4 || args[len(args)-3] != "--" {
		return
	}
	log, err := os.OpenFile(args[len(args)-2], os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	restore := args[len(args)-1]
	resumed := false
	mcpURL := ""
	output := json.NewEncoder(os.Stdout)
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	for scanner.Scan() {
		if _, err := fmt.Fprintln(log, scanner.Text()); err != nil {
			t.Fatal(err)
		}
		var request struct {
			ID     json.RawMessage
			Method string
			Params struct {
				MCPServers []struct {
					URL string `json:"url"`
				} `json:"mcpServers"`
			}
		}
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			t.Fatal(err)
		}
		if len(request.ID) == 0 {
			continue
		}
		var result any
		switch request.Method {
		case "initialize":
			capabilities := map[string]any{
				"loadSession":         true,
				"sessionCapabilities": map[string]any{"resume": map[string]any{}},
			}
			if restore == "agy-midturn-usage" {
				capabilities["mcpCapabilities"] = map[string]any{"http": true}
			}
			result = map[string]any{
				"protocolVersion":   1,
				"agentCapabilities": capabilities,
				"agentInfo":         map[string]any{"name": testACPName, "version": "test-1"},
			}
		case "session/new":
			if restore == "agy-session-new-race-once" {
				data, _ := os.ReadFile(args[len(args)-2])
				if strings.Count(string(data), `"method":"session/new"`) == 1 {
					if err := output.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32603, "message": "Failed to start Antigravity conversation"}}); err != nil {
						t.Fatal(err)
					}
					continue
				}
			}
			if restore == "agy-session-new-auth-error" {
				if err := output.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32000, "message": "Authentication required"}}); err != nil {
					t.Fatal(err)
				}
				continue
			}
			if restore == "agy-session-new-other-32603" {
				if err := output.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32603, "message": "other internal failure"}}); err != nil {
					t.Fatal(err)
				}
				continue
			}
			if len(request.Params.MCPServers) > 0 {
				mcpURL = request.Params.MCPServers[0].URL
			}
			result = map[string]any{"sessionId": "test-session"}
		case "session/load", "session/resume":
			resumed = true
			result = map[string]any{}
		case "session/prompt":
			if restore == "agy-midturn-usage" {
				_ = output.Encode(json.RawMessage(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"usage_update","used":42000,"size":400000}}}`))
				resp, err := http.Post(mcpURL, "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"README.md"}}}`))
				if err != nil {
					t.Fatal(err)
				}
				_, _ = io.ReadAll(resp.Body)
				resp.Body.Close()
				_ = output.Encode(json.RawMessage(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"usage_update","used":55000,"size":400000}}}`))
				_ = output.Encode(json.RawMessage(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"done"}}}}`))
				result = map[string]any{
					"stopReason": "end_turn",
					"usage":      map[string]any{"inputTokens": 3000, "outputTokens": 47, "cachedReadTokens": 167000, "thoughtTokens": 7, "totalTokens": 170047},
				}
				break
			}
			type childUpdate struct{ kind, text string }
			updates := []childUpdate{{kind: "agent_message_chunk", text: "answer"}}
			switch restore {
			case "agy-error":
				updates = []childUpdate{{kind: "agent_message_chunk", text: "\n⚠ agent executor error: generating and executing: Error 400, Message: synthetic Gemini request rejected\n"}}
			case "agy-executor-start":
				updates = []childUpdate{{kind: "agent_message_chunk", text: "\n⚠ failed to construct executor: plan model not specified; you must specify a valid model\n\n"}}
			case "agy-quota":
				updates = []childUpdate{{kind: "agent_message_chunk", text: "\n⚠ Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 1h17m22s.\n"}}
			case "agy-quota-bare":
				updates = []childUpdate{{kind: "agent_message_chunk", text: "⚠ Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 1h17m22s."}}
			case "agy-quota-split":
				updates = []childUpdate{
					{kind: "agent_message_chunk", text: "\n⚠ "},
					{kind: "agent_message_chunk", text: "Individual quota reached. Please upgrade your subscription to increase your limits. "},
					{kind: "agent_message_chunk", text: "Resets in 1h17m22s."},
				}
			case "agy-quota-split-reset":
				updates = []childUpdate{
					{kind: "agent_message_chunk", text: "\n⚠ Individual quota reached. Please upgrade your subscription to increase your limits. "},
					{kind: "agent_message_chunk", text: "Resets in 1h17m22s."},
				}
			case "agy-quota-429":
				updates = []childUpdate{{kind: "agent_message_chunk", text: "\n⚠ agent executor error: Error 429: Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 1h17m22s.\n"}}
			case "agy-quota-429-split":
				updates = []childUpdate{
					{kind: "agent_message_chunk", text: "\n⚠ agent executor error: Error 429: "},
					{kind: "agent_message_chunk", text: "Individual quota reached. Resets in 1h17m22s."},
				}
			case "agy-retrying-then-quota":
				updates = []childUpdate{
					{kind: "agent_message_chunk", text: "\n⚠ API error (attempt 1): Error 429. Retrying.\n"},
					{kind: "agent_message_chunk", text: "\n⚠ Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 1h17m22s.\n"},
				}
			case "agy-tool-then-quota":
				updates = []childUpdate{
					{kind: "agent_message_chunk", text: "checking..."},
					{kind: "tool_call"},
					{kind: "agent_message_chunk", text: "\n⚠ Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 1h17m22s.\n"},
				}
			case "agy-rate-limit":
				updates = []childUpdate{{kind: "agent_message_chunk", text: "\n⚠ Rate limit exceeded. Please try again later.\n"}}
			case "agy-recovered":
				updates = []childUpdate{{kind: "agent_message_chunk", text: "\n⚠ API error (attempt 1): Error 429. Retrying.\nanswer"}}
			case "agy-retrying-split":
				updates = []childUpdate{
					{kind: "agent_message_chunk", text: "\n⚠ API error (attempt 1): Error 429. Retrying.\n"},
					{kind: "agent_message_chunk", text: "answer"},
				}
			case "agy-markdown-warning-emoji":
				updates = []childUpdate{
					{kind: "agent_message_chunk", text: "⚠️ Temporary workaround\n"},
					{kind: "agent_message_chunk", text: "⚠️ 500 lines changed\n"},
					{kind: "agent_message_chunk", text: "⚠️ Timeout 设置过小"},
				}
			case "agy-plain-warning-text":
				updates = []childUpdate{{kind: "agent_message_chunk", text: "⚠ 500 lines changed"}}
			case "agy-multiline-quota-prose":
				updates = []childUpdate{
					{kind: "agent_message_chunk", text: "⚠ Quota reached when testing the API\n"},
					{kind: "agent_message_chunk", text: "Here is the root cause and fix."},
				}
			case "agy-prose-then-warning":
				updates = []childUpdate{
					{kind: "agent_message_chunk", text: "Summary:\n"},
					{kind: "agent_message_chunk", text: "\n⚠ Individual quota reached. Resets in 1h.\n"},
					{kind: "agent_message_chunk", text: "Done."},
				}
			case "agy-warning-before-tool-then-done":
				updates = []childUpdate{
					{kind: "agent_message_chunk", text: "\n⚠ Individual quota reached. Resets in 1h."},
					{kind: "tool_call"},
					{kind: "agent_message_chunk", text: "final answer"},
				}
			case "agy-warning-before-thought-then-done":
				updates = []childUpdate{
					{kind: "agent_message_chunk", text: "\n⚠ agent executor error: transient note"},
					{kind: "agent_thought_chunk", text: "thinking"},
					{kind: "agent_message_chunk", text: "final answer"},
				}
			}
			for _, u := range updates {
				if u.kind == "tool_call" {
					_ = output.Encode(json.RawMessage(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"tool_call"}}}`))
					continue
				}
				_ = output.Encode(json.RawMessage(fmt.Sprintf(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":%q,"content":{"type":"text","text":%q}}}}`, u.kind, u.text)))
			}
			response := map[string]any{"stopReason": "end_turn"}
			if restore == "agy-usage" {
				if !resumed {
					for _, used := range []int{80013, 90034} {
						_ = output.Encode(json.RawMessage(fmt.Sprintf(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"usage_update","used":%d,"size":400000}}}`, used)))
					}
				}
				response["usage"] = map[string]any{"inputTokens": 3000, "outputTokens": 47, "cachedReadTokens": 167000, "thoughtTokens": 7, "totalTokens": 170047}
			}
			result = response
		default:
			t.Fatalf("unexpected method: %s", request.Method)
		}
		if err := output.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}); err != nil {
			t.Fatal(err)
		}
	}
}
