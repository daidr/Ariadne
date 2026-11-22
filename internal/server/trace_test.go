package server

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/eventstore"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
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

func TestTraceTimelineRPC(t *testing.T) {
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
	srv := httptest.NewServer(api)
	defer srv.Close()

	unauth := ariadnev1connect.NewConversationServiceClient(srv.Client(), srv.URL+rpcPrefix)
	client := ariadnev1connect.NewConversationServiceClient(srv.Client(), srv.URL+rpcPrefix, connect.WithInterceptors(bearerClientInterceptor("test-token")))

	if _, err := unauth.GetTrace(t.Context(), connect.NewRequest(&ariadnev1.GetTraceRequest{ConversationId: "coo_a"})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("expected unauthenticated, got %v", err)
	}
	if _, err := client.GetTrace(t.Context(), connect.NewRequest(&ariadnev1.GetTraceRequest{ConversationId: "missing"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("expected not found, got %v", err)
	}
	zero := int64(0)
	if _, err := client.GetTrace(t.Context(), connect.NewRequest(&ariadnev1.GetTraceRequest{ConversationId: "coo_a", From: &zero})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected invalid argument for from=0, got %v", err)
	}
	before := int64(15)
	if _, err := client.GetTrace(t.Context(), connect.NewRequest(&ariadnev1.GetTraceRequest{ConversationId: "coo_a", Before: &before})); err != nil {
		t.Fatalf("before=15 failed: %v", err)
	}
	for _, limits := range []core.TurnEventOptions{{MaxEvents: 1}, {MaxBytes: 1}} {
		if _, err := repo.ListTurnEvents(t.Context(), "coo_a", limits); err == nil {
			t.Fatal("trace bounds ignored")
		}
	}

	fullResp, err := client.GetTrace(t.Context(), connect.NewRequest(&ariadnev1.GetTraceRequest{ConversationId: "coo_a"}))
	if err != nil {
		t.Fatal(err)
	}
	turns := fullResp.Msg.Turns
	if len(turns) != 2 {
		t.Fatal(turns)
	}
	var sawStepReq, sawResearchReq, sawSummaryReq bool
	for _, item := range turns[0].GetItems() {
		req := item.GetRequest()
		if req == nil {
			continue
		}
		if req.Provider == nil || *req.Provider != "openai-compatible" || req.Model == nil || *req.Model != "gpt-6.1" {
			t.Fatalf("expected provider=openai-compatible and model=gpt-6.1 on %s request, got provider=%v model=%v", req.GetPurpose(), req.Provider, req.Model)
		}
		switch req.GetPurpose() {
		case "step":
			sawStepReq = true
			for _, tc := range req.GetToolCalls() {
				for _, r := range tc.GetResearch() {
					sawResearchReq = true
					if r.Provider == nil || *r.Provider != "openai-compatible" || r.Model == nil || *r.Model != "gpt-6.1" {
						t.Fatalf("expected provider/model on research request, got provider=%v model=%v", r.Provider, r.Model)
					}
				}
			}
		case "summary":
			sawSummaryReq = true
		}
	}
	if !sawStepReq || !sawResearchReq || !sawSummaryReq {
		t.Fatalf("expected step/research/summary requests, got step=%v research=%v summary=%v", sawStepReq, sawResearchReq, sawSummaryReq)
	}
	last := turns[1]
	fromSeq := last.Seq
	suffixResp, err := client.GetTrace(t.Context(), connect.NewRequest(&ariadnev1.GetTraceRequest{ConversationId: "coo_a", From: &fromSeq}))
	if err != nil {
		t.Fatal(err)
	}
	if len(suffixResp.Msg.Turns) != 1 || !proto.Equal(suffixResp.Msg.Turns[0], last) {
		t.Fatalf("incremental page changed the turn or request numbers: got=%+v want=%+v", suffixResp.Msg.Turns, last)
	}

	// Verify TraceRequestFailure also preserves *string provider & model fields.
	failedEvents := []eventstore.NewEvent{
		{ProjectID: "prj_a", Type: "turn.started", Data: json.RawMessage(encoded(object{"conversationId": "coo_a", "turnId": "turn_fail", "model": "fixture"}))},
		{ProjectID: "prj_a", Type: "conversation.model_failed", Data: json.RawMessage(encoded(object{
			"conversationId": "coo_a",
			"turnId":         "turn_fail",
			"purpose":        "step",
			"input":          object{"call": "call"},
			"failure": object{
				"attempt":    1,
				"startedAt":  200,
				"finishedAt": 210,
				"detail":     "rate limited",
				"retryable":  true,
				"willRetry":  false,
			},
		}))},
		{ProjectID: "prj_a", Type: "turn.finished", Data: json.RawMessage(encoded(object{"conversationId": "coo_a", "turnId": "turn_fail", "outcome": "failed", "error": nil}))},
	}
	if _, err := repo.Store.Append(t.Context(), failedEvents); err != nil {
		t.Fatal(err)
	}
	failResp, err := client.GetTrace(t.Context(), connect.NewRequest(&ariadnev1.GetTraceRequest{ConversationId: "coo_a"}))
	if err != nil {
		t.Fatal(err)
	}
	var sawFailure bool
	for _, turn := range failResp.Msg.GetTurns() {
		if turn.GetTurnId() != "turn_fail" {
			continue
		}
		for _, item := range turn.GetItems() {
			if f := item.GetRequestFailed(); f != nil {
				sawFailure = true
				if f.Provider == nil || *f.Provider != "openai-compatible" || f.Model == nil || *f.Model != "gpt-6.1" {
					t.Fatalf("expected provider=openai-compatible and model=gpt-6.1 on failed request, got provider=%v model=%v", f.Provider, f.Model)
				}
			}
		}
	}
	if !sawFailure {
		t.Fatalf("expected request-failed item in turn_fail: %+v", failResp.Msg.GetTurns())
	}
}
