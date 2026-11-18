package modelstep

import (
	"context"
	"iter"
	"testing"

	"github.com/daidr/Ariadne/sdk"
)

func TestSummaryCannotReplaceHistoryWithIncompleteOrFailedOutput(t *testing.T) {
	for _, parts := range [][]sdk.Part{
		{{Type: "text", Text: "partial"}, {Type: "finish", Reason: "length"}},
		{{Type: "text", Text: "partial"}, {Type: "finish", Reason: "error", Error: "busy", Retryable: true, Busy: true}},
		{{Type: "text", Text: "   "}},
	} {
		calls := 0
		provider := sdk.ModelProvider{Stream: func(_ context.Context, _ map[string]any, request sdk.Request) iter.Seq[sdk.Part] {
			calls++
			if request.Cache == nil || *request.Cache || len(request.Tools) != 0 || request.Conversation != "" || request.Model != "fixture" {
				t.Fatal("summary request carries turn/session context", request)
			}
			return func(yield func(sdk.Part) bool) {
				for _, part := range parts {
					if !yield(part) {
						return
					}
				}
			}
		}}
		summary, err := Summarise(t.Context(), provider, nil, sdk.Request{Model: "fixture", Conversation: "coo_a", Tools: []sdk.Tool{{Name: "should-not-run"}}}, Options{})
		if err != nil || summary.OK || summary.Detail == "" || calls != 1 {
			t.Fatal("incomplete summary accepted or retried", summary, err, calls)
		}
	}
	summary, err := Summarise(t.Context(), scripted([]sdk.Part{{Type: "text", Text: " \ufeff notes \n"}}), nil, sdk.Request{}, Options{})
	if err != nil || !summary.OK || summary.Text != "notes" {
		t.Fatal("complete summary rejected", summary, err)
	}
}
