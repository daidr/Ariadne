package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type serverConfigSource struct {
	mu      sync.RWMutex
	servers []ServerConfig
}

func (source *serverConfigSource) get() ([]ServerConfig, error) {
	source.mu.RLock()
	defer source.mu.RUnlock()
	data, _ := json.Marshal(source.servers)
	var copy []ServerConfig
	json.Unmarshal(data, &copy)
	return copy, nil
}
func (source *serverConfigSource) set(servers ...ServerConfig) {
	source.mu.Lock()
	source.servers = servers
	source.mu.Unlock()
}
func newTestHub(t *testing.T, options HubOptions) *Hub {
	t.Helper()
	hub, err := NewHub(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := hub.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return hub
}
func waitMCP(t *testing.T, condition func() bool) {
	t.Helper()
	until := time.Now().Add(2 * time.Second)
	for !condition() {
		if time.Now().After(until) {
			t.Fatal("MCP condition timed out")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestHubConnectionReuseRedactionMetadataAndReconfigure(t *testing.T) {
	var initialize, deleted atomic.Int32
	var refused atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.WriteHeader(405)
			return
		}
		if r.Method == "DELETE" {
			deleted.Add(1)
			return
		}
		var message struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&message)
		response := func(result any) {
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": result})
		}
		switch message.Method {
		case "initialize":
			initialize.Add(1)
			w.Header().Set("Mcp-Session-Id", "session")
			response(map[string]any{"protocolVersion": "2025-06-18", "serverInfo": map[string]any{"name": "server", "version": "1"}})
		case "notifications/initialized":
			w.WriteHeader(202)
		case "tools/list":
			if refused.Load() {
				json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": message.ID, "error": map[string]any{"code": -32000, "message": "denied Bearer private-credential"}})
			} else {
				response(map[string]any{"tools": []any{map[string]any{"name": "read", "inputSchema": map[string]any{"type": "object"}, "annotations": map[string]any{}}}})
			}
		case "tools/call":
			response(map[string]any{"content": []any{map[string]any{"type": "text", "text": "result"}}})
		default:
			w.WriteHeader(202)
		}
	}))
	defer server.Close()
	config := ServerConfig{ID: "server", Transport: "http", URL: server.URL, Headers: map[string]string{"Authorization": "Bearer private-credential"}, Tools: map[string]string{}}
	source := &serverConfigSource{}
	source.set(config)
	hub := newTestHub(t, HubOptions{Servers: source.get})
	status, err := hub.Status()
	if err != nil || status[0].State != "idle" {
		t.Fatal(status, err)
	}
	checked, err := hub.Check(t.Context(), "server")
	if err != nil || checked.State != "ready" || checked.Tools == nil || (*checked.Tools)[0].Annotations == nil {
		t.Fatal(checked, err)
	}
	client, err := hub.Client(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.CallTool(t.Context(), "read", nil, time.Second); err != nil {
		t.Fatal(err)
	}
	if initialize.Load() != 1 {
		t.Fatal("connection was not reused")
	}
	config.Name = "renamed"
	config.Tools = map[string]string{"read": "off"}
	source.set(config)
	if err := hub.Reconfigure(t.Context()); err != nil {
		t.Fatal(err)
	}
	status, _ = hub.Status()
	if status[0].State != "ready" || initialize.Load() != 1 {
		t.Fatal("tool marks restarted the server", status)
	}
	refused.Store(true)
	checked, err = hub.Check(t.Context(), "server")
	if err != nil || checked.State != "failed" || checked.Error == nil || strings.Contains(*checked.Error, "private-credential") || !strings.Contains(*checked.Error, "[secret]") || checked.Tools == nil {
		t.Fatal(checked, err)
	}
	refused.Store(false)
	checked, err = hub.Check(t.Context(), "server")
	if err != nil || checked.State != "ready" {
		t.Fatal(checked, err)
	}
	(*checked.Tools)[0].InputSchema["changed"] = true
	status, _ = hub.Status()
	if (*status[0].Tools)[0].InputSchema["changed"] != nil {
		t.Fatal("caller mutated cached tools")
	}
	source.set()
	if err := hub.Reconfigure(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n, _ := hub.last.Stats(); n != 0 {
		t.Fatal("removed server retained tools")
	}
	if n, _ := hub.failures.Stats(); n != 0 {
		t.Fatal("removed server retained failure")
	}
	hub.mu.Lock()
	owned := len(hub.owned)
	hub.mu.Unlock()
	if owned != 0 || deleted.Load() < 3 {
		t.Fatal("removed connections retained", owned, deleted.Load())
	}
}

func TestHubTurnWaitAndCancellationDoNotBlockOnStartup(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.WriteHeader(405)
			return
		}
		var message struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&message)
		switch message.Method {
		case "initialize":
			close(started)
			select {
			case <-release:
				json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": map[string]any{"protocolVersion": "2025-06-18"}})
			case <-r.Context().Done():
			}
		case "tools/list":
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": map[string]any{"tools": []any{}}})
		default:
			w.WriteHeader(202)
		}
	}))
	defer server.Close()
	config := ServerConfig{ID: "slow", Transport: "http", URL: server.URL}
	source := &serverConfigSource{}
	source.set(config)
	hub := newTestHub(t, HubOptions{Servers: source.get, TurnWait: 25 * time.Millisecond, SetupTimeout: time.Second})
	ctx, cancel := context.WithCancel(t.Context())
	first := make(chan error, 1)
	go func() { _, err := hub.Tools(ctx, config); first <- err }()
	<-started
	cancel()
	if err := <-first; err == nil {
		t.Fatal("cancelled waiter succeeded")
	}
	begin := time.Now()
	tools, err := hub.Tools(t.Context(), config)
	if err != nil || tools != nil || time.Since(begin) > 300*time.Millisecond {
		t.Fatal("turn waited for server startup", tools, err, time.Since(begin))
	}
	close(release)
	if _, err := hub.Client(t.Context(), config); err != nil {
		t.Fatal("one cancelled waiter cancelled startup", err)
	}
}

func TestHubToolChangeBurstCoalescesAndCachesExpire(t *testing.T) {
	var lists atomic.Int32
	notify, start := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.Header().Set("Content-Type", "text/event-stream")
			w.(http.Flusher).Flush()
			close(start)
			select {
			case <-notify:
				for range 1000 {
					io.WriteString(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\"}\n\n")
				}
				w.(http.Flusher).Flush()
			case <-r.Context().Done():
				return
			}
			<-r.Context().Done()
			return
		}
		var message struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&message)
		response := func(result any) {
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": result})
		}
		switch message.Method {
		case "initialize":
			response(map[string]any{"protocolVersion": "2025-06-18"})
		case "tools/list":
			lists.Add(1)
			time.Sleep(10 * time.Millisecond)
			response(map[string]any{"tools": []any{map[string]any{"name": "read"}}})
		default:
			w.WriteHeader(202)
		}
	}))
	defer server.Close()
	config := ServerConfig{ID: "server", Transport: "http", URL: server.URL}
	source := &serverConfigSource{}
	source.set(config)
	hub := newTestHub(t, HubOptions{Servers: source.get, CacheTTL: 30 * time.Millisecond})
	if _, err := hub.Client(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	<-start
	close(notify)
	waitMCP(t, func() bool { return lists.Load() >= 2 })
	time.Sleep(60 * time.Millisecond)
	if lists.Load() > 4 {
		t.Fatal("tool notifications started unbounded refreshes", lists.Load())
	}
	waitMCP(t, func() bool { count, _ := hub.last.Stats(); return count == 0 })
	if err := hub.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	hub.mu.Lock()
	retained := len(hub.owned) != 0 || len(hub.connections) != 0
	hub.mu.Unlock()
	if retained {
		t.Fatal("closed hub retained connections")
	}
}
func TestHubRetryAndRedaction(t *testing.T) {
	for count, want := range map[int]time.Duration{0: time.Minute, 1: time.Minute, 2: 2 * time.Minute, 3: 4 * time.Minute, 4: 8 * time.Minute, 5: 15 * time.Minute, 1000: 15 * time.Minute} {
		if got := RetryAfter(count); got != want {
			t.Fatal(count, got, want)
		}
	}
	if result := Redact("abc-secret and abc", []string{"abc", "abc-secret"}); result != "[secret] and [secret]" {
		t.Fatal(result)
	}
	config := ServerConfig{ID: "id", Env: map[string]string{"TOKEN": "secret"}, Tools: map[string]string{"read": "ask"}}
	before := wireKey(config)
	config.Name = "new"
	config.Tools = map[string]string{"read": "off"}
	if wireKey(config) != before {
		t.Fatal("non-wire fields changed connection key")
	}
	config.Env["TOKEN"] = "changed"
	if wireKey(config) == before || strings.Contains(wireKey(config), "changed") {
		t.Fatal("wire credentials not hashed")
	}
}
