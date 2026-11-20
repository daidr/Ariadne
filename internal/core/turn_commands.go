package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/eventstore"
	"github.com/daidr/Ariadne/internal/projectsettings"
	"github.com/daidr/Ariadne/sdk"
)

type Cost struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}
type TokenUsage struct {
	Input        int64  `json:"input"`
	Output       int64  `json:"output"`
	ContextInput *int64 `json:"contextInput,omitempty"`
	Cached       *int64 `json:"cached,omitempty"`
	CacheWrite   *int64 `json:"cacheWrite,omitempty"`
	Reasoning    *int64 `json:"reasoning,omitempty"`
	Cost         *Cost  `json:"cost,omitempty"`
}
type CallTiming struct {
	StartedAt     int64  `json:"startedAt"`
	FirstOutputAt *int64 `json:"firstOutputAt"`
}
type StepInput struct {
	System string `json:"system"`
	Tools  string `json:"tools"`
	Call   string `json:"call"`
	Images bool   `json:"images"`
}
type Step struct {
	Text      string
	Reasoning []sdk.Reasoning
	ToolCalls []sdk.ToolCall
	SawSeq    int64
	Usage     *TokenUsage
	Input     StepInput
	Timing    CallTiming
	Cut       bool
	Quiet     bool
}
type Compaction struct {
	Summary    string
	UpTo       float64
	SawSeq     int64
	Usage      *TokenUsage
	Call       string
	Timing     CallTiming
	Reasoning  []sdk.Reasoning
	State      string
	UserTokens int64
}
type ToolResult struct {
	ToolCallID string
	Name       string
	Content    string
	IsError    bool
	Files      []any
	Timing     any
}

func (repo *Repository) StartTurn(ctx context.Context, id, model string) (string, error) {
	return repo.startTurn(ctx, id, model, false)
}

// StartScheduledTurn rechecks the pending work after the scheduler has selected
// a conversation. An empty ID means the queued work was withdrawn meanwhile.
func (repo *Repository) StartScheduledTurn(ctx context.Context, id, model string) (string, error) {
	return repo.startTurn(ctx, id, model, true)
}

func (repo *Repository) startTurn(ctx context.Context, id, model string, scheduled bool) (turnID string, err error) {
	err = repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		current, err := conversation(ctx, tx.SQL(), id)
		if err != nil {
			return err
		}
		if current.Turn != nil {
			return domainerr.New("error.turnRunning", nil)
		}
		if scheduled && current.PendingSeq == 0 {
			return nil
		}
		changes := []change{}
		if current.Role == "thread" {
			rows, err := records(ctx, tx.SQL(), "SELECT started, stopped_reason FROM threads WHERE id = ?", id)
			if err != nil {
				return err
			}
			if scheduled && len(rows) > 0 && rows[0].present("stopped_reason") {
				return nil
			}
			if len(rows) > 0 && !rows[0].flag("started") {
				changes = append(changes, change{"thread.started", object{"threadId": id}})
			}
		}
		turnID, err = newID("trn")
		if err != nil {
			return err
		}
		changes = append(changes, change{"turn.started", object{"conversationId": id, "turnId": turnID, "model": model}})
		return appendChanges(tx, current.ProjectID, changes)
	})
	if err != nil {
		return "", err
	}
	return
}
func (repo *Repository) withTurn(ctx context.Context, id, turnID string, work func(*eventstore.Transaction, string) error) error {
	return repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		current, err := conversation(ctx, tx.SQL(), id)
		if err != nil {
			return err
		}
		if current.Turn == nil || current.Turn.ID != turnID {
			return domainerr.New("error.turnNotRunning", nil)
		}
		return work(tx, current.ProjectID)
	})
}

type promptKeeper struct {
	w         *projectionWriter
	projectID string
	seen      map[string]bool
	pieces    []change
}

func newPromptKeeper(w *projectionWriter, projectID string) *promptKeeper {
	return &promptKeeper{w: w, projectID: projectID, seen: map[string]bool{}, pieces: []change{}}
}
func (keeper *promptKeeper) keep(content string) string {
	digest := sha256.Sum256([]byte(content))
	hash := hex.EncodeToString(digest[:])
	if !keeper.seen[hash] {
		row := keeper.w.one("SELECT content FROM prompts WHERE project_id = ? AND hash = ?", keeper.projectID, hash)
		if row == nil && keeper.w.err == nil {
			keeper.pieces = append(keeper.pieces, change{"prompt.recorded", object{"hash": hash, "content": content}})
		}
	}
	keeper.seen[hash] = true
	return hash
}
func (repo *Repository) RecordStep(ctx context.Context, id, turnID string, step Step) error {
	return repo.withTurn(ctx, id, turnID, func(tx *eventstore.Transaction, projectID string) error {
		w := &projectionWriter{ctx: ctx, tx: tx.SQL()}
		keeper := newPromptKeeper(w, projectID)
		input := object{"system": keeper.keep(step.Input.System), "tools": keeper.keep(step.Input.Tools), "call": keeper.keep(step.Input.Call), "images": step.Input.Images}
		finish := "stop"
		if step.Cut {
			finish = "length"
		} else if len(step.ToolCalls) > 0 {
			finish = "tool-calls"
		}
		reasoning := step.Reasoning
		if reasoning == nil {
			reasoning = []sdk.Reasoning{}
		}
		calls := step.ToolCalls
		if calls == nil {
			calls = []sdk.ToolCall{}
		}
		data := object{"conversationId": id, "turnId": turnID, "text": step.Text, "reasoning": reasoning, "toolCalls": calls, "finish": finish, "sawSeq": step.SawSeq, "usage": step.Usage, "input": input, "timing": step.Timing}
		if step.Quiet {
			data["quiet"] = true
		}
		if w.err != nil {
			return w.err
		}
		return appendChanges(tx, projectID, append(keeper.pieces, change{"turn.step", data}))
	})
}
func (repo *Repository) RecordResearchStep(ctx context.Context, id, turnID string, step map[string]any) error {
	return repo.withTurn(ctx, id, turnID, func(tx *eventstore.Transaction, projectID string) error {
		w := &projectionWriter{ctx: ctx, tx: tx.SQL()}
		keeper := newPromptKeeper(w, projectID)
		input := asObject(step["input"])
		data := except(step, "")
		data["conversationId"] = id
		data["turnId"] = turnID
		data["input"] = object{"system": keeper.keep(input.str("system")), "tools": keeper.keep(input.str("tools")), "call": keeper.keep(input.str("call"))}
		if w.err != nil {
			return w.err
		}
		return appendChanges(tx, projectID, append(keeper.pieces, change{"research.step", data}))
	})
}

func (repo *Repository) RecordModelFailure(ctx context.Context, id, turnID string, saw int64, input StepInput, purpose, toolCallID string, failure any) error {
	return repo.withTurn(ctx, id, turnID, func(tx *eventstore.Transaction, projectID string) error {
		w := &projectionWriter{ctx: ctx, tx: tx.SQL()}
		keeper := newPromptKeeper(w, projectID)
		data := object{"conversationId": id, "turnId": turnID, "purpose": purpose, "toolCallId": toolCallID, "sawSeq": saw,
			"input": object{"system": keeper.keep(input.System), "tools": keeper.keep(input.Tools), "call": keeper.keep(input.Call), "images": input.Images}, "failure": failure}
		if w.err != nil {
			return w.err
		}
		return appendChanges(tx, projectID, append(keeper.pieces, change{"conversation.model_failed", data}))
	})
}
func (repo *Repository) RecordSearch(ctx context.Context, id, turnID, model string, usage *TokenUsage) error {
	return repo.withTurn(ctx, id, turnID, func(tx *eventstore.Transaction, projectID string) error {
		return appendChanges(tx, projectID, []change{{"search.answered", object{"conversationId": id, "turnId": turnID, "model": model, "usage": usage}}})
	})
}
func (repo *Repository) RecordToolResult(ctx context.Context, id, turnID string, result ToolResult) error {
	return repo.withTurn(ctx, id, turnID, func(tx *eventstore.Transaction, projectID string) error {
		data := object{"conversationId": id, "turnId": turnID, "toolCallId": result.ToolCallID, "name": result.Name, "content": result.Content, "isError": result.IsError}
		if len(result.Files) > 0 {
			data["files"] = result.Files
		}
		if result.Timing != nil {
			data["timing"] = result.Timing
		}
		return appendChanges(tx, projectID, []change{{"tool.finished", data}})
	})
}
func (repo *Repository) UpdatePrompt(ctx context.Context, id, turnID string, base bool, sections [][2]string, tools string, removedTools []string) error {
	return repo.withTurn(ctx, id, turnID, func(tx *eventstore.Transaction, projectID string) error {
		w := &projectionWriter{ctx: ctx, tx: tx.SQL()}
		keeper := newPromptKeeper(w, projectID)
		if sections == nil {
			sections = [][2]string{}
		}
		data := object{"conversationId": id, "base": base, "sections": sections}
		if tools != "" {
			data["tools"] = keeper.keep(tools)
		}
		if len(removedTools) > 0 {
			data["removedTools"] = removedTools
		}
		if w.err != nil {
			return w.err
		}
		return appendChanges(tx, projectID, append(keeper.pieces, change{"conversation.prompt_updated", data}))
	})
}
func (repo *Repository) ClearToolResults(ctx context.Context, id, turnID string, upTo float64, sawSeq int64) error {
	return repo.withTurn(ctx, id, turnID, func(tx *eventstore.Transaction, projectID string) error {
		return appendChanges(tx, projectID, []change{{"conversation.results_cleared", object{"conversationId": id, "upTo": upTo, "sawSeq": sawSeq}}})
	})
}
func (repo *Repository) CompactConversation(ctx context.Context, id, turnID string, input Compaction) error {
	return repo.withTurn(ctx, id, turnID, func(tx *eventstore.Transaction, projectID string) error {
		summary := projectsettings.Trim(input.Summary)
		if summary == "" {
			return badInput("summary")
		}
		w := &projectionWriter{ctx: ctx, tx: tx.SQL()}
		keeper := newPromptKeeper(w, projectID)
		reasoning := []object{}
		for _, block := range input.Reasoning {
			if !block.Redacted && block.Text != "" {
				reasoning = append(reasoning, object{"text": block.Text})
			}
		}
		data := object{"conversationId": id, "summary": summary, "upTo": input.UpTo, "sawSeq": input.SawSeq, "usage": input.Usage, "call": keeper.keep(input.Call), "timing": input.Timing, "reasoning": reasoning, "state": input.State}
		if input.UserTokens != 0 {
			data["userTokens"] = input.UserTokens
		}
		if w.err != nil {
			return w.err
		}
		return appendChanges(tx, projectID, append(keeper.pieces, change{"conversation.compacted", data}))
	})
}
func (repo *Repository) RecordCompactionFailure(ctx context.Context, id, turnID, detail string) error {
	return repo.withTurn(ctx, id, turnID, func(tx *eventstore.Transaction, projectID string) error {
		return appendChanges(tx, projectID, []change{{"conversation.compaction_failed", object{"conversationId": id, "detail": detail}}})
	})
}

type InstructionFile struct {
	Path        string  `json:"path"`
	Hash        string  `json:"hash"`
	Content     *string `json:"content"`
	Uncommitted *bool   `json:"uncommitted,omitempty"`
}

func (repo *Repository) RecordInstructions(ctx context.Context, id, turnID string, file InstructionFile) (recorded bool, err error) {
	err = repo.withTurn(ctx, id, turnID, func(tx *eventstore.Transaction, projectID string) error {
		rows, err := records(ctx, tx.SQL(), "SELECT instructions FROM conversations WHERE id = ?", id)
		if err != nil {
			return err
		}
		before, err := rows[0].json("instructions", nil)
		if err != nil {
			return err
		}
		if before != nil && asObject(before).str("hash") == file.Hash || before == nil && file.Content == nil {
			return nil
		}
		data := object{"conversationId": id, "path": file.Path, "hash": file.Hash, "content": file.Content, "changed": before != nil}
		if file.Uncommitted != nil {
			data["uncommitted"] = *file.Uncommitted
		}
		if err := appendChanges(tx, projectID, []change{{"conversation.instructions", data}}); err != nil {
			return err
		}
		recorded = true
		return nil
	})
	return
}
func (repo *Repository) RecordModelRetry(ctx context.Context, id, turnID string, attempt int, delayMS int64, detail string) error {
	return repo.withTurn(ctx, id, turnID, func(tx *eventstore.Transaction, projectID string) error {
		return appendChanges(tx, projectID, []change{{"conversation.model_retry", object{"conversationId": id, "attempt": attempt, "delayMs": delayMS, "detail": detail}}})
	})
}
func (repo *Repository) RecordModelFallback(ctx context.Context, id, turnID, from, to, detail string) error {
	return repo.withTurn(ctx, id, turnID, func(tx *eventstore.Transaction, projectID string) error {
		return appendChanges(tx, projectID, []change{{"conversation.model_fallback", object{"conversationId": id, "turnId": turnID, "from": from, "to": to, "detail": detail}}})
	})
}

func UnknownResult(outcome string) string {
	return fmt.Sprintf("The result of this call is unknown: the turn ended (%s) before it was recorded. It may already have taken effect; check before calling it again.", outcome)
}
func (repo *Repository) FinishTurn(ctx context.Context, id, turnID, outcome string, failure *domainerr.Error) error {
	return repo.withTurn(ctx, id, turnID, func(tx *eventstore.Transaction, projectID string) error {
		// Ending a turn only needs call identities. Do not decode every chat
		// message, attachment and historical tool output to find unanswered calls.
		calls, err := records(ctx, tx.SQL(), `WITH answered(id) AS MATERIALIZED (
 SELECT DISTINCT coalesce(json_extract(payload, '$.toolCallId'), '')
 FROM transcript WHERE conversation_id = ?1 AND kind = 'tool'
)
SELECT coalesce(json_extract(call.value, '$.id'), '') AS id,
 json_extract(call.value, '$.name') AS name
FROM transcript step, json_each(step.payload, '$.toolCalls') call
LEFT JOIN answered ON answered.id = coalesce(json_extract(call.value, '$.id'), '')
WHERE step.conversation_id = ?1 AND step.kind = 'step' AND answered.id IS NULL
ORDER BY step.id, CAST(call.key AS INTEGER)`, id)
		if err != nil {
			return err
		}
		changes := []change{}
		for _, call := range calls {
			changes = append(changes, change{"tool.finished", object{"conversationId": id, "turnId": turnID, "toolCallId": call["id"], "name": call["name"], "content": UnknownResult(outcome), "isError": true}})
		}
		changes = append(changes, change{"turn.finished", object{"conversationId": id, "turnId": turnID, "outcome": outcome, "error": failure}})
		if err := appendChanges(tx, projectID, changes); err != nil {
			return err
		}
		if outcome == "interrupted" {
			return nil
		}
		current, err := conversation(ctx, tx.SQL(), id)
		if err != nil {
			return err
		}
		if outcome == "completed" && current.PendingSeq > 0 {
			return nil
		}
		w := &projectionWriter{ctx: ctx, tx: tx.SQL()}
		abandoned := abandonedReview(w, id)
		if w.err != nil {
			return w.err
		}
		if err := appendChanges(tx, projectID, abandoned); err != nil {
			return err
		}
		if outcome == "completed" {
			rows, err := records(ctx, tx.SQL(), threadSelect+" WHERE t.id = ?", id)
			if err != nil {
				return err
			}
			if len(rows) > 0 {
				thread, err := toThread(rows[0], repo.Reader.now())
				if err != nil {
					return err
				}
				if thread.Stage == "silent" && (!rows[0].present("nudged_at") || rows[0].integer("nudged_at") < rows[0].integer("reported_at")) {
					return appendChanges(tx, projectID, []change{{"thread.nudged", object{"threadId": id}}})
				}
			}
		}
		return nil
	})
}
func (repo *Repository) RequestTurn(ctx context.Context, id string) error {
	return repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		current, err := conversation(ctx, tx.SQL(), id)
		if err != nil {
			return err
		}
		if current.Turn != nil {
			return domainerr.New("error.turnRunning", nil)
		}
		return appendChanges(tx, current.ProjectID, []change{{"turn.requested", object{"conversationId": id}}})
	})
}
func (repo *Repository) FailBeforeTurn(ctx context.Context, id string, failure *domainerr.Error) error {
	if failure == nil {
		failure = domainerr.New("error.noModel", nil)
	}
	return repo.endBeforeTurn(ctx, id, "failed", failure)
}

// CancelBeforeTurn records cancellation when a scheduled invocation was stopped
// before it could start its model turn. It must not look like missing settings.
func (repo *Repository) CancelBeforeTurn(ctx context.Context, id string, interrupted bool) error {
	if interrupted {
		return repo.endBeforeTurn(ctx, id, "interrupted", domainerr.New("error.interrupted", nil))
	}
	return repo.endBeforeTurn(ctx, id, "cancelled", nil)
}
func (repo *Repository) endBeforeTurn(ctx context.Context, id, outcome string, failure *domainerr.Error) error {
	return repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		current, err := conversation(ctx, tx.SQL(), id)
		if err != nil {
			return err
		}
		if current.Role == "thread" && current.PendingSeq == 0 && current.Turn == nil {
			return nil
		}
		turnID, err := newID("trn")
		if err != nil {
			return err
		}
		w := &projectionWriter{ctx: ctx, tx: tx.SQL()}
		changes := []change{{"turn.started", object{"conversationId": id, "turnId": turnID, "model": ""}}, {"turn.finished", object{"conversationId": id, "turnId": turnID, "outcome": outcome, "error": failure}}}
		if outcome != "interrupted" {
			changes = append(changes, abandonedReview(w, id)...)
		}
		if w.err != nil {
			return w.err
		}
		return appendChanges(tx, current.ProjectID, changes)
	})
}

func (repo *Repository) SetWait(ctx context.Context, id string, until int64, note string) (waitID string, err error) {
	err = repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		current, err := conversation(ctx, tx.SQL(), id)
		if err != nil {
			return err
		}
		waitID, err = newID("wat")
		if err != nil {
			return err
		}
		changes := []change{}
		if before := current.Wait; before != nil {
			changes = append(changes, change{"conversation.wait_ended", object{"conversationId": id, "waitId": before.ID, "how": "cancelled", "by": id, "until": before.Until, "note": before.Note}})
		}
		changes = append(changes, change{"conversation.wait_set", object{"conversationId": id, "waitId": waitID, "until": until, "note": note}})
		return appendChanges(tx, current.ProjectID, changes)
	})
	if err != nil {
		return "", err
	}
	return
}
func (repo *Repository) EndWait(ctx context.Context, id, how, by string, waitID *string) (ended bool, err error) {
	err = repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		current, err := conversation(ctx, tx.SQL(), id)
		if err != nil {
			return err
		}
		wait := current.Wait
		if wait == nil || waitID != nil && wait.ID != *waitID {
			return nil
		}
		if err := appendChanges(tx, current.ProjectID, []change{{"conversation.wait_ended", object{"conversationId": id, "waitId": wait.ID, "how": how, "by": by, "until": wait.Until, "note": wait.Note}}}); err != nil {
			return err
		}
		ended = true
		return nil
	})
	return
}
func (repo *Repository) RunRoutine(ctx context.Context, projectID, routineID string, dueAt int64) error {
	_, err := repo.mutateProject(ctx, projectID, func(current ProjectView) ([]change, error) {
		for _, routine := range current.Settings.Routines {
			if routine.ID == routineID {
				return []change{{"routine.ran", object{"routineId": routineID, "prompt": routine.Prompt, "dueAt": dueAt}}}, nil
			}
		}
		return nil, domainerr.NotFound(routineID)
	})
	return err
}

func (reader Reader) ListWaits(ctx context.Context) ([]object, error) {
	rows, err := records(ctx, reader.DB, "SELECT id, project_id, wait_id, wait_until FROM conversations WHERE wait_id IS NOT NULL ORDER BY wait_until")
	if err != nil {
		return nil, err
	}
	result := []object{}
	for _, row := range rows {
		result = append(result, object{"conversationId": row["id"], "projectId": row["project_id"], "waitId": row["wait_id"], "until": row["wait_until"]})
	}
	return result, nil
}
func (reader Reader) ListAsides(ctx context.Context) ([]object, error) {
	rows, err := records(ctx, reader.DB, "SELECT id, aside_until FROM threads WHERE aside_until IS NOT NULL AND aside_due = 0 ORDER BY aside_until")
	if err != nil {
		return nil, err
	}
	result := []object{}
	for _, row := range rows {
		result = append(result, object{"threadId": row["id"], "until": row["aside_until"]})
	}
	return result, nil
}
func (reader Reader) PendingIsTriage(ctx context.Context, id string) (bool, error) {
	rows, err := records(ctx, reader.DB, `SELECT t.kind, t.payload FROM conversations c JOIN transcript t ON t.conversation_id = c.id AND t.seq >= c.pending_since WHERE c.id = ? AND c.pending_seq > 0 AND c.role = 'coordinator'`, id)
	if err != nil {
		return false, err
	}
	triage := false
	for _, row := range rows {
		payload, err := row.json("payload", nil)
		if err != nil {
			return false, err
		}
		note := asObject(payload)
		if row.text("kind") == "note" && note.str("type") == "user-in-thread" {
			triage = true
		} else if row.text("kind") != "note" || note.str("type") != "report" || note.str("report") != "progress" {
			return false, nil
		}
	}
	return triage, nil
}
func (reader Reader) PendingIsUserMessage(ctx context.Context, id string) (bool, error) {
	rows, err := records(ctx, reader.DB, `SELECT m.author FROM conversations c JOIN transcript t ON t.conversation_id = c.id AND t.seq = c.pending_seq JOIN messages m ON m.id = t.ref WHERE c.id = ? AND t.kind = 'message'`, id)
	return len(rows) > 0 && rows[0].text("author") == "user", err
}

func (reader Reader) LastHandledSeq(ctx context.Context, id string) (int64, error) {
	var seq int64
	err := reader.DB.QueryRowContext(ctx, `SELECT coalesce(max(CAST(json_extract(payload, '$.sawSeq') AS INTEGER)), 0)
		FROM transcript WHERE conversation_id = ? AND kind = 'step'`, id).Scan(&seq)
	return seq, err
}
