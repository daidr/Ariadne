package transcript

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/textutil"
)

const (
	CompactionTodoLead = "Todo list when earlier history was summarised (update_todo replaces the full list; any later update_todo call below supersedes this):"
	CurrentTodoLead    = "Your current todo list in Ariadne (update_todo replaces the full list):"

	maxTodoLabelUnits  = 240
	maxTodoDetailUnits = 180
	maxTodoStateUnits  = 3200
)

var todoTag = regexp.MustCompile(`(?i)[<＜﹤〈‹][` + spaces + `\p{Cf}]*(/?)[` + spaces + `\p{Cf}]*current` + separator + `todo`)

func sanitizeTodoText(value string, limit int) string {
	cleaned := textutil.Trim(whitespace.ReplaceAllString(value, " "))
	if cleaned == "" {
		return ""
	}
	cleaned = Defang(cleaned)
	cleaned = todoTag.ReplaceAllString(cleaned, "‹${1}current todo")
	if limit > 0 && unitLength(cleaned) > limit {
		cleaned = textutil.Trim(textutil.HeadUnits(cleaned, limit)) + "…"
	}
	return cleaned
}

// ParseTodoItems extracts TodoItem entries from a JSON-decoded todo items slice.
func ParseTodoItems(raw any) []core.TodoItem {
	list := arr(raw)
	if len(list) == 0 {
		return nil
	}
	out := make([]core.TodoItem, 0, len(list))
	for _, entry := range list {
		item := obj(entry)
		label := text(item["label"])
		status := text(item["status"])
		if label == "" || status == "" {
			continue
		}
		parsed := core.TodoItem{Label: label, Status: status}
		if detail := text(item["detail"]); detail != "" {
			parsed.Detail = &detail
		}
		out = append(out, parsed)
	}
	return out
}

// FormatTodoState renders a compact, bounded <current_todo> block for model
// context. Empty lists return an empty string so no tokens are spent.
func FormatTodoState(items []core.TodoItem, lead string) string {
	if len(items) == 0 {
		return ""
	}
	if lead == "" {
		lead = CurrentTodoLead
	}
	lines := make([]string, 0, len(items)+1)
	used := unitLength(lead)
	omitted := 0
	for index, item := range items {
		label := sanitizeTodoText(item.Label, maxTodoLabelUnits)
		if label == "" {
			continue
		}
		status := sanitizeTodoText(item.Status, 32)
		if status == "" {
			status = "next"
		}
		line := "- [" + status + "] " + label
		if item.Detail != nil {
			if detail := sanitizeTodoText(*item.Detail, maxTodoDetailUnits); detail != "" {
				line += " — " + detail
			}
		}
		lineUnits := unitLength(line) + 1
		if len(lines) > 0 && used+lineUnits > maxTodoStateUnits {
			omitted = len(items) - index
			break
		}
		lines = append(lines, line)
		used += lineUnits
	}
	if len(lines) == 0 {
		return ""
	}
	if omitted > 0 {
		lines = append(lines, fmt.Sprintf("- (… %d more item(s) omitted)", omitted))
	}
	return "<current_todo>\n" + lead + "\n" + strings.Join(lines, "\n") + "\n</current_todo>"
}

// HasSucceededUpdateTodoAfter reports whether entries already preserve a
// succeeded update_todo tool call after upTo, meaning the post-compaction
// transcript tail already shows the latest todo update verbatim.
func HasSucceededUpdateTodoAfter(entries []Entry, upTo float64) bool {
	succeeded := map[string]bool{}
	for _, entry := range entries {
		if entry["kind"] == "tool" && OrderKey(entry) > upTo && !yes(entry["isError"]) {
			if id := text(entry["toolCallId"]); id != "" {
				succeeded[id] = true
			}
		}
	}
	if len(succeeded) == 0 {
		return false
	}
	for _, entry := range entries {
		if entry["kind"] != "step" || OrderKey(entry) <= upTo {
			continue
		}
		for _, call := range calls(entry["toolCalls"]) {
			if call.Name == "update_todo" && succeeded[call.ID] {
				return true
			}
		}
	}
	return false
}
