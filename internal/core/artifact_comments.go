package core

import (
	"context"
	"fmt"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/eventstore"
	"github.com/daidr/Ariadne/internal/files"
	"github.com/daidr/Ariadne/internal/textutil"
)

func commentText(body string) (string, error) {
	clean := textutil.Trim(body)
	if clean == "" || utf16Length(clean) > maxCommentBody {
		return "", badInput("body")
	}
	return clean, nil
}
func checkedComment(kind, by string, action, thread object) (object, error) {
	checked := except(action, "")
	if action.str("type") == "create" {
		anchor := action.obj("anchor")
		if kind == "whiteboard" || truthy(action["anchor"]) {
			id, isNode := anchor["node"]
			_, isEdge := anchor["edge"]
			if !isNode {
				id = anchor["edge"]
			}
			text, isString := id.(string)
			if anchor == nil || !isString || text == "" || utf16Length(text) > 200 {
				return nil, badInput("anchor")
			}
			if isNode == isEdge {
				return nil, badInput("anchor")
			}
			block, hasBlock := anchor["block"]
			blockText, stringBlock := block.(string)
			if kind == "document" {
				if !stringBlock || textutil.Trim(blockText) == "" || utf16Length(blockText) > 200 {
					return nil, badInput("anchor.block")
				}
			} else if hasBlock {
				return nil, badInput("anchor.block")
			}
			body, err := commentText(action.str("body"))
			if err != nil {
				return nil, err
			}
			quote := ""
			if kind == "document" {
				quote = utf16Slice(textutil.Trim(action.str("quote")), 0, maxCommentQuote)
			}
			cleanAnchor := object{}
			if isNode {
				cleanAnchor["node"] = text
			} else {
				cleanAnchor["edge"] = text
			}
			if truthy(block) {
				cleanAnchor["block"] = block
			}
			return object{"type": "create", "threadId": action["threadId"], "commentId": action["commentId"], "quote": quote, "body": body, "anchor": cleanAnchor}, nil
		}
		quote := textutil.Trim(action.str("quote"))
		if quote == "" || utf16Length(quote) > maxCommentQuote {
			return nil, badInput("quote")
		}
		body, err := commentText(action.str("body"))
		if err != nil {
			return nil, err
		}
		delete(checked, "anchor")
		checked["quote"], checked["body"] = quote, body
		return checked, nil
	}
	if thread == nil {
		return nil, domainerr.NotFound(action.str("threadId"))
	}
	var comment object
	comments := refs(thread["comments"])
	for _, item := range comments {
		if item.str("id") == action.str("commentId") {
			comment = item
			break
		}
	}
	switch action.str("type") {
	case "reply":
		if comment != nil {
			return nil, badInput("commentId")
		}
		body, err := commentText(action.str("body"))
		if err != nil {
			return nil, err
		}
		checked["body"] = body
	case "edit":
		if comment == nil {
			return nil, domainerr.NotFound(action.str("commentId"))
		}
		if comment.str("author") != by {
			return nil, badInput("author")
		}
		body, err := commentText(action.str("body"))
		if err != nil {
			return nil, err
		}
		checked["body"] = body
	case "delete":
		if comment == nil {
			return nil, domainerr.NotFound(action.str("commentId"))
		}
		if comment.str("author") != by || len(comments) > 0 && comments[0].str("id") == comment.str("id") {
			return nil, badInput("author")
		}
	case "remove":
		if by != "user" {
			return nil, badInput("author")
		}
	case "resolve", "reopen":
		if (action.str("type") == "resolve") == truthy(thread["resolved"]) {
			return nil, nil
		}
	default:
		return nil, badInput("type")
	}
	return checked, nil
}
func (repo *Repository) CommentOnArtifact(ctx context.Context, artifactID, by string, action map[string]any) (result []object, err error) {
	err = repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		artifact, err := getArtifact(ctx, tx.SQL(), artifactID, nil)
		if err != nil {
			return err
		}
		if artifact.Kind == "dashboard" {
			return badInput("kind")
		}
		comments, err := artifactComments(ctx, tx.SQL(), artifactID)
		if err != nil {
			return err
		}
		var thread object
		input := object(action)
		for _, candidate := range comments {
			if candidate.str("id") == input.str("threadId") {
				thread = candidate
				break
			}
		}
		if input.str("type") == "create" {
			w := &projectionWriter{ctx: ctx, tx: tx.SQL()}
			taken := w.one("SELECT id FROM artifact_comments WHERE id = ?", input.str("threadId"))
			if w.err != nil {
				return w.err
			}
			if taken != nil {
				return badInput("threadId")
			}
		}
		checked, err := checkedComment(artifact.Kind, by, input, thread)
		if err != nil {
			return err
		}
		if checked == nil {
			result = comments
			return nil
		}
		if err := appendChanges(tx, artifact.ProjectID, []change{{"artifact.commented", object{"artifactId": artifactID, "by": by, "action": checked, "notify": nil}}}); err != nil {
			return err
		}
		result, err = artifactComments(ctx, tx.SQL(), artifactID)
		return err
	})
	return
}

type ArtifactFeedback struct {
	Items  []any    `json:"items"`
	Images []string `json:"images"`
}

func (repo *Repository) SendArtifactFeedback(ctx context.Context, artifactID string, input ArtifactFeedback) (result object, err error) {
	return repo.sendArtifactFeedback(ctx, artifactID, input.Images, func(_ *projectionWriter, _ ArtifactView) ([]any, error) {
		return input.Items, nil
	})
}

func (repo *Repository) SendPendingArtifactFeedback(ctx context.Context, artifactID string, images []string) (object, error) {
	return repo.sendArtifactFeedback(ctx, artifactID, images, pendingArtifactFeedback)
}

func (repo *Repository) sendArtifactFeedback(ctx context.Context, artifactID string, fileIDs []string, itemsOf func(*projectionWriter, ArtifactView) ([]any, error)) (result object, err error) {
	err = repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		artifact, err := getArtifact(ctx, tx.SQL(), artifactID, nil)
		if err != nil {
			return err
		}
		w := &projectionWriter{ctx: ctx, tx: tx.SQL()}
		notify := artifactListener(w, artifactID)
		if w.err != nil {
			return w.err
		}
		if notify == nil {
			return nil
		}
		items, err := itemsOf(w, artifact)
		if err != nil || len(items) == 0 {
			return err
		}
		images := []object{}
		for i, fileID := range fileIDs {
			file := w.one("SELECT mime, size FROM files WHERE project_id = ? AND id = ?", artifact.ProjectID, fileID)
			if w.err != nil {
				return w.err
			}
			if file == nil || !files.IsImage(file.text("mime")) {
				return badInput("images")
			}
			if i < 6 {
				images = append(images, object{"fileId": fileID, "name": fmt.Sprintf("%s %d.png", artifact.Title, i+1), "mime": file["mime"], "size": file["size"]})
			}
		}
		latest := w.one("SELECT round FROM artifact_feedback WHERE artifact_id = ? ORDER BY round DESC LIMIT 1", artifactID)
		if w.err != nil {
			return w.err
		}
		data := object{"artifactId": artifactID, "round": latest.integer("round") + 1, "version": artifact.Version, "notify": notify, "items": items, "images": images}
		if err := appendChanges(tx, artifact.ProjectID, []change{{"artifact.feedback", data}}); err != nil {
			return err
		}
		feedback, err := feedbackRows(ctx, tx.SQL(), "SELECT * FROM artifact_feedback WHERE artifact_id = ? ORDER BY round DESC LIMIT 1", artifactID)
		if err != nil {
			return err
		}
		if len(feedback) > 0 {
			result = feedback[0]
		}
		return nil
	})
	return
}
func (repo *Repository) FinishArtifactFeedback(ctx context.Context, artifactID string, round int64, by, summary string) (result object, err error) {
	err = repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		artifact, err := getArtifact(ctx, tx.SQL(), artifactID, nil)
		if err != nil {
			return err
		}
		const roundQuery = "SELECT * FROM artifact_feedback WHERE artifact_id = ? AND round = ?"
		feedback, err := feedbackRows(ctx, tx.SQL(), roundQuery, artifactID, round)
		if err != nil {
			return err
		}
		if len(feedback) != 0 {
			result = feedback[0]
		}
		if result == nil {
			return domainerr.NotFound(fmt.Sprintf("%s round %d", artifactID, round))
		}
		if result["doneAt"] != nil {
			return nil
		}
		data := object{"artifactId": artifactID, "round": round, "by": by, "summary": utf16Slice(textutil.Trim(summary), 0, maxCommentBody)}
		if err := appendChanges(tx, artifact.ProjectID, []change{{"artifact.feedback_done", data}}); err != nil {
			return err
		}
		feedback, err = feedbackRows(ctx, tx.SQL(), roundQuery, artifactID, round)
		if err != nil {
			return err
		}
		if len(feedback) != 0 {
			result = feedback[0]
		}
		return nil
	})
	return
}
