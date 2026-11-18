package openai

import (
	"context"
	"encoding/json"
	"github.com/daidr/Ariadne/internal/legacytest"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/daidr/Ariadne/sdk"
)

func sse(chunks ...any) string {
	var text strings.Builder
	for _, chunk := range chunks {
		if raw, ok := chunk.(string); ok {
			text.WriteString(raw)
			continue
		}
		data, _ := json.Marshal(chunk)
		text.WriteString("data: " + string(data) + "\n\n")
	}
	return text.String()
}

func TestProviderMatchesLegacyStreamAndBody(t *testing.T) {
	cases := []struct {
		name, body string
		status     int
		headers    map[string]string
		finish     string
	}{
		{"thinking-cache-tools", sse(
			": keepalive\n\n",
			map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"reasoning_content": "先检查"}}}},
			map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "结果"}}}},
			map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 2, "id": "call2", "function": map[string]any{"name": "read_", "arguments": "{\"p\":"}}}}}}},
			map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "function": map[string]any{"name": "list", "arguments": "{}"}}, map[string]any{"index": 2, "function": map[string]any{"name": "file", "arguments": "\"a\"}"}}}}, "finish_reason": "tool_calls"}}},
			map[string]any{"usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 8, "prompt_cache_hit_tokens": 90, "completion_tokens_details": map[string]any{"reasoning_tokens": 3}}},
			"data: [DONE]\n\n"), 200, nil, "tool-calls"},
		{"length-in-tool", sse(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "function": map[string]any{"name": "write", "arguments": "{\"text\":\""}}}}, "finish_reason": "length"}}}), 200, nil, "length"},
		{"incomplete", sse(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "部分回答"}}}}), 200, nil, "error"},
		{"terminal-without-newline", "data: {\"choices\":[{\"delta\":{\"content\":\"好了\"},\"finish_reason\":\"stop\"}]}", 200, nil, "stop"},
		{"quota", `{"error":{"message":"quota exceeded"}}`, 429, map[string]string{"Retry-After": "61"}, "error"},
		{"retryable", `{"error":{"message":"busy"}}`, 503, map[string]string{"Retry-After": "0"}, "error"},
		{"refused", `{"error":{"message":"invalid key"}}`, 401, nil, "error"},
		{"invalid-chunk-then-done", "data: invalid\n\ndata: [DONE]\n\n", 200, nil, "stop"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var sent map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/chat/completions" {
					t.Errorf("unexpected path %s", r.URL.Path)
				}
				if r.Header.Get("Authorization") != "Bearer key" {
					t.Error("provider key header missing")
				}
				if r.Header.Get("X-Custom") != "given" {
					t.Error("custom header missing")
				}
				if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
					t.Error(err)
				}
				for key, value := range test.headers {
					w.Header().Set(key, value)
				}
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			settings := Settings{BaseURL: server.URL + "/v1/", APIKey: "key"}
			request := sdk.Request{Model: "m", Effort: "high", MaxTokens: 1024, Headers: map[string]string{"X-Custom": "given"}, Tools: []sdk.Tool{{Name: "read_file", Description: "read", Parameters: map[string]any{"type": "object"}}}, Messages: []sdk.Message{
				{Role: "system", Content: "system"},
				{Role: "user", Content: "look", Images: []sdk.Image{{MIME: "image/png", Data: "AAAA"}}},
				{Role: "assistant", ToolCalls: []sdk.ToolCall{{ID: "old", Name: "read", Arguments: "{}"}}, Reasoning: []sdk.Reasoning{{Text: "visible"}, {Text: "hidden", Redacted: true}}},
				{Role: "tool", ToolCallID: "old", Content: "result", Images: []sdk.Image{{MIME: "image/png", Data: "BBBB"}}},
			}, ExtraBody: map[string]any{"temperature": 0.5, "model": "cannot-override", "stream": false, "system": "cannot-add"}}
			provider := Provider{Client: server.Client(), Now: func() time.Time { return time.UnixMilli(1800000000000) }}
			parts := make([]sdk.Part, 0)
			for part := range provider.Stream(t.Context(), settings, request) {
				parts = append(parts, part)
			}
			if len(parts) == 0 || parts[len(parts)-1].Type != "finish" || parts[len(parts)-1].Reason != test.finish {
				t.Fatalf("finish: %+v", parts)
			}
			if sent["model"] != "m" || sent["stream"] != true || sent["system"] != nil {
				t.Fatalf("protected body fields changed: %v", sent)
			}
			bun, err := exec.LookPath("bun")
			if err != nil {
				if os.Getenv("ARIADNE_REQUIRE_LEGACY") == "1" {
					t.Fatal(err)
				}
				t.Log("Bun absent: native assertions ran, legacy comparison skipped")
				return
			}
			root, err := filepath.Abs(filepath.Join("..", "..", ".."))
			if err != nil {
				t.Fatal(err)
			}
			input, _ := json.Marshal(map[string]any{"settings": settings, "request": request, "body": test.body, "status": test.status, "headers": test.headers})
			cmd := legacytest.Command(t, bun, "packages/plugin-openai-compatible/src/testing/go-provider-fixture.ts")
			cmd.Dir = root
			cmd.Stdin = strings.NewReader(string(input))
			data, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("legacy oracle: %v\n%s", err, data)
			}
			var legacy, native any
			if err := json.Unmarshal(data, &legacy); err != nil {
				t.Fatalf("legacy JSON: %v\n%s", err, data)
			}
			nativeJSON, _ := json.Marshal(map[string]any{"body": RequestBody(settings, request), "parts": parts})
			if err := json.Unmarshal(nativeJSON, &native); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(native, legacy) {
				t.Fatalf("provider differs\nGo: %s\nTS: %s", nativeJSON, data)
			}
		})
	}
}

func TestOfficialLimitFieldAndCancellation(t *testing.T) {
	for _, test := range []struct{ url, field string }{{"https://api.openai.com/v1", "max_completion_tokens"}, {"https://api.deepseek.com/v1", "max_tokens"}, {"https://api.openai.com.attacker.invalid/v1", "max_tokens"}} {
		body := RequestBody(Settings{BaseURL: test.url}, sdk.Request{MaxTokens: 100})
		if body[test.field] != int64(100) {
			t.Fatalf("wrong output limit for %s: %v", test.url, body)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	provider := Provider{}
	for part := range provider.Stream(ctx, Settings{BaseURL: "http://127.0.0.1:1"}, sdk.Request{}) {
		t.Fatalf("intentional cancellation emitted a failure: %+v", part)
	}
}

func TestTooManyRetainedToolCallsEndTheStream(t *testing.T) {
	chunks := []any{}
	for i := 0; i <= 1024; i++ {
		chunks = append(chunks, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": i, "function": map[string]any{"name": "call"}}}}}}})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, sse(chunks...)) }))
	defer server.Close()
	var last sdk.Part
	for part := range (Provider{Client: server.Client()}).Stream(t.Context(), Settings{BaseURL: server.URL}, sdk.Request{}) {
		last = part
	}
	if last.Type != "finish" || last.Reason != "error" || last.Error != "model response exceeds size limit" {
		t.Fatal(last)
	}
}
