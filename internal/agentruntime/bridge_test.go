package agentruntime

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/daidr/Ariadne/sdk"
)

func newTestBridge(t *testing.T) *ToolBridge {
	t.Helper()
	bridge := NewToolBridge()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := bridge.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return bridge
}
func bridgePost(t *testing.T, url, body string) (*http.Response, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response, data
}
func TestToolBridgeHTTPAndSSE(t *testing.T) {
	bridge := newTestBridge(t)
	registration, err := bridge.Add(AgentTools{Specs: []sdk.Tool{{Name: "read", Description: "read file", Parameters: map[string]any{"properties": map[string]any{}}}}, Call: func(ctx context.Context, name string, args any) ToolOutput {
		return ToolOutput{Content: name + " 中文", Images: []sdk.Image{{MIME: "image/png", Data: "AAAA"}}}
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, data := bridgePost(t, registration.URL, `{"jsonrpc":"2.0","id":"i","method":"initialize","params":{"protocolVersion":"test"}}`)
	var value struct {
		ID     string `json:"id"`
		Result struct {
			Protocol string `json:"protocolVersion"`
		} `json:"result"`
	}
	if json.Unmarshal(data, &value) != nil || value.ID != "i" || value.Result.Protocol != "test" {
		t.Fatal(string(data))
	}
	response, data := bridgePost(t, registration.URL, `[{"id":1,"method":"tools/list"},{"method":"notifications/initialized"},{"id":2,"method":"unknown"}]`)
	if response.StatusCode != 200 || response.Header.Get("Mcp-Session-Id") == "" || !strings.Contains(string(data), `"inputSchema":{"properties":{},"type":"object"}`) || !strings.Contains(string(data), `"code":-32601`) {
		t.Fatal(string(data), response.StatusCode)
	}
	response, data = bridgePost(t, registration.URL, `{"id":3,"method":"tools/call","params":{"name":"read","arguments":{}}}`)
	if response.Header.Get("Content-Type") != "text/event-stream" || !strings.Contains(string(data), "event: message\ndata:") || !strings.Contains(string(data), `"text":"read 中文"`) || !strings.Contains(string(data), `"mimeType":"image/png"`) {
		t.Fatal(string(data))
	}
	response, data = bridgePost(t, registration.URL, `{"id":4,"method":"tools/call","params":{}}`)
	if !strings.Contains(string(data), `"code":-32602`) {
		t.Fatal(string(data))
	}
	response, _ = bridgePost(t, registration.URL, `{"method":"notifications/initialized"}`)
	if response.StatusCode != 202 {
		t.Fatal(response.StatusCode)
	}
	response, data = bridgePost(t, registration.URL, `{`)
	if response.StatusCode != 400 || !strings.Contains(string(data), `-32700`) {
		t.Fatal(response.StatusCode, string(data))
	}
	registration.Remove()
	registration.Remove()
	if bridge.Count() != 0 {
		t.Fatal("registration retained")
	}
	response, _ = bridgePost(t, registration.URL, `{"id":1,"method":"ping"}`)
	if response.StatusCode != 404 {
		t.Fatal("removed token still authorized")
	}
}
func TestToolBridgeAuthorizationBoundariesAndLimits(t *testing.T) {
	bridge := newTestBridge(t)
	registration, err := bridge.Add(AgentTools{Call: func(context.Context, string, any) ToolOutput { return ToolOutput{} }})
	if err != nil {
		t.Fatal(err)
	}
	for _, headers := range []map[string]string{{"Origin": "https://evil.invalid"}, {"Host": "evil.invalid"}} {
		request, _ := http.NewRequestWithContext(t.Context(), "POST", registration.URL, strings.NewReader(`{"id":1,"method":"ping"}`))
		for key, value := range headers {
			if key == "Host" {
				request.Host = value
			} else {
				request.Header.Set(key, value)
			}
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 403 {
			t.Fatal("foreign origin or DNS rebinding accepted", headers, response.StatusCode)
		}
	}
	response, _ := bridgePost(t, registration.URL+"/unknown", `{"id":1,"method":"ping"}`)
	if response.StatusCode != 404 {
		t.Fatal("partial token accepted")
	}
	response, _ = bridgePost(t, registration.URL, strings.Repeat(" ", maxBridgeBody+1))
	if response.StatusCode != 413 {
		t.Fatal("body limit missing", response.StatusCode)
	}
	response, data := bridgePost(t, registration.URL, "["+strings.Repeat(`{"id":1,"method":"ping"},`, 128)+`{"id":1,"method":"ping"}]`)
	if response.StatusCode != 400 || !strings.Contains(string(data), "too many requests") {
		t.Fatal(response.StatusCode, string(data))
	}
	for _, method := range []string{"GET", "DELETE"} {
		request, _ := http.NewRequestWithContext(t.Context(), method, registration.URL, nil)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		wanted := 405
		if method == "DELETE" {
			wanted = 200
		}
		if response.StatusCode != wanted {
			t.Fatal(method, response.StatusCode)
		}
	}
}
func TestToolBridgeKeepAliveCancellationAndClose(t *testing.T) {
	for _, remove := range []bool{true, false} {
		t.Run(map[bool]string{true: "remove", false: "close"}[remove], func(t *testing.T) {
			bridge := newTestBridge(t)
			bridge.keepAlive = 5 * time.Millisecond
			started, finished := make(chan struct{}), make(chan struct{})
			registration, err := bridge.Add(AgentTools{Call: func(ctx context.Context, _ string, _ any) ToolOutput {
				close(started)
				defer close(finished)
				<-ctx.Done()
				return endedOutput("cancelled")
			}})
			if err != nil {
				t.Fatal(err)
			}
			request, _ := http.NewRequestWithContext(t.Context(), "POST", registration.URL, strings.NewReader(`{"id":1,"method":"tools/call","params":{"name":"read"}}`))
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			<-started
			line, err := bufio.NewReader(response.Body).ReadString('\n')
			if err != nil || line != ": keep-alive\n" {
				t.Fatal(line, err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if remove {
				registration.Remove()
			} else if err := bridge.Close(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case <-finished:
			case <-ctx.Done():
				t.Fatal("held callback not cancelled")
			}
			if bridge.Count() != 0 {
				t.Fatal("client retained")
			}
		})
	}
}
func TestToolBridgeResultBudget(t *testing.T) {
	budget := &bridgeBudget{}
	if !budget.reserve(maxBridgeResult-65536) || budget.reserve(1) {
		t.Fatal("batch result memory budget missing")
	}
	bridge := newTestBridge(t)
	registration, err := bridge.Add(AgentTools{Call: func(context.Context, string, any) ToolOutput {
		return ToolOutput{Content: strings.Repeat("x", maxBridgeResult+1)}
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, data := bridgePost(t, registration.URL, `{"id":1,"method":"tools/call","params":{"name":"read"}}`)
	if len(data) > 1024 || !strings.Contains(string(data), "size limit") {
		t.Fatal("oversized result retained or transmitted", len(data))
	}
}
