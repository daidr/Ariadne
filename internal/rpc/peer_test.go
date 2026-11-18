package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func receive(t *testing.T, channel <-chan map[string]any) map[string]any {
	t.Helper()
	select {
	case value := <-channel:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("RPC message did not arrive")
		return nil
	}
}
func sender(channel chan<- map[string]any) func(context.Context, []byte) error {
	return func(ctx context.Context, data []byte) error {
		var value map[string]any
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case channel <- value:
			return nil
		}
	}
}
func take(t *testing.T, peer *Peer, value any) {
	t.Helper()
	data, _ := json.Marshal(value)
	if err := peer.Take(data); err != nil {
		t.Fatal(err)
	}
}
func TestBidirectionalRepliesErrorsNotificationsAndNumericIDs(t *testing.T) {
	sent := make(chan map[string]any, 20)
	notified := make(chan string, 1)
	peer, err := New(Options{Strict: true, Send: sender(sent), Request: func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		if method != "double" {
			return nil, ErrUnhandled
		}
		var value struct {
			N int `json:"n"`
		}
		if err := json.Unmarshal(params, &value); err != nil {
			return nil, err
		}
		return value.N * 2, nil
	}, Notification: func(method string, _ json.RawMessage) { notified <- method }})
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close(t.Context())
	result := make(chan error, 1)
	var response map[string]any
	go func() { result <- peer.Request(t.Context(), "initialize", map[string]any{"name": "client"}, &response) }()
	first := receive(t, sent)
	if first["jsonrpc"] != "2.0" || first["method"] != "initialize" || first["id"] != float64(1) {
		t.Fatal(first)
	}
	if err := peer.Take([]byte(`{"id":1.0,"result":{"ok":true}}`)); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil || response["ok"] != true {
		t.Fatal(response, err)
	}
	take(t, peer, map[string]any{"id": "remote", "method": "double", "params": map[string]any{"n": 3}})
	answered := receive(t, sent)
	if answered["id"] != "remote" || answered["result"] != float64(6) || answered["jsonrpc"] != "2.0" {
		t.Fatal(answered)
	}
	take(t, peer, map[string]any{"id": 0, "method": "unknown"})
	failed := receive(t, sent)
	if failed["id"] != float64(0) || failed["error"].(map[string]any)["code"] != float64(-32601) {
		t.Fatal(failed)
	}
	take(t, peer, map[string]any{"method": "progress", "params": map[string]any{"text": "working"}})
	select {
	case name := <-notified:
		if name != "progress" {
			t.Fatal(name)
		}
	case <-time.After(time.Second):
		t.Fatal("notification missing")
	}
	if err := peer.Notify(t.Context(), "initialized", nil); err != nil {
		t.Fatal(err)
	}
	note := receive(t, sent)
	if _, present := note["params"]; present || note["method"] != "initialized" {
		t.Fatal(note)
	}
	go func() { result <- peer.Request(t.Context(), "refused", nil, nil) }()
	request := receive(t, sent)
	take(t, peer, map[string]any{"id": request["id"], "error": map[string]any{"message": "refused", "data": map[string]any{"detail": "value"}}})
	var remote *Error
	if err := <-result; !errors.As(err, &remote) || remote.Code != -32000 || remote.Message != "refused" || string(remote.Data) != `{"detail":"value"}` {
		t.Fatal(err)
	}
}
func TestPendingRequestsAreBoundedAndCancelledWaitersAreReleased(t *testing.T) {
	sent := make(chan map[string]any, 10)
	peer, err := New(Options{Send: sender(sent), MaxPending: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close(t.Context())
	firstCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	first := make(chan error, 1)
	second := make(chan error, 1)
	go func() { first <- peer.Request(firstCtx, "one", nil, nil) }()
	receive(t, sent)
	go func() { second <- peer.Request(t.Context(), "two", nil, nil) }()
	receive(t, sent)
	if err := peer.Request(t.Context(), "over limit", nil, nil); !errors.Is(err, ErrPendingLimit) {
		t.Fatal(err)
	}
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	third := make(chan error, 1)
	go func() { third <- peer.Request(t.Context(), "three", nil, nil) }()
	receive(t, sent)
	ended := errors.New("program exited")
	peer.End(ended)
	for _, done := range []<-chan error{second, third} {
		select {
		case err := <-done:
			if !errors.Is(err, ended) {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("ended peer retained a waiter")
		}
	}
	peer.mu.Lock()
	left := len(peer.waiting)
	peer.mu.Unlock()
	if left != 0 {
		t.Fatal(left)
	}
	if err := peer.Request(t.Context(), "after end", nil, nil); !errors.Is(err, ended) {
		t.Fatal(err)
	}
}
func TestCloseCancelsBlockedWritesAndIncomingHandlers(t *testing.T) {
	entered := make(chan struct{})
	peer, err := New(Options{Send: func(ctx context.Context, _ []byte) error { close(entered); <-ctx.Done(); return ctx.Err() }})
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- peer.Request(t.Context(), "blocked", nil, nil) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("write did not enter")
	}
	if err := peer.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("blocked request succeeded after close")
		}
	case <-time.After(time.Second):
		t.Fatal("close retained a blocked writer")
	}
	sent := make(chan map[string]any, 10)
	handlerEntered := make(chan struct{})
	peer, err = New(Options{Send: sender(sent), MaxIncoming: 1, Request: func(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
		close(handlerEntered)
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	if err != nil {
		t.Fatal(err)
	}
	take(t, peer, map[string]any{"id": "first", "method": "wait"})
	<-handlerEntered
	take(t, peer, map[string]any{"id": "second", "method": "wait"})
	rejected := receive(t, sent)
	if rejected["error"].(map[string]any)["code"] != float64(-32000) {
		t.Fatal(rejected)
	}
	if err := peer.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(peer.incoming) != 0 {
		t.Fatal("close retained a request handler")
	}
}

func TestAReplyAlreadyReceivedWinsOverImmediateProcessExit(t *testing.T) {
	for i := 0; i < 20; i++ {
		var peer *Peer
		var err error
		peer, err = New(Options{Send: func(_ context.Context, data []byte) error {
			var request map[string]any
			json.Unmarshal(data, &request)
			take(t, peer, map[string]any{"id": request["id"], "result": "last reply"})
			peer.End(errors.New("exited"))
			return nil
		}})
		if err != nil {
			t.Fatal(err)
		}
		var value string
		if err := peer.Request(t.Context(), "last", nil, &value); err != nil || value != "last reply" {
			t.Fatal(value, err)
		}
		peer.Close(t.Context())
	}
}

func TestIncomingByteLimitCountsEveryRetainedField(t *testing.T) {
	sent := make(chan map[string]any, 1)
	peer, err := New(Options{Send: sender(sent), MaxIncomingBytes: 1024, Request: func(context.Context, string, json.RawMessage) (any, error) { return "accepted", nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close(t.Context())
	take(t, peer, map[string]any{"id": 1, "method": "tool", "result": strings.Repeat("x", 2048)})
	message := receive(t, sent)
	failure, ok := message["error"].(map[string]any)
	if !ok || failure["code"] != float64(-32000) {
		t.Fatal(message)
	}
}
