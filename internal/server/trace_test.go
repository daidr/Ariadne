package server

import (
	"encoding/json"
	"github.com/daidr/Ariadne/internal/legacytest"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
func TestTraceTimelineHTTPMatchesLegacy(t *testing.T) {
	no := false
	requests := []apiRequest{{Method: "GET", Path: "/api/conversations/coo_a/trace", Auth: &no}, {Method: "GET", Path: "/api/conversations/coo_a/trace"}, {Method: "GET", Path: "/api/conversations/coo_a/trace?before=15"}, {Method: "GET", Path: "/api/conversations/coo_a/trace?before=0"}, {Method: "GET", Path: "/api/conversations/coo_a/trace?before=1.5"}, {Method: "GET", Path: "/api/conversations/coo_a/trace?before=Infinity"}, {Method: "GET", Path: "/api/conversations/missing/trace"}}
	var expected []any
	if bun, err := exec.LookPath("bun"); err == nil {
		cmd := legacytest.Command(t, bun, "packages/server/src/testing/go-api-fixture.ts")
		cmd.Dir = filepath.Join("..", "..")
		cmd.Stdin = strings.NewReader(encoded(object{"seed": traceSeed(), "sql": "SELECT 1", "requests": requests}))
		data, err := cmd.CombinedOutput()
		if err != nil || json.Unmarshal(data, &expected) != nil {
			t.Fatalf("legacy %v %s", err, data)
		}
	} else if os.Getenv("ARIADNE_REQUIRE_LEGACY") == "1" {
		t.Fatal(err)
	}
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
	for i, item := range requests {
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
		actual := object{"status": response.Code, "body": body}
		if expected != nil && encoded(canonical(actual)) != encoded(canonical(expected[i])) {
			t.Fatalf("%s\nGo %s\nTS %s", item.Path, encoded(actual), encoded(expected[i]))
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
}
