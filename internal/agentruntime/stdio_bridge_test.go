package agentruntime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStdioBridgeParallelCallsAndPing(t *testing.T) {
	bridge := newTestBridge(t)
	started, release := make(chan struct{}), make(chan struct{})
	registration, err := bridge.Add(AgentTools{Call: func(ctx context.Context, _ string, _ any) ToolOutput {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return ToolOutput{Content: "中文结果"}
	}})
	if err != nil {
		t.Fatal(err)
	}
	input, writer := io.Pipe()
	output, reader := io.Pipe()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { defer reader.Close(); done <- RunStdioBridge(ctx, registration.URL, input, reader) }()
	if _, err := io.WriteString(writer, "{\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":\"read\"}}\n"); err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err := io.WriteString(writer, "{\"id\":2,\"method\":\"ping\"}\n"); err != nil {
		t.Fatal(err)
	}
	lines := make(chan string, 2)
	go func() {
		scanner := bufio.NewScanner(output)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()
	select {
	case line := <-lines:
		var message struct {
			ID int `json:"id"`
		}
		if json.Unmarshal([]byte(line), &message) != nil || message.ID != 2 {
			t.Fatal("ping held behind tool call", line)
		}
	case <-ctx.Done():
		t.Fatal("ping blocked")
	}
	close(release)
	writer.Close()
	select {
	case line := <-lines:
		if !strings.Contains(line, "中文结果") {
			t.Fatal(line)
		}
	case <-ctx.Done():
		t.Fatal("tool result missing")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
func TestStdioBridgeCancellationAndEndpointBoundary(t *testing.T) {
	for _, address := range []string{"https://127.0.0.1:1/mcp/token", "http://example.com:80/mcp/token", "http://user@127.0.0.1:1/mcp/token", "http://127.0.0.1:1/mcp/token?secret=1", "http://127.0.0.1:1/other"} {
		if err := RunStdioBridge(t.Context(), address, strings.NewReader(""), io.Discard); err == nil {
			t.Fatal("invalid endpoint accepted", address)
		}
	}
	bridge := newTestBridge(t)
	finished := make(chan struct{})
	registration, err := bridge.Add(AgentTools{Call: func(ctx context.Context, _ string, _ any) ToolOutput {
		defer close(finished)
		<-ctx.Done()
		return endedOutput("cancelled")
	}})
	if err != nil {
		t.Fatal(err)
	}
	input, writer := io.Pipe()
	defer writer.Close()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- RunStdioBridge(ctx, registration.URL, input, io.Discard) }()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled shim succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled shim retained stdin")
	}
}
func TestStdioBridgeSSEAndHTTPFailure(t *testing.T) {
	for _, broken := range []bool{false, true} {
		t.Run(map[bool]string{false: "SSE", true: "HTTP"}[broken], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if broken {
					http.Error(w, "private secret response", 503)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, ": keep-alive\r\n\r\nevent: message\r\ndata: {\"id\":1,\"result\":\r\ndata: {\"value\":\"中文\"}}\r\n\r\n")
			}))
			defer server.Close()
			var output bytes.Buffer
			if err := RunStdioBridge(t.Context(), server.URL+"/mcp/token", strings.NewReader("invalid json\n{\"id\":1,\"method\":\"ping\"}\n"), &output); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(output.String(), "private secret") {
				t.Fatal("HTTP error body leaked")
			}
			if broken {
				if !strings.Contains(output.String(), `"code":-32000`) {
					t.Fatal(output.String())
				}
			} else {
				var message struct {
					Result struct {
						Value string `json:"value"`
					} `json:"result"`
				}
				if json.Unmarshal(output.Bytes(), &message) != nil || message.Result.Value != "中文" {
					t.Fatal(output.String())
				}
			}
		})
	}
}
