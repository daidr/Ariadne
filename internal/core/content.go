package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/daidr/Ariadne/internal/eventstore"
)

// ContentProjection maintains cards, messages, todos, notices and project memory.
// Notice creation runs before memory edits, matching the original undo payloads.
type ContentProjection struct{}

func (ContentProjection) Setup(context.Context, *sql.Tx) error          { return nil }
func (ContentProjection) Forget(context.Context, *sql.Tx, string) error { return nil } // LifecycleProjection owns the shared schema cleanup.
func (ContentProjection) Apply(ctx context.Context, tx *sql.Tx, event eventstore.Event) error {
	var d object
	if err := json.Unmarshal(event.Data, &d); err != nil {
		return err
	}
	w := &projectionWriter{ctx: ctx, tx: tx}
	attentionEvent(w, event, d)
	messageEvent(w, event, d)
	todoEvent(w, event, d)
	noticeEvent(w, event, d)
	memoryEvent(w, event, d)
	return w.err
}

func (w *projectionWriter) one(query string, args ...any) record {
	if w.err != nil {
		return nil
	}
	rows, err := records(w.ctx, w.tx, query, args...)
	if err != nil {
		w.err = err
		return nil
	}
	if len(rows) == 0 {
		return nil
	}
	return rows[0]
}
func (w *projectionWriter) parsed(row record, key string, other any) any {
	if w.err != nil {
		return other
	}
	value, err := row.json(key, other)
	if err != nil {
		w.err = err
		return other
	}
	return value
}
func coordinator(w *projectionWriter, projectID string) any {
	return w.one("SELECT coordinator_id FROM projects WHERE id = ?", projectID)["coordinator_id"]
}
func pick(d object, keys ...string) object {
	result := object{}
	for _, key := range keys {
		if value, ok := d[key]; ok {
			result[key] = value
		}
	}
	return result
}
func array(d object, key string) []any { value, _ := listValue(d[key]); return value }
func listValue(raw any) ([]any, bool) {
	switch values := raw.(type) {
	case []any:
		return values, true
	case []object:
		result := make([]any, len(values))
		for i, value := range values {
			result[i] = value
		}
		return result, true
	case []map[string]any:
		result := make([]any, len(values))
		for i, value := range values {
			result[i] = value
		}
		return result, true
	case []string:
		result := make([]any, len(values))
		for i, value := range values {
			result[i] = value
		}
		return result, true
	}
	return nil, false
}
func arrayJSON(value any) any {
	if values, ok := value.([]any); ok && len(values) > 0 {
		return encoded(value)
	}
	return nil
}

func attentionEvent(w *projectionWriter, e eventstore.Event, d object) {
	touchAttentionThreads := func() {
		w.run(`UPDATE threads SET changed_seq = ? WHERE id IN (
		 SELECT thread_id FROM attentions WHERE id = ? AND thread_id IS NOT NULL
		 UNION
		 SELECT thread_id FROM attention_waiters WHERE attention_id = ?
		)`, e.Seq, d["attentionId"], d["attentionId"])
	}
	switch e.Type {
	case "attention.opened":
		conversation := d["threadId"]
		if conversation == nil {
			conversation = coordinator(w, e.ProjectID)
		}
		w.run("INSERT INTO attentions (id, project_id, thread_id, conversation_id, message_id, block, purpose, created_at, changed_seq) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)", d["attentionId"], e.ProjectID, d["threadId"], conversation, d["messageId"], encoded(d["block"]), d["purpose"], e.At, e.Seq)
		touchThread(w, e.Seq, d["threadId"])
	case "attention.joined":
		w.run("INSERT OR IGNORE INTO attention_waiters (attention_id, thread_id) VALUES (?, ?)", d["attentionId"], d["threadId"])
		w.run("UPDATE attentions SET changed_seq = ? WHERE id = ? AND message_id IS NULL AND purpose IS NOT NULL", e.Seq, d["attentionId"])
		touchThread(w, e.Seq, d["threadId"])
	case "attention.answered":
		var approved any
		if d.obj("answer").str("type") == "confirm" {
			approved = boolean(d.obj("answer").yes("approved"))
		}
		w.run("UPDATE attentions SET closed = 1, answer = ?, approved = ?, changed_seq = ? WHERE id = ?", encoded(d["answer"]), approved, e.Seq, d["attentionId"])
		touchAttentionThreads()
	case "attention.withdrawn":
		w.run("UPDATE attentions SET closed = 1, withdrawn = ?, changed_seq = ? WHERE id = ?", d["reason"], e.Seq, d["attentionId"])
		touchAttentionThreads()
	case "approval.used":
		w.run("INSERT OR IGNORE INTO used_approvals (attention_id, thread_id) VALUES (?, ?)", d["attentionId"], d["threadId"])
	}
}

func messageEvent(w *projectionWriter, e eventstore.Event, d object) {
	switch e.Type {
	case "message.posted":
		w.run(`INSERT INTO messages (id, project_id, conversation_id, author, kind, content, blocks,
		 attachments, mentions, comments, cites, reply_to, created_at, changed_seq)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, d["messageId"], e.ProjectID, d["conversationId"], d["author"], d["kind"], d["content"], arrayJSON(d["blocks"]), arrayJSON(d["attachments"]), arrayJSON(d["mentions"]), arrayJSON(d["comments"]), arrayJSON(d["cites"]), nullableJSON(d["replyTo"]), e.At, e.Seq)
		touchThread(w, e.Seq, d["conversationId"])
		for _, value := range array(d, "cites") {
			cited := object(value.(map[string]any))
			if cited["conversationId"] == d["conversationId"] {
				continue
			}
			relay := object{"threadId": d["conversationId"], "messageId": d["messageId"]}
			w.run("UPDATE messages SET relayed_to = json_insert(coalesce(relayed_to, '[]'), '$[#]', json(?)), changed_seq = ? WHERE id = ?", encoded(relay), e.Seq, cited["messageId"])
		}
	case "message.edited":
		w.run("UPDATE messages SET content = ?, edited_at = ?, edit_reason = ?, original_content = coalesce(original_content, content), edited_since_read = CASE WHEN read_at IS NULL THEN 0 ELSE 1 END, changed_seq = ? WHERE id = ?", d["content"], e.At, d["reason"], e.Seq, d["messageId"])
	case "message.reacted":
		row := w.one("SELECT reactions FROM messages WHERE id = ?", d["messageId"])
		if row == nil {
			return
		}
		before, _ := w.parsed(row, "reactions", []any{}).([]any)
		kept := []any{}
		for _, value := range before {
			reaction := object(value.(map[string]any))
			if reaction["emoji"] != d["emoji"] || reaction["by"] != d["by"] {
				kept = append(kept, value)
			}
		}
		if !d.yes("removed") {
			kept = append(kept, pick(d, "emoji", "by"))
		}
		w.run("UPDATE messages SET reactions = ?, changed_seq = ? WHERE id = ?", arrayJSON(kept), e.Seq, d["messageId"])
	case "message.read":
		w.run("UPDATE messages SET read_at = coalesce(read_at, ?), edited_since_read = 0, changed_seq = CASE WHEN edited_since_read = 1 THEN ? ELSE changed_seq END WHERE conversation_id = ? AND id <= ? AND author != 'user'", e.At, e.Seq, d["conversationId"], d["upTo"])
	}
}

func todoEvent(w *projectionWriter, e eventstore.Event, d object) {
	if e.Type == "todo.updated" {
		w.run("INSERT INTO todos (conversation_id, project_id, items, updated_at) VALUES (?, ?, ?, ?) ON CONFLICT (conversation_id) DO UPDATE SET items = excluded.items, updated_at = excluded.updated_at", d["conversationId"], e.ProjectID, encoded(d["items"]), e.At)
		touchThread(w, e.Seq, d["conversationId"])
	}
}
func memoryEvent(w *projectionWriter, e eventstore.Event, d object) {
	switch e.Type {
	case "memory.saved":
		w.run("INSERT INTO memories (id, project_id, text, author, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (id) DO UPDATE SET text = excluded.text, author = excluded.author, updated_at = excluded.updated_at", d["memoryId"], e.ProjectID, d["text"], d["author"], e.At, e.At)
	case "memory.removed":
		w.run("DELETE FROM memories WHERE id = ?", d["memoryId"])
	}
}

func noticeEvent(w *projectionWriter, e eventstore.Event, d object) {
	added := 0
	add := func(conversationID any, kind string, payload any) {
		id := fmt.Sprintf("ntc_%d", e.Seq)
		added++
		if added > 1 {
			id = fmt.Sprintf("ntc_%d_%d", e.Seq, added)
		}
		w.run("INSERT INTO notices (id, project_id, conversation_id, kind, payload, created_at, changed_seq) VALUES (?, ?, ?, ?, ?, ?, ?)", id, e.ProjectID, conversationID, kind, encoded(payload), e.At, e.Seq)
	}
	switch e.Type {
	case "memory.saved", "memory.removed":
		if d.str("undoes") != "" {
			w.run("UPDATE notices SET undone = 1, changed_seq = ? WHERE id = ?", e.Seq, d["undoes"])
		}
		if d.str("author") == "user" {
			return
		}
		before := w.one("SELECT text FROM memories WHERE id = ?", d["memoryId"])["text"]
		if e.Type == "memory.saved" {
			add(d["author"], "memory", object{"action": "saved", "memoryId": d["memoryId"], "text": d["text"], "previous": before})
		} else {
			add(d["author"], "memory", object{"action": "removed", "memoryId": d["memoryId"], "text": fallback(before, ""), "previous": before})
		}
	case "memory.everywhere":
		add(d["conversationId"], "memory-everywhere", pick(d, "action", "text"))
	case "conversation.compaction_failed":
		add(d["conversationId"], "compaction-failed", pick(d, "detail"))
	case "project.settings.set":
		if routine := d.obj("routine"); routine != nil {
			add(routine["by"], "routine-change", pick(routine, "action", "prompt"))
		}
	case "plan.issue_linked", "plan.issue_unlinked":
		if d.str("by") == "user" {
			return
		}
		node := w.one("SELECT title FROM plan_nodes WHERE project_id = ? AND id = ?", e.ProjectID, d["nodeId"])
		issue := d
		linked := e.Type == "plan.issue_linked"
		if linked {
			issue = d.obj("issue")
		}
		add(d["by"], "issue-linked", object{"nodeId": d["nodeId"], "nodeTitle": fallback(node["title"], d["nodeId"]), "tracker": issue["tracker"], "number": issue["number"], "linked": linked})
	case "issue.written":
		add(d["threadId"], "issue-written", pick(d, "tracker", "number", "action", "text", "url"))
	case "branch.merged":
		payload := pick(d, "base", "method", "sha")
		if d.yes("auto") {
			payload["auto"] = true
		}
		add(d["threadId"], "merged", payload)
	case "branch.merge_undone":
		w.run("UPDATE notices SET undone = 1, changed_seq = ? WHERE conversation_id = ? AND kind = 'merged' AND json_extract(payload, '$.sha') = ?", e.Seq, d["threadId"], d["sha"])
	case "pull_request.closed":
		if auto := d.obj("auto"); auto != nil {
			add(d["threadId"], "merged", object{"base": auto["base"], "method": auto["method"], "sha": fallback(auto["sha"], ""), "auto": true, "pullRequest": d["number"]})
		}
	case "branch.conflicted":
		payload := pick(d, "base", "files")
		if d["pullRequest"] != nil && d["pullRequest"] != float64(0) {
			if value, ok := d["pullRequest"]; ok {
				payload["pullRequest"] = value
			}
		}
		add(d["threadId"], "merge-conflict", payload)
	case "plan.revised":
		if coordinatorID := coordinator(w, e.ProjectID); coordinatorID != nil && d.str("author") == "coordinator" {
			count := 0
			for _, value := range array(d, "ops") {
				if object(value.(map[string]any)).str("op") == "add" {
					count++
				}
			}
			add(coordinatorID, "plan-changed", object{"revisionId": d["revisionId"], "reason": d["reason"], "added": count, "revert": d["revertOf"] != nil})
		}
	case "thread.created":
		if coordinatorID := coordinator(w, e.ProjectID); coordinatorID != nil && d.yes("auto") {
			add(coordinatorID, "thread-auto-started", pick(d, "threadId", "title"))
		}
	case "autopilot.held":
		add(d["threadId"], "autopilot-held", pick(d, "reason", "detail"))
	case "workspace.removed":
		add(d["threadId"], "worktree-removed", pick(d, "reason"))
	case "review.requested", "review.submitted", "review.abandoned":
		state := d["verdict"]
		count := 0
		if e.Type == "review.requested" {
			state = "started"
		} else if e.Type == "review.abandoned" {
			state = "abandoned"
		} else {
			count = len(array(d, "comments"))
		}
		payload := pick(d, "threadId", "reviewerId", "round")
		payload["state"] = state
		payload["count"] = count
		add(d["threadId"], "review", payload)
		add(d["reviewerId"], "review", payload)
	case "turn.changed":
		payload := pick(d, "turnId", "from", "to", "files", "total")
		if repos, ok := d["repos"]; ok && repos != nil {
			payload["repos"] = repos
		}
		if started, ok := d["startedAt"]; ok && started != nil && started != float64(0) {
			payload["startedAt"] = started
		}
		add(d["conversationId"], "turn-changes", payload)
	case "conversation.model_fallback":
		add(d["conversationId"], "model-fallback", pick(d, "from", "to", "detail"))
	case "conversation.wait_ended":
		if d.str("how") != "cancelled" {
			add(d["conversationId"], "woken", pick(d, "how", "note"))
		}
	case "routine.ran":
		if id := coordinator(w, e.ProjectID); id != nil {
			add(id, "routine", pick(d, "routineId", "prompt", "dueAt"))
		}
	case "conversation.model_retry":
		add(d["conversationId"], "model-retry", pick(d, "attempt", "delayMs", "detail"))
	case "artifact.revised":
		if d.str("notify") != "" {
			add(d["notify"], "artifact-edited", pick(d, "artifactId", "title", "version"))
		}
	case "artifact.commented":
		action := d.obj("action")
		if action.str("type") != "create" && action.str("type") != "reply" {
			return
		}
		id := d["by"]
		if id == "user" {
			id = d["notify"]
		}
		if id == nil || id == "" {
			return
		}
		title := fallback(w.one("SELECT title FROM artifacts WHERE id = ?", d["artifactId"])["title"], "")
		add(id, "artifact-comment", object{"artifactId": d["artifactId"], "title": title, "by": d["by"], "action": action["type"], "body": action["body"]})
	case "artifact.feedback":
		title := fallback(w.one("SELECT title FROM artifacts WHERE id = ?", d["artifactId"])["title"], "")
		payload := pick(d, "artifactId", "round", "items")
		payload["title"] = title
		payload["done"] = false
		payload["summary"] = nil
		add(d["notify"], "artifact-feedback", payload)
	case "artifact.feedback_done":
		row := w.one("SELECT id, payload FROM notices WHERE kind = 'artifact-feedback' AND project_id = ? AND json_extract(payload, '$.artifactId') = ? AND json_extract(payload, '$.round') = ?", e.ProjectID, d["artifactId"], d["round"])
		if row == nil {
			return
		}
		payload, _ := w.parsed(row, "payload", map[string]any{}).(map[string]any)
		if w.err != nil {
			return
		}
		payload["done"] = true
		payload["summary"] = nil
		if d.str("summary") != "" {
			payload["summary"] = d["summary"]
		}
		w.run("UPDATE notices SET payload = ?, changed_seq = ? WHERE id = ?", encoded(payload), e.Seq, row["id"])
	case "user.returned":
		if id := coordinator(w, e.ProjectID); id != nil {
			w.run("DELETE FROM notices WHERE project_id = ? AND kind = 'recap'", e.ProjectID)
			add(id, "recap", d)
		}
	case "notice.dismissed":
		w.run("UPDATE notices SET undone = 1, changed_seq = ? WHERE id = ?", e.Seq, d["noticeId"])
	}
}
