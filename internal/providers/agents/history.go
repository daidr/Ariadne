package agents

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode/utf16"

	"github.com/daidr/Ariadne/internal/jsonutil"
	"github.com/daidr/Ariadne/sdk"
)

type Input struct {
	Text   string      `json:"text"`
	Images []sdk.Image `json:"images"`
}

func hash(text string) string {
	digest := sha256.Sum256([]byte(text))
	return hex.EncodeToString(digest[:])[:16]
}
func Fingerprint(message sdk.Message) string {
	switch message.Role {
	case "system":
		return "s"
	case "user":
		units := utf16.Encode([]rune(message.Content))
		return "u:" + hash(string(utf16.Decode(units[:min(400, len(units))])))
	case "assistant":
		ids := []string{}
		for _, call := range message.ToolCalls {
			ids = append(ids, call.ID)
		}
		return "a:" + strings.Join(ids, ",")
	case "tool":
		return "t:" + message.ToolCallID
	}
	return ""
}
func NativeFingerprint(message sdk.Message) string {
	if message.Role == "system" {
		return "s"
	}
	images := message.Images
	if images == nil {
		images = []sdk.Image{}
	}
	var value any
	switch message.Role {
	case "assistant":
		calls := message.ToolCalls
		if calls == nil {
			calls = []sdk.ToolCall{}
		}
		value = []any{message.Role, message.Content, calls}
	case "tool":
		value = []any{message.Role, message.ToolCallID, message.Content, message.IsError, images}
	default:
		value = []any{message.Role, message.Content, images}
	}
	data, _ := jsonutil.Encode(value)
	return hash(string(data))
}
func SystemHash(system string) string { return hash(system) }
func UpdatedInstructions(system string) string {
	return "<updated_instructions>\nYour instructions have changed. From now on they are:\n\n" + system + "\n</updated_instructions>"
}
func RenderHistory(messages []sdk.Message) Input {
	if len(messages) == 1 && messages[0].Role == "user" {
		images := messages[0].Images
		if images == nil {
			images = []sdk.Image{}
		}
		return Input{Text: messages[0].Content, Images: images}
	}
	images := []sdk.Image{}
	names := map[string]string{}
	parts := []string{}
	for _, message := range messages {
		switch message.Role {
		case "user":
			images = append(images, message.Images...)
			parts = append(parts, "<user>\n"+message.Content+"\n</user>")
		case "assistant":
			content := []string{}
			if message.Content != "" {
				content = append(content, message.Content)
			}
			for _, call := range message.ToolCalls {
				names[call.ID] = call.Name
				content = append(content, `<call id="`+call.ID+`" name="`+call.Name+`">`+call.Arguments+`</call>`)
			}
			parts = append(parts, "<assistant>\n"+strings.Join(content, "\n")+"\n</assistant>")
		case "tool":
			images = append(images, message.Images...)
			errorPart := ""
			if message.IsError {
				errorPart = ` error="true"`
			}
			parts = append(parts, `<result id="`+message.ToolCallID+`" name="`+names[message.ToolCallID]+`"`+errorPart+">\n"+message.Content+"\n</result>")
		}
	}
	ending := "Go on from where it ends; the calls above have already run."
	if len(messages) > 0 && messages[len(messages)-1].Role == "user" {
		ending = "Answer the last user message."
	}
	return Input{Text: "<history>\nThe conversation so far, from before this session started. Your earlier replies and tool calls are shown as they were; call the tools again yourself when you need them.\n\n" + strings.Join(parts, "\n\n") + "\n</history>\n\n" + ending, Images: images}
}
func JoinInput(messages []sdk.Message, prefix *string) Input {
	texts := []string{}
	if prefix != nil && *prefix != "" {
		texts = append(texts, *prefix)
	}
	images := []sdk.Image{}
	for _, message := range messages {
		if message.Role == "user" {
			texts = append(texts, message.Content)
			images = append(images, message.Images...)
		}
	}
	return Input{Text: strings.Join(texts, "\n\n"), Images: images}
}
