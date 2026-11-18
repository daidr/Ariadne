package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/process"
)

func TestRPCProcess(t *testing.T) {
	if os.Getenv("ARIADNE_RPC_HELPER") != "1" {
		return
	}
	fmt.Println("a stray startup log")
	fmt.Fprint(os.Stderr, strings.Repeat("x", 5000)+" last error")
	input := bufio.NewScanner(os.Stdin)
	input.Buffer(make([]byte, 4096), MaxMessageBytes)
	var waiting any
	send := func(value any) { data, _ := json.Marshal(value); fmt.Println(string(data)) }
	for input.Scan() {
		var message map[string]any
		if json.Unmarshal(input.Bytes(), &message) != nil {
			continue
		}
		if message["id"] == "tool" {
			send(map[string]any{"id": waiting, "result": message["result"]})
			continue
		}
		switch message["method"] {
		case "echo":
			send(map[string]any{"id": message["id"], "result": message["params"]})
		case "call":
			waiting = message["id"]
			send(map[string]any{"id": "tool", "method": "local_tool", "params": map[string]any{"n": 3}})
		case "finish":
			send(map[string]any{"id": message["id"], "result": "final"})
			os.Exit(0)
		case "flood":
			fmt.Print(strings.Repeat("x", 3000))
		}
	}
	os.Exit(0)
}
func stdioOptions(t *testing.T) process.JSONOptions {
	t.Helper()
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return process.JSONOptions{Argv: []string{program, "-test.run=TestRPCProcess"}, Env: map[string]string{"ARIADNE_RPC_HELPER": "1"}, ReplaceEnv: true}
}
func TestNativeStdioBidirectionalExchangeAndFinalReply(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	link, err := StartStdio(ctx, stdioOptions(t), Options{Strict: true, Request: func(_ context.Context, method string, params json.RawMessage) (any, error) {
		if method != "local_tool" {
			return nil, ErrUnhandled
		}
		var value struct {
			N int `json:"n"`
		}
		json.Unmarshal(params, &value)
		return value.N * 2, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer link.Close(t.Context())
	var echo map[string]any
	if err := link.Peer.Request(ctx, "echo", map[string]any{"text": "中文😀"}, &echo); err != nil || echo["text"] != "中文😀" {
		t.Fatal(echo, err)
	}
	var called int
	if err := link.Peer.Request(ctx, "call", nil, &called); err != nil || called != 6 {
		t.Fatal(called, err)
	}
	var final string
	if err := link.Peer.Request(ctx, "finish", nil, &final); err != nil || final != "final" {
		t.Fatal(final, err)
	}
	select {
	case <-link.Process.Done():
	case <-ctx.Done():
		t.Fatal("process did not exit")
	}
	tail := link.Process.StderrTail()
	if len(tail) > 4000 || !strings.HasSuffix(tail, "last error") {
		t.Fatal(len(tail))
	}
}
func TestNativeStdioStopsAnOversizedUnterminatedLine(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	options := stdioOptions(t)
	options.MaxLineBytes = 1024
	link, err := StartStdio(ctx, options, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer link.Close(t.Context())
	err = link.Peer.Request(ctx, "flood", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "line over") {
		t.Fatal(err)
	}
	if err := link.Close(ctx); err != nil {
		t.Fatal(err)
	}
}
