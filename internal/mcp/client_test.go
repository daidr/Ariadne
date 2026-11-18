package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMCPHTTPHandshakeToolsNotificationsAndCancellation(t *testing.T) {
	getStarted, getEnded, changed, cancelled := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	pingAnswered := make(chan struct{}, 1)
	slowEnded := make(chan struct{})
	var deleted atomic.Bool
	var initialized atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer private" {
			t.Error("missing authorization")
		}
		if r.Method == "DELETE" {
			deleted.Store(true)
			w.WriteHeader(200)
			return
		}
		if r.Method == "GET" {
			if !initialized.Load() {
				t.Error("notification listener opened before initialization completed")
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.(http.Flusher).Flush()
			close(getStarted)
			defer close(getEnded)
			select {
			case <-changed:
				fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\"}\n\n")
				w.(http.Flusher).Flush()
			case <-r.Context().Done():
				return
			}
			<-r.Context().Done()
			return
		}
		var message struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params map[string]any  `json:"params"`
			Result json.RawMessage `json:"result"`
		}
		if err := json.NewDecoder(r.Body).Decode(&message); err != nil {
			t.Error(err)
			return
		}
		if message.Method != "initialize" {
			if r.Header.Get("Mcp-Session-Id") != "session" || r.Header.Get("Mcp-Protocol-Version") != "2025-06-18" {
				t.Error("missing negotiated headers", r.Header)
			}
		}
		respond := func(result any) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": result})
		}
		switch message.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "session")
			respond(map[string]any{"protocolVersion": "2025-06-18", "serverInfo": map[string]any{"name": "native MCP", "version": "1"}})
		case "notifications/initialized":
			initialized.Store(true)
			w.WriteHeader(202)
		case "notifications/cancelled":
			close(cancelled)
			w.WriteHeader(202)
		case "tools/list":
			if message.Params["cursor"] == "next" {
				respond(map[string]any{"tools": []any{map[string]any{"name": "second", "title": 1}, map[string]any{"name": 12}}})
			} else {
				respond(map[string]any{"tools": []any{map[string]any{"name": "first", "inputSchema": map[string]any{"type": "string", "properties": map[string]any{}}, "description": "Read"}}, "nextCursor": "next"})
			}
		case "tools/call":
			w.Header().Set("Content-Type", "text/event-stream")
			w.(http.Flusher).Flush()
			if message.Params["name"] == "slow" {
				defer close(slowEnded)
				<-r.Context().Done()
				return
			}
			fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"id\":\"server-ping\",\"method\":\"ping\"}\n\n")
			w.(http.Flusher).Flush()
			select {
			case <-pingAnswered:
			case <-r.Context().Done():
				return
			}
			data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "中文"}, map[string]any{"type": "image", "data": "AAAA", "mimeType": "image/png"}, nil, map[string]any{"foo": "ignored"}}, "structuredContent": nil, "isError": false}})
			fmt.Fprintf(w, "data: %s\n\n", data)
			w.(http.Flusher).Flush()
		case "":
			pingAnswered <- struct{}{}
			w.WriteHeader(202)
		default:
			t.Error("unknown request", message.Method)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	transport := NewHTTP(HTTPOptions{URL: server.URL, Headers: map[string]string{"Authorization": "Bearer private"}, Client: server.Client()})
	client, err := NewClient(transport, ClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := client.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	info, err := client.Connect(t.Context())
	if err != nil || info.Name != "native MCP" {
		t.Fatal(info, err)
	}
	tools, err := client.ListTools(t.Context())
	if err != nil || len(tools) != 2 || tools[0].InputSchema["type"] != "object" || tools[1].Title != nil {
		t.Fatal(tools, err)
	}
	changes := make(chan struct{}, 1)
	client.OnToolsChanged(func() { changes <- struct{}{} })
	<-getStarted
	close(changed)
	select {
	case <-changes:
	case <-time.After(time.Second):
		t.Fatal("tool change notification missing")
	}
	result, err := client.CallTool(t.Context(), "read", nil, time.Second)
	if err != nil || len(result.Content) != 2 || string(result.StructuredContent) != "null" {
		t.Fatal(result, err)
	}
	_, err = client.CallTool(t.Context(), "slow", nil, 50*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "within") {
		t.Fatal("missing call deadline", err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("cancellation notification missing")
	}
	select {
	case <-slowEnded:
	case <-time.After(time.Second):
		t.Fatal("cancelled request stream retained")
	}
	if err := client.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !deleted.Load() {
		t.Fatal("session was not deleted")
	}
	select {
	case <-getEnded:
	case <-time.After(time.Second):
		t.Fatal("notification stream retained")
	}
	transport.mu.Lock()
	retained := len(transport.requests) != 0 || transport.bytes != 0
	transport.mu.Unlock()
	if retained {
		t.Fatal("transport retained requests")
	}
}

func TestMCPHTTPRedirectAndSessionExpiry(t *testing.T) {
	for _, redirect := range []bool{false, true} {
		t.Run(fmt.Sprint(redirect), func(t *testing.T) {
			var reached atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/target" {
					reached.Store(true)
				}
				if r.Method == "GET" {
					w.WriteHeader(405)
					return
				}
				if r.Method == "DELETE" {
					return
				}
				var message struct {
					ID     int    `json:"id"`
					Method string `json:"method"`
				}
				json.NewDecoder(r.Body).Decode(&message)
				if message.Method == "initialize" {
					w.Header().Set("Mcp-Session-Id", "session")
					json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": map[string]any{"protocolVersion": "2025-06-18"}})
					return
				}
				if message.Method == "notifications/initialized" {
					w.WriteHeader(202)
					return
				}
				if redirect {
					w.Header().Set("Location", "/target")
					w.WriteHeader(302)
				} else {
					w.WriteHeader(404)
				}
				io.WriteString(w, "private response")
			}))
			defer server.Close()
			transport := NewHTTP(HTTPOptions{URL: server.URL})
			client, err := NewClient(transport, ClientOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close(context.Background())
			if _, err := client.Connect(t.Context()); err != nil {
				t.Fatal(err)
			}
			_, err = client.ListTools(t.Context())
			if err == nil || strings.Contains(err.Error(), "private response") {
				t.Fatal(err)
			}
			if reached.Load() {
				t.Fatal("MCP followed redirect")
			}
			if !redirect && !errors.Is(err, ErrSessionEnded) {
				t.Fatal("session expiry not distinguished", err)
			}
		})
	}
}

type memoryTransport struct {
	take   func([]byte) error
	onSend func([]byte) error
	closed bool
	mu     sync.Mutex
}

func (m *memoryTransport) Start(_ context.Context, take func([]byte) error, _ func(error)) error {
	m.take = take
	return nil
}
func (m *memoryTransport) Send(_ context.Context, data []byte) error { return m.onSend(data) }
func (m *memoryTransport) Close(context.Context) error {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	return nil
}
func TestMCPRejectsUnknownVersionAndReentrantClose(t *testing.T) {
	transport := &memoryTransport{}
	transport.onSend = func(data []byte) error {
		var message struct {
			ID int `json:"id"`
		}
		json.Unmarshal(data, &message)
		return transport.take([]byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"private unsupported version"}}`, message.ID)))
	}
	client, err := NewClient(transport, ClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Connect(t.Context()); err == nil || strings.Contains(err.Error(), "private unsupported") {
		t.Fatal(err)
	}
	transport = &memoryTransport{}
	client, err = NewClient(transport, ClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.OnClose(func(error) {
		if err := client.Close(t.Context()); err != nil {
			t.Error(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}
