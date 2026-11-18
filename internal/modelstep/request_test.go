package modelstep

import (
	"context"
	"errors"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/daidr/Ariadne/sdk"
)

func scripted(scripts ...[]sdk.Part) sdk.ModelProvider {
	next := 0
	return sdk.ModelProvider{Stream: func(ctx context.Context, _ map[string]any, _ sdk.Request) iter.Seq[sdk.Part] {
		return func(yield func(sdk.Part) bool) {
			at := min(next, len(scripts)-1)
			next++
			for _, part := range scripts[at] {
				if ctx.Err() != nil || !yield(part) {
					return
				}
			}
		}
	}}
}
func TestRetryDropsFailedOutputAndKeepsFinalUsage(t *testing.T) {
	provider := scripted(
		[]sdk.Part{{Type: "text", Text: "discard me"}, {Type: "tool-input", Name: "send_message", Arguments: `{"content":"preview`}, {Type: "finish", Reason: "error", Error: "busy", Busy: true, Retryable: true, RetryAfterMS: new(int64(0))}},
		[]sdk.Part{{Type: "reasoning", Text: "thinking", StartedAt: 50}, {Type: "text", Text: "answer"}, {Type: "tool-call", ID: "call", Name: "read_file", Arguments: "{}"}, {Type: "usage", Input: new(int64(100)), Output: new(int64(10)), ContextInput: new(int64(0)), Cached: new(int64(20)), CostUSD: new(float64(0))}, {Type: "finish", Reason: "tool-calls"}},
	)
	retries, inputs, resets := []Retry{}, 0, 0
	result, err := Request(t.Context(), provider, nil, sdk.Request{}, Options{Now: func() time.Time { return time.UnixMilli(100) }, OnRetry: func(retry Retry) error { retries = append(retries, retry); return nil }, OnInput: func(*int, string, string) { inputs++ }, Reset: func() { resets++ }})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "answer" || len(result.ToolCalls) != 1 || result.Timing.StartedAt != 100 || result.Timing.FirstOutputAt == nil || *result.Timing.FirstOutputAt != 50 {
		t.Fatal("wrong final attempt", result)
	}
	if len(retries) != 1 || retries[0].Attempt != 1 || retries[0].DelayMS != 0 || inputs != 1 || resets != 1 {
		t.Fatal("retry callbacks", retries, inputs, resets)
	}
	if result.Usage == nil || result.Usage.Cached == nil || *result.Usage.Cached != 20 || result.Usage.Cost == nil || result.Usage.Cost.Amount != 0 || result.Usage.ContextInput == nil {
		t.Fatal("usage lost", result.Usage)
	}
}
func TestOnlyBusyFailureAllowsFallbackAndImagesAreLocalized(t *testing.T) {
	for _, busy := range []bool{false, true} {
		provider := scripted([]sdk.Part{{Type: "finish", Reason: "error", Error: strings.Repeat("detail", 200), Busy: busy, Retryable: true}})
		result, err := Request(t.Context(), provider, nil, sdk.Request{Messages: []sdk.Message{{Role: "user", Images: []sdk.Image{{MIME: "image/png"}}}}}, Options{Retries: new(0)})
		var failure *Failure
		if result != nil || !errors.As(err, &failure) || failure.Localized.Key != "error.modelRequestImages" || (failure.Exhausted != nil) != busy {
			t.Fatal("wrong terminal error", err)
		}
		if len(failure.Localized.Params["detail"].(string)) != 500 {
			t.Fatal("error detail not bounded")
		}
	}
}
func TestLengthLimitDropsIncompleteCallAndRetractsPreview(t *testing.T) {
	provider := scripted([]sdk.Part{{Type: "tool-call", ID: "good", Name: "read_file", Arguments: "{}"}, {Type: "tool-call", ID: "cut", Name: "write_file", Arguments: "{"}, {Type: "finish", Reason: "length"}})
	resets := 0
	result, err := Request(t.Context(), provider, nil, sdk.Request{}, Options{Reset: func() { resets++ }})
	if err != nil || !result.Cut || len(result.ToolCalls) != 1 || result.ToolCalls[0].ID != "good" || resets != 1 {
		t.Fatal("incomplete call retained", result, err)
	}
	all := result.ToolCalls[:cap(result.ToolCalls)]
	if len(all) > 1 && all[1].Arguments != "" {
		t.Fatal("removed arguments still retained")
	}
}
func TestWatchdogCancelsEachSilentAttemptAndCancellationStopsRetryWait(t *testing.T) {
	started, stopped := 0, 0
	provider := sdk.ModelProvider{Stream: func(ctx context.Context, _ map[string]any, _ sdk.Request) iter.Seq[sdk.Part] {
		return func(func(sdk.Part) bool) { started++; defer func() { stopped++ }(); <-ctx.Done() }
	}}
	retries := 0
	_, err := Request(t.Context(), provider, nil, sdk.Request{}, Options{Retries: new(1), BaseDelay: new(time.Duration(0)), Timeout: 20 * time.Millisecond, OnRetry: func(Retry) error { retries++; return nil }})
	var failure *Failure
	if !errors.As(err, &failure) || started != 2 || stopped != 2 || retries != 1 || failure.Exhausted != nil {
		t.Fatal("silent request retained", err, started, stopped, retries)
	}
	ctx, cancel := context.WithCancel(t.Context())
	provider = scripted([]sdk.Part{{Type: "finish", Reason: "error", Error: "busy", Retryable: true, Busy: true, RetryAfterMS: new(int64(60000))}})
	_, err = Request(ctx, provider, nil, sdk.Request{}, Options{OnRetry: func(Retry) error { cancel(); return nil }})
	if !errors.Is(err, context.Canceled) {
		t.Fatal("retry wait ignored cancellation", err)
	}
}
func TestActivityExtendsWatchdogAndFinishedRequestStopsTimer(t *testing.T) {
	var retained context.Context
	provider := sdk.ModelProvider{Stream: func(ctx context.Context, _ map[string]any, _ sdk.Request) iter.Seq[sdk.Part] {
		retained = ctx
		return func(yield func(sdk.Part) bool) {
			for range 5 {
				timer := time.NewTimer(15 * time.Millisecond)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
				if !yield(sdk.Part{Type: "text", Text: "x"}) {
					return
				}
			}
		}
	}}
	result, err := Request(t.Context(), provider, nil, sdk.Request{}, Options{Timeout: 80 * time.Millisecond})
	if err != nil || result.Text != "xxxxx" {
		t.Fatal("active stream timed out", err)
	}
	if retained.Err() == nil || errors.Is(context.Cause(retained), errSilent) {
		t.Fatal("attempt lifetime not closed after completion")
	}
}
func TestResponseCapsStopProducerAndPanicIsContained(t *testing.T) {
	for _, part := range []sdk.Part{{Type: "text", Text: "12345"}, {Type: "reasoning", Text: ""}, {Type: "tool-call", ID: "a", Name: "tool", Arguments: "{}"}} {
		count := 0
		provider := sdk.ModelProvider{Stream: func(ctx context.Context, _ map[string]any, _ sdk.Request) iter.Seq[sdk.Part] {
			return func(yield func(sdk.Part) bool) {
				for {
					count++
					if ctx.Err() != nil || !yield(part) {
						return
					}
				}
			}
		}}
		_, err := Request(t.Context(), provider, nil, sdk.Request{}, Options{MaxOutputBytes: 20})
		if err == nil || count > maxBlocks+1 {
			t.Fatal("unbounded model response", err, count)
		}
	}
	provider := sdk.ModelProvider{Stream: func(context.Context, map[string]any, sdk.Request) iter.Seq[sdk.Part] { panic("provider failed") }}
	if _, err := Request(t.Context(), provider, nil, sdk.Request{}, Options{}); err == nil {
		t.Fatal("provider panic escaped")
	}
}
func TestUsageAndCostPreserveReportedCostAndCountCachedTokens(t *testing.T) {
	usage := UsageOf(sdk.Part{Input: new(int64(100)), Output: new(int64(20)), Cached: new(int64(40)), CacheWrite: new(int64(10)), ContextInput: new(int64(0)), Reasoning: new(int64(0))})
	price := &sdk.ModelPrice{Currency: "CNY", Input: 2, Output: 4, CacheRead: new(.5), CacheWrite: new(float64(3))}
	priced := WithCost(usage, price)
	if priced.Cost.Amount != .00023 || priced.Cost.Currency != "CNY" || usage.Cost != nil || usage.Reasoning != nil {
		t.Fatal("wrong estimated usage", priced, usage)
	}
	if WithCost(priced, price) != priced || WithCost(nil, price) != nil || WithCost(usage, nil) != usage {
		t.Fatal("reported or missing price changed")
	}
}
