package core

import (
	"context"
	"encoding/json"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/eventstore"
)

// ApprovalCard is evaluated only when a new question is needed.
type ApprovalCard func() map[string]any

func assertThreadIn(w *projectionWriter, projectID, threadID string) error {
	row := w.one("SELECT project_id FROM threads WHERE id = ?", threadID)
	if w.err != nil {
		return w.err
	}
	if row.text("project_id") != projectID {
		return domainerr.NotFound(threadID)
	}
	return nil
}
func onApprovalCard(w *projectionWriter, attentionID, asker, threadID string) bool {
	return asker == threadID || w.one("SELECT 1 FROM attention_waiters WHERE attention_id = ? AND thread_id = ?", attentionID, threadID) != nil
}
func openApproval(w *projectionWriter, projectID, purpose string) record {
	return w.one("SELECT id, thread_id FROM attentions WHERE project_id = ? AND purpose = ? AND closed = 0 ORDER BY created_at, id LIMIT 1", projectID, purpose)
}
func approvalFor(w *projectionWriter, projectID, threadID, purpose string) *string {
	state := "approved"
	if w.one("SELECT id FROM attentions WHERE project_id = ? AND purpose = ? AND closed = 1 AND approved = 1 LIMIT 1", projectID, purpose) != nil {
		return &state
	}
	state = "pending"
	if openApproval(w, projectID, purpose) != nil {
		return &state
	}
	state = "declined"
	if w.one(`SELECT a.approved FROM attentions a
	 WHERE a.project_id = ? AND a.purpose = ? AND a.closed = 1
	 AND (a.thread_id = ? OR EXISTS (SELECT 1 FROM attention_waiters w WHERE w.attention_id = a.id AND w.thread_id = ?))
	 ORDER BY a.created_at DESC, a.id DESC LIMIT 1`, projectID, purpose, threadID, threadID) != nil {
		return &state
	}
	return nil
}
func (reader Reader) ApprovalFor(ctx context.Context, projectID, threadID, purpose string) (*string, error) {
	tx, err := reader.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	w := &projectionWriter{ctx: ctx, tx: tx}
	state := approvalFor(w, projectID, threadID, purpose)
	return state, w.err
}
func askApproval(tx *eventstore.Transaction, projectID, conversationID, purpose string, coordinator bool, card ApprovalCard, options CallApprovalOptions) error {
	id, err := newID("att")
	if err != nil {
		return err
	}
	input := AttentionInput{Purpose: &purpose, Block: card()}
	if !coordinator {
		input.ThreadID = &conversationID
	}
	changes := []change{}
	if options.InChat {
		messageID, err := newID("msg")
		if err != nil {
			return err
		}
		input.MessageID = &messageID
		author := "thread"
		if coordinator {
			author = "coordinator"
		}
		changes = append(changes, change{"message.posted", object{"messageId": messageID, "conversationId": conversationID, "author": author, "kind": "chat", "content": ""}})
	}
	opened, err := attentionOpened(id, input, options.No != nil)
	if err != nil {
		return err
	}
	return appendChanges(tx, projectID, append(changes, opened))
}
func (repo *Repository) RequestApproval(ctx context.Context, projectID, threadID, purpose string, card ApprovalCard) (state string, err error) {
	err = repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		w := &projectionWriter{ctx: ctx, tx: tx.SQL()}
		current := approvalFor(w, projectID, threadID, purpose)
		if w.err != nil {
			return w.err
		}
		if current != nil && (*current == "approved" || *current == "declined") {
			state = *current
			return nil
		}
		if err := assertThreadIn(w, projectID, threadID); err != nil {
			return err
		}
		open := openApproval(w, projectID, purpose)
		if open != nil {
			state = "pending"
			onCard := onApprovalCard(w, open.text("id"), open.text("thread_id"), threadID)
			if w.err != nil {
				return w.err
			}
			if onCard {
				return nil
			}
			return appendChanges(tx, projectID, []change{{"attention.joined", object{"attentionId": open.text("id"), "threadId": threadID}}})
		}
		if w.err != nil {
			return w.err
		}
		state = "asked"
		return askApproval(tx, projectID, threadID, purpose, false, card, CallApprovalOptions{})
	})
	return
}

type CallApprovalOptions struct {
	InChat bool `json:"inChat"`
	No     *int `json:"no"`
}
type CallApprovalResult struct {
	State       string `json:"state"`
	AttentionID string `json:"attentionId,omitempty"`
	Choice      *int   `json:"choice,omitempty"`
}

// MarshalJSON retains a null choice on approved confirmations.
func (result CallApprovalResult) MarshalJSON() ([]byte, error) {
	value := object{"state": result.State}
	if result.State == "approved" {
		value["attentionId"] = result.AttentionID
		value["choice"] = result.Choice
	}
	return json.Marshal(value)
}
func (repo *Repository) CallApproval(ctx context.Context, projectID, conversationID, purpose string, card ApprovalCard, options CallApprovalOptions) (result CallApprovalResult, err error) {
	err = repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		if err := assertConversationIn(ctx, tx.SQL(), projectID, conversationID); err != nil {
			return err
		}
		w := &projectionWriter{ctx: ctx, tx: tx.SQL()}
		project := w.one("SELECT coordinator_id FROM projects WHERE id = ?", projectID)
		newest := w.one(`SELECT a.id, a.closed, a.approved, a.answer,
		 (SELECT 1 FROM used_approvals c WHERE c.attention_id = a.id) AS used FROM attentions a
		 WHERE a.project_id = ? AND a.purpose = ? AND a.conversation_id = ? ORDER BY a.created_at DESC, a.id DESC LIMIT 1`, projectID, purpose, conversationID)
		if w.err != nil {
			return w.err
		}
		ask := func() error {
			result.State = "asked"
			return askApproval(tx, projectID, conversationID, purpose, project.text("coordinator_id") == conversationID, card, options)
		}
		if newest == nil {
			return ask()
		}
		if !newest.flag("closed") {
			result.State = "pending"
			return nil
		}
		answer := asObject(w.parsed(newest, "answer", nil))
		if w.err != nil {
			return w.err
		}
		approved := newest.integer("approved")
		if !newest.present("approved") {
			approved = -1
		}
		var choice *int
		if answer.str("type") == "choice" {
			indexValue, _ := number(answer["choice"])
			index := int(indexValue)
			choice = &index
			approved = 1
			if options.No != nil && index == *options.No {
				approved = 0
			}
		}
		if approved == 1 && !newest.present("used") {
			result = CallApprovalResult{State: "approved", AttentionID: newest.text("id"), Choice: choice}
			return nil
		}
		if approved == 0 {
			userWrote := w.one(`SELECT 1 FROM transcript t JOIN messages m ON m.id = t.ref
			 WHERE t.conversation_id = ? AND t.kind = 'message' AND m.author = 'user'
			 AND t.seq > (SELECT a.seq FROM transcript a WHERE a.conversation_id = ?
			 AND a.kind = 'note' AND json_extract(a.payload, '$.attentionId') = ?) LIMIT 1`, conversationID, conversationID, newest.text("id")) != nil
			if w.err != nil {
				return w.err
			}
			if !userWrote {
				result.State = "declined"
				return nil
			}
		}
		return ask()
	})
	return
}
func (repo *Repository) UseApproval(ctx context.Context, projectID, conversationID, attentionID, what string) error {
	return repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		if err := assertConversationIn(ctx, tx.SQL(), projectID, conversationID); err != nil {
			return err
		}
		w := &projectionWriter{ctx: ctx, tx: tx.SQL()}
		used := w.one("SELECT 1 FROM used_approvals WHERE attention_id = ?", attentionID)
		if w.err != nil {
			return w.err
		}
		if used != nil {
			return badInput("approval used")
		}
		return appendChanges(tx, projectID, []change{{"approval.used", object{"threadId": conversationID, "attentionId": attentionID, "what": what}}})
	})
}
