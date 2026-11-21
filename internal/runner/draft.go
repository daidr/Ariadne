package runner

import (
	"encoding/json"
	"strings"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/google/uuid"
)

func (turn *turn) clearDraft() {
	if turn.draft != nil {
		turn.draft.Withdraw()
	}
	turn.draft, turn.draftCall = nil, nil
	turn.draftTool = ""
}

func (turn *turn) preview(call *int, name, arguments string) {
	if turn.quiet || turn.Drafts == nil || !replyTool(name) || call == nil || len(arguments) > 256<<10 {
		return
	}
	if turn.draft != nil && (*turn.draftCall != *call || turn.draftTool != name) {
		return
	}
	input := prefixObject(arguments)
	content := textutil.ChatMarkdown(previewString(input["text"]))
	var blocks json.RawMessage
	if name == "post_ui" {
		blocks = previewBlocks(input)
	}
	if strings.TrimSpace(content) == "" && len(blocks) == 0 {
		return
	}
	if turn.draft == nil {
		id, err := uuid.NewV7()
		if err != nil {
			return
		}
		handle, err := turn.Drafts.Start(turn.conversation.ProjectID, turn.conversation.ID, "msg_"+id.String())
		if err != nil {
			return
		}
		turn.draft, turn.draftCall, turn.draftTool = &handle, new(*call), name
	}
	_ = turn.draft.WriteUI(content, blocks)
}

func replyTool(name string) bool {
	return name == "send_message" || name == "post_ui"
}

func previewBlocks(input args) json.RawMessage {
	var blocks []args
	raw, _ := input["blocks"].([]any)
	for _, value := range raw[:min(len(raw), 6)] {
		block, ok := value.(args)
		if !ok {
			continue
		}
		if block["type"] == "thread" {
			if _, complete := block["threadId"].(string); !complete {
				continue
			}
		}
		kind, _ := block["type"].(string)
		if kind == "" {
			continue
		}
		if valid, err := core.ParseBlock(previewValues(block)); err == nil {
			blocks = append(blocks, valid)
		} else if partial := blockPreview(block, kind); partial != nil {
			blocks = append(blocks, partial)
		}
	}
	ids, _ := input["threadIds"].([]any)
	for _, value := range ids[:min(len(ids), 6-len(blocks))] {
		if id, complete := value.(string); complete {
			if valid, err := core.ParseBlock(args{"type": "thread", "threadId": id}); err == nil {
				blocks = append(blocks, valid)
			}
		}
	}
	if len(blocks) == 0 {
		return nil
	}
	data, _ := json.Marshal(blocks)
	return data
}

// Only preview data goes through this conversion. Final tool arguments are
// decoded and validated independently before a message or attention is saved.
func previewValues(value any) any {
	switch value := value.(type) {
	case growingString:
		return string(value)
	case args:
		for key, item := range value {
			value[key] = previewValues(item)
		}
	case []any:
		for i, item := range value {
			value[i] = previewValues(item)
		}
	}
	return value
}

// While a block is still missing required fields, show its heading and the
// labels already written using the same read-only draft slot as valid blocks.
func blockPreview(block args, kind string) args {
	titleKey, itemKey, labelKey := "title", "", "label"
	switch kind {
	case "choice":
		titleKey, itemKey = "question", "options"
	case "form":
		itemKey = "fields"
	case "steps":
		itemKey, labelKey = "items", "title"
	case "table":
		itemKey, labelKey = "columns", "title"
	case "connector":
		titleKey = "name"
	case "chart", "confirm":
	default:
		return nil
	}
	title := textutil.HeadUnits(previewString(block[titleKey]), 2000)
	description := textutil.HeadUnits(previewString(block["description"]), 2000)
	var labels []string
	items, _ := block[itemKey].([]any)
	for _, item := range items[:min(12, len(items))] {
		object, _ := item.(args)
		if label := strings.TrimSpace(previewString(object[labelKey])); label != "" {
			labels = append(labels, textutil.HeadUnits(label, 200))
		}
	}
	if strings.TrimSpace(title) == "" && len(labels) == 0 {
		return nil
	}
	return args{"type": "preview", "title": title, "description": description, "labels": labels}
}
