package core

import (
	"context"
	"database/sql"
	"math"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/eventstore"
)

func assertNode(w *projectionWriter, projectID, nodeID string) error {
	project := w.one("SELECT id FROM projects WHERE id = ?", projectID)
	if w.err != nil {
		return w.err
	}
	if project == nil {
		return domainerr.NotFound(projectID)
	}
	node := w.one("SELECT id FROM plan_nodes WHERE project_id = ? AND id = ?", projectID, nodeID)
	if w.err != nil {
		return w.err
	}
	if node == nil {
		return domainerr.New("error.planInvalid", map[string]any{"reason": "no node " + nodeID})
	}
	return nil
}
func (reader Reader) RequirePlanNode(ctx context.Context, projectID, nodeID string) error {
	return reader.snapshot(ctx, func(tx *sql.Tx) error { return assertNode(&projectionWriter{ctx: ctx, tx: tx}, projectID, nodeID) })
}
func (reader Reader) PlanIssues(ctx context.Context, projectID, nodeID string) ([]LinkedIssue, error) {
	rows, err := reader.DB.QueryContext(ctx, "SELECT tracker,number,title,url FROM plan_node_issues WHERE project_id = ? AND node_id = ? ORDER BY tracker,number", projectID, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	issues := []LinkedIssue{}
	for rows.Next() {
		var issue LinkedIssue
		if err := rows.Scan(&issue.Tracker, &issue.Number, &issue.Title, &issue.URL); err != nil {
			return nil, err
		}
		issues = append(issues, issue)
	}
	return issues, rows.Err()
}
func (repo *Repository) AskGate(ctx context.Context, projectID, nodeID string, block map[string]any) (result AttentionView, err error) {
	err = repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		w := &projectionWriter{ctx: ctx, tx: tx.SQL()}
		if err := assertNode(w, projectID, nodeID); err != nil {
			return err
		}
		id, err := newID("att")
		if err != nil {
			return err
		}
		purpose := milestoneGate + nodeID
		opened, err := attentionOpened(id, AttentionInput{Purpose: &purpose, Block: block}, false)
		if err != nil {
			return err
		}
		if err := appendChanges(tx, projectID, []change{opened}); err != nil {
			return err
		}
		cards, err := openAttentions(ctx, tx.SQL(), projectID)
		if err != nil {
			return err
		}
		for _, card := range cards {
			if card.ID == id {
				result = card
				return nil
			}
		}
		return domainerr.NotFound(id)
	})
	return
}

type LinkedIssue struct {
	Tracker string  `json:"tracker"`
	Number  float64 `json:"number"`
	Title   string  `json:"title"`
	URL     string  `json:"url"`
}

func (repo *Repository) LinkIssue(ctx context.Context, projectID, nodeID string, issue LinkedIssue, by string) (result object, err error) {
	err = repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		w := &projectionWriter{ctx: ctx, tx: tx.SQL()}
		if err := assertNode(w, projectID, nodeID); err != nil {
			return err
		}
		if math.IsNaN(issue.Number) || math.IsInf(issue.Number, 0) || issue.Number < 1 || math.Trunc(issue.Number) != issue.Number {
			return badInput("number")
		}
		data := object{"nodeId": nodeID, "issue": object{"tracker": issue.Tracker, "number": issue.Number, "title": utf16Slice(issue.Title, 0, 300), "url": issue.URL}, "by": by}
		if err := appendChanges(tx, projectID, []change{{"plan.issue_linked", data}}); err != nil {
			return err
		}
		result, err = getPlan(w, projectID, repo.Reader.now())
		return err
	})
	return
}
func (repo *Repository) UnlinkIssue(ctx context.Context, projectID, nodeID, tracker string, number float64, by string) (result object, err error) {
	err = repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		w := &projectionWriter{ctx: ctx, tx: tx.SQL()}
		if err := assertNode(w, projectID, nodeID); err != nil {
			return err
		}
		linked := w.one("SELECT 1 FROM plan_node_issues WHERE project_id = ? AND node_id = ? AND tracker = ? AND number = ?", projectID, nodeID, tracker, number)
		if w.err != nil {
			return w.err
		}
		if linked != nil {
			if err := appendChanges(tx, projectID, []change{{"plan.issue_unlinked", object{"nodeId": nodeID, "tracker": tracker, "number": number, "by": by}}}); err != nil {
				return err
			}
		}
		result, err = getPlan(w, projectID, repo.Reader.now())
		return err
	})
	return
}
