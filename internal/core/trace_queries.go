package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/eventstore"
)

func eventRows(ctx context.Context, db queryer, query string, args ...any) ([]eventstore.Event, error) {
	rows, err := records(ctx, db, query, args...)
	if err != nil {
		return nil, err
	}
	result := []eventstore.Event{}
	for _, row := range rows {
		data := json.RawMessage(row.text("data"))
		if !json.Valid(data) {
			return nil, fmt.Errorf("invalid JSON in event %d", row.integer("seq"))
		}
		result = append(result, eventstore.Event{Seq: row.integer("seq"), ID: row.text("id"), Type: row.text("type"), ProjectID: row.text("project_id"), Data: data, At: row.integer("at")})
	}
	return result, nil
}
func (reader Reader) ResearchSteps(ctx context.Context, id, toolCallID string, upTo int64) (result []eventstore.Event, err error) {
	err = reader.snapshot(ctx, func(tx *sql.Tx) error {
		current, err := conversation(ctx, tx, id)
		if err != nil {
			return err
		}
		result, err = eventRows(ctx, tx, `SELECT * FROM events WHERE project_id = ? AND type = 'research.step' AND json_extract(data, '$.conversationId') = ? AND json_extract(data, '$.toolCallId') = ? AND seq <= ? ORDER BY seq`, current.ProjectID, id, toolCallID, upTo)
		return err
	})
	return
}
func (reader Reader) CountRequests(ctx context.Context, id string, before int64) (count int64, err error) {
	err = reader.snapshot(ctx, func(tx *sql.Tx) error {
		current, err := conversation(ctx, tx, id)
		if err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, `SELECT count(*) FROM events WHERE project_id = ? AND type IN ('turn.step', 'conversation.compacted') AND json_extract(data, '$.conversationId') = ? AND seq < ?`, current.ProjectID, id, before).Scan(&count)
	})
	return
}
func (reader Reader) LastStepBefore(ctx context.Context, id string, before int64) (result *eventstore.Event, err error) {
	err = reader.snapshot(ctx, func(tx *sql.Tx) error {
		current, err := conversation(ctx, tx, id)
		if err != nil {
			return err
		}
		rows, err := eventRows(ctx, tx, `SELECT * FROM events WHERE project_id = ? AND type = 'turn.step' AND json_extract(data, '$.conversationId') = ? AND seq < ? ORDER BY seq DESC LIMIT 1`, current.ProjectID, id, before)
		if err != nil {
			return err
		}
		if len(rows) > 0 {
			result = &rows[0]
		}
		return nil
	})
	return
}

type TurnEventOptions struct {
	Before              *int64
	Turns               *int
	MaxBytes, MaxEvents int64
}
type TurnEventPage struct {
	Events []eventstore.Event `json:"events"`
	More   bool               `json:"more"`
}

const traceEventTypes = `'turn.started', 'turn.step', 'research.step', 'tool.finished', 'turn.finished', 'conversation.model_retry', 'conversation.model_fallback', 'conversation.compacted', 'conversation.results_cleared', 'conversation.compaction_failed', 'conversation.instructions'`
const TraceLimitReason = "trace is too large"

func (reader Reader) ListTurnEvents(ctx context.Context, id string, options TurnEventOptions) (result TurnEventPage, err error) {
	result.Events = []eventstore.Event{}
	err = reader.snapshot(ctx, func(tx *sql.Tx) error {
		current, err := conversation(ctx, tx, id)
		if err != nil {
			return err
		}
		count := 10
		if options.Turns != nil {
			count = *options.Turns
		}
		if count <= 0 {
			return nil
		}
		before := maxSafeInteger
		if options.Before != nil {
			before = *options.Before
		}
		starts, err := records(ctx, tx, `SELECT seq FROM events WHERE project_id = ? AND type = 'turn.started' AND json_extract(data, '$.conversationId') = ? AND seq < ? ORDER BY seq DESC LIMIT ?`, current.ProjectID, id, before, count+1)
		if err != nil {
			return err
		}
		if len(starts) == 0 {
			return nil
		}
		result.More = len(starts) > count
		first := starts[min(len(starts), count)-1].integer("seq")
		if options.MaxBytes > 0 || options.MaxEvents > 0 {
			var bytes, count int64
			query := "SELECT coalesce(sum(length(CAST(data AS BLOB))),0),count(*) FROM events WHERE project_id = ? AND seq >= ? AND seq < ? AND type IN (" + traceEventTypes + ") AND json_extract(data, '$.conversationId') = ?"
			if err := tx.QueryRowContext(ctx, query, current.ProjectID, first, before, id).Scan(&bytes, &count); err != nil {
				return err
			}
			if options.MaxBytes > 0 && bytes > options.MaxBytes || options.MaxEvents > 0 && count > options.MaxEvents {
				return domainerr.New("error.invalidBody", map[string]any{"reason": TraceLimitReason})
			}
		}
		result.Events, err = eventRows(ctx, tx, "SELECT * FROM events WHERE project_id = ? AND seq >= ? AND seq < ? AND type IN ("+traceEventTypes+") AND json_extract(data, '$.conversationId') = ? ORDER BY seq", current.ProjectID, first, before, id)
		return err
	})
	return
}
