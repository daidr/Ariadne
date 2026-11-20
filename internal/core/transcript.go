package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/daidr/Ariadne/internal/eventstore"
)

// TranscriptProjection runs last, after all materialized domain rows for an event.
// Waking sets a durable cursor; no coordinator work is placed in the thread queue.
type TranscriptProjection struct{}

func (TranscriptProjection) Setup(context.Context, *sql.Tx) error          { return nil }
func (TranscriptProjection) Forget(context.Context, *sql.Tx, string) error { return nil }
func (TranscriptProjection) Apply(ctx context.Context, tx *sql.Tx, e eventstore.Event) error {
	var d object
	if err := json.Unmarshal(e.Data, &d); err != nil {
		return err
	}
	w := &projectionWriter{ctx: ctx, tx: tx}
	transcriptEvent(w, e, d)
	return w.err
}

func tagged(kind string, d object) object {
	result := except(d, "")
	result["type"] = kind
	return result
}
func (w *projectionWriter) all(query string, args ...any) []record {
	if w.err != nil {
		return nil
	}
	rows, err := records(w.ctx, w.tx, query, args...)
	if err != nil {
		w.err = err
	}
	return rows
}
func askTitle(block object) any {
	if block.str("type") == "choice" {
		return block["question"]
	}
	return block["title"]
}
func describeAnswer(block, answer object) string {
	if block.str("type") == "choice" && answer.str("type") == "choice" {
		choice, _ := number(answer["choice"])
		options := array(block, "options")
		if choice >= 0 && choice < float64(len(options)) {
			return "choice: " + asObject(options[int(choice)]).str("label")
		}
		return fmt.Sprintf("choice: %v", answer["choice"])
	}
	if answer.str("type") == "confirm" {
		if answer.yes("approved") {
			switch answer.str("scope") {
			case "thread":
				return "approved for this thread"
			case "project":
				return "approved for the project"
			}
			return "approved"
		}
		return "declined"
	}
	if block.str("type") == "form" && answer.str("type") == "form" {
		lines := []string{}
		values := answer.obj("values")
		for _, value := range array(block, "fields") {
			field := asObject(value)
			lines = append(lines, fmt.Sprintf("%s (%s): %s", field.str("key"), field.str("label"), encoded(values[field.str("key")])))
		}
		return "submitted:\n" + strings.Join(lines, "\n")
	}
	return encoded(answer)
}

func notifyCoordinatorOfThreadAnswer(w *projectionWriter, e eventstore.Event, source record, question object, answer object, attentionID any) {
	id := coordinator(w, e.ProjectID)
	if id == nil || id == source["conversation_id"] {
		return
	}
	openAsks := []object{}
	for _, ask := range w.all(`SELECT a.id, a.block, m.content
		FROM attentions a JOIN messages m ON m.id = a.message_id
		WHERE a.conversation_id = ? AND a.closed = 0 ORDER BY a.id`, id) {
		openAsks = append(openAsks, object{
			"attentionId": ask["id"], "question": w.parsed(ask, "block", nil), "context": ask.text("content"),
		})
	}
	addEntry(w, e, id, "note", nil, object{
		"type": "answer-in-thread", "threadId": source["conversation_id"], "attentionId": attentionID,
		"question": question, "context": source.text("content"), "answer": answer, "openAsks": openAsks,
	}, true)
}

func transcriptEvent(w *projectionWriter, e eventstore.Event, d object) {
	note := func(id any, payload any, wake bool) { addEntry(w, e, id, "note", nil, payload, wake) }
	switch e.Type {
	case "message.posted":
		id := d["conversationId"]
		if d.str("author") == "user" {
			asks := []any{}
			for _, row := range w.all("SELECT id, block FROM attentions WHERE conversation_id = ? AND closed = 0 AND message_id IS NOT NULL ORDER BY id", id) {
				block, _ := w.parsed(row, "block", map[string]any{}).(map[string]any)
				asks = append(asks, object{"attentionId": row["id"], "title": askTitle(block)})
			}
			var payload any
			if len(asks) > 0 {
				payload = object{"openAsks": asks}
			}
			addEntry(w, e, id, "message", d["messageId"], payload, true)
		} else if d.str("kind") == "relay" {
			addEntry(w, e, id, "message", d["messageId"], nil, true)
		}
		if c := coordinator(w, e.ProjectID); d.str("author") == "user" && c != nil && c != id && len(array(d, "comments")) == 0 {
			payload := pick(d, "messageId", "content")
			payload["type"] = "user-in-thread"
			payload["threadId"] = id
			note(c, payload, true)
		}
	case "thread.reported":
		c := coordinator(w, e.ProjectID)
		reviewer := w.one("SELECT review_of FROM threads WHERE id = ?", d["threadId"])["review_of"]
		if c == nil || d.yes("reviewing") || reviewer != nil && reviewer != "" {
			return
		}
		note(c, tagged("report", except(d, "reviewing")), d.str("report") != "progress")
		if d.str("report") == "done" {
			upstreamDone(w, e, d)
		}
	case "thread.completed":
		upstreamDone(w, e, d)
	case "review.requested":
		author := w.one("SELECT last_summary FROM threads WHERE id = ?", d["threadId"])
		before := w.one("SELECT comments, replies FROM reviews WHERE thread_id = ? AND id < ? ORDER BY id DESC LIMIT 1", d["threadId"], e.Seq)
		var previous any
		round, _ := d["round"].(float64)
		if round > 1 && before.present("replies") {
			previous = object{"comments": w.parsed(before, "comments", nil), "replies": w.parsed(before, "replies", nil)}
		}
		payload := tagged("review-requested", pick(d, "threadId", "round", "head"))
		payload["summary"] = fallback(author["last_summary"], "")
		payload["previous"] = previous
		note(d["reviewerId"], payload, true)
	case "review.submitted", "review.abandoned":
		if e.Type == "review.submitted" && d.str("verdict") == "changes" {
			payload := tagged("review-comments", pick(d, "reviewerId", "round", "summary", "comments"))
			round, _ := d["round"].(float64)
			payload["last"] = round >= 2
			note(d["threadId"], payload, true)
			return
		}
		c := coordinator(w, e.ProjectID)
		author := w.one("SELECT last_summary, last_headline, last_artifacts FROM threads WHERE id = ?", d["threadId"])
		if c == nil || author == nil {
			return
		}
		payload := object{"type": "report", "threadId": d["threadId"], "report": "ready", "summary": fallback(author["last_summary"], ""), "headline": fallback(author["last_headline"], ""), "artifacts": w.parsed(author, "last_artifacts", []any{}), "reviewRounds": d["round"]}
		if e.Type == "review.abandoned" {
			payload["reviewAbandoned"] = true
		}
		note(c, payload, true)
	case "checks.failed":
		attempt, _ := fallback(d["attempt"], float64(1)).(float64)
		note(d["threadId"], tagged("checks-failed", d), attempt <= 3)
	case "conversation.instructions":
		if d.yes("changed") {
			note(d["conversationId"], object{"type": "instructions-changed", "path": d["path"], "removed": d["content"] == nil}, false)
		}
	case "thread.stopped":
		if c := coordinator(w, e.ProjectID); c != nil && d.str("by") == "user" {
			note(c, tagged("thread-stopped-by-user", pick(d, "threadId", "reason")), false)
		}
	case "thread.created":
		if c := coordinator(w, e.ProjectID); c != nil && d.yes("auto") {
			note(c, tagged("thread-auto-started", pick(d, "threadId", "nodeId", "title")), false)
		}
	case "thread.nudged":
		note(d["threadId"], object{"type": "report-missing"}, true)
	case "thread.reviewed":
		if c := coordinator(w, e.ProjectID); c != nil {
			note(c, tagged("thread-reviewed", pick(d, "threadId")), false)
		}
	case "thread.resumed":
		payload := tagged("thread-resumed", d)
		note(d["threadId"], payload, true)
		if c := coordinator(w, e.ProjectID); c != nil && d["by"] != c {
			note(c, payload, false)
		}
	case "pull_request.closed", "branch.merged", "branch.merge_undone":
		kind := map[string]string{"pull_request.closed": "pull-request-closed", "branch.merged": "branch-merged", "branch.merge_undone": "merge-undone"}[e.Type]
		data := d
		if e.Type == "branch.merge_undone" {
			data = except(d, "head")
		}
		payload := tagged(kind, data)
		note(d["threadId"], payload, false)
		if c := coordinator(w, e.ProjectID); c != nil {
			note(c, payload, true)
		}
	case "branch.conflicted":
		note(d["threadId"], tagged("merge-conflict", d), true)
	case "message.reacted":
		if d.str("by") == "user" {
			payload := tagged("user-reacted", pick(d, "messageId", "emoji"))
			payload["quote"] = fallback(d["quote"], "")
			payload["removed"] = d.yes("removed")
			note(d["conversationId"], payload, false)
		}
	case "artifact.revised":
		if d.str("notify") != "" {
			payload := tagged("artifact-edited", pick(d, "artifactId", "title", "version"))
			payload["diff"] = d["diff"]
			note(d["notify"], payload, false)
		}
	case "artifact.commented":
		action := d.obj("action")
		if d.str("notify") == "" || action.str("type") != "create" && action.str("type") != "reply" {
			return
		}
		thread := w.one("SELECT c.quote, a.title FROM artifact_comments c JOIN artifacts a ON a.id = c.artifact_id WHERE c.id = ?", action["threadId"])
		if thread == nil {
			return
		}
		note(d["notify"], object{"type": "artifact-comment", "artifactId": d["artifactId"], "title": thread["title"], "threadId": action["threadId"], "action": action["type"], "quote": thread["quote"], "body": action["body"]}, true)
	case "artifact.feedback":
		artifact := w.one("SELECT title, kind FROM artifacts WHERE id = ?", d["artifactId"])
		if artifact == nil {
			return
		}
		payload := tagged("artifact-feedback", pick(d, "artifactId", "round", "items", "images"))
		payload["title"] = artifact["title"]
		payload["kind"] = artifact["kind"]
		note(d["notify"], payload, true)
	case "conversation.feedback_reminder":
		note(d["conversationId"], object{"type": "artifact-feedback-reminder", "rounds": d["rounds"]}, false)
	case "plan.proposal.stale":
		c := coordinator(w, e.ProjectID)
		proposal := w.one("SELECT reason FROM plan_proposals WHERE id = ?", d["proposalId"])
		if c != nil && proposal != nil {
			note(c, object{"type": "proposal-stale", "proposalId": d["proposalId"], "reason": proposal["reason"], "staleBecause": d["reason"]}, true)
		}
	case "project.source.set", "project.file_sources.set":
		c := coordinator(w, e.ProjectID)
		if c == nil {
			return
		}
		if begun := w.one("SELECT 1 AS one FROM transcript WHERE conversation_id = ? AND kind = 'message' LIMIT 1", c); begun != nil {
			set := d["source"] != nil
			note(c, object{"type": "source-changed", "set": set}, set)
		}
	case "plan.revised":
		if c := coordinator(w, e.ProjectID); c != nil && d.str("author") == "user" {
			note(c, tagged("plan-changed-by-user", pick(d, "revisionId", "revertOf", "reason")), d.str("revertOf") != "")
		}
	case "attention.answered":
		row := w.one(`SELECT a.conversation_id, a.block, m.content
			FROM attentions a LEFT JOIN messages m ON m.id = a.message_id WHERE a.id = ?`, d["attentionId"])
		if row == nil || row["conversation_id"] == nil || row["conversation_id"] == "" {
			return
		}
		block, _ := w.parsed(row, "block", map[string]any{}).(map[string]any)
		answer := d.obj("answer")
		description := describeAnswer(block, answer)
		ids := []any{row["conversation_id"]}
		for _, waiter := range w.all("SELECT thread_id FROM attention_waiters WHERE attention_id = ? ORDER BY rowid", d["attentionId"]) {
			ids = append(ids, waiter["thread_id"])
		}
		seen := map[any]bool{}
		for _, id := range ids {
			if seen[id] {
				continue
			}
			seen[id] = true
			note(id, object{"type": "answer", "attentionId": d["attentionId"], "question": askTitle(block), "answer": description}, true)
		}
		if d.yes("notifyCoordinator") {
			notifyCoordinatorOfThreadAnswer(w, e, row, block, answer, d["attentionId"])
		}
	case "issue.written", "issue.write_unsure":
		write := w.one("SELECT thread_id, tracker, number, action FROM issue_writes WHERE attention_id = ?", d["attentionId"])
		if write == nil || e.Type == "issue.write_unsure" && d.str("threadId") == "" {
			return
		}
		told := write["thread_id"]
		outcome := "made"
		var url any = d["url"]
		if e.Type == "issue.write_unsure" {
			told = d["threadId"]
			outcome = "unsure"
			url = nil
		}
		for _, id := range cardThreads(w, d["attentionId"]) {
			if id != told {
				note(id, object{"type": "issue-write-shared", "threadId": write["thread_id"], "tracker": write["tracker"], "number": write["number"], "action": write["action"], "outcome": outcome, "url": url}, true)
			}
		}
	case "conversation.compacted":
		payload := pick(d, "summary", "upTo", "reasoning", "call", "state")
		if value, ok := d["userTokens"]; ok && value != nil && value != float64(0) {
			payload["userTokens"] = value
		}
		addEntry(w, e, d["conversationId"], "summary", nil, payload, false)
	case "conversation.wait_ended":
		if d.str("how") == "cancelled" && d.str("by") != "user" {
			return
		}
		note(d["conversationId"], tagged("wait-over", pick(d, "how", "until", "note")), d.str("how") != "cancelled")
	case "routine.ran":
		if c := coordinator(w, e.ProjectID); c != nil {
			note(c, tagged("routine", pick(d, "routineId", "prompt", "dueAt")), true)
		}
	case "conversation.prompt_updated":
		payload := pick(d, "base", "sections")
		if d.str("tools") != "" {
			payload["tools"] = d["tools"]
		}
		if len(array(d, "removedTools")) > 0 {
			payload["removedTools"] = d["removedTools"]
		}
		addEntry(w, e, d["conversationId"], "prompt", nil, payload, false)
	case "conversation.results_cleared":
		addEntry(w, e, d["conversationId"], "cleared", nil, pick(d, "upTo"), false)
	case "turn.step":
		payload := pick(d, "text", "reasoning", "toolCalls", "sawSeq", "quiet")
		payload["call"] = d.obj("input")["call"]
		if d.str("finish") == "length" {
			payload["cut"] = true
		}
		addEntry(w, e, d["conversationId"], "step", nil, payload, false)
	case "tool.finished":
		payload := pick(d, "toolCallId", "name", "content", "isError")
		if len(d.str("content")) >= inlineToolOutputLimit {
			delete(payload, "content")
		}
		if len(array(d, "files")) > 0 {
			payload["files"] = d["files"]
		}
		addEntry(w, e, d["conversationId"], "tool", nil, payload, false)
	}
}

func cardThreads(w *projectionWriter, attentionID any) []any {
	ids := []any{}
	if id := w.one("SELECT thread_id FROM attentions WHERE id = ?", attentionID)["thread_id"]; id != nil && id != "" {
		ids = append(ids, id)
	}
	for _, row := range w.all("SELECT thread_id FROM attention_waiters WHERE attention_id = ? ORDER BY rowid", attentionID) {
		ids = append(ids, row["thread_id"])
	}
	seen := map[any]bool{}
	unique := []any{}
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}
	return unique
}
func upstreamDone(w *projectionWriter, e eventstore.Event, d object) {
	source := w.one("SELECT node_id, title FROM threads WHERE id = ?", d["threadId"])
	if source["node_id"] == nil || source["node_id"] == "" {
		return
	}
	node := w.one("SELECT title FROM plan_nodes WHERE project_id = ? AND id = ?", e.ProjectID, source["node_id"])
	for _, row := range w.all("SELECT t.id FROM threads t JOIN plan_edges e ON e.project_id = t.project_id AND e.to_id = t.node_id WHERE e.from_id = ? AND t.project_id = ? AND coalesce(t.last_report, '') != 'done'", source["node_id"], e.ProjectID) {
		addEntry(w, e, row["id"], "note", nil, object{"type": "upstream-done", "threadId": d["threadId"], "nodeTitle": fallback(node["title"], source["title"]), "summary": d["summary"]}, true)
	}
}
