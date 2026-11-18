package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"

	"github.com/daidr/Ariadne/internal/eventstore"
)

type PlanningProjection struct{}

func (PlanningProjection) Setup(context.Context, *sql.Tx) error          { return nil }
func (PlanningProjection) Forget(context.Context, *sql.Tx, string) error { return nil }
func (PlanningProjection) Apply(ctx context.Context, tx *sql.Tx, e eventstore.Event) error {
	var d object
	if err := json.Unmarshal(e.Data, &d); err != nil {
		return err
	}
	w := &projectionWriter{ctx: ctx, tx: tx}
	planEvent(w, e, d)
	reviewEvent(w, e, d)
	if e.Type == "prompt.recorded" {
		w.run("INSERT OR IGNORE INTO prompts (project_id, hash, content) VALUES (?, ?, ?)", e.ProjectID, d["hash"], d["content"])
	}
	return w.err
}

func loadPlanGraph(w *projectionWriter, projectID string) planGraph {
	graph := emptyGraph()
	for _, row := range w.all("SELECT id, kind, title, description, acceptance FROM plan_nodes WHERE project_id = ? ORDER BY position", projectID) {
		graph.nodes[row.text("id")] = object{"kind": row["kind"], "title": row["title"], "description": row["description"], "acceptance": row["acceptance"]}
		graph.order = append(graph.order, row.text("id"))
	}
	for _, row := range w.all("SELECT from_id, to_id FROM plan_edges WHERE project_id = ?", projectID) {
		graph.edges = append(graph.edges, row.text("from_id")+">"+row.text("to_id"))
	}
	return graph
}
func storePlanGraph(w *projectionWriter, projectID string, before, next planGraph, revisionID any, at int64) {
	for _, id := range before.order {
		if next.nodes[id] == nil {
			w.run("DELETE FROM plan_nodes WHERE project_id = ? AND id = ?", projectID, id)
		}
	}
	for position, id := range next.order {
		node, old := next.nodes[id], before.nodes[id]
		if old == nil {
			w.run("INSERT INTO plan_nodes (id, project_id, kind, title, description, acceptance, position, changed_in, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)", id, projectID, node["kind"], node["title"], node["description"], node["acceptance"], position, revisionID, at, at)
		} else {
			changed := boolean(old["title"] != node["title"] || old["description"] != node["description"] || old["acceptance"] != node["acceptance"])
			w.run("UPDATE plan_nodes SET title = ?, description = ?, acceptance = ?, position = ?, changed_in = CASE WHEN ? THEN ? ELSE changed_in END, updated_at = CASE WHEN ? THEN ? ELSE updated_at END WHERE project_id = ? AND id = ?", node["title"], node["description"], node["acceptance"], position, changed, revisionID, changed, at, projectID, id)
		}
	}
	w.run("DELETE FROM plan_edges WHERE project_id = ?", projectID)
	for _, key := range next.edges {
		from, to := edgeParts(key)
		var changed any
		if !slices.Contains(before.edges, key) {
			changed = revisionID
		}
		w.run("INSERT INTO plan_edges (project_id, from_id, to_id, changed_in) VALUES (?, ?, ?, ?)", projectID, from, to, changed)
	}
}
func planEvent(w *projectionWriter, e eventstore.Event, d object) {
	switch e.Type {
	case "plan.revised":
		before := loadPlanGraph(w, e.ProjectID)
		if w.err != nil {
			return
		}
		ops := []object{}
		for _, value := range array(d, "ops") {
			item, ok := value.(map[string]any)
			if !ok {
				return
			}
			ops = append(ops, item)
		}
		next, inverse, err := applyOps(before, ops)
		if err != nil {
			return
		} // Old revisions may no longer apply during replay.
		storePlanGraph(w, e.ProjectID, before, next, d["revisionId"], e.At)
		for _, id := range before.order {
			if next.nodes[id] == nil {
				w.run("DELETE FROM plan_node_issues WHERE project_id = ? AND node_id = ?", e.ProjectID, id)
			}
		}
		w.run("INSERT INTO plan_revisions (id, project_id, author, reason, ops, inverse, revert_of, proposal_id, seq, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)", d["revisionId"], e.ProjectID, d["author"], d["reason"], encoded(d["ops"]), encoded(inverse), d["revertOf"], d["proposalId"], e.Seq, e.At)
		if d.str("revertOf") != "" {
			w.run("UPDATE plan_revisions SET reverted_by = ? WHERE id = ?", d["revisionId"], d["revertOf"])
			w.run("UPDATE plan_revisions SET reverted_by = NULL WHERE id = (SELECT revert_of FROM plan_revisions WHERE id = ?)", d["revertOf"])
		}
		if d.str("proposalId") != "" {
			w.run("UPDATE plan_proposals SET status = 'applied' WHERE id = ?", d["proposalId"])
		}
	case "plan.issue_linked":
		issue := d.obj("issue")
		w.run("INSERT INTO plan_node_issues (project_id, node_id, tracker, number, title, url, linked_by, seq) VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO UPDATE SET title = excluded.title, url = excluded.url", e.ProjectID, d["nodeId"], issue["tracker"], issue["number"], issue["title"], issue["url"], d["by"], e.Seq)
	case "plan.issue_unlinked":
		w.run("DELETE FROM plan_node_issues WHERE project_id = ? AND node_id = ? AND tracker = ? AND number = ?", e.ProjectID, d["nodeId"], d["tracker"], d["number"])
	case "issue.writing", "issue.written":
		status := "writing"
		var url, seq any
		conflict := "NOTHING"
		if e.Type == "issue.written" {
			status = "written"
			url = d["url"]
			seq = e.Seq
			conflict = "UPDATE SET status = 'written', url = excluded.url, written_seq = excluded.written_seq"
		}
		w.run("INSERT INTO issue_writes (attention_id, project_id, purpose, thread_id, tracker, number, action, status, url, written_seq) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO "+conflict, d["attentionId"], e.ProjectID, d["purpose"], d["threadId"], d["tracker"], d["number"], d["action"], status, url, seq)
	case "issue.write_unsure":
		w.run("UPDATE issue_writes SET status = 'unsure' WHERE attention_id = ?", d["attentionId"])
	case "plan.proposal.dismissed":
		w.run("UPDATE plan_proposals SET status = 'expired' WHERE id = ? AND status = 'stale'", d["proposalId"])
	case "plan.proposed":
		w.run("UPDATE plan_proposals SET status = 'expired' WHERE project_id = ? AND status = 'stale'", e.ProjectID)
		w.run("INSERT INTO plan_proposals (id, project_id, attention_id, reason, ops, revert_of, status, created_at) VALUES (?, ?, ?, ?, ?, ?, 'open', ?)", d["proposalId"], e.ProjectID, d["attentionId"], d["reason"], encoded(d["ops"]), d["revertOf"], e.At)
	case "plan.proposal.stale":
		w.run("UPDATE plan_proposals SET status = 'stale', stale_reason = ? WHERE id = ?", d["reason"], d["proposalId"])
	case "attention.answered":
		var approved any
		if answer := d.obj("answer"); answer.str("type") == "confirm" {
			approved = boolean(answer.yes("approved"))
		}
		w.run("UPDATE plan_proposals SET status = CASE WHEN ? = 1 THEN 'accepted' ELSE 'rejected' END WHERE attention_id = ? AND status = 'open'", approved, d["attentionId"])
	}
}

func reviewEvent(w *projectionWriter, e eventstore.Event, d object) {
	const latest = "id = (SELECT max(id) FROM reviews WHERE thread_id = ?)"
	switch e.Type {
	case "review.requested":
		w.run("INSERT INTO reviews (id, project_id, thread_id, reviewer_id, round, head, requested_at) VALUES (?, ?, ?, ?, ?, ?, ?)", e.Seq, e.ProjectID, d["threadId"], d["reviewerId"], d["round"], d["head"], e.At)
	case "review.submitted":
		w.run("UPDATE reviews SET verdict = ?, summary = ?, comments = ?, submitted_at = ? WHERE "+latest+" AND verdict IS NULL", d["verdict"], d["summary"], encoded(d["comments"]), e.At, d["threadId"])
	case "review.abandoned":
		w.run("UPDATE reviews SET abandoned_at = ? WHERE "+latest+" AND verdict IS NULL", e.At, d["threadId"])
	case "review.replied":
		w.run("UPDATE reviews SET replies = ? WHERE "+latest+" AND replies IS NULL", encoded(d["replies"]), d["threadId"])
	}
}
