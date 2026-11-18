package core

import (
	"context"
	"encoding/json"

	"github.com/daidr/Ariadne/internal/eventstore"
)

type activeIssueWrite struct{ ProjectID, ThreadID string }
type IssueWrite struct {
	ThreadID    string `json:"threadId"`
	AttentionID string `json:"attentionId"`
	Purpose     string `json:"purpose"`
	Tracker     string `json:"tracker"`
	Number      int    `json:"number"`
	Action      string `json:"action"`
	Text        string `json:"text"`
}
type IssueApprovalResult struct {
	State       string  `json:"state"`
	AttentionID string  `json:"attentionId,omitempty"`
	By          string  `json:"by,omitempty"`
	URL         *string `json:"url,omitempty"`
}

func (result IssueApprovalResult) MarshalJSON() ([]byte, error) {
	value := object{"state": result.State}
	switch result.State {
	case "approved", "unsure":
		value["attentionId"] = result.AttentionID
	case "writing":
		value["by"] = result.By
	case "made":
		value["by"] = result.By
		value["url"] = result.URL
	}
	return json.Marshal(value)
}
func (repo *Repository) IssueApproval(ctx context.Context, projectID, threadID, purpose string, card ApprovalCard) (result IssueApprovalResult, err error) {
	err = repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		w := &projectionWriter{ctx: ctx, tx: tx.SQL()}
		if err := assertThreadIn(w, projectID, threadID); err != nil {
			return err
		}
		newest := w.one(`SELECT a.id, a.closed, a.approved, a.thread_id, w.status, w.thread_id AS writer, w.url, w.written_seq
		 FROM attentions a LEFT JOIN issue_writes w ON w.attention_id = a.id
		 WHERE a.project_id = ? AND a.purpose = ? ORDER BY a.created_at DESC, a.id DESC LIMIT 1`, projectID, purpose)
		if w.err != nil {
			return w.err
		}
		ask := func() error {
			result.State = "asked"
			return askApproval(tx, projectID, threadID, purpose, false, card, CallApprovalOptions{})
		}
		if newest == nil {
			return ask()
		}
		id := newest.text("id")
		onCard := onApprovalCard(w, id, newest.text("thread_id"), threadID)
		if w.err != nil {
			return w.err
		}
		join := func() error {
			if onCard {
				return nil
			}
			return appendChanges(tx, projectID, []change{{"attention.joined", object{"attentionId": id, "threadId": threadID}}})
		}
		if !newest.flag("closed") {
			result.State = "pending"
			return join()
		}
		if newest.flag("approved") {
			if !newest.present("status") {
				result = IssueApprovalResult{State: "approved", AttentionID: id}
				return nil
			}
			if newest.text("status") == "writing" {
				write, active := repo.issueWrites[id]
				if !active {
					result = IssueApprovalResult{State: "unsure", AttentionID: id}
					return nil
				}
				result = IssueApprovalResult{State: "writing", By: write.ThreadID}
				if write.ThreadID != threadID {
					return join()
				}
				return nil
			}
			if newest.text("status") == "written" && onCard && newest.text("writer") != "" && newest.text("writer") != threadID {
				seen := w.one("SELECT json_extract(payload, '$.sawSeq') AS saw FROM transcript WHERE conversation_id = ? AND kind = 'step' ORDER BY id DESC LIMIT 1", threadID)
				if w.err != nil {
					return w.err
				}
				if seen.integer("saw") < newest.integer("written_seq") {
					var url *string
					if newest.present("url") {
						text := newest.text("url")
						url = &text
					}
					result = IssueApprovalResult{State: "made", By: newest.text("writer"), URL: url}
					return nil
				}
			}
		}
		if newest.present("approved") && newest.integer("approved") == 0 && onCard {
			result.State = "declined"
			return nil
		}
		return ask()
	})
	return
}
func (repo *Repository) StartIssueWrite(ctx context.Context, projectID string, write IssueWrite) error {
	return repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		w := &projectionWriter{ctx: ctx, tx: tx.SQL()}
		if err := assertThreadIn(w, projectID, write.ThreadID); err != nil {
			return err
		}
		used := w.one("SELECT 1 FROM issue_writes WHERE attention_id = ?", write.AttentionID)
		if w.err != nil {
			return w.err
		}
		if used != nil {
			return badInput("approval used")
		}
		if err := appendChanges(tx, projectID, []change{{"issue.writing", write}}); err != nil {
			return err
		}
		tx.AfterCommit(func() {
			repo.issueWrites[write.AttentionID] = activeIssueWrite{ProjectID: projectID, ThreadID: write.ThreadID}
		})
		return nil
	})
}
func (repo *Repository) FinishIssueWrite(ctx context.Context, projectID string, write IssueWrite, url *string) error {
	return repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		data := object{"threadId": write.ThreadID, "attentionId": write.AttentionID, "purpose": write.Purpose, "tracker": write.Tracker, "number": write.Number, "action": write.Action, "text": write.Text, "url": url}
		if err := appendChanges(tx, projectID, []change{{"issue.written", data}}); err != nil {
			return err
		}
		tx.AfterCommit(func() { delete(repo.issueWrites, write.AttentionID) })
		return nil
	})
}
func (repo *Repository) MarkIssueWriteUnsure(ctx context.Context, projectID, attentionID, threadID string) error {
	return repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		if err := appendChanges(tx, projectID, []change{{"issue.write_unsure", object{"attentionId": attentionID, "threadId": threadID}}}); err != nil {
			return err
		}
		tx.AfterCommit(func() { delete(repo.issueWrites, attentionID) })
		return nil
	})
}
