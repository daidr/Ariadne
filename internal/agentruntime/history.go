package agentruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/daidr/Ariadne/internal/jsonutil"
	"github.com/daidr/Ariadne/sdk"
)

const (
	// DefaultHistoryReplayBytes bounds the rendered replay prompt text so
	// external CLI / ACP agents do not truncate the tail of an oversized single
	// prompt (which would drop the newest turns and the triggering user message).
	DefaultHistoryReplayBytes = 48 * 1024
	// MinHistoryReplayBytes guarantees a minimum budget for replayed history
	// even when system instructions alone exceed DefaultHistoryReplayBytes.
	MinHistoryReplayBytes = 24 * 1024
	// DefaultStatelessReplayBytes gives stateless, tool-free requests (such as
	// conversation summarisation) enough room to include the conversation being
	// summarised instead of truncating most of it away.
	DefaultStatelessReplayBytes = 256 * 1024
	maxReplayToolResultBytes    = 3 * 1024
	maxReplayToolArgsBytes      = 2 * 1024
	maxReplayOldMessageBytes    = 6 * 1024

	historyHeader         = "<history>\nThe conversation so far, from before this session started. Your earlier replies and tool calls are shown as they were; call the tools again yourself when you need them.\n\n"
	historyFooter         = "\n</history>\n\n"
	historyOmittedNote    = "[Earlier messages were omitted to stay within the history replay limit.]"
	historyContinueEnding = "Go on from where it ends; the calls above have already run."
	historyAnswerEnding   = "Answer the last user message."
	summaryLeadPrefix     = "<history_summarised>"
	summaryNotesPrefix    = "<your_notes>"
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

type replayItem struct {
	role         string
	hasToolCalls bool
	text         string
	images       []sdk.Image
}

func headUTF8Bytes(text string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(text) <= maxBytes {
		return text
	}
	end := 0
	for i := 0; i < len(text); {
		_, size := utf8.DecodeRuneInString(text[i:])
		if i+size > maxBytes {
			break
		}
		i += size
		end = i
	}
	return text[:end]
}

func tailUTF8Bytes(text string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(text) <= maxBytes {
		return text
	}
	start := len(text)
	for start > 0 {
		_, size := utf8.DecodeLastRuneInString(text[:start])
		if len(text)-(start-size) > maxBytes {
			break
		}
		start -= size
	}
	return text[start:]
}

func truncateReplayBytes(text string, limit int, prefix, suffix string) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	maxNotice := len(prefix) + len(strconv.Itoa(len(text))) + len(suffix)
	keep := limit - maxNotice
	if keep <= 0 {
		return headUTF8Bytes(text, limit)
	}
	headLimit := keep * 3 / 5
	tailLimit := keep - headLimit
	head := headUTF8Bytes(text, headLimit)
	tail := tailUTF8Bytes(text[len(head):], tailLimit)
	omitted := len(text) - len(head) - len(tail)
	if omitted <= 0 {
		return text
	}
	return head + prefix + strconv.Itoa(omitted) + suffix + tail
}

func renderReplayItem(message sdk.Message, names map[string]string, capContent bool) (replayItem, bool) {
	switch message.Role {
	case "user":
		body := message.Content
		if capContent {
			body = truncateReplayBytes(body, maxReplayOldMessageBytes, "\n[... omitted ", " bytes from earlier message ...]\n")
		}
		return replayItem{
			role:   "user",
			text:   "<user>\n" + body + "\n</user>",
			images: message.Images,
		}, true
	case "assistant":
		content := []string{}
		if message.Content != "" {
			body := message.Content
			if capContent {
				body = truncateReplayBytes(body, maxReplayOldMessageBytes, "\n[... omitted ", " bytes from earlier message ...]\n")
			}
			content = append(content, body)
		}
		for _, call := range message.ToolCalls {
			args := call.Arguments
			if capContent {
				args = truncateReplayBytes(args, maxReplayToolArgsBytes, "... [omitted ", " bytes] ...")
			}
			content = append(content, `<call id="`+call.ID+`" name="`+call.Name+`">`+args+`</call>`)
		}
		return replayItem{
			role:         "assistant",
			hasToolCalls: len(message.ToolCalls) > 0,
			text:         "<assistant>\n" + strings.Join(content, "\n") + "\n</assistant>",
		}, true
	case "tool":
		body := message.Content
		if capContent {
			body = truncateReplayBytes(body, maxReplayToolResultBytes, "\n[... omitted ", " bytes from historical tool result; call the tool again if needed ...]\n")
		}
		errorPart := ""
		if message.IsError {
			errorPart = ` error="true"`
		}
		return replayItem{
			role:   "tool",
			text:   `<result id="` + message.ToolCallID + `" name="` + names[message.ToolCallID] + `"` + errorPart + ">\n" + body + "\n</result>",
			images: message.Images,
		}, true
	}
	return replayItem{}, false
}

func joinedReplayBytes(items []replayItem) int {
	if len(items) == 0 {
		return 0
	}
	total := (len(items) - 1) * 2
	for _, item := range items {
		total += len(item.text)
	}
	return total
}

func groupReplayItems(items []replayItem) [][]replayItem {
	groups := make([][]replayItem, 0, len(items))
	for i := 0; i < len(items); {
		if items[i].role == "assistant" && items[i].hasToolCalls {
			end := i + 1
			for end < len(items) && items[end].role == "tool" {
				end++
			}
			groups = append(groups, items[i:end])
			i = end
			continue
		}
		groups = append(groups, items[i:i+1])
		i++
	}
	return groups
}

func renderReplayItems(messages []sdk.Message, names map[string]string, capContent bool) []replayItem {
	items := make([]replayItem, 0, len(messages))
	for _, message := range messages {
		if item, ok := renderReplayItem(message, names, capContent); ok {
			items = append(items, item)
		}
	}
	return items
}

func splitSummaryPrefix(messages []sdk.Message) ([]sdk.Message, []sdk.Message) {
	if len(messages) == 0 || messages[0].Role != "user" || !strings.HasPrefix(messages[0].Content, summaryLeadPrefix) {
		return nil, messages
	}
	if len(messages) > 1 && messages[1].Role == "assistant" && strings.HasPrefix(messages[1].Content, summaryNotesPrefix) {
		return messages[:2], messages[2:]
	}
	return messages[:1], messages[1:]
}

func RenderHistory(messages []sdk.Message) Input {
	return RenderHistoryWithBudget(messages, DefaultHistoryReplayBytes)
}

func RenderHistoryWithBudget(messages []sdk.Message, maxBytes int) Input {
	if len(messages) == 1 && messages[0].Role == "user" {
		images := messages[0].Images
		if images == nil {
			images = []sdk.Image{}
		}
		return Input{Text: messages[0].Content, Images: images}
	}
	names := map[string]string{}
	relevant := make([]sdk.Message, 0, len(messages))
	for _, message := range messages {
		switch message.Role {
		case "user", "tool":
			relevant = append(relevant, message)
		case "assistant":
			for _, call := range message.ToolCalls {
				names[call.ID] = call.Name
			}
			relevant = append(relevant, message)
		}
	}
	historyMessages := relevant
	var currentMessages []sdk.Message
	if len(relevant) > 0 && relevant[len(relevant)-1].Role == "user" {
		lastNonUser := -1
		for i := len(relevant) - 1; i >= 0; i-- {
			if relevant[i].Role != "user" {
				lastNonUser = i
				break
			}
		}
		if lastNonUser >= 0 {
			historyMessages = relevant[:lastNonUser+1]
			currentMessages = relevant[lastNonUser+1:]
		} else {
			historyMessages = nil
			currentMessages = relevant
		}
	}
	// Preserve a leading compaction summary pair (<history_summarised> +
	// <your_notes>) verbatim so trimming older middle turns never causes
	// amnesia after compaction.
	pinnedMessages, tailMessages := splitSummaryPrefix(historyMessages)
	pinnedItems := renderReplayItems(pinnedMessages, names, false)
	currentItems := renderReplayItems(currentMessages, names, false)
	historyItems := renderReplayItems(tailMessages, names, false)
	ending := historyContinueEnding
	if len(messages) > 0 && messages[len(messages)-1].Role == "user" {
		ending = historyAnswerEnding
	}
	wrapperBytes := len(historyHeader) + len(historyFooter) + len(ending)
	pinnedBytes := joinedReplayBytes(pinnedItems)
	currentBytes := joinedReplayBytes(currentItems)
	available := maxBytes - wrapperBytes - pinnedBytes - currentBytes
	if len(pinnedItems) > 0 && len(historyItems) > 0 {
		available -= 2
	}
	if len(currentItems) > 0 && (len(pinnedItems) > 0 || len(historyItems) > 0) {
		available -= 2
	}

	var keptHistory []replayItem
	if len(historyItems) == 0 || joinedReplayBytes(historyItems) <= available {
		keptHistory = historyItems
	} else {
		// Only tighten oversized historical entries when the full replay would
		// otherwise exceed the budget; keep short histories completely verbatim.
		historyItems = renderReplayItems(tailMessages, names, true)
		if joinedReplayBytes(historyItems) <= available {
			keptHistory = historyItems
		} else {
			remaining := available - len(historyOmittedNote)
			groups := groupReplayItems(historyItems)
			keptReversed := make([]replayItem, 0, len(historyItems))
			for g := len(groups) - 1; g >= 0; g-- {
				group := groups[g]
				need := 2 + joinedReplayBytes(group)
				if need <= remaining {
					for i := len(group) - 1; i >= 0; i-- {
						keptReversed = append(keptReversed, group[i])
					}
					remaining -= need
					continue
				}
				if len(keptReversed) == 0 {
					for i := len(group) - 1; i >= 0; i-- {
						itemNeed := 2 + len(group[i].text)
						if itemNeed > remaining {
							break
						}
						keptReversed = append(keptReversed, group[i])
						remaining -= itemNeed
					}
				}
				break
			}
			if len(currentItems) == 0 && len(keptReversed) == 0 {
				keptReversed = append(keptReversed, historyItems[len(historyItems)-1])
			}
			slices.Reverse(keptReversed)
			keptHistory = keptReversed
		}
	}
	omitted := len(keptHistory) < len(historyItems)

	parts := make([]string, 0, len(pinnedItems)+len(keptHistory)+len(currentItems)+1)
	images := []sdk.Image{}
	for _, item := range pinnedItems {
		parts = append(parts, item.text)
		images = append(images, item.images...)
	}
	if omitted {
		parts = append(parts, historyOmittedNote)
	}
	for _, item := range keptHistory {
		parts = append(parts, item.text)
		images = append(images, item.images...)
	}
	for _, item := range currentItems {
		parts = append(parts, item.text)
		images = append(images, item.images...)
	}
	return Input{Text: historyHeader + strings.Join(parts, "\n\n") + historyFooter + ending, Images: images}
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
