package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/daidr/Ariadne/internal/modelhttp"
	"github.com/daidr/Ariadne/sdk"
)

type usage struct {
	Input      int64 `json:"input_tokens"`
	Output     int64 `json:"output_tokens"`
	CacheWrite int64 `json:"cache_creation_input_tokens"`
	Cached     int64 `json:"cache_read_input_tokens"`
}
type streamEvent struct {
	Type    string `json:"type"`
	Index   *int   `json:"index"`
	Message struct {
		Usage usage `json:"usage"`
	} `json:"message"`
	Block struct {
		Type string  `json:"type"`
		ID   *string `json:"id"`
		Name string  `json:"name"`
		Data string  `json:"data"`
	} `json:"content_block"`
	Delta struct {
		Type      string  `json:"type"`
		Text      string  `json:"text"`
		Thinking  string  `json:"thinking"`
		Signature string  `json:"signature"`
		JSON      string  `json:"partial_json"`
		Stop      *string `json:"stop_reason"`
	} `json:"delta"`
	Usage struct {
		Output *int64 `json:"output_tokens"`
	} `json:"usage"`
	Error struct {
		Type    string  `json:"type"`
		Message *string `json:"message"`
	} `json:"error"`
}

func nonzero(value int64) *int64 {
	if value == 0 {
		return nil
	}
	return &value
}

type toolBuffer struct {
	id, name  string
	arguments strings.Builder
}
type thoughtBuffer struct {
	text, signature strings.Builder
	startedAt       int64
	redacted        bool
}

func (provider Provider) readStream(ctx context.Context, response *http.Response, yield func(sdk.Part) bool) {
	blocks := map[int]*toolBuffer{}
	order := map[int]int{}
	thoughts := map[int]*thoughtBuffer{}
	var input, cached, cacheWrite, output int64
	stop := ""
	done, stopped := false, false
	emit := func(part sdk.Part) bool {
		if stopped || ctx.Err() != nil {
			stopped = true
			return false
		}
		if !yield(part) {
			stopped = true
			return false
		}
		return true
	}
	retained := 0
	err := modelhttp.ReadSSE(response.Body, func(sse modelhttp.Event) bool {
		if !emit(sdk.Part{Type: "alive"}) {
			return false
		}
		if sse.Comment {
			return true
		}
		var event streamEvent
		if json.Unmarshal([]byte(sse.Data), &event) != nil {
			return true
		}
		if event.Type == "content_block_start" || event.Type == "content_block_delta" {
			retained += len(event.Block.Data) + len(event.Block.Name) + len(event.Delta.Thinking) + len(event.Delta.Signature) + len(event.Delta.JSON)
			if retained > 64*1024*1024 || len(blocks)+len(thoughts) >= 1024 && event.Type == "content_block_start" {
				emit(sdk.Part{Type: "finish", Reason: "error", Error: "model response exceeds size limit"})
				stopped = true
				return false
			}
		}
		switch event.Type {
		case "message_start":
			usage := event.Message.Usage
			input = usage.Input + usage.CacheWrite + usage.Cached
			cached = usage.Cached
			cacheWrite = usage.CacheWrite
			output = usage.Output
		case "content_block_start":
			if event.Index == nil {
				break
			}
			index := *event.Index
			switch event.Block.Type {
			case "thinking":
				thoughts[index] = &thoughtBuffer{startedAt: provider.now().UnixMilli()}
			case "redacted_thinking":
				thought := &thoughtBuffer{redacted: true, startedAt: provider.now().UnixMilli()}
				thought.text.WriteString(event.Block.Data)
				thoughts[index] = thought
			case "tool_use":
				id := fmt.Sprintf("toolu_%d", index)
				if event.Block.ID != nil {
					id = *event.Block.ID
				}
				if _, ok := order[index]; !ok {
					order[index] = len(order)
				}
				blocks[index] = &toolBuffer{id: id, name: event.Block.Name}
			}
		case "content_block_delta":
			index := -1
			if event.Index != nil {
				index = *event.Index
			}
			switch event.Delta.Type {
			case "text_delta":
				if event.Delta.Text != "" {
					return emit(sdk.Part{Type: "text", Text: event.Delta.Text})
				}
			case "thinking_delta":
				if thought := thoughts[index]; event.Index != nil && thought != nil {
					thought.text.WriteString(event.Delta.Thinking)
				}
			case "signature_delta":
				if thought := thoughts[index]; event.Index != nil && thought != nil {
					thought.signature.WriteString(event.Delta.Signature)
				}
			case "input_json_delta":
				if call := blocks[index]; event.Index != nil && call != nil {
					call.arguments.WriteString(event.Delta.JSON)
					position := order[index]
					return emit(sdk.Part{Type: "tool-input", Call: &position, Name: call.name, Arguments: call.arguments.String()})
				}
			}
		case "content_block_stop":
			if event.Index == nil {
				break
			}
			index := *event.Index
			if thought := thoughts[index]; thought != nil {
				if !emit(sdk.Part{Type: "reasoning", Text: thought.text.String(), Signature: thought.signature.String(), StartedAt: thought.startedAt, Redacted: thought.redacted}) {
					return false
				}
			}
			if call := blocks[index]; call != nil {
				arguments := call.arguments.String()
				if arguments == "" {
					arguments = "{}"
				}
				return emit(sdk.Part{Type: "tool-call", ID: call.id, Name: call.name, Arguments: arguments})
			}
		case "message_delta":
			if event.Delta.Stop != nil {
				stop = *event.Delta.Stop
			}
			if event.Usage.Output != nil {
				output = *event.Usage.Output
			}
		case "message_stop":
			done = true
		case "error":
			message := sse.Data
			if event.Error.Message != nil {
				message = *event.Error.Message
			}
			passing := event.Error.Type == "overloaded_error" || event.Error.Type == "rate_limit_error" || event.Error.Type == "api_error"
			emit(sdk.Part{Type: "finish", Reason: "error", Error: message, Retryable: passing, Busy: passing})
			stopped = true
			return false
		}
		return true
	})
	if stopped || ctx.Err() != nil {
		return
	}
	if err != nil {
		if part := modelhttp.TransportError(ctx, err, true); part != nil {
			emit(*part)
		}
		return
	}
	if !done {
		emit(modelhttp.Incomplete())
		return
	}
	if !emit(sdk.Part{Type: "usage", Input: &input, Output: &output, Cached: nonzero(cached), CacheWrite: nonzero(cacheWrite)}) {
		return
	}
	reason := "stop"
	if stop == "tool_use" {
		reason = "tool-calls"
	} else if stop == "max_tokens" {
		reason = "length"
	}
	emit(sdk.Part{Type: "finish", Reason: reason})
}
