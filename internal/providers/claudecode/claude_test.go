package claudecode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/daidr/Ariadne/internal/agentruntime"
	"github.com/daidr/Ariadne/sdk"
)

func tokenValue(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func TestClaudeStreamCountingAndLimits(t *testing.T) {
	var events []agentruntime.Event
	d := &claudeDriver{ctx: agentruntime.DriverContext{Emit: func(event agentruntime.Event) { events = append(events, event) }}, seen: map[string]claudeTokens{}, streamed: map[string]bool{}}
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
	var events []agentruntime.Event
	d := &claudeDriver{ctx: agentruntime.DriverContext{Emit: func(event agentruntime.Event) { events = append(events, event) }}, seen: map[string]claudeTokens{}, streamed: map[string]bool{}}
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
	var usages []agentruntime.Usage
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
	models := ParseModels(json.RawMessage(`[{"value":"default","displayName":"Default","resolvedModel":"real","supportedEffortLevels":["max","minimal","low"]},{"value":"alias","displayName":"Real Name","resolvedModel":"real","supportedEffortLevels":["low","high"]},{"value":"other","supportsEffort":false}]`))
	if len(models) != 3 || models[0].Title != "Real Name" || models[0].Resolves != "real" || !reflect.DeepEqual(*models[0].Efforts, []string{"low", "max"}) || models[2].Efforts == nil || len(*models[2].Efforts) != 0 {
		t.Fatal(models)
	}
	old := ParseModels(json.RawMessage(`[{"value":"model"}]`))
	if old[0].Efforts != nil {
		t.Fatal("old CLI effort support should be unknown")
	}
	dir := t.TempDir()
	bridge := agentruntime.NewToolBridge()
	defer bridge.Close(context.Background())
	driver, err := Kind(bridge).Create(agentruntime.DriverContext{Settings: map[string]any{"command": "this-command-does-not-exist-ariadne"}, Directory: dir, System: "instructions", Tools: agentruntime.AgentTools{Specs: []sdk.Tool{{Name: "read"}}, Call: func(context.Context, string, any) agentruntime.ToolOutput { return agentruntime.ToolOutput{} }}, Emit: func(agentruntime.Event) {}})
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

func TestFindProvidersRespectsClaudeConfigDir(t *testing.T) {
	whichYes := func(program string) bool { return program == DefaultCommand }

	t.Run("custom CLAUDE_CONFIG_DIR with .claude.json only", func(t *testing.T) {
		home := t.TempDir()
		custom := t.TempDir()
		if err := os.WriteFile(filepath.Join(custom, ".claude.json"), []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
		found := findProviders(sdk.FindContext{
			Home:  home,
			Env:   map[string]string{"CLAUDE_CONFIG_DIR": custom},
			Which: whichYes,
		})
		if len(found) != 1 || found[0].ID != ProviderKind {
			t.Fatalf("expected claude-code to be discovered from CLAUDE_CONFIG_DIR/.claude.json, got %+v", found)
		}
	})

	t.Run("custom CLAUDE_CONFIG_DIR with .credentials.json only", func(t *testing.T) {
		home := t.TempDir()
		custom := t.TempDir()
		if err := os.WriteFile(filepath.Join(custom, ".credentials.json"), []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
		found := findProviders(sdk.FindContext{
			Home:  home,
			Env:   map[string]string{"CLAUDE_CONFIG_DIR": custom},
			Which: whichYes,
		})
		if len(found) != 1 || found[0].ID != ProviderKind {
			t.Fatalf("expected claude-code to be discovered from CLAUDE_CONFIG_DIR/.credentials.json, got %+v", found)
		}
	})

	t.Run("empty custom CLAUDE_CONFIG_DIR does not fall back to HOME/.claude.json", func(t *testing.T) {
		home := t.TempDir()
		custom := t.TempDir()
		if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
		found := findProviders(sdk.FindContext{
			Home:  home,
			Env:   map[string]string{"CLAUDE_CONFIG_DIR": custom},
			Which: whichYes,
		})
		if len(found) != 0 {
			t.Fatalf("expected isolated empty CLAUDE_CONFIG_DIR not to match HOME/.claude.json, got %+v", found)
		}
	})

	t.Run("default HOME checks .claude/.credentials.json, .claude.json, and .claude/.claude.json", func(t *testing.T) {
		for _, rel := range []string{
			filepath.Join(".claude", ".credentials.json"),
			".claude.json",
			filepath.Join(".claude", ".claude.json"),
		} {
			home := t.TempDir()
			full := filepath.Join(home, rel)
			if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte("{}"), 0600); err != nil {
				t.Fatal(err)
			}
			found := findProviders(sdk.FindContext{
				Home:  home,
				Env:   map[string]string{},
				Which: whichYes,
			})
			if len(found) != 1 || found[0].ID != ProviderKind {
				t.Fatalf("expected claude-code to be discovered from %s, got %+v", rel, found)
			}
		}
	})

	t.Run("oauth token overrides empty directories", func(t *testing.T) {
		found := findProviders(sdk.FindContext{
			Home:  t.TempDir(),
			Env:   map[string]string{"CLAUDE_CONFIG_DIR": t.TempDir(), "CLAUDE_CODE_OAUTH_TOKEN": "tok"},
			Which: whichYes,
		})
		if len(found) != 1 || found[0].ID != ProviderKind {
			t.Fatalf("expected claude-code to be discovered via CLAUDE_CODE_OAUTH_TOKEN, got %+v", found)
		}
	})
}
