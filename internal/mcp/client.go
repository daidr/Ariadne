package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/daidr/Ariadne/internal/rpc"
)

type RemoteError = rpc.Error
type ClientOptions struct {
	Context                   context.Context
	SetupTimeout, CallTimeout time.Duration
}
type Client struct {
	mu              sync.Mutex
	transport       Transport
	peer            *rpc.Peer
	options         ClientOptions
	ctx             context.Context
	cancel          context.CancelFunc
	info            *ServerInfo
	closed          error
	connecting      bool
	closeListeners  []func(error)
	changeListeners []func()
	closeOnce       sync.Once
	closeDone       chan struct{}
	closeErr        error
	active          int
}

func NewClient(transport Transport, options ClientOptions) (*Client, error) {
	if transport == nil {
		return nil, errors.New("MCP transport is required")
	}
	if options.Context == nil {
		options.Context = context.Background()
	}
	if options.SetupTimeout <= 0 {
		options.SetupTimeout = time.Minute
	}
	if options.CallTimeout <= 0 {
		options.CallTimeout = 5 * time.Minute
	}
	ctx, cancel := context.WithCancel(options.Context)
	client := &Client{transport: transport, options: options, ctx: ctx, cancel: cancel, closeDone: make(chan struct{})}
	peer, err := rpc.New(rpc.Options{Context: ctx, Strict: true, Send: transport.Send,
		Request: func(ctx context.Context, method string, _ json.RawMessage) (any, error) {
			if method == "ping" {
				return map[string]any{}, nil
			}
			return nil, rpc.Unsupported(method)
		},
		Notification: func(method string, _ json.RawMessage) {
			if method != "notifications/tools/list_changed" {
				return
			}
			client.mu.Lock()
			callbacks := append([]func(){}, client.changeListeners...)
			closed := client.closed != nil
			client.mu.Unlock()
			if !closed {
				for _, callback := range callbacks {
					callback()
				}
			}
		},
		OnCancel: func(id int64, method string, err error) {
			if method == "initialize" {
				return
			} // MCP forbids cancellation of initialization.
			reason := "cancelled by the user"
			if errors.Is(err, context.DeadlineExceeded) {
				reason = "timeout"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			defer cancel()
			_ = client.peer.Notify(ctx, "notifications/cancelled", map[string]any{"requestId": id, "reason": reason})
		},
		OnRequestEnd: func(id int64) {
			if transport, ok := transport.(requestTransport); ok {
				transport.EndRequest(id)
			}
		},
	})
	if err != nil {
		cancel()
		return nil, err
	}
	client.peer = peer
	return client, nil
}
func (client *Client) Closed() bool {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.closed != nil || client.ctx.Err() != nil
}
func (client *Client) Info() *ServerInfo {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.info == nil {
		return nil
	}
	info := *client.info
	return &info
}
func (client *Client) OnClose(callback func(error)) error {
	client.mu.Lock()
	if client.closed != nil {
		err := client.closed
		client.mu.Unlock()
		callback(err)
		return nil
	}
	if len(client.closeListeners) >= 16 {
		client.mu.Unlock()
		return errors.New("too many MCP close listeners")
	}
	client.closeListeners = append(client.closeListeners, callback)
	client.mu.Unlock()
	return nil
}
func (client *Client) OnToolsChanged(callback func()) error {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.closed != nil {
		return client.closed
	}
	if len(client.changeListeners) >= 16 {
		return errors.New("too many MCP tool listeners")
	}
	client.changeListeners = append(client.changeListeners, callback)
	return nil
}
func (client *Client) markEnded(err error) ([]func(error), error) {
	client.mu.Lock()
	if client.closed != nil {
		client.mu.Unlock()
		return nil, err
	}
	if err == nil {
		err = &ConnectionError{Reason: "the connection was closed"}
	}
	client.closed = err
	callbacks := client.closeListeners
	client.closeListeners = nil
	client.changeListeners = nil
	client.mu.Unlock()
	client.peer.End(err)
	return callbacks, err
}
func (client *Client) end(err error) {
	callbacks, reason := client.markEnded(err)
	for _, callback := range callbacks {
		callback(reason)
	}
}
func (client *Client) Connect(ctx context.Context) (info *ServerInfo, err error) {
	client.mu.Lock()
	if client.connecting || client.closed != nil {
		client.mu.Unlock()
		return nil, &ConnectionError{Reason: "the connection was already started or closed"}
	}
	client.connecting = true
	client.mu.Unlock()
	defer func() {
		if err != nil {
			client.end(err)
			cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = client.Close(cleanup)
		}
	}()
	if err := client.transport.Start(client.ctx, client.peer.Take, client.end); err != nil {
		return nil, err
	}
	var result map[string]json.RawMessage
	if err := client.request(ctx, "initialize", map[string]any{"protocolVersion": ProtocolVersions[0], "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "ariadne", "version": "0.0.0"}}, client.options.SetupTimeout, &result); err != nil {
		return nil, err
	}
	var version string
	_ = json.Unmarshal(result["protocolVersion"], &version)
	known := false
	for _, supported := range ProtocolVersions {
		known = known || version == supported
	}
	if !known {
		return nil, &ConnectionError{Reason: "the server speaks no protocol version this client knows"}
	}
	if transport, ok := client.transport.(protocolTransport); ok {
		transport.SetProtocolVersion(version)
	}
	if err := client.peer.Notify(ctx, "notifications/initialized", nil); err != nil {
		return nil, err
	}
	if transport, ok := client.transport.(notificationsTransport); ok {
		transport.StartNotifications()
	}
	server := rawFields(result["serverInfo"])
	info = &ServerInfo{Name: rawText(server["name"]), Version: rawText(server["version"]), ProtocolVersion: version}
	client.mu.Lock()
	client.info = info
	client.mu.Unlock()
	copy := *info
	return &copy, nil
}
func rawFields(raw json.RawMessage) map[string]json.RawMessage {
	var value map[string]json.RawMessage
	_ = json.Unmarshal(raw, &value)
	return value
}
func rawText(raw json.RawMessage) string {
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}
func (client *Client) request(ctx context.Context, method string, params any, timeout time.Duration, target any) error {
	client.mu.Lock()
	closed := client.closed
	client.mu.Unlock()
	if closed != nil {
		return closed
	}
	if client.ctx.Err() != nil {
		return &ConnectionError{Reason: "the connection is closed"}
	}
	client.mu.Lock()
	client.active++
	client.mu.Unlock()
	defer func() { client.mu.Lock(); client.active--; client.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err := client.peer.Request(ctx, method, params, target)
	if errors.Is(err, context.DeadlineExceeded) {
		return &ConnectionError{Reason: fmt.Sprintf("no answer to %s within %g seconds", method, timeout.Seconds())}
	}
	return err
}

func (client *Client) Pending() int { client.mu.Lock(); defer client.mu.Unlock(); return client.active }
func (client *Client) ListTools(ctx context.Context) ([]ToolInfo, error) {
	tools := []ToolInfo{}
	cursor := ""
	weight := 0
	for range 20 {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var raw json.RawMessage
		if err := client.request(ctx, "tools/list", params, client.options.SetupTimeout, &raw); err != nil {
			return nil, err
		}
		weight += len(raw)
		if weight > MaxMessageBytes {
			return nil, &ConnectionError{Reason: "tools/list exceeded its size limit"}
		}
		page, next, err := ToolsOf(raw)
		if err != nil {
			return nil, err
		}
		if len(page) > 10000-len(tools) {
			return nil, &ConnectionError{Reason: "tools/list exceeded its tool count limit"}
		}
		tools = append(tools, page...)
		cursor = next
		if cursor == "" {
			break
		}
	}
	return tools, nil
}
func (client *Client) CallTool(ctx context.Context, name string, args map[string]any, timeout time.Duration) (ToolResult, error) {
	if timeout <= 0 {
		timeout = client.options.CallTimeout
	}
	var raw json.RawMessage
	if args == nil {
		args = map[string]any{}
	}
	if err := client.request(ctx, "tools/call", map[string]any{"name": name, "arguments": args}, timeout, &raw); err != nil {
		return ToolResult{}, err
	}
	return ToolResultOf(raw)
}
func (client *Client) Close(ctx context.Context) error {
	var callbacks []func(error)
	var reason error
	client.closeOnce.Do(func() {
		callbacks, reason = client.markEnded(&ConnectionError{Reason: "the connection was closed"})
		client.cancel()
		go func() {
			defer close(client.closeDone)
			cleanup, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			client.closeErr = client.transport.Close(cleanup)
			if err := client.peer.Close(cleanup); client.closeErr == nil {
				client.closeErr = err
			}
			client.mu.Lock()
			client.info = nil
			client.mu.Unlock()
		}()
	})
	for _, callback := range callbacks {
		callback(reason)
	}
	select {
	case <-client.closeDone:
		return client.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}
