package core

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/eventstore"
)

const artifactColumns = "id, project_id, kind, title, node_id, version, preview, created_by, updated_by, open_comments, commented_at, created_at, updated_at"

type ArtifactSummary struct {
	Preview      any    `json:"preview"`
	ID           string `json:"id"`
	ProjectID    string `json:"projectId"`
	Kind         string `json:"kind"`
	Title        string `json:"title"`
	NodeID       any    `json:"nodeId"`
	Version      int64  `json:"version"`
	CreatedBy    string `json:"createdBy"`
	UpdatedBy    string `json:"updatedBy"`
	OpenComments int64  `json:"openComments"`
	CommentedAt  int64  `json:"commentedAt"`
	CreatedAt    int64  `json:"createdAt"`
	UpdatedAt    int64  `json:"updatedAt"`
}
type ArtifactView struct {
	ArtifactSummary
	Content string `json:"content"`
}

func artifactSummary(row record) (ArtifactSummary, error) {
	preview, err := row.json("preview", nil)
	if err != nil {
		return ArtifactSummary{}, err
	}
	return ArtifactSummary{Preview: preview, ID: row.text("id"), ProjectID: row.text("project_id"), Kind: row.text("kind"), Title: row.text("title"), NodeID: row["node_id"], Version: row.integer("version"), CreatedBy: row.text("created_by"), UpdatedBy: row.text("updated_by"), OpenComments: row.integer("open_comments"), CommentedAt: row.integer("commented_at"), CreatedAt: row.integer("created_at"), UpdatedAt: row.integer("updated_at")}, nil
}
func (reader Reader) ListArtifacts(ctx context.Context, projectID string) ([]ArtifactSummary, error) {
	rows, err := records(ctx, reader.DB, "SELECT "+artifactColumns+" FROM artifacts WHERE project_id = ? ORDER BY updated_at DESC, id DESC", projectID)
	if err != nil {
		return nil, err
	}
	result := []ArtifactSummary{}
	for _, row := range rows {
		view, err := artifactSummary(row)
		if err != nil {
			return nil, err
		}
		result = append(result, view)
	}
	return result, nil
}
func getArtifact(ctx context.Context, db queryer, id string, version *int64) (ArtifactView, error) {
	rows, err := records(ctx, db, "SELECT * FROM artifacts WHERE id = ?", id)
	if err != nil {
		return ArtifactView{}, err
	}
	if len(rows) == 0 {
		return ArtifactView{}, domainerr.NotFound(id)
	}
	row := rows[0]
	summary, err := artifactSummary(row)
	if err != nil {
		return ArtifactView{}, err
	}
	view := ArtifactView{ArtifactSummary: summary, Content: row.text("content")}
	if version == nil || *version == summary.Version {
		return view, nil
	}
	old, err := records(ctx, db, "SELECT title, content, author, created_at FROM artifact_versions WHERE artifact_id = ? AND version = ?", id, *version)
	if err != nil {
		return ArtifactView{}, err
	}
	if len(old) == 0 {
		return ArtifactView{}, domainerr.NotFound(fmt.Sprintf("%s@%d", id, *version))
	}
	view.Title = old[0].text("title")
	view.Version = *version
	view.Content = old[0].text("content")
	view.UpdatedBy = old[0].text("author")
	view.UpdatedAt = old[0].integer("created_at")
	return view, nil
}
func (reader Reader) GetArtifact(ctx context.Context, id string, version *int64) (result ArtifactView, err error) {
	err = reader.snapshot(ctx, func(tx *sql.Tx) error { result, err = getArtifact(ctx, tx, id, version); return err })
	return
}
func (reader Reader) ListArtifactVersions(ctx context.Context, id string) (result []object, err error) {
	err = reader.snapshot(ctx, func(tx *sql.Tx) error {
		metadata, err := records(ctx, tx, "SELECT "+artifactColumns+" FROM artifacts WHERE id = ?", id)
		if err != nil {
			return err
		}
		if len(metadata) == 0 {
			return domainerr.NotFound(id)
		}
		if _, err := artifactSummary(metadata[0]); err != nil {
			return err
		}
		rows, err := records(ctx, tx, "SELECT version, title, author, reason, created_at FROM artifact_versions WHERE artifact_id = ? ORDER BY version DESC", id)
		if err != nil {
			return err
		}
		result = []object{}
		for _, row := range rows {
			result = append(result, object{"version": row["version"], "title": row["title"], "author": row["author"], "reason": row["reason"], "createdAt": row["created_at"]})
		}
		return nil
	})
	return
}
func (reader Reader) ListArtifactComments(ctx context.Context, id string) ([]object, error) {
	return artifactComments(ctx, reader.DB, id)
}
func artifactComments(ctx context.Context, db queryer, id string) ([]object, error) {
	rows, err := records(ctx, db, "SELECT * FROM artifact_comments WHERE artifact_id = ? ORDER BY created_at, rowid", id)
	if err != nil {
		return nil, err
	}
	result := []object{}
	for _, row := range rows {
		anchor, err := row.json("anchor", nil)
		if err != nil {
			return nil, err
		}
		comments, err := row.json("comments", []any{})
		if err != nil {
			return nil, err
		}
		var resolved any
		if row.text("resolved_by") != "" {
			resolved = object{"by": row["resolved_by"], "at": row.integer("resolved_at")}
		}
		result = append(result, object{"id": row["id"], "artifactId": row["artifact_id"], "quote": row["quote"], "anchor": anchor, "comments": comments, "resolved": resolved, "createdAt": row["created_at"]})
	}
	return result, nil
}
func (reader Reader) ListArtifactFeedback(ctx context.Context, id string) ([]object, error) {
	return artifactFeedback(ctx, reader.DB, id)
}
func artifactFeedback(ctx context.Context, db queryer, id string) ([]object, error) {
	return feedbackRows(ctx, db, "SELECT * FROM artifact_feedback WHERE artifact_id = ? ORDER BY round DESC", id)
}

func feedbackRows(ctx context.Context, db queryer, query string, args ...any) ([]object, error) {
	rows, err := records(ctx, db, query, args...)
	if err != nil {
		return nil, err
	}
	result := []object{}
	for _, row := range rows {
		items, err := row.json("items", []any{})
		if err != nil {
			return nil, err
		}
		images, err := row.json("images", []any{})
		if err != nil {
			return nil, err
		}
		result = append(result, object{"artifactId": row["artifact_id"], "round": row["round"], "version": row["version"], "notify": row["notify"], "items": items, "images": images, "doneBy": row["done_by"], "doneAt": row["done_at"], "summary": row["summary"], "createdAt": row["created_at"]})
	}
	return result, nil
}

type SharedState struct {
	Version int64
	State   []byte
}

func (reader Reader) ReadSharedState(ctx context.Context, id string) (*SharedState, error) {
	rows, err := records(ctx, reader.DB, "SELECT version, state FROM artifact_shared WHERE artifact_id = ?", id)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	state, _ := rows[0]["state"].([]byte)
	return &SharedState{Version: rows[0].integer("version"), State: append([]byte{}, state...)}, nil
}
func (repo *Repository) WriteSharedState(ctx context.Context, id string, shared SharedState) error {
	return repo.writeShared(ctx, id, shared, false)
}

func (repo *Repository) CheckpointSharedState(ctx context.Context, id string, shared SharedState) error {
	return repo.writeShared(ctx, id, shared, true)
}

func (repo *Repository) writeShared(ctx context.Context, id string, shared SharedState, verifyVersion bool) error {
	return repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		rows, err := records(ctx, tx.SQL(), "SELECT project_id, version FROM artifacts WHERE id = ?", id)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return domainerr.NotFound(id)
		}
		if current := rows[0].integer("version"); verifyVersion && current != shared.Version {
			return domainerr.New("error.artifactConflict", map[string]any{"version": current})
		}
		return writeSharedState(ctx, tx, rows[0].text("project_id"), id, shared, repo.Reader.now())
	})
}

func writeSharedState(ctx context.Context, tx *eventstore.Transaction, projectID, id string, shared SharedState, now int64) error {
	_, err := tx.SQL().ExecContext(ctx, `INSERT INTO artifact_shared (artifact_id, project_id, version, state, updated_at)
		VALUES (?, ?, ?, ?, ?) ON CONFLICT (artifact_id) DO UPDATE
		SET version = excluded.version, state = excluded.state, updated_at = excluded.updated_at`,
		id, projectID, shared.Version, shared.State, now)
	return err
}

func (reader Reader) ListReviews(ctx context.Context, id string) (result []object, err error) {
	err = reader.snapshot(ctx, func(tx *sql.Tx) error {
		threads, err := records(ctx, tx, threadSelect+" WHERE t.id = ?", id)
		if err != nil {
			return err
		}
		if len(threads) == 0 {
			return domainerr.NotFound(id)
		}
		if _, err := toThread(threads[0], reader.now()); err != nil {
			return err
		}
		author := id
		if threads[0].present("review_of") {
			author = threads[0].text("review_of")
		}
		rows, err := records(ctx, tx, "SELECT * FROM reviews WHERE thread_id = ? ORDER BY id", author)
		if err != nil {
			return err
		}
		result = []object{}
		for _, row := range rows {
			comments, err := row.json("comments", []any{})
			if err != nil {
				return err
			}
			replies, err := row.json("replies", nil)
			if err != nil {
				return err
			}
			result = append(result, object{"threadId": row["thread_id"], "reviewerId": row["reviewer_id"], "round": row["round"], "head": row["head"], "requestedAt": row["requested_at"], "verdict": row["verdict"], "abandoned": row.present("abandoned_at"), "summary": row["summary"], "comments": comments, "replies": replies, "submittedAt": row["submitted_at"]})
		}
		return nil
	})
	return
}
