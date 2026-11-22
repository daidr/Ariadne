package core

import (
	"context"
	"database/sql"
	"fmt"
	"slices"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/eventstore"
	"github.com/daidr/Ariadne/internal/textutil"
)

const milestoneGate = "milestone:"

func nodeStatus(all []string, gate any) string {
	if gate == "passed" {
		return "done"
	}
	if gate == "open" {
		return "waiting"
	}
	if len(all) == 0 {
		return "none"
	}
	stages := []string{}
	for _, stage := range all {
		if stage != "stopped" {
			stages = append(stages, stage)
		}
	}
	if len(stages) == 0 {
		return "paused"
	}
	if slices.Contains(stages, "blocked") {
		return "error"
	}
	done := true
	for _, stage := range stages {
		if slices.Contains([]string{"answer", "review", "halted", "silent"}, stage) {
			return "waiting"
		}
		done = done && stage == "done"
	}
	if done {
		return "done"
	}
	return "running"
}
func milestoneGates(w *projectionWriter, projectID string) map[string]string {
	gates := map[string]string{}
	for _, row := range w.all("SELECT purpose, closed, approved FROM attentions WHERE project_id = ? AND purpose LIKE ? ORDER BY created_at, id", projectID, milestoneGate+"%") {
		id := row.text("purpose")[len(milestoneGate):]
		if row.flag("closed") && row.flag("approved") {
			gates[id] = "passed"
		} else if !row.flag("closed") && gates[id] != "passed" {
			gates[id] = "open"
		}
	}
	return gates
}
func objects(raw any) []object {
	values, _ := raw.([]any)
	result := []object{}
	for _, value := range values {
		result = append(result, asObject(value))
	}
	return result
}
func getPlan(w *projectionWriter, projectID string, now int64) (object, error) {
	if project := w.one("SELECT id FROM projects WHERE id = ?", projectID); project == nil {
		if w.err != nil {
			return nil, w.err
		}
		return nil, domainerr.NotFound(projectID)
	}
	graph := loadPlanGraph(w, projectID)
	changed := map[string]any{}
	for _, row := range w.all("SELECT id, changed_in FROM plan_nodes WHERE project_id = ?", projectID) {
		changed[row.text("id")] = row["changed_in"]
	}
	threads, err := listThreads(w.ctx, w.tx, projectID, now)
	if err != nil {
		return nil, err
	}
	onNodes := map[string][]ThreadView{}
	for _, thread := range threads {
		if id, ok := thread.NodeID.(string); ok {
			onNodes[id] = append(onNodes[id], thread)
		}
	}
	depends := map[string][]string{}
	for _, key := range graph.edges {
		from, to := edgeParts(key)
		depends[to] = append(depends[to], from)
	}
	issues := map[string][]object{}
	for _, row := range w.all("SELECT node_id, tracker, number, title, url, linked_by FROM plan_node_issues WHERE project_id = ? ORDER BY seq", projectID) {
		id := row.text("node_id")
		issues[id] = append(issues[id], object{"tracker": row["tracker"], "number": row["number"], "title": row["title"], "url": row["url"], "linkedBy": row["linked_by"]})
	}
	gates := milestoneGates(w, projectID)
	nodes := []object{}
	for _, id := range graph.order {
		node := except(graph.nodes[id], "")
		node["id"] = id
		node["changedIn"] = changed[id]
		node["gate"] = nil
		if node.str("kind") == "milestone" {
			if gate, has := gates[id]; has {
				node["gate"] = gate
			}
		}
		dependencies := depends[id]
		if dependencies == nil {
			dependencies = []string{}
		}
		node["dependsOn"] = dependencies
		linked := issues[id]
		if linked == nil {
			linked = []object{}
		}
		node["issues"] = linked
		stages := []string{}
		onNode := []object{}
		for _, thread := range onNodes[id] {
			stages = append(stages, thread.Stage)
			var stoppedBy any
			if thread.Stopped != nil {
				stoppedBy = thread.Stopped.By
			}
			var pullRequest any
			if pr := asObject(thread.PullRequest); pr != nil {
				pullRequest = pick(pr, "number", "url", "state", "checks")
				if truthy(pr["reference"]) {
					asObject(pullRequest)["reference"] = pr["reference"]
				}
				if truthy(pr["conflicted"]) && pr.str("state") == "open" {
					asObject(pullRequest)["conflicted"] = true
				}
			}
			onNode = append(onNode, object{"id": thread.ID, "title": thread.Title, "stage": thread.Stage, "stoppedBy": stoppedBy, "lastHeadline": thread.LastHeadline, "pullRequest": pullRequest})
		}
		node["threads"] = onNode
		node["status"] = nodeStatus(stages, node["gate"])
		nodes = append(nodes, node)
	}
	revisions := []object{}
	for _, row := range w.all("SELECT * FROM plan_revisions WHERE project_id = ? ORDER BY seq DESC LIMIT 50", projectID) {
		ops := w.parsed(row, "ops", []any{})
		revisions = append(revisions, object{"id": row["id"], "author": row["author"], "reason": row["reason"], "ops": ops, "revertOf": row["revert_of"], "revertedBy": row["reverted_by"], "createdAt": row["created_at"]})
	}
	proposals := []object{}
	for _, row := range w.all("SELECT * FROM plan_proposals WHERE project_id = ? AND status IN ('open', 'stale') ORDER BY id", projectID) {
		rawOps := w.parsed(row, "ops", []any{})
		ops := objects(rawOps)
		counts := func(id string) int {
			count := 0
			for _, thread := range threads {
				if thread.NodeID == id && thread.Stage != "done" && thread.Stopped == nil {
					count++
				}
			}
			return count
		}
		var preview any
		if row.text("status") == "open" {
			preview = previewPlanOps(graph, ops)
		}
		proposals = append(proposals, object{"id": row["id"], "attentionId": row["attention_id"], "reason": row["reason"], "ops": rawOps, "changes": describePlanOps(graph, ops, counts), "preview": preview, "status": row["status"], "staleReason": row["stale_reason"], "createdAt": row["created_at"]})
	}
	return object{"projectId": projectID, "nodes": nodes, "revisions": revisions, "proposals": proposals}, w.err
}
func (reader Reader) GetPlan(ctx context.Context, projectID string) (result object, err error) {
	err = reader.snapshot(ctx, func(tx *sql.Tx) error {
		w := &projectionWriter{ctx: ctx, tx: tx}
		result, err = getPlan(w, projectID, reader.now())
		return err
	})
	return
}

// PlanBrief reads the part of a plan a linked thread needs without loading
// revision history or all the project's thread state.
func (reader Reader) PlanBrief(ctx context.Context, projectID, nodeID string) (brief string, err error) {
	err = reader.snapshot(ctx, func(tx *sql.Tx) error {
		w := &projectionWriter{ctx: ctx, tx: tx}
			node := w.one("SELECT title, description, acceptance, group_name FROM plan_nodes WHERE project_id = ? AND id = ?", projectID, nodeID)
			if node == nil {
				return w.err
			}
			brief = node.text("title") + "\n" + node.text("description")
			if group := node.text("group_name"); group != "" {
				brief += "\nGroup: " + group
			}
		if accepted := node.text("acceptance"); accepted != "" {
			brief += "\nAcceptance: " + accepted
		}
		for _, row := range w.all("SELECT number, title FROM plan_node_issues WHERE project_id = ? AND node_id = ? ORDER BY seq LIMIT 20", projectID, nodeID) {
			brief += fmt.Sprintf("\nIssue #%d: %s", row.integer("number"), row.text("title"))
		}
		for _, row := range w.all(`SELECT n.title, t.last_summary FROM plan_edges e
			JOIN plan_nodes n ON n.project_id = e.project_id AND n.id = e.from_id
			JOIN threads t ON t.project_id = e.project_id AND t.node_id = n.id
			WHERE e.project_id = ? AND e.to_id = ? AND t.last_report = 'done'
			ORDER BY n.position, t.reported_at, t.id LIMIT 20`, projectID, nodeID) {
			brief += "\nUpstream " + row.text("title") + ": " + textutil.HeadUnits(row.text("last_summary"), 1000)
		}
		return w.err
	})
	return
}

func (reader Reader) ModelPlan(ctx context.Context, projectID string) (map[string]any, error) {
	plan, err := reader.GetPlan(ctx, projectID)
	if err != nil {
		return nil, err
	}
	nodes, waiting, stale, revisions := []object{}, []object{}, []object{}, []object{}
		for _, node := range plan["nodes"].([]object) {
			item := pick(node, "id", "kind", "title", "description", "acceptance", "group", "dependsOn", "status", "threads")
		issues := []any{}
		for _, issue := range node["issues"].([]object) {
			issues = append(issues, issue["number"])
		}
		item["issues"] = issues
		nodes = append(nodes, item)
	}
	for _, proposal := range plan["proposals"].([]object) {
		item := object{"proposalId": proposal["id"], "reason": proposal["reason"]}
		if proposal.str("status") == "open" {
			waiting = append(waiting, item)
		} else {
			item["because"] = proposal["staleReason"]
			stale = append(stale, item)
		}
	}
	history := plan["revisions"].([]object)
	for _, revision := range history[:min(len(history), 5)] {
		revisions = append(revisions, object{"revisionId": revision["id"], "reason": revision["reason"], "author": revision["author"], "reverted": truthy(revision["revertedBy"])})
	}
	return object{"nodes": nodes, "waitingOnUser": waiting, "agreedButNotApplied": stale, "recentChanges": revisions}, nil
}

const milestoneHistory = `SELECT a.closed, a.approved, a.changed_seq, p.autopilot_since
	FROM attentions a LEFT JOIN projects p ON p.id = a.project_id WHERE a.project_id = ? AND a.purpose = ?`

func (reader Reader) MayAskGate(ctx context.Context, projectID, nodeID string) (bool, error) {
	rows, err := records(ctx, reader.DB, milestoneHistory, projectID, milestoneGate+nodeID)
	if err != nil {
		return false, err
	}
	return mayAskGate(rows), nil
}

func mayAskGate(rows []record) bool {
	for _, row := range rows {
		if !row.flag("closed") || row.flag("approved") || row.present("approved") && row.integer("approved") == 0 && row.integer("changed_seq") > row.integer("autopilot_since") {
			return false
		}
	}
	return true
}
func (reader Reader) StaleGates(ctx context.Context, projectID string) ([]string, error) {
	rows, err := records(ctx, reader.DB, "SELECT a.id FROM attentions a LEFT JOIN plan_nodes n ON n.project_id = a.project_id AND n.id = substr(a.purpose, ?) WHERE a.project_id = ? AND a.closed = 0 AND a.purpose LIKE ? AND coalesce(n.kind, '') != 'milestone'", len(milestoneGate)+1, projectID, milestoneGate+"%")
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for _, row := range rows {
		ids = append(ids, row.text("id"))
	}
	return ids, nil
}
func (repo *Repository) WithdrawSystemAttention(ctx context.Context, projectID, id, reason string) error {
	return repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		rows, err := records(ctx, tx.SQL(), "SELECT closed, message_id FROM attentions WHERE id = ? AND project_id = ?", id, projectID)
		if err != nil {
			return err
		}
		if len(rows) == 0 || rows[0].present("message_id") || rows[0].flag("closed") {
			return nil
		}
		return appendChanges(tx, projectID, []change{{"attention.withdrawn", object{"attentionId": id, "reason": reason}}})
	})
}
func (repo *Repository) HoldAutopilot(ctx context.Context, threadID string, head *string, reason, detail string) error {
	_, err := repo.mutateThread(ctx, threadID, func(_ *eventstore.Transaction, current ThreadView) ([]change, error) {
		return []change{{"autopilot.held", object{"threadId": threadID, "head": head, "reason": reason, "detail": detail}}}, nil
	})
	return err
}
func (reader Reader) ListOpenAttentions(ctx context.Context, projectID string) (result []AttentionView, err error) {
	err = reader.snapshot(ctx, func(tx *sql.Tx) error { result, err = openAttentions(ctx, tx, projectID); return err })
	return
}
func (reader Reader) ListNeedsMe(ctx context.Context, projectID string) (result []object, err error) {
	err = reader.snapshot(ctx, func(tx *sql.Tx) error {
		threads, err := listThreads(ctx, tx, projectID, reader.now())
		if err != nil {
			return err
		}
		result, err = needsMe(ctx, tx, projectID, threads)
		return err
	})
	return
}
