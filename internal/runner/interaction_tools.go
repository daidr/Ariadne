package runner

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/daidr/Ariadne/internal/core"
)

func uiBlocks() args {
	object := func(fields args, required ...string) args {
		return args{"type": "object", "properties": fields, "required": required}
	}
	enum := func(values ...string) args { return args{"type": "string", "enum": values} }
	block := func(kind string, fields args, required ...string) args {
		fields["type"] = args{"type": "string", "const": kind}
		return object(fields, append([]string{"type"}, required...)...)
	}
	text := field("string")
	boolean := field("boolean")
	number := field("number")
	row := field("object")
	return args{"type": "array", "minItems": 1, "maxItems": 6, "items": args{"anyOf": []args{
		block("choice", args{"question": text, "options": arrayOf(object(args{"label": text, "consequence": text}, "label", "consequence")),
			"recommended": field("integer")}, "question", "options"),
		block("confirm", args{"title": text, "description": text, "detail": text, "tone": enum("neutral", "warning", "danger"),
			"approveText": text, "denyText": text}, "title"),
		block("form", args{"title": text, "description": text, "submitText": text,
			"fields": arrayOf(object(args{"key": text, "label": text, "kind": enum("text", "textarea", "number", "select", "multiselect", "switch"),
				"required": boolean, "placeholder": text, "min": number, "max": number, "default": args{},
				"options": arrayOf(args{"anyOf": []args{text, object(args{"label": text, "value": text}, "label", "value")}})}, "key", "label", "kind"))}, "title", "fields"),
		block("table", args{"title": text, "columns": arrayOf(object(args{"key": text, "title": text, "align": enum("start", "end")}, "key", "title")),
			"rows": arrayOf(row)}, "columns", "rows"),
		block("chart", args{"title": text, "kind": enum("line", "area", "bar", "pie"), "x": text,
			"y": arrayOf(text), "labels": row, "rows": arrayOf(row), "stacked": boolean, "horizontal": boolean}, "kind", "x", "y", "rows"),
		block("steps", args{"title": text, "items": arrayOf(object(args{"title": text, "description": text,
			"status": enum("done", "current", "pending", "failed")}, "title"))}, "items"),
		block("thread", args{"threadId": text}, "threadId"),
	}}}
}

func (turn *turn) interactionTools() []tool {
	return []tool{
		specification("post_ui", "Post a message with a choice, confirmation, form, table, chart, steps or a link to a thread. One question per message. An answer arrives in a later turn. Ask when the user's intent is unclear or a decision has material consequences.",
			args{"text": field("string"), "blocks": uiBlocks()}, []string{"blocks"}, turn.postUI),
		specification("withdraw_ask", "Withdraw your own unanswered question when the user answered elsewhere or it no longer matters. Give a short reason for the user.",
			args{"attentionId": field("string"), "reason": field("string")}, []string{"attentionId", "reason"}, func(ctx context.Context, input args) (string, error) {
				return "Withdrawn.", turn.Repository.WithdrawAttention(ctx, turn.conversation.ID, text(input, "attentionId"), text(input, "reason"))
			}),
		specification("wait", "End the turn and wake once later for something that does not notify this conversation. Give a delay in minutes or a local time (14:30 or 2026-11-05 14:30) and a note. Use cancel to drop your existing wait.",
			args{"minutes": field("number"), "at": field("string"), "note": field("string"), "cancel": field("boolean")}, nil, turn.wait),
	}
}

func (turn *turn) postUI(ctx context.Context, input args) (string, error) {
	raw := input["text"]
	if _, ok := raw.(string); raw != nil && !ok {
		return "", errors.New("text must be a string")
	}
	blocks, ok := input["blocks"].([]any)
	if !ok || len(blocks) == 0 {
		return "", errors.New("blocks must be a nonempty array")
	}
	conversation := turn.conversation
	message, err := turn.Repository.PostMessage(ctx, conversation.ProjectID, core.MessageInput{
		ConversationID: conversation.ID, Author: conversation.Role, Content: text(input, "text"), Blocks: blocks,
	})
	if err != nil {
		return "", err
	}
	result := args{"messageId": message.ID, "status": "shown"}
	if attention := message.AttentionID(); attention != "" {
		result["attentionId"] = attention
		result["status"] = "asked; the answer will arrive later"
	}
	return reply(result, nil)
}

const maxWait = 7 * 24 * time.Hour

func waitUntil(input args, now time.Time) (time.Time, error) {
	minutes, haveMinutes := input["minutes"]
	at, haveAt := input["at"]
	if haveMinutes == haveAt {
		return time.Time{}, errors.New("give either minutes or at")
	}
	if haveMinutes {
		n, ok := minutes.(float64)
		if !ok || !(n >= 1 && n <= maxWait.Minutes()) {
			return time.Time{}, fmt.Errorf("minutes must be between 1 and %g", maxWait.Minutes())
		}
		return now.Add(time.Duration(math.Round(n*60000)) * time.Millisecond), nil
	}
	raw, ok := at.(string)
	if !ok {
		return time.Time{}, errors.New("at must be a local time")
	}
	raw = strings.Replace(strings.TrimSpace(raw), "T", " ", 1)
	local := now.Location()
	until, err := time.ParseInLocation("2006-01-02 15:04", raw, local)
	if err != nil {
		clock, err := time.ParseInLocation("15:04", raw, local)
		if err != nil {
			return time.Time{}, errors.New("at must be a local time like 14:30 or 2026-11-05 14:30")
		}
		until = time.Date(now.Year(), now.Month(), now.Day(), clock.Hour(), clock.Minute(), 0, 0, local)
		if !until.After(now) {
			until = until.AddDate(0, 0, 1)
		}
	}
	if !until.After(now) || until.Sub(now) > maxWait {
		return time.Time{}, errors.New("the wait must end within the next week")
	}
	return until, nil
}

func (turn *turn) wait(ctx context.Context, input args) (string, error) {
	id := turn.conversation.ID
	if cancel, _ := input["cancel"].(bool); cancel {
		ended, err := turn.Repository.EndWait(ctx, id, "cancelled", id, nil)
		if ended {
			return "Your wait is dropped.", err
		}
		return "You had no wait.", err
	}
	now := time.Now()
	until, err := waitUntil(input, now)
	if err != nil {
		return "", err
	}
	note := text(input, "note")
	if len(utf16.Encode([]rune(note))) > 500 || note == "" {
		return "", errors.New("note must say what to check in 500 characters or fewer")
	}
	_, err = turn.Repository.SetWait(ctx, id, until.UnixMilli(), note)
	return "You will wake at " + until.Format("2006-01-02 15:04") + " local time. End your turn now.", err
}
