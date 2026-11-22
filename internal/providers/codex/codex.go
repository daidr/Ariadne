package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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

var codexOwnTools = []string{
	"shell_tool", "unified_exec", "view_image", "image_generation", "multi_agent", "multi_agent_v2",
	"apps", "plugins", "browser_use", "in_app_browser", "computer_use", "sleep_tool", "tool_suggest",
	"skill_search", "memories", "goals", "code_mode", "code_mode_only", "deferred_executor",
	"request_permissions_tool", "send_message_to_user_async", "token_budget", "current_time_reminder",
}
var codexLocalContext = []string{"include_environment_context=false", "include_permissions_instructions=false", "include_apps_instructions=false", "skills.include_instructions=false", "skills.bundled.enabled=false", "project_doc_max_bytes=0"}

func codexCommand(settings Settings) []string {
	command := textutil.Trim(settings.Command)
	if command == "" {
		command = DefaultCommand
	}
	return process.SplitCommand(command)
}

func Argv(settings Settings) []string {
	argv := append(codexCommand(settings), "app-server",
		"-c", `web_search="disabled"`,
		"-c", "mcp_servers={}",
		"-c", "tools.experimental_request_user_input.enabled=false",
		"-c", "tools.update_plan.enabled=false",
		"-c", "features.hooks=false",
		"-c", "features.step_model_switching=true",
	)
	for _, tool := range codexOwnTools {
		argv = append(argv, "-c", "features."+tool+"=false")
	}
	for _, setting := range codexLocalContext {
		argv = append(argv, "-c", setting)
	}
	return argv
}

func writeCodexToolFreeCatalog(ctx context.Context, settings Settings, directory string, env map[string]string) (string, error) {
	argv := append(codexCommand(settings), "debug", "models")
	result := process.Run(ctx, argv, process.RunOptions{Directory: directory, Env: env, ReplaceEnv: true, Whole: true, Timeout: time.Minute})
	if result.ExitCode != 0 {
		return "", fmt.Errorf("read Codex model catalog (exit %d): %s", result.ExitCode, textutil.Trim(result.Stderr))
	}
	return saveCodexToolFreeCatalog(directory, []byte(result.Stdout))
}

func saveCodexToolFreeCatalog(directory string, data []byte) (string, error) {
	var catalog map[string]json.RawMessage
	if err := json.Unmarshal(data, &catalog); err != nil {
		return "", fmt.Errorf("decode Codex model catalog: %w", err)
	}
	var models []map[string]json.RawMessage
	if err := json.Unmarshal(catalog["models"], &models); err != nil {
		return "", fmt.Errorf("decode Codex model entries: %w", err)
	}
	if len(models) == 0 {
		return "", errors.New("the Codex model catalog is empty")
	}
	for _, model := range models {
		if model == nil {
			return "", errors.New("the Codex model catalog contains an empty entry")
		}
		delete(model, "apply_patch_tool_type")
		model["experimental_supported_tools"] = json.RawMessage(`[]`)
		model["supports_search_tool"] = json.RawMessage(`false`)
		model["tool_mode"] = json.RawMessage(`"direct"`)
		delete(model, "multi_agent_version")
	}
	entries, err := json.Marshal(models)
	if err != nil {
		return "", err
	}
	catalog["models"] = entries
	directory, err = filepath.Abs(directory)
	if err != nil {
		return "", err
	}
	// The session owns this directory and removes it after app-server exits.
	file, err := os.CreateTemp(directory, "codex-models-*.json")
	if err != nil {
		return "", err
	}
	if err := errors.Join(json.NewEncoder(file).Encode(catalog), file.Close()); err != nil {
		_ = os.Remove(file.Name())
		return "", err
	}
	return file.Name(), nil
}

var codexVersion = regexp.MustCompile(`/([0-9]+)\.([0-9]+)\.([0-9]+)`)

func CodexTooOld(userAgent string) string {
	match := codexVersion.FindStringSubmatch(userAgent)
	if match == nil {
		return ""
	}
	for i, minimum := range []int{0, 167, 1} {
		version, err := strconv.Atoi(match[i+1])
		if err != nil {
			return ""
		}
		if version != minimum {
			if version < minimum {
				return strings.Join(match[1:], ".")
			}
			return ""
		}
	}
	return ""
}

type codexModel struct {
	ID          json.RawMessage `json:"id"`
	Model       json.RawMessage `json:"model"`
	DisplayName json.RawMessage `json:"displayName"`
	Efforts     json.RawMessage `json:"supportedReasoningEfforts"`
}

func ParseModels(data json.RawMessage) []sdk.ListedModel {
	var entries []codexModel
	if json.Unmarshal(data, &entries) != nil {
		return nil
	}
	models := []sdk.ListedModel{}
	for _, entry := range entries {
		id := agentruntime.TrimmedJSON(entry.Model)
		if id == "" {
			id = agentruntime.TrimmedJSON(entry.ID)
		}
		if id == "" {
			continue
		}
		model := sdk.ListedModel{ID: id}
		title := agentruntime.TrimmedJSON(entry.DisplayName)
		if title != id {
			model.Title = title
		}
		var listed []json.RawMessage
		if len(entry.Efforts) > 0 && string(entry.Efforts) != "null" && json.Unmarshal(entry.Efforts, &listed) == nil {
			names := map[string]bool{}
			for _, raw := range listed {
				var name string
				if json.Unmarshal(raw, &name) != nil {
					var value struct {
						Effort string `json:"reasoningEffort"`
					}
					if json.Unmarshal(raw, &value) == nil {
						name = textutil.Trim(value.Effort)
					}
				}
				names[name] = true
			}
			valid := []string{}
			for _, effort := range sdk.Efforts {
				if names[effort] {
					valid = append(valid, effort)
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
	stdio, err := rpc.StartStdio(ctx, process.JSONOptions{Argv: Argv(settings), Directory: directory, Env: process.AgentEnv(nil), ReplaceEnv: true}, rpc.Options{})
	if err != nil {
		return nil, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = stdio.Close(cleanup)
	}()
	if err := stdio.Peer.Request(ctx, "initialize", map[string]any{"clientInfo": agentruntime.ClientInfo}, nil); err != nil {
		return nil, err
	}
	if err := stdio.Peer.Notify(ctx, "initialized", nil); err != nil {
		return nil, err
	}
	models := []sdk.ListedModel{{ID: "default"}}
	cursor := ""
	for range 10 {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var result struct {
			Data       json.RawMessage `json:"data"`
			NextCursor string          `json:"nextCursor"`
		}
		if err := stdio.Peer.Request(ctx, "model/list", params, &result); err != nil {
			return nil, err
		}
		models = append(models, ParseModels(result.Data)...)
		cursor = result.NextCursor
		if cursor == "" {
			break
		}
	}
	return models, nil
}
func Kind() agentruntime.Kind {
	return agentruntime.Kind{Name: ProviderKind, Persistent: true, MutableEffort: true, Fingerprint: agentruntime.NativeFingerprint, Create: func(ctx agentruntime.DriverContext) (agentruntime.Driver, error) {
		settings := Settings{}
		if command, ok := ctx.Settings["command"].(string); ok {
			settings.Command = command
		}
		return &codexDriver{ctx: ctx, settings: settings, streamed: map[string]bool{}, supportsUpdates: true}, nil
	}}
}

type tokenBreakdown struct {
	Input      *int64 `json:"inputTokens,omitempty"`
	Cached     *int64 `json:"cachedInputTokens,omitempty"`
	CacheWrite *int64 `json:"cacheWriteInputTokens,omitempty"`
	Output     *int64 `json:"outputTokens,omitempty"`
	Reasoning  *int64 `json:"reasoningOutputTokens,omitempty"`
}
type codexSaved struct {
	ThreadID      string          `json:"threadId"`
	Total         *tokenBreakdown `json:"total"`
	DefaultEffort string          `json:"defaultEffort,omitempty"`
}
type codexDriver struct {
	ctx                                 agentruntime.DriverContext
	settings                            Settings
	mu                                  sync.Mutex
	stdio                               *rpc.Stdio
	threadID, defaultEffort, activeTurn string
	currentEffort                       *string
	supportsUpdates, started, closing   bool
	total                               tokenBreakdown
	streamed                            map[string]bool
	epoch                               uint64
}

func dataURL(image sdk.Image) string { return "data:" + image.MIME + ";base64," + image.Data }
func (d *codexDriver) request(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	var params struct {
		Tool      string `json:"tool"`
		Arguments any    `json:"arguments"`
	}
	switch method {
	case "item/tool/call":
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, err
		}
		if params.Arguments == nil {
			params.Arguments = map[string]any{}
		}
		output := d.ctx.Tools.Call(ctx, params.Tool, params.Arguments)
		items := []any{map[string]any{"type": "inputText", "text": output.Content}}
		for _, image := range output.Images {
			items = append(items, map[string]any{"type": "inputImage", "imageUrl": dataURL(image)})
		}
		return map[string]any{"contentItems": items, "success": !output.IsError}, nil
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		return map[string]any{"decision": "decline"}, nil
	case "execCommandApproval", "applyPatchApproval":
		return map[string]any{"decision": map[string]any{"denied": map[string]any{"rejection": "Use the tools you were given."}}}, nil
	case "mcpServer/elicitation/request":
		return map[string]any{"action": "decline", "content": nil, "_meta": nil}, nil
	default:
		return nil, fmt.Errorf("%w: %s is not supported", rpc.ErrUnhandled, method)
	}
}
func tokenValue(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}
func tokenGrowth(now, before *int64) *int64 {
	return new(max(int64(0), tokenValue(now)-tokenValue(before)))
}
func (d *codexDriver) notification(method string, raw json.RawMessage) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closing {
		return
	}
	emit := d.ctx.Emit
	switch method {
	case "item/agentMessage/delta":
		var params struct {
			Delta  string `json:"delta"`
			ItemID string `json:"itemId"`
		}
		if json.Unmarshal(raw, &params) != nil {
			return
		}
		if params.Delta != "" {
			if params.ItemID != "" {
				if len(d.streamed) >= 1024 && !d.streamed[params.ItemID] {
					emit(agentruntime.Event{Type: "failed", Detail: "Codex streamed items exceeded their limit"})
					return
				}
				d.streamed[params.ItemID] = true
			}
			emit(agentruntime.Event{Type: "text", Text: params.Delta})
		} else {
			emit(agentruntime.Event{Type: "alive"})
		}
	case "item/completed":
		var params struct {
			Item struct {
				Type, ID, Text   string
				Summary, Content []string
			} `json:"item"`
		}
		if json.Unmarshal(raw, &params) != nil {
			return
		}
		item := params.Item
		if item.Type == "agentMessage" && item.Text != "" {
			if item.ID != "" && d.streamed[item.ID] {
				delete(d.streamed, item.ID)
				emit(agentruntime.Event{Type: "alive"})
			} else {
				emit(agentruntime.Event{Type: "text", Text: item.Text})
			}
		} else if item.Type == "reasoning" {
			text := textutil.Trim(strings.Join(append(item.Summary, item.Content...), "\n"))
			if text != "" {
				emit(agentruntime.Event{Type: "reasoning", Text: text})
			}
		} else {
			emit(agentruntime.Event{Type: "alive"})
		}
	case "thread/tokenUsage/updated":
		var params struct {
			TokenUsage struct {
				Total tokenBreakdown `json:"total"`
				Last  tokenBreakdown `json:"last"`
			} `json:"tokenUsage"`
		}
		if json.Unmarshal(raw, &params) != nil {
			return
		}
		now := params.TokenUsage.Total
		usage := agentruntime.Usage{Input: *tokenGrowth(now.Input, d.total.Input), Output: *tokenGrowth(now.Output, d.total.Output), Cached: tokenGrowth(now.Cached, d.total.Cached), CacheWrite: tokenGrowth(now.CacheWrite, d.total.CacheWrite), Reasoning: tokenGrowth(now.Reasoning, d.total.Reasoning), ContextInput: params.TokenUsage.Last.Input}
		d.total = now
		if d.started && (usage.Input != 0 || usage.Output != 0 || tokenValue(usage.ContextInput) > 0) {
			emit(agentruntime.Event{Type: "usage", Usage: usage})
		}
	case "turn/completed":
		d.activeTurn = ""
		d.epoch++
		clear(d.streamed)
		var params struct {
			Turn struct {
				Status string `json:"status"`
				Error  *struct {
					Message    string `json:"message"`
					Additional string `json:"additionalDetails"`
				} `json:"error"`
			} `json:"turn"`
		}
		if json.Unmarshal(raw, &params) != nil {
			return
		}
		if params.Turn.Status == "completed" {
			emit(agentruntime.Event{Type: "done"})
		} else {
			detail := ""
			if params.Turn.Error != nil {
				detail = params.Turn.Error.Message
				if params.Turn.Error.Additional != "" {
					if detail != "" {
						detail += ": "
					}
					detail += params.Turn.Error.Additional
				}
			}
			if detail == "" {
				status := params.Turn.Status
				if status == "" {
					status = "unexpectedly"
				}
				detail = "the turn ended " + status
			}
			quota := rpc.QuotaText(detail)
			busy := quota || rpc.BusyText(detail)
			retryable := !quota && rpc.BusyText(detail)
			emit(agentruntime.Event{Type: "failed", Detail: detail, Retryable: retryable, Busy: busy})
		}
	default:
		emit(agentruntime.Event{Type: "alive"})
	}
}
func (d *codexDriver) Start(life context.Context) error {
	env := process.AgentEnv(nil)
	catalog, err := writeCodexToolFreeCatalog(life, d.settings, d.ctx.Directory, env)
	if err != nil {
		return err
	}
	argv := append(Argv(d.settings), "-c", "model_catalog_json="+strconv.Quote(filepath.ToSlash(catalog)))
	lines := process.NewJSONLines(process.JSONOptions{Argv: argv, Directory: d.ctx.Directory, Env: env, ReplaceEnv: true})
	peer, err := rpc.New(rpc.Options{Context: life, Send: lines.SendJSON, Request: d.request, Notification: d.notification})
	if err != nil {
		return err
	}
	d.stdio = &rpc.Stdio{Peer: peer, Process: lines}
	if err := lines.Start(life, peer.Take, func(err error) {
		peer.End(err)
		d.mu.Lock()
		started, closing := d.started, d.closing
		d.mu.Unlock()
		if !closing {
			d.ctx.Emit(agentruntime.Event{Type: "failed", Detail: err.Error(), Retryable: started})
		}
	}); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(life, time.Minute)
	defer cancel()
	var init struct {
		UserAgent string `json:"userAgent"`
	}
	if err = peer.Request(ctx, "initialize", map[string]any{"clientInfo": agentruntime.ClientInfo, "capabilities": map[string]any{"experimentalApi": true}}, &init); err != nil {
		return d.startError(err)
	}
	if old := CodexTooOld(init.UserAgent); old != "" {
		return d.startError(fmt.Errorf("codex %s is too old: 0.167.1 or later is needed, update it first", old))
	}
	if err = peer.Notify(ctx, "initialized", nil); err != nil {
		return d.startError(err)
	}
	var config struct {
		Config struct {
			MCPServers map[string]json.RawMessage `json:"mcp_servers"`
		} `json:"config"`
	}
	if err = peer.Request(ctx, "config/read", map[string]any{"cwd": d.ctx.Directory}, &config); err != nil {
		return d.startError(err)
	}
	var saved *codexSaved
	if len(d.ctx.Resume) > 0 {
		if json.Unmarshal(d.ctx.Resume, &saved) != nil || saved == nil || saved.ThreadID == "" || saved.Total == nil {
			return d.startError(errors.New("invalid Codex resume checkpoint"))
		}
		for _, value := range []*int64{saved.Total.Input, saved.Total.Cached, saved.Total.CacheWrite, saved.Total.Output, saved.Total.Reasoning} {
			if value != nil && *value < 0 {
				return d.startError(errors.New("invalid Codex resume checkpoint"))
			}
		}
		d.mu.Lock()
		d.total = *saved.Total
		d.mu.Unlock()
	}
	tools := []any{}
	for _, tool := range d.ctx.Tools.Specs {
		schema := map[string]any{"type": "object"}
		for key, value := range tool.Parameters {
			schema[key] = value
		}
		tools = append(tools, map[string]any{"type": "function", "name": tool.Name, "description": tool.Description, "inputSchema": schema, "deferLoading": false})
	}
	params := map[string]any{"cwd": d.ctx.Directory, "approvalPolicy": "untrusted", "approvalsReviewer": "user", "sandbox": "read-only", "baseInstructions": d.ctx.System, "environments": []any{}, "dynamicTools": tools}
	if len(config.Config.MCPServers) > 0 {
		// Codex merges TOML tables, so an empty table does not clear inherited servers.
		servers := make(map[string]any, len(config.Config.MCPServers))
		for name := range config.Config.MCPServers {
			servers[name] = map[string]bool{"enabled": false}
		}
		params["config"] = map[string]any{"mcp_servers": servers}
	}
	if d.ctx.Model != "" && d.ctx.Model != "default" {
		params["model"] = d.ctx.Model
	}
	method := "thread/start"
	if saved != nil {
		method = "thread/resume"
		params["threadId"] = saved.ThreadID
		params["excludeTurns"] = true
	} else {
		params["ephemeral"] = !d.ctx.Persistent
	}
	var result struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
		Effort string `json:"reasoningEffort"`
	}
	if err = peer.Request(ctx, method, params, &result); err != nil {
		return d.startError(err)
	}
	if result.Thread.ID == "" {
		return d.startError(errors.New("codex started no thread"))
	}
	d.mu.Lock()
	d.threadID = result.Thread.ID
	d.defaultEffort = result.Effort
	if saved != nil {
		d.defaultEffort = saved.DefaultEffort
	}
	d.started = true
	d.mu.Unlock()
	return nil
}
func (d *codexDriver) startError(err error) error {
	tail := strings.Split(d.stdio.Process.StderrTail(), "\n")
	if len(tail) > 3 {
		tail = tail[len(tail)-3:]
	}
	text := strings.Join(tail, "\n")
	if text != "" {
		return fmt.Errorf("%w: %s", err, text)
	}
	return err
}

func History(messages []sdk.Message) ([]any, agentruntime.Input) {
	history := messages
	var last *sdk.Message
	if len(messages) > 0 && messages[len(messages)-1].Role == "user" {
		last = &messages[len(messages)-1]
		history = messages[:len(messages)-1]
	}
	items := []any{}
	content := func(message sdk.Message) []any {
		parts := []any{map[string]any{"type": "input_text", "text": message.Content}}
		for _, image := range message.Images {
			parts = append(parts, map[string]any{"type": "input_image", "image_url": dataURL(image)})
		}
		return parts
	}
	for _, message := range history {
		switch message.Role {
		case "user":
			items = append(items, map[string]any{"type": "message", "role": "user", "content": content(message)})
		case "assistant":
			if message.Content != "" {
				items = append(items, map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": message.Content}}})
			}
			for _, call := range message.ToolCalls {
				items = append(items, map[string]any{"type": "function_call", "call_id": call.ID, "name": call.Name, "arguments": call.Arguments})
			}
		case "tool":
			items = append(items, map[string]any{"type": "function_call_output", "call_id": message.ToolCallID, "output": content(message)})
		}
	}
	if last != nil {
		images := last.Images
		if images == nil {
			images = []sdk.Image{}
		}
		return items, agentruntime.Input{Text: last.Content, Images: images}
	}
	return items, agentruntime.Input{Text: "Continue from the latest tool results. All recorded tool calls have already run.", Images: []sdk.Image{}}
}
func (d *codexDriver) Seed(ctx context.Context, messages []sdk.Message) (agentruntime.Input, error) {
	items, input := History(messages)
	if len(items) > 0 {
		if err := d.stdio.Peer.Request(ctx, "thread/inject_items", map[string]any{"threadId": d.threadID, "items": items}, nil); err != nil {
			return agentruntime.Input{}, err
		}
	}
	return input, nil
}
func (d *codexDriver) Prompt(ctx context.Context, input agentruntime.Input, effort string) error {
	parts := []any{map[string]any{"type": "text", "text": input.Text, "text_elements": []any{}}}
	for _, image := range input.Images {
		parts = append(parts, map[string]any{"type": "image", "url": dataURL(image)})
	}
	d.mu.Lock()
	if effort == "" {
		effort = d.defaultEffort
	}
	var active *string
	if effort != "" {
		active = &effort
	}
	d.currentEffort = active
	epoch := d.epoch
	threadID := d.threadID
	d.mu.Unlock()
	var result struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := d.stdio.Peer.Request(ctx, "turn/start", map[string]any{"threadId": threadID, "input": parts, "effort": active}, &result); err != nil {
		return err
	}
	d.mu.Lock()
	if d.epoch == epoch {
		d.activeTurn = result.Turn.ID
	}
	d.mu.Unlock()
	return nil
}
func (d *codexDriver) Configure(ctx context.Context, effort string) error {
	d.mu.Lock()
	if effort == "" {
		effort = d.defaultEffort
	}
	var active *string
	if effort != "" {
		active = &effort
	}
	same := active == nil && d.currentEffort == nil || active != nil && d.currentEffort != nil && *active == *d.currentEffort
	turn, thread, enabled := d.activeTurn, d.threadID, d.supportsUpdates
	d.mu.Unlock()
	if turn == "" || !enabled || same {
		return nil
	}
	err := d.stdio.Peer.Request(ctx, "turn/settings/update", map[string]any{"threadId": thread, "turnId": turn, "effort": active}, nil)
	var remote *rpc.Error
	if errors.As(err, &remote) && remote.Code == -32600 && (strings.Contains(remote.Message, "only fallback metadata") || strings.Contains(remote.Message, "does not support reasoning effort updates")) {
		d.mu.Lock()
		d.supportsUpdates = false
		d.mu.Unlock()
		return nil
	}
	if err == nil {
		d.mu.Lock()
		d.currentEffort = active
		d.mu.Unlock()
	}
	return err
}
func (d *codexDriver) Checkpoint() (json.RawMessage, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return jsonutil.Encode(codexSaved{ThreadID: d.threadID, Total: &d.total, DefaultEffort: d.defaultEffort})
}
func (d *codexDriver) Close(ctx context.Context, discard bool) error {
	d.mu.Lock()
	d.closing = true
	thread, stdio := d.threadID, d.stdio
	d.mu.Unlock()
	if stdio == nil {
		return nil
	}
	if discard && d.ctx.Persistent && thread != "" {
		archive, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		_ = stdio.Peer.Request(archive, "thread/archive", map[string]any{"threadId": thread}, nil)
		cancel()
	}
	return stdio.Close(ctx)
}
