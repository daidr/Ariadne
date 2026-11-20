// Package modelstep collects one bounded model response and retries only failures
// the provider marks retryable. Each attempt owns its cancellation and watchdog.
package modelstep

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/sdk"
)

const maxOutputBytes = 32 << 20
const maxBlocks = 4096
const maxCalls = 1024
const maxRetryDelay = time.Minute

var errSilent = errors.New("model response timed out")
var contextLimit = regexp.MustCompile(`(?i)context_length_exceeded|maximum context length|context (window|length) (is )?(exceeded|full|too (large|long))|input token count[^\n]*(exceeds|above) (the )?(maximum|limit)|prompt is too long`)

type Failure struct {
	Localized *domainerr.Error
	// ContextLimit requires reducing or rebuilding the request instead of backing off.
	ContextLimit bool
	// Exhausted is set only for a busy service, allowing the turn to use its
	// configured fallback. Bad credentials or invalid requests do not trigger it.
	Exhausted *string
}

func (failure *Failure) Error() string { return failure.Localized.Error() }
func (failure *Failure) Unwrap() error { return failure.Localized }

type Retry struct {
	Attempt int    `json:"attempt"`
	DelayMS int64  `json:"delayMs"`
	Detail  string `json:"detail"`
}
type Result struct {
	Text      string           `json:"text"`
	Reasoning []sdk.Reasoning  `json:"reasoning"`
	ToolCalls []sdk.ToolCall   `json:"toolCalls"`
	Usage     *core.TokenUsage `json:"usage"`
	Timing    core.CallTiming  `json:"timing"`
	Cut       bool             `json:"cut"`
}
type Options struct {
	Retries   *int
	BaseDelay *time.Duration
	Timeout   time.Duration
	OnRetry   func(Retry) error
	OnInput   func(call *int, name, arguments string)
	Reset     func()
	Now       func() time.Time
	// Lower limits are useful for embedding and tests; zero uses the hard cap.
	MaxOutputBytes int
	summary        bool
}

func UsageOf(part sdk.Part) *core.TokenUsage {
	value := func(pointer *int64) int64 {
		if pointer == nil {
			return 0
		}
		return *pointer
	}
	copyCount := func(pointer *int64) *int64 {
		if pointer == nil {
			return nil
		}
		return new(*pointer)
	}
	usage := &core.TokenUsage{Input: value(part.Input), Output: value(part.Output), ContextInput: copyCount(part.ContextInput)}
	if value(part.Cached) != 0 {
		usage.Cached = copyCount(part.Cached)
	}
	if value(part.CacheWrite) != 0 {
		usage.CacheWrite = copyCount(part.CacheWrite)
	}
	if value(part.Reasoning) != 0 {
		usage.Reasoning = copyCount(part.Reasoning)
	}
	if part.CostUSD != nil {
		usage.Cost = &core.Cost{Amount: *part.CostUSD, Currency: "USD"}
	}
	return usage
}
func WithCost(usage *core.TokenUsage, price *sdk.ModelPrice) *core.TokenUsage {
	if usage == nil || usage.Cost != nil || price == nil {
		return usage
	}
	value := func(pointer *int64) int64 {
		if pointer == nil {
			return 0
		}
		return *pointer
	}
	read, write := price.Input, price.Input
	if price.CacheRead != nil {
		read = *price.CacheRead
	}
	if price.CacheWrite != nil {
		write = *price.CacheWrite
	}
	cached, written := value(usage.Cached), value(usage.CacheWrite)
	fresh := max(int64(0), usage.Input-cached-written)
	out := *usage
	out.Cost = &core.Cost{Amount: (float64(fresh)*price.Input + float64(cached)*read + float64(written)*write + float64(usage.Output)*price.Output) / 1000000, Currency: price.Currency}
	return &out
}
func fail(detail string, busy bool, request sdk.Request) *Failure {
	tooLarge := contextLimit.MatchString(detail)
	detail = strings.Clone(textutil.HeadUnits(detail, 500))
	key := "error.modelRequest"
	for _, message := range request.Messages {
		if (message.Role == "user" || message.Role == "tool") && len(message.Images) > 0 {
			key = "error.modelRequestImages"
			break
		}
	}
	failure := &Failure{Localized: domainerr.New(key, map[string]any{"detail": detail}), ContextLimit: tooLarge}
	if busy && !tooLarge {
		failure.Exhausted = new(detail)
	}
	return failure
}
func reset(options Options) {
	if options.Reset != nil {
		options.Reset()
	}
}
func Request(ctx context.Context, provider sdk.ModelProvider, settings map[string]any, request sdk.Request, options Options) (*Result, error) {
	retries := 4
	if options.Retries != nil {
		retries = max(0, min(10, *options.Retries))
	}
	base := time.Second
	if options.BaseDelay != nil {
		base = max(time.Duration(0), min(maxRetryDelay, *options.BaseDelay))
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	limit := maxOutputBytes
	if options.MaxOutputBytes > 0 {
		limit = min(limit, options.MaxOutputBytes)
	}
	for attempt := 0; ; attempt++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		result, failure, err := collect(ctx, provider, settings, request, options, limit)
		if ctx.Err() != nil {
			reset(options)
			return nil, ctx.Err()
		}
		if err != nil {
			reset(options)
			return nil, err
		}
		if failure == nil {
			if result.Cut {
				reset(options)
			}
			return result, nil
		}
		reset(options)
		if !failure.Retryable || attempt >= retries || contextLimit.MatchString(failure.Error) {
			return nil, fail(failure.Error, failure.Busy, request)
		}
		delay := min(maxRetryDelay, base*time.Duration(1<<attempt))
		if failure.RetryAfterMS != nil {
			delay = time.Duration(max(int64(0), min(int64(maxRetryDelay/time.Millisecond), *failure.RetryAfterMS))) * time.Millisecond
		}
		if options.OnRetry != nil {
			if err := options.OnRetry(Retry{Attempt: attempt + 1, DelayMS: delay.Milliseconds(), Detail: strings.Clone(textutil.HeadUnits(failure.Error, 500))}); err != nil {
				return nil, err
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
func collect(ctx context.Context, provider sdk.ModelProvider, settings map[string]any, request sdk.Request, options Options, limit int) (result *Result, failure *sdk.Part, err error) {
	attempt, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	watch := newWatchdog(options.Timeout, cancel)
	defer watch.close()
	result = &Result{Reasoning: []sdk.Reasoning{}, ToolCalls: []sdk.ToolCall{}, Timing: core.CallTiming{StartedAt: options.Now().UnixMilli()}}
	defer func() {
		if recovered := recover(); recovered != nil {
			result = nil
			failure = nil
			err = fail(fmt.Sprint(recovered), false, request)
		}
		if errors.Is(context.Cause(attempt), errSilent) {
			result = nil
			failure = &sdk.Part{Type: "finish", Reason: "error", Error: fmt.Sprintf("no response for %g seconds", options.Timeout.Seconds()), Retryable: true}
			err = nil
		}
	}()
	if provider.Stream == nil {
		return nil, nil, fail("model provider has no stream", false, request)
	}
	var text strings.Builder
	last := ""
	size := 0
	for part := range provider.Stream(attempt, settings, request) {
		if attempt.Err() != nil {
			break
		}
		watch.touch()
		partSize := len(part.Text) + len(part.Signature) + len(part.ID) + len(part.Name) + len(part.Arguments) + len(part.Error)
		available := limit - size
		if part.Type == "tool-input" {
			available = limit
		}
		if partSize > available {
			return nil, nil, fail("model response exceeds size limit", false, request)
		}
		// Tool-input previews repeat the accumulated arguments; only the actual
		// call is retained. Still reject an individual oversized preview.
		if part.Type != "tool-input" {
			size += partSize
		}
		output := part.Type == "text" || part.Type == "reasoning" || part.Type == "tool-call"
		if options.summary {
			output = part.Type == "reasoning" || part.Type == "text" && part.Text != ""
		}
		if output {
			last = part.Type
			if result.Timing.FirstOutputAt == nil {
				at := options.Now().UnixMilli()
				if part.Type == "reasoning" && part.StartedAt != 0 {
					at = part.StartedAt
				}
				result.Timing.FirstOutputAt = &at
			}
		}
		switch part.Type {
		case "text":
			text.WriteString(part.Text)
		case "reasoning":
			if len(result.Reasoning) >= maxBlocks {
				return nil, nil, fail("too many reasoning blocks", false, request)
			}
			result.Reasoning = append(result.Reasoning, sdk.Reasoning{Text: part.Text, Signature: part.Signature, Redacted: part.Redacted})
		case "tool-call":
			if len(result.ToolCalls) >= maxCalls {
				return nil, nil, fail("too many tool calls", false, request)
			}
			result.ToolCalls = append(result.ToolCalls, sdk.ToolCall{ID: part.ID, Name: part.Name, Arguments: part.Arguments})
		case "tool-input":
			if options.OnInput != nil {
				options.OnInput(part.Call, part.Name, part.Arguments)
			}
		case "usage":
			result.Usage = UsageOf(part)
		case "finish":
			if part.Reason == "error" {
				return nil, &part, nil
			}
			if part.Reason == "length" {
				result.Cut = true
				if last == "tool-call" && len(result.ToolCalls) > 0 {
					result.ToolCalls[len(result.ToolCalls)-1] = sdk.ToolCall{}
					result.ToolCalls = result.ToolCalls[:len(result.ToolCalls)-1]
				}
			}
		}
	}
	result.Text = text.String()
	return result, nil, nil
}
