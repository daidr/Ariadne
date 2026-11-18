// Package transcript projects durable conversation records into the history
// sent to a model, keeping tool calls and their results together.
package transcript

import (
	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/sdk"
)

type Entry = map[string]any
type Attachment struct {
	FileID string `json:"fileId"`
	Name   string `json:"name"`
	MIME   string `json:"mime"`
	Size   int64  `json:"size"`
}
type Message struct {
	Role       string          `json:"role"`
	Content    string          `json:"content"`
	Images     []Attachment    `json:"images,omitempty"`
	ToolCalls  []sdk.ToolCall  `json:"toolCalls,omitempty"`
	Reasoning  []sdk.Reasoning `json:"reasoning,omitempty"`
	ToolCallID string          `json:"toolCallId,omitempty"`
	IsError    bool            `json:"isError,omitempty"`
}
type ChatOptions struct {
	Images        bool
	ReadText      func(string) (string, bool)
	KeepReasoning func(string) bool
	ClearUpTo     *float64
	PromptUpdates *bool
}

func obj(raw any) map[string]any {
	switch value := raw.(type) {
	case map[string]any:
		return value
	case *core.MessageView:
		return map[string]any{"id": value.ID, "conversationId": value.ConversationID, "author": value.Author, "kind": value.Kind, "content": value.Content, "attachments": value.Attachments, "mentions": value.Mentions, "comments": value.Comments, "cites": value.Cites, "createdAt": value.CreatedAt}
	}
	return map[string]any{}
}
func arr(raw any) []any {
	values, _ := raw.([]any)
	if values == nil {
		return []any{}
	}
	return values
}
func text(raw any) string { value, _ := raw.(string); return value }
func num(raw any) float64 {
	switch value := raw.(type) {
	case float64:
		return value
	case int:
		return float64(value)
	case int64:
		return float64(value)
	}
	return 0
}
func yes(raw any) bool {
	if raw == nil {
		return false
	}
	switch value := raw.(type) {
	case bool:
		return value
	case string:
		return value != ""
	case float64:
		return value != 0
	case int64:
		return value != 0
	case int:
		return value != 0
	}
	return true
}
func reasonings(raw any) []sdk.Reasoning {
	out := []sdk.Reasoning{}
	for _, value := range arr(raw) {
		block := obj(value)
		out = append(out, sdk.Reasoning{Text: text(block["text"]), Signature: text(block["signature"]), Redacted: yes(block["redacted"])})
	}
	return out
}
func calls(raw any) []sdk.ToolCall {
	out := []sdk.ToolCall{}
	for _, value := range arr(raw) {
		call := obj(value)
		out = append(out, sdk.ToolCall{ID: text(call["id"]), Name: text(call["name"]), Arguments: text(call["arguments"])})
	}
	return out
}
func attachments(raw any) []Attachment {
	out := []Attachment{}
	for _, value := range arr(raw) {
		file := obj(value)
		out = append(out, Attachment{FileID: text(file["fileId"]), Name: text(file["name"]), MIME: text(file["mime"]), Size: int64(num(file["size"]))})
	}
	return out
}
