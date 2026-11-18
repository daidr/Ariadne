package agents

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/daidr/Ariadne/internal/fsutil"
	"github.com/daidr/Ariadne/internal/jsonutil"
	"github.com/daidr/Ariadne/sdk"
	"github.com/google/uuid"
)

type SessionOptions struct {
	DataDir           string
	Context           context.Context
	Now               func() time.Time
	Quiet, Idle, Reap time.Duration
	MaxIdle           int
}
type Sessions struct {
	mu          sync.Mutex
	kind        Kind
	options     SessionOptions
	root        string
	pool        map[string]*session
	checkpoints *Checkpoints
	ctx         context.Context
	cancel      context.CancelFunc
	closed      bool
	closeOnce   sync.Once
	closeDone   chan struct{}
	reapDone    chan struct{}
	streams     sync.WaitGroup
	next        uint64
}
type session struct {
	key, config, dir, system string
	fps, awaiting            []string
	busy                     bool // protected by Sessions.mu, including startup and stream delivery
	lastUsed                 time.Time
	order                    uint64
	driver                   Driver
	life                     context.Context
	cancel                   context.CancelFunc
	queue                    *eventQueue
	mu                       sync.Mutex // tool callbacks run concurrently with stream delivery
	pending                  map[string]chan ToolOutput
	closed                   bool
	closeOnce                sync.Once
	closeErr                 error
	closeDone                chan struct{}
}

func NewSessions(kind Kind, options SessionOptions) (*Sessions, error) {
	if !providerKind.MatchString(kind.Name) || kind.Create == nil || options.DataDir == "" {
		return nil, errors.New("agent sessions require a provider kind, driver and data directory")
	}
	if options.Context == nil {
		options.Context = context.Background()
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Quiet <= 0 {
		options.Quiet = 150 * time.Millisecond
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
	if kind.Fingerprint == nil {
		kind.Fingerprint = Fingerprint
	}
	root := filepath.Join(options.DataDir, "sessions")
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(options.Context)
	manager := &Sessions{kind: kind, options: options, root: root, pool: map[string]*session{}, ctx: ctx, cancel: cancel, closeDone: make(chan struct{}), reapDone: make(chan struct{})}
	if kind.Persistent {
		var err error
		manager.checkpoints, err = NewCheckpoints(options.DataDir, kind.Name, 256)
		if err != nil {
			cancel()
			return nil, err
		}
	}
	go manager.reapLoop()
	return manager, nil
}
func (m *Sessions) Count() int       { m.mu.Lock(); defer m.mu.Unlock(); return len(m.pool) }
func (s *session) pendingCount() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.pending) }
func endedOutput(text string) ToolOutput {
	return ToolOutput{Content: text, IsError: true, Images: []sdk.Image{}}
}
func (s *session) call(ctx context.Context, name string, args any) ToolOutput {
	data, err := jsonutil.Encode(args)
	if err != nil {
		return endedOutput(err.Error())
	}
	if args == nil {
		data = []byte("{}")
	}
	id := "call_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:20]
	result := make(chan ToolOutput, 1)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return endedOutput("The session has ended.")
	}
	if len(s.pending) >= 128 {
		s.mu.Unlock()
		s.queue.push(Event{Type: "failed", Detail: "agent pending tool calls exceeded their limit"})
		return endedOutput("Too many pending tool calls.")
	}
	s.pending[id] = result
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.pending, id); s.mu.Unlock() }()
	if !s.queue.push(Event{Type: "call", ID: id, Name: name, Arguments: string(data)}) {
		return endedOutput("The session event queue is full.")
	}
	select {
	case output := <-result:
		return output
	case <-ctx.Done():
		return endedOutput("The tool call was cancelled.")
	}
}
func (s *session) resolve(id string, output ToolOutput) {
	s.mu.Lock()
	if result, ok := s.pending[id]; ok {
		delete(s.pending, id)
		result <- output
	}
	s.mu.Unlock()
}
func (s *session) close(root string, suspend bool) error {
	s.closeOnce.Do(func() {
		defer close(s.closeDone)
		s.mu.Lock()
		s.closed = true
		for id, result := range s.pending {
			result <- endedOutput("The session ended before the call ran.")
			delete(s.pending, id)
		}
		s.mu.Unlock()
		s.queue.close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if s.driver != nil {
			s.closeErr = s.driver.Close(ctx, !suspend)
		}
		if s.cancel != nil {
			s.cancel()
		}
		// Process shutdown and delayed Windows directory locks have separate bounds.
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cleanupCancel()
		s.closeErr = errors.Join(s.closeErr, fsutil.RemoveTree(cleanup, root, s.dir))
		s.fps, s.awaiting = nil, nil
	})
	return s.closeErr
}
func (m *Sessions) forget(s *session, suspend bool) {
	if s == nil {
		return
	}
	m.mu.Lock()
	current := m.pool[s.key]
	if s.key != "" && current == s {
		delete(m.pool, s.key)
	}
	if !suspend && s.key != "" && m.checkpoints != nil && (current == nil || current == s) {
		_ = m.checkpoints.Remove(s.key)
	}
	m.mu.Unlock()
	_ = s.close(m.root, suspend)
}

// Forget releases deleted or explicitly stopped conversations, including tools
// waiting between steps. Other conversations continue independently.
func (m *Sessions) Forget(ctx context.Context, key string) error {
	m.mu.Lock()
	s := m.pool[key]
	if s == nil {
		defer m.mu.Unlock()
		if m.checkpoints != nil {
			return m.checkpoints.Remove(key)
		}
		return nil
	}
	delete(m.pool, key)
	busy := s.busy
	if busy {
		s.cancel()
		s.queue.close()
	}
	if m.checkpoints != nil {
		_ = m.checkpoints.Remove(key)
	}
	m.mu.Unlock()
	if !busy {
		_ = s.close(m.root, false)
	}
	select {
	case <-s.closeDone:
		return s.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func olderSession(s, other *session) bool {
	return other == nil || s.lastUsed.Before(other.lastUsed) || s.lastUsed.Equal(other.lastUsed) && s.order < other.order
}
func (m *Sessions) trimIdle() {
	m.mu.Lock()
	var evicted []*session
	for len(m.pool) > m.options.MaxIdle {
		var oldest *session
		for _, s := range m.pool {
			if !s.busy && s.pendingCount() == 0 && olderSession(s, oldest) {
				oldest = s
			}
		}
		if oldest == nil {
			break
		}
		delete(m.pool, oldest.key)
		evicted = append(evicted, oldest)
	}
	m.mu.Unlock()
	for _, s := range evicted {
		_ = s.close(m.root, true)
	}
}

func (m *Sessions) reap() {
	m.mu.Lock()
	var expired []*session
	for key, s := range m.pool {
		if !s.busy && m.options.Now().Sub(s.lastUsed) > m.options.Idle {
			delete(m.pool, key)
			expired = append(expired, s)
		}
	}
	m.mu.Unlock()
	for _, s := range expired {
		_ = s.close(m.root, true)
	}
}
func (m *Sessions) reapLoop() {
	defer close(m.reapDone)
	ticker := time.NewTicker(m.options.Reap)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.reap()
		}
	}
}
func (m *Sessions) Close(ctx context.Context) error {
	m.closeOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		m.cancel()
		m.mu.Unlock()
		go func() {
			defer close(m.closeDone)
			<-m.reapDone
			m.streams.Wait()
			m.mu.Lock()
			all := m.pool
			m.pool = map[string]*session{}
			m.mu.Unlock()
			for _, s := range all {
				m.forget(s, s.pendingCount() == 0)
			}
			if m.checkpoints != nil {
				_ = m.checkpoints.Close()
			}
		}()
	})
	select {
	case <-m.closeDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (m *Sessions) config(settings map[string]any, request sdk.Request) (string, error) {
	var effort any
	if !m.kind.MutableEffort && request.Effort != "" {
		effort = request.Effort
	}
	tools := request.Tools
	if tools == nil {
		tools = []sdk.Tool{}
	}
	if settings == nil {
		settings = map[string]any{}
	}
	data, err := jsonutil.Encode([]any{m.kind.Name, settings, request.Model, effort, tools})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])[:24], nil
}

// claim reserves only this conversation. Active sessions in other conversations
// never consume a semaphore; MaxIdle evicts idle processes, not coordinators.
func (m *Sessions) claim(key, config string) (*session, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.ctx.Err() != nil {
		return nil, false, errors.New("agent provider is closed")
	}
	if s := m.pool[key]; key != "" && s != nil {
		if s.busy {
			return nil, false, errors.New("this conversation already has an active agent request")
		}
		s.busy = true
		return s, false, nil
	}
	s := &session{key: key, config: config, dir: filepath.Join(m.root, uuid.NewString()), busy: true, lastUsed: m.options.Now(), queue: newEventQueue(), pending: map[string]chan ToolOutput{}, closeDone: make(chan struct{})}
	s.life, s.cancel = context.WithCancel(m.ctx)
	m.next++
	s.order = m.next
	if key != "" {
		m.pool[key] = s
	}
	return s, true, nil
}
func (m *Sessions) open(ctx context.Context, s *session, settings map[string]any, request sdk.Request, system string, saved *Checkpoint) error {
	if err := os.Mkdir(s.dir, 0700); err != nil {
		return err
	}
	driverContext := DriverContext{Settings: settings, Model: request.Model, Effort: request.Effort, System: system, Directory: s.dir, Persistent: m.checkpoints != nil && s.key != "", Tools: AgentTools{Specs: request.Tools, Call: s.call}, Emit: func(event Event) { s.queue.push(event) }}
	if saved != nil {
		driverContext.Resume = saved.Driver
		s.fps = append([]string{}, saved.FPS...)
	}
	driver, err := m.kind.Create(driverContext)
	if err != nil {
		return err
	}
	if driver == nil {
		return errors.New("agent driver factory returned no driver")
	}
	s.driver, s.system = driver, system
	stop := context.AfterFunc(ctx, s.cancel)
	err = driver.Start(s.life)
	stop()
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	var oldest *session
	if s.key != "" && len(m.pool) > m.options.MaxIdle {
		for _, other := range m.pool {
			if other != s && !other.busy && other.pendingCount() == 0 && olderSession(other, oldest) {
				oldest = other
			}
		}
		if oldest != nil {
			delete(m.pool, oldest.key)
		}
	}
	m.mu.Unlock()
	if oldest != nil {
		_ = oldest.close(m.root, true)
	}
	return nil
}
func (m *Sessions) continuation(s *session, rest []sdk.Message, fps []string, system, effort string) func(context.Context) error {
	if len(fps) <= len(s.fps) {
		return nil
	}
	for i, value := range s.fps {
		if fps[i] != value {
			return nil
		}
	}
	tail := rest[len(s.fps):]
	results := map[string]sdk.Message{}
	inputs := []sdk.Message{}
	count := 0
	for _, message := range tail {
		switch message.Role {
		case "assistant", "system":
			return nil
		case "tool":
			results[message.ToolCallID] = message
			count++
		case "user":
			inputs = append(inputs, message)
		}
	}
	var prefix *string
	if SystemHash(system) != SystemHash(s.system) {
		text := UpdatedInstructions(system)
		prefix = &text
	}
	if len(s.awaiting) == 0 {
		if count != 0 || len(inputs) == 0 {
			return nil
		}
		s.system = system
		return func(ctx context.Context) error { return s.driver.Prompt(ctx, JoinInput(inputs, prefix), effort) }
	}
	if count != len(s.awaiting) {
		return nil
	}
	for _, id := range s.awaiting {
		if _, ok := results[id]; !ok {
			return nil
		}
	}
	s.system = system
	return func(ctx context.Context) error {
		extra := JoinInput(inputs, prefix)
		for index, id := range s.awaiting {
			result := results[id]
			images := append([]sdk.Image{}, result.Images...)
			content := result.Content
			if (len(inputs) > 0 || prefix != nil) && index == len(s.awaiting)-1 {
				content += "\n\n<arrived_meanwhile>\n" + extra.Text + "\n</arrived_meanwhile>"
				images = append(images, extra.Images...)
			}
			s.resolve(id, ToolOutput{Content: content, IsError: result.IsError, Images: images})
		}
		return nil
	}
}
func failure(err error, retryable, busy bool) sdk.Part {
	return sdk.Part{Type: "finish", Reason: "error", Error: err.Error(), Retryable: retryable, Busy: busy}
}

func (m *Sessions) Stream(ctx context.Context, settings map[string]any, request sdk.Request) iter.Seq[sdk.Part] {
	return func(yield func(sdk.Part) bool) {
		m.mu.Lock()
		if m.closed || m.ctx.Err() != nil {
			m.mu.Unlock()
			yield(failure(errors.New("agent provider is closed"), false, false))
			return
		}
		m.streams.Add(1)
		m.mu.Unlock()
		defer m.streams.Done()
		ctx, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(m.ctx, cancel)
		defer cancel()
		defer stop()
		if ctx.Err() != nil {
			return
		}
		if len(request.Messages) > 100000 {
			yield(failure(errors.New("agent history exceeded its message limit"), false, false))
			return
		}
		system := ""
		rest := request.Messages
		if len(rest) > 0 && rest[0].Role == "system" {
			system = rest[0].Content
			rest = rest[1:]
		}
		if len(request.Tools) > 0 {
			if system != "" {
				system += "\n\n"
			}
			system += PlaceNote
		}
		fps := make([]string, len(rest))
		for i, message := range rest {
			fps[i] = m.kind.Fingerprint(message)
		}
		config, err := m.config(settings, request)
		if err != nil {
			yield(failure(err, false, false))
			return
		}
		s, fresh, err := m.claim(request.Conversation, config)
		if err != nil {
			yield(failure(err, false, false))
			return
		}
		retained := false
		defer func() {
			if !retained || ctx.Err() != nil || s != nil && s.life.Err() != nil {
				m.forget(s, false)
			}
			if s != nil {
				m.mu.Lock()
				s.busy = false
				s.lastUsed = m.options.Now()
				m.next++
				s.order = m.next
				m.mu.Unlock()
			}
			m.trimIdle()
		}()
		var deliver func(context.Context) error
		if !fresh {
			if s.config == config {
				deliver = m.continuation(s, rest, fps, system, request.Effort)
			}
			if deliver == nil {
				m.forget(s, false)
				s, fresh, err = m.claim(request.Conversation, config)
				if err != nil {
					yield(failure(err, false, false))
					return
				}
			}
		}
		if fresh {
			var saved *Checkpoint
			if m.checkpoints != nil && s.key != "" {
				saved = m.checkpoints.Read(s.key)
			}
			compatible := saved != nil && saved.Config == config && len(fps) > len(saved.FPS)
			if compatible {
				for i, value := range saved.FPS {
					if fps[i] != value {
						compatible = false
						break
					}
				}
				for _, message := range rest[len(saved.FPS):] {
					if message.Role != "user" {
						compatible = false
					}
				}
			}
			initial := system
			if compatible {
				initial = saved.System
			} else {
				saved = nil
			}
			err = m.open(ctx, s, settings, request, initial, saved)
			if err != nil && saved != nil {
				m.forget(s, false)
				s, _, err = m.claim(request.Conversation, config)
				if err == nil {
					err = m.open(ctx, s, settings, request, system, nil)
				}
			}
			if err != nil {
				if ctx.Err() == nil {
					yield(failure(err, false, false))
				}
				return
			}
			if compatible && len(s.fps) > 0 {
				deliver = m.continuation(s, rest, fps, system, request.Effort)
			}
			if deliver == nil {
				input := RenderHistory(rest)
				if seeder, ok := s.driver.(HistorySeeder); ok {
					input, err = seeder.Seed(ctx, rest)
				}
				if err != nil {
					yield(failure(err, false, false))
					return
				}
				deliver = func(ctx context.Context) error { return s.driver.Prompt(ctx, input, request.Effort) }
			}
		}
		if s.key != "" && m.checkpoints != nil {
			_ = m.checkpoints.Remove(s.key)
		}
		s.fps = append([]string{}, fps...)
		if configurable, ok := s.driver.(EffortConfigurer); ok {
			err = configurable.Configure(ctx, request.Effort)
		}
		if err == nil {
			err = deliver(ctx)
		}
		if err != nil {
			if ctx.Err() == nil && s.life.Err() == nil {
				yield(failure(err, true, false))
			}
			return
		}
		retained = m.consume(ctx, s, request, yield)
	}
}

func (m *Sessions) consume(ctx context.Context, s *session, request sdk.Request, yield func(sdk.Part) bool) bool {
	calls := []sdk.ToolCall{}
	var text strings.Builder
	usage := sdk.Part{Type: "usage", Input: new(int64), Output: new(int64)}
	counted := false
	add := func(dst **int64, value *int64) {
		if value != nil {
			if *dst == nil {
				*dst = new(int64)
			}
			**dst += *value
		}
	}
	usagePart := func() bool {
		if !counted {
			return true
		}
		if usage.Cached != nil && *usage.Cached == 0 {
			usage.Cached = nil
		}
		if usage.CacheWrite != nil && *usage.CacheWrite == 0 {
			usage.CacheWrite = nil
		}
		if usage.Reasoning != nil && *usage.Reasoning == 0 {
			usage.Reasoning = nil
		}
		return yield(usage)
	}
	for {
		wait := time.Duration(0)
		if len(calls) > 0 {
			wait = m.options.Quiet
		}
		event, timeout, err := s.queue.next(ctx, wait)
		if err != nil {
			if ctx.Err() == nil && s.life.Err() == nil {
				yield(failure(err, true, false))
			}
			return false
		}
		if timeout {
			for _, call := range calls {
				if !yield(sdk.Part{Type: "tool-call", ID: call.ID, Name: call.Name, Arguments: call.Arguments}) {
					return false
				}
			}
			if !usagePart() {
				return false
			}
			s.awaiting = make([]string, len(calls))
			for i, call := range calls {
				s.awaiting[i] = call.ID
			}
			s.fps = append(s.fps, m.kind.Fingerprint(sdk.Message{Role: "assistant", Content: text.String(), ToolCalls: calls}))
			return yield(sdk.Part{Type: "finish", Reason: "tool-calls"}) && s.key != ""
		}
		switch event.Type {
		case "call":
			if len(calls) >= 128 {
				yield(failure(errors.New("agent tool calls exceeded their limit"), false, false))
				return false
			}
			calls = append(calls, sdk.ToolCall{ID: event.ID, Name: event.Name, Arguments: event.Arguments})
			if !yield(sdk.Part{Type: "alive"}) {
				return false
			}
		case "text":
			if len(event.Text) > 64<<20-text.Len() {
				yield(failure(errors.New("agent response exceeded its size limit"), false, false))
				return false
			}
			text.WriteString(event.Text)
			if event.Text != "" && !yield(sdk.Part{Type: "text", Text: event.Text}) {
				return false
			}
		case "reasoning":
			if event.Text != "" && !yield(sdk.Part{Type: "reasoning", Text: event.Text, StartedAt: m.options.Now().UnixMilli()}) {
				return false
			}
		case "alive":
			if !yield(sdk.Part{Type: "alive"}) {
				return false
			}
		case "usage":
			counted = true
			u := event.Usage
			*usage.Input += u.Input
			*usage.Output += u.Output
			if u.ContextInput != nil {
				value := *u.ContextInput
				usage.ContextInput = &value
			}
			add(&usage.Cached, u.Cached)
			add(&usage.CacheWrite, u.CacheWrite)
			add(&usage.Reasoning, u.Reasoning)
			if u.CostUSD != nil {
				if usage.CostUSD == nil {
					usage.CostUSD = new(float64)
				}
				*usage.CostUSD += *u.CostUSD
			}
		case "failed":
			yield(failure(errors.New(event.Detail), event.Retryable, event.Busy))
			return false
		case "done":
			for _, call := range calls {
				s.resolve(call.ID, endedOutput("The turn ended before the call ran."))
			}
			if !usagePart() {
				return false
			}
			s.awaiting = nil
			s.fps = append(s.fps, m.kind.Fingerprint(sdk.Message{Role: "assistant", Content: text.String()}))
			if cp, ok := s.driver.(Checkpointer); ok && m.checkpoints != nil && s.key != "" {
				if data, err := cp.Checkpoint(); err == nil && driverPresent(data) {
					m.mu.Lock()
					if m.pool[s.key] == s && s.life.Err() == nil {
						_ = m.checkpoints.Write(s.key, Checkpoint{Config: s.config, System: s.system, FPS: s.fps, Driver: data})
					}
					m.mu.Unlock()
				}
			}
			return yield(sdk.Part{Type: "finish", Reason: "stop"}) && s.key != ""
		default:
			yield(failure(fmt.Errorf("unknown agent event %q", event.Type), false, false))
			return false
		}
	}
}
