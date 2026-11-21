package server

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/eventstore"
)

func traceSeed() []eventstore.NewEvent {
	event := func(kind string, data object) eventstore.NewEvent {
		data["conversationId"] = "coo_a"
		return eventstore.NewEvent{ProjectID: "prj_a", Type: kind, Data: json.RawMessage(encoded(data))}
	}
	timing := object{"startedAt": 100, "firstOutputAt": 110}
	input := object{"call": "call", "system": "system", "tools": "tools"}
	return []eventstore.NewEvent{
		{ProjectID: "prj_a", Type: "project.created", Data: json.RawMessage(`{"name":"project","coordinatorId":"coo_a"}`)},
		{ProjectID: "prj_a", Type: "prompt.recorded", Data: json.RawMessage(encoded(object{"hash": "call", "content": encoded(object{"provider": "openai-compatible", "model": "gpt-6.1", "effort": "high", "settings": object{}})}))},
		event("turn.started", object{"turnId": "turn_a", "model": "fixture"}),
		event("turn.step", object{"turnId": "turn_a", "number": 1, "text": "looking", "reasoning": []any{object{"text": "hidden", "redacted": true}, object{"text": "visible"}}, "toolCalls": []any{object{"id": "c1", "name": "research", "arguments": "{}"}, object{"id": "c2", "name": "read_file", "arguments": "{}"}}, "finish": "tool-calls", "usage": object{"input": 10, "output": 2, "cached": 4, "reasoning": 1}, "timing": timing, "input": input, "sawSeq": 1}),
		event("research.step", object{"turnId": "turn_a", "toolCallId": "c1", "number": 1, "question": "find it", "text": "found", "reasoning": []any{}, "toolCalls": []any{object{"id": "r1", "name": "search", "arguments": "{}"}}, "results": []any{object{"toolCallId": "r1", "content": "answer", "isError": false}}, "finish": "stop", "usage": object{"input": 7, "output": 3, "cacheWrite": 2}, "timing": timing, "input": input, "answeredAt": 120}),
		event("tool.finished", object{"turnId": "turn_a", "toolCallId": "c2", "name": "read_file", "content": "file", "isError": false, "timing": object{"startedAt": 121, "finishedAt": 125}}),
		event("tool.finished", object{"turnId": "turn_a", "toolCallId": "c1", "name": "research", "content": "summary", "isError": false}),
		event("conversation.model_retry", object{"turnId": "turn_a", "attempt": 1, "delayMs": 10, "detail": "retry"}),
		event("conversation.model_fallback", object{"turnId": "turn_a", "from": "a", "to": "b", "detail": "fallback"}),
		event("conversation.compacted", object{"turnId": "turn_a", "summary": "notes", "reasoning": []any{object{"text": "summary reasoning"}}, "call": "call", "timing": timing, "usage": object{"input": 5, "output": 2}, "upTo": 0, "sawSeq": 1, "state": "state"}),
		event("conversation.results_cleared", object{"turnId": "turn_a", "upTo": 0}),
		event("conversation.compaction_failed", object{"turnId": "turn_a", "detail": "failure"}),
		event("conversation.instructions", object{"turnId": "turn_a", "path": "AGENTS.md", "content": nil, "changed": true}),
		event("turn.finished", object{"turnId": "turn_a", "outcome": "completed", "error": nil}),
		event("turn.started", object{"turnId": "turn_b", "model": "fixture"}),
		event("turn.step", object{"turnId": "turn_b", "number": 1, "text": "done", "reasoning": []any{}, "toolCalls": []any{}, "finish": "stop", "usage": object{"input": 2, "output": 1}, "timing": timing, "input": input, "sawSeq": 14}),
		event("turn.finished", object{"turnId": "turn_b", "outcome": "completed", "error": nil}),
	}
}
func TestTraceTimelineHTTP(t *testing.T) {
	no := false
	requests := []apiRequest{{Method: "GET", Path: "/api/conversations/coo_a/trace", Auth: &no}, {Method: "GET", Path: "/api/conversations/coo_a/trace"}, {Method: "GET", Path: "/api/conversations/coo_a/trace?before=15"}, {Method: "GET", Path: "/api/conversations/coo_a/trace?before=0"}, {Method: "GET", Path: "/api/conversations/coo_a/trace?before=1.5"}, {Method: "GET", Path: "/api/conversations/coo_a/trace?before=Infinity"}, {Method: "GET", Path: "/api/conversations/missing/trace"}}
	now := func() time.Time { return time.UnixMilli(1_800_000_000_000) }
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(t.TempDir(), "trace.sqlite"), Now: now})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if _, err := repo.Store.Append(t.Context(), traceSeed()); err != nil {
		t.Fatal(err)
	}
	api, err := New(Options{Repository: repo, Token: "test-token", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	for _, item := range requests {
		r := httptest.NewRequest(item.Method, item.Path, nil)
		if item.Auth == nil || *item.Auth {
			r.Header.Set("Authorization", "Bearer test-token")
		}
		response := httptest.NewRecorder()
		api.ServeHTTP(response, r)
		var body any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err, response.Body.String())
		}
		if response.Code >= 500 {
			t.Fatal(response.Body.String())
		}
	}
	for _, limits := range []core.TurnEventOptions{{MaxEvents: 1}, {MaxBytes: 1}} {
		if _, err := repo.ListTurnEvents(t.Context(), "coo_a", limits); err == nil {
			t.Fatal("trace bounds ignored")
		}
	}

	full := func(query string) []any {
		t.Helper()
		r := httptest.NewRequest("GET", "/api/conversations/coo_a/trace"+query, nil)
		r.Header.Set("Authorization", "Bearer test-token")
		response := httptest.NewRecorder()
		api.ServeHTTP(response, r)
		var body struct {
			Turns []any `json:"turns"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != 200 {
			t.Fatal(response.Code, response.Body.String(), err)
		}
		return body.Turns
	}
	turns := full("")
	if len(turns) != 2 {
		t.Fatal(turns)
	}
	last := turns[1].(map[string]any)
	suffix := full("?from=" + strconv.FormatInt(int64(last["seq"].(float64)), 10))
	if len(suffix) != 1 || encoded(suffix[0]) != encoded(last) {
		t.Fatalf("incremental page changed the turn or request numbers: %+v", suffix)
	}
}
