package agents

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
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/internal/modelstep"
	"github.com/daidr/Ariadne/sdk"
)

func TestACPToolPermissionBoundary(t *testing.T) {
	tools := []sdk.Tool{{Name: "read"}}
	cases := []struct {
		raw     string
		allowed bool
	}{
		{`{"title":"mcp__ariadne__read"}`, true},
		{`{"title":"mcp_ariadne_read"}`, true},
		{`{"name":"mcp_ariadne_read","title":"Run mcp_ariadne_read"}`, true},
		{`{"title":" READ (Ariadne MCP Server) "}`, true},
		{`{"title":"ariadne/read"}`, true},
		{`{"title":"ariadne.read"}`, true},
		{`{"title":"ariadne__read"}`, true},
		{`{"rawInput":{"tool_name":"mcp__ariadne__read"}}`, true},
		{`{"title":"run shell command ariadne"}`, false},
		{`{"title":"ariadne/read /etc/passwd"}`, false},
		{`{"title":"read"}`, false},
		{`{"rawInput":{"path":"mcp__ariadne__read"}}`, false},
		{`{"rawInput":{"command":"mcp__ariadne__read"}}`, false},
		{`{"title":"mcp__ariadne__other"}`, false},
		{`{"name":"mcp_ariadne_other","title":"Run mcp_ariadne_other"}`, false},
		{`null`, false},
	}
	for _, test := range cases {
		if got := ForConversationTools(json.RawMessage(test.raw), tools); got != test.allowed {
			t.Fatalf("%s: %v", test.raw, got)
		}
	}
	driver := &acpDriver{ctx: DriverContext{Tools: AgentTools{Specs: tools}}, sessionID: "session", active: true}
	for _, own := range []bool{false, true} {
		title := "shell"
		want := "no"
		if own {
			title = "ariadne/read"
			want = "yes"
		}
		params, _ := json.Marshal(map[string]any{"sessionId": "session", "toolCall": map[string]any{"title": title}, "options": []any{map[string]any{"optionId": "always", "kind": "allow_always"}, map[string]any{"optionId": "yes", "kind": "allow_once"}, map[string]any{"optionId": "never", "kind": "reject_always"}, map[string]any{"optionId": "no", "kind": "reject_once"}}})
		answer, err := driver.request(t.Context(), "session/request_permission", params)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(answer)
		if !strings.Contains(string(data), `"optionId":"`+want+`"`) {
			t.Fatal(string(data))
		}
	}
	for _, raw := range []string{
		`{"sessionId":"other","toolCall":{"title":"ariadne/read"},"options":[{"optionId":"yes","kind":"allow_once"}]}`,
		`{"sessionId":"session","toolCall":{"title":"ariadne/read"},"options":[{"optionId":"always","kind":"allow_always"}]}`,
	} {
		answer, err := driver.request(t.Context(), "session/request_permission", json.RawMessage(raw))
		data, _ := json.Marshal(answer)
		if err != nil || string(data) != `{"outcome":{"outcome":"cancelled"}}` {
			t.Fatalf("permission should be cancelled: %s %v", data, err)
		}
	}
}
func TestACPModelsAndThoughts(t *testing.T) {
	models := ACPModels(json.RawMessage(`{"currentModelId":"big","availableModels":[{"modelId":"default"},{"modelId":"big","name":"Big Model"},{"modelId":"small","name":"small"},{"modelId":" "}]}`))
	want := []sdk.ListedModel{{ID: "default", Title: "Big Model", Resolves: "big"}, {ID: "big", Title: "Big Model"}, {ID: "small"}}
	if !reflect.DeepEqual(models, want) {
		t.Fatal(models)
	}
	events := []Event{}
	d := &acpDriver{ctx: DriverContext{Emit: func(event Event) { events = append(events, event) }}, sessionID: "session", started: true, active: true}
	d.notification("session/update", []byte(`{"sessionId":"other","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"other"}}}`))
	for _, chunk := range []string{"thin", "king"} {
		data, _ := json.Marshal(map[string]any{"sessionId": "session", "update": map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]any{"type": "text", "text": chunk}}})
		d.notification("session/update", data)
	}
	d.notification("session/update", []byte(`{"sessionId":"session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"answer"}}}`))
	if len(events) != 4 || events[2].Type != "reasoning" || events[2].Text != "thinking" || events[3].Text != "answer" || d.thought.Len() != 0 {
		t.Fatal(events)
	}
	d.closing = true
	d.notification("session/update", []byte(`{"sessionId":"session","update":{"sessionUpdate":"tool_call"}}`))
	if len(events) != 4 {
		t.Fatal("closed driver emitted")
	}
}
func TestACPRequiresCommandAndBridge(t *testing.T) {
	driver, err := ACPKind(nil, nil).Create(DriverContext{Settings: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.Start(context.Background()); err == nil {
		t.Fatal("empty command accepted")
	}
	if err := driver.Close(context.Background(), true); err != nil {
		t.Fatal(err)
	}
}

func TestAntigravityToolProfile(t *testing.T) {
	directory := t.TempDir()
	argv := []string{filepath.Join(directory, "AGY.EXE"), "acp", "--process-per-session"}
	if !usesAntigravityToolProfile(argv) {
		t.Fatal("the Antigravity ACP command should use the Ariadne profile")
	}
	for _, override := range [][]string{
		{"agy", "acp", "--agent", "custom"},
		{"agy", "acp", "--agent=custom"},
		{"wrapper", "acp"},
		{"agy", "models"},
	} {
		if usesAntigravityToolProfile(override) {
			t.Fatalf("unexpected managed profile for %v", override)
		}
	}
	prepared, err := prepareAntigravityToolProfile(argv, directory)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(prepared, append(argv, "--agent", antigravityToolProfileName, "--add-dir", directory)) {
		t.Fatalf("unexpected Antigravity command: %v", prepared)
	}
	profile, err := os.ReadFile(filepath.Join(directory, ".agents", "agents", antigravityToolProfileName+".md"))
	if err != nil || !strings.Contains(string(profile), "\ntools: []\n") {
		t.Fatalf("Antigravity tool profile is missing: %s, %v", profile, err)
	}
}

func TestAntigravityReplaysHistoryAfterToolProfileChange(t *testing.T) {
	driver, err := AntigravityKind(nil, nil).Create(DriverContext{Resume: json.RawMessage(`{"sessionId":"old"}`)})
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

func acpChildCommand(t *testing.T, scenario string) (string, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "acp.jsonl")
	return fmt.Sprintf(`"%s" -test.run=^TestACPChild$ -- "%s" "%s"`, exe, log, scenario), log
}

func TestACPAntigravityExecutorError(t *testing.T) {
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
		{scenario: "other-agent", reason: "stop"},
		{scenario: "other-agent-quota", reason: "stop"},
	} {
		t.Run(test.scenario, func(t *testing.T) {
			command, _ := acpChildCommand(t, test.scenario)
			m := newTestSessions(t, ACPKind(nil, nil), SessionOptions{})
			request := sdk.Request{Conversation: "errors", Messages: []sdk.Message{user("hello")}}
			parts := codexParts(t, m, map[string]any{"command": command}, request)
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

func TestACPAntigravityQuotaDoesNotRetryAndAllowsFallback(t *testing.T) {
	command, _ := acpChildCommand(t, "agy-quota")
	m := newTestSessions(t, ACPKind(nil, nil), SessionOptions{})
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

func TestACPFailureIncludesRPCDiagnostics(t *testing.T) {
	command, _ := acpChildCommand(t, "agy-rpc-error")
	m := newTestSessions(t, ACPKind(nil, nil), SessionOptions{})
	parts := codexParts(t, m, map[string]any{"command": command}, sdk.Request{Conversation: "diagnostics", Messages: []sdk.Message{user("hello")}})
	lastPart(t, parts, "error")
	detail := parts[len(parts)-1].AgentError
	if detail == nil || detail.Operation != "session/prompt" || detail.SessionMethod != "session/new" ||
		detail.ProtocolVersion != 1 || detail.Name != antigravityACPName || detail.Version != "test-1" ||
		detail.RPCCode == nil || *detail.RPCCode != -32603 || !strings.Contains(detail.RPCData, "request-id-42") {
		t.Fatalf("missing ACP error details: %+v", detail)
	}
}

func TestACPAntigravityUsageSeparatesTotalsFromContext(t *testing.T) {
	command, _ := acpChildCommand(t, "agy-usage")
	settings := map[string]any{"command": command}
	opts := SessionOptions{DataDir: t.TempDir()}
	m := newTestSessions(t, ACPKind(nil, nil), opts)
	request := sdk.Request{Conversation: "coordinator", Messages: []sdk.Message{user("first")}}
	first := codexParts(t, m, settings, request)
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
				t.Fatalf("incorrect ACP usage: %+v", part)
			}
			return
		}
		t.Fatal("no ACP usage reported")
	}
	check(first)
	if err := m.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	m = newTestSessions(t, ACPKind(nil, nil), opts)
	request.Messages = append(request.Messages, assistant(textParts(first)), user("second"))
	check(codexParts(t, m, settings, request))
}

func TestACPMidTurnUsageBeforeFirstMessage(t *testing.T) {
	command, _ := acpChildCommand(t, "agy-midturn-usage")
	bridge := newTestBridge(t)
	m := newTestSessions(t, ACPKind(bridge, nil), SessionOptions{})
	settings := map[string]any{"command": command}
	tools := []sdk.Tool{{Name: "read_file", Description: "Read a file", Parameters: map[string]any{"properties": map[string]any{"path": map[string]any{"type": "string"}}}}}
	request := sdk.Request{Conversation: "thread-midturn", Messages: []sdk.Message{user("inspect repo")}, Tools: tools}

	first := codexParts(t, m, settings, request)
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
	second := codexParts(t, m, settings, request)
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
		t.Fatalf("unexpected final ACP usage after mid-turn update: %+v", finalUsage)
	}
}

func TestACPModernProtocolAndRestart(t *testing.T) {
	for _, restore := range []string{"session/resume", "session/load", "rejected"} {
		t.Run(restore, func(t *testing.T) {
			command, log := acpChildCommand(t, restore)
			models, err := ListACPModels(t.Context(), ACPSettings{Command: command}, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if len(models) != 4 || models[0].Resolves != "base" || models[2].ID != "alternate" || models[3].ID != "fixed" ||
				models[0].Efforts == nil || !reflect.DeepEqual(*models[0].Efforts, []string{"low", "high"}) ||
				models[2].Efforts == nil || !reflect.DeepEqual(*models[2].Efforts, []string{"low", "high", "xhigh"}) ||
				models[3].Efforts == nil || len(*models[3].Efforts) != 0 {
				t.Fatalf("unexpected model catalog: %+v", models)
			}
			if err := os.Truncate(log, 0); err != nil {
				t.Fatal(err)
			}
			opts := SessionOptions{DataDir: t.TempDir()}
			m := newTestSessions(t, ACPKind(nil, nil), opts)
			settings := map[string]any{"command": command}
			request := sdk.Request{Conversation: "existing", Model: "alternate", Effort: "xhigh", Messages: []sdk.Message{user("first")}}
			first := codexParts(t, m, settings, request)
			lastPart(t, first, "stop")
			if err := m.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			m = newTestSessions(t, ACPKind(nil, nil), opts)
			request.Messages = append(request.Messages, assistant(textParts(first)), user("second"))
			request.Effort = "low"
			second := codexParts(t, m, settings, request)
			lastPart(t, second, "length")
			if textParts(second) != "answer" {
				t.Fatalf("history replay leaked into live output: %+v", second)
			}
			var newDirectories, restoredDirectories []string
			var prompts, configurations []string
			for _, entry := range readCodexLog(t, log) {
				params := entry["params"].(map[string]any)
				switch entry["method"] {
				case "session/new":
					newDirectories = append(newDirectories, params["cwd"].(string))
				case "session/load", "session/resume":
					restoredDirectories = append(restoredDirectories, params["cwd"].(string))
				case "session/prompt":
					text := params["prompt"].([]any)[0].(map[string]any)["text"].(string)
					prompts = append(prompts, text)
				case "session/set_config_option":
					configurations = append(configurations, params["value"].(string))
				}
			}
			if len(newDirectories) == 0 || len(restoredDirectories) != 1 || restoredDirectories[0] != newDirectories[0] || len(prompts) != 2 {
				t.Fatalf("unexpected restore: new=%v restored=%v prompts=%q", newDirectories, restoredDirectories, prompts)
			}
			if restore == "rejected" {
				if len(newDirectories) != 2 || !strings.Contains(prompts[1], "<user>\nfirst") || !strings.Contains(prompts[1], "<user>\nsecond") {
					t.Fatalf("history was not rebuilt after rejected restore: %q", prompts)
				}
				if !reflect.DeepEqual(configurations, []string{"alternate", "extra", "alternate", "low"}) {
					t.Fatal(configurations)
				}
			} else if len(newDirectories) != 1 || prompts[1] != "second" ||
				!reflect.DeepEqual(configurations, []string{"alternate", "extra", "low"}) {
				t.Fatalf("old context was resent or a session option was lost: %q %v", prompts, configurations)
			}
		})
	}
}

func TestACPChild(t *testing.T) {
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
	model, effort := "base", "high"
	resumed := false
	mcpURL := ""
	options := func() json.RawMessage {
		modelOption := fmt.Sprintf(`{"id":"model-picker","type":"select","category":"model","currentValue":%q,"options":[{"group":"test","name":"Test","options":[{"value":"base","name":"Base"},{"value":"alternate","name":"Alternate"},{"value":"fixed","name":"Fixed"}]}]}`, model)
		if model == "fixed" {
			return json.RawMessage("[" + modelOption + "]")
		}
		choices := `{"value":"low"},{"value":"high"}`
		if model == "alternate" {
			choices += `,{"value":"extra"}`
		}
		return json.RawMessage(fmt.Sprintf(`[%s,{"id":"thinking","type":"select","category":"thought_level","currentValue":%q,"options":[%s]}]`, modelOption, effort, choices))
	}
	output := json.NewEncoder(os.Stdout)
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		if _, err := fmt.Fprintln(log, scanner.Text()); err != nil {
			t.Fatal(err)
		}
		var request struct {
			ID     json.RawMessage
			Method string
			Params struct {
				ConfigID, Value string
				MCPServers      []struct {
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
			capabilities := map[string]any{"loadSession": true}
			if restore != "session/load" {
				capabilities["sessionCapabilities"] = map[string]any{"resume": map[string]any{}}
			}
			if restore == "agy-midturn-usage" {
				capabilities["mcpCapabilities"] = map[string]any{"http": true}
			}
			hello := map[string]any{"protocolVersion": 1, "agentCapabilities": capabilities}
			if strings.HasPrefix(restore, "agy-") {
				hello["agentInfo"] = map[string]any{"name": antigravityACPName, "version": "test-1"}
			}
			result = hello
		case "session/new":
			if len(request.Params.MCPServers) > 0 {
				mcpURL = request.Params.MCPServers[0].URL
			}
			result = map[string]any{"sessionId": "test-session", "configOptions": options()}
		case "session/load", "session/resume":
			if restore == "rejected" {
				_ = output.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32002, "message": "unknown session"}})
				continue
			}
			if restore == "session/load" {
				_ = output.Encode(json.RawMessage(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"old history"}}}}`))
			}
			model, effort = "alternate", "extra"
			resumed = true
			result = map[string]any{"configOptions": options()}
		case "session/set_config_option":
			switch request.Params.ConfigID {
			case "model-picker":
				model = request.Params.Value
			case "thinking":
				effort = request.Params.Value
			}
			result = map[string]any{"configOptions": options()}
		case "session/prompt":
			if restore == "agy-rpc-error" {
				_ = output.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32603, "message": "synthetic ACP failure", "data": map[string]any{"requestId": "request-id-42"}}})
				continue
			}
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
			case "agy-error", "other-agent":
				updates = []childUpdate{{kind: "agent_message_chunk", text: "\n⚠ agent executor error: generating and executing: Error 400, Message: synthetic Gemini request rejected\n"}}
			case "agy-executor-start":
				updates = []childUpdate{{kind: "agent_message_chunk", text: "\n⚠ failed to construct executor: plan model not specified; you must specify a valid model\n\n"}}
			case "agy-quota", "other-agent-quota":
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
			stop := "end_turn"
			if effort == "low" {
				stop = "max_tokens"
			}
			response := map[string]any{"stopReason": stop}
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
