package agents

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/daidr/Ariadne/sdk"
)

// A native executable, not a shell mock: exercises pipes, process lifetime,
// bidirectional requests and the Windows Job Object used in production.
func TestCodexChild(t *testing.T) {
	index := slices.Index(os.Args, "--codex-child")
	if index < 0 {
		return
	}
	logFile := os.Args[index+1]
	if slices.Contains(os.Args, "debug") {
		fmt.Fprintln(os.Stdout, `{"models":[
			{"slug":"big","apply_patch_tool_type":"freeform","experimental_supported_tools":["clock"],
			 "supports_search_tool":true,"tool_mode":"code_mode_only","multi_agent_version":"v2","context_window":32000},
			{"slug":"small","apply_patch_tool_type":"function","context_window":16000}
		]}`)
		os.Exit(0)
	}
	catalogReady := false
	for _, arg := range os.Args {
		if rawPath, ok := strings.CutPrefix(arg, "model_catalog_json="); ok {
			var path string
			var catalog struct{ Models []map[string]any }
			if json.Unmarshal([]byte(rawPath), &path) != nil {
				os.Exit(4)
			}
			data, err := os.ReadFile(path)
			if err != nil || json.Unmarshal(data, &catalog) != nil || len(catalog.Models) != 2 {
				os.Exit(4)
			}
			for i, model := range catalog.Models {
				tools, ok := model["experimental_supported_tools"].([]any)
				if !ok || len(tools) != 0 || model["apply_patch_tool_type"] != nil || model["multi_agent_version"] != nil ||
					model["context_window"] != []float64{32000, 16000}[i] || model["supports_search_tool"] != false || model["tool_mode"] != "direct" {
					os.Exit(4)
				}
			}
			catalogReady = true
		}
	}
	var writeMu, mu sync.Mutex
	next := 1000
	waiting := map[int]chan json.RawMessage{}
	write := func(value any) {
		data, _ := json.Marshal(value)
		writeMu.Lock()
		fmt.Fprintln(os.Stdout, string(data))
		writeMu.Unlock()
	}
	ask := func(method string, params any) json.RawMessage {
		mu.Lock()
		id := next
		next++
		result := make(chan json.RawMessage, 1)
		waiting[id] = result
		mu.Unlock()
		write(map[string]any{"id": id, "method": method, "params": params})
		return <-result
	}
	notify := func(method string, params any) { write(map[string]any{"method": method, "params": params}) }
	inputTokens, outputTokens, cachedTokens := int64(0), int64(0), int64(0)
	turn := 0
	lastEffort := ""
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 32<<20)
	for scanner.Scan() {
		var message struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(scanner.Bytes(), &message) != nil {
			os.Exit(2)
		}
		if message.Method == "" {
			mu.Lock()
			if result := waiting[message.ID]; result != nil {
				result <- message.Result
				delete(waiting, message.ID)
			}
			mu.Unlock()
			continue
		}
		if (message.Method == "thread/start" || message.Method == "thread/resume") && !catalogReady {
			os.Exit(4)
		}
		file, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			os.Exit(3)
		}
		file.Write(append(append([]byte{}, scanner.Bytes()...), '\n'))
		file.Close()
		response := func(result any) { write(map[string]any{"id": message.ID, "result": result}) }
		switch message.Method {
		case "initialize":
			agent := "codex/0.167.1"
			if slices.Contains(os.Args, "--old") {
				agent = "codex/0.166.0"
			}
			response(map[string]any{"userAgent": agent})
		case "initialized":
		case "config/read":
			response(map[string]any{"config": map[string]any{"mcp_servers": map[string]any{"personal.mcp": map[string]any{"enabled": true}}}})
		case "model/list":
			var params map[string]any
			json.Unmarshal(message.Params, &params)
			if params["cursor"] == "next" {
				response(map[string]any{"data": []any{map[string]any{"model": "small", "displayName": "Small", "supportedReasoningEfforts": []any{}}}, "nextCursor": nil})
			} else {
				response(map[string]any{"data": []any{map[string]any{"id": "internal", "model": "big", "displayName": "Big", "supportedReasoningEfforts": []any{map[string]any{"reasoningEffort": "high"}, "low", "minimal"}}}, "nextCursor": "next"})
			}
		case "thread/start":
			response(map[string]any{"thread": map[string]any{"id": "thread"}, "reasoningEffort": "medium"})
		case "thread/resume":
			data, err := os.ReadFile(logFile + ".usage")
			if err != nil {
				write(map[string]any{"id": message.ID, "error": map[string]any{"code": -32600, "message": "missing thread"}})
			} else {
				var saved []int64
				json.Unmarshal(data, &saved)
				mu.Lock()
				inputTokens, outputTokens, cachedTokens = saved[0], saved[1], saved[2]
				mu.Unlock()
				response(map[string]any{"thread": map[string]any{"id": "thread"}})
			}
		case "thread/inject_items", "thread/archive":
			response(map[string]any{})
		case "turn/settings/update":
			if slices.Contains(os.Args, "--fallback") {
				write(map[string]any{"id": message.ID, "error": map[string]any{"code": -32600, "message": "the active model has only fallback metadata"}})
			} else {
				var params struct {
					Effort string `json:"effort"`
				}
				json.Unmarshal(message.Params, &params)
				mu.Lock()
				lastEffort = params.Effort
				mu.Unlock()
				response(map[string]any{})
			}
		case "turn/start":
			var params struct {
				Input  []struct{ Type, Text string } `json:"input"`
				Effort string                        `json:"effort"`
			}
			json.Unmarshal(message.Params, &params)
			mu.Lock()
			turn++
			number := turn
			lastEffort = params.Effort
			mu.Unlock()
			response(map[string]any{"turn": map[string]any{"id": fmt.Sprint("turn", number)}})
			go func() {
				text := ""
				for _, item := range params.Input {
					if item.Type == "text" {
						text = item.Text
					}
				}
				if text == "SLOW" {
					return
				}
				var approval struct {
					Decision string `json:"decision"`
				}
				json.Unmarshal(ask("item/commandExecution/requestApproval", map[string]any{}), &approval)
				answer := "hello 中文 " + approval.Decision
				if strings.HasPrefix(text, "CALL") {
					var output struct {
						Success bool                                    `json:"success"`
						Items   []struct{ Type, Text, ImageURL string } `json:"contentItems"`
					}
					json.Unmarshal(ask("item/tool/call", map[string]any{"tool": "read", "arguments": map[string]any{"path": "中文"}}), &output)
					answer = "result " + output.Items[0].Text
				}
				mu.Lock()
				inputTokens += 100
				outputTokens += 5
				cachedTokens += 80
				in, out, cached, effort := inputTokens, outputTokens, cachedTokens, lastEffort
				mu.Unlock()
				saved, _ := json.Marshal([]int64{in, out, cached})
				os.WriteFile(logFile+".usage", saved, 0600)
				notify("thread/tokenUsage/updated", map[string]any{"tokenUsage": map[string]any{"total": map[string]any{"inputTokens": in, "outputTokens": out, "cachedInputTokens": cached}, "last": map[string]any{"inputTokens": 100}}})
				if strings.HasPrefix(text, "FAIL") {
					notify("turn/completed", map[string]any{"turn": map[string]any{"status": "failed", "error": map[string]any{"message": "rate limit exceeded"}}})
					return
				}
				notify("item/completed", map[string]any{"item": map[string]any{"type": "reasoning", "summary": []string{"thinking"}, "content": []string{"more"}}})
				answer += " effort " + effort
				notify("item/agentMessage/delta", map[string]any{"itemId": "m", "delta": answer})
				notify("item/completed", map[string]any{"item": map[string]any{"type": "agentMessage", "id": "m", "text": answer}})
				notify("turn/completed", map[string]any{"turn": map[string]any{"status": "completed"}})
			}()
		}
	}
	os.Exit(0)
}

func codexTestCommand(t *testing.T, extra ...string) (string, string) {
	t.Helper()
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "rpc.jsonl")
	args := []string{program, "-test.run=^TestCodexChild$", "--", "--codex-child", log}
	args = append(args, extra...)
	for i, arg := range args {
		if strings.ContainsAny(arg, " \t") {
			args[i] = `"` + arg + `"`
		}
	}
	return strings.Join(args, " "), log
}
func codexParts(t *testing.T, m *Sessions, settings map[string]any, request sdk.Request) []sdk.Part {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	result := []sdk.Part{}
	for part := range m.Stream(ctx, settings, request) {
		result = append(result, part)
	}
	if ctx.Err() != nil {
		t.Fatal("Codex stream timed out", result)
	}
	return result
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
func readCodexLog(t *testing.T, path string) []map[string]any {
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
func methodCount(messages []map[string]any, method string) int {
	count := 0
	for _, message := range messages {
		if message["method"] == method {
			count++
		}
	}
	return count
}

func TestCodexNativeProtocolAndConversationReuse(t *testing.T) {
	command, log := codexTestCommand(t)
	settings := map[string]any{"command": command}
	m := newTestSessions(t, CodexKind(), SessionOptions{})
	request := sdk.Request{Conversation: "native", Messages: []sdk.Message{user("hello")}, Tools: []sdk.Tool{{Name: "read", Description: "Read a file", Parameters: map[string]any{"properties": map[string]any{"path": map[string]any{"type": "string"}}}}}}
	first := codexParts(t, m, settings, request)
	lastPart(t, first, "stop")
	text := textParts(first)
	if text != "hello 中文 decline effort medium" {
		t.Fatal("duplicated or lost text", first)
	}
	for _, part := range first {
		if part.Type == "usage" {
			if *part.Input != 100 || *part.Output != 5 || *part.Cached != 80 || *part.ContextInput != 100 {
				t.Fatal("bad usage", part)
			}
		}
	}
	request.Messages = append(request.Messages, assistant(text), user("second"))
	request.Effort = "high"
	second := codexParts(t, m, settings, request)
	lastPart(t, second, "stop")
	if textParts(second) != "hello 中文 decline effort high" {
		t.Fatal(second)
	}
	for _, part := range second {
		if part.Type == "usage" && *part.Input != 100 {
			t.Fatal("counted cumulative usage again")
		}
	}
	messages := readCodexLog(t, log)
	if methodCount(messages, "thread/start") != 1 || methodCount(messages, "turn/start") != 2 || methodCount(messages, "thread/inject_items") != 0 {
		t.Fatal(messages)
	}
	for _, message := range messages {
		if message["method"] == "thread/start" {
			params := message["params"].(map[string]any)
			server := params["config"].(map[string]any)["mcp_servers"].(map[string]any)["personal.mcp"].(map[string]any)
			if server["enabled"] != false {
				t.Fatal(server)
			}
			if len(params["environments"].([]any)) != 0 || params["ephemeral"] != false || params["baseInstructions"] != PlaceNote || params["sandbox"] != "read-only" {
				t.Fatal(params)
			}
			if _, ok := params["model"]; ok {
				t.Fatal("default model must stay unspecified")
			}
		}
	}
}

func TestCodexToolReturnAndMutableEffort(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		t.Run(fmt.Sprint(fallback), func(t *testing.T) {
			extra := []string{}
			if fallback {
				extra = append(extra, "--fallback")
			}
			command, log := codexTestCommand(t, extra...)
			m := newTestSessions(t, CodexKind(), SessionOptions{})
			settings := map[string]any{"command": command}
			request := sdk.Request{Conversation: "tools", Effort: "low", Messages: []sdk.Message{user("CALL read")}, Tools: []sdk.Tool{{Name: "read"}}}
			first := codexParts(t, m, settings, request)
			lastPart(t, first, "tool-calls")
			var call sdk.ToolCall
			for _, part := range first {
				if part.Type == "tool-call" {
					call = sdk.ToolCall{ID: part.ID, Name: part.Name, Arguments: part.Arguments}
				}
			}
			if call.ID == "" || call.Arguments != `{"path":"中文"}` {
				t.Fatal(first)
			}
			request.Messages = append(request.Messages, sdk.Message{Role: "assistant", ToolCalls: []sdk.ToolCall{call}}, sdk.Message{Role: "tool", ToolCallID: call.ID, Content: "文件内容", Images: []sdk.Image{{MIME: "image/png", Data: "AAAA"}}})
			request.Effort = "high"
			second := codexParts(t, m, settings, request)
			lastPart(t, second, "stop")
			want := "result 文件内容 effort high"
			if fallback {
				want = "result 文件内容 effort low"
			}
			if textParts(second) != want {
				t.Fatal(second)
			}
			messages := readCodexLog(t, log)
			if methodCount(messages, "thread/start") != 1 || methodCount(messages, "turn/start") != 1 || methodCount(messages, "turn/settings/update") != 1 {
				t.Fatal(messages)
			}
		})
	}
}

func TestCodexCheckpointAcrossProcessRestart(t *testing.T) {
	command, log := codexTestCommand(t)
	home := t.TempDir()
	settings := map[string]any{"command": command}
	m := newTestSessions(t, CodexKind(), SessionOptions{DataDir: home})
	request := sdk.Request{Conversation: "persist", Messages: []sdk.Message{user("first")}}
	first := codexParts(t, m, settings, request)
	lastPart(t, first, "stop")
	if err := m.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	m = newTestSessions(t, CodexKind(), SessionOptions{DataDir: home})
	request.Messages = append(request.Messages, assistant(textParts(first)), user("second"))
	second := codexParts(t, m, settings, request)
	lastPart(t, second, "stop")
	for _, part := range second {
		if part.Type == "usage" && (*part.Input != 100 || *part.Cached != 80) {
			t.Fatal("resume usage recounted previous turns", part)
		}
	}
	messages := readCodexLog(t, log)
	if methodCount(messages, "thread/start") != 1 || methodCount(messages, "thread/resume") != 1 || methodCount(messages, "thread/archive") != 0 {
		t.Fatal(messages)
	}
}

func TestCodexNativeHistoryAndModels(t *testing.T) {
	command, log := codexTestCommand(t)
	models, err := ListCodexModels(t.Context(), CodexSettings{Command: command}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 3 || models[1].ID != "big" || models[1].Efforts == nil || strings.Join(*models[1].Efforts, ",") != "low,high" || models[2].Efforts == nil || len(*models[2].Efforts) != 0 {
		t.Fatal(models)
	}
	m := newTestSessions(t, CodexKind(), SessionOptions{})
	request := sdk.Request{Conversation: "history", Messages: []sdk.Message{user("old"), {Role: "assistant", Content: "answer", ToolCalls: []sdk.ToolCall{{ID: "call_old", Name: "read", Arguments: `{"a":1}`}}}, {Role: "tool", ToolCallID: "call_old", Content: "result", Images: []sdk.Image{{MIME: "image/png", Data: "AAAA"}}}, user("new")}}
	lastPart(t, codexParts(t, m, map[string]any{"command": command}, request), "stop")
	messages := readCodexLog(t, log)
	if methodCount(messages, "thread/inject_items") != 1 {
		t.Fatal(messages)
	}
	for _, message := range messages {
		if message["method"] == "thread/inject_items" {
			params := message["params"].(map[string]any)
			items := params["items"].([]any)
			if len(items) != 4 || items[2].(map[string]any)["type"] != "function_call" || items[3].(map[string]any)["call_id"] != "call_old" {
				t.Fatal(items)
			}
		}
	}
}

func TestCodexErrorsAndCancellation(t *testing.T) {
	command, _ := codexTestCommand(t, "--old")
	m := newTestSessions(t, CodexKind(), SessionOptions{})
	request := sdk.Request{Conversation: "old", Messages: []sdk.Message{user("hello")}}
	result := codexParts(t, m, map[string]any{"command": command}, request)
	lastPart(t, result, "error")
	if !strings.Contains(result[len(result)-1].Error, "0.167.1") || m.Count() != 0 {
		t.Fatal(result)
	}
	command, _ = codexTestCommand(t)
	request.Messages = []sdk.Message{user("FAIL now")}
	result = codexParts(t, m, map[string]any{"command": command}, request)
	lastPart(t, result, "error")
	if !result[len(result)-1].Busy || !result[len(result)-1].Retryable {
		t.Fatal(result)
	}
	request.Messages = []sdk.Message{user("SLOW")}
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	for range m.Stream(ctx, map[string]any{"command": command}, request) {
	}
	if ctx.Err() == nil || m.Count() != 0 {
		t.Fatal("cancelled Codex process retained")
	}
}

func TestCodexTokenUsageEmitsContextInputWithoutTotalGrowth(t *testing.T) {
	var events []Event
	d := &codexDriver{
		ctx:     DriverContext{Emit: func(event Event) { events = append(events, event) }},
		started: true,
		total:   tokenBreakdown{Input: new(int64(20000)), Output: new(int64(100))},
	}
	raw := json.RawMessage(`{"tokenUsage":{"total":{"inputTokens":20000,"outputTokens":100},"last":{"inputTokens":24500}}}`)
	d.notification("thread/tokenUsage/updated", raw)
	if len(events) != 1 || events[0].Type != "usage" || tokenValue(events[0].Usage.ContextInput) != 24500 ||
		events[0].Usage.Input != 0 || events[0].Usage.Output != 0 {
		t.Fatalf("expected contextInput-only usage event, got %+v", events)
	}
}
