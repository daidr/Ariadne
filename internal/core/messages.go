package core

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strings"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/eventstore"
	"github.com/daidr/Ariadne/internal/projectsettings"
)

var attachmentSpaces = regexp.MustCompile(`(?:` + jsSpace + `|\p{Cc})+`)
var leadingDotPath = regexp.MustCompile(`^\./+`)
var skillReference = regexp.MustCompile(`^[\p{L}\p{N}](?:[\p{L}\p{N}._-]{0,62}[\p{L}\p{N}])?(?:@[a-z][a-z0-9-]{0,31})?$`)

type MessageInput struct {
	ConversationID string   `json:"conversationId"`
	Author         string   `json:"author"`
	Content        string   `json:"content"`
	Kind           string   `json:"kind"`
	Blocks         []any    `json:"blocks"`
	Attachments    []any    `json:"attachments"`
	ReplyTo        *string  `json:"replyTo"`
	Mentions       []any    `json:"mentions"`
	Comments       []any    `json:"comments"`
	Cites          []string `json:"cites"`
	MessageID      *string  `json:"messageId"`
}

func messageAttachments(w *projectionWriter, projectID string, given []any) ([]object, error) {
	if len(given) > 10 {
		return nil, domainerr.New("error.tooManyAttachments", map[string]any{"max": 10})
	}
	attachments := []object{}
	for _, raw := range given {
		item := asObject(raw)
		fileID := item.str("fileId")
		var info record
		if fileID != "" {
			info = w.one("SELECT mime, size FROM files WHERE project_id = ? AND id = ?", projectID, fileID)
		}
		if w.err != nil {
			return nil, w.err
		}
		if info == nil {
			what := fileID
			if what == "" {
				what = "file"
			}
			return nil, domainerr.NotFound(what)
		}
		name := projectsettings.Trim(attachmentSpaces.ReplaceAllString(item.str("name"), " "))
		name = utf16Slice(name, 0, 200)
		if name == "" {
			name = fileID
		}
		attachments = append(attachments, object{"fileId": fileID, "name": name, "mime": info["mime"], "size": info["size"]})
	}
	return attachments, nil
}
func quoteMessage(w *projectionWriter, id, messageID string) (object, error) {
	row := w.one("SELECT author, kind, content, cites FROM messages WHERE id = ? AND conversation_id = ?", messageID, id)
	if w.err != nil {
		return nil, w.err
	}
	if row == nil {
		return nil, domainerr.NotFound(messageID)
	}
	cites, _ := w.parsed(row, "cites", []any{}).([]any)
	if w.err != nil {
		return nil, w.err
	}
	relayed := row.text("kind") == "relay" && len(cites) > 0
	text := row.text("content")
	author := row.text("author")
	if relayed {
		parts := []string{}
		for _, cite := range cites {
			parts = append(parts, asObject(cite).str("content"))
		}
		text = strings.Join(parts, " ")
		author = "user"
	}
	text = projectsettings.Trim(spaces.ReplaceAllString(text, " "))
	letters := []rune(text)
	if len(letters) > 120 {
		text = projectsettings.Trim(string(letters[:120])) + "…"
	}
	return object{"messageId": messageID, "author": author, "text": text}, nil
}
func messageMentions(w *projectionWriter, projectID string, given []any) ([]object, error) {
	if len(given) > 20 {
		return nil, badInput("mentions")
	}
	seen := map[string]bool{}
	mentions := []object{}
	for _, raw := range given {
		item := asObject(raw)
		kind := item.str("kind")
		var mention object
		var key string
		switch kind {
		case "file":
			clean := leadingDotPath.ReplaceAllString(projectsettings.Trim(item.str("path")), "")
			if clean == "" || utf16Length(clean) > 1000 || strings.ContainsAny(clean, "\x00\n\r") || strings.HasPrefix(clean, "/") {
				return nil, badInput("mentions")
			}
			mention = object{"kind": kind, "path": clean}
			key = "file:" + clean
		case "skill":
			name := item.str("name")
			if !skillReference.MatchString(name) {
				return nil, badInput("mentions")
			}
			mention = object{"kind": kind, "name": name}
			key = "skill:" + name
		case "thread", "artifact":
			id, idOK := item["id"].(string)
			var row record
			if idOK {
				table := "threads"
				if kind == "artifact" {
					table = "artifacts"
				}
				row = w.one("SELECT title FROM "+table+" WHERE id = ? AND project_id = ?", id, projectID)
			}
			if w.err != nil {
				return nil, w.err
			}
			if row == nil {
				what := kind
				if idOK {
					what = id
				}
				return nil, domainerr.NotFound(what)
			}
			mention = object{"kind": kind, "id": id, "title": row["title"]}
			key = kind + ":" + id
		default:
			return nil, badInput("mentions")
		}
		if !seen[key] {
			seen[key] = true
			mentions = append(mentions, mention)
		}
	}
	return mentions, nil
}
func lineComments(given []any) ([]object, error) {
	if len(given) > 100 {
		return nil, badInput("comments")
	}
	comments := []object{}
	for _, raw := range given {
		item := asObject(raw)
		path, pathOK := item["path"].(string)
		body, bodyOK := item["body"].(string)
		line, lineOK := number(item["line"])
		if !pathOK || projectsettings.Trim(path) == "" || utf16Length(path) > 1000 || !bodyOK || projectsettings.Trim(body) == "" || utf16Length(body) > 4000 || !lineOK || line != math.Trunc(line) || line < 1 {
			return nil, badInput("comments")
		}
		if side, has := item["side"]; has && side != "original" {
			return nil, badInput("comments")
		}
		comment := object{"path": projectsettings.Trim(path), "line": item["line"], "quote": utf16Slice(item.str("quote"), 0, 500), "body": projectsettings.Trim(body)}
		if item["side"] == "original" {
			comment["side"] = "original"
		}
		comments = append(comments, comment)
	}
	return comments, nil
}
func messageCites(w *projectionWriter, projectID string, ids []string) ([]object, error) {
	unique := []string{}
	seen := map[string]bool{}
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}
	if len(unique) > 8 {
		return nil, badInput("cites")
	}
	cites := []object{}
	for _, id := range unique {
		row := w.one("SELECT conversation_id, author, content, created_at, attachments, mentions FROM messages WHERE id = ? AND project_id = ?", id, projectID)
		if w.err != nil {
			return nil, w.err
		}
		if row == nil || row.text("author") != "user" {
			return nil, domainerr.NotFound(id)
		}
		cite := object{"messageId": id, "conversationId": row["conversation_id"], "content": row["content"], "at": row["created_at"]}
		for _, key := range []string{"attachments", "mentions"} {
			if row.text(key) != "" {
				cite[key] = w.parsed(row, key, nil)
			}
		}
		cites = append(cites, cite)
	}
	return cites, w.err
}
func resumedByUser(w *projectionWriter, threadID string) []change {
	if threadID == "" {
		return nil
	}
	row := w.one("SELECT project_id, stopped_reason FROM threads WHERE id = ?", threadID)
	if row.text("stopped_reason") == "" {
		return nil
	}
	return []change{{"thread.resumed", object{"threadId": threadID, "by": "user", "reason": row["stopped_reason"]}}}
}

type AttentionInput struct {
	ThreadID  *string
	Block     map[string]any
	MessageID *string
	Purpose   *string
}

func attentionOpened(id string, input AttentionInput, choice bool) (change, error) {
	block, err := ParseBlock(input.Block)
	if err != nil {
		return change{}, err
	}
	if !IsAskBlock(block) {
		return change{}, domainerr.New("error.blockInvalid", map[string]any{"reason": "not an ask block"})
	}
	if input.Purpose != nil && *input.Purpose != "" && block.str("type") != "confirm" && !(choice && block.str("type") == "choice") {
		return change{}, domainerr.New("error.blockInvalid", map[string]any{"reason": "an approval is a confirm block"})
	}
	data := object{"attentionId": id, "threadId": input.ThreadID, "block": block, "messageId": input.MessageID}
	if input.Purpose != nil && *input.Purpose != "" {
		data["purpose"] = *input.Purpose
	}
	return change{"attention.opened", data}, nil
}
func (repo *Repository) OpenAttention(ctx context.Context, projectID string, input AttentionInput) (result AttentionView, err error) {
	err = repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		w := &projectionWriter{ctx: ctx, tx: tx.SQL()}
		if project := w.one("SELECT id FROM projects WHERE id = ?", projectID); project == nil {
			if w.err != nil {
				return w.err
			}
			return domainerr.NotFound(projectID)
		}
		if input.ThreadID != nil && *input.ThreadID != "" {
			thread := w.one("SELECT project_id FROM threads WHERE id = ?", *input.ThreadID)
			if w.err != nil {
				return w.err
			}
			if thread.text("project_id") != projectID {
				return domainerr.NotFound(*input.ThreadID)
			}
		}
		id, err := newID("att")
		if err != nil {
			return err
		}
		opened, err := attentionOpened(id, input, false)
		if err != nil {
			return err
		}
		if err := appendChanges(tx, projectID, []change{opened}); err != nil {
			return err
		}
		attentions, err := openAttentions(ctx, tx.SQL(), projectID)
		if err != nil {
			return err
		}
		for _, view := range attentions {
			if view.ID == id {
				result = view
				return nil
			}
		}
		return fmt.Errorf("attention projection missing")
	})
	return
}
func postMessage(ctx context.Context, tx *eventstore.Transaction, projectID string, input MessageInput) (MessageView, error) {
	if err := assertConversationIn(ctx, tx.SQL(), projectID, input.ConversationID); err != nil {
		return MessageView{}, err
	}
	w := &projectionWriter{ctx: ctx, tx: tx.SQL()}
	attachments, err := messageAttachments(w, projectID, input.Attachments)
	if err != nil {
		return MessageView{}, err
	}
	var replyTo any
	if input.ReplyTo != nil {
		replyTo, err = quoteMessage(w, input.ConversationID, *input.ReplyTo)
		if err != nil {
			return MessageView{}, err
		}
	}
	mentions, err := messageMentions(w, projectID, input.Mentions)
	if err != nil {
		return MessageView{}, err
	}
	comments, err := lineComments(input.Comments)
	if err != nil {
		return MessageView{}, err
	}
	if (len(mentions) > 0 || len(comments) > 0) && input.Author != "user" {
		reason := "comments"
		if len(mentions) > 0 {
			reason = "mentions"
		}
		return MessageView{}, badInput(reason)
	}
	coordinatorID := w.one("SELECT coordinator_id FROM projects WHERE id = ?", projectID).text("coordinator_id")
	if w.err != nil {
		return MessageView{}, w.err
	}
	if len(comments) > 0 && input.ConversationID == coordinatorID {
		return MessageView{}, badInput("comments")
	}
	kind := input.Kind
	if kind == "" {
		kind = "chat"
	}
	if kind == "relay" && input.Author != "coordinator" {
		return MessageView{}, badInput("kind")
	}
	if len(input.Cites) > 0 && kind != "relay" {
		return MessageView{}, badInput("cites")
	}
	cites, err := messageCites(w, projectID, input.Cites)
	if err != nil {
		return MessageView{}, err
	}
	blocks := []object{}
	for i, raw := range input.Blocks {
		block, err := ParseBlock(raw, fmt.Sprintf("blocks[%d]", i))
		if err != nil {
			return MessageView{}, err
		}
		blocks = append(blocks, block)
	}
	if len(blocks) > 0 && (input.Author == "user" || kind != "chat") {
		return MessageView{}, badInput("blocks")
	}
	if len(blocks) > 6 {
		return MessageView{}, domainerr.New("error.blockInvalid", map[string]any{"reason": "a message holds at most 6 blocks"})
	}
	shown, asks := []object{}, []object{}
	for _, block := range blocks {
		if IsAskBlock(block) {
			asks = append(asks, block)
		} else {
			shown = append(shown, block)
		}
	}
	if len(asks) > 1 {
		return MessageView{}, domainerr.New("error.blockInvalid", map[string]any{"reason": "a message asks at most one thing: one choice, confirm or form"})
	}
	for i, block := range blocks {
		if block.str("type") == "thread" {
			owner := w.one("SELECT project_id FROM threads WHERE id = ?", block["threadId"])
			if w.err != nil {
				return MessageView{}, w.err
			}
			if owner.text("project_id") != projectID {
				return MessageView{}, domainerr.New("error.blockInvalid", map[string]any{"reason": fmt.Sprintf("blocks[%d].threadId is not a thread of this project", i)})
			}
		}
	}
	if projectsettings.Trim(input.Content) == "" && len(blocks) == 0 && len(attachments) == 0 && len(comments) == 0 && len(cites) == 0 {
		return MessageView{}, badInput("content")
	}
	id := ""
	if input.MessageID != nil {
		id = *input.MessageID
	} else {
		id, err = newID("msg")
		if err != nil {
			return MessageView{}, err
		}
	}
	changes := []change{}
	if input.Author == "user" {
		changes = append(changes, resumedByUser(w, input.ConversationID)...)
	}
	data := object{"messageId": id, "conversationId": input.ConversationID, "author": input.Author, "kind": kind, "content": input.Content}
	for _, field := range []struct {
		key    string
		values []object
	}{{"blocks", shown}, {"attachments", attachments}, {"mentions", mentions}, {"comments", comments}, {"cites", cites}} {
		if len(field.values) > 0 {
			data[field.key] = field.values
		}
	}
	if replyTo != nil {
		data["replyTo"] = replyTo
	}
	changes = append(changes, change{"message.posted", data})
	var asker *string
	if input.ConversationID != coordinatorID {
		asker = &input.ConversationID
	}
	for _, block := range asks {
		attentionID, err := newID("att")
		if err != nil {
			return MessageView{}, err
		}
		opened, err := attentionOpened(attentionID, AttentionInput{ThreadID: asker, Block: block, MessageID: &id}, false)
		if err != nil {
			return MessageView{}, err
		}
		changes = append(changes, opened)
	}
	if w.err != nil {
		return MessageView{}, w.err
	}
	if err := appendChanges(tx, projectID, changes); err != nil {
		return MessageView{}, err
	}
	message, err := findMessage(ctx, tx.SQL(), id)
	if err != nil {
		return MessageView{}, err
	}
	if message == nil {
		return MessageView{}, fmt.Errorf("message projection missing")
	}
	return *message, nil
}
func (repo *Repository) PostMessage(ctx context.Context, projectID string, input MessageInput) (result MessageView, err error) {
	err = repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		result, err = postMessage(ctx, tx, projectID, input)
		return err
	})
	return
}
func (repo *Repository) EditMessage(ctx context.Context, id, author, conversationID, content, reason string) (result MessageView, err error) {
	err = repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		message, err := findMessage(ctx, tx.SQL(), id)
		if err != nil {
			return err
		}
		if message == nil {
			return domainerr.NotFound(id)
		}
		if message.Author != author || message.ConversationID != conversationID || message.Kind == "relay" {
			return badInput("editor")
		}
		rows, err := records(ctx, tx.SQL(), "SELECT project_id FROM messages WHERE id = ?", id)
		if err != nil {
			return err
		}
		if err := appendChanges(tx, rows[0].text("project_id"), []change{{"message.edited", object{"messageId": id, "content": content, "reason": reason}}}); err != nil {
			return err
		}
		after, err := findMessage(ctx, tx.SQL(), id)
		if err != nil {
			return err
		}
		result = *after
		return nil
	})
	return
}
func (repo *Repository) MarkRead(ctx context.Context, id, upTo string) error {
	return repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		message, err := findMessage(ctx, tx.SQL(), upTo)
		if err != nil {
			return err
		}
		if message == nil || message.ConversationID != id {
			return domainerr.NotFound(upTo)
		}
		rows, err := records(ctx, tx.SQL(), "SELECT 1 AS n FROM messages WHERE conversation_id = ? AND id <= ? AND author != 'user' AND (read_at IS NULL OR edited_since_read = 1) LIMIT 1", id, upTo)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		owner, err := records(ctx, tx.SQL(), "SELECT project_id FROM messages WHERE id = ?", upTo)
		if err != nil {
			return err
		}
		return appendChanges(tx, owner[0].text("project_id"), []change{{"message.read", object{"conversationId": id, "upTo": upTo}}})
	})
}
