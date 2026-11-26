package core

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/daidr/Ariadne/internal/eventstore"
)

type ArtifactProjection struct{}

func (ArtifactProjection) Setup(context.Context, *sql.Tx) error          { return nil }
func (ArtifactProjection) Forget(context.Context, *sql.Tx, string) error { return nil }
func (ArtifactProjection) Apply(ctx context.Context, tx *sql.Tx, e eventstore.Event) error {
	var d object
	if err := json.Unmarshal(e.Data, &d); err != nil {
		return err
	}
	w := &projectionWriter{ctx: ctx, tx: tx}
	artifactEvent(w, e, d)
	return w.err
}
func artifactKind(w *projectionWriter, id any) string {
	kind := w.one("SELECT kind FROM artifacts WHERE id = ?", id).text("kind")
	if kind == "" {
		return "document"
	}
	return kind
}
func artifactEvent(w *projectionWriter, e eventstore.Event, d object) {
	switch e.Type {
	case "artifact.created":
		var agent any = d["author"]
		if agent == "user" {
			agent = nil
		}
		meta := computeArtifactMetadata(d.str("kind"), d.str("content"))
		hasProto := 0
		if meta.hasPrototype {
			hasProto = 1
		}
		w.run("INSERT INTO artifacts (id, project_id, kind, title, node_id, version, content, preview, created_by, updated_by, last_agent, has_prototype, pending_actions, created_at, updated_at) VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?)", d["artifactId"], e.ProjectID, d["kind"], d["title"], d["nodeId"], d["content"], encoded(meta.preview), d["author"], d["author"], agent, hasProto, meta.pendingActions, e.At, e.At)
		w.run("INSERT INTO artifact_versions (artifact_id, version, title, content, author, reason, created_at) VALUES (?, 1, ?, ?, ?, '', ?)", d["artifactId"], d["title"], d["content"], d["author"], e.At)
		w.run("UPDATE threads SET changed_seq = ? WHERE project_id = ? AND EXISTS (SELECT 1 FROM json_each(last_artifacts) WHERE value = ?)", e.Seq, e.ProjectID, d["artifactId"])
	case "artifact.revised":
		kind := artifactKind(w, d["artifactId"])
		meta := computeArtifactMetadata(kind, d.str("content"))
		hasProto := 0
		if meta.hasPrototype {
			hasProto = 1
		}
		w.run("UPDATE artifacts SET title = ?, version = ?, content = ?, updated_by = ?, updated_at = ?, preview = ?, has_prototype = ?, pending_actions = ?, last_agent = CASE WHEN ? = 'user' THEN last_agent ELSE ? END WHERE id = ?", d["title"], d["version"], d["content"], d["author"], e.At, encoded(meta.preview), hasProto, meta.pendingActions, d["author"], d["author"], d["artifactId"])
		w.run("INSERT INTO artifact_versions (artifact_id, version, title, content, author, reason, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)", d["artifactId"], d["version"], d["title"], d["content"], d["author"], d["reason"], e.At)
		for threadID, quote := range d.obj("quotes") {
			w.run("UPDATE artifact_comments SET quote = ? WHERE id = ? AND artifact_id = ?", quote, threadID, d["artifactId"])
		}
		w.run("UPDATE threads SET changed_seq = ? WHERE project_id = ? AND EXISTS (SELECT 1 FROM json_each(last_artifacts) WHERE value = ?)", e.Seq, e.ProjectID, d["artifactId"])
	case "artifact.commented":
		applyComment(w, d["artifactId"], d["by"], d.obj("action"), e.At)
	case "artifact.feedback":
		fingerprints := d["fingerprints"]
		if fingerprints == nil {
			fingerprints = []any{}
		}
		w.run("INSERT INTO artifact_feedback (artifact_id, round, version, notify, items, images, fingerprints, event_seq, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)", d["artifactId"], d["round"], d["version"], d["notify"], encoded(d["items"]), encoded(d["images"]), encoded(fingerprints), e.Seq, e.At)
	case "artifact.feedback_done":
		w.run("UPDATE artifact_feedback SET done_by = ?, done_at = ?, summary = ? WHERE artifact_id = ? AND round = ?", d["by"], e.At, d["summary"], d["artifactId"], d["round"])
	case "artifact.feedback_withdrawn":
		w.run("UPDATE artifact_feedback SET withdrawn_at = ? WHERE artifact_id = ? AND round = ?", e.At, d["artifactId"], d["round"])
	}
}
func applyComment(w *projectionWriter, artifactID, by any, action object, at int64) {
	row := w.one("SELECT comments FROM artifact_comments WHERE id = ? AND artifact_id = ?", action["threadId"], artifactID)
	comments, _ := w.parsed(row, "comments", []any{}).([]any)
	comment := func() object {
		return object{"id": action["commentId"], "author": by, "body": action["body"], "createdAt": at, "editedAt": nil}
	}
	save := func(values []any) {
		w.run("UPDATE artifact_comments SET comments = ? WHERE id = ?", encoded(values), action["threadId"])
	}
	switch action.str("type") {
	case "create":
		w.run("INSERT INTO artifact_comments (id, artifact_id, quote, anchor, comments, created_at) VALUES (?, ?, ?, ?, ?, ?)", action["threadId"], artifactID, action["quote"], nullableJSON(action["anchor"]), encoded([]any{comment()}), at)
	case "reply":
		save(append(comments, comment()))
	case "edit":
		next := []any{}
		for _, value := range comments {
			item := asObject(value)
			if item["id"] == action["commentId"] {
				item["body"] = action["body"]
				item["editedAt"] = at
			}
			next = append(next, item)
		}
		save(next)
	case "delete":
		next := []any{}
		for _, value := range comments {
			if asObject(value)["id"] != action["commentId"] {
				next = append(next, value)
			}
		}
		save(next)
	case "resolve":
		w.run("UPDATE artifact_comments SET resolved_by = ?, resolved_at = ?, reopened_by = NULL, reopened_at = NULL WHERE id = ?", by, at, action["threadId"])
	case "reopen":
		w.run("UPDATE artifact_comments SET resolved_by = NULL, resolved_at = NULL, reopened_by = ?, reopened_at = ? WHERE id = ?", by, at, action["threadId"])
	case "remove":
		w.run("DELETE FROM artifact_comments WHERE id = ?", action["threadId"])
	}
	w.run("UPDATE artifacts SET commented_at = ?1, open_comments = (SELECT COUNT(*) FROM artifact_comments WHERE artifact_id = ?2 AND resolved_by IS NULL) WHERE id = ?2", at, artifactID)
}
