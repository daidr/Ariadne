package agents

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
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

func TestACPRejectedRestoreBoundsReplayAndKeepsLatestUserAndImages(t *testing.T) {
	command, log := acpChildCommand(t, "rejected-replay")
	opts := SessionOptions{DataDir: t.TempDir()}
	settings := map[string]any{"command": command}
	system := "SYS_HEAD " + strings.Repeat("指令内容", 1300) + " SYS_TAIL" // ~15.6 KiB system instructions

	// Build a long coordinator history with an early huge search result and old images.
	oldImg := sdk.Image{MIME: "image/png", Data: "T0xEX0lNRw=="}
	newImg := sdk.Image{MIME: "image/png", Data: "TkVXX0lNRw=="}
	messages := []sdk.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: "OLD_MSG_msg_01a1ff4e " + strings.Repeat("旧问题", 400), Images: []sdk.Image{oldImg}},
		{Role: "assistant", Content: "running search", ToolCalls: []sdk.ToolCall{{ID: "call_big_search", Name: "search", Arguments: `{"pattern":"foo"}`}}},
		{Role: "tool", ToolCallID: "call_big_search", Content: "HUGE_SEARCH_HEAD\n" + strings.Repeat("matched line\n", 14_000) + "\nHUGE_SEARCH_TAIL"},
		{Role: "assistant", Content: "old search summary"},
	}
	for i := 1; i <= 29; i++ {
		messages = append(messages,
			sdk.Message{Role: "user", Content: fmt.Sprintf("MID_TURN_%02d %s", i, strings.Repeat("历史轮次", 220))},
			sdk.Message{Role: "assistant", Content: fmt.Sprintf("MID_REPLY_%02d %s", i, strings.Repeat("历史回复", 220))},
		)
	}
	messages = append(messages, sdk.Message{Role: "user", Content: "MID_TURN_30 " + strings.Repeat("最新前序轮次", 100)})

	// Run the first turn with the long history prefix so the saved checkpoint
	// matches the prefix and the restarted session actually attempts session/resume.
	m := newTestSessions(t, ACPKind(nil, nil), opts)
	first := codexParts(t, m, settings, sdk.Request{
		Conversation: "coordinator",
		Model:        "alternate",
		Messages:     messages,
	})
	lastPart(t, first, "stop")
	if err := m.Close(t.Context()); err != nil {
		t.Fatal(err)
	}

	// Keep the exact checkpointed prefix, append the assistant reply and the new
	// triggering user message with a screenshot, and restart so session/resume is rejected.
	latest := "NEW_MSG_msg_01a1ffc8 请排查刚才截图里的问题"
	messages = append(messages, assistant(textParts(first)), sdk.Message{Role: "user", Content: latest, Images: []sdk.Image{newImg}})

	if err := os.Truncate(log, 0); err != nil {
		t.Fatal(err)
	}
	m = newTestSessions(t, ACPKind(nil, nil), opts)
	second := codexParts(t, m, settings, sdk.Request{
		Conversation: "coordinator",
		Model:        "alternate",
		Messages:     messages,
	})
	lastPart(t, second, "stop")

	var lifecycle []string
	var promptBlocks []any
	for _, entry := range readCodexLog(t, log) {
		method, _ := entry["method"].(string)
		switch method {
		case "session/resume", "session/load", "session/new", "session/prompt":
			lifecycle = append(lifecycle, method)
		}
		if method == "session/prompt" {
			promptBlocks = entry["params"].(map[string]any)["prompt"].([]any)
		}
	}
	wantLifecycle := []string{"session/resume", "session/new", "session/prompt"}
	if !reflect.DeepEqual(lifecycle, wantLifecycle) {
		t.Fatalf("expected rejected resume fallback sequence %v, got %v", wantLifecycle, lifecycle)
	}
	if len(promptBlocks) == 0 {
		t.Fatal("missing session/prompt RPC call")
	}
	text := promptBlocks[0].(map[string]any)["text"].(string)
	if len(text) > defaultHistoryReplayBytes {
		t.Fatalf("rebuilt ACP prompt (including <instructions>) exceeded replay budget: %d > %d", len(text), defaultHistoryReplayBytes)
	}
	if !strings.Contains(text, "SYS_HEAD") || !strings.Contains(text, "SYS_TAIL") {
		t.Fatal("system instructions were truncated")
	}
	if !strings.Contains(text, historyOmittedNote) {
		t.Fatal("expected omission note in rebuilt ACP history")
	}
	if strings.Contains(text, "OLD_MSG_msg_01a1ff4e") || strings.Contains(text, "HUGE_SEARCH_HEAD") {
		t.Fatal("old dropped message and huge search result should have been evicted from replay")
	}
	if !strings.Contains(text, "MID_TURN_30") || !strings.Contains(text, "<assistant>\n"+textParts(first)+"\n</assistant>") {
		t.Fatal("most recent turns before latest user message were not preserved")
	}
	if !strings.Contains(text, "<user>\n"+latest+"\n</user>") || !strings.HasSuffix(text, historyFooter+historyAnswerEnding) {
		t.Fatal("latest triggering user message was lost or truncated")
	}
	var imagePayloads []string
	for _, block := range promptBlocks[1:] {
		item := block.(map[string]any)
		if item["type"] == "image" {
			imagePayloads = append(imagePayloads, item["data"].(string))
		}
	}
	if !reflect.DeepEqual(imagePayloads, []string{newImg.Data}) {
		t.Fatalf("expected only kept message images, got %v", imagePayloads)
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
	scanner.Buffer(make([]byte, 4096), 4<<20)
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
			if restore == "rejected-replay" {
				capabilities["promptCapabilities"] = map[string]any{"image": true}
			}
			hello := map[string]any{"protocolVersion": 1, "agentCapabilities": capabilities}
			if strings.HasPrefix(restore, "agy-") {
				hello["agentInfo"] = map[string]any{"name": antigravityACPName, "version": "test-1"}
			}
			result = hello
		case "session/new":
			result = map[string]any{"sessionId": "test-session", "configOptions": options()}
		case "session/load", "session/resume":
			if restore == "rejected" || restore == "rejected-replay" {
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
