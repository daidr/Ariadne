package core

type StageFacts struct {
	OpenAttentions int
	Started        bool
	LastReport     string
	TurnRunning    bool
	TurnPending    bool
	TurnFailed     bool
	TurnCancelled  bool
	Unreported     bool
	Waiting        bool
	ChecksGaveUp   bool
	Stopped        bool
	Landed         bool
	Reviewed       bool
	UnderReview    bool
	Reviewer       bool
}

func ThreadStage(facts StageFacts) string {
	if facts.OpenAttentions > 0 {
		return "answer"
	}
	if facts.Stopped {
		return "stopped"
	}
	if facts.TurnRunning {
		return "working"
	}
	if facts.TurnPending {
		return "queued"
	}
	if facts.TurnFailed || facts.ChecksGaveUp {
		return "blocked"
	}
	if facts.TurnCancelled && !facts.Reviewer {
		return "halted"
	}
	if facts.LastReport == "ready" && facts.UnderReview && !facts.Landed {
		return "working"
	}
	if facts.LastReport == "ready" {
		if facts.Landed || facts.Reviewed {
			return "done"
		}
		return "review"
	}
	if facts.LastReport == "blocked" || facts.LastReport == "failed" {
		return "blocked"
	}
	if facts.LastReport == "done" {
		return "done"
	}
	if !facts.Started {
		return "queued"
	}
	if facts.LastReport == "waiting" || facts.Reviewer {
		return "idle"
	}
	if facts.Unreported && !facts.Waiting {
		return "silent"
	}
	return "working"
}

func NeedsUser(stage string) bool {
	switch stage {
	case "answer", "review", "blocked", "halted", "silent":
		return true
	}
	return false
}
