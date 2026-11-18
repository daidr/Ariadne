package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/daidr/Ariadne/internal/process"
	"github.com/daidr/Ariadne/internal/rpc"
	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/sdk"
)

type ACPSettings struct {
	Command string `json:"command,omitempty"`
}
type StdioLauncher func(string) (string, []string, error)

var acpClient = map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{"fs": map[string]any{"readTextFile": false, "writeTextFile": false}, "terminal": false}, "clientInfo": codexClient}

func ForConversationTools(raw json.RawMessage, tools []sdk.Tool) bool {
	var call struct {
		Title any            `json:"title"`
		Input map[string]any `json:"rawInput"`
	}
	if json.Unmarshal(raw, &call) != nil || len(tools) == 0 {
		return false
	}
	known := map[string]bool{}
	for _, tool := range tools {
		name := textutil.Lower(tool.Name)
		for _, spelling := range []string{"mcp__" + BridgeServerName + "__" + name, BridgeServerName + "__" + name, BridgeServerName + "/" + name, BridgeServerName + "." + name, name + " (" + BridgeServerName + " mcp server)"} {
			known[spelling] = true
		}
	}
	candidates := []any{call.Title}
	for _, field := range []string{"tool", "toolName", "tool_name", "name"} {
		candidates = append(candidates, call.Input[field])
	}
	for _, candidate := range candidates {
		if text, ok := candidate.(string); ok && known[textutil.Lower(textutil.Trim(text))] {
			return true
		}
	}
	return false
}

type acpSessionModels struct {
	Current   json.RawMessage `json:"currentModelId"`
	Available []struct {
		ID   json.RawMessage `json:"modelId"`
		Name json.RawMessage `json:"name"`
	} `json:"availableModels"`
}

func ACPModels(data json.RawMessage) []sdk.ListedModel {
	var offered acpSessionModels
	_ = json.Unmarshal(data, &offered)
	available := []sdk.ListedModel{}
	first := sdk.ListedModel{ID: "default"}
	current := trimmedJSON(offered.Current)
	for _, entry := range offered.Available {
		id := trimmedJSON(entry.ID)
		if id == "" {
			continue
		}
		model := sdk.ListedModel{ID: id}
		title := trimmedJSON(entry.Name)
		if title != id {
			model.Title = title
		}
		if id == current && id != "default" && first.Resolves == "" {
			first.Title = title
			if first.Title == "" {
				first.Title = id
			}
			first.Resolves = id
		}
		if id != "default" {
			available = append(available, model)
		}
	}
	return append([]sdk.ListedModel{first}, available...)
}
func ListACPModels(ctx context.Context, settings ACPSettings, directory string) ([]sdk.ListedModel, error) {
	argv := process.SplitCommand(settings.Command)
	if len(argv) == 0 {
		return []sdk.ListedModel{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	stdio, err := rpc.StartStdio(ctx, process.JSONOptions{Argv: argv, Directory: directory, Env: process.AgentEnv(nil), ReplaceEnv: true}, rpc.Options{Strict: true})
	if err != nil {
		return nil, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = stdio.Close(cleanup)
	}()
	if err := stdio.Peer.Request(ctx, "initialize", acpClient, nil); err != nil {
		return nil, err
	}
	var result struct {
		Models json.RawMessage `json:"models"`
	}
	if err := stdio.Peer.Request(ctx, "session/new", map[string]any{"cwd": directory, "mcpServers": []any{}}, &result); err != nil {
		return nil, err
	}
	return ACPModels(result.Models), nil
}
func ACPKind(bridge *ToolBridge, shim StdioLauncher) Kind {
	if shim == nil {
		shim = func(url string) (string, []string, error) {
			program, err := os.Executable()
			return program, []string{StdioBridgeCommand, url}, err
		}
	}
	return Kind{Name: "acp", Create: func(ctx DriverContext) (Driver, error) {
		settings := ACPSettings{}
		if command, ok := ctx.Settings["command"].(string); ok {
			settings.Command = command
		}
		return &acpDriver{ctx: ctx, settings: settings, bridge: bridge, shim: shim, first: true}, nil
	}}
}

type acpDriver struct {
	ctx                             DriverContext
	settings                        ACPSettings
	bridge                          *ToolBridge
	shim                            StdioLauncher
	stdio                           *rpc.Stdio
	life                            context.Context
	mu                              sync.Mutex
	sessionID                       string
	started, first, images, closing bool
	thought                         strings.Builder
	removeTools                     func()
	requests                        sync.WaitGroup
}

func (d *acpDriver) emitLocked(event Event) {
	if !d.closing {
		d.ctx.Emit(event)
	}
}
func (d *acpDriver) flushThoughtLocked() {
	if text := textutil.Trim(d.thought.String()); text != "" {
		d.emitLocked(Event{Type: "reasoning", Text: text})
	}
	d.thought.Reset()
}
func (d *acpDriver) request(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	if method != "session/request_permission" {
		return nil, fmt.Errorf("%w: %s is not supported", rpc.ErrUnhandled, method)
	}
	var params struct {
		Call    json.RawMessage `json:"toolCall"`
		Options []struct {
			ID   string `json:"optionId"`
			Kind string `json:"kind"`
		} `json:"options"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, err
	}
	prefix := "reject"
	if ForConversationTools(params.Call, d.ctx.Tools.Specs) {
		prefix = "allow"
	}
	pick := -1
	for i, option := range params.Options {
		if option.Kind == prefix+"_once" {
			pick = i
			break
		}
		if pick < 0 && strings.HasPrefix(option.Kind, prefix) {
			pick = i
		}
	}
	if pick < 0 {
		return map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}, nil
	}
	return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": params.Options[pick].ID}}, nil
}
func (d *acpDriver) notification(method string, raw json.RawMessage) {
	if method != "session/update" {
		return
	}
	var params struct {
		Update struct {
			Type    string                      `json:"sessionUpdate"`
			Content struct{ Type, Text string } `json:"content"`
		} `json:"update"`
	}
	if json.Unmarshal(raw, &params) != nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closing {
		return
	}
	update := params.Update
	if update.Type == "agent_thought_chunk" {
		if update.Content.Type == "text" && update.Content.Text != "" {
			if len(update.Content.Text) > 64<<20-d.thought.Len() {
				d.emitLocked(Event{Type: "failed", Detail: "ACP reasoning exceeded its size limit"})
				return
			}
			d.thought.WriteString(update.Content.Text)
		}
		d.emitLocked(Event{Type: "alive"})
		return
	}
	d.flushThoughtLocked()
	if update.Type == "agent_message_chunk" && update.Content.Type == "text" {
		if update.Content.Text != "" {
			d.emitLocked(Event{Type: "text", Text: update.Content.Text})
		}
		return
	}
	d.emitLocked(Event{Type: "alive"})
}
func (d *acpDriver) Start(life context.Context) error {
	argv := process.SplitCommand(d.settings.Command)
	if len(argv) == 0 {
		return errors.New("no command is set for the agent")
	}
	d.life = life
	lines := process.NewJSONLines(process.JSONOptions{Argv: argv, Directory: d.ctx.Directory, Env: process.AgentEnv(nil), ReplaceEnv: true})
	peer, err := rpc.New(rpc.Options{Context: life, Strict: true, Send: lines.SendJSON, Request: d.request, Notification: d.notification})
	if err != nil {
		return err
	}
	d.stdio = &rpc.Stdio{Peer: peer, Process: lines}
	if err := lines.Start(life, peer.Take, func(err error) {
		peer.End(err)
		d.mu.Lock()
		defer d.mu.Unlock()
		d.emitLocked(Event{Type: "failed", Detail: err.Error(), Retryable: d.started})
	}); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(life, time.Minute)
	defer cancel()
	var init struct {
		Capabilities struct {
			Prompt struct {
				Image bool `json:"image"`
			} `json:"promptCapabilities"`
			MCP struct {
				HTTP bool `json:"http"`
			} `json:"mcpCapabilities"`
		} `json:"agentCapabilities"`
	}
	if err := peer.Request(ctx, "initialize", acpClient, &init); err != nil {
		return d.startError(err)
	}
	d.images = init.Capabilities.Prompt.Image
	servers := []any{}
	if len(d.ctx.Tools.Specs) > 0 {
		if d.bridge == nil {
			return errors.New("ACP tools require an agent tool bridge")
		}
		registration, err := d.bridge.Add(d.ctx.Tools)
		if err != nil {
			return err
		}
		d.removeTools = registration.Remove
		if init.Capabilities.MCP.HTTP {
			servers = append(servers, map[string]any{"type": "http", "name": BridgeServerName, "url": registration.URL, "headers": []any{}})
		} else {
			command, args, err := d.shim(registration.URL)
			if err != nil {
				return err
			}
			servers = append(servers, map[string]any{"name": BridgeServerName, "command": command, "args": args, "env": []any{}})
		}
	}
	var session struct {
		ID     string           `json:"sessionId"`
		Models acpSessionModels `json:"models"`
	}
	if err := peer.Request(ctx, "session/new", map[string]any{"cwd": d.ctx.Directory, "mcpServers": servers}, &session); err != nil {
		return d.startError(err)
	}
	d.sessionID = session.ID
	if d.sessionID == "" {
		return errors.New("the agent opened no session")
	}
	model := textutil.Trim(d.ctx.Model)
	if model != "" && model != "default" {
		for _, offered := range session.Models.Available {
			if trimmedJSON(offered.ID) == model {
				_ = peer.Request(ctx, "session/set_model", map[string]any{"sessionId": d.sessionID, "modelId": model}, nil)
				break
			}
		}
	}
	d.mu.Lock()
	d.started = true
	d.mu.Unlock()
	return nil
}
func (d *acpDriver) startError(err error) error {
	var remote *rpc.Error
	if errors.As(err, &remote) && remote.Code == -32000 {
		err = fmt.Errorf("the agent asks to sign in first (run it once in a terminal): %w", err)
	}
	tail := strings.Split(d.stdio.Process.StderrTail(), "\n")
	if len(tail) > 3 {
		tail = tail[len(tail)-3:]
	}
	if text := strings.Join(tail, "\n"); text != "" {
		return fmt.Errorf("%w: %s", err, text)
	}
	return err
}
func (d *acpDriver) Prompt(ctx context.Context, input Input, effort string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.mu.Lock()
	if d.closing {
		d.mu.Unlock()
		return errors.New("ACP session is closed")
	}
	text := input.Text
	if d.first {
		text = "<instructions>\n" + d.ctx.System + "\n</instructions>\n\n" + text
	}
	d.first = false
	prompt := []any{map[string]any{"type": "text", "text": text}}
	if d.images {
		for _, image := range input.Images {
			prompt = append(prompt, map[string]any{"type": "image", "mimeType": image.MIME, "data": image.Data})
		}
	}
	d.requests.Add(1)
	d.mu.Unlock()
	// session/prompt returns only after the whole turn. Its lifetime spans the
	// Ariadne steps that separately execute its pending tool calls.
	go func() {
		defer d.requests.Done()
		var result struct {
			Stop  string `json:"stopReason"`
			Usage *struct {
				Input   int64 `json:"inputTokens"`
				Output  int64 `json:"outputTokens"`
				Cached  int64 `json:"cachedReadTokens"`
				Written int64 `json:"cachedWriteTokens"`
				Thought int64 `json:"thoughtTokens"`
			} `json:"usage"`
		}
		err := d.stdio.Peer.Request(d.life, "session/prompt", map[string]any{"sessionId": d.sessionID, "prompt": prompt}, &result)
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.closing {
			return
		}
		if err != nil {
			busy := rpc.BusyText(err.Error())
			d.emitLocked(Event{Type: "failed", Detail: err.Error(), Retryable: busy, Busy: busy})
			return
		}
		d.flushThoughtLocked()
		if u := result.Usage; u != nil {
			d.emitLocked(Event{Type: "usage", Usage: Usage{Input: u.Input, Output: u.Output, Cached: &u.Cached, CacheWrite: &u.Written, Reasoning: &u.Thought}})
		}
		switch result.Stop {
		case "refusal":
			d.emitLocked(Event{Type: "failed", Detail: "the agent refused to go on"})
		case "cancelled":
			d.emitLocked(Event{Type: "failed", Detail: "the agent cancelled the turn"})
		default:
			d.emitLocked(Event{Type: "done"})
		}
	}()
	return nil
}
func (d *acpDriver) Close(ctx context.Context, discard bool) error {
	d.mu.Lock()
	d.closing = true
	d.mu.Unlock()
	if d.removeTools != nil {
		d.removeTools()
	}
	var err error
	if d.stdio != nil {
		if d.sessionID != "" {
			notify, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
			_ = d.stdio.Peer.Notify(notify, "session/cancel", map[string]any{"sessionId": d.sessionID})
			cancel()
		}
		err = d.stdio.Close(ctx)
	}
	d.requests.Wait()
	d.mu.Lock()
	d.thought.Reset()
	d.mu.Unlock()
	return err
}
