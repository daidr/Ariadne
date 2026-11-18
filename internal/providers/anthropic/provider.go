// Package anthropic implements Messages streaming, including signed thinking
// replay and the four existing prompt cache points.
package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"iter"
	"net/http"
	"strings"
	"time"

	"github.com/daidr/Ariadne/internal/modelhttp"
	"github.com/daidr/Ariadne/sdk"
)

const APIVersion = "2023-06-01"
const DefaultMaxTokens = 8192

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

type block = map[string]any
type WireMessage struct {
	Role    string  `json:"role"`
	Content []block `json:"content"`
}

func imageBlock(image sdk.Image) block {
	return block{"type": "image", "source": block{"type": "base64", "media_type": image.MIME, "data": image.Data}}
}
func textImages(text string, images []sdk.Image) []block {
	parts := []block{}
	if text != "" {
		parts = append(parts, block{"type": "text", "text": text})
	}
	for _, image := range images {
		parts = append(parts, imageBlock(image))
	}
	return parts
}
func ToWire(messages []sdk.Message) (string, []WireMessage) {
	system := []string{}
	wire := []WireMessage{}
	push := func(role string, blocks []block) {
		if len(blocks) == 0 {
			return
		}
		if len(wire) > 0 && wire[len(wire)-1].Role == role {
			wire[len(wire)-1].Content = append(wire[len(wire)-1].Content, blocks...)
		} else {
			wire = append(wire, WireMessage{Role: role, Content: blocks})
		}
	}
	for _, message := range messages {
		switch message.Role {
		case "system":
			system = append(system, message.Content)
		case "user":
			push("user", textImages(message.Content, message.Images))
		case "assistant":
			blocks := []block{}
			for _, thought := range message.Reasoning {
				if thought.Redacted {
					blocks = append(blocks, block{"type": "redacted_thinking", "data": thought.Text})
				} else if thought.Signature != "" {
					blocks = append(blocks, block{"type": "thinking", "thinking": thought.Text, "signature": thought.Signature})
				}
			}
			if message.Content != "" {
				blocks = append(blocks, block{"type": "text", "text": message.Content})
			}
			for _, call := range message.ToolCalls {
				input := map[string]any{}
				if json.Unmarshal([]byte(call.Arguments), &input) != nil || input == nil {
					input = map[string]any{}
				}
				blocks = append(blocks, block{"type": "tool_use", "id": call.ID, "name": call.Name, "input": input})
			}
			push("assistant", blocks)
		case "tool":
			var content any = message.Content
			if len(message.Images) > 0 {
				content = textImages(message.Content, message.Images)
			}
			result := block{"type": "tool_result", "tool_use_id": message.ToolCallID, "content": content}
			if message.IsError {
				result["is_error"] = true
			}
			push("user", []block{result})
		}
	}
	return strings.Join(system, "\n\n"), wire
}
func markLast(message *WireMessage) {
	for i := len(message.Content) - 1; i >= 0; i-- {
		part := message.Content[i]
		if part["type"] != "thinking" && part["type"] != "redacted_thinking" {
			part["cache_control"] = block{"type": "ephemeral"}
			return
		}
	}
}
func RequestBody(_ Settings, request sdk.Request) map[string]any {
	system, messages := ToWire(request.Messages)
	cache := request.Cache == nil || *request.Cache
	if cache && len(messages) > 0 {
		markLast(&messages[len(messages)-1])
		for i := len(messages) - 2; i >= 0; i-- {
			if messages[i].Role == "user" {
				markLast(&messages[i])
				break
			}
		}
	}
	maxTokens := request.MaxTokens
	if maxTokens == 0 {
		maxTokens = DefaultMaxTokens
	}
	body := block{"model": request.Model, "max_tokens": maxTokens, "messages": messages, "stream": true}
	if request.Effort != "" {
		body["thinking"] = block{"type": "adaptive", "display": "summarized"}
		body["output_config"] = block{"effort": request.Effort}
	}
	if system != "" {
		if cache {
			body["system"] = []block{{"type": "text", "text": system, "cache_control": block{"type": "ephemeral"}}}
		} else {
			body["system"] = system
		}
	}
	tools := []block{}
	for i, tool := range request.Tools {
		item := block{"name": tool.Name, "description": tool.Description, "input_schema": tool.Parameters}
		if cache && i == len(request.Tools)-1 {
			item["cache_control"] = block{"type": "ephemeral"}
		}
		tools = append(tools, item)
	}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	return sdk.WithExtraBody(body, request.ExtraBody)
}
func (provider Provider) newRequest(ctx context.Context, settings Settings, method, path string, body []byte, headers map[string]string) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(settings.BaseURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("anthropic-version", APIVersion)
	request.Header.Set("x-api-key", settings.APIKey)
	return request, nil
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
		httpRequest, err := provider.newRequest(ctx, settings, http.MethodPost, "/v1/messages", body, request.Headers)
		if err != nil {
			yield(sdk.Part{Type: "finish", Reason: "error", Error: err.Error()})
			return
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
