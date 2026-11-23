package acp

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
	"github.com/daidr/Ariadne/internal/process"
	"github.com/daidr/Ariadne/internal/rpc"
	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/sdk"
)

type Settings struct {
	Command string `json:"command,omitempty"`
}

// TurnUsage reports token counters returned by an ACP session/prompt response.
type TurnUsage struct {
	Input   int64  `json:"inputTokens"`
	Output  int64  `json:"outputTokens"`
	Cached  int64  `json:"cachedReadTokens"`
	Written int64  `json:"cachedWriteTokens"`
	Thought int64  `json:"thoughtTokens"`
	Total   *int64 `json:"totalTokens"`
}

// TurnFilter intercepts streaming text chunks and prompt completion for
// provider-specific ACP dialects.
type TurnFilter interface {
	Reset()
	OnMessageChunk(text string) []string
	OnNonMessageUpdate() []string
	OnTurnComplete(rpcErr error) (emit string, failureDetail string)
}

// Hooks allows specialized ACP-based plugins to customize launch behavior,
// stream filtering, and usage accounting without coupling into generic ACP.
type Hooks struct {
	DefaultCommand      string
	ResolveProfile      func(argv []string) string
	PrepareProfile      func(argv []string, directory string, profile string) ([]string, error)
	NewTurnFilter       func() TurnFilter
	AdjustInputTokens   func(TurnUsage) int64
	RetryableStartError func(operation string, err error) bool
}

// ListOptions controls optional behavior during ACP model catalog discovery.
type ListOptions struct {
	ProbeThoughtLevels bool
}

var acpClient = map[string]any{
	"protocolVersion":    1,
	"clientCapabilities": map[string]any{"fs": map[string]any{"readTextFile": false, "writeTextFile": false}, "terminal": false},
	"clientInfo":         agentruntime.ClientInfo,
}

type acpInitialize struct {
	Version   int `json:"protocolVersion"`
	AgentInfo struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"agentInfo"`
	Capabilities struct {
		Load   bool `json:"loadSession"`
		Prompt struct {
			Image bool `json:"image"`
		} `json:"promptCapabilities"`
		MCP struct {
			HTTP bool `json:"http"`
		} `json:"mcpCapabilities"`
		Session struct {
			Resume *struct{} `json:"resume"`
		} `json:"sessionCapabilities"`
	} `json:"agentCapabilities"`
}

func initializeACP(ctx context.Context, peer *rpc.Peer) (acpInitialize, error) {
	var hello acpInitialize
	if err := peer.Request(ctx, "initialize", acpClient, &hello); err != nil {
		return hello, err
	}
	if hello.Version != 1 {
		return hello, fmt.Errorf("unsupported ACP protocol version %d (expected 1)", hello.Version)
	}
	return hello, nil
}

func ForConversationTools(raw json.RawMessage, tools []sdk.Tool) bool {
	var call struct {
		Name  string         `json:"name"`
		Title any            `json:"title"`
		Input map[string]any `json:"rawInput"`
	}
	if json.Unmarshal(raw, &call) != nil || len(tools) == 0 {
		return false
	}
	known := map[string]bool{}
	for _, tool := range tools {
		name := textutil.Lower(tool.Name)
		for _, spelling := range []string{
			"mcp__" + agentruntime.BridgeServerName + "__" + name,
			"mcp_" + agentruntime.BridgeServerName + "_" + name,
			agentruntime.BridgeServerName + "__" + name,
			agentruntime.BridgeServerName + "/" + name,
			agentruntime.BridgeServerName + "." + name,
			name + " (" + agentruntime.BridgeServerName + " mcp server)",
		} {
			known[spelling] = true
		}
	}
	candidates := []any{call.Name, call.Title}
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

func ParseModels(data json.RawMessage) []sdk.ListedModel {
	var offered acpSessionModels
	_ = json.Unmarshal(data, &offered)
	available := []sdk.ListedModel{}
	first := sdk.ListedModel{ID: "default"}
	current := agentruntime.TrimmedJSON(offered.Current)
	for _, entry := range offered.Available {
		id := agentruntime.TrimmedJSON(entry.ID)
		if id == "" {
			continue
		}
		model := sdk.ListedModel{ID: id}
		title := agentruntime.TrimmedJSON(entry.Name)
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

func ListModels(ctx context.Context, settings Settings, directory string) ([]sdk.ListedModel, error) {
	return ListModelsWithOptions(ctx, settings, directory, ListOptions{})
}

func ListModelsWithOptions(ctx context.Context, settings Settings, directory string, opts ListOptions) ([]sdk.ListedModel, error) {
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
	if _, err := initializeACP(ctx, stdio.Peer); err != nil {
		return nil, err
	}
	var result acpSession
	if err := stdio.Peer.Request(ctx, "session/new", map[string]any{"cwd": directory, "mcpServers": []any{}}, &result); err != nil {
		return nil, err
	}
	if result.ID == "" {
		return nil, errors.New("the ACP agent opened no session")
	}
	return result.listedModels(ctx, stdio.Peer, opts.ProbeThoughtLevels), nil
}

func Kind(bridge *agentruntime.ToolBridge, shim agentruntime.StdioLauncher) agentruntime.Kind {
	return NewKind(ProviderKind, bridge, shim, Hooks{})
}

func NewKind(name string, bridge *agentruntime.ToolBridge, shim agentruntime.StdioLauncher, hooks Hooks) agentruntime.Kind {
	if shim == nil {
		shim = func(url string) (string, []string, error) {
			program, err := os.Executable()
			return program, []string{agentruntime.StdioBridgeCommand, url}, err
		}
	}
	return agentruntime.Kind{
		Name:            name,
		Persistent:      true,
		ResumeDirectory: true,
		MutableEffort:   true,
		Fingerprint:     agentruntime.NativeFingerprint,
		Create: func(ctx agentruntime.DriverContext) (agentruntime.Driver, error) {
			command, _ := ctx.Settings["command"].(string)
			if textutil.Trim(command) == "" {
				command = hooks.DefaultCommand
			}
			var filter TurnFilter
			if hooks.NewTurnFilter != nil {
				filter = hooks.NewTurnFilter()
			}
			return &acpDriver{
				ctx:      ctx,
				settings: Settings{Command: command},
				bridge:   bridge,
				shim:     shim,
				hooks:    hooks,
				filter:   filter,
				first:    true,
			}, nil
		},
	}
}

type acpDriver struct {
	ctx                             agentruntime.DriverContext
	settings                        Settings
	bridge                          *agentruntime.ToolBridge
	shim                            agentruntime.StdioLauncher
	hooks                           Hooks
	filter                          TurnFilter
	stdio                           *rpc.Stdio
	life                            context.Context
	mu                              sync.Mutex
	sessionID                       string
	executable, sessionMethod       string
	hello                           acpInitialize
	started, first, images, closing bool
	active, restorable              bool
	agentProfile                    string
	thought                         strings.Builder
	contextUsed                     *int64
	thoughtOption                   acpConfigOption
	defaultLevel                    string
	removeTools                     func()
	requests                        sync.WaitGroup
	promptDone                      chan struct{}
	settledUsage                    *agentruntime.Usage
}

type acpCheckpoint struct {
	SessionID    string `json:"sessionId"`
	AgentProfile string `json:"agentProfile,omitempty"`
	DefaultLevel string `json:"defaultLevel,omitempty"`
	ContextUsed  *int64 `json:"contextUsed,omitempty"`
}

func (d *acpDriver) diagnosticLocked(operation string, err error) *sdk.AgentError {
	detail := &sdk.AgentError{
		Operation: operation, Executable: d.executable, SessionMethod: d.sessionMethod,
		Name: textutil.HeadUnits(d.hello.AgentInfo.Name, 256), Version: textutil.HeadUnits(d.hello.AgentInfo.Version, 256),
		ProtocolVersion: d.hello.Version,
	}
	if d.stdio != nil {
		detail.Stderr = d.stdio.Process.StderrTail()
	}
	var remote *rpc.Error
	if errors.As(err, &remote) {
		detail.RPCCode = new(remote.Code)
		detail.RPCData = strings.Clone(textutil.HeadUnits(string(remote.Data), 8192))
	}
	return detail
}

func (d *acpDriver) diagnosticError(operation string, err error) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return &agentruntime.NativeAgentError{Err: err, Detail: d.diagnosticLocked(operation, err)}
}

func (d *acpDriver) emitLocked(event agentruntime.Event) {
	if !d.closing {
		d.ctx.Emit(event)
	}
}

func (d *acpDriver) flushThoughtLocked() {
	if text := textutil.Trim(d.thought.String()); text != "" {
		d.emitLocked(agentruntime.Event{Type: "reasoning", Text: text})
	}
	d.thought.Reset()
}

func (d *acpDriver) emitChunksLocked(chunks []string) {
	for _, chunk := range chunks {
		if chunk != "" {
			d.emitLocked(agentruntime.Event{Type: "text", Text: chunk})
		}
	}
}

func (d *acpDriver) request(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	if method != "session/request_permission" {
		return nil, fmt.Errorf("%w: %s is not supported", rpc.ErrUnhandled, method)
	}
	var params struct {
		SessionID string          `json:"sessionId"`
		Call      json.RawMessage `json:"toolCall"`
		Options   []struct {
			ID   string `json:"optionId"`
			Kind string `json:"kind"`
		} `json:"options"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, err
	}
	cancelled := map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}
	d.mu.Lock()
	allowedSession := d.active && !d.closing && params.SessionID == d.sessionID
	d.mu.Unlock()
	if !allowedSession {
		return cancelled, nil
	}
	choice := "reject_once"
	if ForConversationTools(params.Call, d.ctx.Tools.Specs) {
		choice = "allow_once"
	}
	for _, option := range params.Options {
		if option.Kind == choice && option.ID != "" {
			return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": option.ID}}, nil
		}
	}
	return cancelled, nil
}

func (d *acpDriver) notification(method string, raw json.RawMessage) {
	if method != "session/update" {
		return
	}
	var params struct {
		SessionID string `json:"sessionId"`
		Update    struct {
			Type    string                      `json:"sessionUpdate"`
			Content struct{ Type, Text string } `json:"content"`
			Options []acpConfigOption           `json:"configOptions"`
			Used    *int64                      `json:"used"`
		} `json:"update"`
	}
	if json.Unmarshal(raw, &params) != nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closing || !d.started || params.SessionID != d.sessionID {
		return
	}
	update := params.Update
	if update.Type == "config_option_update" {
		d.thoughtOption = (acpSession{Options: update.Options}).option("thought_level")
	}
	if update.Type == "usage_update" && update.Used != nil && *update.Used >= 0 {
		used := *update.Used
		d.contextUsed = &used
	}
	if !d.active {
		return
	}
	if update.Type == "usage_update" {
		if update.Used != nil && *update.Used > 0 {
			used := *update.Used
			d.emitLocked(agentruntime.Event{Type: "usage", Usage: agentruntime.Usage{ContextInput: &used}})
		}
		d.emitLocked(agentruntime.Event{Type: "alive"})
		return
	}
	if update.Type == "agent_thought_chunk" {
		if d.filter != nil {
			d.emitChunksLocked(d.filter.OnNonMessageUpdate())
		}
		if update.Content.Type == "text" && update.Content.Text != "" {
			if len(update.Content.Text) > 64<<20-d.thought.Len() {
				d.emitLocked(agentruntime.Event{Type: "failed", Detail: "ACP reasoning exceeded its size limit"})
				return
			}
			d.thought.WriteString(update.Content.Text)
		}
		d.emitLocked(agentruntime.Event{Type: "alive"})
		return
	}
	d.flushThoughtLocked()
	if update.Type == "agent_message_chunk" && update.Content.Type == "text" {
		text := update.Content.Text
		if d.filter != nil && text != "" {
			d.emitChunksLocked(d.filter.OnMessageChunk(text))
			return
		}
		if text != "" {
			d.emitLocked(agentruntime.Event{Type: "text", Text: text})
		}
		return
	}
	if d.filter != nil && (update.Type == "tool_call" || update.Type == "tool_call_update") {
		d.emitChunksLocked(d.filter.OnNonMessageUpdate())
	}
	d.emitLocked(agentruntime.Event{Type: "alive"})
}

func (d *acpDriver) Start(life context.Context) error {
	argv := process.SplitCommand(d.settings.Command)
	if len(argv) == 0 {
		return d.diagnosticError("launch", errors.New("no command is set for the agent"))
	}
	d.executable = filepath.Base(argv[0])
	if d.hooks.ResolveProfile != nil {
		d.agentProfile = d.hooks.ResolveProfile(argv)
	}
	var saved acpCheckpoint
	resuming := len(d.ctx.Resume) != 0
	if resuming {
		if json.Unmarshal(d.ctx.Resume, &saved) != nil || saved.SessionID == "" {
			return d.diagnosticError("restore checkpoint", errors.New("ACP session checkpoint cannot be restored"))
		}
		if saved.AgentProfile != d.agentProfile {
			return d.diagnosticError("restore checkpoint", errors.New("ACP agent profile changed"))
		}
	}
	if d.agentProfile != "" && d.hooks.PrepareProfile != nil {
		var err error
		argv, err = d.hooks.PrepareProfile(argv, d.ctx.Directory, d.agentProfile)
		if err != nil {
			return d.diagnosticError("prepare agent profile", err)
		}
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
		d.emitLocked(agentruntime.Event{Type: "failed", Detail: err.Error(), Retryable: d.started, AgentError: d.diagnosticLocked("transport", err)})
	}); err != nil {
		return d.diagnosticError("launch", err)
	}
	ctx, cancel := context.WithTimeout(life, time.Minute)
	defer cancel()
	init, err := initializeACP(ctx, peer)
	d.mu.Lock()
	d.hello = init
	d.mu.Unlock()
	if err != nil {
		return d.startError("initialize", err)
	}
	d.images = init.Capabilities.Prompt.Image
	d.restorable = init.Capabilities.Session.Resume != nil || init.Capabilities.Load
	servers := []any{}
	if len(d.ctx.Tools.Specs) > 0 {
		if d.bridge == nil {
			return d.diagnosticError("tool bridge", errors.New("ACP tools require an agent tool bridge"))
		}
		registration, err := d.bridge.Add(d.ctx.Tools)
		if err != nil {
			return d.diagnosticError("tool bridge", err)
		}
		d.removeTools = registration.Remove
		if init.Capabilities.MCP.HTTP {
			servers = append(servers, map[string]any{"type": "http", "name": agentruntime.BridgeServerName, "url": registration.URL, "headers": []any{}})
		} else {
			command, args, err := d.shim(registration.URL)
			if err != nil {
				return d.diagnosticError("tool bridge", err)
			}
			servers = append(servers, map[string]any{"name": agentruntime.BridgeServerName, "command": command, "args": args, "env": []any{}})
		}
	}
	params := map[string]any{"cwd": d.ctx.Directory, "mcpServers": servers}
	method := "session/new"
	if resuming {
		if !d.restorable {
			return d.diagnosticError("restore checkpoint", errors.New("ACP session checkpoint cannot be restored"))
		}
		method = "session/load"
		if init.Capabilities.Session.Resume != nil {
			method = "session/resume"
		}
		params["sessionId"] = saved.SessionID
	}
	var session acpSession
	d.mu.Lock()
	d.sessionMethod = method
	d.mu.Unlock()
	if err := peer.Request(ctx, method, params, &session); err != nil {
		return d.startError(method, err)
	}
	sessionID := session.ID
	if resuming {
		sessionID = saved.SessionID
	}
	if sessionID == "" {
		return d.diagnosticError(method, errors.New("the agent opened no session"))
	}
	d.mu.Lock()
	d.sessionID = sessionID
	d.mu.Unlock()
	if !resuming || len(session.Models) != 0 || session.option("model").ID != "" {
		session, err = d.selectModel(ctx, session)
		if err != nil {
			return d.startError("select model", err)
		}
	}
	d.mu.Lock()
	d.first = !resuming
	d.thoughtOption = session.option("thought_level")
	d.defaultLevel = agentruntime.TrimmedJSON(d.thoughtOption.Current)
	if resuming && saved.DefaultLevel != "" {
		d.defaultLevel = saved.DefaultLevel
	}
	d.contextUsed = saved.ContextUsed
	d.started = true
	d.mu.Unlock()
	return nil
}

func (d *acpDriver) startError(operation string, err error) error {
	retryable := d.hooks.RetryableStartError != nil && d.hooks.RetryableStartError(operation, err)
	var remote *rpc.Error
	if errors.As(err, &remote) && remote.Code == -32000 {
		err = fmt.Errorf("the agent asks to sign in first (run it once in a terminal): %w", err)
	}
	tail := strings.Split(d.stdio.Process.StderrTail(), "\n")
	if len(tail) > 3 {
		tail = tail[len(tail)-3:]
	}
	if text := strings.Join(tail, "\n"); text != "" {
		err = fmt.Errorf("%w: %s", err, text)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return &agentruntime.NativeAgentError{Err: err, Detail: d.diagnosticLocked(operation, err), Retryable: retryable}
}

func (d *acpDriver) HistoryBudget(system string) int {
	overhead := len("<instructions>\n") + len(system) + len("\n</instructions>\n\n")
	return max(agentruntime.MinHistoryReplayBytes, agentruntime.DefaultHistoryReplayBytes-overhead)
}

func (d *acpDriver) recordSettledUsageLocked(u *TurnUsage) {
	if u == nil {
		return
	}
	input := u.Input
	if d.hooks.AdjustInputTokens != nil {
		input = d.hooks.AdjustInputTokens(*u)
	}
	if input <= 0 && u.Output <= 0 && u.Cached <= 0 && u.Written <= 0 && u.Thought <= 0 {
		return
	}
	if d.settledUsage == nil {
		d.settledUsage = &agentruntime.Usage{}
	}
	d.settledUsage.Input += input
	d.settledUsage.Output += u.Output
	if u.Cached > 0 {
		if d.settledUsage.Cached == nil {
			d.settledUsage.Cached = new(int64)
		}
		*d.settledUsage.Cached += u.Cached
	}
	if u.Written > 0 {
		if d.settledUsage.CacheWrite == nil {
			d.settledUsage.CacheWrite = new(int64)
		}
		*d.settledUsage.CacheWrite += u.Written
	}
	if u.Thought > 0 {
		if d.settledUsage.Reasoning == nil {
			d.settledUsage.Reasoning = new(int64)
		}
		*d.settledUsage.Reasoning += u.Thought
	}
}

func (d *acpDriver) DrainUsage() *agentruntime.Usage {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.settledUsage == nil {
		return nil
	}
	usage := *d.settledUsage
	d.settledUsage = nil
	return &usage
}

func (d *acpDriver) Prompt(ctx context.Context, input agentruntime.Input, effort string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.mu.Lock()
	if d.closing || !d.started || d.active {
		d.mu.Unlock()
		return errors.New("ACP session is not ready for a prompt")
	}
	text := input.Text
	if d.first {
		text = "<instructions>\n" + d.ctx.System + "\n</instructions>\n\n" + text
	}
	d.first = false
	d.active = true
	if d.filter != nil {
		d.filter.Reset()
	}
	sessionID := d.sessionID
	prompt := []any{map[string]any{"type": "text", "text": text}}
	if d.images {
		for _, image := range input.Images {
			prompt = append(prompt, map[string]any{"type": "image", "mimeType": image.MIME, "data": image.Data})
		}
	}
	done := make(chan struct{})
	d.promptDone = done
	d.requests.Add(1)
	d.mu.Unlock()
	// session/prompt returns only after the whole turn. Its lifetime spans the
	// Ariadne steps that separately execute its pending tool calls.
	go func() {
		defer close(done)
		defer d.requests.Done()
		var result struct {
			Stop  string     `json:"stopReason"`
			Usage *TurnUsage `json:"usage"`
		}
		err := d.stdio.Peer.Request(d.life, "session/prompt", map[string]any{"sessionId": sessionID, "prompt": prompt}, &result)
		d.mu.Lock()
		defer d.mu.Unlock()
		d.active = false
		if d.closing {
			if err == nil && result.Usage != nil {
				d.recordSettledUsageLocked(result.Usage)
			}
			return
		}
		var pending, filterDetail string
		if d.filter != nil {
			pending, filterDetail = d.filter.OnTurnComplete(err)
		}
		var detail string
		if err != nil {
			detail = err.Error()
		} else if filterDetail != "" {
			detail = filterDetail
		}
		if detail != "" {
			quota := rpc.QuotaText(detail)
			busy := quota || rpc.BusyText(detail)
			retryable := !quota && rpc.BusyText(detail)
			operation := "session/prompt"
			if err == nil {
				operation = "session/update"
			}
			diagnostic := d.diagnosticLocked(operation, err)
			diagnostic.StopReason = result.Stop
			d.emitLocked(agentruntime.Event{Type: "failed", Detail: detail, Retryable: retryable, Busy: busy, AgentError: diagnostic})
			return
		}
		d.flushThoughtLocked()
		if pending != "" {
			d.emitLocked(agentruntime.Event{Type: "text", Text: pending})
		}
		var contextInput *int64
		if d.contextUsed != nil {
			used := *d.contextUsed
			contextInput = &used
		}
		if u := result.Usage; u != nil {
			input := u.Input
			if d.hooks.AdjustInputTokens != nil {
				input = d.hooks.AdjustInputTokens(*u)
			}
			d.emitLocked(agentruntime.Event{Type: "usage", Usage: agentruntime.Usage{
				Input:        input,
				Output:       u.Output,
				Cached:       &u.Cached,
				CacheWrite:   &u.Written,
				Reasoning:    &u.Thought,
				ContextInput: contextInput,
			}})
		} else if contextInput != nil {
			d.emitLocked(agentruntime.Event{Type: "usage", Usage: agentruntime.Usage{ContextInput: contextInput}})
		}
		var failure string
		switch result.Stop {
		case "end_turn":
			d.emitLocked(agentruntime.Event{Type: "done"})
		case "max_tokens", "max_turn_requests":
			d.emitLocked(agentruntime.Event{Type: "done", StopReason: "length"})
		case "refusal":
			failure = "the agent refused to go on"
		case "cancelled":
			failure = "the agent cancelled the turn"
		default:
			failure = fmt.Sprintf("unknown ACP stop reason %q", result.Stop)
		}
		if failure != "" {
			diagnostic := d.diagnosticLocked("session/prompt", nil)
			diagnostic.StopReason = result.Stop
			d.emitLocked(agentruntime.Event{Type: "failed", Detail: failure, AgentError: diagnostic})
		}
	}()
	return nil
}

func (d *acpDriver) Checkpoint() (json.RawMessage, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.restorable || !d.started || d.closing || d.sessionID == "" {
		return nil, nil
	}
	return json.Marshal(acpCheckpoint{SessionID: d.sessionID, AgentProfile: d.agentProfile, DefaultLevel: d.defaultLevel, ContextUsed: d.contextUsed})
}

func (d *acpDriver) Close(ctx context.Context, discard bool) error {
	d.mu.Lock()
	d.closing = true
	active, sessionID, promptDone := d.active, d.sessionID, d.promptDone
	d.mu.Unlock()
	if d.removeTools != nil {
		d.removeTools()
	}
	var err error
	if d.stdio != nil {
		if active && sessionID != "" {
			notify, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
			_ = d.stdio.Peer.Notify(notify, "session/cancel", map[string]any{"sessionId": sessionID})
			cancel()
			if promptDone != nil {
				wait := time.NewTimer(time.Second)
				select {
				case <-promptDone:
				case <-wait.C:
				case <-ctx.Done():
				}
				wait.Stop()
			}
		}
		err = d.stdio.Close(ctx)
	}
	d.requests.Wait()
	d.mu.Lock()
	d.thought.Reset()
	if d.filter != nil {
		d.filter.Reset()
	}
	d.mu.Unlock()
	return err
}
