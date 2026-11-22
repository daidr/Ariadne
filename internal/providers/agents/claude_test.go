package agents

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/daidr/Ariadne/sdk"
)

func TestClaudeStreamCountingAndLimits(t *testing.T) {
	var events []Event
	d := &claudeDriver{ctx: DriverContext{Emit: func(event Event) { events = append(events, event) }}, seen: map[string]claudeTokens{}, streamed: map[string]bool{}}
	lines := []string{
		`{"type":"assistant","message":{"id":"a","content":[{"type":"thinking","thinking":"thought"}],"usage":{"input_tokens":10,"cache_read_input_tokens":80,"cache_creation_input_tokens":10,"output_tokens":1}}}`,
		`{"type":"stream_event","event":{"type":"message_start","message":{"id":"a"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"hello"}}}`,
		`{"type":"assistant","message":{"id":"a","content":[{"type":"text","text":"hello"}],"usage":{"input_tokens":10,"cache_read_input_tokens":80,"cache_creation_input_tokens":10,"output_tokens":3}}}`,
		`{"type":"result","subtype":"success","total_cost_usd":0.1,"usage":{"input_tokens":10,"cache_read_input_tokens":80,"cache_creation_input_tokens":10,"output_tokens":7,"output_tokens_details":{"thinking_tokens":2}}}`,
	}
	for _, line := range lines {
		if err := d.onLine([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	input, output, cached, written, reasoning := int64(0), int64(0), int64(0), int64(0), int64(0)
	text := ""
	cost := 0.
	for _, event := range events {
		if event.Type == "usage" {
			input += event.Usage.Input
			output += event.Usage.Output
			cached += tokenValue(event.Usage.Cached)
			written += tokenValue(event.Usage.CacheWrite)
			reasoning += tokenValue(event.Usage.Reasoning)
			if event.Usage.CostUSD != nil {
				cost += *event.Usage.CostUSD
			}
		}
		if event.Type == "text" {
			text += event.Text
		}
	}
	if input != 100 || output != 7 || cached != 80 || written != 10 || reasoning != 2 || text != "hello" || cost != .1 {
		t.Fatal(events)
	}
	if len(d.seen) != 0 || len(d.streamed) != 0 || d.writing != "" {
		t.Fatal("turn retained message state")
	}
	if err := d.onLine([]byte(`{"type":"result","subtype":"success","total_cost_usd":0.15}`)); err != nil {
		t.Fatal(err)
	}
	usage := events[len(events)-2].Usage
	if usage.CostUSD == nil || *usage.CostUSD < .0499 || *usage.CostUSD > .0501 {
		t.Fatal("cumulative cost counted twice", events)
	}
	if err := d.onLine([]byte(`{"type":"result","subtype":"error","result":"too many requests"}`)); err != nil {
		t.Fatal(err)
	}
	if event := events[len(events)-1]; event.Type != "failed" || !event.Busy || !event.Retryable {
		t.Fatal(event)
	}
	for i := range 1024 {
		d.seen[string(rune(i))] = claudeTokens{}
	}
	if err := d.onLine([]byte(`{"type":"assistant","message":{"id":"overflow","usage":{}}}`)); err == nil {
		t.Fatal("unbounded usage map")
	}
}

func TestClaudeMessageStartAndContextInputUsage(t *testing.T) {
	var events []Event
	d := &claudeDriver{ctx: DriverContext{Emit: func(event Event) { events = append(events, event) }}, seen: map[string]claudeTokens{}, streamed: map[string]bool{}}
	lines := []string{
		`{"type":"stream_event","event":{"type":"message_start","message":{"id":"m1","usage":{"input_tokens":10,"cache_read_input_tokens":8000,"cache_creation_input_tokens":500,"output_tokens":1}}}}`,
		`{"type":"assistant","message":{"id":"m1","content":[{"type":"tool_use"}],"usage":{"input_tokens":10,"cache_read_input_tokens":8000,"cache_creation_input_tokens":500,"output_tokens":45}}}`,
		`{"type":"result","subtype":"success","total_cost_usd":0.12,"usage":{"input_tokens":10,"cache_read_input_tokens":8000,"cache_creation_input_tokens":500,"output_tokens":47}}`,
	}
	for _, line := range lines {
		if err := d.onLine([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	var usages []Usage
	for _, event := range events {
		if event.Type == "usage" {
			usages = append(usages, event.Usage)
		}
	}
	if len(usages) != 4 {
		t.Fatalf("expected 4 usage events (message_start, assistant delta, result delta, cost), got %+v", usages)
	}
	if usages[0].Input != 8510 || usages[0].Output != 1 || tokenValue(usages[0].ContextInput) != 8510 {
		t.Fatalf("message_start usage mismatch: %+v", usages[0])
	}
	if usages[1].Input != 0 || usages[1].Output != 44 || tokenValue(usages[1].ContextInput) != 8510 {
		t.Fatalf("assistant follow-up must keep ContextInput without double counting input: %+v", usages[1])
	}
	if usages[2].Input != 0 || usages[2].Output != 2 || usages[2].ContextInput != nil {
		t.Fatalf("result delta must not set ContextInput: %+v", usages[2])
	}
	if usages[3].CostUSD == nil || *usages[3].CostUSD < .1199 || usages[3].ContextInput != nil {
		t.Fatalf("cost-only usage must not set ContextInput: %+v", usages[3])
	}
}

func TestClaudeModelsAndPromptShape(t *testing.T) {
	models := ClaudeModels(json.RawMessage(`[{"value":"default","displayName":"Default","resolvedModel":"real","supportedEffortLevels":["max","minimal","low"]},{"value":"alias","displayName":"Real Name","resolvedModel":"real","supportedEffortLevels":["low","high"]},{"value":"other","supportsEffort":false}]`))
	if len(models) != 3 || models[0].Title != "Real Name" || models[0].Resolves != "real" || !reflect.DeepEqual(*models[0].Efforts, []string{"low", "max"}) || models[2].Efforts == nil || len(*models[2].Efforts) != 0 {
		t.Fatal(models)
	}
	old := ClaudeModels(json.RawMessage(`[{"value":"model"}]`))
	if old[0].Efforts != nil {
		t.Fatal("old CLI effort support should be unknown")
	}
	dir := t.TempDir()
	bridge := NewToolBridge()
	defer bridge.Close(context.Background())
	driver, err := ClaudeKind(bridge).Create(DriverContext{Settings: map[string]any{"command": "this-command-does-not-exist-ariadne"}, Directory: dir, System: "instructions", Tools: AgentTools{Specs: []sdk.Tool{{Name: "read"}}, Call: func(context.Context, string, any) ToolOutput { return ToolOutput{} }}, Emit: func(Event) {}})
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.Start(t.Context()); err == nil {
		t.Fatal("missing command unexpectedly launched")
	}
	if bridge.Count() != 1 {
		t.Fatal("tool registration missing")
	}
	if err := driver.Close(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	if bridge.Count() != 0 {
		t.Fatal("failed startup retained its tools")
	}
	data, err := os.ReadFile(filepath.Join(dir, "system.md"))
	if err != nil || string(data) != "instructions" {
		t.Fatal(string(data), err)
	}
	var config map[string]any
	data, err = os.ReadFile(filepath.Join(dir, "mcp.json"))
	if err != nil || json.Unmarshal(data, &config) != nil {
		t.Fatal(err)
	}
}
