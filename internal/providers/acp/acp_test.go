package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/agentruntime"
	"github.com/daidr/Ariadne/sdk"
)

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
		t.Fatal("ACP stream timed out", result)
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

func readLog(t *testing.T, path string) []map[string]any {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 32<<20)
	messages := []map[string]any{}
	for scanner.Scan() {
		var message map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			t.Fatal(err)
		}
		messages = append(messages, message)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return messages
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
	driver := &acpDriver{ctx: agentruntime.DriverContext{Tools: agentruntime.AgentTools{Specs: tools}}, sessionID: "session", active: true}
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
	models := ParseModels(json.RawMessage(`{"currentModelId":"big","availableModels":[{"modelId":"default"},{"modelId":"big","name":"Big Model"},{"modelId":"small","name":"small"},{"modelId":" "}]}`))
	want := []sdk.ListedModel{{ID: "default", Title: "Big Model", Resolves: "big"}, {ID: "big", Title: "Big Model"}, {ID: "small"}}
	if !reflect.DeepEqual(models, want) {
		t.Fatal(models)
	}
	events := []agentruntime.Event{}
	d := &acpDriver{ctx: agentruntime.DriverContext{Emit: func(event agentruntime.Event) { events = append(events, event) }}, sessionID: "session", started: true, active: true}
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
	driver, err := Kind(nil, nil).Create(agentruntime.DriverContext{Settings: map[string]any{}})
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

func TestACPDoesNotInterceptWarningChunks(t *testing.T) {
	for _, scenario := range []string{"other-agent", "other-agent-quota"} {
		t.Run(scenario, func(t *testing.T) {
			command, _ := acpChildCommand(t, scenario)
			m := newTestSessions(t, Kind(nil, nil), agentruntime.SessionOptions{})
			parts := collectParts(t, m, map[string]any{"command": command}, sdk.Request{Conversation: "plain", Messages: []sdk.Message{user("hello")}})
			lastPart(t, parts, "stop")
			if !strings.Contains(textParts(parts), "⚠") {
				t.Fatalf("generic ACP should pass warning text through verbatim, got %+v", parts)
			}
		})
	}
}

func TestACPFailureIncludesRPCDiagnostics(t *testing.T) {
	command, _ := acpChildCommand(t, "rpc-error")
	m := newTestSessions(t, Kind(nil, nil), agentruntime.SessionOptions{})
	parts := collectParts(t, m, map[string]any{"command": command}, sdk.Request{Conversation: "diagnostics", Messages: []sdk.Message{user("hello")}})
	lastPart(t, parts, "error")
	detail := parts[len(parts)-1].AgentError
	if detail == nil || detail.Operation != "session/prompt" || detail.SessionMethod != "session/new" ||
		detail.ProtocolVersion != 1 || detail.Name != "Generic ACP Server" || detail.Version != "test-1" ||
		detail.RPCCode == nil || *detail.RPCCode != -32603 || !strings.Contains(detail.RPCData, "request-id-42") {
		t.Fatalf("missing ACP error details: %+v", detail)
	}
}

func TestACPMidTurnUsageBeforeFirstMessage(t *testing.T) {
	command, _ := acpChildCommand(t, "midturn-usage")
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
		t.Fatalf("unexpected final ACP usage after mid-turn update: %+v", finalUsage)
	}
}

func TestACPHistoryBudgetMinimumFloor(t *testing.T) {
	d := &acpDriver{}
	hugeSystem := strings.Repeat("S", agentruntime.DefaultHistoryReplayBytes+8192)
	if got := d.HistoryBudget(hugeSystem); got != agentruntime.MinHistoryReplayBytes {
		t.Fatalf("expected MinHistoryReplayBytes (%d) when system prompt exceeds default replay budget, got %d", agentruntime.MinHistoryReplayBytes, got)
	}
}

func TestACPRebuildAfterCancelDrainsSettledTurnUsage(t *testing.T) {
	command, _ := acpChildCommand(t, "cancel-usage")
	bridge := newTestBridge(t)
	m := newTestSessions(t, Kind(bridge, nil), agentruntime.SessionOptions{})
	settings := map[string]any{"command": command}
	tools := []sdk.Tool{{Name: "read_file", Description: "Read a file", Parameters: map[string]any{"properties": map[string]any{"path": map[string]any{"type": "string"}}}}}

	// Step 1 starts an ACP session and pauses mid-turn on a tool call.
	first := collectParts(t, m, settings, sdk.Request{
		Conversation: "thread-cancel-usage",
		Messages:     []sdk.Message{user("inspect repo")},
		Tools:        tools,
	})
	lastPart(t, first, "tool-calls")

	// Step 2 rewrites history (simulating compaction). Sessions cancels and
	// closes the old ACP process, drains its settled TurnUsage, and merges it
	// into the rebuilt session's usage without polluting ContextInput.
	second := collectParts(t, m, settings, sdk.Request{
		Conversation: "thread-cancel-usage",
		Messages: []sdk.Message{
			user("<history_summarised>Earlier turns summarised.</history_summarised>"),
			{Role: "assistant", Content: "<your_notes>Summary notes.</your_notes>"},
			user("continue after compaction"),
		},
		Tools: tools,
	})
	lastPart(t, second, "stop")
	var finalUsage *sdk.Part
	for i := range second {
		if second[i].Type == "usage" {
			finalUsage = &second[i]
		}
	}
	if finalUsage == nil ||
		tokenValue(finalUsage.Input) != 140000 ||
		tokenValue(finalUsage.Output) != 430 ||
		tokenValue(finalUsage.Cached) != 115000 ||
		tokenValue(finalUsage.Reasoning) != 25 ||
		tokenValue(finalUsage.ContextInput) != 18000 {
		t.Fatalf("expected drained pre-cancel usage merged with new session usage and ContextInput=18000, got %+v", finalUsage)
	}
}

func TestACPModernProtocolAndRestart(t *testing.T) {
	for _, restore := range []string{"session/resume", "session/load", "rejected"} {
		t.Run(restore, func(t *testing.T) {
			command, log := acpChildCommand(t, restore)
			models, err := ListModels(t.Context(), Settings{Command: command}, t.TempDir())
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
			opts := agentruntime.SessionOptions{DataDir: t.TempDir()}
			m := newTestSessions(t, Kind(nil, nil), opts)
			settings := map[string]any{"command": command}
			request := sdk.Request{Conversation: "existing", Model: "alternate", Effort: "xhigh", Messages: []sdk.Message{user("first")}}
			first := collectParts(t, m, settings, request)
			lastPart(t, first, "stop")
			if err := m.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			m = newTestSessions(t, Kind(nil, nil), opts)
			request.Messages = append(request.Messages, assistant(textParts(first)), user("second"))
			request.Effort = "low"
			second := collectParts(t, m, settings, request)
			lastPart(t, second, "length")
			if textParts(second) != "answer" {
				t.Fatalf("history replay leaked into live output: %+v", second)
			}
			var newDirectories, restoredDirectories []string
			var prompts, configurations []string
			for _, entry := range readLog(t, log) {
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
	opts := agentruntime.SessionOptions{DataDir: t.TempDir()}
	settings := map[string]any{"command": command}
	system := "SYS_HEAD " + strings.Repeat("指令内容", 1300) + " SYS_TAIL" // ~15.6 KiB system instructions

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

	m := newTestSessions(t, Kind(nil, nil), opts)
	first := collectParts(t, m, settings, sdk.Request{
		Conversation: "coordinator",
		Model:        "alternate",
		Messages:     messages,
	})
	lastPart(t, first, "stop")
	if err := m.Close(t.Context()); err != nil {
		t.Fatal(err)
	}

	latest := "NEW_MSG_msg_01a1ffc8 请排查刚才截图里的问题"
	messages = append(messages, assistant(textParts(first)), sdk.Message{Role: "user", Content: latest, Images: []sdk.Image{newImg}})

	if err := os.Truncate(log, 0); err != nil {
		t.Fatal(err)
	}
	m = newTestSessions(t, Kind(nil, nil), opts)
	second := collectParts(t, m, settings, sdk.Request{
		Conversation: "coordinator",
		Model:        "alternate",
		Messages:     messages,
	})
	lastPart(t, second, "stop")

	var lifecycle []string
	var promptBlocks []any
	for _, entry := range readLog(t, log) {
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
	if len(text) > agentruntime.DefaultHistoryReplayBytes {
		t.Fatalf("rebuilt ACP prompt (including <instructions>) exceeded replay budget: %d > %d", len(text), agentruntime.DefaultHistoryReplayBytes)
	}
	if !strings.Contains(text, "SYS_HEAD") || !strings.Contains(text, "SYS_TAIL") {
		t.Fatal("system instructions were truncated")
	}
	if !strings.Contains(text, "[Earlier messages were omitted to stay within the history replay limit.]") {
		t.Fatal("expected omission note in rebuilt ACP history")
	}
	if strings.Contains(text, "OLD_MSG_msg_01a1ff4e") || strings.Contains(text, "HUGE_SEARCH_HEAD") {
		t.Fatal("old dropped message and huge search result should have been evicted from replay")
	}
	if !strings.Contains(text, "MID_TURN_30") || !strings.Contains(text, "<assistant>\n"+textParts(first)+"\n</assistant>") {
		t.Fatal("most recent turns before latest user message were not preserved")
	}
	if !strings.Contains(text, "<user>\n"+latest+"\n</user>") || !strings.HasSuffix(text, "\n</history>\n\nAnswer the last user message.") {
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
	scanner.Buffer(make([]byte, 4096), 4<<20)
	for scanner.Scan() {
		if _, err := fmt.Fprintln(log, scanner.Text()); err != nil {
			t.Fatal(err)
		}
		var request struct {
			ID     json.RawMessage
			Method string
			Params struct {
				ConfigID, Value string
				Prompt          []struct {
					Text string `json:"text"`
				} `json:"prompt"`
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
			capabilities := map[string]any{"loadSession": true}
			if restore != "session/load" {
				capabilities["sessionCapabilities"] = map[string]any{"resume": map[string]any{}}
			}
			if restore == "rejected-replay" {
				capabilities["promptCapabilities"] = map[string]any{"image": true}
			}
			if restore == "midturn-usage" || restore == "cancel-usage" {
				capabilities["mcpCapabilities"] = map[string]any{"http": true}
			}
			hello := map[string]any{"protocolVersion": 1, "agentCapabilities": capabilities}
			if restore == "rpc-error" {
				hello["agentInfo"] = map[string]any{"name": "Generic ACP Server", "version": "test-1"}
			}
			result = hello
		case "session/new":
			if len(request.Params.MCPServers) > 0 {
				mcpURL = request.Params.MCPServers[0].URL
			}
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
			if restore == "rpc-error" {
				_ = output.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32603, "message": "synthetic ACP failure", "data": map[string]any{"requestId": "request-id-42"}}})
				continue
			}
			if restore == "cancel-usage" {
				promptText := ""
				for _, block := range request.Params.Prompt {
					promptText += block.Text
				}
				if !strings.Contains(promptText, "<history_summarised>") {
					_ = output.Encode(json.RawMessage(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"usage_update","used":42000,"size":400000}}}`))
					resp, err := http.Post(mcpURL, "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"README.md"}}}`))
					if err == nil {
						_, _ = io.ReadAll(resp.Body)
						resp.Body.Close()
					}
					result = map[string]any{
						"stopReason": "cancelled",
						"usage":      map[string]any{"inputTokens": 120000, "outputTokens": 350, "cachedReadTokens": 100000, "thoughtTokens": 25},
					}
					break
				}
				_ = output.Encode(json.RawMessage(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"usage_update","used":18000,"size":400000}}}`))
				_ = output.Encode(json.RawMessage(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"compacted reply"}}}}`))
				result = map[string]any{
					"stopReason": "end_turn",
					"usage":      map[string]any{"inputTokens": 20000, "outputTokens": 80, "cachedReadTokens": 15000},
				}
				break
			}
			if restore == "midturn-usage" {
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
					"usage":      map[string]any{"inputTokens": 170000, "outputTokens": 47, "cachedReadTokens": 167000, "thoughtTokens": 7, "totalTokens": 170047},
				}
				break
			}
			text := "answer"
			switch restore {
			case "other-agent":
				text = "\n⚠ agent executor error: generating and executing: Error 400, Message: synthetic Gemini request rejected\n"
			case "other-agent-quota":
				text = "\n⚠ Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 1h17m22s.\n"
			}
			_ = output.Encode(json.RawMessage(fmt.Sprintf(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":%q}}}}`, text)))
			stop := "end_turn"
			if effort == "low" {
				stop = "max_tokens"
			}
			result = map[string]any{"stopReason": stop}
		default:
			t.Fatalf("unexpected method: %s", request.Method)
		}
		if err := output.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}); err != nil {
			t.Fatal(err)
		}
	}
}
