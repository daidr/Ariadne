package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/daidr/Ariadne/internal/eventstore"
	"github.com/daidr/Ariadne/internal/projectsettings"
)

const (
	// PulseRetention is the rolling history window retained in pulse_spans and pulse_markers.
	PulseRetention = 30 * 24 * time.Hour
	// PulseRetentionMs is PulseRetention expressed in Unix milliseconds.
	PulseRetentionMs = int64(PulseRetention / time.Millisecond)

	// pulseCoalesceMs merges consecutive spans of the same stage for one conversation
	// when the gap between ending and re-entering does not exceed 2 seconds.
	pulseCoalesceMs = int64(2_000)

	// pulsePruneInterval is how often the background pruner sweeps expired Pulse rows.
	pulsePruneInterval = time.Hour
)

// Pulse stage values stored in pulse_spans.stage.
const (
	PulseStageWorking = "working"
	PulseStageQueued  = "queued"
	PulseStageReview  = "review"
	PulseStageAnswer  = "answer"
	PulseStageWaiting = "waiting"
	PulseStageCI      = "ci"
	PulseStageHalted  = "halted"
	PulseStageAside   = "aside"
)

// Pulse marker kinds stored in pulse_markers.kind.
const (
	PulseMarkerReviewHandoff   = "review_handoff"
	PulseMarkerReviewSubmitted = "review_submitted"
	PulseMarkerPROpened        = "pr_opened"
	PulseMarkerPRMerged        = "pr_merged"
	PulseMarkerCIFailed        = "ci_failed"
	PulseMarkerAttentionOpened = "attention_opened"
	PulseMarkerArtifactUpdated = "artifact_updated"
	PulseMarkerUserReturned    = "user_returned"
)

type pulseFacts struct {
	IsCoordinator  bool
	IsReviewer     bool
	OpenAttentions int
	TurnRunning    bool
	TurnPending    bool
	TurnFailed     bool
	TurnCancelled  bool
	Waiting        bool
	Aside          bool
	AsideDue       bool
	Stopped        bool
	Reviewing      bool
	LastReport     string
	Landed         bool
	Reviewed       bool
	PRState        string
	PRChecksState  string
	ChecksFailures int
	MergeConflict  bool
	Started        bool
}

// derivePulseStage maps conversation and thread facts into one macro Pulse stage
// and whether it currently occupies a global worker concurrency slot.
// An empty stage means the lane is inactive/idle and any open span should close.
func derivePulseStage(facts pulseFacts) (string, bool) {
	if facts.OpenAttentions > 0 {
		return PulseStageAnswer, false
	}
	if facts.Stopped {
		return PulseStageHalted, false
	}
	if facts.TurnRunning {
		return PulseStageWorking, !facts.IsCoordinator
	}
	if facts.TurnPending {
		return PulseStageQueued, false
	}
	if !facts.IsCoordinator && facts.Aside && !facts.AsideDue {
		return PulseStageAside, false
	}
	if facts.TurnFailed ||
		(facts.TurnCancelled && !facts.IsReviewer) ||
		(!facts.IsCoordinator && (facts.LastReport == "blocked" || facts.LastReport == "failed")) ||
		(!facts.IsCoordinator && facts.MergeConflict) ||
		(!facts.IsCoordinator && facts.LastReport != "done" && !facts.Landed && facts.PRState == "open" && facts.PRChecksState == "failure") ||
		(!facts.IsCoordinator && facts.ChecksFailures >= 3 && facts.LastReport != "done" && !facts.Landed) {
		return PulseStageHalted, false
	}
	if !facts.IsCoordinator && !facts.IsReviewer && facts.Reviewing && !facts.Landed {
		return PulseStageReview, false
	}
	if !facts.IsCoordinator && facts.LastReport != "done" && !facts.Landed && facts.PRState == "open" && facts.PRChecksState == "pending" {
		return PulseStageCI, false
	}
	if !facts.IsCoordinator && !facts.IsReviewer && facts.LastReport == "ready" && !facts.Landed && !facts.Reviewed {
		return PulseStageReview, false
	}
	if (facts.Waiting && facts.LastReport != "done") || (!facts.IsCoordinator && facts.LastReport == "waiting") {
		return PulseStageWaiting, false
	}
	if !facts.IsCoordinator && !facts.IsReviewer && !facts.Started && facts.LastReport == "" {
		return PulseStageQueued, false
	}
	return "", false
}

func truncatePulseLabel(text string, maxRunes int) string {
	line, _, _ := strings.Cut(projectsettings.Trim(text), "\n")
	line = projectsettings.Trim(line)
	if line == "" {
		return ""
	}
	chars := []rune(line)
	if len(chars) > maxRunes {
		return string(chars[:maxRunes]) + "…"
	}
	return line
}

func pulseAttentionLabel(d object) string {
	if block := d.obj("block"); block != nil {
		for _, key := range []string{"title", "question", "label", "text", "prompt"} {
			if s := truncatePulseLabel(block.str(key), 60); s != "" {
				return s
			}
		}
		if kind := truncatePulseLabel(block.str("type"), 60); kind != "" {
			return kind
		}
	}
	return truncatePulseLabel(d.str("purpose"), 60)
}

func pulseDoingTodoLabel(items any) string {
	list, _ := items.([]any)
	for _, raw := range list {
		item := asObject(raw)
		if item.str("status") == "doing" {
			if s := truncatePulseLabel(item.str("label"), 60); s != "" {
				return s
			}
		}
	}
	return ""
}

func pulsePRNumber(pr object) int64 {
	if pr == nil {
		return 0
	}
	if n, ok := number(pr["number"]); ok && n > 0 {
		return int64(n)
	}
	return 0
}

func pulsePROpenedLabel(pr object) string {
	num := pulsePRNumber(pr)
	title := truncatePulseLabel(pr.str("title"), 48)
	switch {
	case num > 0 && title != "":
		return truncatePulseLabel(fmt.Sprintf("PR #%d %s", num, title), 60)
	case num > 0:
		return fmt.Sprintf("PR #%d", num)
	default:
		return title
	}
}

func pulsePRMergedLabel(d object, isBranch bool) string {
	if isBranch {
		if base := truncatePulseLabel(d.str("base"), 48); base != "" {
			return base
		}
		return "merged"
	}
	if num := pulsePRNumber(d); num > 0 {
		return fmt.Sprintf("PR #%d", num)
	}
	return "merged"
}

func pulseCIFailedLabel(d object) string {
	num := pulsePRNumber(d)
	var names []string
	for _, raw := range array(d, "failed") {
		switch v := raw.(type) {
		case string:
			if s := projectsettings.Trim(v); s != "" {
				names = append(names, s)
			}
		case map[string]any:
			if s := projectsettings.Trim(object(v).str("name")); s != "" {
				names = append(names, s)
			}
		}
	}
	prefix := ""
	if num > 0 {
		prefix = fmt.Sprintf("PR #%d", num)
	}
	if len(names) > 0 {
		joined := strings.Join(names, ", ")
		if prefix != "" {
			return truncatePulseLabel(prefix+": "+joined, 60)
		}
		return truncatePulseLabel(joined, 60)
	}
	return prefix
}

func pulseReviewSubmittedLabel(verdict, summary string) string {
	v := projectsettings.Trim(verdict)
	s := truncatePulseLabel(summary, 48)
	switch {
	case v != "" && s != "":
		return truncatePulseLabel(v+": "+s, 60)
	case v != "":
		return v
	default:
		return s
	}
}

func pulseTurnErrorLabel(raw any) string {
	if raw == nil {
		return ""
	}
	switch value := raw.(type) {
	case string:
		return truncatePulseLabel(value, 60)
	case object:
		return pulseTurnErrorLabel(map[string]any(value))
	case map[string]any:
		obj := object(value)
		if message := truncatePulseLabel(obj.str("message"), 60); message != "" {
			return message
		}
		if detail := truncatePulseLabel(obj.str("detail"), 60); detail != "" {
			return detail
		}
		if params := obj.obj("params"); params != nil {
			for _, candidate := range []string{"detail", "reason", "message", "error"} {
				if text := truncatePulseLabel(params.str(candidate), 60); text != "" {
					return text
				}
			}
		}
		if key := truncatePulseLabel(obj.str("key"), 60); key != "" {
			return key
		}
	}
	return ""
}

func pulseUserReturnedAt(eventAt int64, d object) int64 {
	if since, ok := number(d["since"]); ok && since > 0 {
		ms := int64(since)
		if ms <= eventAt {
			return ms
		}
	}
	return eventAt
}

func pulseEvent(w *projectionWriter, e eventstore.Event, d object, beforePR object) {
	if w.err != nil {
		return
	}

	type syncTarget struct {
		id       string
		label    string
		forStage string
		conflict bool
	}
	targets := make([]syncTarget, 0, 2+len(w.wokeConversations))
	addTarget := func(id, label, forStage string, conflict bool) {
		if id == "" {
			return
		}
		for i := range targets {
			if targets[i].id == id {
				if label != "" && targets[i].label == "" {
					targets[i].label = label
					targets[i].forStage = forStage
				}
				if conflict {
					targets[i].conflict = true
				}
				return
			}
		}
		targets = append(targets, syncTarget{id: id, label: label, forStage: forStage, conflict: conflict})
	}

	insertMarkerAt := func(convID string, relatedThreadID any, kind string, at int64, label string) {
		var related any
		if s, ok := relatedThreadID.(string); ok && s != "" {
			related = s
		}
		w.run(`INSERT OR IGNORE INTO pulse_markers (seq, project_id, conversation_id, related_thread_id, kind, at, label)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`, e.Seq, e.ProjectID, convID, related, kind, at, truncatePulseLabel(label, 60))
	}
	insertMarker := func(convID string, relatedThreadID any, kind, label string) {
		insertMarkerAt(convID, relatedThreadID, kind, e.At, label)
	}

	switch e.Type {
	case "thread.created":
		addTarget(d.str("threadId"), "", "", false)
	case "thread.started", "thread.completed", "thread.reopened", "thread.resumed", "thread.reviewed", "thread.aside_due", "thread.aside_ended", "branch.merge_undone":
		addTarget(d.str("threadId"), "", "", false)
	case "thread.reported":
		addTarget(d.str("threadId"), "", "", false)
	case "thread.stopped":
		addTarget(d.str("threadId"), d.str("reason"), PulseStageHalted, false)
	case "thread.set_aside":
		addTarget(d.str("threadId"), d.str("reason"), PulseStageAside, false)
	case "turn.requested", "turn.started":
		addTarget(d.str("conversationId"), "", "", false)
	case "turn.step":
		label := ""
		if d.str("finish") != "stop" && !d.yes("quiet") {
			label = truncatePulseLabel(d.str("text"), 60)
		}
		addTarget(d.str("conversationId"), label, PulseStageWorking, false)
	case "turn.finished":
		label := pulseTurnErrorLabel(d["error"])
		addTarget(d.str("conversationId"), label, PulseStageHalted, false)
	case "conversation.wait_set":
		addTarget(d.str("conversationId"), d.str("note"), PulseStageWaiting, false)
	case "conversation.wait_ended":
		addTarget(d.str("conversationId"), "", "", false)
	case "message.posted":
		addTarget(d.str("conversationId"), "", "", false)
	case "todo.updated":
		convID := d.str("conversationId")
		if doing := pulseDoingTodoLabel(d["items"]); convID != "" && doing != "" {
			w.run("UPDATE pulse_spans SET label = ? WHERE conversation_id = ? AND ended_at IS NULL AND stage = ?", doing, convID, PulseStageWorking)
		}
	case "attention.opened":
		convID := d.str("threadId")
		if convID == "" {
			if coord, ok := coordinator(w, e.ProjectID).(string); ok {
				convID = coord
			}
		}
		label := pulseAttentionLabel(d)
		insertMarker(convID, nil, PulseMarkerAttentionOpened, label)
		addTarget(convID, label, PulseStageAnswer, false)
	case "attention.joined":
		label := ""
		if row := w.one("SELECT block, purpose FROM attentions WHERE id = ?", d["attentionId"]); row != nil {
			block := asObject(w.parsed(row, "block", nil))
			label = pulseAttentionLabel(object{"block": block, "purpose": row.text("purpose")})
		}
		addTarget(d.str("threadId"), label, PulseStageAnswer, false)
	case "attention.answered", "attention.withdrawn":
		if row := w.one("SELECT thread_id, conversation_id FROM attentions WHERE id = ?", d["attentionId"]); row != nil {
			addTarget(row.text("thread_id"), "", "", false)
			addTarget(row.text("conversation_id"), "", "", false)
		}
		for _, row := range w.all("SELECT thread_id FROM attention_waiters WHERE attention_id = ?", d["attentionId"]) {
			addTarget(row.text("thread_id"), "", "", false)
		}
	case "review.requested":
		authorID := d.str("threadId")
		reviewerID := d.str("reviewerId")
		label := ""
		if round, ok := number(d["round"]); ok && round > 0 {
			label = "#" + strconv.Itoa(int(round))
		}
		insertMarker(authorID, reviewerID, PulseMarkerReviewHandoff, label)
		addTarget(authorID, label, PulseStageReview, false)
		addTarget(reviewerID, "", "", false)
	case "review.submitted":
		authorID := d.str("threadId")
		reviewerID := d.str("reviewerId")
		if reviewerID == "" {
			reviewerID = lookupLatestReviewerID(w, authorID)
		}
		label := pulseReviewSubmittedLabel(d.str("verdict"), d.str("summary"))
		if reviewerID != "" {
			insertMarker(reviewerID, authorID, PulseMarkerReviewSubmitted, label)
		} else {
			insertMarker(authorID, nil, PulseMarkerReviewSubmitted, label)
		}
		addTarget(authorID, "", "", false)
		addTarget(reviewerID, "", "", false)
	case "review.abandoned":
		authorID := d.str("threadId")
		reviewerID := d.str("reviewerId")
		if reviewerID == "" {
			reviewerID = lookupLatestReviewerID(w, authorID)
		}
		addTarget(authorID, "", "", false)
		addTarget(reviewerID, "", "", false)
	case "pull_request.synced":
		threadID := d.str("threadId")
		afterState := d.str("state")
		afterNum := pulsePRNumber(d)
		if afterState == "open" && (beforePR == nil || beforePR.str("state") != "open" || (afterNum > 0 && pulsePRNumber(beforePR) != afterNum)) {
			insertMarker(threadID, nil, PulseMarkerPROpened, pulsePROpenedLabel(d))
		}
		label := ""
		if afterState == "open" && afterNum > 0 {
			label = fmt.Sprintf("PR #%d", afterNum)
		}
		addTarget(threadID, label, PulseStageCI, false)
	case "pull_request.closed":
		threadID := d.str("threadId")
		if d.yes("merged") {
			insertMarker(threadID, nil, PulseMarkerPRMerged, pulsePRMergedLabel(d, false))
		}
		addTarget(threadID, "", "", false)
	case "checks.failed":
		threadID := d.str("threadId")
		label := pulseCIFailedLabel(d)
		insertMarker(threadID, nil, PulseMarkerCIFailed, label)
		addTarget(threadID, label, PulseStageHalted, false)
	case "branch.merged":
		threadID := d.str("threadId")
		insertMarker(threadID, nil, PulseMarkerPRMerged, pulsePRMergedLabel(d, true))
		addTarget(threadID, "", "", false)
	case "branch.conflicted":
		threadID := d.str("threadId")
		label := "merge conflict"
		if base := truncatePulseLabel(d.str("base"), 40); base != "" {
			label = "conflict: " + base
		}
		addTarget(threadID, label, PulseStageHalted, true)
	case "artifact.created", "artifact.revised":
		author := d.str("author")
		convID := ""
		if author != "" && author != "user" {
			convID = author
		}
		insertMarker(convID, nil, PulseMarkerArtifactUpdated, d.str("title"))
	case "artifact.feedback":
		addTarget(d.str("notify"), "", "", false)
	case "artifact.feedback_withdrawn":
		addTarget(d.str("notify"), "", "", false)
	case "routine.ran":
		if coord, ok := coordinator(w, e.ProjectID).(string); ok {
			addTarget(coord, "", "", false)
		}
	case "user.returned":
		insertMarkerAt("", nil, PulseMarkerUserReturned, pulseUserReturnedAt(e.At, d), "")
	}

	for _, wokeID := range w.wokeConversations {
		addTarget(wokeID, "", "", false)
	}

	for _, target := range targets {
		syncPulseSpanFromDB(w, e.ProjectID, target.id, e.At, e.Seq, target.label, target.forStage, target.conflict)
		if w.err != nil {
			return
		}
	}
}

func lookupLatestReviewerID(w *projectionWriter, authorID string) string {
	if authorID == "" {
		return ""
	}
	if row := w.one("SELECT reviewer_id FROM reviews WHERE thread_id = ? ORDER BY id DESC LIMIT 1", authorID); row != nil {
		if id := row.text("reviewer_id"); id != "" {
			return id
		}
	}
	if row := w.one("SELECT reviewer_id FROM threads WHERE id = ?", authorID); row != nil {
		return row.text("reviewer_id")
	}
	return ""
}

func loadCurrentPulseFacts(w *projectionWriter, convID string, conflict bool) (facts pulseFacts, projectID string, defaultLabel string, exists bool) {
	conv := w.one("SELECT project_id, role, turn_id, pending_seq, last_outcome, last_error, last_turn_at, wait_id, wait_note, activity FROM conversations WHERE id = ?", convID)
	if conv == nil || w.err != nil {
		return pulseFacts{}, "", "", false
	}
	projectID = conv.text("project_id")
	isCoord := conv.text("role") == "coordinator"
	facts.IsCoordinator = isCoord
	facts.TurnRunning = conv.present("turn_id")
	facts.TurnPending = !facts.TurnRunning && conv.integer("pending_seq") > 0
	facts.TurnFailed = !facts.TurnRunning && conv.text("last_outcome") == "failed"
	facts.Waiting = conv.present("wait_id")
	facts.MergeConflict = conflict

	lastErrLabel := ""
	if conv.present("last_error") {
		lastErrLabel = pulseTurnErrorLabel(w.parsed(conv, "last_error", nil))
	}

	if isCoord {
		att := w.one("SELECT id, block, purpose FROM attentions WHERE project_id = ? AND closed = 0 AND conversation_id = ? AND thread_id IS NULL ORDER BY created_at DESC LIMIT 1", projectID, convID)
		if att != nil {
			facts.OpenAttentions = 1
			defaultLabel = pulseAttentionLabel(object{"block": asObject(w.parsed(att, "block", nil)), "purpose": att.text("purpose")})
		}
		stage, _ := derivePulseStage(facts)
		if defaultLabel == "" {
			switch stage {
			case PulseStageWorking:
				defaultLabel = conv.text("activity")
			case PulseStageHalted:
				defaultLabel = lastErrLabel
			case PulseStageWaiting:
				defaultLabel = conv.text("wait_note")
			}
		}
		return facts, projectID, truncatePulseLabel(defaultLabel, 60), true
	}

	thread := w.one(`SELECT started, last_report, last_headline, reported_at, reported_head, merged, landed_at, pull_request,
	 check_failures, stopped_reason, reviewed_at, review_of, reviewing, aside_until, aside_reason, aside_due
	 FROM threads WHERE id = ?`, convID)
	if thread == nil || w.err != nil {
		return pulseFacts{}, "", "", false
	}

	att := w.one(`SELECT a.id, a.block, a.purpose FROM attentions a
	 WHERE a.project_id = ? AND a.closed = 0 AND (a.thread_id = ? OR EXISTS
	  (SELECT 1 FROM attention_waiters aw WHERE aw.attention_id = a.id AND aw.thread_id = ?))
	 ORDER BY a.created_at DESC LIMIT 1`, projectID, convID, convID)
	if att != nil {
		facts.OpenAttentions = 1
	}

	merged := asObject(w.parsed(thread, "merged", nil))
	pullRequest := asObject(w.parsed(thread, "pull_request", nil))
	mergedAt, _ := merged["at"].(float64)

	facts.IsReviewer = thread.present("review_of")
	facts.Started = thread.flag("started") || conv.present("last_turn_at")
	facts.LastReport = thread.text("last_report")
	facts.TurnCancelled = !facts.TurnRunning && conv.text("last_outcome") == "cancelled" && conv.present("last_turn_at") && thread.integer("reported_at") < conv.integer("last_turn_at")
	facts.Aside = thread.present("aside_until")
	facts.AsideDue = thread.flag("aside_due")
	facts.Stopped = thread.present("stopped_reason")
	facts.Reviewing = thread.flag("reviewing")
	facts.ChecksFailures = int(thread.integer("check_failures"))
	facts.Landed = (merged != nil && (int64(mergedAt) >= thread.integer("reported_at") || merged["head"] == thread["reported_head"])) ||
		(thread.present("landed_at") && thread.integer("landed_at") >= thread.integer("reported_at")) ||
		pullRequest.str("state") == "merged"
	facts.Reviewed = thread.present("reviewed_at") && thread.integer("reviewed_at") >= thread.integer("reported_at")
	facts.PRState = pullRequest.str("state")
	facts.PRChecksState = pullRequest.obj("checks").str("state")

	stage, _ := derivePulseStage(facts)
	switch stage {
	case PulseStageAnswer:
		if att != nil {
			defaultLabel = pulseAttentionLabel(object{"block": asObject(w.parsed(att, "block", nil)), "purpose": att.text("purpose")})
		}
	case PulseStageWorking:
		defaultLabel = conv.text("activity")
		if defaultLabel == "" {
			if todoRow := w.one("SELECT items FROM todos WHERE conversation_id = ?", convID); todoRow != nil {
				defaultLabel = pulseDoingTodoLabel(w.parsed(todoRow, "items", []any{}))
			}
		}
	case PulseStageWaiting:
		defaultLabel = conv.text("wait_note")
	case PulseStageAside:
		defaultLabel = thread.text("aside_reason")
	case PulseStageHalted:
		defaultLabel = thread.text("stopped_reason")
		if defaultLabel == "" && facts.TurnFailed {
			defaultLabel = lastErrLabel
		}
		if defaultLabel == "" {
			defaultLabel = thread.text("last_headline")
		}
	case PulseStageReview:
		defaultLabel = thread.text("last_headline")
		if defaultLabel == "" {
			if num := pulsePRNumber(pullRequest); num > 0 {
				defaultLabel = fmt.Sprintf("PR #%d", num)
			}
		}
	case PulseStageCI:
		if num := pulsePRNumber(pullRequest); num > 0 {
			defaultLabel = fmt.Sprintf("PR #%d", num)
		}
	}
	return facts, projectID, truncatePulseLabel(defaultLabel, 60), true
}

func syncPulseSpanFromDB(w *projectionWriter, fallbackProjectID, convID string, at, seq int64, labelHint, forStage string, conflict bool) {
	facts, projectID, defaultLabel, exists := loadCurrentPulseFacts(w, convID, conflict)
	if !exists || w.err != nil {
		return
	}
	if projectID == "" {
		projectID = fallbackProjectID
	}
	stage, occupiesSlot := derivePulseStage(facts)
	label := ""
	if forStage == "" || forStage == stage {
		label = truncatePulseLabel(labelHint, 60)
	}
	if label == "" {
		label = defaultLabel
	}
	applyPulseSpanTransition(w, projectID, convID, stage, occupiesSlot, at, seq, label)
}

func applyPulseSpanTransition(w *projectionWriter, projectID, convID, stage string, occupiesSlot bool, at, seq int64, label string) {
	latest := w.one("SELECT id, stage, occupies_slot, started_at, ended_at, label FROM pulse_spans WHERE conversation_id = ? ORDER BY id DESC LIMIT 1", convID)
	if w.err != nil {
		return
	}
	slotInt := boolean(occupiesSlot)

	if stage == "" {
		if latest != nil && !latest.present("ended_at") {
			w.run("UPDATE pulse_spans SET ended_at = CASE WHEN ?1 < started_at THEN started_at ELSE ?1 END, seq_end = ?2 WHERE conversation_id = ?3 AND ended_at IS NULL", at, seq, convID)
		}
		return
	}

	if latest != nil && !latest.present("ended_at") {
		if latest.text("stage") == stage && int(latest.integer("occupies_slot")) == slotInt {
			if label != "" && label != latest.text("label") {
				w.run("UPDATE pulse_spans SET label = ? WHERE id = ?", label, latest["id"])
			}
			return
		}
		w.run("UPDATE pulse_spans SET ended_at = CASE WHEN ?1 < started_at THEN started_at ELSE ?1 END, seq_end = ?2 WHERE conversation_id = ?3 AND ended_at IS NULL", at, seq, convID)
		w.run("INSERT INTO pulse_spans (project_id, conversation_id, stage, occupies_slot, started_at, ended_at, seq_start, seq_end, label) VALUES (?, ?, ?, ?, ?, NULL, ?, NULL, ?)", projectID, convID, stage, slotInt, at, seq, label)
		return
	}

	if latest != nil && latest.present("ended_at") {
		gap := at - latest.integer("ended_at")
		if latest.text("stage") == stage && int(latest.integer("occupies_slot")) == slotInt && gap >= 0 && gap <= pulseCoalesceMs {
			w.run("UPDATE pulse_spans SET ended_at = NULL, seq_end = NULL, label = CASE WHEN ? != '' THEN ? ELSE label END WHERE id = ?", label, label, latest["id"])
			return
		}
	}

	w.run("INSERT INTO pulse_spans (project_id, conversation_id, stage, occupies_slot, started_at, ended_at, seq_start, seq_end, label) VALUES (?, ?, ?, ?, ?, NULL, ?, NULL, ?)", projectID, convID, stage, slotInt, at, seq, label)
}

// backfillPulse populates pulse_spans and pulse_markers on an in-place upgrade
// from schema v67..v74 by scanning only the last 30 days of events via events_project,
// avoiding any full-table events scan or full domain replay.
func backfillPulse(ctx context.Context, tx *sql.Tx, nowMs int64) error {
	w := &projectionWriter{ctx: ctx, tx: tx}
	latestRow := w.one("SELECT seq, at FROM events ORDER BY seq DESC LIMIT 1")
	if w.err != nil {
		return w.err
	}
	if latestRow == nil {
		return nil
	}
	latestSeq := latestRow.integer("seq")
	latestAt := latestRow.integer("at")
	refNow := max(nowMs, latestAt)
	cutoff := refNow - PulseRetentionMs

	firstSeq := int64(1)
	if cutoff > 0 {
		firstSeq = firstSeqAfter(w, float64(cutoff-1))
		if w.err != nil {
			return w.err
		}
	}

	projects := w.all("SELECT id, coordinator_id FROM projects ORDER BY id")
	if w.err != nil {
		return w.err
	}

	for _, proj := range projects {
		projectID := proj.text("id")
		coordID := proj.text("coordinator_id")
		if err := backfillProjectPulse(ctx, tx, projectID, coordID, firstSeq, cutoff, latestSeq); err != nil {
			return err
		}
	}
	return nil
}

type pulseBackfillSpan struct {
	projectID    string
	convID       string
	stage        string
	occupiesSlot bool
	startedAt    int64
	endedAt      *int64
	seqStart     int64
	seqEnd       *int64
	label        string
}

type pulseBackfillMarker struct {
	seq             int64
	projectID       string
	convID          string
	relatedThreadID string
	kind            string
	at              int64
	label           string
}

type pulseBackfillConv struct {
	facts          pulseFacts
	attentions     map[string]bool
	prNumber       int64
	prState        string
	reviewerID     string
	waitID         string
	pendingSeq     int64
	pendingSince   int64
	pendingWakeSeq []int64
	turnStartedSeq int64
	lastSpanIdx    int
	hasSpan        bool
}

func (c *pulseBackfillConv) syncPending() {
	c.facts.TurnPending = !c.facts.TurnRunning && c.pendingSeq > 0
}

func (c *pulseBackfillConv) markWake(seq int64) {
	if seq <= 0 {
		return
	}
	c.pendingSeq = seq
	if c.pendingSince == 0 {
		c.pendingSince = seq
	}
	c.pendingWakeSeq = append(c.pendingWakeSeq, seq)
	c.syncPending()
}

func (c *pulseBackfillConv) clearPending() {
	c.pendingSeq = 0
	c.pendingSince = 0
	c.pendingWakeSeq = c.pendingWakeSeq[:0]
	c.facts.TurnPending = false
}

func (c *pulseBackfillConv) consumePendingUpTo(upToSeq int64) {
	if c.pendingSeq == 0 {
		return
	}
	if c.pendingSeq <= upToSeq {
		c.clearPending()
		return
	}
	if upToSeq+1 > c.pendingSince {
		c.pendingSince = upToSeq + 1
	}
	kept := c.pendingWakeSeq[:0]
	for _, s := range c.pendingWakeSeq {
		if s > upToSeq {
			kept = append(kept, s)
		}
	}
	c.pendingWakeSeq = kept
	c.syncPending()
}

func (c *pulseBackfillConv) withdrawPending(eventSeq int64) {
	if eventSeq <= 0 || c.pendingSeq == 0 {
		return
	}
	kept := c.pendingWakeSeq[:0]
	for _, s := range c.pendingWakeSeq {
		if s != eventSeq {
			kept = append(kept, s)
		}
	}
	c.pendingWakeSeq = kept
	switch {
	case c.pendingSeq == eventSeq && c.pendingSince == eventSeq:
		c.clearPending()
	case c.pendingSeq == eventSeq:
		if len(c.pendingWakeSeq) > 0 {
			c.pendingSeq = c.pendingWakeSeq[len(c.pendingWakeSeq)-1]
			c.syncPending()
		} else {
			c.clearPending()
		}
	case c.pendingSince == eventSeq:
		if len(c.pendingWakeSeq) > 0 {
			c.pendingSince = c.pendingWakeSeq[0]
			c.syncPending()
		} else {
			c.clearPending()
		}
	}
}

func backfillProjectPulse(ctx context.Context, tx *sql.Tx, projectID, coordID string, firstSeq, cutoff, latestSeq int64) error {
	w := &projectionWriter{ctx: ctx, tx: tx}
	convs := map[string]*pulseBackfillConv{}
	ensureConv := func(id string, isCoord bool) *pulseBackfillConv {
		if id == "" {
			return nil
		}
		c := convs[id]
		if c == nil {
			c = &pulseBackfillConv{
				facts:      pulseFacts{IsCoordinator: isCoord, Started: true},
				attentions: map[string]bool{},
			}
			convs[id] = c
		}
		if isCoord {
			c.facts.IsCoordinator = true
		}
		return c
	}

	if coordID != "" {
		ensureConv(coordID, true)
	}
	for _, row := range w.all("SELECT id, review_of, reviewer_id FROM threads WHERE project_id = ?", projectID) {
		c := ensureConv(row.text("id"), false)
		if c == nil {
			continue
		}
		c.facts.IsReviewer = row.present("review_of")
		c.reviewerID = row.text("reviewer_id")
	}
	if w.err != nil {
		return w.err
	}

	var spans []pulseBackfillSpan
	var markers []pulseBackfillMarker
	attTargets := map[string]map[string]bool{}
	addAttTarget := func(attID, convID string) {
		if attID == "" || convID == "" {
			return
		}
		set := attTargets[attID]
		if set == nil {
			set = map[string]bool{}
			attTargets[attID] = set
		}
		set[convID] = true
	}

	transition := func(convID string, at, seq int64, label, forStage string) {
		c := convs[convID]
		if c == nil {
			return
		}
		stage, occupies := derivePulseStage(c.facts)
		cleanLabel := ""
		if forStage == "" || forStage == stage {
			cleanLabel = truncatePulseLabel(label, 60)
		}
		var last *pulseBackfillSpan
		if c.hasSpan {
			last = &spans[c.lastSpanIdx]
		}
		if stage == "" {
			if last != nil && last.endedAt == nil {
				end := max(at, last.startedAt)
				last.endedAt = &end
				s := seq
				last.seqEnd = &s
			}
			return
		}
		if last != nil && last.endedAt == nil {
			if last.stage == stage && last.occupiesSlot == occupies {
				if cleanLabel != "" {
					last.label = cleanLabel
				}
				return
			}
			end := max(at, last.startedAt)
			last.endedAt = &end
			s := seq
			last.seqEnd = &s
		} else if last != nil && last.endedAt != nil {
			gap := at - *last.endedAt
			if last.stage == stage && last.occupiesSlot == occupies && gap >= 0 && gap <= pulseCoalesceMs {
				last.endedAt = nil
				last.seqEnd = nil
				if cleanLabel != "" {
					last.label = cleanLabel
				}
				return
			}
		}
		spans = append(spans, pulseBackfillSpan{
			projectID:    projectID,
			convID:       convID,
			stage:        stage,
			occupiesSlot: occupies,
			startedAt:    at,
			seqStart:     seq,
			label:        cleanLabel,
		})
		c.lastSpanIdx = len(spans) - 1
		c.hasSpan = true
	}

	addMarker := func(seq int64, convID, related, kind string, at int64, label string) {
		markers = append(markers, pulseBackfillMarker{
			seq:             seq,
			projectID:       projectID,
			convID:          convID,
			relatedThreadID: related,
			kind:            kind,
			at:              at,
			label:           truncatePulseLabel(label, 60),
		})
	}

	rows, err := tx.QueryContext(ctx, `SELECT seq, type, data, at FROM events
	 WHERE project_id = ? AND seq >= ? ORDER BY seq`, projectID, firstSeq)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var seq, at int64
		var evType, rawData string
		if err := rows.Scan(&seq, &evType, &rawData, &at); err != nil {
			return err
		}
		if at < cutoff {
			continue
		}
		var d object
		if err := json.Unmarshal([]byte(rawData), &d); err != nil {
			continue
		}

		switch evType {
		case "thread.created":
			id := d.str("threadId")
			c := ensureConv(id, false)
			if c == nil {
				continue
			}
			c.facts.Started = false
			c.facts.IsReviewer = d["reviewOf"] != nil
			if authorID := d.str("reviewOf"); authorID != "" {
				if author := ensureConv(authorID, false); author != nil {
					author.reviewerID = id
				}
			}
			transition(id, at, seq, "", "")
		case "thread.started":
			id := d.str("threadId")
			c := ensureConv(id, false)
			if c == nil {
				continue
			}
			c.facts.Started = true
			transition(id, at, seq, "", "")
		case "thread.nudged":
			id := d.str("threadId")
			c := ensureConv(id, false)
			if c == nil {
				continue
			}
			c.markWake(seq)
			transition(id, at, seq, "", "")
		case "thread.reported":
			id := d.str("threadId")
			c := ensureConv(id, false)
			if c == nil {
				continue
			}
			c.facts.LastReport = d.str("report")
			c.facts.MergeConflict = false
			if d.str("report") == "ready" && !d.yes("reviewing") {
				c.facts.Reviewing = false
			}
			if d.str("report") == "done" {
				c.facts.ChecksFailures = 0
				c.facts.Aside = false
				c.facts.AsideDue = false
			}
			forStage := ""
			switch d.str("report") {
			case "ready":
				forStage = PulseStageReview
			case "blocked", "failed":
				forStage = PulseStageHalted
			}
			transition(id, at, seq, d.str("headline"), forStage)
		case "thread.completed":
			id := d.str("threadId")
			c := ensureConv(id, false)
			if c == nil {
				continue
			}
			c.facts.LastReport = "done"
			c.facts.Stopped = false
			c.clearPending()
			c.facts.TurnFailed = false
			c.facts.TurnCancelled = false
			c.facts.ChecksFailures = 0
			c.facts.Aside = false
			c.facts.AsideDue = false
			c.facts.MergeConflict = false
			transition(id, at, seq, "", "")
		case "thread.reopened":
			id := d.str("threadId")
			c := ensureConv(id, false)
			if c == nil {
				continue
			}
			c.facts.Started = true
			c.facts.LastReport = "idle"
			c.clearPending()
			c.facts.TurnFailed = false
			c.facts.TurnCancelled = false
			c.facts.ChecksFailures = 0
			c.facts.Aside = false
			c.facts.AsideDue = false
			c.facts.MergeConflict = false
			transition(id, at, seq, "", "")
		case "thread.stopped":
			id := d.str("threadId")
			c := ensureConv(id, false)
			if c == nil {
				continue
			}
			c.facts.Stopped = true
			transition(id, at, seq, d.str("reason"), PulseStageHalted)
		case "thread.resumed":
			id := d.str("threadId")
			c := ensureConv(id, false)
			if c == nil {
				continue
			}
			c.facts.Stopped = false
			c.markWake(seq)
			transition(id, at, seq, "", "")
		case "thread.reviewed":
			id := d.str("threadId")
			c := ensureConv(id, false)
			if c == nil {
				continue
			}
			c.facts.Reviewed = true
			transition(id, at, seq, "", "")
		case "thread.set_aside":
			id := d.str("threadId")
			c := ensureConv(id, false)
			if c == nil {
				continue
			}
			c.facts.Aside = true
			c.facts.AsideDue = false
			transition(id, at, seq, d.str("reason"), PulseStageAside)
		case "thread.aside_due":
			id := d.str("threadId")
			c := ensureConv(id, false)
			if c == nil {
				continue
			}
			c.facts.AsideDue = true
			transition(id, at, seq, "", "")
		case "thread.aside_ended":
			id := d.str("threadId")
			c := ensureConv(id, false)
			if c == nil {
				continue
			}
			c.facts.Aside = false
			c.facts.AsideDue = false
			transition(id, at, seq, "", "")
		case "turn.requested":
			id := d.str("conversationId")
			c := ensureConv(id, id == coordID)
			if c == nil {
				continue
			}
			c.markWake(seq)
			c.facts.TurnFailed = false
			c.facts.TurnCancelled = false
			c.facts.MergeConflict = false
			transition(id, at, seq, "", "")
		case "turn.started":
			id := d.str("conversationId")
			c := ensureConv(id, id == coordID)
			if c == nil {
				continue
			}
			c.facts.TurnRunning = true
			c.turnStartedSeq = seq
			c.facts.TurnPending = false
			c.facts.TurnFailed = false
			c.facts.TurnCancelled = false
			c.facts.MergeConflict = false
			c.facts.Started = true
			transition(id, at, seq, "", "")
		case "turn.step":
			id := d.str("conversationId")
			if c := convs[id]; c != nil {
				if sawSeq, ok := number(d["sawSeq"]); ok && sawSeq > 0 {
					c.consumePendingUpTo(int64(sawSeq))
				}
				label := ""
				if d.str("finish") != "stop" && !d.yes("quiet") {
					label = truncatePulseLabel(d.str("text"), 60)
				}
				if label != "" && c.hasSpan && spans[c.lastSpanIdx].endedAt == nil && spans[c.lastSpanIdx].stage == PulseStageWorking {
					spans[c.lastSpanIdx].label = label
				}
			}
		case "turn.finished":
			id := d.str("conversationId")
			c := ensureConv(id, id == coordID)
			if c == nil {
				continue
			}
			c.facts.TurnRunning = false
			outcome := d.str("outcome")
			switch outcome {
			case "interrupted", "suspended":
				c.markWake(seq)
				c.facts.TurnFailed = false
				c.facts.TurnCancelled = false
			case "completed":
				c.facts.TurnFailed = false
				c.facts.TurnCancelled = false
				c.syncPending()
			case "failed":
				c.facts.TurnFailed = true
				c.facts.TurnCancelled = false
				c.consumePendingUpTo(c.turnStartedSeq)
			case "cancelled":
				c.facts.TurnFailed = false
				c.facts.TurnCancelled = true
				c.consumePendingUpTo(c.turnStartedSeq)
			default:
				c.facts.TurnFailed = false
				c.facts.TurnCancelled = false
				c.consumePendingUpTo(c.turnStartedSeq)
			}
			label := pulseTurnErrorLabel(d["error"])
			transition(id, at, seq, label, PulseStageHalted)
		case "conversation.wait_set":
			id := d.str("conversationId")
			c := ensureConv(id, id == coordID)
			if c == nil {
				continue
			}
			c.facts.Waiting = true
			c.waitID = d.str("waitId")
			transition(id, at, seq, d.str("note"), PulseStageWaiting)
		case "conversation.wait_ended":
			id := d.str("conversationId")
			c := ensureConv(id, id == coordID)
			if c == nil {
				continue
			}
			if waitID := d.str("waitId"); c.waitID == "" || waitID == "" || c.waitID == waitID {
				c.facts.Waiting = false
				c.waitID = ""
			}
			if d.str("how") != "cancelled" {
				c.markWake(seq)
			}
			transition(id, at, seq, "", "")
		case "message.posted":
			id := d.str("conversationId")
			c := ensureConv(id, id == coordID)
			if c == nil {
				continue
			}
			if d.str("author") == "user" {
				c.facts.ChecksFailures = 0
				c.facts.Aside = false
				c.facts.AsideDue = false
				c.markWake(seq)
			} else if d.str("kind") == "relay" {
				c.markWake(seq)
			}
			transition(id, at, seq, "", "")
		case "todo.updated":
			id := d.str("conversationId")
			if c := convs[id]; c != nil && c.hasSpan && spans[c.lastSpanIdx].endedAt == nil && spans[c.lastSpanIdx].stage == PulseStageWorking {
				if doing := pulseDoingTodoLabel(d["items"]); doing != "" {
					spans[c.lastSpanIdx].label = doing
				}
			}
		case "attention.opened":
			attID := d.str("attentionId")
			convID := d.str("threadId")
			if convID == "" {
				convID = coordID
			}
			label := pulseAttentionLabel(d)
			addMarker(seq, convID, "", PulseMarkerAttentionOpened, at, label)
			if c := ensureConv(convID, convID == coordID); c != nil {
				c.attentions[attID] = true
				c.facts.OpenAttentions = len(c.attentions)
				addAttTarget(attID, convID)
				transition(convID, at, seq, label, PulseStageAnswer)
			}
		case "attention.joined":
			attID := d.str("attentionId")
			threadID := d.str("threadId")
			if c := ensureConv(threadID, false); c != nil {
				c.attentions[attID] = true
				c.facts.OpenAttentions = len(c.attentions)
				addAttTarget(attID, threadID)
				transition(threadID, at, seq, "", "")
			}
		case "attention.answered", "attention.withdrawn":
			attID := d.str("attentionId")
			for convID := range attTargets[attID] {
				if c := convs[convID]; c != nil {
					delete(c.attentions, attID)
					c.facts.OpenAttentions = len(c.attentions)
					if evType == "attention.answered" {
						c.markWake(seq)
					}
					transition(convID, at, seq, "", "")
				}
			}
			delete(attTargets, attID)
		case "review.requested":
			authorID := d.str("threadId")
			reviewerID := d.str("reviewerId")
			label := ""
			if round, ok := number(d["round"]); ok && round > 0 {
				label = "#" + strconv.Itoa(int(round))
			}
			addMarker(seq, authorID, reviewerID, PulseMarkerReviewHandoff, at, label)
			if author := ensureConv(authorID, false); author != nil {
				author.facts.Reviewing = true
				author.reviewerID = reviewerID
				transition(authorID, at, seq, label, PulseStageReview)
			}
			if reviewer := ensureConv(reviewerID, false); reviewer != nil {
				reviewer.facts.IsReviewer = true
				reviewer.markWake(seq)
				transition(reviewerID, at, seq, "", "")
			}
		case "review.submitted":
			authorID := d.str("threadId")
			author := ensureConv(authorID, false)
			reviewerID := d.str("reviewerId")
			if reviewerID == "" && author != nil {
				reviewerID = author.reviewerID
			}
			label := pulseReviewSubmittedLabel(d.str("verdict"), d.str("summary"))
			if reviewerID != "" {
				addMarker(seq, reviewerID, authorID, PulseMarkerReviewSubmitted, at, label)
			} else {
				addMarker(seq, authorID, "", PulseMarkerReviewSubmitted, at, label)
			}
			if author != nil {
				author.facts.Reviewing = false
				if d.str("verdict") == "changes" {
					author.markWake(seq)
				}
				transition(authorID, at, seq, "", "")
			}
			if d.str("verdict") != "changes" {
				if coord := ensureConv(coordID, true); coord != nil {
					coord.markWake(seq)
					transition(coordID, at, seq, "", "")
				}
			}
		case "review.abandoned":
			authorID := d.str("threadId")
			if author := ensureConv(authorID, false); author != nil {
				author.facts.Reviewing = false
				transition(authorID, at, seq, "", "")
			}
			if coord := ensureConv(coordID, true); coord != nil {
				coord.markWake(seq)
				transition(coordID, at, seq, "", "")
			}
		case "pull_request.synced":
			threadID := d.str("threadId")
			c := ensureConv(threadID, false)
			if c == nil {
				continue
			}
			afterState := d.str("state")
			afterNum := pulsePRNumber(d)
			if afterState == "open" && (c.prState != "open" || (afterNum > 0 && c.prNumber != afterNum)) {
				addMarker(seq, threadID, "", PulseMarkerPROpened, at, pulsePROpenedLabel(d))
			}
			c.prState = afterState
			if afterNum > 0 {
				c.prNumber = afterNum
			}
			c.facts.PRState = afterState
			c.facts.PRChecksState = d.obj("checks").str("state")
			if c.facts.PRChecksState == "success" {
				c.facts.ChecksFailures = 0
			}
			if afterState == "merged" {
				c.facts.Landed = true
			}
			label := ""
			if afterState == "open" && afterNum > 0 {
				label = fmt.Sprintf("PR #%d", afterNum)
			}
			transition(threadID, at, seq, label, PulseStageCI)
		case "pull_request.closed":
			threadID := d.str("threadId")
			c := ensureConv(threadID, false)
			if c == nil {
				continue
			}
			c.facts.ChecksFailures = 0
			if d.yes("merged") {
				c.facts.Landed = true
				c.prState = "merged"
				c.facts.PRState = "merged"
				addMarker(seq, threadID, "", PulseMarkerPRMerged, at, pulsePRMergedLabel(d, false))
			} else {
				c.prState = "closed"
				c.facts.PRState = "closed"
			}
			transition(threadID, at, seq, "", "")
		case "checks.failed":
			threadID := d.str("threadId")
			c := ensureConv(threadID, false)
			if c == nil {
				continue
			}
			attempt := c.facts.ChecksFailures + 1
			if n, ok := number(d["attempt"]); ok && n > 0 {
				attempt = int(n)
			}
			c.facts.ChecksFailures = attempt
			if attempt <= 3 {
				c.markWake(seq)
			}
			label := pulseCIFailedLabel(d)
			addMarker(seq, threadID, "", PulseMarkerCIFailed, at, label)
			transition(threadID, at, seq, label, PulseStageHalted)
		case "branch.merged":
			threadID := d.str("threadId")
			c := ensureConv(threadID, false)
			if c == nil {
				continue
			}
			c.facts.Landed = true
			c.facts.MergeConflict = false
			addMarker(seq, threadID, "", PulseMarkerPRMerged, at, pulsePRMergedLabel(d, true))
			transition(threadID, at, seq, "", "")
		case "branch.merge_undone":
			threadID := d.str("threadId")
			c := ensureConv(threadID, false)
			if c == nil {
				continue
			}
			c.facts.Landed = false
			transition(threadID, at, seq, "", "")
		case "branch.conflicted":
			threadID := d.str("threadId")
			c := ensureConv(threadID, false)
			if c == nil {
				continue
			}
			c.facts.MergeConflict = true
			c.markWake(seq)
			label := "merge conflict"
			if base := truncatePulseLabel(d.str("base"), 40); base != "" {
				label = "conflict: " + base
			}
			transition(threadID, at, seq, label, PulseStageHalted)
		case "artifact.created", "artifact.revised":
			author := d.str("author")
			convID := ""
			if author != "" && author != "user" {
				convID = author
			}
			addMarker(seq, convID, "", PulseMarkerArtifactUpdated, at, d.str("title"))
		case "artifact.commented":
			action := d.obj("action")
			if notify := d.str("notify"); notify != "" && (action.str("type") == "create" || action.str("type") == "reply") {
				if c := ensureConv(notify, notify == coordID); c != nil {
					c.markWake(seq)
					transition(notify, at, seq, "", "")
				}
			}
		case "artifact.feedback":
			if notify := d.str("notify"); notify != "" {
				if c := ensureConv(notify, notify == coordID); c != nil {
					c.markWake(seq)
					transition(notify, at, seq, "", "")
				}
			}
		case "artifact.feedback_withdrawn":
			if notify := d.str("notify"); notify != "" {
				if eventSeq, ok := number(d["eventSeq"]); ok && eventSeq > 0 {
					if c := convs[notify]; c != nil {
						c.withdrawPending(int64(eventSeq))
						transition(notify, at, seq, "", "")
					}
				}
			}
		case "routine.ran":
			if coord := ensureConv(coordID, true); coord != nil {
				coord.markWake(seq)
				transition(coordID, at, seq, "", "")
			}
		case "user.returned":
			if returnedAt := pulseUserReturnedAt(at, d); returnedAt >= cutoff {
				addMarker(seq, "", "", PulseMarkerUserReturned, returnedAt, "")
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	if len(spans) > 0 {
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO pulse_spans (project_id, conversation_id, stage, occupies_slot, started_at, ended_at, seq_start, seq_end, label)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, span := range spans {
			var endedAt, seqEnd any
			if span.endedAt != nil {
				endedAt = *span.endedAt
			}
			if span.seqEnd != nil {
				seqEnd = *span.seqEnd
			}
			if _, err := stmt.ExecContext(ctx, span.projectID, span.convID, span.stage, boolean(span.occupiesSlot), span.startedAt, endedAt, span.seqStart, seqEnd, span.label); err != nil {
				return err
			}
		}
	}

	if len(markers) > 0 {
		stmt, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO pulse_markers (seq, project_id, conversation_id, related_thread_id, kind, at, label)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, marker := range markers {
			var related any
			if marker.relatedThreadID != "" {
				related = marker.relatedThreadID
			}
			if _, err := stmt.ExecContext(ctx, marker.seq, marker.projectID, marker.convID, related, marker.kind, marker.at, marker.label); err != nil {
				return err
			}
		}
	}

	// Reconcile currently active conversations whose open stage started before
	// the 30-day window (or whose current DB state differs from the truncated replay).
	convRows := w.all(`SELECT c.id, c.turn_started_at, c.last_turn_at, t.updated_at, t.created_at
	 FROM conversations c LEFT JOIN threads t ON t.id = c.id
	 WHERE c.project_id = ?`, projectID)
	if w.err != nil {
		return w.err
	}
	for _, row := range convRows {
		convID := row.text("id")
		anchorAt := max(row.integer("turn_started_at"), row.integer("last_turn_at"), row.integer("updated_at"), row.integer("created_at"), cutoff)
		if c := convs[convID]; c != nil && c.hasSpan {
			last := spans[c.lastSpanIdx]
			anchorAt = max(anchorAt, last.startedAt)
			if last.endedAt != nil {
				anchorAt = max(anchorAt, *last.endedAt)
			}
		}
		if anchorAt <= 0 {
			anchorAt = max(cutoff, 1)
		}
		syncPulseSpanFromDB(w, projectID, convID, anchorAt, latestSeq, "", "", false)
		if w.err != nil {
			return w.err
		}
	}
	return nil
}

// PulsePruneResult reports how many expired rows PrunePulse removed.
type PulsePruneResult struct {
	SpansDeleted   int64
	MarkersDeleted int64
}

// PrunePulse deletes finished pulse_spans whose ended_at is strictly older than
// cutoffMs and pulse_markers whose at is strictly older than cutoffMs.
// Open spans (ended_at IS NULL) are never pruned.
// When cutoffMs <= 0, the rolling 30-day cutoff relative to repo.Reader.now() is used.
func (repo *Repository) PrunePulse(ctx context.Context, cutoffMs int64) (result PulsePruneResult, err error) {
	if cutoffMs <= 0 {
		cutoffMs = repo.Reader.now() - PulseRetentionMs
	}
	err = repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		resSpans, err := tx.SQL().ExecContext(ctx,
			"DELETE FROM pulse_spans WHERE ended_at IS NOT NULL AND ended_at < ?", cutoffMs)
		if err != nil {
			return err
		}
		if n, err := resSpans.RowsAffected(); err == nil {
			result.SpansDeleted = n
		}
		resMarkers, err := tx.SQL().ExecContext(ctx,
			"DELETE FROM pulse_markers WHERE at < ?", cutoffMs)
		if err != nil {
			return err
		}
		if n, err := resMarkers.RowsAffected(); err == nil {
			result.MarkersDeleted = n
		}
		return nil
	})
	return result, err
}

// PulsePruner runs periodic 30-day rolling cleanup of Pulse projection tables.
type PulsePruner struct {
	Repository *Repository
	Interval   time.Duration
	OnError    func(error)
	mu         sync.Mutex
	cancel     context.CancelFunc
	done       chan struct{}
}

func (p *PulsePruner) Start(ctx context.Context) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cancel != nil || p.Repository == nil {
		return
	}
	interval := p.Interval
	if interval <= 0 {
		interval = pulsePruneInterval
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	p.cancel = cancel
	p.done = done
	go func() {
		defer close(done)
		defer cancel()
		sweep := func() {
			if _, err := p.Repository.PrunePulse(runCtx, 0); err != nil && runCtx.Err() == nil && p.OnError != nil {
				p.OnError(err)
			}
		}
		sweep()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				sweep()
			}
		}
	}()
}

func (p *PulsePruner) Stop(ctx context.Context) error {
	p.mu.Lock()
	cancel, done := p.cancel, p.done
	if cancel != nil {
		cancel()
	}
	p.mu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
	}
	p.mu.Lock()
	if p.done == done {
		p.cancel = nil
		p.done = nil
	}
	p.mu.Unlock()
	return nil
}
