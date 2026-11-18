package core

import (
	"context"
	"math"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/eventstore"
	"github.com/daidr/Ariadne/internal/projectsettings"
	"github.com/google/uuid"
)

func newID(prefix string) (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", err
	}
	return prefix + "_" + id.String(), nil
}
func (repo *Repository) mutateThread(ctx context.Context, threadID string, mutate func(*eventstore.Transaction, ThreadView) ([]change, error)) (result ThreadView, err error) {
	err = repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		rows, err := records(ctx, tx.SQL(), threadSelect+" WHERE t.id = ?", threadID)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return domainerr.NotFound(threadID)
		}
		current, err := toThread(rows[0], repo.Reader.now())
		if err != nil {
			return err
		}
		changes, err := mutate(tx, current)
		if err != nil {
			return err
		}
		if len(changes) == 0 {
			result = current
			return nil
		}
		if err := appendChanges(tx, current.ProjectID, changes); err != nil {
			return err
		}
		rows, err = records(ctx, tx.SQL(), threadSelect+" WHERE t.id = ?", threadID)
		if err != nil {
			return err
		}
		result, err = toThread(rows[0], repo.Reader.now())
		return err
	})
	return
}

type NewThread struct {
	Title  string
	NodeID *string
	Model  *string
	Branch *string
	Auto   bool
}

func (repo *Repository) CreateThread(ctx context.Context, projectID string, input NewThread) (result ThreadView, err error) {
	err = repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		result, err = repo.createThread(ctx, tx, projectID, input)
		return err
	})
	return
}
func (repo *Repository) createThread(ctx context.Context, tx *eventstore.Transaction, projectID string, input NewThread) (ThreadView, error) {
	rows, err := records(ctx, tx.SQL(), "SELECT id FROM projects WHERE id = ?", projectID)
	if err != nil {
		return ThreadView{}, err
	}
	if len(rows) == 0 {
		return ThreadView{}, domainerr.NotFound(projectID)
	}
	title := projectsettings.Trim(input.Title)
	if title == "" {
		return ThreadView{}, badInput("title")
	}
	if input.NodeID != nil && *input.NodeID != "" {
		nodes, err := records(ctx, tx.SQL(), "SELECT id FROM plan_nodes WHERE project_id = ? AND id = ?", projectID, *input.NodeID)
		if err != nil {
			return ThreadView{}, err
		}
		if len(nodes) == 0 {
			return ThreadView{}, domainerr.NotFound(*input.NodeID)
		}
	}
	id, err := newID("thr")
	if err != nil {
		return ThreadView{}, err
	}
	var branch any
	if input.Branch != nil {
		branch = *input.Branch
	}
	data := object{"threadId": id, "title": title, "nodeId": input.NodeID, "model": input.Model, "branch": projectsettings.BranchSlug(branch)}
	if input.Auto {
		data["auto"] = true
	}
	if err := appendChanges(tx, projectID, []change{{"thread.created", data}}); err != nil {
		return ThreadView{}, err
	}
	rows, err = records(ctx, tx.SQL(), threadSelect+" WHERE t.id = ?", id)
	if err != nil {
		return ThreadView{}, err
	}
	return toThread(rows[0], repo.Reader.now())
}
func (repo *Repository) StartAutoThread(ctx context.Context, projectID, title, nodeID, brief string) (ThreadView, error) {
	return repo.CreateBriefedThread(ctx, projectID, NewThread{Title: title, NodeID: &nodeID, Auto: true}, brief, nil)
}

func (repo *Repository) CreateBriefedThread(ctx context.Context, projectID string, input NewThread, brief string, cites []string) (result ThreadView, err error) {
	err = repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		thread, err := repo.createThread(ctx, tx, projectID, input)
		if err != nil {
			return err
		}
		if _, err := postMessage(ctx, tx, projectID, MessageInput{ConversationID: thread.ID, Author: "coordinator", Kind: "relay", Content: brief, Cites: cites}); err != nil {
			return err
		}
		rows, err := records(ctx, tx.SQL(), threadSelect+" WHERE t.id = ?", thread.ID)
		if err != nil {
			return err
		}
		result, err = toThread(rows[0], repo.Reader.now())
		return err
	})
	return
}
func (repo *Repository) SetThreadModel(ctx context.Context, threadID string, model *string, by string) (ThreadView, error) {
	return repo.mutateThread(ctx, threadID, func(_ *eventstore.Transaction, current ThreadView) ([]change, error) {
		if encoded(current.Model) == encoded(model) {
			return nil, nil
		}
		return []change{{"thread.model_set", object{"threadId": threadID, "model": model, "by": by}}}, nil
	})
}
func (repo *Repository) StartThread(ctx context.Context, threadID string) (ThreadView, error) {
	return repo.mutateThread(ctx, threadID, func(_ *eventstore.Transaction, current ThreadView) ([]change, error) {
		return []change{{"thread.started", object{"threadId": threadID}}}, nil
	})
}
func (repo *Repository) RenameThread(ctx context.Context, threadID, title, by string) (ThreadView, error) {
	return repo.mutateThread(ctx, threadID, func(_ *eventstore.Transaction, current ThreadView) ([]change, error) {
		clean := projectsettings.Trim(spaces.ReplaceAllString(title, " "))
		if clean == "" || len([]rune(clean)) > 80 {
			return nil, badInput("title")
		}
		if clean == current.Title {
			return nil, nil
		}
		return []change{{"thread.renamed", object{"threadId": threadID, "title": clean, "by": by}}}, nil
	})
}
func (repo *Repository) SetAside(ctx context.Context, threadID string, days int, reason, by string) (ThreadView, error) {
	return repo.mutateThread(ctx, threadID, func(_ *eventstore.Transaction, current ThreadView) ([]change, error) {
		now := repo.Reader.now()
		if days < 1 || int64(days) > (math.MaxInt64-now)/86400000 {
			return nil, badInput("days")
		}
		clean := projectsettings.Trim(spaces.ReplaceAllString(reason, " "))
		clean = utf16Slice(clean, 0, 200)
		return []change{{"thread.set_aside", object{"threadId": threadID, "until": now + int64(days)*86400000, "reason": clean, "by": by}}}, nil
	})
}
func (repo *Repository) EndAside(ctx context.Context, threadID, by string) (ended bool, err error) {
	_, err = repo.mutateThread(ctx, threadID, func(_ *eventstore.Transaction, current ThreadView) ([]change, error) {
		if current.Aside == nil {
			return nil, nil
		}
		ended = true
		return []change{{"thread.aside_ended", object{"threadId": threadID, "by": by}}}, nil
	})
	return
}
func (repo *Repository) AsideDue(ctx context.Context, threadID string) error {
	_, err := repo.mutateThread(ctx, threadID, func(_ *eventstore.Transaction, current ThreadView) ([]change, error) {
		if current.Aside == nil || current.Aside.Due {
			return nil, nil
		}
		return []change{{"thread.aside_due", object{"threadId": threadID}}}, nil
	})
	return err
}
func (repo *Repository) CompleteThread(ctx context.Context, threadID string) (ThreadView, error) {
	return repo.mutateThread(ctx, threadID, func(_ *eventstore.Transaction, current ThreadView) ([]change, error) {
		if current.Stage != "review" {
			return nil, domainerr.New("error.threadNotInReview", nil)
		}
		return []change{{"thread.reviewed", object{"threadId": threadID}}}, nil
	})
}

// FinishThread records an explicit request to close a discussion without
// changing its workspace, branch, or previous report contents.
func (repo *Repository) FinishThread(ctx context.Context, threadID, by string) (ThreadView, error) {
	return repo.mutateThread(ctx, threadID, func(tx *eventstore.Transaction, current ThreadView) ([]change, error) {
		if current.Stage == "answer" {
			return nil, badInput("the thread still has a question waiting for an answer")
		}
		conv, err := conversation(ctx, tx.SQL(), threadID)
		if err != nil {
			return nil, err
		}
		if conv.Turn != nil || (current.Review != nil && current.Review.Reviewing) {
			return nil, domainerr.New("error.merge.working", nil)
		}
		if current.Stage == "done" && current.Aside == nil && conv.Wait == nil {
			return nil, nil
		}
		var changes []change
		if wait := conv.Wait; wait != nil {
			changes = append(changes, change{"conversation.wait_ended", object{"conversationId": threadID,
				"waitId": wait.ID, "how": "cancelled", "by": by, "until": wait.Until, "note": wait.Note}})
		}
		changes = append(changes, change{"thread.completed", object{"threadId": threadID, "by": by, "summary": current.LastSummary}})
		return changes, nil
	})
}

func (repo *Repository) ResumeThread(ctx context.Context, threadID, by string) (ThreadView, error) {
	return repo.mutateThread(ctx, threadID, func(_ *eventstore.Transaction, current ThreadView) ([]change, error) {
		if current.Stopped == nil {
			return nil, nil
		}
		if current.Stopped.By == "user" && by != "user" {
			return nil, badInput("only the user can resume this thread")
		}
		return []change{{"thread.resumed", object{"threadId": threadID, "by": by, "reason": current.Stopped.Reason}}}, nil
	})
}
func abandonedReview(w *projectionWriter, conversationID string) []change {
	reviewer := w.one("SELECT review_of, project_id FROM threads WHERE id = ?", conversationID)
	if reviewer.text("review_of") == "" {
		return nil
	}
	author := w.one("SELECT reviewing FROM threads WHERE id = ?", reviewer["review_of"])
	round := w.one("SELECT * FROM reviews WHERE thread_id = ? ORDER BY id DESC LIMIT 1", reviewer["review_of"])
	if !author.flag("reviewing") || round == nil || round.present("verdict") || round.present("abandoned_at") || round.text("reviewer_id") != conversationID {
		return nil
	}
	return []change{{"review.abandoned", object{"threadId": reviewer["review_of"], "reviewerId": conversationID, "round": round["round"]}}}
}
func (repo *Repository) StopThread(ctx context.Context, threadID, reason, by string) (ThreadView, error) {
	return repo.mutateThread(ctx, threadID, func(tx *eventstore.Transaction, current ThreadView) ([]change, error) {
		takeover := current.Stopped != nil && by == "user" && current.Stopped.By != "user"
		if current.Stopped != nil && !takeover {
			return nil, nil
		}
		clean := projectsettings.Trim(reason)
		if clean == "" && takeover {
			clean = current.Stopped.Reason
		}
		if clean == "" {
			return nil, badInput("reason")
		}
		w := &projectionWriter{ctx: ctx, tx: tx.SQL()}
		conversation := w.one("SELECT wait_id, wait_until, wait_note FROM conversations WHERE id = ?", threadID)
		changes := []change{}
		if conversation.present("wait_id") {
			changes = append(changes, change{"conversation.wait_ended", object{"conversationId": threadID, "waitId": conversation["wait_id"], "how": "cancelled", "by": by, "until": conversation["wait_until"], "note": conversation["wait_note"]}})
		}
		changes = append(changes, change{"thread.stopped", object{"threadId": threadID, "reason": clean, "by": by}})
		if current.Stopped == nil {
			changes = append(changes, abandonedReview(w, threadID)...)
		}
		return changes, w.err
	})
}
