package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/modelhttp"
	"github.com/daidr/Ariadne/sdk"
)

func sse(events ...any) string {
	var result strings.Builder
	for _, event := range events {
		if raw, ok := event.(string); ok {
			result.WriteString(raw)
		} else {
			data, _ := json.Marshal(event)
			result.WriteString("data: " + string(data) + "\n\n")
		}
	}
	return result.String()
}
func TestAnthropicMessagesCacheStreamingModelsAndSearch(t *testing.T) {
	type obj = map[string]any
	complete := []any{
		": ping\n\n", "data: invalid\n\n",
		obj{"type": "message_start", "message": obj{"usage": obj{"input_tokens": 10, "output_tokens": 1, "cache_creation_input_tokens": 30, "cache_read_input_tokens": 60}}},
		obj{"type": "content_block_start", "index": 0, "content_block": obj{"type": "thinking"}},
		obj{"type": "content_block_delta", "index": 0, "delta": obj{"type": "thinking_delta", "thinking": "检查"}},
		obj{"type": "content_block_delta", "index": 0, "delta": obj{"type": "signature_delta", "signature": "签名"}},
		obj{"type": "content_block_stop", "index": 0},
		obj{"type": "content_block_start", "index": 1, "content_block": obj{"type": "redacted_thinking", "data": "redacted"}},
		obj{"type": "content_block_stop", "index": 1},
		obj{"type": "content_block_start", "index": 9, "content_block": obj{"type": "thinking"}},
		obj{"type": "content_block_stop", "index": 9},
		obj{"type": "content_block_delta", "delta": obj{"type": "text_delta", "text": "结果"}},
		obj{"type": "content_block_start", "index": 3, "content_block": obj{"type": "tool_use", "id": "call_a", "name": "read"}},
		obj{"type": "content_block_start", "index": 2, "content_block": obj{"type": "tool_use", "name": "list"}},
		obj{"type": "content_block_delta", "index": 3, "delta": obj{"type": "input_json_delta", "partial_json": "{\"path\":"}},
		obj{"type": "content_block_delta", "index": 2, "delta": obj{"type": "input_json_delta", "partial_json": ""}},
		obj{"type": "content_block_delta", "index": 3, "delta": obj{"type": "input_json_delta", "partial_json": "\"a\"}"}},
		obj{"type": "content_block_delta", "index": 2, "delta": obj{"type": "input_json_delta", "partial_json": "{}"}},
		obj{"type": "content_block_stop", "index": 3}, obj{"type": "content_block_stop", "index": 2},
		obj{"type": "message_delta", "delta": obj{"stop_reason": "tool_use"}, "usage": obj{"output_tokens": 8}}, obj{"type": "message_stop"},
	}
	cacheOff := false
	baseRequest := sdk.Request{Model: "claude", Effort: "high", Tools: []sdk.Tool{{Name: "read", Description: "读取", Parameters: obj{"type": "object"}}, {Name: "list", Description: "列表", Parameters: obj{"type": "object"}}}, Messages: []sdk.Message{
		{Role: "system", Content: "系统"}, {Role: "system", Content: "约定"}, {Role: "user", Content: "用户", Images: []sdk.Image{{MIME: "image/png", Data: "AAAA"}}}, {Role: "user", Content: "补充"},
		{Role: "assistant", Content: "", Reasoning: []sdk.Reasoning{{Text: "不带签名"}, {Text: "已签名", Signature: "signed"}, {Text: "隐藏", Redacted: true}}, ToolCalls: []sdk.ToolCall{{ID: "old", Name: "read", Arguments: `{"path":"a"}`}, {ID: "bad", Name: "list", Arguments: `[]`}}},
		{Role: "tool", ToolCallID: "old", Content: "结果", Images: []sdk.Image{{MIME: "image/png", Data: "BBBB"}}, IsError: true}, {Role: "tool", ToolCallID: "bad", Content: "结果"}, {Role: "assistant", Content: "继续"}, {Role: "user", Content: "新问题"},
	}, Headers: map[string]string{"X-Custom": "yes", "x-api-key": "incorrect"}, ExtraBody: obj{"temperature": 0.2, "model": "incorrect", "messages": nil, "tools": nil, "system": "incorrect", "stream": false}}
	type streamCase struct {
		Request  sdk.Request       `json:"request"`
		Settings Settings          `json:"settings"`
		Body     string            `json:"body"`
		Status   int               `json:"status"`
		Headers  map[string]string `json:"headers"`
	}
	settings := Settings{BaseURL: "https://unused.invalid/", APIKey: "key"}
	streams := []streamCase{{baseRequest, settings, sse(complete...), 200, nil}}
	withoutCache := baseRequest
	withoutCache.Cache = &cacheOff
	withoutCache.Effort = ""
	withoutCache.MaxTokens = 123
	streams = append(streams, streamCase{withoutCache, settings, sse(obj{"type": "message_delta", "delta": obj{"stop_reason": "max_tokens"}}, obj{"type": "message_stop"}), 200, nil})
	for _, event := range []obj{{"type": "error", "error": obj{"type": "overloaded_error", "message": "busy"}}, {"type": "error", "error": obj{"type": "invalid_request_error", "message": "bad request"}}, {"type": "content_block_delta", "delta": obj{"type": "text_delta", "text": "部分回答"}}} {
		streams = append(streams, streamCase{baseRequest, settings, sse(event), 200, nil})
	}
	streams = append(streams, streamCase{baseRequest, settings, `{"error":{"message":"invalid key"}}`, 401, nil}, streamCase{baseRequest, settings, `{"error":{"message":"rate limit"}}`, 429, map[string]string{"Retry-After": "1"}})
	searches := []obj{}
	for _, response := range []string{
		`{"stop_reason":"end_turn","content":[{"type":"web_search_tool_result","content":[{"url":"https://other.example","title":"其他"},{"url":"https://cited.example","title":"搜索标题"}]},{"type":"text","text":" 答案 ","citations":[{"url":"https://cited.example","title":"引用标题","cited_text":" 片段1 "},{"url":"https://cited.example","cited_text":"片段2"},{"url":"https://cited.example","cited_text":"片段1"}]}],"usage":{"input_tokens":5,"output_tokens":2,"cache_creation_input_tokens":10,"cache_read_input_tokens":20}}`,
		`{"content":[{"type":"web_search_tool_result","content":{"error_code":"unavailable"}}]}`, `{"stop_reason":"refusal"}`, `{"stop_reason":"pause_turn"}`, `{"stop_reason":"max_tokens"}`, `{"content":[{"type":"text","text":"没有找到相关结果"}]}`, `{"content":[{"type":"web_search_tool_result","content":{"error_code":"failed"}},{"type":"web_search_tool_result","content":[{"url":"https://found.example"}]}]}`,
	} {
		var decoded any
		if err := json.Unmarshal([]byte(response), &decoded); err != nil {
			t.Fatal(err)
		}
		searches = append(searches, obj{"response": decoded, "limit": 2})
	}
	models := obj{"data": []obj{{"id": "new", "display_name": " 新模型 ", "capabilities": obj{"effort": obj{"supported": true, "low": obj{"supported": true}, "xhigh": obj{"supported": true}, "ultra": obj{"supported": true}}}}, {"id": "old", "capabilities": obj{"effort": obj{"supported": false}}}, {"id": "unknown"}, {"id": 5}}}
	envs := []map[string]string{{}, {"ANTHROPIC_API_KEY": " key "}, {"ANTHROPIC_API_KEY": "key", "ANTHROPIC_BASE_URL": " https://proxy.example "}}
	nativeStreams := []obj{}
	for _, test := range streams {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "key" || r.Header.Get("anthropic-version") != APIVersion || r.Header.Get("X-Custom") != "yes" {
				t.Errorf("bad request: %s %v", r.URL, r.Header)
			}
			var sent any
			if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
				t.Error(err)
			}
			if !reflect.DeepEqual(roundtrip(RequestBody(settings, test.Request)), sent) {
				t.Error("HTTP body differs")
			}
			for key, value := range test.Headers {
				w.Header().Set(key, value)
			}
			w.WriteHeader(test.Status)
			io.WriteString(w, test.Body)
		}))
		provider := Provider{Client: server.Client(), Now: func() time.Time { return time.UnixMilli(1800000000000) }}
		parts := []sdk.Part{}
		for part := range provider.Stream(t.Context(), Settings{BaseURL: server.URL + "/", APIKey: "key"}, test.Request) {
			parts = append(parts, part)
		}
		server.Close()
		if len(parts) == 0 || parts[len(parts)-1].Type != "finish" {
			t.Fatalf("missing finish: %+v", parts)
		}
		nativeStreams = append(nativeStreams, obj{"body": RequestBody(settings, test.Request), "parts": parts})
	}
	nativeSearches := []obj{}
	for _, test := range searches {
		data, _ := json.Marshal(test["response"])
		value, err := ReadSearch(data, 2)
		entry := obj{"value": value, "error": nil}
		if err != nil {
			entry["value"] = nil
			entry["error"] = err.Error()
		}
		nativeSearches = append(nativeSearches, entry)
	}
	modelData, _ := json.Marshal(models)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RequestURI() != "/v1/models?limit=1000" {
			t.Error(r.URL)
		}
		w.Write(modelData)
	}))
	defer server.Close()
	nativeModels, err := (Provider{Client: server.Client()}).Models(t.Context(), Settings{BaseURL: server.URL, APIKey: "key"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := []any{}
	for _, env := range envs {
		found = append(found, Find(env))
	}
	if len(nativeStreams) != len(streams) || len(nativeSearches) != len(searches) || len(nativeModels) == 0 || len(found) != len(envs) {
		t.Fatalf("unexpected counts: streams=%d searches=%d models=%d found=%d", len(nativeStreams), len(nativeSearches), len(nativeModels), len(found))
	}
}
func roundtrip(value any) any {
	data, _ := json.Marshal(value)
	var result any
	json.Unmarshal(data, &result)
	return result
}
func TestCancellationAndSearchHTTPRefusal(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for part := range (Provider{}).Stream(ctx, Settings{BaseURL: "http://127.0.0.1:1"}, sdk.Request{}) {
		t.Fatalf("cancellation emitted %+v", part)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) }))
	defer server.Close()
	_, err := (Provider{Client: server.Client()}).Search(t.Context(), Settings{BaseURL: server.URL}, sdk.SearchRequest{})
	var refused *modelhttp.SearchRefused
	if !errors.As(err, &refused) || refused.Status != 403 {
		t.Fatal(err)
	}
	if !SearchAvailable(Settings{BaseURL: "https://api.anthropic.com"}) || SearchAvailable(Settings{BaseURL: "https://api.anthropic.com.attacker.invalid"}) {
		t.Fatal("invalid search host boundary")
	}
}

func TestTooManyRetainedBlocksEndTheStream(t *testing.T) {
	events := []any{}
	for i := 0; i <= 1024; i++ {
		events = append(events, map[string]any{"type": "content_block_start", "index": i, "content_block": map[string]any{"type": "tool_use", "name": "call"}})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, sse(events...)) }))
	defer server.Close()
	var last sdk.Part
	for part := range (Provider{Client: server.Client()}).Stream(t.Context(), Settings{BaseURL: server.URL}, sdk.Request{}) {
		last = part
	}
	if last.Type != "finish" || last.Reason != "error" || last.Error != "model response exceeds size limit" {
		t.Fatal(last)
	}
}
