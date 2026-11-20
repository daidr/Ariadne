package agents

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/sdk"
)

func TestACPStreamMatchesLegacyThroughSameChildProtocol(t *testing.T) {
	bridge := newTestBridge(t)
	kind := ACPKind(bridge, nil)
	// Match the frozen oracle's process lifecycle. In-session effort changes
	// and other modern ACP behavior are covered by TestACPModernProtocolAndRestart.
	kind.MutableEffort = false
	compareLegacyAgent(t, "acp", "acp", kind)
}
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
	for _, test := range []struct{ scenario, reason, detail string }{
		{"agy-error", "error", "synthetic Gemini request rejected"},
		{"agy-executor-start", "error", "plan model not specified"},
		{"agy-recovered", "stop", ""},
		{"other-agent", "stop", ""},
	} {
		t.Run(test.scenario, func(t *testing.T) {
			command, _ := acpChildCommand(t, test.scenario)
			m := newTestSessions(t, ACPKind(nil, nil), SessionOptions{})
			request := sdk.Request{Conversation: "errors", Messages: []sdk.Message{user("hello")}}
			parts := codexParts(t, m, map[string]any{"command": command}, request)
			lastPart(t, parts, test.reason)
			if test.reason == "error" && (textParts(parts) != "" ||
				!strings.Contains(parts[len(parts)-1].Error, test.detail) ||
				parts[len(parts)-1].Retryable) {
				t.Fatalf("unexpected executor failure: %+v", parts)
			}
		})
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
			Params struct{ ConfigID, Value string }
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
			hello := map[string]any{"protocolVersion": 1, "agentCapabilities": capabilities}
			if strings.HasPrefix(restore, "agy-") {
				hello["agentInfo"] = map[string]any{"name": antigravityACPName}
			}
			result = hello
		case "session/new":
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
			text := "answer"
			switch restore {
			case "agy-error", "other-agent":
				text = "\n⚠ agent executor error: generating and executing: Error 400, Message: synthetic Gemini request rejected\n"
			case "agy-executor-start":
				text = "\n⚠ failed to construct executor: plan model not specified; you must specify a valid model\n\n"
			case "agy-recovered":
				text = "\n⚠ API error (attempt 1): Error 429. Retrying.\nanswer"
			}
			_ = output.Encode(json.RawMessage(fmt.Sprintf(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":%q}}}}`, text)))
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
