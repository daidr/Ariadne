package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/daidr/Ariadne/internal/eventstore"
	"github.com/daidr/Ariadne/internal/projectsettings"
	"github.com/daidr/Ariadne/internal/schema"
)

// LifecycleProjection maintains projects, threads and conversation execution state.
// Other domain projections must also be registered before this can drive the app.
type LifecycleProjection struct{}

func (LifecycleProjection) Setup(ctx context.Context, tx *sql.Tx) error { return schema.Setup(ctx, tx) }

func (LifecycleProjection) Forget(ctx context.Context, tx *sql.Tx, projectID string) error {
	for _, query := range []string{
		"DELETE FROM attention_waiters WHERE attention_id IN (SELECT id FROM attentions WHERE project_id = ?)",
		"DELETE FROM used_approvals WHERE attention_id IN (SELECT id FROM attentions WHERE project_id = ?)",
		"DELETE FROM artifact_comments WHERE artifact_id IN (SELECT id FROM artifacts WHERE project_id = ?)",
		"DELETE FROM artifact_feedback WHERE artifact_id IN (SELECT id FROM artifacts WHERE project_id = ?)",
		"DELETE FROM artifact_versions WHERE artifact_id IN (SELECT id FROM artifacts WHERE project_id = ?)",
		"DELETE FROM search_text WHERE rowid IN (SELECT id FROM search_docs WHERE project_id = ?)",
	} {
		if _, err := tx.ExecContext(ctx, query, projectID); err != nil {
			return err
		}
	}
	// Only the application's known tables are touched, preserving plugin tables.
	for _, table := range []string{"artifacts", "artifact_shared", "attentions", "issue_writes", "messages", "notices", "memories", "todos", "conversations", "plan_edges", "plan_node_issues", "plan_nodes", "plan_proposals", "plan_revisions", "reviews", "search_docs", "transcript", "prompts", "threads", "files"} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE project_id = ?", projectID); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, "DELETE FROM projects WHERE id = ?", projectID)
	return err
}

type object map[string]any

func (value object) obj(key string) object {
	switch child := value[key].(type) {
	case map[string]any:
		return child
	case object:
		return child
	}
	return nil
}
func (value object) str(key string) string { s, _ := value[key].(string); return s }
func (value object) yes(key string) bool   { b, _ := value[key].(bool); return b }
func (value object) number(key string) any {
	if value[key] == nil {
		return 0
	}
	return value[key]
}
func encoded(value any) string { bytes, _ := json.Marshal(value); return string(bytes) }
func nullableJSON(value any) any {
	if value == nil {
		return nil
	}
	return encoded(value)
}
func boolean(value bool) int {
	if value {
		return 1
	}
	return 0
}
func fallback(value, other any) any {
	if value == nil {
		return other
	}
	return value
}
func except(value object, key string) object {
	copy := make(object, len(value))
	for k, v := range value {
		if k != key {
			copy[k] = v
		}
	}
	return copy
}

type projectionWriter struct {
	ctx context.Context
	tx  *sql.Tx
	err error
}

func (writer *projectionWriter) run(query string, args ...any) {
	if writer.err == nil {
		_, writer.err = writer.tx.ExecContext(writer.ctx, query, args...)
	}
}

func (LifecycleProjection) Apply(ctx context.Context, tx *sql.Tx, event eventstore.Event) error {
	var data object
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return err
	}
	w := &projectionWriter{ctx: ctx, tx: tx}
	if err := projectEvent(w, event, data); err != nil {
		return err
	}
	threadEvent(w, event, data)
	conversationEvent(w, event, data)
	return w.err
}

func projectEvent(w *projectionWriter, e eventstore.Event, d object) error {
	switch e.Type {
	case "project.created":
		w.run("INSERT INTO projects (id, name, coordinator_id, unnamed, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)", e.ProjectID, d["name"], d["coordinatorId"], boolean(d.yes("unnamed")), e.At, e.At)
	case "project.renamed":
		w.run("UPDATE projects SET name = ?, unnamed = 0, updated_at = ? WHERE id = ?", d["name"], e.At, e.ProjectID)
	case "project.slots.set":
		w.run("UPDATE projects SET slots = ?, updated_at = ? WHERE id = ?", d["slots"], e.At, e.ProjectID)
	case "project.references.set":
		w.run("UPDATE projects SET reference_sources = ?, updated_at = ? WHERE id = ?", encoded(d["references"]), e.At, e.ProjectID)
	case "project.mcp.set":
		w.run("UPDATE projects SET mcp = ?, updated_at = ? WHERE id = ?", encoded(d["servers"]), e.At, e.ProjectID)
	case "project.settings.set":
		var before sql.NullString
		err := w.tx.QueryRowContext(w.ctx, "SELECT settings FROM projects WHERE id = ?", e.ProjectID).Scan(&before)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		var previous object
		if before.Valid {
			if err := json.Unmarshal([]byte(before.String), &previous); err != nil {
				return err
			}
		}
		w.run("UPDATE projects SET settings = ?, updated_at = ? WHERE id = ?", encoded(d["settings"]), e.At, e.ProjectID)
		if d.obj("settings").obj("autopilot").yes("enabled") && !previous.obj("autopilot").yes("enabled") {
			w.run("UPDATE projects SET autopilot_paused = 0, autopilot_since = ? WHERE id = ?", e.Seq, e.ProjectID)
		}
	case "project.autopilot.paused":
		w.run("UPDATE projects SET autopilot_paused = ?1, updated_at = ?2, autopilot_since = CASE WHEN ?1 THEN autopilot_since ELSE ?3 END WHERE id = ?4", boolean(d.yes("paused")), e.At, e.Seq, e.ProjectID)
	case "project.setup.noted", "project.check.noted":
		var before sql.NullString
		err := w.tx.QueryRowContext(w.ctx, "SELECT detected FROM projects WHERE id = ?", e.ProjectID).Scan(&before)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		detected := object{"setup": nil, "check": nil}
		if before.Valid {
			if err := json.Unmarshal([]byte(before.String), &detected); err != nil {
				return err
			}
		}
		key := "setup"
		if e.Type == "project.check.noted" {
			key = "check"
		}
		detected[key] = object{"command": d["command"], "threadId": d["threadId"]}
		w.run("UPDATE projects SET detected = ?, updated_at = ? WHERE id = ?", encoded(detected), e.At, e.ProjectID)
	case "project.source.set":
		w.run("UPDATE projects SET source = ?, detected = NULL, updated_at = ? WHERE id = ?", nullableJSON(d["source"]), e.At, e.ProjectID)
	case "project.file_sources.set":
		w.run("UPDATE projects SET source = ?, detected = CASE WHEN ? THEN NULL ELSE detected END, updated_at = ? WHERE id = ?", nullableJSON(d["source"]), d.yes("primaryChanged"), e.At, e.ProjectID)
	default:
		w.run("UPDATE projects SET updated_at = ? WHERE id = ?", e.At, e.ProjectID)
	}
	return w.err
}

func touchThread(w *projectionWriter, seq int64, id any) {
	if id != nil {
		w.run("UPDATE threads SET changed_seq = ? WHERE id = ?", seq, id)
	}
}

func threadEvent(w *projectionWriter, e eventstore.Event, d object) {
	id := d["threadId"]
	switch e.Type {
		case "thread.created":
			w.run("INSERT INTO threads (id, project_id, title, node_id, model, effort, branch_slug, review_of, auto, created_at, updated_at, changed_seq) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)", id, e.ProjectID, d["title"], d["nodeId"], d["model"], d["effort"], d["branch"], d["reviewOf"], boolean(d.yes("auto")), e.At, e.At, e.Seq)
		if d["reviewOf"] != nil {
			w.run("UPDATE threads SET reviewer_id = ?, changed_seq = ? WHERE id = ?", id, e.Seq, d["reviewOf"])
		}
	case "review.requested":
		w.run("UPDATE threads SET reviewing = 1, review_delivered = 0, review_rounds = ?, updated_at = ?, changed_seq = ? WHERE id = ?", d["round"], e.At, e.Seq, id)
	case "review.submitted":
		w.run("UPDATE threads SET reviewing = 0, updated_at = ?, review_delivered = CASE WHEN ? THEN 1 ELSE review_delivered END, changed_seq = ? WHERE id = ?", e.At, boolean(d.str("verdict") == "pass"), e.Seq, id)
	case "review.abandoned":
		w.run("UPDATE threads SET reviewing = 0, review_delivered = 1, updated_at = ?, changed_seq = ? WHERE id = ?", e.At, e.Seq, id)
	case "thread.references.set":
		w.run("UPDATE threads SET reference_sources = ?, updated_at = ?, changed_seq = ? WHERE id = ?", encoded(d["references"]), e.At, e.Seq, id)
		if ended := d.obj("ended"); ended != nil {
			w.run("UPDATE threads SET reference_ended = json_insert(reference_ended, '$[#]', json(?)), changed_seq = ? WHERE id = ?", encoded(object{"attentionId": ended["attentionId"], "kept": ended["now"] != nil}), e.Seq, id)
			addEntry(w, e, id, "note", nil, object{"type": "reference-ended", "name": ended["name"], "now": ended["now"]}, false)
		}
		case "thread.model_set":
			w.run("UPDATE threads SET model = ?, effort = ?, updated_at = ?, changed_seq = ? WHERE id = ?", d["model"], d["effort"], e.At, e.Seq, id)
	case "thread.node.set":
		w.run("UPDATE threads SET node_id = ?, updated_at = ?, changed_seq = ? WHERE id = ?", d["nodeId"], e.At, e.Seq, id)
	case "thread.started":
		w.run("UPDATE threads SET started = 1, updated_at = ?, changed_seq = ? WHERE id = ?", e.At, e.Seq, id)
	case "thread.reported":
		w.run("UPDATE threads SET last_report = ?, last_summary = ?, last_headline = ?, last_artifacts = ?, reported_at = ?, reported_head = ?, updated_at = ?, changed_seq = ? WHERE id = ?", d["report"], d["summary"], d["headline"], encoded(d["artifacts"]), e.At, d["head"], e.At, e.Seq, id)
		if d.str("report") == "ready" {
			w.run("UPDATE threads SET autopilot_held = NULL WHERE id = ?", id)
			if !d.yes("reviewing") {
				w.run("UPDATE threads SET review_delivered = 1, reviewing = 0 WHERE id = ?", id)
			}
		}
		if d.str("report") == "done" || d.str("report") == "failed" {
			w.run("UPDATE threads SET review_delivered = 1 WHERE id = ?", id)
		}
	case "workspace.opened":
		w.run("UPDATE threads SET workspace_source_id = coalesce(?, workspace_source_id), workspace_label = ?, workspace_branch = ?, workspace_base = ?, workspace_removed = 0, workspace_parked = 0, updated_at = ?, changed_seq = ? WHERE id = ?", d["sourceId"], d["label"], d["branch"], d["base"], e.At, e.Seq, id)
	case "workspace.removed":
		w.run("UPDATE threads SET workspace_removed = 1, workspace_parked = ?, updated_at = ?, changed_seq = ? WHERE id = ?", boolean(d.str("reason") == "idle"), e.At, e.Seq, id)
	case "turn.changed":
		if d["branch"] != nil {
			w.run("UPDATE threads SET changes = ?, changed_seq = ? WHERE id = ?", encoded(d["branch"]), e.Seq, d["conversationId"])
		}
	case "branch.changes_synced":
		if d["changes"] != nil {
			w.run("UPDATE threads SET changes = ?, changed_seq = ? WHERE id = ?", encoded(d["changes"]), e.Seq, id)
		}
	case "branch.merged":
		merge := except(d, "threadId")
		merge["at"] = e.At
		w.run("UPDATE threads SET merged = ?1, landed_at = ?2, updated_at = ?2, workspace_parked = 0, changed_seq = ?3 WHERE id = ?4", encoded(merge), e.At, e.Seq, id)
	case "branch.merge_undone":
		w.run("UPDATE threads SET merged = NULL, landed_at = NULL, autopilot_held = ?, updated_at = ?, changed_seq = ? WHERE id = ?", d["head"], e.At, e.Seq, id)
	case "pull_request.synced":
		w.run("UPDATE threads SET pull_request = ?1, updated_at = ?2, check_failures = CASE WHEN ?3 THEN 0 ELSE check_failures END, changed_seq = ?4 WHERE id = ?5", encoded(except(d, "threadId")), e.At, boolean(d.obj("checks").str("state") == "success"), e.Seq, id)
	case "pull_request.closed":
		w.run("UPDATE threads SET check_failures = 0, workspace_parked = 0, landed_at = CASE WHEN ?1 THEN ?2 ELSE landed_at END, changed_seq = ?3 WHERE id = ?4", boolean(d.yes("merged")), e.At, e.Seq, id)
	case "autopilot.held":
		w.run("UPDATE threads SET autopilot_held = ?, changed_seq = ? WHERE id = ?", fallback(d["head"], ""), e.Seq, id)
	case "checks.failed":
		w.run("UPDATE threads SET check_failures = ?, changed_seq = ? WHERE id = ?", fallback(d["attempt"], 1), e.Seq, id)
	case "message.posted":
		if d.str("author") == "user" {
			w.run("UPDATE threads SET check_failures = 0, user_at = ?, aside_until = NULL, aside_reason = NULL, aside_by = NULL, aside_due = 0, changed_seq = ? WHERE id = ?", e.At, e.Seq, d["conversationId"])
		}
	case "thread.renamed":
		w.run("UPDATE threads SET title = ?, updated_at = ?, changed_seq = ? WHERE id = ?", d["title"], e.At, e.Seq, id)
	case "thread.set_aside":
		w.run("UPDATE threads SET aside_until = ?, aside_reason = ?, aside_by = ?, aside_due = 0, updated_at = ?, changed_seq = ? WHERE id = ?", d["until"], d["reason"], d["by"], e.At, e.Seq, id)
	case "thread.aside_due":
		w.run("UPDATE threads SET aside_due = 1, updated_at = ?, changed_seq = ? WHERE id = ? AND aside_until IS NOT NULL", e.At, e.Seq, id)
	case "thread.aside_ended":
		w.run("UPDATE threads SET aside_until = NULL, aside_reason = NULL, aside_by = NULL, aside_due = 0, updated_at = ?, changed_seq = ? WHERE id = ?", e.At, e.Seq, id)
	case "thread.stopped":
		w.run("UPDATE threads SET stopped_reason = ?, stopped_by = ?, stopped_at = ?, updated_at = ?, changed_seq = ? WHERE id = ?", d["reason"], d["by"], e.At, e.At, e.Seq, id)
	case "thread.nudged":
		w.run("UPDATE threads SET nudged_at = ?, changed_seq = ? WHERE id = ?", e.At, e.Seq, id)
	case "thread.reviewed":
		w.run("UPDATE threads SET reviewed_at = ?, updated_at = ?, changed_seq = ? WHERE id = ?", e.At, e.At, e.Seq, id)
	case "thread.completed":
		w.run(`UPDATE threads SET last_report = 'done', reported_at = ?1, updated_at = ?1,
			stopped_reason = NULL, stopped_by = NULL, stopped_at = NULL, check_failures = 0,
			aside_until = NULL, aside_reason = NULL, aside_by = NULL, aside_due = 0, changed_seq = ?2 WHERE id = ?3`, e.At, e.Seq, id)
	case "thread.resumed":
		w.run("UPDATE threads SET stopped_reason = NULL, stopped_by = NULL, stopped_at = NULL, updated_at = ?, changed_seq = ? WHERE id = ?", e.At, e.Seq, id)
	}
}

func activity(text string) any {
	line, _, _ := strings.Cut(projectsettings.Trim(text), "\n")
	line = projectsettings.Trim(line)
	if line == "" {
		return nil
	}
	chars := []rune(line)
	if len(chars) > 60 {
		return string(chars[:60]) + "…"
	}
	return line
}

func positiveNumber(value any) (any, bool) {
	switch v := value.(type) {
	case float64:
		return v, v > 0
	case int64:
		return v, v > 0
	case int:
		return v, v > 0
	case json.Number:
		if n, err := v.Float64(); err == nil {
			return v, n > 0
		}
	}
	return 0, false
}

func contextTokens(u object) (any, bool) {
	if u == nil {
		return 0, false
	}
	if value, ok := positiveNumber(u["contextInput"]); ok {
		return value, true
	}
	if u["contextInput"] != nil {
		return 0, false
	}
	return positiveNumber(u["input"])
}

func cost(usage object, currency string) any {
	if usage.obj("cost").str("currency") == currency {
		return usage.obj("cost").number("amount")
	}
	return 0
}

func conversationEvent(w *projectionWriter, e eventstore.Event, d object) {
	id := d["conversationId"]
	u := d.obj("usage")
	switch e.Type {
	case "conversation.instructions":
		w.run("UPDATE conversations SET instructions = ? WHERE id = ?", encoded(object{"path": d["path"], "hash": d["hash"], "content": d["content"]}), id)
	case "project.created":
		w.run("INSERT INTO conversations (id, project_id, role) VALUES (?, ?, 'coordinator')", d["coordinatorId"], e.ProjectID)
	case "thread.created":
		w.run("INSERT INTO conversations (id, project_id, role) VALUES (?, ?, 'thread')", d["threadId"], e.ProjectID)
	case "thread.completed":
		w.run("UPDATE conversations SET pending_seq = 0, pending_since = 0, last_outcome = NULL, last_error = NULL WHERE id = ?", d["threadId"])
	case "turn.started":
		w.run("UPDATE conversations SET turn_id = ?1, turn_started_at = ?2, last_turn_at = ?2, turn_started_seq = ?3, model = ?4, activity = NULL, fallback = 0 WHERE id = ?5", d["turnId"], e.At, e.Seq, d["model"], id)
		touchThread(w, e.Seq, id)
	case "turn.step":
		var status any
		if d.str("finish") != "stop" && !d.yes("quiet") {
			status = activity(d.str("text"))
		}
		ctxTokens, hasContext := contextTokens(u)
		w.run(`UPDATE conversations SET
		 pending_since = CASE WHEN pending_seq <= ?1 THEN 0 ELSE max(pending_since, ?1 + 1) END,
		 pending_seq = CASE WHEN pending_seq <= ?1 THEN 0 ELSE pending_seq END,
		 input_tokens = input_tokens + ?2, output_tokens = output_tokens + ?3,
		 cached_tokens = cached_tokens + ?7, cache_write_tokens = cache_write_tokens + ?8,
		 reasoning_tokens = reasoning_tokens + ?9, cost_usd = cost_usd + ?10, cost_cny = cost_cny + ?11,
		 context_tokens = CASE WHEN ?5 THEN ?12 ELSE context_tokens END,
		 activity = coalesce(?6, activity) WHERE id = ?4`, d["sawSeq"], u.number("input"), u.number("output"), id, boolean(hasContext), status, u.number("cached"), u.number("cacheWrite"), u.number("reasoning"), cost(u, "USD"), cost(u, "CNY"), ctxTokens)
		touchThread(w, e.Seq, id)
	case "conversation.model_fallback":
		w.run("UPDATE conversations SET model = ?, fallback = 1 WHERE id = ? AND turn_id = ?", d["to"], id, d["turnId"])
	case "conversation.wait_set":
		w.run("UPDATE conversations SET wait_id = ?, wait_until = ?, wait_note = ? WHERE id = ?", d["waitId"], d["until"], d["note"], id)
		touchThread(w, e.Seq, id)
	case "conversation.wait_ended":
		w.run("UPDATE conversations SET wait_id = NULL, wait_until = NULL, wait_note = NULL WHERE id = ? AND wait_id = ?", id, d["waitId"])
		touchThread(w, e.Seq, id)
	case "research.step", "search.answered", "conversation.compacted":
		if e.Type == "conversation.compacted" {
			w.run("UPDATE conversations SET compactions = compactions + 1 WHERE id = ?", id)
		}
		w.run("UPDATE conversations SET input_tokens = input_tokens + ?, output_tokens = output_tokens + ?, cached_tokens = cached_tokens + ?, cache_write_tokens = cache_write_tokens + ?, reasoning_tokens = reasoning_tokens + ?, cost_usd = cost_usd + ?, cost_cny = cost_cny + ? WHERE id = ?", u.number("input"), u.number("output"), u.number("cached"), u.number("cacheWrite"), u.number("reasoning"), cost(u, "USD"), cost(u, "CNY"), id)
		touchThread(w, e.Seq, id)
	case "turn.finished":
		end := "turn_id = NULL, turn_started_at = NULL, activity = NULL, last_outcome = ?, last_error = ?"
		switch d.str("outcome") {
		case "interrupted":
			w.run("UPDATE conversations SET "+end+", pending_seq = ?, pending_since = CASE WHEN pending_since = 0 THEN ? ELSE pending_since END WHERE id = ?", d["outcome"], nullableJSON(d["error"]), e.Seq, e.Seq, id)
		case "completed":
			w.run("UPDATE conversations SET "+end+" WHERE id = ?", d["outcome"], nullableJSON(d["error"]), id)
		default:
			w.run("UPDATE conversations SET "+end+", pending_since = CASE WHEN pending_seq > turn_started_seq THEN max(pending_since, turn_started_seq + 1) ELSE 0 END, pending_seq = CASE WHEN pending_seq > turn_started_seq THEN pending_seq ELSE 0 END WHERE id = ?", d["outcome"], nullableJSON(d["error"]), id)
		}
		touchThread(w, e.Seq, id)
	case "turn.requested":
		w.run("UPDATE conversations SET pending_seq = ?1, last_error = NULL, pending_since = CASE WHEN pending_since = 0 THEN ?1 ELSE pending_since END WHERE id = ?2", e.Seq, id)
		touchThread(w, e.Seq, id)
	}
}

func addEntry(w *projectionWriter, e eventstore.Event, conversationID any, kind string, ref, payload any, wake bool) {
	w.run("INSERT INTO transcript (project_id, conversation_id, seq, kind, ref, payload) VALUES (?, ?, ?, ?, ?, ?)", e.ProjectID, conversationID, e.Seq, kind, ref, nullableJSON(payload))
	if wake {
		w.run("UPDATE conversations SET pending_seq = ?1, pending_since = CASE WHEN pending_since = 0 THEN ?1 ELSE pending_since END WHERE id = ?2", e.Seq, conversationID)
	}
	touchThread(w, e.Seq, conversationID)
}
