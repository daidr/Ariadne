package core

import (
	"context"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/eventstore"
	"github.com/daidr/Ariadne/internal/projectsettings"
	"github.com/daidr/Ariadne/internal/textutil"
)

func putReaction(ctx context.Context, tx *eventstore.Transaction, message MessageView, by, emoji string, remove bool) (MessageView, error) {
	clean := projectsettings.Trim(emoji)
	if !textutil.IsEmoji(clean) {
		return MessageView{}, badInput("emoji")
	}
	has := false
	count := 0
	for _, reaction := range refs(message.Reactions) {
		if reaction.str("by") == by {
			count++
			has = has || reaction.str("emoji") == clean
		}
	}
	if has != remove {
		return message, nil
	}
	if !remove && count >= 3 {
		return MessageView{}, badInput("reactions")
	}
	w := &projectionWriter{ctx: ctx, tx: tx.SQL()}
	owner := w.one("SELECT project_id FROM messages WHERE id = ?", message.ID)
	data := object{"messageId": message.ID, "conversationId": message.ConversationID, "by": by, "emoji": clean}
	if remove {
		data["removed"] = true
	}
	if by == "user" {
		quote, err := quoteMessage(w, message.ConversationID, message.ID)
		if err != nil {
			return MessageView{}, err
		}
		data["quote"] = quote["text"]
	}
	if w.err != nil {
		return MessageView{}, w.err
	}
	if err := appendChanges(tx, owner.text("project_id"), []change{{"message.reacted", data}}); err != nil {
		return MessageView{}, err
	}
	after, err := findMessage(ctx, tx.SQL(), message.ID)
	if err != nil {
		return MessageView{}, err
	}
	return *after, nil
}
func (repo *Repository) React(ctx context.Context, id, author, conversationID, emoji string, remove bool) (result MessageView, err error) {
	err = repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		message, err := findMessage(ctx, tx.SQL(), id)
		if err != nil {
			return err
		}
		if message == nil || author == "user" || message.ConversationID != conversationID {
			return domainerr.NotFound(id)
		}
		if message.Author == author && message.Kind != "relay" {
			return badInput("messageId")
		}
		result, err = putReaction(ctx, tx, *message, author, emoji, remove)
		return err
	})
	return
}
func (repo *Repository) ReactAsUser(ctx context.Context, id, emoji string, remove bool) (result MessageView, err error) {
	err = repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		message, err := findMessage(ctx, tx.SQL(), id)
		if err != nil {
			return err
		}
		if message == nil {
			return domainerr.NotFound(id)
		}
		if message.Author == "user" || message.Kind == "relay" {
			return badInput("messageId")
		}
		result, err = putReaction(ctx, tx, *message, "user", emoji, remove)
		return err
	})
	return
}
