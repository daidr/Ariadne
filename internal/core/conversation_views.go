package core

import (
	"context"
	"database/sql"
	"slices"

	"github.com/daidr/Ariadne/internal/domainerr"
)

const maxSafeInteger int64 = 9007199254740991
const messageSelect = `SELECT m.*, a.id AS ask_id, a.block AS ask_block, a.answer AS ask_answer,
 a.withdrawn AS ask_withdrawn FROM messages m LEFT JOIN attentions a ON a.message_id = m.id`

type TurnView struct {
	ID        string `json:"id"`
	StartedAt int64  `json:"startedAt"`
	Model     any    `json:"model"`
	Activity  any    `json:"activity"`
	Fallback  bool   `json:"fallback"`
}
type WaitView struct {
	ID    string `json:"id"`
	Until int64  `json:"until"`
	Note  string `json:"note"`
}
type ConversationView struct {
	ID            string         `json:"id"`
	ProjectID     string         `json:"projectId"`
	Role          string         `json:"role"`
	PendingSeq    int64          `json:"pendingSeq"`
	Turn          *TurnView      `json:"turn"`
	LastOutcome   any            `json:"lastOutcome"`
	LastError     any            `json:"lastError"`
	Usage         map[string]any `json:"usage"`
	ContextTokens int64          `json:"contextTokens"`
	Compactions   int64          `json:"compactions"`
	Wait          *WaitView      `json:"wait"`
}

func toConversation(row record) (ConversationView, error) {
	lastError, err := row.json("last_error", nil)
	if err != nil {
		return ConversationView{}, err
	}
	view := ConversationView{ID: row.text("id"), ProjectID: row.text("project_id"), Role: row.text("role"), PendingSeq: row.integer("pending_seq"), LastOutcome: row["last_outcome"], LastError: lastError, Usage: usage(row), ContextTokens: row.integer("context_tokens"), Compactions: row.integer("compactions")}
	if row.present("turn_id") {
		view.Turn = &TurnView{ID: row.text("turn_id"), StartedAt: row.integer("turn_started_at"), Model: row["model"], Activity: row["activity"], Fallback: row.flag("fallback")}
	}
	if row.present("wait_id") {
		view.Wait = &WaitView{ID: row.text("wait_id"), Until: row.integer("wait_until"), Note: row.text("wait_note")}
	}
	return view, nil
}
func conversation(ctx context.Context, db queryer, id string) (ConversationView, error) {
	rows, err := records(ctx, db, "SELECT * FROM conversations WHERE id = ?", id)
	if err != nil {
		return ConversationView{}, err
	}
	if len(rows) == 0 {
		return ConversationView{}, domainerr.NotFound(id)
	}
	return toConversation(rows[0])
}
func (reader Reader) GetConversation(ctx context.Context, id string) (ConversationView, error) {
	return conversation(ctx, reader.DB, id)
}
func (reader Reader) listConversations(ctx context.Context, query string) ([]ConversationView, error) {
	rows, err := records(ctx, reader.DB, query)
	if err != nil {
		return nil, err
	}
	result := []ConversationView{}
	for _, row := range rows {
		view, err := toConversation(row)
		if err != nil {
			return nil, err
		}
		result = append(result, view)
	}
	return result, nil
}
func (reader Reader) ListPendingConversations(ctx context.Context) ([]ConversationView, error) {
	return reader.listConversations(ctx, `SELECT * FROM conversations WHERE pending_seq > 0 AND turn_id IS NULL AND id NOT IN (SELECT id FROM threads WHERE stopped_reason IS NOT NULL) ORDER BY pending_since`)
}
func (reader Reader) ListRunningConversations(ctx context.Context) ([]ConversationView, error) {
	return reader.listConversations(ctx, "SELECT * FROM conversations WHERE turn_id IS NOT NULL")
}

type MessageView struct {
	ID              string `json:"id"`
	ConversationID  string `json:"conversationId"`
	Author          string `json:"author"`
	Kind            string `json:"kind"`
	Content         string `json:"content"`
	Blocks          any    `json:"blocks"`
	Ask             any    `json:"ask"`
	Attachments     any    `json:"attachments"`
	Mentions        any    `json:"mentions"`
	Comments        any    `json:"comments"`
	Cites           any    `json:"cites"`
	RelayedTo       any    `json:"relayedTo"`
	ReplyTo         any    `json:"replyTo"`
	Reactions       any    `json:"reactions"`
	CreatedAt       int64  `json:"createdAt"`
	EditedAt        any    `json:"editedAt"`
	EditReason      any    `json:"editReason"`
	OriginalContent any    `json:"originalContent"`
	EditedSinceRead bool   `json:"editedSinceRead"`
}

func (message MessageView) AttentionID() string {
	return asObject(message.Ask).str("attentionId")
}

func toMessage(row record) (MessageView, error) {
	view := MessageView{ID: row.text("id"), ConversationID: row.text("conversation_id"), Author: row.text("author"), Kind: row.text("kind"), Content: row.text("content"), CreatedAt: row.integer("created_at"), EditedAt: row["edited_at"], EditReason: row["edit_reason"], OriginalContent: row["original_content"], EditedSinceRead: row.flag("edited_since_read")}
	for _, field := range []struct {
		key      string
		target   *any
		fallback any
	}{{"blocks", &view.Blocks, []any{}}, {"attachments", &view.Attachments, []any{}}, {"mentions", &view.Mentions, []any{}}, {"comments", &view.Comments, []any{}}, {"cites", &view.Cites, []any{}}, {"relayed_to", &view.RelayedTo, []any{}}, {"reply_to", &view.ReplyTo, nil}, {"reactions", &view.Reactions, []any{}}} {
		value, err := row.json(field.key, field.fallback)
		if err != nil {
			return MessageView{}, err
		}
		*field.target = value
	}
	if row.text("ask_id") != "" && row.text("ask_block") != "" {
		block, err := row.json("ask_block", nil)
		if err != nil {
			return MessageView{}, err
		}
		answer, err := row.json("ask_answer", nil)
		if err != nil {
			return MessageView{}, err
		}
		view.Ask = object{"attentionId": row["ask_id"], "block": block, "answer": answer, "withdrawn": row["ask_withdrawn"]}
	}
	return view, nil
}
func readMessages(ctx context.Context, db queryer, query string, args ...any) ([]MessageView, error) {
	rows, err := records(ctx, db, query, args...)
	if err != nil {
		return nil, err
	}
	messages := []MessageView{}
	for _, row := range rows {
		view, err := toMessage(row)
		if err != nil {
			return nil, err
		}
		messages = append(messages, view)
	}
	return messages, nil
}
func findMessage(ctx context.Context, db queryer, id string) (*MessageView, error) {
	rows, err := readMessages(ctx, db, messageSelect+" WHERE m.id = ?", id)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}
func (reader Reader) GetMessage(ctx context.Context, id string) (MessageView, error) {
	message, err := findMessage(ctx, reader.DB, id)
	if err != nil {
		return MessageView{}, err
	}
	if message == nil {
		return MessageView{}, domainerr.NotFound(id)
	}
	return *message, nil
}
func (reader Reader) ListMessages(ctx context.Context, id string) ([]MessageView, error) {
	return readMessages(ctx, reader.DB, messageSelect+" WHERE m.conversation_id = ? ORDER BY m.id", id)
}
func toNotice(row record) (object, error) {
	payload, err := row.json("payload", nil)
	if err != nil {
		return nil, err
	}
	notice := object{"id": row["id"], "conversationId": row["conversation_id"], "createdAt": row["created_at"], "kind": row["kind"]}
	for key, value := range asObject(payload) {
		notice[key] = value
	}
	if row.text("kind") == "memory" || row.text("kind") == "memory-everywhere" {
		notice["undone"] = row.flag("undone")
	} else if row.text("kind") == "merged" && row.flag("undone") {
		notice["undone"] = true
	}
	return notice, nil
}
func readNotices(ctx context.Context, db queryer, query string, args ...any) ([]object, error) {
	rows, err := records(ctx, db, query, args...)
	if err != nil {
		return nil, err
	}
	notices := []object{}
	for _, row := range rows {
		notice, err := toNotice(row)
		if err != nil {
			return nil, err
		}
		notices = append(notices, notice)
	}
	return notices, nil
}

type NoticeRange struct {
	From *int64
	To   *int64
}

func listNotices(ctx context.Context, db queryer, id string, bounds NoticeRange) ([]object, error) {
	from, to := int64(-1), maxSafeInteger
	if bounds.From != nil {
		from = *bounds.From
	}
	if bounds.To != nil {
		to = *bounds.To
	}
	return readNotices(ctx, db, "SELECT * FROM notices WHERE conversation_id = ? AND created_at >= ? AND created_at < ? ORDER BY created_at, rowid", id, from, to)
}
func (reader Reader) ListNotices(ctx context.Context, id string, bounds NoticeRange) ([]object, error) {
	return listNotices(ctx, reader.DB, id, bounds)
}

type MessageCursor struct {
	Before *string
	From   *string
	Limit  *int
	Since  *int64
}
type MessagePage struct {
	Messages []MessageView `json:"messages"`
	Notices  []object      `json:"notices"`
	More     bool          `json:"more"`
}

func (reader Reader) PageMessages(ctx context.Context, id string, cursor MessageCursor) (result MessagePage, err error) {
	err = reader.snapshot(ctx, func(tx *sql.Tx) error {
		if cursor.Since != nil {
			from := ""
			if cursor.From != nil {
				from = *cursor.From
			}
			result.Messages, err = readMessages(ctx, tx, messageSelect+" WHERE m.conversation_id = ? AND m.id >= ? AND (m.changed_seq > ? OR coalesce(a.changed_seq, 0) > ?) ORDER BY m.id", id, from, *cursor.Since, *cursor.Since)
			if err != nil {
				return err
			}
			result.Notices, err = readNotices(ctx, tx, "SELECT * FROM notices WHERE conversation_id = ? AND changed_seq > ? ORDER BY created_at, id", id, *cursor.Since)
			return err
		}
		limit := 40
		if cursor.Limit != nil {
			limit = max(1, min(*cursor.Limit, 500))
		}
		if cursor.From != nil {
			result.Messages, err = readMessages(ctx, tx, messageSelect+" WHERE m.conversation_id = ? AND m.id >= ? ORDER BY m.id", id, *cursor.From)
		} else {
			before := "\uffff"
			if cursor.Before != nil {
				before = *cursor.Before
			}
			result.Messages, err = readMessages(ctx, tx, messageSelect+" WHERE m.conversation_id = ? AND m.id < ? ORDER BY m.id DESC LIMIT ?", id, before, limit)
			slices.Reverse(result.Messages)
		}
		if err != nil {
			return err
		}
		first := ""
		if len(result.Messages) > 0 {
			first = result.Messages[0].ID
		} else if cursor.Before != nil {
			first = *cursor.Before
		} else if cursor.From != nil {
			first = *cursor.From
		}
		previous, err := records(ctx, tx, "SELECT 1 AS one FROM messages WHERE conversation_id = ? AND id < ? LIMIT 1", id, first)
		if err != nil {
			return err
		}
		result.More = len(previous) > 0
		bounds := NoticeRange{}
		if result.More && len(result.Messages) > 0 {
			bounds.From = &result.Messages[0].CreatedAt
		}
		if cursor.Before != nil {
			end, err := records(ctx, tx, "SELECT created_at FROM messages WHERE id = ?", *cursor.Before)
			if err != nil {
				return err
			}
			if len(end) > 0 {
				at := end[0].integer("created_at")
				bounds.To = &at
			}
		}
		result.Notices, err = listNotices(ctx, tx, id, bounds)
		return err
	})
	return
}

type TranscriptOptions struct {
	UpTo  *int64
	After *int64
	// ModelContext omits superseded summaries and already summarised steps,
	// results and notes in SQL. User messages and prompt changes remain available
	// for verbatim folding and rebuilding instructions. This is a projection for
	// the model, not a replacement for the full audit transcript.
	ModelContext bool
}

// A coordinator's user-in-thread note points to the original message. Resolve
// attachment metadata here so existing transcripts work without rebuilding them.
const sourceAttachments = `CASE WHEN t.kind = 'note' AND json_extract(t.payload, '$.type') = 'user-in-thread'
 THEN (SELECT m.attachments FROM messages m
  WHERE m.id = json_extract(t.payload, '$.messageId') AND m.project_id = t.project_id
   AND m.conversation_id = json_extract(t.payload, '$.threadId') AND m.author = 'user'
   AND m.attachments != '[]') END AS source_attachments`

func listTranscript(ctx context.Context, db queryer, id string, options TranscriptOptions) ([]object, error) {
	after, upTo := int64(-1), maxSafeInteger
	if options.After != nil {
		after = *options.After
	}
	if options.UpTo != nil {
		upTo = *options.UpTo
	}
	query := "SELECT t.seq, t.kind, t.ref, t.payload, " + sourceAttachments + " FROM transcript t WHERE t.conversation_id = ? AND t.seq > ? AND t.seq <= ? ORDER BY t.id"
	if options.ModelContext {
		query = `WITH scope AS MATERIALIZED (
 SELECT id, seq, kind FROM transcript WHERE conversation_id = ? AND seq > ? AND seq <= ?
), latest_summary AS MATERIALIZED (
 SELECT id FROM scope WHERE kind = 'summary' ORDER BY id DESC LIMIT 1
), boundary AS MATERIALIZED (
 SELECT t.id, json_extract(t.payload, '$.upTo') AS up_to FROM latest_summary s
 JOIN transcript t ON t.id = s.id
)
SELECT t.seq, t.kind, t.ref, t.payload, ` + sourceAttachments + ` FROM scope s JOIN transcript t ON t.id = s.id
WHERE s.kind IN ('message', 'prompt', 'cleared')
 OR s.id = (SELECT id FROM boundary)
 OR (s.kind != 'summary' AND
 CASE WHEN s.kind = 'step' THEN COALESCE(json_extract(t.payload, '$.sawSeq'), 0) + 0.5 ELSE s.seq END
 > COALESCE((SELECT up_to FROM boundary), -1))
ORDER BY s.id`
	}
	rows, err := records(ctx, db, query, id, after, upTo)
	if err != nil {
		return nil, err
	}
	entries := []object{}
	for _, row := range rows {
		payload, err := row.json("payload", nil)
		if err != nil {
			return nil, err
		}
		kind := row.text("kind")
		entry := object{"seq": row["seq"], "kind": kind}
		switch kind {
		case "message":
			message, err := findMessage(ctx, db, row.text("ref"))
			if err != nil {
				return nil, err
			}
			if message == nil {
				continue
			}
			if options.UpTo != nil && message.EditedAt != nil {
				edits, err := records(ctx, db, `SELECT json_extract(data, '$.content') AS content FROM events WHERE project_id = (SELECT project_id FROM messages WHERE id = ?1) AND type = 'message.edited' AND json_extract(data, '$.messageId') = ?2 AND seq <= ?3 ORDER BY seq DESC LIMIT 1`, message.ID, message.ID, upTo)
				if err != nil {
					return nil, err
				}
				if len(edits) > 0 && edits[0].present("content") {
					message.Content = edits[0].text("content")
				} else if original, ok := message.OriginalContent.(string); ok {
					message.Content = original
				}
			}
			entry["message"] = message
			if asks, has := asObject(payload)["openAsks"]; has && truthy(asks) {
				entry["openAsks"] = asks
			}
		case "note":
			if row.present("source_attachments") {
				files, err := row.json("source_attachments", nil)
				if err != nil {
					return nil, err
				}
				asObject(payload)["attachments"] = files
			}
			entry["note"] = payload
		case "step", "tool", "summary", "cleared", "prompt":
			for key, value := range asObject(payload) {
				entry[key] = value
			}
		default:
			continue
		}
		entries = append(entries, entry)
	}
	return entries, nil
}
func (reader Reader) ListTranscript(ctx context.Context, id string, options TranscriptOptions) (result []object, err error) {
	err = reader.snapshot(ctx, func(tx *sql.Tx) error { result, err = listTranscript(ctx, tx, id, options); return err })
	return
}
func (reader Reader) MessagesChangedSince(ctx context.Context, id string, seq int64) (bool, error) {
	rows, err := records(ctx, reader.DB, "SELECT 1 AS one FROM messages WHERE conversation_id = ? AND changed_seq > ? AND edited_at IS NOT NULL LIMIT 1", id, seq)
	return len(rows) > 0, err
}
func (reader Reader) GetTodo(ctx context.Context, id string) (object, error) {
	rows, err := records(ctx, reader.DB, "SELECT items, updated_at FROM todos WHERE conversation_id = ?", id)
	if err != nil {
		return nil, err
	}
	row := record{}
	if len(rows) > 0 {
		row = rows[0]
	}
	items, err := row.json("items", []any{})
	if err != nil {
		return nil, err
	}
	return object{"conversationId": id, "items": items, "updatedAt": row.integer("updated_at")}, nil
}
func (reader Reader) GetInstructions(ctx context.Context, id string) (any, error) {
	rows, err := records(ctx, reader.DB, "SELECT instructions FROM conversations WHERE id = ?", id)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rows[0].json("instructions", nil)
}
func (reader Reader) GetPrompt(ctx context.Context, projectID, hash string) (*string, error) {
	rows, err := records(ctx, reader.DB, "SELECT content FROM prompts WHERE project_id = ? AND hash = ?", projectID, hash)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	content := rows[0].text("content")
	return &content, nil
}
