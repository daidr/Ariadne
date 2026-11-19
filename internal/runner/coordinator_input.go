package runner

import (
	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/transcript"
)

// Only work triggered by discussion updates is quiet. Read the same snapshot
// that goes to the model, so a concurrent direct request can be answered there.
func (turn *turn) trackCoordinatorInput(entries []transcript.Entry) {
	threads := map[string]bool{}
	var reports []string
	independent := false
	for _, entry := range entries {
		if asNumber(entry["seq"]) <= float64(turn.handledBefore) {
			continue
		}
		switch entry["kind"] {
		case "message":
			message, _ := entry["message"].(*core.MessageView)
			if message != nil && message.Author == "user" {
				turn.userSpoke = true
				independent = true
			}
		case "note":
			note, _ := entry["note"].(map[string]any)
			switch note["type"] {
			case "user-in-thread", "answer-in-thread":
				threads[text(note, "threadId")] = true
				turn.userSpoke = turn.userSpoke || note["type"] == "user-in-thread"
			case "answer":
				turn.userSpoke = true
				independent = true
			case "report":
				if note["report"] != "progress" {
					reports = append(reports, text(note, "threadId"))
				}
			case "instructions-changed", "thread-stopped-by-user", "thread-auto-started",
				"thread-reviewed", "thread-resumed", "user-reacted", "artifact-edited":
				// These notes provide context and do not wake the coordinator.
			default:
				independent = true
			}
		}
	}
	for _, id := range reports {
		if !threads[id] {
			independent = true
		}
	}
	turn.quiet = len(threads) != 0 && !independent
}

func coordinatorChatTool(name string) bool {
	switch name {
	case "send_message", "post_ui", "edit_message", "react", "suggest_connector":
		return true
	}
	return false
}
