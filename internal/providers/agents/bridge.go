package agents

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/daidr/Ariadne/internal/jsonutil"
)

const BridgeServerName = "ariadne"
const bridgeProtocol = "2025-06-18"
const maxBridgeBody = 16 << 20
const maxBridgeResult = 32 << 20

type bridgeClient struct {
	tools  AgentTools
	ctx    context.Context
	cancel context.CancelFunc
	active chan struct{}
}
type BridgeRegistration struct {
	URL    string
	Remove func()
}
type ToolBridge struct {
	mu            sync.Mutex
	clients       map[string]*bridgeClient
	server        *http.Server
	listener      net.Listener
	done          chan struct{}
	closed        bool
	keepAlive     time.Duration
	writeTimeout  time.Duration
	handlers      sync.WaitGroup
	closeOnce     sync.Once
	closeDone     chan struct{}
	closeErr      error
	reading       chan struct{}
	incomingBytes int
}

func NewToolBridge() *ToolBridge {
	return &ToolBridge{clients: map[string]*bridgeClient{}, keepAlive: 20 * time.Second, writeTimeout: 10 * time.Second, reading: make(chan struct{}, 4)}
}
func (bridge *ToolBridge) Add(tools AgentTools) (BridgeRegistration, error) {
	if tools.Call == nil {
		return BridgeRegistration{}, errors.New("agent tool callback is required")
	}
	tokenBytes := make([]byte, 24)
	if _, err := rand.Read(tokenBytes); err != nil {
		return BridgeRegistration{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	if bridge.closed {
		return BridgeRegistration{}, errors.New("agent tool bridge is closed")
	}
	if bridge.server == nil {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			return BridgeRegistration{}, err
		}
		bridge.listener = listener
		bridge.done = make(chan struct{})
		bridge.server = &http.Server{Handler: bridge, ReadHeaderTimeout: 5 * time.Second, MaxHeaderBytes: 8 << 10, IdleTimeout: time.Minute}
		go func() { defer close(bridge.done); _ = bridge.server.Serve(listener) }()
	}
	ctx, cancel := context.WithCancel(context.Background())
	bridge.clients[token] = &bridgeClient{tools: tools, ctx: ctx, cancel: cancel, active: make(chan struct{}, 128)}
	var once sync.Once
	return BridgeRegistration{URL: "http://" + bridge.listener.Addr().String() + "/mcp/" + token, Remove: func() {
		once.Do(func() {
			bridge.mu.Lock()
			if client := bridge.clients[token]; client != nil {
				delete(bridge.clients, token)
				client.cancel()
			}
			bridge.mu.Unlock()
		})
	}}, nil
}
func (bridge *ToolBridge) Count() int {
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	return len(bridge.clients)
}
func (bridge *ToolBridge) Close(ctx context.Context) error {
	bridge.closeOnce.Do(func() {
		bridge.mu.Lock()
		bridge.closed = true
		bridge.closeDone = make(chan struct{})
		for key, client := range bridge.clients {
			client.cancel()
			delete(bridge.clients, key)
		}
		server, done := bridge.server, bridge.done
		bridge.mu.Unlock()
		go func() {
			defer close(bridge.closeDone)
			if server != nil {
				bridge.closeErr = server.Close()
				<-done
			}
			bridge.handlers.Wait()
		}()
	})
	select {
	case <-bridge.closeDone:
		return bridge.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

type bridgeMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type bridgeBudget struct {
	mu    sync.Mutex
	bytes int
}

func (budget *bridgeBudget) reserve(size int) bool {
	budget.mu.Lock()
	defer budget.mu.Unlock()
	if size > maxBridgeResult-65536-budget.bytes {
		return false
	}
	budget.bytes += size
	return true
}

func bridgeError(id json.RawMessage, code int, message string) map[string]any {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}}
}
func bridgeResult(id json.RawMessage, result any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
}
func bridgeJSON(w http.ResponseWriter, status int, value any) {
	data, err := jsonutil.Encode(value)
	if err != nil || len(data) > maxBridgeResult {
		data, _ = jsonutil.Encode(bridgeError(nil, -32603, "tool response exceeded its size limit"))
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}
func (bridge *ToolBridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	bridge.mu.Lock()
	if bridge.closed {
		bridge.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	bridge.handlers.Add(1)
	defer bridge.handlers.Done()
	token := strings.TrimPrefix(r.URL.Path, "/mcp/")
	client := bridge.clients[token]
	validHost := bridge.listener != nil && r.Host == bridge.listener.Addr().String()
	bridge.mu.Unlock()
	if client == nil || !strings.HasPrefix(r.URL.Path, "/mcp/") {
		http.NotFound(w, r)
		return
	}
	if !validHost || r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "http://"+r.Host {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if r.Method == http.MethodDelete {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	controller := http.NewResponseController(w)
	select {
	case bridge.reading <- struct{}{}:
	default:
		http.Error(w, "too many concurrent request bodies", 429)
		return
	}
	_ = controller.SetReadDeadline(time.Now().Add(30 * time.Second))
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBridgeBody))
	_ = controller.SetReadDeadline(time.Time{})
	<-bridge.reading
	if err != nil {
		var limit *http.MaxBytesError
		if errors.As(err, &limit) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		bridgeJSON(w, 400, bridgeError(nil, -32700, "parse error"))
		return
	}
	weight := len(data)
	bridge.mu.Lock()
	if weight > 64<<20-bridge.incomingBytes {
		bridge.mu.Unlock()
		http.Error(w, "tool requests exceeded their memory limit", 429)
		return
	}
	bridge.incomingBytes += weight
	bridge.mu.Unlock()
	defer func() { bridge.mu.Lock(); bridge.incomingBytes -= weight; bridge.mu.Unlock() }()
	data = bytes.TrimSpace(data)
	var messages []bridgeMessage
	batch := len(data) > 0 && data[0] == '['
	if batch {
		err = json.Unmarshal(data, &messages)
	} else {
		var message bridgeMessage
		err = json.Unmarshal(data, &message)
		messages = []bridgeMessage{message}
	}
	if err != nil {
		bridgeJSON(w, 400, bridgeError(nil, -32700, "parse error"))
		return
	}
	if len(messages) > 128 {
		bridgeJSON(w, 400, bridgeError(nil, -32600, "too many requests"))
		return
	}
	requests := messages[:0]
	for _, message := range messages {
		if message.Method != "" && len(message.ID) > 0 && string(message.ID) != "null" {
			requests = append(requests, message)
		}
	}
	if len(requests) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	select {
	case client.active <- struct{}{}:
		defer func() { <-client.active }()
	default:
		bridgeJSON(w, 429, bridgeError(nil, -32000, "too many active tool requests"))
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := context.AfterFunc(client.ctx, cancel)
	defer stop()
	if client.ctx.Err() != nil {
		return
	}
	if len(requests) == 1 && requests[0].Method == "tools/call" {
		bridge.stream(ctx, w, client, requests[0])
		return
	}
	answers := make([]any, len(requests))
	budget := &bridgeBudget{}
	var group sync.WaitGroup
	for index, message := range requests {
		group.Add(1)
		go func() {
			defer group.Done()
			encoded, err := jsonutil.Encode(bridge.answer(ctx, client, message))
			if err != nil || !budget.reserve(len(encoded)) {
				answers[index] = bridgeError(message.ID, -32603, "tool response exceeded its size limit")
			} else {
				answers[index] = json.RawMessage(encoded)
			}
		}()
	}
	group.Wait()
	if ctx.Err() != nil {
		return
	}
	w.Header().Set("Mcp-Session-Id", token[:16])
	if batch {
		bridgeJSON(w, 200, answers)
	} else {
		bridgeJSON(w, 200, answers[0])
	}
}
func (bridge *ToolBridge) stream(ctx context.Context, w http.ResponseWriter, client *bridgeClient, message bridgeMessage) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	controller := http.NewResponseController(w)
	write := func(data []byte) error {
		if err := controller.SetWriteDeadline(time.Now().Add(bridge.writeTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return err
		}
		if _, err := w.Write(data); err != nil {
			return err
		}
		return controller.Flush()
	}
	if err := controller.Flush(); err != nil {
		return
	}
	answer := make(chan any, 1)
	done := make(chan struct{})
	go func() { defer close(done); answer <- bridge.answer(ctx, client, message) }()
	defer func() { cancel(); <-done }()
	ticker := time.NewTicker(bridge.keepAlive)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := write([]byte(": keep-alive\n\n")); err != nil {
				return
			}
		case result := <-answer:
			data, err := jsonutil.Encode(result)
			if err != nil || len(data) > maxBridgeResult {
				data, _ = jsonutil.Encode(bridgeError(message.ID, -32603, "tool response exceeded its size limit"))
			}
			frame := append([]byte("event: message\ndata: "), data...)
			frame = append(frame, '\n', '\n')
			_ = write(frame)
			return
		}
	}
}
func (bridge *ToolBridge) answer(ctx context.Context, client *bridgeClient, message bridgeMessage) (answer any) {
	defer func() {
		if value := recover(); value != nil {
			answer = bridgeError(message.ID, -32603, fmt.Sprint(value))
		}
	}()
	var params map[string]json.RawMessage
	_ = json.Unmarshal(message.Params, &params)
	switch message.Method {
	case "initialize":
		protocol := bridgeProtocol
		var asked string
		if json.Unmarshal(params["protocolVersion"], &asked) == nil {
			protocol = asked
		}
		return bridgeResult(message.ID, map[string]any{"protocolVersion": protocol, "capabilities": map[string]any{"tools": map[string]any{"listChanged": false}}, "serverInfo": map[string]any{"name": BridgeServerName, "version": "1.0.0"}})
	case "ping":
		return bridgeResult(message.ID, map[string]any{})
	case "tools/list":
		tools := []any{}
		for _, tool := range client.tools.Specs {
			schema := map[string]any{"type": "object"}
			for key, value := range tool.Parameters {
				schema[key] = value
			}
			tools = append(tools, map[string]any{"name": tool.Name, "description": tool.Description, "inputSchema": schema})
		}
		return bridgeResult(message.ID, map[string]any{"tools": tools})
	case "tools/call":
		var name string
		if json.Unmarshal(params["name"], &name) != nil {
			return bridgeError(message.ID, -32602, "name")
		}
		var args any
		_ = json.Unmarshal(params["arguments"], &args)
		if args == nil {
			args = map[string]any{}
		}
		output := client.tools.Call(ctx, name, args)
		size := len(output.Content)
		for _, image := range output.Images {
			if len(image.Data) > maxBridgeResult-size {
				return bridgeError(message.ID, -32603, "tool response exceeded its size limit")
			}
			size += len(image.Data)
		}
		if size > maxBridgeResult || len(output.Images) > 1024 {
			return bridgeError(message.ID, -32603, "tool response exceeded its size limit")
		}
		content := []any{map[string]any{"type": "text", "text": output.Content}}
		for _, image := range output.Images {
			content = append(content, map[string]any{"type": "image", "data": image.Data, "mimeType": image.MIME})
		}
		return bridgeResult(message.ID, map[string]any{"content": content, "isError": output.IsError})
	default:
		return bridgeError(message.ID, -32601, "no method "+message.Method)
	}
}
