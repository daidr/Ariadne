package agents

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/daidr/Ariadne/sdk"
)

type fakeDriver struct {
	ctx             DriverContext
	life            context.Context
	mu              sync.Mutex
	inputs          []Input
	efforts         []string
	outputs         []ToolOutput
	closed, discard bool
	startErr        error
	onPrompt        func(*fakeDriver, Input)
}

func (f *fakeDriver) Start(ctx context.Context) error { f.life = ctx; return f.startErr }
func (f *fakeDriver) Prompt(ctx context.Context, input Input, effort string) error {
	f.mu.Lock()
	f.inputs = append(f.inputs, input)
	f.efforts = append(f.efforts, effort)
	f.mu.Unlock()
	if f.onPrompt != nil {
		f.onPrompt(f, input)
	} else {
		f.ctx.Emit(Event{Type: "text", Text: "reply"})
		f.ctx.Emit(Event{Type: "done"})
	}
	return nil
}
func (f *fakeDriver) Close(ctx context.Context, discard bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	f.discard = discard
	return nil
}
func (f *fakeDriver) Checkpoint() (json.RawMessage, error) {
	return json.RawMessage(`{"threadId":"saved-thread"}`), nil
}

func newTestSessions(t *testing.T, kind Kind, options SessionOptions) *Sessions {
	t.Helper()
	if options.DataDir == "" {
		options.DataDir = t.TempDir()
	}
	if options.Quiet == 0 {
		options.Quiet = 5 * time.Millisecond
	}
	m, err := NewSessions(kind, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := m.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return m
}
func parts(m *Sessions, request sdk.Request) []sdk.Part {
	return collectParts(m, context.Background(), request)
}
func collectParts(m *Sessions, ctx context.Context, request sdk.Request) []sdk.Part {
	result := []sdk.Part{}
	for part := range m.Stream(ctx, nil, request) {
		result = append(result, part)
	}
	return result
}
func lastPart(t *testing.T, items []sdk.Part, reason string) {
	t.Helper()
	if len(items) == 0 || items[len(items)-1].Reason != reason {
		t.Fatalf("wanted finish %s, got %#v", reason, items)
	}
}
func user(text string) sdk.Message      { return sdk.Message{Role: "user", Content: text} }
func assistant(text string) sdk.Message { return sdk.Message{Role: "assistant", Content: text} }

func TestSessionReuseAndHistoryChanges(t *testing.T) {
	var drivers []*fakeDriver
	m := newTestSessions(t, Kind{Name: "fake", MutableEffort: true, Fingerprint: NativeFingerprint, Create: func(ctx DriverContext) (Driver, error) {
		f := &fakeDriver{ctx: ctx}
		drivers = append(drivers, f)
		return f, nil
	}}, SessionOptions{})
	request := sdk.Request{Conversation: "conversation", Model: "default", Effort: "low", Messages: []sdk.Message{{Role: "system", Content: "initial"}, user("first")}}
	lastPart(t, parts(m, request), "stop")
	if drivers[0].life.Err() != nil {
		t.Fatal("process lifetime ended with its first stream")
	}
	request.Messages = append(request.Messages, assistant("reply"), user("second"))
	request.Effort = "high"
	lastPart(t, parts(m, request), "stop")
	if len(drivers) != 1 || drivers[0].inputs[1].Text != "second" || drivers[0].efforts[1] != "high" {
		t.Fatalf("session not reused: %#v", drivers)
	}
	request.Messages[0].Content = "changed"
	request.Messages = append(request.Messages, assistant("reply"), user("third"))
	lastPart(t, parts(m, request), "stop")
	if len(drivers) != 1 || !strings.HasPrefix(drivers[0].inputs[2].Text, UpdatedInstructions("changed")) {
		t.Fatal("changed instructions lost continuation")
	}
	request.Messages[1].Content = "edited first message"
	request.Messages = append(request.Messages, assistant("reply"), user("fourth"))
	lastPart(t, parts(m, request), "stop")
	if len(drivers) != 2 || !drivers[0].closed || !drivers[0].discard || !strings.Contains(drivers[1].inputs[0].Text, "edited first message") {
		t.Fatal("edited history did not create a fresh session")
	}
	if _, err := os.Stat(drivers[0].ctx.Directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("closed session directory remains: %v", err)
	}
}

func TestSessionToolContinuationAndArrivingMessage(t *testing.T) {
	var driver *fakeDriver
	output := make(chan ToolOutput, 1)
	m := newTestSessions(t, Kind{Name: "fake", Fingerprint: NativeFingerprint, Create: func(ctx DriverContext) (Driver, error) {
		driver = &fakeDriver{ctx: ctx, onPrompt: func(f *fakeDriver, _ Input) {
			go func() {
				result := f.ctx.Tools.Call(f.life, "read", map[string]any{"path": "中文"})
				output <- result
				f.ctx.Emit(Event{Type: "text", Text: "after tool"})
				f.ctx.Emit(Event{Type: "done"})
			}()
		}}
		return driver, nil
	}}, SessionOptions{})
	request := sdk.Request{Conversation: "tools", Messages: []sdk.Message{user("read")}, Tools: []sdk.Tool{{Name: "read"}}}
	first := parts(m, request)
	lastPart(t, first, "tool-calls")
	var call sdk.ToolCall
	for _, part := range first {
		if part.Type == "tool-call" {
			call = sdk.ToolCall{ID: part.ID, Name: part.Name, Arguments: part.Arguments}
		}
	}
	if call.ID == "" || driver.life.Err() != nil {
		t.Fatal("missing pending call or dead process")
	}
	request.Messages = append(request.Messages, sdk.Message{Role: "assistant", ToolCalls: []sdk.ToolCall{call}}, sdk.Message{Role: "tool", ToolCallID: call.ID, Content: "result", Images: []sdk.Image{{MIME: "image/png", Data: "old"}}}, sdk.Message{Role: "user", Content: "also this", Images: []sdk.Image{{MIME: "image/png", Data: "new"}}})
	lastPart(t, parts(m, request), "stop")
	result := <-output
	if !strings.Contains(result.Content, "<arrived_meanwhile>\nalso this\n</arrived_meanwhile>") || len(result.Images) != 2 || result.IsError {
		t.Fatalf("bad tool delivery: %#v", result)
	}
	if len(driver.inputs) != 1 || driver.ctx.System != PlaceNote {
		t.Fatal("tool result incorrectly started another turn or omitted workspace instructions")
	}
	if driver.ctx.Tools.Call == nil {
		t.Fatal("tools missing")
	}
}

func TestSessionCheckpointResumeAndFailedResume(t *testing.T) {
	home := t.TempDir()
	var drivers []*fakeDriver
	failResume := false
	kind := Kind{Name: "fake", Persistent: true, Fingerprint: NativeFingerprint, Create: func(ctx DriverContext) (Driver, error) {
		f := &fakeDriver{ctx: ctx}
		if failResume && len(ctx.Resume) > 0 {
			f.startErr = errors.New("missing remote thread")
		}
		drivers = append(drivers, f)
		return f, nil
	}}
	m := newTestSessions(t, kind, SessionOptions{DataDir: home})
	request := sdk.Request{Conversation: "saved", Messages: []sdk.Message{user("first")}}
	lastPart(t, parts(m, request), "stop")
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if drivers[0].discard {
		t.Fatal("successful idle session was discarded on shutdown")
	}
	m = newTestSessions(t, kind, SessionOptions{DataDir: home})
	request.Messages = append(request.Messages, assistant("reply"), user("second"))
	lastPart(t, parts(m, request), "stop")
	if len(drivers[1].ctx.Resume) == 0 || drivers[1].inputs[0].Text != "second" {
		t.Fatal("checkpoint not resumed with only new input")
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	failResume = true
	m = newTestSessions(t, kind, SessionOptions{DataDir: home})
	request.Messages = append(request.Messages, assistant("reply"), user("third"))
	lastPart(t, parts(m, request), "stop")
	if len(drivers) != 4 || !drivers[2].closed || len(drivers[3].ctx.Resume) > 0 || !strings.Contains(drivers[3].inputs[0].Text, "<history>") {
		t.Fatal("failed resume did not retry with native history")
	}
}

func TestSessionCancellationAndEarlyConsumerRelease(t *testing.T) {
	var driver *fakeDriver
	started := make(chan struct{}, 1)
	m := newTestSessions(t, Kind{Name: "fake", Create: func(ctx DriverContext) (Driver, error) {
		driver = &fakeDriver{ctx: ctx, onPrompt: func(f *fakeDriver, _ Input) { started <- struct{}{}; f.ctx.Emit(Event{Type: "text", Text: "first"}) }}
		return driver, nil
	}}, SessionOptions{})
	request := sdk.Request{Conversation: "cancel", Messages: []sdk.Message{user("go")}}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan []sdk.Part, 1)
	go func() { finished <- collectParts(m, ctx, request) }()
	<-started
	cancel()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("cancelled stream did not exit")
	}
	if m.Count() != 0 || !driver.closed || driver.life.Err() == nil {
		t.Fatal("cancelled session retained resources")
	}
	for range m.Stream(context.Background(), nil, request) {
		break
	}
	if m.Count() != 0 || !driver.closed {
		t.Fatal("early iterator termination retained session")
	}
	if entries, err := os.ReadDir(filepath.Join(m.options.DataDir, "sessions")); err != nil || len(entries) != 0 {
		t.Fatalf("session directories leaked: %v %v", entries, err)
	}
}

func TestSessionActiveConversationsNeverQueueAtIdleLimit(t *testing.T) {
	started := make(chan string, 20)
	var mu sync.Mutex
	drivers := []*fakeDriver{}
	m := newTestSessions(t, Kind{Name: "fake", Create: func(ctx DriverContext) (Driver, error) {
		f := &fakeDriver{ctx: ctx, onPrompt: func(f *fakeDriver, input Input) { started <- input.Text }}
		mu.Lock()
		drivers = append(drivers, f)
		mu.Unlock()
		return f, nil
	}}, SessionOptions{MaxIdle: 2})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var running sync.WaitGroup
	for i := range 20 {
		running.Add(1)
		go func() {
			defer running.Done()
			collectParts(m, ctx, sdk.Request{Conversation: strings.Repeat("c", i+1), Messages: []sdk.Message{user("start")}})
		}()
	}
	for range 20 {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("active conversation queued behind idle limit")
		}
	}
	if m.Count() != 20 {
		t.Fatalf("active sessions %d", m.Count())
	}
	// Duplicate requests fail promptly and leave the original conversation alive.
	duplicate := parts(m, sdk.Request{Conversation: "c", Messages: []sdk.Message{user("duplicate")}})
	lastPart(t, duplicate, "error")
	if m.Count() != 20 {
		t.Fatal("duplicate request removed another running session")
	}
	cancel()
	running.Wait()
	if m.Count() != 0 {
		t.Fatal("active sessions not released after cancellation")
	}
	mu.Lock()
	defer mu.Unlock()
	for _, f := range drivers {
		if !f.closed {
			t.Fatal("driver leaked")
		}
	}
}

func TestSessionIdleEvictionAndExpiry(t *testing.T) {
	var drivers []*fakeDriver
	m := newTestSessions(t, Kind{Name: "fake", Create: func(ctx DriverContext) (Driver, error) {
		f := &fakeDriver{ctx: ctx}
		drivers = append(drivers, f)
		return f, nil
	}}, SessionOptions{MaxIdle: 2, Idle: time.Millisecond})
	for _, key := range []string{"one", "two", "three"} {
		lastPart(t, parts(m, sdk.Request{Conversation: key, Messages: []sdk.Message{user(key)}}), "stop")
	}
	if m.Count() != 2 || !drivers[0].closed || drivers[0].discard {
		t.Fatal("idle LRU not suspended")
	}
	m.mu.Lock()
	for _, s := range m.pool {
		s.lastUsed = time.Now().Add(-time.Hour)
	}
	m.mu.Unlock()
	m.reap()
	if m.Count() != 0 || !drivers[1].closed || !drivers[2].closed {
		t.Fatal("idle expiry did not release sessions")
	}
}

func TestSessionQueueLimitsAndRelease(t *testing.T) {
	q := newEventQueue()
	for range 10000 {
		if !q.push(Event{Type: "alive"}) {
			t.Fatal("alive coalescing failed")
		}
	}
	if len(q.items) != 1 {
		t.Fatal("alive messages accumulated")
	}
	q.limit = 2
	q.push(Event{Type: "text", Text: "one"})
	q.push(Event{Type: "text", Text: "two"})
	if !errors.Is(q.err, errQueueLimit) || len(q.items) != 0 || q.bytes != 0 {
		t.Fatal("queue overflow retained events")
	}
	q = newEventQueue()
	q.maxSize = 128
	q.push(Event{Type: "text", Text: "a"})
	if !errors.Is(q.err, errQueueLimit) {
		t.Fatal("byte limit missing")
	}
	m := newTestSessions(t, Kind{Name: "fake", Create: func(ctx DriverContext) (Driver, error) {
		return &fakeDriver{ctx: ctx, onPrompt: func(f *fakeDriver, _ Input) {
			for range 600 {
				f.ctx.Emit(Event{Type: "text", Text: "x"})
			}
		}}, nil
	}}, SessionOptions{})
	result := parts(m, sdk.Request{Conversation: "overflow", Messages: []sdk.Message{user("start")}})
	lastPart(t, result, "error")
	if m.Count() != 0 {
		t.Fatal("overflow session retained")
	}
}

func TestSessionUsage(t *testing.T) {
	m := newTestSessions(t, Kind{Name: "fake", Create: func(ctx DriverContext) (Driver, error) {
		return &fakeDriver{ctx: ctx, onPrompt: func(f *fakeDriver, _ Input) {
			f.ctx.Emit(Event{Type: "usage", Usage: Usage{Input: 10, Output: 2, Cached: new(int64(7)), CostUSD: new(float64(.1))}})
			f.ctx.Emit(Event{Type: "usage", Usage: Usage{Input: 5, Output: 3, ContextInput: new(int64(15)), CostUSD: new(float64(.2))}})
			f.ctx.Emit(Event{Type: "done"})
		}}, nil
	}}, SessionOptions{})
	result := parts(m, sdk.Request{Conversation: "usage", Messages: []sdk.Message{user("start")}})
	lastPart(t, result, "stop")
	found := false
	for _, part := range result {
		if part.Type == "usage" {
			found = true
			if *part.Input != 15 || *part.Output != 5 || *part.Cached != 7 || *part.ContextInput != 15 || *part.CostUSD < .299 {
				t.Fatalf("bad usage %#v", part)
			}
		}
	}
	if !found {
		t.Fatal("no usage part")
	}
}

func TestSessionFailedFactoryAndShutdown(t *testing.T) {
	m := newTestSessions(t, Kind{Name: "fake", Create: func(DriverContext) (Driver, error) { return nil, errors.New("failed factory") }}, SessionOptions{})
	result := parts(m, sdk.Request{Conversation: "bad", Messages: []sdk.Message{user("start")}})
	lastPart(t, result, "error")
	if m.Count() != 0 {
		t.Fatal("factory failure leaked session")
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	result = parts(m, sdk.Request{Conversation: "bad", Messages: []sdk.Message{user("start")}})
	if !reflect.DeepEqual(result, []sdk.Part{failure(errors.New("agent provider is closed"), false, false)}) {
		t.Fatal(result)
	}
}

func TestSessionForgetAndPendingExpiry(t *testing.T) {
	for _, expire := range []bool{false, true} {
		t.Run(map[bool]string{false: "delete", true: "expiry"}[expire], func(t *testing.T) {
			resolved := make(chan ToolOutput, 1)
			m := newTestSessions(t, Kind{Name: "fake", Create: func(ctx DriverContext) (Driver, error) {
				return &fakeDriver{ctx: ctx, onPrompt: func(f *fakeDriver, _ Input) { go func() { resolved <- f.ctx.Tools.Call(f.life, "read", nil) }() }}, nil
			}}, SessionOptions{Idle: time.Minute})
			lastPart(t, parts(m, sdk.Request{Conversation: "pending", Messages: []sdk.Message{user("go")}}), "tool-calls")
			if expire {
				m.mu.Lock()
				m.pool["pending"].lastUsed = time.Now().Add(-time.Hour)
				m.mu.Unlock()
				m.reap()
			} else if err := m.Forget(context.Background(), "pending"); err != nil {
				t.Fatal(err)
			}
			select {
			case output := <-resolved:
				if !output.IsError {
					t.Fatal("deleted tool call succeeded")
				}
			case <-time.After(time.Second):
				t.Fatal("pending tool result was retained")
			}
			if m.Count() != 0 {
				t.Fatal("pending session retained")
			}
		})
	}
}

func TestSessionForgetActiveAndCloseActive(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(map[bool]string{false: "forget", true: "close"}[shutdown], func(t *testing.T) {
			started := make(chan struct{})
			m := newTestSessions(t, Kind{Name: "fake", Create: func(ctx DriverContext) (Driver, error) {
				return &fakeDriver{ctx: ctx, onPrompt: func(*fakeDriver, Input) { close(started) }}, nil
			}}, SessionOptions{})
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				parts(m, sdk.Request{Conversation: "active", Messages: []sdk.Message{user("go")}})
			}()
			<-started
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var err error
			if shutdown {
				err = m.Close(ctx)
			} else {
				err = m.Forget(ctx, "active")
			}
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-finished:
			case <-ctx.Done():
				t.Fatal("active stream retained")
			}
			if m.Count() != 0 {
				t.Fatal("active session retained")
			}
		})
	}
}
