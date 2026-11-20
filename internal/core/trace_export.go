package core

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/daidr/Ariadne/internal/eventstore"
)

// TraceSnapshot scopes every entry of a trace export to the same read transaction.
type TraceSnapshot struct {
	Conversation ConversationView
	Title        string
	Project      string
	ctx          context.Context
	tx           *sql.Tx
}

func (reader Reader) ReadTraceSnapshot(ctx context.Context, id string, read func(TraceSnapshot) error) error {
	return reader.snapshot(ctx, func(tx *sql.Tx) error {
		current, err := conversation(ctx, tx, id)
		if err != nil {
			return err
		}
		snapshot := TraceSnapshot{Conversation: current, ctx: ctx, tx: tx}
		err = tx.QueryRowContext(ctx, `SELECT p.name, coalesce(t.title, '') FROM projects p
 LEFT JOIN threads t ON t.id = ? AND t.project_id = p.id WHERE p.id = ?`, id, current.ProjectID).Scan(&snapshot.Project, &snapshot.Title)
		if err != nil {
			return err
		}
		return read(snapshot)
	})
}

func (s TraceSnapshot) Events(visit func(eventstore.Event) error) error {
	rows, err := s.tx.QueryContext(s.ctx, `SELECT e.seq, e.id, e.type, e.project_id, e.data, e.at FROM events e
 WHERE e.project_id = ?1 AND (
  e.type IN (`+traceEventTypes+`, 'conversation.prompt_updated', 'message.posted') AND json_extract(e.data, '$.conversationId') = ?2
  OR e.type = 'message.edited' AND EXISTS (SELECT 1 FROM messages m
   WHERE m.id = json_extract(e.data, '$.messageId') AND m.project_id = ?1 AND m.conversation_id = ?2)
 ) ORDER BY e.seq`, s.Conversation.ProjectID, s.Conversation.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var event eventstore.Event
		var data string
		if err := rows.Scan(&event.Seq, &event.ID, &event.Type, &event.ProjectID, &data, &event.At); err != nil {
			return err
		}
		event.Data = json.RawMessage(data)
		if err := visit(event); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (s TraceSnapshot) Transcript(visit func(map[string]any) error) error {
	const pageSize = 128
	var after int64
	for {
		rows, err := records(s.ctx, s.tx, "SELECT t.id, "+transcriptFields+` FROM transcript t
 WHERE t.conversation_id = ? AND t.id > ? ORDER BY t.id LIMIT ?`, s.Conversation.ID, after, pageSize)
		if err != nil {
			return err
		}
		entries, err := decodeTranscript(s.ctx, s.tx, rows, nil)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := visit(entry); err != nil {
				return err
			}
		}
		if len(rows) < pageSize {
			return nil
		}
		after = rows[len(rows)-1].integer("id")
	}
}

// Prompts emits referenced input records once, including the inputs of failed requests.
func (s TraceSnapshot) Prompts(visit func(hash, content string) error) error {
	rows, err := s.tx.QueryContext(s.ctx, `WITH referenced AS (
 SELECT DISTINCT hashes.value AS hash FROM events e, json_each(json_array(
  json_extract(e.data, '$.input.system'), json_extract(e.data, '$.input.tools'),
  json_extract(e.data, '$.input.call'), json_extract(e.data, '$.call'), json_extract(e.data, '$.tools')
 )) hashes WHERE e.project_id = ?1 AND json_extract(e.data, '$.conversationId') = ?2
  AND e.type IN ('turn.step', 'research.step', 'conversation.model_failed', 'conversation.compacted', 'conversation.prompt_updated')
  AND hashes.type = 'text'
 ) SELECT p.hash, p.content FROM referenced r JOIN prompts p ON p.project_id = ?1 AND p.hash = r.hash ORDER BY p.hash`, s.Conversation.ProjectID, s.Conversation.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var hash, content string
		if err := rows.Scan(&hash, &content); err != nil {
			return err
		}
		if err := visit(hash, content); err != nil {
			return err
		}
	}
	return rows.Err()
}
