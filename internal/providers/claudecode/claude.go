package claudecode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/daidr/Ariadne/internal/agentruntime"
	"github.com/daidr/Ariadne/internal/jsonutil"
	"github.com/daidr/Ariadne/internal/process"
	"github.com/daidr/Ariadne/internal/rpc"
	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/sdk"
)

type Settings struct {
	Command string `json:"command,omitempty"`
}

func Argv(settings Settings, model, effort string) []string {
	command := textutil.Trim(settings.Command)
	if command == "" {
		command = "claude"
	}
	argv := append(process.SplitCommand(command), "-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--include-partial-messages")
	if model != "" && model != "default" {
		argv = append(argv, "--model", model)
	}
	if effort != "" {
		argv = append(argv, "--effort", effort)
	}
	return append(argv, "--tools", "", "--allowedTools", "mcp__"+agentruntime.BridgeServerName, "--permission-mode", "dontAsk", "--strict-mcp-config", "--setting-sources", "", "--settings", `{"disableAllHooks":true}`, "--disable-slash-commands", "--no-session-persistence")
}

type claudeModel struct {
	Value    json.RawMessage `json:"value"`
	Title    json.RawMessage `json:"displayName"`
	Resolved json.RawMessage `json:"resolvedModel"`
	Supports json.RawMessage `json:"supportsEffort"`
	Levels   json.RawMessage `json:"supportedEffortLevels"`
}

func ParseModels(data json.RawMessage) []sdk.ListedModel {
	var offered []claudeModel
	if json.Unmarshal(data, &offered) != nil {
		return nil
	}
	reports := false
	isArray := func(raw json.RawMessage) bool { return len(raw) > 0 && raw[0] == '[' }
	for _, entry := range offered {
		reports = reports || isArray(entry.Levels)
	}
	models := []sdk.ListedModel{}
	for index, entry := range offered {
		id := agentruntime.TrimmedJSON(entry.Value)
		if id == "" {
			continue
		}
		title := agentruntime.TrimmedJSON(entry.Title)
		resolved := agentruntime.TrimmedJSON(entry.Resolved)
		if id == "default" {
			title = ""
			for otherIndex, other := range offered {
				if otherIndex != index && agentruntime.TrimmedJSON(other.Resolved) != "" && agentruntime.TrimmedJSON(other.Resolved) == resolved {
					title = agentruntime.TrimmedJSON(other.Title)
					break
				}
			}
		}
		model := sdk.ListedModel{ID: id}
		if title != id {
			model.Title = title
		}
		if resolved != id {
			model.Resolves = resolved
		}
		if string(entry.Supports) == "false" || reports && !isArray(entry.Levels) {
			model.Efforts = new([]string{})
		} else if isArray(entry.Levels) {
			var levels []string
			_ = json.Unmarshal(entry.Levels, &levels)
			valid := []string{}
			for _, effort := range sdk.Efforts {
				for _, level := range levels {
					if effort == level {
						valid = append(valid, effort)
						break
					}
				}
			}
			model.Efforts = &valid
		}
		models = append(models, model)
	}
	return models
}
func ListModels(ctx context.Context, settings Settings, directory string) ([]sdk.ListedModel, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	proc := process.NewJSONLines(process.JSONOptions{Argv: Argv(settings, "default", ""), Directory: directory, Env: process.AgentEnv(nil), ReplaceEnv: true})
	type answer struct {
		models []sdk.ListedModel
		err    error
	}
	result := make(chan answer, 1)
	if err := proc.Start(ctx, func(data []byte) error {
		var line struct {
			Type     string `json:"type"`
			Response struct {
				Subtype  string `json:"subtype"`
				ID       string `json:"request_id"`
				Error    string `json:"error"`
				Response struct {
					Models json.RawMessage `json:"models"`
				} `json:"response"`
			} `json:"response"`
		}
		if json.Unmarshal(data, &line) != nil || line.Type != "control_response" || line.Response.ID != "ariadne-models" {
			return nil
		}
		value := answer{models: ParseModels(line.Response.Response.Models)}
		if line.Response.Subtype == "error" {
			detail := line.Response.Error
			if detail == "" {
				detail = "it would not say"
			}
			value.err = errors.New(detail)
		}
		select {
		case result <- value:
		default:
		}
		return nil
	}, func(err error) {
		select {
		case result <- answer{err: err}:
		default:
		}
	}); err != nil {
		return nil, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = proc.Close(cleanup)
	}()
	message, _ := jsonutil.Encode(map[string]any{"type": "control_request", "request_id": "ariadne-models", "request": map[string]any{"subtype": "initialize"}})
	if err := proc.SendJSON(ctx, message); err != nil {
		return nil, err
	}
	select {
	case value := <-result:
		return value.models, value.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func Kind(bridge *agentruntime.ToolBridge) agentruntime.Kind {
	return agentruntime.Kind{Name: ProviderKind, Create: func(ctx agentruntime.DriverContext) (agentruntime.Driver, error) {
		settings := Settings{}
		if command, ok := ctx.Settings["command"].(string); ok {
			settings.Command = command
		}
		return &claudeDriver{ctx: ctx, settings: settings, bridge: bridge, seen: map[string]claudeTokens{}, streamed: map[string]bool{}}, nil
	}}
}

type claudeAPIUsage struct {
	Input         int64 `json:"input_tokens"`
	Output        int64 `json:"output_tokens"`
	Cached        int64 `json:"cache_read_input_tokens"`
	CacheWrite    int64 `json:"cache_creation_input_tokens"`
	OutputDetails struct {
		Thinking int64 `json:"thinking_tokens"`
	} `json:"output_tokens_details"`
}
type claudeTokens [5]int64

func claudeUsage(usage claudeAPIUsage) claudeTokens {
	return claudeTokens{usage.Input + usage.Cached + usage.CacheWrite, usage.Output, usage.Cached, usage.CacheWrite, usage.OutputDetails.Thinking}
}

type claudeDriver struct {
	ctx                agentruntime.DriverContext
	settings           Settings
	bridge             *agentruntime.ToolBridge
	proc               *process.JSONLines
	removeTools        func()
	mu                 sync.Mutex
	sawOutput, closing bool
	seen               map[string]claudeTokens
	turn               claudeTokens
	streamed           map[string]bool
	writing            string
	cost               float64
}

func (d *claudeDriver) count(now, before claudeTokens, contextInput *int64) {
	var grew claudeTokens
	any := false
	for i := range grew {
		grew[i] = max(int64(0), now[i]-before[i])
		d.turn[i] += grew[i]
		any = any || grew[i] != 0
	}
	if any {
		d.ctx.Emit(agentruntime.Event{Type: "usage", Usage: agentruntime.Usage{Input: grew[0], Output: grew[1], ContextInput: contextInput, Cached: new(grew[2]), CacheWrite: new(grew[3]), Reasoning: new(grew[4])}})
	}
}
func (d *claudeDriver) recordMessageUsage(id string, raw claudeAPIUsage) error {
	if id == "" {
		return nil
	}
	if len(d.seen) >= 1024 {
		if _, ok := d.seen[id]; !ok {
			return errors.New("Claude usage messages exceeded their limit")
		}
	}
	now := claudeUsage(raw)
	var contextInput *int64
	if now[0] > 0 {
		value := now[0]
		contextInput = &value
	}
	d.count(now, d.seen[id], contextInput)
	d.seen[id] = now
	return nil
}
func (d *claudeDriver) onLine(data []byte) error {
	var line struct {
		Type  string `json:"type"`
		Event *struct {
			Type    string `json:"type"`
			Message struct {
				ID    string          `json:"id"`
				Usage *claudeAPIUsage `json:"usage"`
			} `json:"message"`
			Block struct {
				Type string `json:"type"`
			} `json:"content_block"`
			Delta struct{ Type, Text string } `json:"delta"`
		} `json:"event"`
		Subtype string          `json:"subtype"`
		IsError bool            `json:"is_error"`
		Result  string          `json:"result"`
		Errors  []string        `json:"errors"`
		Cost    *float64        `json:"total_cost_usd"`
		Usage   *claudeAPIUsage `json:"usage"`
		Message *struct {
			ID      string          `json:"id"`
			Content json.RawMessage `json:"content"`
			Usage   *claudeAPIUsage `json:"usage"`
		} `json:"message"`
	}
	if err := json.Unmarshal(data, &line); err != nil {
		return fmt.Errorf("invalid Claude stream: %w", err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closing {
		return nil
	}
	d.sawOutput = true
	emit := d.ctx.Emit
	if line.Type == "stream_event" && line.Event != nil {
		event := line.Event
		switch {
		case event.Type == "message_start":
			d.writing = event.Message.ID
			if event.Message.Usage != nil {
				if err := d.recordMessageUsage(event.Message.ID, *event.Message.Usage); err != nil {
					return err
				}
			}
		case event.Type == "content_block_start" && event.Block.Type == "tool_use":
			emit(agentruntime.Event{Type: "alive"})
		case event.Type == "content_block_delta" && event.Delta.Type == "text_delta":
			if event.Delta.Text != "" {
				if d.writing != "" {
					if len(d.streamed) >= 1024 && !d.streamed[d.writing] {
						return errors.New("Claude streamed messages exceeded their limit")
					}
					d.streamed[d.writing] = true
				}
				emit(agentruntime.Event{Type: "text", Text: event.Delta.Text})
			}
		default:
			emit(agentruntime.Event{Type: "alive"})
		}
		return nil
	}
	if line.Type == "assistant" && line.Message != nil {
		var content []struct{ Type, Text, Thinking string }
		_ = json.Unmarshal(line.Message.Content, &content)
		whole := line.Message.ID == "" || !d.streamed[line.Message.ID]
		for _, block := range content {
			if block.Type == "text" && block.Text != "" {
				if whole {
					emit(agentruntime.Event{Type: "text", Text: block.Text})
				}
			} else if block.Type == "thinking" && block.Thinking != "" {
				emit(agentruntime.Event{Type: "reasoning", Text: block.Thinking})
			}
		}
		if line.Message.Usage != nil {
			if err := d.recordMessageUsage(line.Message.ID, *line.Message.Usage); err != nil {
				return err
			}
		}
		emit(agentruntime.Event{Type: "alive"})
		return nil
	}
	if line.Type == "result" {
		if line.Usage != nil {
			d.count(claudeUsage(*line.Usage), d.turn, nil)
		}
		d.turn = claudeTokens{}
		clear(d.seen)
		clear(d.streamed)
		d.writing = ""
		if line.Cost != nil {
			cost := *line.Cost
			if cost >= d.cost {
				cost -= d.cost
			}
			d.cost = *line.Cost
			if cost > 0 {
				emit(agentruntime.Event{Type: "usage", Usage: agentruntime.Usage{CostUSD: &cost}})
			}
		}
		if line.IsError || line.Subtype != "" && line.Subtype != "success" {
			detail := line.Result
			if detail == "" {
				detail = strings.Join(line.Errors, "; ")
			}
			if detail == "" {
				detail = line.Subtype
			}
			if detail == "" {
				detail = "error"
			}
			quota := rpc.QuotaText(detail)
			busy := quota || rpc.BusyText(detail)
			retryable := !quota && rpc.BusyText(detail)
			emit(agentruntime.Event{Type: "failed", Detail: detail, Retryable: retryable, Busy: busy})
		} else {
			emit(agentruntime.Event{Type: "done"})
		}
		return nil
	}
	emit(agentruntime.Event{Type: "alive"})
	return nil
}
func (d *claudeDriver) Start(ctx context.Context) error {
	systemFile, mcpFile := filepath.Join(d.ctx.Directory, "system.md"), filepath.Join(d.ctx.Directory, "mcp.json")
	if err := os.WriteFile(systemFile, []byte(d.ctx.System), 0600); err != nil {
		return err
	}
	argv := append(Argv(d.settings, d.ctx.Model, d.ctx.Effort), "--system-prompt-file", systemFile)
	if len(d.ctx.Tools.Specs) > 0 {
		if d.bridge == nil {
			return errors.New("Claude tools require an agent tool bridge")
		}
		registration, err := d.bridge.Add(d.ctx.Tools)
		if err != nil {
			return err
		}
		d.removeTools = registration.Remove
		config, _ := jsonutil.Encode(map[string]any{"mcpServers": map[string]any{agentruntime.BridgeServerName: map[string]any{"type": "http", "url": registration.URL}}})
		if err := os.WriteFile(mcpFile, config, 0600); err != nil {
			return err
		}
		argv = append(argv, "--mcp-config", mcpFile)
	}
	d.proc = process.NewJSONLines(process.JSONOptions{Argv: argv, Directory: d.ctx.Directory, Env: process.AgentEnv(map[string]string{"MCP_TOOL_TIMEOUT": "86400000", "MAX_MCP_OUTPUT_TOKENS": "200000"}), ReplaceEnv: true})
	return d.proc.Start(ctx, d.onLine, func(err error) {
		d.mu.Lock()
		saw, closing := d.sawOutput, d.closing
		d.mu.Unlock()
		if !closing {
			d.ctx.Emit(agentruntime.Event{Type: "failed", Detail: err.Error(), Retryable: saw})
		}
	})
}
func (d *claudeDriver) Prompt(ctx context.Context, input agentruntime.Input, effort string) error {
	content := []any{map[string]any{"type": "text", "text": input.Text}}
	for _, image := range input.Images {
		content = append(content, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": image.MIME, "data": image.Data}})
	}
	data, err := jsonutil.Encode(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": content}, "parent_tool_use_id": nil, "session_id": "default"})
	if err != nil {
		return err
	}
	return d.proc.SendJSON(ctx, data)
}
func (d *claudeDriver) Close(ctx context.Context, discard bool) error {
	d.mu.Lock()
	d.closing = true
	d.mu.Unlock()
	if d.removeTools != nil {
		d.removeTools()
	}
	if d.proc != nil {
		return d.proc.Close(ctx)
	}
	return nil
}
