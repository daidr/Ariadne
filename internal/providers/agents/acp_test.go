package agents

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/sdk"
)

func TestACPStreamMatchesLegacyThroughSameChildProtocol(t *testing.T) {
	bridge := newTestBridge(t)
	compareLegacyAgent(t, "acp", "acp", ACPKind(bridge, nil))
}
func TestACPToolPermissionBoundary(t *testing.T) {
	tools := []sdk.Tool{{Name: "read"}}
	cases := []struct {
		raw     string
		allowed bool
	}{
		{`{"title":"mcp__ariadne__read"}`, true},
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
		{`null`, false},
	}
	for _, test := range cases {
		if got := ForConversationTools(json.RawMessage(test.raw), tools); got != test.allowed {
			t.Fatalf("%s: %v", test.raw, got)
		}
	}
	driver := &acpDriver{ctx: DriverContext{Tools: AgentTools{Specs: tools}}}
	for _, own := range []bool{false, true} {
		title := "shell"
		want := "no"
		if own {
			title = "ariadne/read"
			want = "yes"
		}
		params, _ := json.Marshal(map[string]any{"toolCall": map[string]any{"title": title}, "options": []any{map[string]any{"optionId": "always", "kind": "allow_always"}, map[string]any{"optionId": "yes", "kind": "allow_once"}, map[string]any{"optionId": "never", "kind": "reject_always"}, map[string]any{"optionId": "no", "kind": "reject_once"}}})
		answer, err := driver.request(t.Context(), "session/request_permission", params)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(answer)
		if !strings.Contains(string(data), `"optionId":"`+want+`"`) {
			t.Fatal(string(data))
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
	d := &acpDriver{ctx: DriverContext{Emit: func(event Event) { events = append(events, event) }}}
	for _, chunk := range []string{"thin", "king"} {
		data, _ := json.Marshal(map[string]any{"update": map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]any{"type": "text", "text": chunk}}})
		d.notification("session/update", data)
	}
	d.notification("session/update", []byte(`{"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"answer"}}}`))
	if len(events) != 4 || events[2].Type != "reasoning" || events[2].Text != "thinking" || events[3].Text != "answer" || d.thought.Len() != 0 {
		t.Fatal(events)
	}
	d.closing = true
	d.notification("session/update", []byte(`{"update":{"sessionUpdate":"tool_call"}}`))
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
