package modelstep

import (
	"context"
	"errors"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/sdk"
)

type Summary struct {
	OK        bool             `json:"ok"`
	Text      string           `json:"text,omitempty"`
	Reasoning []sdk.Reasoning  `json:"reasoning,omitempty"`
	Usage     *core.TokenUsage `json:"usage,omitempty"`
	Timing    core.CallTiming  `json:"timing,omitempty"`
	Detail    string           `json:"detail,omitempty"`
}

// Summarise uses an independent, uncached request. A failed or empty response
// leaves the original history intact; only a complete summary may replace it.
func Summarise(ctx context.Context, provider sdk.ModelProvider, settings map[string]any, request sdk.Request, options Options) (Summary, error) {
	request.Tools = []sdk.Tool{}
	request.Cache = new(false)
	request.Conversation = ""
	options.Retries = new(0)
	options.OnInput = nil
	options.Reset = nil
	options.OnRetry = nil
	options.summary = true
	response, err := Request(ctx, provider, settings, request, options)
	if ctx.Err() != nil {
		return Summary{}, ctx.Err()
	}
	if err != nil {
		var failure *Failure
		if errors.As(err, &failure) {
			detail, _ := failure.Localized.Params["detail"].(string)
			return Summary{Detail: detail}, nil
		}
		return Summary{Detail: err.Error()}, nil
	}
	if response.Cut {
		return Summary{Detail: "the summary was cut off"}, nil
	}
	text := textutil.Trim(response.Text)
	if text == "" {
		return Summary{Detail: "empty summary"}, nil
	}
	return Summary{OK: true, Text: text, Reasoning: response.Reasoning, Usage: response.Usage, Timing: response.Timing}, nil
}
