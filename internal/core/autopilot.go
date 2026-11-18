package core

import (
	"context"
	"fmt"

	"github.com/daidr/Ariadne/internal/eventstore"
)

// AutopilotProjects scans enabled projects in bounded pages for startup and
// for pull requests whose CI did not publish any checks during its grace period.
func (reader Reader) AutopilotProjects(ctx context.Context, after string, limit int) ([]string, error) {
	rows, err := records(ctx, reader.DB, `SELECT id FROM projects
		WHERE id > ? AND autopilot_paused = 0 AND json_extract(settings, '$.autopilot.enabled') = 1
		ORDER BY id LIMIT ?`, after, min(max(limit, 1), 128))
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.text("id"))
	}
	return ids, nil
}

// AdvanceAutopilot makes decisions and records them against the same plan and
// conversation snapshot, including the coordinator's pending wake.
func (repo *Repository) AdvanceAutopilot(ctx context.Context, projectID string, slots int, locale string) error {
	return repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		w := &projectionWriter{ctx: ctx, tx: tx.SQL()}
		row := w.one("SELECT * FROM projects WHERE id = ?", projectID)
		if w.err != nil || row == nil || row.flag("autopilot_paused") {
			return w.err
		}
		project, err := toProject(ctx, tx.SQL(), row, 0)
		if err != nil || !project.Settings.Autopilot.Enabled {
			return err
		}
		coordinator, err := conversation(ctx, tx.SQL(), project.CoordinatorID)
		if err != nil || coordinator.Turn != nil || coordinator.PendingSeq > 0 && coordinator.LastOutcome != "failed" {
			return err
		}
		return repo.advanceAutopilotPlan(ctx, tx, w, project, slots, locale)
	})
}

func (repo *Repository) advanceAutopilotPlan(ctx context.Context, tx *eventstore.Transaction, w *projectionWriter, project ProjectView, slots int, locale string) error {
	now := repo.Reader.now()
	plan, err := getPlan(w, project.ID, now)
	if err != nil {
		return err
	}
	threads, err := listThreads(ctx, tx.SQL(), project.ID, now)
	if err != nil {
		return err
	}
	if n, ok := project.Slots.(int64); ok {
		slots = int(n)
	}
	for _, thread := range threads {
		if !truthy(thread.ReviewOf) && thread.Stage != "done" && thread.Stage != "stopped" {
			slots--
		}
	}
	budget := project.Settings.Budget.Project
	usage := record(project.Usage)
	outOfBudget := budget != nil && float64(usage.integer("input")+usage.integer("output")) >= *budget
	nodes := plan["nodes"].([]object)
	graph := make(map[string]object, len(nodes))
	status := make(map[string]string, len(nodes))
	for _, node := range nodes {
		id := node.str("id")
		graph[id] = node
		status[id] = node.str("status")
	}
	markPastMilestones(nodes, graph, status)
	pending := map[string]bool{}
	for _, proposal := range plan["proposals"].([]object) {
		if proposal.str("status") != "open" {
			continue
		}
		for _, op := range objects(proposal["ops"]) {
			if id := op.str("id"); id != "" {
				pending[id] = true
			} else {
				pending[op.str("from")] = true
				pending[op.str("to")] = true
			}
		}
	}
	for _, node := range nodes {
		id := node.str("id")
		if pending[id] || !dependenciesDone(node, status) {
			continue
		}
		switch node.str("kind") {
		case "milestone":
			if node["gate"] == nil && status[id] != "done" {
				if err := openAutopilotGate(w, tx, project.ID, id, node.str("title"), locale); err != nil {
					return err
				}
			}
		case "task":
			if slots <= 0 || outOfBudget || len(node["threads"].([]object)) > 0 {
				continue
			}
			title := node.str("title")
			brief := fmt.Sprintf("开始做「%s」，它依赖的节点都已完成。", title)
			if locale == "en" {
				brief = fmt.Sprintf("Start on %q: the nodes it depends on are done.", title)
			}
			thread, err := repo.createThread(ctx, tx, project.ID, NewThread{Title: title, NodeID: &id, Auto: true})
			if err != nil {
				return err
			}
			if _, err := postMessage(ctx, tx, project.ID, MessageInput{ConversationID: thread.ID, Author: "coordinator", Kind: "relay", Content: brief}); err != nil {
				return err
			}
			slots--
		}
	}
	return nil
}

func dependenciesDone(node object, status map[string]string) bool {
	for _, dependency := range node["dependsOn"].([]string) {
		if status[dependency] != "done" {
			return false
		}
	}
	return true
}

func markPastMilestones(nodes []object, graph map[string]object, status map[string]string) {
	queue := []string{}
	for _, node := range nodes {
		if len(node["threads"].([]object)) > 0 {
			queue = append(queue, node["dependsOn"].([]string)...)
		}
	}
	seen := map[string]bool{}
	for len(queue) > 0 {
		id := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		node := graph[id]
		if node == nil || seen[id] {
			continue
		}
		seen[id] = true
		if node.str("kind") == "milestone" && node["gate"] == nil {
			status[id] = "done"
		}
		queue = append(queue, node["dependsOn"].([]string)...)
	}
}

func openAutopilotGate(w *projectionWriter, tx *eventstore.Transaction, projectID, nodeID, title, locale string) error {
	if !mayAskGate(w.all(milestoneHistory, projectID, milestoneGate+nodeID)) || w.err != nil {
		return w.err
	}
	question := fmt.Sprintf("「%s」之前的都完成了，进入下一阶段？", title)
	goOn, wait := "进入下一阶段", "先不"
	if locale == "en" {
		question = fmt.Sprintf("Everything before %q is done. Go on to the next stage?", title)
		goOn, wait = "Go on", "Not yet"
	}
	id, err := newID("att")
	if err != nil {
		return err
	}
	purpose := milestoneGate + nodeID
	block := ConfirmBlock(object{"title": question, "approveText": goOn, "denyText": wait})
	opened, err := attentionOpened(id, AttentionInput{Purpose: &purpose, Block: block}, false)
	if err != nil {
		return err
	}
	return appendChanges(tx, projectID, []change{opened})
}
