package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/daidr/Ariadne/internal/cache"
	"github.com/daidr/Ariadne/internal/jsonutil"
	"github.com/daidr/Ariadne/internal/process"
	secrettext "github.com/daidr/Ariadne/internal/secrets"
	"github.com/daidr/Ariadne/internal/textutil"
)

type ServerConfig struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Transport string            `json:"transport"`
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Directory string            `json:"cwd,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	URL       string            `json:"url,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Tools     map[string]string `json:"tools"`
}
type ServerStatus struct {
	ID    string      `json:"id"`
	State string      `json:"state"`
	Error *string     `json:"error"`
	Log   *string     `json:"log"`
	Info  *ServerInfo `json:"info"`
	Tools *[]ToolInfo `json:"tools"`
}
type HubOptions struct {
	Servers                                      func() ([]ServerConfig, error)
	Env                                          func() map[string]string
	Client                                       *http.Client
	Context                                      context.Context
	Now                                          func() time.Time
	TurnWait, SetupTimeout, Idle, Reap, CacheTTL time.Duration
	MaxIdle                                      int
	MaxCacheWeight                               int64
}
type lastTools struct {
	Key   string
	Tools []ToolInfo
	Info  *ServerInfo
}
type failure struct {
	Key   string
	At    time.Time
	Count int
	Error string
	Log   *string
}
type connection struct {
	config    ServerConfig
	key       string
	client    *Client
	transport Transport
	ready     chan struct{}
	err       error
	tools     []ToolInfo
	changes   chan struct{}
	ctx       context.Context
	cancel    context.CancelFunc
	lastUsed  time.Time
	order     uint64
	closeOnce sync.Once
	closing   bool // protected by Hub.mu
	closed    chan struct{}
	previous  *connection
}
type Hub struct {
	mu          sync.Mutex
	options     HubOptions
	ctx         context.Context
	cancel      context.CancelFunc
	connections map[string]*connection
	owned       map[*connection]struct{}
	last        *cache.Cache[string, lastTools]
	failures    *cache.Cache[string, failure]
	workers     sync.WaitGroup
	closed      bool
	next        uint64
	closeOnce   sync.Once
	closeDone   chan struct{}
}

func RetryAfter(count int) time.Duration {
	if count <= 1 {
		return time.Minute
	}
	if count >= 5 {
		return 15 * time.Minute
	}
	return min(15*time.Minute, time.Minute*time.Duration(1<<uint(count-1)))
}
func SecretsOf(config ServerConfig) []string {
	secrets := []string{}
	for _, values := range []map[string]string{config.Env, config.Headers} {
		for _, value := range values {
			if len(utf16.Encode([]rune(value))) >= 4 {
				secrets = append(secrets, value)
			}
		}
	}
	return secrets
}
func Redact(text string, secrets []string) string {
	return secrettext.Redact(text, secrets)
}
func wireKey(config ServerConfig) string {
	config.Name = ""
	config.Tools = nil
	data, _ := jsonutil.Encode(config)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
func NewHub(options HubOptions) (*Hub, error) {
	if options.Servers == nil {
		return nil, errors.New("MCP server configuration getter is required")
	}
	if options.Context == nil {
		options.Context = context.Background()
	}
	if options.Env == nil {
		options.Env = func() map[string]string { return process.ThreadEnv(process.Environ()) }
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.TurnWait <= 0 {
		options.TurnWait = 5 * time.Second
	}
	if options.SetupTimeout <= 0 {
		options.SetupTimeout = time.Minute
	}
	if options.Idle <= 0 {
		options.Idle = 20 * time.Minute
	}
	if options.Reap <= 0 {
		options.Reap = time.Minute
	}
	if options.MaxIdle <= 0 {
		options.MaxIdle = 16
	}
	if options.CacheTTL <= 0 {
		options.CacheTTL = 5 * time.Minute
	}
	if options.MaxCacheWeight <= 0 {
		options.MaxCacheWeight = 16 << 20
	}
	last, err := cache.New[string, lastTools](cache.Options{MaxEntries: 32, MaxWeight: options.MaxCacheWeight, TTL: options.CacheTTL, Now: options.Now})
	if err != nil {
		return nil, err
	}
	failures, err := cache.New[string, failure](cache.Options{MaxEntries: 128, MaxWeight: 1 << 20, TTL: 24 * time.Hour, Now: options.Now})
	if err != nil {
		last.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(options.Context)
	hub := &Hub{options: options, ctx: ctx, cancel: cancel, connections: map[string]*connection{}, owned: map[*connection]struct{}{}, last: last, failures: failures, closeDone: make(chan struct{})}
	hub.workers.Add(1)
	go func() {
		defer hub.workers.Done()
		ticker := time.NewTicker(options.Reap)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				hub.reap()
			}
		}
	}()
	return hub, nil
}
func cloneTools(tools []ToolInfo) []ToolInfo {
	if tools == nil {
		return nil
	}
	data, _ := json.Marshal(tools)
	var copy []ToolInfo
	_ = json.Unmarshal(data, &copy)
	return copy
}
func (hub *Hub) Config(id string) (ServerConfig, bool, error) {
	servers, err := hub.options.Servers()
	if err != nil {
		return ServerConfig{}, false, err
	}
	for _, server := range servers {
		if server.ID == id {
			return server, true, nil
		}
	}
	return ServerConfig{}, false, nil
}
func (hub *Hub) Status() ([]ServerStatus, error) {
	servers, err := hub.options.Servers()
	if err != nil {
		return nil, err
	}
	result := []ServerStatus{}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	for _, config := range servers {
		key := wireKey(config)
		current := hub.connections[config.ID]
		if current != nil && current.key != key {
			current = nil
		}
		failed, hasFailure := hub.failures.Get(config.ID)
		hasFailure = hasFailure && failed.Key == key
		status := ServerStatus{ID: config.ID, State: "idle"}
		if current != nil {
			status.State = "connecting"
			if current.tools != nil {
				status.State = "ready"
				tools := cloneTools(current.tools)
				status.Tools = &tools
				status.Info = current.client.Info()
			}
		} else if hasFailure {
			status.State = "failed"
			status.Error = new(failed.Error)
			if failed.Log != nil {
				status.Log = new(*failed.Log)
			}
		}
		if status.Tools == nil {
			if last, exists := hub.last.Get(config.ID); exists && last.Key == key {
				tools := cloneTools(last.Tools)
				status.Tools = &tools
				if last.Info != nil {
					info := *last.Info
					status.Info = &info
				}
			}
		}
		result = append(result, status)
	}
	return result, nil
}
func (hub *Hub) transportFor(config ServerConfig) (Transport, error) {
	if config.Transport == "http" {
		return NewHTTP(HTTPOptions{URL: config.URL, Headers: config.Headers, Client: hub.options.Client}), nil
	}
	if config.Transport != "stdio" {
		return nil, errors.New("unsupported MCP transport")
	}
	directory := config.Directory
	if directory == "" {
		directory, _ = os.UserHomeDir()
	}
	env := map[string]string{}
	for key, value := range hub.options.Env() {
		env[key] = value
	}
	for key, value := range config.Env {
		env[key] = value
	}
	return NewStdio(StdioOptions{Argv: append([]string{config.Command}, config.Args...), Directory: directory, Env: env}), nil
}
func (hub *Hub) closeConnection(conn *connection) {
	conn.closeOnce.Do(func() {
		defer close(conn.closed)
		conn.cancel()
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		_ = conn.client.Close(ctx)
		hub.mu.Lock()
		delete(hub.owned, conn)
		hub.mu.Unlock()
	})
}

func (hub *Hub) scheduleClose(conn *connection) {
	conn.cancel()
	hub.mu.Lock()
	if hub.closed || conn.closing {
		hub.mu.Unlock()
		return
	}
	conn.closing = true
	hub.workers.Add(1)
	hub.mu.Unlock()
	go func() { defer hub.workers.Done(); hub.closeConnection(conn) }()
}
func (hub *Hub) fail(conn *connection, err error) {
	hub.mu.Lock()
	if hub.connections[conn.config.ID] != conn {
		hub.mu.Unlock()
		return
	}
	delete(hub.connections, conn.config.ID)
	if !hub.closed && !errors.Is(err, ErrSessionEnded) {
		before, _ := hub.failures.Get(conn.config.ID)
		if before.Key != conn.key {
			before = failure{}
		}
		secrets := SecretsOf(conn.config)
		detail := Redact(err.Error(), secrets)
		if len(detail) > 16000 {
			detail = detail[:16000]
		}
		var log *string
		if transport, ok := conn.transport.(interface{ StderrTail() string }); ok {
			tail := textutil.Trim(Redact(transport.StderrTail(), secrets))
			if tail != "" {
				log = &tail
			}
		}
		failed := failure{Key: conn.key, At: hub.options.Now(), Count: before.Count + 1, Error: detail, Log: log}
		weight := len(detail) + 128
		if log != nil {
			weight += len(*log)
		}
		hub.failures.Put(conn.config.ID, failed, int64(weight))
	}
	hub.mu.Unlock()
}
func (hub *Hub) rememberLocked(conn *connection, tools []ToolInfo) {
	conn.tools = tools
	known := lastTools{Key: conn.key, Tools: tools, Info: conn.client.Info()}
	encoded, _ := json.Marshal(known)
	hub.last.Put(conn.config.ID, known, int64(len(encoded)))
	hub.failures.Remove(conn.config.ID)
}
func (hub *Hub) start(config ServerConfig) (*connection, error) {
	key := wireKey(config)
	hub.mu.Lock()
	if hub.closed || hub.ctx.Err() != nil {
		hub.mu.Unlock()
		return nil, errors.New("MCP hub is closed")
	}
	hub.next++
	if current := hub.connections[config.ID]; current != nil && current.key == key {
		current.lastUsed = hub.options.Now()
		current.order = hub.next
		hub.mu.Unlock()
		return current, nil
	}
	transport, err := hub.transportFor(config)
	if err != nil {
		hub.mu.Unlock()
		return nil, err
	}
	ctx, cancel := context.WithCancel(hub.ctx)
	client, err := NewClient(transport, ClientOptions{Context: ctx, SetupTimeout: hub.options.SetupTimeout})
	if err != nil {
		cancel()
		hub.mu.Unlock()
		return nil, err
	}
	conn := &connection{config: config, key: key, client: client, transport: transport, ctx: ctx, cancel: cancel, ready: make(chan struct{}), changes: make(chan struct{}, 1), lastUsed: hub.options.Now(), order: hub.next, closed: make(chan struct{})}
	previous := hub.connections[config.ID]
	conn.previous = previous
	hub.connections[config.ID] = conn
	hub.owned[conn] = struct{}{}
	client.OnClose(func(err error) {
		hub.mu.Lock()
		ready := conn.tools != nil
		hub.mu.Unlock()
		if ready {
			hub.fail(conn, err)
			hub.scheduleClose(conn)
		}
	})
	client.OnToolsChanged(func() {
		select {
		case conn.changes <- struct{}{}:
		default:
		}
	})
	hub.workers.Add(2)
	hub.mu.Unlock()
	if previous != nil {
		hub.scheduleClose(previous)
	}
	go hub.setup(conn)
	go hub.refresh(conn)
	hub.reap()
	return conn, nil
}
func (hub *Hub) setup(conn *connection) {
	defer hub.workers.Done()
	ctx, cancel := context.WithTimeout(conn.ctx, hub.options.SetupTimeout)
	defer cancel()
	var err error
	if conn.previous != nil {
		select {
		case <-conn.previous.closed:
		case <-ctx.Done():
			err = ctx.Err()
		}
		conn.previous = nil
	}
	if err == nil {
		_, err = conn.client.Connect(ctx)
	}
	var tools []ToolInfo
	if err == nil {
		tools, err = conn.client.ListTools(ctx)
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		err = &ConnectionError{Reason: fmt.Sprintf("not ready within %d seconds", int(math.Floor(hub.options.SetupTimeout.Seconds()+.5)))}
	}
	hub.mu.Lock()
	if err == nil && conn.client.Closed() {
		err = &ConnectionError{Reason: "the server stopped"}
	}
	conn.err = err
	if err == nil && hub.connections[conn.config.ID] == conn && !hub.closed {
		hub.rememberLocked(conn, tools)
	}
	hub.mu.Unlock()
	if err != nil {
		hub.fail(conn, err)
	}
	close(conn.ready)
	if err != nil {
		hub.closeConnection(conn)
	}
}
func (hub *Hub) refresh(conn *connection) {
	defer hub.workers.Done()
	select {
	case <-conn.ctx.Done():
		return
	case <-conn.ready:
	}
	if conn.err != nil {
		return
	}
	for {
		select {
		case <-conn.ctx.Done():
			return
		case <-conn.changes:
			ctx, cancel := context.WithTimeout(conn.ctx, hub.options.SetupTimeout)
			tools, err := conn.client.ListTools(ctx)
			cancel()
			if err != nil {
				hub.fail(conn, err)
				hub.closeConnection(conn)
				return
			}
			hub.mu.Lock()
			if hub.connections[conn.config.ID] == conn && !hub.closed {
				hub.rememberLocked(conn, tools)
			}
			hub.mu.Unlock()
		}
	}
}
func (hub *Hub) Tools(ctx context.Context, config ServerConfig) ([]ToolInfo, error) {
	hub.mu.Lock()
	current := hub.connections[config.ID]
	same := current != nil && current.key == wireKey(config)
	if !same {
		if failed, exists := hub.failures.Get(config.ID); exists && failed.Key == wireKey(config) && hub.options.Now().Sub(failed.At) < RetryAfter(failed.Count) {
			hub.mu.Unlock()
			return nil, nil
		}
	}
	hub.mu.Unlock()
	conn, err := hub.start(config)
	if err != nil {
		return nil, err
	}
	timer := time.NewTimer(hub.options.TurnWait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, nil
	case <-conn.ready:
		if conn.err != nil {
			return nil, nil
		}
		hub.mu.Lock()
		tools := cloneTools(conn.tools)
		hub.mu.Unlock()
		return tools, nil
	}
}
func (hub *Hub) Client(ctx context.Context, config ServerConfig) (*Client, error) {
	conn, err := hub.start(config)
	if err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-conn.ready:
		if conn.err != nil {
			return nil, conn.err
		}
		if conn.client.Closed() {
			return nil, &ConnectionError{Reason: "the server stopped"}
		}
		return conn.client, nil
	}
}
func (hub *Hub) CallTool(ctx context.Context, config ServerConfig, name string, args map[string]any, timeout time.Duration) (ToolResult, error) {
	for attempt := 0; attempt < 2; attempt++ {
		client, err := hub.Client(ctx, config)
		if err != nil {
			return ToolResult{}, err
		}
		result, err := client.CallTool(ctx, name, args, timeout)
		if !errors.Is(err, ErrSessionEnded) || attempt == 1 {
			return result, err
		}
	}
	return ToolResult{}, ErrSessionEnded
}
func (hub *Hub) Check(ctx context.Context, id string) (*ServerStatus, error) {
	config, exists, err := hub.Config(id)
	if err != nil || !exists {
		return nil, err
	}
	hub.mu.Lock()
	current := hub.connections[id]
	delete(hub.connections, id)
	hub.failures.Remove(id)
	hub.mu.Unlock()
	if current != nil {
		hub.scheduleClose(current)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-current.closed:
		}
	}
	conn, err := hub.start(config)
	if err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-conn.ready:
	}
	status, err := hub.Status()
	if err != nil {
		return nil, err
	}
	for _, entry := range status {
		if entry.ID == id {
			return &entry, nil
		}
	}
	return nil, nil
}
func (hub *Hub) Reconfigure(ctx context.Context) error {
	servers, err := hub.options.Servers()
	if err != nil {
		return err
	}
	known := map[string]string{}
	for _, config := range servers {
		known[config.ID] = wireKey(config)
	}
	hub.mu.Lock()
	stale := []*connection{}
	for conn := range hub.owned {
		id := conn.config.ID
		if known[id] == conn.key {
			continue
		}
		if hub.connections[id] == conn {
			delete(hub.connections, id)
		}
		stale = append(stale, conn)
	}
	for _, id := range hub.failures.Keys() {
		if key, exists := known[id]; !exists {
			hub.failures.Remove(id)
		} else if failure, _ := hub.failures.Get(id); failure.Key != key {
			hub.failures.Remove(id)
		}
	}
	for _, id := range hub.last.Keys() {
		if key, exists := known[id]; !exists {
			hub.last.Remove(id)
		} else if last, _ := hub.last.Get(id); last.Key != key {
			hub.last.Remove(id)
		}
	}
	hub.mu.Unlock()
	for _, conn := range stale {
		hub.scheduleClose(conn)
	}
	for _, conn := range stale {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-conn.closed:
		}
	}
	return nil
}
func (hub *Hub) reap() {
	hub.mu.Lock()
	stale := []*connection{}
	for id, conn := range hub.connections {
		if conn.tools != nil && conn.client.Pending() == 0 && hub.options.Now().Sub(conn.lastUsed) > hub.options.Idle {
			delete(hub.connections, id)
			stale = append(stale, conn)
		}
	}
	for len(hub.connections) > hub.options.MaxIdle {
		var oldest *connection
		for _, conn := range hub.connections {
			if conn.tools != nil && conn.client.Pending() == 0 && (oldest == nil || conn.lastUsed.Before(oldest.lastUsed) || conn.lastUsed.Equal(oldest.lastUsed) && conn.order < oldest.order) {
				oldest = conn
			}
		}
		if oldest == nil {
			break
		}
		delete(hub.connections, oldest.config.ID)
		stale = append(stale, oldest)
	}
	hub.mu.Unlock()
	for _, conn := range stale {
		hub.scheduleClose(conn)
	}
}
func (hub *Hub) Close(ctx context.Context) error {
	hub.closeOnce.Do(func() {
		hub.mu.Lock()
		hub.closed = true
		hub.cancel()
		all := make([]*connection, 0, len(hub.owned))
		for conn := range hub.owned {
			all = append(all, conn)
		}
		hub.connections = map[string]*connection{}
		hub.mu.Unlock()
		hub.last.Close()
		hub.failures.Close()
		go func() {
			defer close(hub.closeDone)
			jobs := make(chan *connection)
			var group sync.WaitGroup
			for range min(32, len(all)) {
				group.Add(1)
				go func() {
					defer group.Done()
					for conn := range jobs {
						hub.closeConnection(conn)
					}
				}()
			}
			for _, conn := range all {
				jobs <- conn
			}
			close(jobs)
			group.Wait()
			hub.workers.Wait()
		}()
	})
	select {
	case <-hub.closeDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
