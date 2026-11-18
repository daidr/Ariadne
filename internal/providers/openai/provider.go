// Package openai implements the existing OpenAI-compatible chat protocol.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/daidr/Ariadne/internal/modelhttp"
	"github.com/daidr/Ariadne/sdk"
)

type Settings struct {
	BaseURL string `json:"baseUrl"`
	APIKey  string `json:"apiKey"`
}
type Provider struct {
	Client *http.Client
	Now    func() time.Time
}

func (provider Provider) client() *http.Client {
	if provider.Client != nil {
		return provider.Client
	}
	return http.DefaultClient
}
func (provider Provider) now() time.Time {
	if provider.Now != nil {
		return provider.Now()
	}
	return time.Now()
}

func imagePart(image sdk.Image) map[string]any {
	return map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:" + image.MIME + ";base64," + image.Data}}
}
func userContent(text string, images []sdk.Image) any {
	if len(images) == 0 {
		return text
	}
	parts := make([]any, 0, len(images)+1)
	if text != "" {
		parts = append(parts, map[string]any{"type": "text", "text": text})
	}
	for _, image := range images {
		parts = append(parts, imagePart(image))
	}
	return parts
}
func reasoningOf(message sdk.Message) string {
	var text strings.Builder
	if message.Role == "assistant" {
		for _, block := range message.Reasoning {
			if !block.Redacted {
				text.WriteString(block.Text)
			}
		}
	}
	return text.String()
}

func ToWire(messages []sdk.Message) []map[string]any {
	wire := make([]map[string]any, 0, len(messages))
	pending := make([]any, 0)
	flush := func() {
		if len(pending) > 0 {
			wire = append(wire, map[string]any{"role": "user", "content": pending})
			pending = make([]any, 0)
		}
	}
	thinks := false
	for _, message := range messages {
		if reasoningOf(message) != "" {
			thinks = true
			break
		}
	}
	for _, message := range messages {
		if message.Role != "tool" {
			flush()
		}
		switch message.Role {
		case "system":
			wire = append(wire, map[string]any{"role": "system", "content": message.Content})
		case "user":
			wire = append(wire, map[string]any{"role": "user", "content": userContent(message.Content, message.Images)})
		case "assistant":
			var content any
			if message.Content != "" {
				content = message.Content
			}
			item := map[string]any{"role": "assistant", "content": content}
			if thinks {
				item["reasoning_content"] = reasoningOf(message)
			}
			if len(message.ToolCalls) > 0 {
				calls := make([]any, 0, len(message.ToolCalls))
				for _, call := range message.ToolCalls {
					calls = append(calls, map[string]any{"id": call.ID, "type": "function", "function": map[string]any{"name": call.Name, "arguments": call.Arguments}})
				}
				item["tool_calls"] = calls
			}
			wire = append(wire, item)
		case "tool":
			wire = append(wire, map[string]any{"role": "tool", "tool_call_id": message.ToolCallID, "content": message.Content})
			if len(message.Images) > 0 {
				pending = append(pending, map[string]any{"type": "text", "text": "Images from the result of tool call " + message.ToolCallID + ":"})
				for _, image := range message.Images {
					pending = append(pending, imagePart(image))
				}
			}
		}
	}
	flush()
	return wire
}

func RequestBody(settings Settings, request sdk.Request) map[string]any {
	body := map[string]any{"model": request.Model, "messages": ToWire(request.Messages), "stream": true, "stream_options": map[string]any{"include_usage": true}}
	if len(request.Tools) > 0 {
		tools := make([]any, 0, len(request.Tools))
		for _, tool := range request.Tools {
			tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": tool.Name, "description": tool.Description, "parameters": tool.Parameters}})
		}
		body["tools"] = tools
	}
	if request.Effort != "" {
		body["reasoning_effort"] = request.Effort
	}
	if request.MaxTokens > 0 {
		field := "max_tokens"
		if address, err := url.Parse(settings.BaseURL); err == nil && strings.EqualFold(address.Hostname(), "api.openai.com") {
			field = "max_completion_tokens"
		}
		body[field] = request.MaxTokens
	}
	return sdk.WithExtraBody(body, request.ExtraBody)
}

func (provider Provider) Stream(ctx context.Context, settings Settings, request sdk.Request) iter.Seq[sdk.Part] {
	return func(yield func(sdk.Part) bool) {
		if ctx.Err() != nil {
			return
		}
		body, err := json.Marshal(RequestBody(settings, request))
		if err != nil {
			yield(sdk.Part{Type: "finish", Reason: "error", Error: err.Error()})
			return
		}
		httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(settings.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			yield(sdk.Part{Type: "finish", Reason: "error", Error: err.Error()})
			return
		}
		for key, value := range request.Headers {
			httpRequest.Header.Set(key, value)
		}
		httpRequest.Header.Set("Content-Type", "application/json")
		if settings.APIKey != "" {
			httpRequest.Header.Set("Authorization", "Bearer "+settings.APIKey)
		}
		response, err := provider.client().Do(httpRequest)
		if err != nil {
			if part := modelhttp.TransportError(ctx, err, false); part != nil {
				yield(*part)
			}
			return
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 || response.StatusCode == 204 || response.StatusCode == 205 {
			yield(modelhttp.HTTPError(response, provider.now()))
			return
		}
		provider.readStream(ctx, response, yield)
	}
}

type chunk struct {
	Choices []struct {
		Delta struct {
			Content          string  `json:"content"`
			ReasoningContent *string `json:"reasoning_content"`
			Reasoning        string  `json:"reasoning"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		Input         int64 `json:"prompt_tokens"`
		Output        int64 `json:"completion_tokens"`
		PromptDetails struct {
			Cached *int64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		CacheHits     *int64 `json:"prompt_cache_hit_tokens"`
		OutputDetails struct {
			Reasoning int64 `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func nonzero(value int64) *int64 {
	if value == 0 {
		return nil
	}
	return &value
}

type toolBuffer struct {
	id        string
	name      strings.Builder
	arguments strings.Builder
}

func (provider Provider) readStream(ctx context.Context, response *http.Response, yield func(sdk.Part) bool) {
	calls := make(map[int]*toolBuffer)
	order := make(map[int]int)
	var reason *string
	var reasoning strings.Builder
	var reasoningFrom int64
	done, stopped := false, false
	retained := 0
	emit := func(part sdk.Part) bool {
		if !yield(part) {
			stopped = true
			return false
		}
		return true
	}
	err := modelhttp.ReadSSE(response.Body, func(event modelhttp.Event) bool {
		if !emit(sdk.Part{Type: "alive"}) {
			return false
		}
		if event.Comment {
			return true
		}
		if event.Data == "[DONE]" {
			done = true
			return false
		}
		var chunk chunk
		if json.Unmarshal([]byte(event.Data), &chunk) != nil {
			return true
		}
		if chunk.Error != nil {
			message := chunk.Error.Message
			if message == "" {
				message = event.Data
			}
			emit(sdk.Part{Type: "finish", Reason: "error", Error: message})
			stopped = true
			return false
		}
		if usage := chunk.Usage; usage != nil {
			cached := usage.CacheHits
			if usage.PromptDetails.Cached != nil {
				cached = usage.PromptDetails.Cached
			}
			if cached != nil && *cached == 0 {
				cached = nil
			}
			if !emit(sdk.Part{Type: "usage", Input: &usage.Input, Output: &usage.Output, Cached: cached, Reasoning: nonzero(usage.OutputDetails.Reasoning)}) {
				return false
			}
		}
		if len(chunk.Choices) == 0 {
			return true
		}
		choice := chunk.Choices[0]
		delta := choice.Delta
		thought := delta.Reasoning
		if delta.ReasoningContent != nil {
			thought = *delta.ReasoningContent
		}
		retained += len(thought)
		for _, piece := range delta.ToolCalls {
			retained += len(piece.ID) + len(piece.Function.Name) + len(piece.Function.Arguments)
		}
		if retained > 64*1024*1024 {
			emit(sdk.Part{Type: "finish", Reason: "error", Error: "model response exceeds size limit"})
			stopped = true
			return false
		}
		if thought != "" && reasoning.Len() == 0 {
			reasoningFrom = provider.now().UnixMilli()
		}
		reasoning.WriteString(thought)
		if reasoning.Len() != 0 && (delta.Content != "" || len(delta.ToolCalls) > 0) {
			if !emit(sdk.Part{Type: "reasoning", Text: reasoning.String(), StartedAt: reasoningFrom}) {
				return false
			}
			reasoning.Reset()
		}
		if delta.Content != "" {
			if !emit(sdk.Part{Type: "text", Text: delta.Content}) {
				return false
			}
		}
		for _, piece := range delta.ToolCalls {
			call := calls[piece.Index]
			if call == nil {
				if len(calls) >= 1024 {
					emit(sdk.Part{Type: "finish", Reason: "error", Error: "model response exceeds size limit"})
					stopped = true
					return false
				}
				call = &toolBuffer{}
				calls[piece.Index] = call
				order[piece.Index] = len(order)
			}
			if piece.ID != "" {
				call.id = piece.ID
			}
			call.name.WriteString(piece.Function.Name)
			call.arguments.WriteString(piece.Function.Arguments)
			if piece.Function.Arguments != "" && call.name.Len() != 0 {
				index := order[piece.Index]
				if !emit(sdk.Part{Type: "tool-input", Call: &index, Name: call.name.String(), Arguments: call.arguments.String()}) {
					return false
				}
			}
		}
		if choice.FinishReason != nil && *choice.FinishReason != "" {
			reason = choice.FinishReason
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
	if !done && reason == nil {
		emit(modelhttp.Incomplete())
		return
	}
	if reasoning.Len() != 0 {
		if !emit(sdk.Part{Type: "reasoning", Text: reasoning.String(), StartedAt: reasoningFrom}) {
			return
		}
	}
	indices := make([]int, 0, len(calls))
	for index := range calls {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, index := range indices {
		call := calls[index]
		if call.id == "" {
			call.id = fmt.Sprintf("call_%d", index)
		}
		arguments := call.arguments.String()
		if arguments == "" {
			arguments = "{}"
		}
		if !emit(sdk.Part{Type: "tool-call", ID: call.id, Name: call.name.String(), Arguments: arguments}) {
			return
		}
	}
	finish := "stop"
	if reason != nil {
		switch *reason {
		case "length":
			finish = "length"
		case "tool_calls", "function_call":
			finish = "tool-calls"
		}
	}
	if len(calls) > 0 && finish != "length" {
		finish = "tool-calls"
	}
	emit(sdk.Part{Type: "finish", Reason: finish})
}
