package core

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/eventstore"
)

const (
	artifactColumns         = "id, project_id, kind, title, node_id, version, preview, created_by, updated_by, open_comments, commented_at, created_at, updated_at"
	defaultArtifactPageSize = 100
	defaultVersionPageSize  = 50
	defaultRevisionPageSize = 50
	maxPageSize             = 200
)

type ArtifactPage struct {
	Artifacts     []ArtifactSummary
	NextPageToken string
	HasMore       bool
}

type ArtifactVersionPage struct {
	Versions      []object
	NextPageToken string
	HasMore       bool
}

type ArtifactDetail struct {
	Artifact             ArtifactView
	Versions             []object
	Comments             []object
	NextVersionPageToken string
	HasMoreVersions      bool
	AskBaseline          *DocumentAskBaseline
}

func normalizePageSize(pageSize, fallback int) (int, error) {
	if pageSize < 0 {
		return 0, badInput("pageSize")
	}
	if pageSize == 0 {
		return fallback, nil
	}
	if pageSize > maxPageSize {
		return maxPageSize, nil
	}
	return pageSize, nil
}

func encodeInt64Cursor(value int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(value, 10)))
}

func decodeInt64Cursor(token string) (int64, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return 0, badInput("pageToken")
	}
	value, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil || value <= 0 {
		return 0, badInput("pageToken")
	}
	return value, nil
}

func encodeArtifactCursor(updatedAt int64, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(updatedAt, 10) + ":" + id))
}

func decodeArtifactCursor(token string) (int64, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return 0, "", badInput("pageToken")
	}
	tsPart, id, ok := strings.Cut(string(raw), ":")
	if !ok || id == "" {
		return 0, "", badInput("pageToken")
	}
	updatedAt, err := strconv.ParseInt(tsPart, 10, 64)
	if err != nil {
		return 0, "", badInput("pageToken")
	}
	return updatedAt, id, nil
}

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
func (reader Reader) ListArtifactsPage(ctx context.Context, projectID string, pageSize int, pageToken string) (ArtifactPage, error) {
	limit, err := normalizePageSize(pageSize, defaultArtifactPageSize)
	if err != nil {
		return ArtifactPage{}, err
	}
	var rows []record
	if pageToken != "" {
		cursorAt, cursorID, err := decodeArtifactCursor(pageToken)
		if err != nil {
			return ArtifactPage{}, err
		}
		rows, err = records(ctx, reader.DB,
			"SELECT "+artifactColumns+" FROM artifacts WHERE project_id = ? AND (updated_at < ? OR (updated_at = ? AND id < ?)) ORDER BY updated_at DESC, id DESC LIMIT ?",
			projectID, cursorAt, cursorAt, cursorID, limit+1)
		if err != nil {
			return ArtifactPage{}, err
		}
	} else {
		rows, err = records(ctx, reader.DB,
			"SELECT "+artifactColumns+" FROM artifacts WHERE project_id = ? ORDER BY updated_at DESC, id DESC LIMIT ?",
			projectID, limit+1)
		if err != nil {
			return ArtifactPage{}, err
		}
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	items := make([]ArtifactSummary, 0, len(rows))
	for _, row := range rows {
		view, err := artifactSummary(row)
		if err != nil {
			return ArtifactPage{}, err
		}
		items = append(items, view)
	}
	nextToken := ""
	if hasMore && len(items) > 0 {
		last := items[len(items)-1]
		nextToken = encodeArtifactCursor(last.UpdatedAt, last.ID)
	}
	return ArtifactPage{Artifacts: items, NextPageToken: nextToken, HasMore: hasMore}, nil
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
func listArtifactVersionsPage(ctx context.Context, db queryer, id string, pageSize int, pageToken string) (ArtifactVersionPage, error) {
	limit, err := normalizePageSize(pageSize, defaultVersionPageSize)
	if err != nil {
		return ArtifactVersionPage{}, err
	}
	var rows []record
	if pageToken != "" {
		beforeVersion, err := decodeInt64Cursor(pageToken)
		if err != nil {
			return ArtifactVersionPage{}, err
		}
		rows, err = records(ctx, db,
			"SELECT version, title, author, reason, created_at FROM artifact_versions WHERE artifact_id = ? AND version < ? ORDER BY version DESC LIMIT ?",
			id, beforeVersion, limit+1)
		if err != nil {
			return ArtifactVersionPage{}, err
		}
	} else {
		rows, err = records(ctx, db,
			"SELECT version, title, author, reason, created_at FROM artifact_versions WHERE artifact_id = ? ORDER BY version DESC LIMIT ?",
			id, limit+1)
		if err != nil {
			return ArtifactVersionPage{}, err
		}
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	versions := make([]object, 0, len(rows))
	var lastVersion int64
	for _, row := range rows {
		lastVersion = row.integer("version")
		versions = append(versions, object{"version": row["version"], "title": row["title"], "author": row["author"], "reason": row["reason"], "createdAt": row["created_at"]})
	}
	nextToken := ""
	if hasMore && lastVersion > 0 {
		nextToken = encodeInt64Cursor(lastVersion)
	}
	return ArtifactVersionPage{Versions: versions, NextPageToken: nextToken, HasMore: hasMore}, nil
}
func (reader Reader) GetArtifactDetail(ctx context.Context, id string, version *int64, includeVersions bool, versionPageSize int, versionPageToken string) (result ArtifactDetail, err error) {
	err = reader.snapshot(ctx, func(tx *sql.Tx) error {
		artifact, err := getArtifact(ctx, tx, id, version)
		if err != nil {
			return err
		}
		versions := []object{}
		nextVersionPageToken := ""
		hasMoreVersions := false
		if includeVersions {
			page, err := listArtifactVersionsPage(ctx, tx, id, versionPageSize, versionPageToken)
			if err != nil {
				return err
			}
			versions = page.Versions
			nextVersionPageToken = page.NextPageToken
			hasMoreVersions = page.HasMore
		}
		comments, err := artifactComments(ctx, tx, id)
		if err != nil {
			return err
		}
		var askBaseline *DocumentAskBaseline
		if version == nil && artifact.Kind == "document" {
			askBaseline, err = documentAskBaseline(ctx, tx, artifact)
			if err != nil {
				return err
			}
		}
		result = ArtifactDetail{
			Artifact:             artifact,
			Versions:             versions,
			Comments:             comments,
			NextVersionPageToken: nextVersionPageToken,
			HasMoreVersions:      hasMoreVersions,
			AskBaseline:          askBaseline,
		}
		return nil
	})
	return
}
func (reader Reader) ListArtifactVersionsPage(ctx context.Context, id string, pageSize int, pageToken string) (result ArtifactVersionPage, err error) {
	err = reader.snapshot(ctx, func(tx *sql.Tx) error {
		exists, err := records(ctx, tx, "SELECT 1 FROM artifacts WHERE id = ?", id)
		if err != nil {
			return err
		}
		if len(exists) == 0 {
			return domainerr.NotFound(id)
		}
		result, err = listArtifactVersionsPage(ctx, tx, id, pageSize, pageToken)
		return err
	})
	return
}
func (reader Reader) ListArtifactVersions(ctx context.Context, id string) (result []object, err error) {
	err = reader.snapshot(ctx, func(tx *sql.Tx) error {
		exists, err := records(ctx, tx, "SELECT 1 FROM artifacts WHERE id = ?", id)
		if err != nil {
			return err
		}
		if len(exists) == 0 {
			return domainerr.NotFound(id)
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
	return feedbackRows(ctx, db, "SELECT * FROM artifact_feedback WHERE artifact_id = ? AND withdrawn_at IS NULL ORDER BY round DESC", id)
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

type ReviewPageOptions struct {
	BeforeID *int64
	Limit    *int
}

type ReviewPage struct {
	Rounds       []object
	HasMore      bool
	NextBeforeID *int64
}

func (reader Reader) PageReviews(ctx context.Context, id string, options ReviewPageOptions) (result ReviewPage, err error) {
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
		limit := 50
		if options.Limit != nil {
			limit = max(1, min(*options.Limit, 200))
		}
		var rows []record
		if options.BeforeID != nil {
			rows, err = records(ctx, tx, "SELECT * FROM reviews WHERE thread_id = ? AND id < ? ORDER BY id DESC LIMIT ?", author, *options.BeforeID, limit+1)
		} else {
			rows, err = records(ctx, tx, "SELECT * FROM reviews WHERE thread_id = ? ORDER BY id DESC LIMIT ?", author, limit+1)
		}
		if err != nil {
			return err
		}
		if len(rows) > limit {
			result.HasMore = true
			rows = rows[:limit]
		}
		slices.Reverse(rows)
		if result.HasMore && len(rows) > 0 {
			nextID := rows[0].integer("id")
			result.NextBeforeID = &nextID
		}
		result.Rounds = make([]object, 0, len(rows))
		for _, row := range rows {
			comments, err := row.json("comments", []any{})
			if err != nil {
				return err
			}
			replies, err := row.json("replies", nil)
			if err != nil {
				return err
			}
			result.Rounds = append(result.Rounds, object{"threadId": row["thread_id"], "reviewerId": row["reviewer_id"], "round": row["round"], "head": row["head"], "requestedAt": row["requested_at"], "verdict": row["verdict"], "abandoned": row.present("abandoned_at"), "summary": row["summary"], "comments": comments, "replies": replies, "submittedAt": row["submitted_at"]})
		}
		return nil
	})
	return
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
