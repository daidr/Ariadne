package core

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"slices"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/textutil"
)

const maxSafeInteger int64 = 9007199254740991
const messageSelect = `SELECT m.*, a.id AS ask_id, a.block AS ask_block, a.answer AS ask_answer,
 a.withdrawn AS ask_withdrawn FROM messages m LEFT JOIN attentions a ON a.message_id = m.id`

type TurnView struct {
	ID        string `json:"id"`
	StartedAt int64  `json:"startedAt"`
	Model     any    `json:"model"`
	Activity  any    `json:"activity"`
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
		view.Turn = &TurnView{ID: row.text("turn_id"), StartedAt: row.integer("turn_started_at"), Model: row["model"], Activity: row["activity"]}
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

type ConversationActivity struct {
	Status   string    `json:"status"`
	Progress *Progress `json:"progress,omitempty"`
}

type ThreadActivity struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	ConversationActivity
}

type ProjectActivity struct {
	Coordinator ConversationActivity `json:"coordinator"`
	Threads     []ThreadActivity     `json:"threads"`
}

// ProjectActivity serves the sidebar on demand without loading the project transcript or thread views.
func (reader Reader) ProjectActivity(ctx context.Context, projectID string) (ProjectActivity, error) {
	rows, err := records(ctx, reader.DB, `SELECT c.id, c.role, t.title, td.items AS todo_items,
	 CASE WHEN c.turn_id IS NOT NULL THEN 'running'
	      WHEN c.pending_seq > 0 THEN 'queued'
	      WHEN c.wait_id IS NOT NULL THEN 'waiting'
	      ELSE 'idle' END AS status
	 FROM conversations c LEFT JOIN threads t ON t.id = c.id
	 LEFT JOIN todos td ON td.conversation_id = c.id AND c.turn_id IS NOT NULL
	 WHERE c.project_id = ? AND (c.role = 'coordinator' OR
	   (t.id IS NOT NULL AND t.stopped_reason IS NULL AND (c.turn_id IS NOT NULL OR c.pending_seq > 0 OR c.wait_id IS NOT NULL)))
	 ORDER BY c.turn_id IS NULL, c.pending_seq = 0, c.pending_since, c.id`, projectID)
	if err != nil {
		return ProjectActivity{}, err
	}
	view := ProjectActivity{Threads: []ThreadActivity{}}
	for _, row := range rows {
		progress, _, err := readTodoProgress(row)
		if err != nil {
			return ProjectActivity{}, err
		}
		activity := ConversationActivity{Status: row.text("status"), Progress: progress}
		if row.text("role") == "coordinator" {
			view.Coordinator = activity
		} else {
			view.Threads = append(view.Threads, ThreadActivity{ID: row.text("id"), Title: row.text("title"), ConversationActivity: activity})
		}
	}
	if view.Coordinator.Status == "" {
		return ProjectActivity{}, domainerr.NotFound(projectID)
	}
	return view, nil
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
	if view.Author != "user" && view.Kind == "chat" {
		view.Content = textutil.ChatMarkdown(view.Content)
		if original, ok := view.OriginalContent.(string); ok {
			view.OriginalContent = textutil.ChatMarkdown(original)
		}
	}
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

func lastReadMessage(ctx context.Context, db queryer, id string) (string, error) {
	rows, err := records(ctx, db, "SELECT id FROM messages WHERE conversation_id = ? AND read_at IS NOT NULL ORDER BY id DESC LIMIT 1", id)
	if err != nil || len(rows) == 0 {
		return "", err
	}
	return rows[0].text("id"), nil
}

func (reader Reader) LastReadMessage(ctx context.Context, id string) (string, error) {
	return lastReadMessage(ctx, reader.DB, id)
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

func pageNotices(ctx context.Context, db queryer, id string, bounds NoticeRange, since *int64) ([]object, error) {
	var notices []object
	var err error
	// Tool approvals already have a durable card and answer. Surface unlinked cards
	// in the conversation that asked or joined without creating a second approval.
	approvalsQuery := `WITH selected AS (
	 SELECT id FROM attentions WHERE conversation_id = ?1 AND message_id IS NULL AND purpose IS NOT NULL
	 UNION SELECT attention_id FROM attention_waiters WHERE thread_id = ?1
	)
	 SELECT a.id, ?1 AS conversation_id, a.created_at, 'approval' AS kind,
	 json_object('block', json(a.block), 'answer', json(a.answer), 'withdrawn', a.withdrawn) AS payload
	 FROM selected CROSS JOIN attentions a ON a.id = selected.id
	 WHERE a.message_id IS NULL AND a.purpose NOT LIKE ?2`
	args := []any{id, milestoneGate + "%"}
	if since != nil {
		notices, err = readNotices(ctx, db, "SELECT * FROM notices WHERE conversation_id = ? AND changed_seq > ? ORDER BY created_at, id", id, *since)
		approvalsQuery += " AND a.changed_seq > ?"
		args = append(args, *since)
	} else {
		notices, err = listNotices(ctx, db, id, bounds)
		if bounds.From != nil {
			approvalsQuery += " AND a.created_at >= ?"
			args = append(args, *bounds.From)
		}
		if bounds.To != nil {
			approvalsQuery += " AND a.created_at < ?"
			args = append(args, *bounds.To)
		}
	}
	if err != nil {
		return nil, err
	}
	approvals, err := readNotices(ctx, db, approvalsQuery+" ORDER BY a.created_at, a.id", args...)
	if err != nil || len(approvals) == 0 {
		return notices, err
	}
	notices = append(notices, approvals...)
	slices.SortStableFunc(notices, func(a, b object) int {
		return cmp.Compare(record(a).integer("createdAt"), record(b).integer("createdAt"))
	})
	return notices, nil
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

func pageMessages(ctx context.Context, tx *sql.Tx, id string, cursor MessageCursor) (result MessagePage, err error) {
	if cursor.Since != nil {
		from := ""
		if cursor.From != nil {
			from = *cursor.From
		}
		result.Messages, err = readMessages(ctx, tx, messageSelect+" WHERE m.conversation_id = ? AND m.id >= ? AND (m.changed_seq > ? OR coalesce(a.changed_seq, 0) > ?) ORDER BY m.id", id, from, *cursor.Since, *cursor.Since)
		if err != nil {
			return result, err
		}
		result.Notices, err = pageNotices(ctx, tx, id, NoticeRange{}, cursor.Since)
		return result, err
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
		return result, err
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
		return result, err
	}
	result.More = len(previous) > 0
	bounds := NoticeRange{}
	if result.More && len(result.Messages) > 0 {
		bounds.From = &result.Messages[0].CreatedAt
	}
	if cursor.Before != nil {
		end, err := records(ctx, tx, "SELECT created_at FROM messages WHERE id = ?", *cursor.Before)
		if err != nil {
			return result, err
		}
		if len(end) > 0 {
			at := end[0].integer("created_at")
			bounds.To = &at
		}
	}
	result.Notices, err = pageNotices(ctx, tx, id, bounds, nil)
	return result, err
}

func (reader Reader) PageMessages(ctx context.Context, id string, cursor MessageCursor) (result MessagePage, err error) {
	err = reader.snapshot(ctx, func(tx *sql.Tx) error {
		result, err = pageMessages(ctx, tx, id, cursor)
		return err
	})
	return
}

// ConversationSnapshot bundles all SQLite reads for GetConversation into one
// consistent read-only snapshot transaction.
type ConversationSnapshot struct {
	Conversation ConversationView
	Messages     []MessageView
	Notices      []object
	More         bool
	LastSeq      int64
	Todo         object
	ReadThrough  string
	ThreadModel  string
	ThreadEffort string
}

func (reader Reader) GetConversationSnapshot(ctx context.Context, id string, cursor MessageCursor) (result ConversationSnapshot, err error) {
	err = reader.snapshot(ctx, func(tx *sql.Tx) error {
		conv, err := conversation(ctx, tx, id)
		if err != nil {
			return err
		}
		page, err := pageMessages(ctx, tx, id, cursor)
		if err != nil {
			return err
		}
		var seq int64
		if err := tx.QueryRowContext(ctx, "SELECT coalesce(max(seq), 0) FROM events").Scan(&seq); err != nil {
			return err
		}
		todo, err := readTodo(ctx, tx, id)
		if err != nil {
			return err
		}
		readThrough := ""
		if conv.Role == "coordinator" && cursor.Since == nil && cursor.Before == nil {
			readThrough, err = lastReadMessage(ctx, tx, id)
			if err != nil {
				return err
			}
		}
		threadModel := ""
		threadEffort := ""
		if conv.Role == "thread" {
			rows, err := records(ctx, tx, "SELECT model, effort FROM threads WHERE id = ?", id)
			if err != nil {
				return err
			}
			if len(rows) > 0 {
				threadModel = rows[0].text("model")
				threadEffort = rows[0].text("effort")
			}
		}
		result = ConversationSnapshot{
			Conversation: conv,
			Messages:     page.Messages,
			Notices:      page.Notices,
			More:         page.More,
			LastSeq:      seq,
			Todo:         todo,
			ReadThrough:  readThrough,
			ThreadModel:  threadModel,
			ThreadEffort: threadEffort,
		}
		return nil
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

const inlineToolOutputLimit = 8 << 10

const sourceToolOutput = `CASE WHEN t.kind = 'tool' AND json_type(t.payload, '$.content') IS NULL
 THEN (SELECT json_extract(e.data, '$.content') FROM events e
  WHERE e.seq = t.seq AND e.type = 'tool.finished' AND e.project_id = t.project_id
   AND json_extract(e.data, '$.conversationId') = t.conversation_id) END AS source_tool_output`

const transcriptFields = "t.seq, t.kind, t.ref, t.payload, " + sourceAttachments + ", " + sourceToolOutput

func listTranscript(ctx context.Context, db queryer, id string, options TranscriptOptions) ([]object, error) {
	after, upTo := int64(-1), maxSafeInteger
	if options.After != nil {
		after = *options.After
	}
	if options.UpTo != nil {
		upTo = *options.UpTo
	}
	query := "SELECT " + transcriptFields + " FROM transcript t WHERE t.conversation_id = ? AND t.seq > ? AND t.seq <= ? ORDER BY t.id"
	if options.ModelContext {
		query = `WITH boundary AS MATERIALIZED (
 SELECT id, json_extract(payload, '$.upTo') AS up_to FROM transcript
 WHERE conversation_id = ?1 AND seq > ?2 AND seq <= ?3 AND kind = 'summary'
 ORDER BY id DESC LIMIT 1
)
SELECT ` + transcriptFields + ` FROM transcript t
WHERE t.conversation_id = ?1 AND t.seq > ?2 AND t.seq <= ?3 AND (
 t.kind IN ('message', 'prompt', 'cleared')
 OR t.id = (SELECT id FROM boundary)
 OR (t.kind != 'summary' AND
 CASE WHEN t.kind = 'step' THEN COALESCE(json_extract(t.payload, '$.sawSeq'), 0) + 0.5 ELSE t.seq END
 > COALESCE((SELECT up_to FROM boundary), -1))
)
ORDER BY t.id`
	}
	rows, err := records(ctx, db, query, id, after, upTo)
	if err != nil {
		return nil, err
	}
	return decodeTranscript(ctx, db, rows, options.UpTo)
}

func decodeTranscript(ctx context.Context, db queryer, rows []record, upTo *int64) ([]object, error) {
	messages, err := transcriptMessages(ctx, db, rows, upTo)
	if err != nil {
		return nil, err
	}
	entries := make([]object, 0, len(rows))
	for _, row := range rows {
		payload, err := row.json("payload", nil)
		if err != nil {
			return nil, err
		}
		kind := row.text("kind")
		entry := object{"seq": row["seq"], "kind": kind}
		switch kind {
		case "message":
			message, ok := messages[row.text("ref")]
			if !ok {
				continue
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
			if row.present("source_tool_output") {
				entry["content"] = row["source_tool_output"]
			}
		default:
			continue
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func transcriptMessages(ctx context.Context, db queryer, rows []record, upTo *int64) (map[string]*MessageView, error) {
	var ids []string
	for _, row := range rows {
		if row.text("kind") == "message" {
			ids = append(ids, row.text("ref"))
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	encoded, _ := json.Marshal(ids)
	values, err := readMessages(ctx, db, messageSelect+" WHERE m.id IN (SELECT value FROM json_each(?)) ORDER BY m.id, a.rowid", string(encoded))
	if err != nil {
		return nil, err
	}
	byID := make(map[string]*MessageView, len(values))
	var edited []string
	for i := range values {
		message := &values[i]
		if _, exists := byID[message.ID]; exists {
			continue
		}
		byID[message.ID] = message
		if upTo != nil && message.EditedAt != nil {
			edited = append(edited, message.ID)
		}
	}
	if len(edited) == 0 {
		return byID, nil
	}
	encoded, _ = json.Marshal(edited)
	// The latest eligible edit supplies content at the replay cursor.
	history, err := records(ctx, db, `WITH edited AS MATERIALIZED (
	 SELECT m.id, m.project_id FROM messages m WHERE m.id IN (SELECT value FROM json_each(?1))
	), latest AS (
	 SELECT json_extract(e.data,'$.messageId') AS id, max(e.seq) AS seq
	 FROM events e WHERE e.project_id IN (SELECT project_id FROM edited)
	 AND e.type = 'message.edited' AND e.seq <= ?2
	 AND EXISTS (SELECT 1 FROM edited m WHERE m.id = json_extract(e.data,'$.messageId') AND m.project_id = e.project_id)
	 GROUP BY id
	)
	SELECT l.id, json_extract(e.data,'$.content') AS content FROM latest l JOIN events e ON e.seq = l.seq`, string(encoded), *upTo)
	if err != nil {
		return nil, err
	}
	content := make(map[string]string, len(history))
	for _, row := range history {
		if row.present("content") {
			content[row.text("id")] = row.text("content")
		}
	}
	for _, id := range edited {
		message := byID[id]
		if text, ok := content[id]; ok {
			message.Content = text
		} else if original, ok := message.OriginalContent.(string); ok {
			message.Content = original
		}
	}
	return byID, nil
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
	return readTodo(ctx, reader.DB, id)
}
func readTodo(ctx context.Context, db queryer, id string) (object, error) {
	rows, err := records(ctx, db, `SELECT t.items, t.updated_at, json_extract(e.data, '$.position') AS position, e.at AS event_at
 FROM todos t LEFT JOIN events e ON e.seq = (
  SELECT seq FROM events WHERE type = 'todo.updated' AND json_extract(data, '$.conversationId') = ?1
  ORDER BY seq DESC LIMIT 1
 ) WHERE t.conversation_id = ?1`, id)
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
	result := object{"conversationId": id, "items": items, "updatedAt": row.integer("updated_at")}
	raw, err := row.json("position", nil)
	if err != nil {
		return nil, err
	}
	if position := asObject(raw); position != nil {
		at := row.integer("event_at")
		if original, ok := number(position["at"]); ok {
			at = int64(original)
		}
		position["at"] = at
		result["position"] = position
	}
	return result, nil
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
