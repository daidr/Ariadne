package modelstep

import (
	"encoding/json"
	"github.com/daidr/Ariadne/internal/legacytest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/daidr/Ariadne/sdk"
)

func TestRequestCollectionAndFailureMatchLegacy(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		if os.Getenv("ARIADNE_REQUIRE_LEGACY") == "1" {
			t.Fatal(err)
		}
		t.Skip("Bun unavailable")
	}
	usage := sdk.Part{Type: "usage", Input: new(int64(100)), Output: new(int64(20)), Cached: new(int64(40)), CacheWrite: new(int64(10)), ContextInput: new(int64(0)), Reasoning: new(int64(0))}
	cases := [][][]sdk.Part{
		{{{Type: "text", Text: "discard"}, {Type: "finish", Reason: "error", Error: "busy", Retryable: true, Busy: true, RetryAfterMS: new(int64(0))}}, {{Type: "reasoning", Text: "thinking", StartedAt: 50}, {Type: "text", Text: "answer"}, {Type: "tool-input", Call: new(0), Name: "send_message", Arguments: "{}"}, {Type: "tool-call", ID: "call", Name: "send_message", Arguments: "{}"}, usage, {Type: "finish", Reason: "tool-calls"}}},
		{{{Type: "tool-call", ID: "cut", Name: "read_file", Arguments: "{"}, {Type: "finish", Reason: "length"}}},
		{{{Type: "finish", Reason: "error", Error: "busy", Retryable: true, Busy: true}}},
		{{{Type: "finish", Reason: "error", Error: "wrong key", Retryable: false}}},
	}
	for i, scripts := range cases {
		retries := []Retry{}
		inputs := []any{}
		resets := 0
		request := sdk.Request{}
		images := i == 3
		if images {
			request.Messages = []sdk.Message{{Role: "user", Images: []sdk.Image{{MIME: "image/png"}}}}
		}
		retryCount := 1
		result, err := Request(t.Context(), scripted(scripts...), nil, request, Options{Retries: &retryCount, BaseDelay: new(time.Duration(0)), Now: func() time.Time { return time.UnixMilli(100) }, OnRetry: func(retry Retry) error { retries = append(retries, retry); return nil }, OnInput: func(call *int, name, args string) { inputs = append(inputs, []any{call, name, args}) }, Reset: func() { resets++ }})
		var outcome any = result
		if err != nil {
			failure, ok := err.(*Failure)
			if !ok {
				t.Fatal(err)
			}
			var exhausted any
			if failure.Exhausted != nil {
				exhausted = map[string]any{"detail": *failure.Exhausted}
			}
			outcome = map[string]any{"error": failure.Localized, "exhausted": exhausted}
		}
		priced := WithCost(UsageOf(usage), &sdk.ModelPrice{Currency: "CNY", Input: 2, Output: 4, CacheRead: new(.5), CacheWrite: new(float64(3))})
		native := map[string]any{"result": outcome, "retries": retries, "inputs": inputs, "resets": resets, "usage": UsageOf(usage), "priced": priced}
		input, _ := json.Marshal(map[string]any{"scripts": scripts, "retries": retryCount, "images": images, "usage": usage})
		cmd := legacytest.Command(t, bun, "packages/agent/src/testing/go-model-step-fixture.ts")
		cmd.Dir = filepath.Join("..", "..")
		cmd.Stdin = strings.NewReader(string(input))
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatal(err, string(output))
		}
		data, _ := json.Marshal(native)
		var actual, want map[string]any
		if json.Unmarshal(data, &actual) != nil || json.Unmarshal(output, &want) != nil {
			t.Fatal("invalid JSON", string(output))
		}
		if !reflect.DeepEqual(actual, want) {
			t.Fatalf("case %d\nGo %s\nTS %s", i, data, output)
		}
	}
}
