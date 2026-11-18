// Package rpc provides bounded, cancellable bidirectional JSON-RPC for native
// agent programs and plugin transports. It does not own their process lifecycle.
package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
)

const MaxMessageBytes = 32 * 1024 * 1024

var ErrUnhandled = errors.New("unhandled method")
var ErrPendingLimit = errors.New("too many pending RPC requests")

type unsupported struct{ method string }

func (err unsupported) Error() string { return err.method + " is not supported" }
func (err unsupported) Unwrap() error { return ErrUnhandled }
func Unsupported(method string) error { return unsupported{method: method} }

type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (err *Error) Error() string { return err.Message }

type Options struct {
	Context          context.Context
	Strict           bool
	Send             func(context.Context, []byte) error
	Request          func(context.Context, string, json.RawMessage) (any, error)
	Notification     func(string, json.RawMessage)
	MaxPending       int
	MaxIncoming      int
	MaxIncomingBytes int64
	OnCancel         func(int64, string, error)
	OnRequestEnd     func(int64)
}
type reply struct {
	result json.RawMessage
	err    error
}
type Peer struct {
	options       Options
	ctx           context.Context
	cancel        context.CancelFunc
	mu            sync.Mutex
	next          int64
	waiting       map[string]chan reply
	ended         error
	incoming      chan struct{}
	handlers      sync.WaitGroup
	closeOnce     sync.Once
	closeDone     chan struct{}
	sendGate      chan struct{}
	incomingBytes int64
}

func New(options Options) (*Peer, error) {
	if options.Send == nil {
		return nil, errors.New("RPC sender is required")
	}
	if options.Context == nil {
		options.Context = context.Background()
	}
	if options.MaxPending <= 0 {
		options.MaxPending = 128
	}
	if options.MaxIncoming <= 0 {
		options.MaxIncoming = 128
	}
	if options.MaxIncomingBytes <= 0 {
		options.MaxIncomingBytes = 64 * 1024 * 1024
	}
	ctx, cancel := context.WithCancel(options.Context)
	return &Peer{options: options, ctx: ctx, cancel: cancel, next: 1, waiting: map[string]chan reply{}, incoming: make(chan struct{}, options.MaxIncoming), closeDone: make(chan struct{}), sendGate: make(chan struct{}, 1)}, nil
}
func (peer *Peer) send(ctx context.Context, message map[string]any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := peer.ctx.Err(); err != nil {
		return err
	}
	active, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(peer.ctx, cancel)
	defer stop()
	select {
	case <-active.Done():
		return active.Err()
	case peer.sendGate <- struct{}{}:
	}
	defer func() { <-peer.sendGate }()
	if peer.options.Strict {
		message["jsonrpc"] = "2.0"
	}
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	if len(data) > MaxMessageBytes {
		return errors.New("RPC message exceeds size limit")
	}
	return peer.options.Send(active, data)
}
func idKey(raw json.RawMessage) string {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	switch value := value.(type) {
	case string:
		return "s:" + value
	case float64:
		return "n:" + strconv.FormatFloat(value, 'g', -1, 64)
	}
	return ""
}
func (peer *Peer) Request(ctx context.Context, method string, params any, target any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	peer.mu.Lock()
	if peer.ended != nil {
		err := peer.ended
		peer.mu.Unlock()
		return err
	}
	if len(peer.waiting) >= peer.options.MaxPending {
		peer.mu.Unlock()
		return ErrPendingLimit
	}
	id := peer.next
	peer.next++
	key := "n:" + strconv.FormatInt(id, 10)
	waiting := make(chan reply, 1)
	peer.waiting[key] = waiting
	peer.mu.Unlock()
	defer func() {
		peer.mu.Lock()
		if peer.waiting[key] == waiting {
			delete(peer.waiting, key)
		}
		peer.mu.Unlock()
		if peer.options.OnRequestEnd != nil {
			peer.options.OnRequestEnd(id)
		}
	}()
	message := map[string]any{"id": id, "method": method}
	if params != nil {
		message["params"] = params
	}
	if err := peer.send(ctx, message); err != nil {
		if ctx.Err() == nil {
			peer.mu.Lock()
			ended := peer.ended
			peer.mu.Unlock()
			if ended != nil {
				return ended
			}
		}
		return err
	}
	select {
	case <-ctx.Done():
		if peer.options.OnCancel != nil {
			peer.options.OnCancel(id, method, ctx.Err())
		}
		return ctx.Err()
	case <-peer.ctx.Done():
		select {
		case response := <-waiting:
			if response.err != nil {
				return response.err
			}
			if target == nil {
				return nil
			}
			return json.Unmarshal(response.result, target)
		default:
		}
		peer.mu.Lock()
		err := peer.ended
		peer.mu.Unlock()
		if err != nil {
			return err
		}
		return peer.ctx.Err()
	case response := <-waiting:
		if response.err != nil {
			return response.err
		}
		if target == nil {
			return nil
		}
		return json.Unmarshal(response.result, target)
	}
}
func (peer *Peer) Notify(ctx context.Context, method string, params any) error {
	peer.mu.Lock()
	ended := peer.ended
	peer.mu.Unlock()
	if ended != nil {
		return ended
	}
	message := map[string]any{"method": method}
	if params != nil {
		message["params"] = params
	}
	return peer.send(ctx, message)
}

type message struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    *int            `json:"code"`
		Message *string         `json:"message"`
		Data    json.RawMessage `json:"data"`
	} `json:"error"`
}

func (peer *Peer) Take(data []byte) error {
	if len(data) > MaxMessageBytes {
		return errors.New("RPC message exceeds size limit")
	}
	var message message
	if json.Unmarshal(data, &message) != nil {
		return nil
	}
	peer.mu.Lock()
	ended := peer.ended != nil || peer.ctx.Err() != nil
	if ended {
		peer.mu.Unlock()
		return nil
	}
	key := idKey(message.ID)
	if message.Method != "" && key != "" {
		// Count the complete frame: even a malformed request can carry result or
		// error fields that the decoded message retains while its handler waits.
		weight := int64(len(data) + 64)
		if peer.incomingBytes > peer.options.MaxIncomingBytes-weight {
			peer.mu.Unlock()
			return peer.send(peer.ctx, map[string]any{"id": message.ID, "error": &Error{Code: -32000, Message: "pending RPC data exceeds size limit"}})
		}
		select {
		case peer.incoming <- struct{}{}:
			peer.incomingBytes += weight
			peer.handlers.Add(1)
			peer.mu.Unlock()
			go peer.answer(message, weight)
			return nil
		default:
			peer.mu.Unlock()
			return peer.send(peer.ctx, map[string]any{"id": message.ID, "error": &Error{Code: -32000, Message: "too many concurrent RPC requests"}})
		}
	}
	if message.Method != "" {
		peer.mu.Unlock()
		if peer.options.Notification != nil {
			peer.options.Notification(message.Method, message.Params)
		}
		return nil
	}
	waiting := peer.waiting[key]
	delete(peer.waiting, key)
	if waiting == nil {
		peer.mu.Unlock()
		return nil
	}
	response := reply{result: message.Result}
	if len(response.result) == 0 {
		response.result = json.RawMessage("null")
	}
	if failure := message.Error; failure != nil {
		code, text := -32000, "error"
		if failure.Code != nil {
			code = *failure.Code
		}
		if failure.Message != nil {
			text = *failure.Message
		}
		response.err = &Error{Code: code, Message: text, Data: failure.Data}
	}
	waiting <- response
	peer.mu.Unlock()
	return nil
}
func (peer *Peer) answer(message message, weight int64) {
	defer peer.handlers.Done()
	defer func() { peer.mu.Lock(); peer.incomingBytes -= weight; peer.mu.Unlock(); <-peer.incoming }()
	var result any
	err := func() (err error) {
		defer func() {
			if cause := recover(); cause != nil {
				err = fmt.Errorf("RPC handler panicked: %v", cause)
			}
		}()
		if peer.options.Request == nil {
			return fmt.Errorf("%w: %s", ErrUnhandled, message.Method)
		}
		result, err = peer.options.Request(peer.ctx, message.Method, message.Params)
		return err
	}()
	reply := map[string]any{"id": message.ID, "result": result}
	if err != nil {
		code := -32603
		if errors.Is(err, ErrUnhandled) {
			code = -32601
		}
		delete(reply, "result")
		reply["error"] = &Error{Code: code, Message: err.Error()}
	}
	if err := peer.send(peer.ctx, reply); err != nil && peer.ctx.Err() == nil {
		peer.End(err)
	}
}
func (peer *Peer) End(reason error) {
	if reason == nil {
		reason = errors.New("RPC peer ended")
	}
	peer.mu.Lock()
	defer peer.mu.Unlock()
	if peer.ended != nil {
		return
	}
	peer.ended = reason
	peer.cancel()
	for key, waiting := range peer.waiting {
		delete(peer.waiting, key)
		waiting <- reply{err: reason}
	}
}
func (peer *Peer) Close(ctx context.Context) error {
	peer.End(nil)
	// No new handlers can enter after End, so Wait cannot race with Add.
	peer.closeOnce.Do(func() { go func() { peer.handlers.Wait(); close(peer.closeDone) }() })
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-peer.closeDone:
		return nil
	}
}
