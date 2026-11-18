package core

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/eventstore"
	"github.com/daidr/Ariadne/internal/projectsettings"
)

type PlanConfirmation func([]map[string]any) (map[string]any, error)
type PlanRevision struct {
	Author       string
	Reason       string
	Ops          []map[string]any
	Confirmation PlanConfirmation
	UniqueAdds   bool
}

func mapOps(ops []map[string]any) []object {
	result := make([]object, len(ops))
	for i, op := range ops {
		result[i] = op
	}
	return result
}
func ParsePlanOps(raw any) ([]map[string]any, error) {
	ops, err := parsePlanOps(raw)
	if err != nil {
		return nil, err
	}
	result := make([]map[string]any, len(ops))
	for i, op := range ops {
		result[i] = op
	}
	return result, nil
}
func planningThreads(w *projectionWriter, projectID, by string, now int64) []ThreadView {
	threads, err := listThreads(w.ctx, w.tx, projectID, now)
	if err != nil {
		w.err = err
		return nil
	}
	result := []ThreadView{}
	for _, thread := range threads {
		if thread.Stage != "done" && (thread.Stopped == nil || by == "user" && thread.Stopped.By != "user") {
			result = append(result, thread)
		}
	}
	return result
}
func describePlanChange(w *projectionWriter, projectID string, ops []object, now int64) []object {
	threads := planningThreads(w, projectID, "coordinator", now)
	graph := loadPlanGraph(w, projectID)
	return describePlanOps(graph, ops, func(nodeID string) int {
		count := 0
		for _, thread := range threads {
			if thread.NodeID == nodeID {
				count++
			}
		}
		return count
	})
}
func stopsForRemoved(w *projectionWriter, projectID string, before, after planGraph, reason, by string, now int64) []change {
	removed := map[string]bool{}
	for id := range before.nodes {
		if after.nodes[id] == nil {
			removed[id] = true
		}
	}
	if len(removed) == 0 {
		return nil
	}
	changes := []change{}
	for _, thread := range planningThreads(w, projectID, by, now) {
		nodeID, _ := thread.NodeID.(string)
		if nodeID != "" && removed[nodeID] {
			changes = append(changes, change{"thread.stopped", object{"threadId": thread.ID, "reason": reason, "by": by}})
		}
	}
	return changes
}
func changePlan(ctx context.Context, tx *eventstore.Transaction, projectID, author, reason string, ops []object, revertOf any, confirmation PlanConfirmation, now int64, uniqueAdds bool) (object, error) {
	w := &projectionWriter{ctx: ctx, tx: tx.SQL()}
	graph := loadPlanGraph(w, projectID)
	if w.err != nil {
		return nil, w.err
	}
	if uniqueAdds {
		if err := checkAddedPlanTitles(graph, ops); err != nil {
			return nil, err
		}
	}
	after, _, err := applyOps(graph, ops)
	if err != nil {
		return nil, err
	}
	if author == "coordinator" && needsConfirmation(graph, ops) {
		if confirmation == nil {
			return nil, badInput("confirmation")
		}
		proposalID, err := newID("prp")
		if err != nil {
			return nil, err
		}
		attentionID, err := newID("att")
		if err != nil {
			return nil, err
		}
		described := describePlanChange(w, projectID, ops, now)
		if w.err != nil {
			return nil, w.err
		}
		lines := make([]map[string]any, len(described))
		for i, line := range described {
			lines[i] = line
		}
		block, err := confirmation(lines)
		if err != nil {
			return nil, err
		}
		attention, err := attentionOpened(attentionID, AttentionInput{Block: block}, false)
		if err != nil {
			return nil, err
		}
		if err := appendChanges(tx, projectID, []change{attention, {"plan.proposed", object{"proposalId": proposalID, "attentionId": attentionID, "reason": reason, "ops": ops, "revertOf": revertOf}}}); err != nil {
			return nil, err
		}
		return object{"status": "proposed", "proposalId": proposalID, "attentionId": attentionID}, nil
	}
	revisionID, err := newID("rev")
	if err != nil {
		return nil, err
	}
	changes := []change{{"plan.revised", object{"revisionId": revisionID, "author": author, "reason": reason, "ops": ops, "revertOf": revertOf, "proposalId": nil}}}
	changes = append(changes, stopsForRemoved(w, projectID, graph, after, reason, author, now)...)
	if w.err != nil {
		return nil, w.err
	}
	if err := appendChanges(tx, projectID, changes); err != nil {
		return nil, err
	}
	return object{"status": "applied", "revisionId": revisionID}, nil
}
func (repo *Repository) RevisePlan(ctx context.Context, projectID string, input PlanRevision) (result object, err error) {
	err = repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		rows, err := records(ctx, tx.SQL(), "SELECT id FROM projects WHERE id = ?", projectID)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return domainerr.NotFound(projectID)
		}
		reason := projectsettings.Trim(input.Reason)
		if reason == "" {
			return badInput("reason")
		}
		if len(input.Ops) == 0 {
			return badInput("ops")
		}
		result, err = changePlan(ctx, tx, projectID, input.Author, reason, mapOps(input.Ops), nil, input.Confirmation, repo.Reader.now(), input.UniqueAdds)
		return err
	})
	return
}

func checkAddedPlanTitles(graph planGraph, ops []object) error {
	normalize := func(title string) string {
		return strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) || unicode.IsPunct(r) {
				return -1
			}
			return unicode.ToLower(r)
		}, title)
	}
	changed := map[string]bool{}
	known := map[string]bool{}
	for _, op := range ops {
		if op.str("op") == "remove" || op.str("op") == "update" && op.str("title") != "" {
			changed[op.str("id")] = true
			if title := op.str("title"); title != "" {
				known[normalize(title)] = true
			}
		}
	}
	for id, node := range graph.nodes {
		if !changed[id] {
			known[normalize(node.str("title"))] = true
		}
	}
	duplicate := []string{}
	for _, op := range ops {
		if op.str("op") != "add" {
			continue
		}
		title := op.str("title")
		key := normalize(title)
		if known[key] {
			duplicate = append(duplicate, title)
		}
		known[key] = true
	}
	if len(duplicate) > 0 {
		return invalidPlan("nodes already exist with these titles: " + strings.Join(duplicate, ", "))
	}
	return nil
}
func (reader Reader) DescribePlanChange(ctx context.Context, projectID string, ops []map[string]any) (result []object, err error) {
	err = reader.snapshot(ctx, func(tx *sql.Tx) error {
		w := &projectionWriter{ctx: ctx, tx: tx}
		result = describePlanChange(w, projectID, mapOps(ops), reader.now())
		return w.err
	})
	return
}

type planUndo struct {
	graph  planGraph
	undo   []object
	titles map[string]string
	reason string
}

func undoOf(w *projectionWriter, projectID, revisionID string) (planUndo, error) {
	row := w.one("SELECT r.ops, r.inverse, r.reverted_by, r.reason, u.revert_of IS NOT NULL AS undone_is_undo FROM plan_revisions r LEFT JOIN plan_revisions u ON u.id = r.revert_of WHERE r.id = ? AND r.project_id = ?", revisionID, projectID)
	if w.err != nil {
		return planUndo{}, w.err
	}
	if row == nil {
		return planUndo{}, domainerr.NotFound(revisionID)
	}
	if row.text("reverted_by") != "" {
		return planUndo{}, domainerr.New("error.planReverted", nil)
	}
	if row.flag("undone_is_undo") {
		return planUndo{}, domainerr.New("error.planUndoRestore", nil)
	}
	undo := objects(w.parsed(row, "inverse", []any{}))
	ops := objects(w.parsed(row, "ops", []any{}))
	titles := map[string]string{}
	for _, op := range append(append([]object{}, undo...), ops...) {
		if (op.str("op") == "add" || op.str("op") == "update") && op.str("title") != "" {
			titles[op.str("id")] = op.str("title")
		}
	}
	graph := loadPlanGraph(w, projectID)
	return planUndo{graph: graph, undo: undo, titles: titles, reason: row.text("reason")}, w.err
}
func (reader Reader) PreviewPlanUndo(ctx context.Context, projectID, revisionID string) (result object, err error) {
	err = reader.snapshot(ctx, func(tx *sql.Tx) error {
		w := &projectionWriter{ctx: ctx, tx: tx}
		undo, err := undoOf(w, projectID, revisionID)
		if err != nil {
			return err
		}
		blocked := whyUndoFails(undo.graph, undo.undo, undo.titles)
		if blocked != nil {
			result = object{"changes": []object{}, "threads": []object{}, "blocked": blocked}
			return nil
		}
		after, _, err := applyOps(undo.graph, undo.undo)
		if err != nil {
			return err
		}
		title := func(id string) string {
			if node := undo.graph.nodes[id]; node != nil {
				return node.str("title")
			}
			return id
		}
		changes := describePlanOps(undo.graph, undo.undo, func(string) int { return 0 })
		unlinked := map[string]bool{}
		for _, op := range undo.undo {
			if op.str("op") == "unlink" {
				unlinked[op.str("from")+">"+op.str("to")] = true
			}
		}
		for _, key := range undo.graph.edges {
			from, to := edgeParts(key)
			if !unlinked[key] {
				found := false
				for _, kept := range after.edges {
					found = found || kept == key
				}
				if !found {
					changes = append(changes, object{"key": "plan.op.unlink", "params": object{"from": title(from), "to": title(to)}})
				}
			}
		}
		threads := []object{}
		for _, thread := range planningThreads(w, projectID, "user", reader.now()) {
			nodeID, _ := thread.NodeID.(string)
			if nodeID != "" && undo.graph.nodes[nodeID] != nil && after.nodes[nodeID] == nil {
				threads = append(threads, object{"id": thread.ID, "title": thread.Title})
			}
		}
		result = object{"changes": changes, "threads": threads, "blocked": nil}
		return w.err
	})
	return
}
func (repo *Repository) RevertPlanRevision(ctx context.Context, projectID, revisionID, author, reason string, confirmation PlanConfirmation) (result object, err error) {
	err = repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		w := &projectionWriter{ctx: ctx, tx: tx.SQL()}
		undo, err := undoOf(w, projectID, revisionID)
		if err != nil {
			return err
		}
		if blocked := asObject(whyUndoFails(undo.graph, undo.undo, undo.titles)); blocked != nil {
			return domainerr.New(blocked.str("key"), blocked.obj("params"))
		}
		clean := projectsettings.Trim(reason)
		if clean == "" {
			clean = undo.reason
		}
		result, err = changePlan(ctx, tx, projectID, author, clean, undo.undo, revisionID, confirmation, repo.Reader.now(), false)
		return err
	})
	return
}
func (repo *Repository) DismissPlanProposal(ctx context.Context, projectID, id string) error {
	return repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		rows, err := records(ctx, tx.SQL(), "SELECT status FROM plan_proposals WHERE id = ? AND project_id = ?", id, projectID)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return domainerr.NotFound(id)
		}
		if rows[0].text("status") != "stale" {
			return nil
		}
		return appendChanges(tx, projectID, []change{{"plan.proposal.dismissed", object{"proposalId": id}}})
	})
}
func (repo *Repository) AnswerAttention(ctx context.Context, id string, input any) error {
	return repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		w := &projectionWriter{ctx: ctx, tx: tx.SQL()}
		row := w.one("SELECT * FROM attentions WHERE id = ?", id)
		if w.err != nil {
			return w.err
		}
		if row.present("withdrawn") {
			return domainerr.New("error.askWithdrawn", map[string]any{"reason": row["withdrawn"]})
		}
		if row == nil || row.flag("closed") {
			return domainerr.NotFound(id)
		}
		block := asObject(w.parsed(row, "block", nil))
		if w.err != nil {
			return w.err
		}
		answer, err := ParseAnswer(block, input)
		if err != nil {
			return err
		}
		projectID := row.text("project_id")
		changes := []change{{"attention.answered", object{"attentionId": id, "answer": answer}}}
		proposal := w.one("SELECT id, reason, ops, revert_of FROM plan_proposals WHERE attention_id = ? AND status = 'open'", id)
		if w.err != nil {
			return w.err
		}
		if proposal != nil && answer.str("type") == "confirm" && answer.yes("approved") {
			ops := objects(w.parsed(proposal, "ops", []any{}))
			before := loadPlanGraph(w, projectID)
			if w.err != nil {
				return w.err
			}
			after, _, err := applyOps(before, ops)
			if err == nil {
				revisionID, err := newID("rev")
				if err != nil {
					return err
				}
				changes = append(changes, change{"plan.revised", object{"revisionId": revisionID, "author": "coordinator", "reason": proposal["reason"], "ops": ops, "revertOf": proposal["revert_of"], "proposalId": proposal["id"]}})
				changes = append(changes, stopsForRemoved(w, projectID, before, after, proposal.text("reason"), "coordinator", repo.Reader.now())...)
			} else {
				var domain *domainerr.Error
				if !errors.As(err, &domain) {
					return err
				}
				reason := domain.Key
				if value := domain.Params["reason"]; value != nil {
					reason = jsString(value)
				}
				changes = append(changes, change{"plan.proposal.stale", object{"proposalId": proposal["id"], "reason": reason}})
			}
		}
		resumed := resumedByUser(w, row.text("thread_id"))
		if w.err != nil {
			return w.err
		}
		return appendChanges(tx, projectID, append(resumed, changes...))
	})
}
func (repo *Repository) WithdrawAttention(ctx context.Context, conversationID, id, reason string) error {
	return repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		rows, err := records(ctx, tx.SQL(), "SELECT project_id, conversation_id, message_id, closed FROM attentions WHERE id = ?", id)
		if err != nil {
			return err
		}
		if len(rows) == 0 || rows[0].text("conversation_id") != conversationID || !rows[0].present("message_id") {
			return domainerr.NotFound(id)
		}
		if rows[0].flag("closed") {
			return domainerr.New("error.askClosed", nil)
		}
		text := utf16Slice(projectsettings.Trim(spaces.ReplaceAllString(reason, " ")), 0, 200)
		if text == "" {
			return badInput("reason")
		}
		return appendChanges(tx, rows[0].text("project_id"), []change{{"attention.withdrawn", object{"attentionId": id, "reason": text}}})
	})
}
