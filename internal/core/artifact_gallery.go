package core

import (
	"context"
	"database/sql"
	"encoding/base64"
	"strconv"
	"strings"
)

const (
	defaultGalleryPageSize = 48
	galleryArtifactColumns = "a.id, a.project_id, a.kind, a.title, a.node_id, a.version, a.preview, a.created_by, a.updated_by, a.open_comments, a.commented_at, a.created_at, a.updated_at, a.has_prototype, a.pending_actions, (SELECT COUNT(*) FROM artifact_feedback f WHERE f.artifact_id = a.id AND f.done_at IS NULL AND f.withdrawn_at IS NULL) AS pending_feedback"
)

type GalleryArtifactsQuery struct {
	ProjectID      string
	Kind           string
	OnlyPrototypes bool
	Status         string
	Query          string
	Sort           string
	PageSize       int
	PageToken      string
	IncludeCounts  bool
}

type GalleryArtifactSummary struct {
	Artifact             ArtifactSummary `json:"artifact"`
	ProjectName          string          `json:"projectName"`
	UpdatedByLabel       string          `json:"updatedByLabel,omitempty"`
	UpdatedByCoordinator bool            `json:"updatedByCoordinator"`
	HasPrototype         bool            `json:"hasPrototype"`
	PendingActions       int64           `json:"pendingActions"`
	PendingFeedback      int64           `json:"pendingFeedback"`
	NodeTitle            string          `json:"nodeTitle,omitempty"`
}

type GalleryProjectCount struct {
	ProjectID   string `json:"projectId"`
	ProjectName string `json:"projectName"`
	Count       int64  `json:"count"`
}

type GalleryCounts struct {
	Total           int64                 `json:"total"`
	Documents       int64                 `json:"documents"`
	Whiteboards     int64                 `json:"whiteboards"`
	Dashboards      int64                 `json:"dashboards"`
	Prototypes      int64                 `json:"prototypes"`
	NeedsMe         int64                 `json:"needsMe"`
	FeedbackPending int64                 `json:"feedbackPending"`
	Projects        []GalleryProjectCount `json:"projects"`
}

type GalleryArtifactPage struct {
	Items         []GalleryArtifactSummary `json:"items"`
	NextPageToken string                   `json:"nextPageToken"`
	HasMore       bool                     `json:"hasMore"`
	Counts        *GalleryCounts           `json:"counts,omitempty"`
}

func sqliteLower(value string) string {
	var b strings.Builder
	b.Grow(len(value))
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b.WriteByte(c)
	}
	return b.String()
}

func encodeGalleryCursor(sort string, updatedAt, createdAt int64, title, id string) string {
	switch sort {
	case "created_desc":
		return base64.RawURLEncoding.EncodeToString([]byte("c:" + strconv.FormatInt(createdAt, 10) + ":" + id))
	case "title_asc":
		return base64.RawURLEncoding.EncodeToString([]byte("t:" + sqliteLower(title) + "\x00" + id))
	default:
		return base64.RawURLEncoding.EncodeToString([]byte("u:" + strconv.FormatInt(updatedAt, 10) + ":" + id))
	}
}

type galleryCursor struct {
	ts    int64
	title string
	id    string
}

func decodeGalleryCursor(sort, token string) (galleryCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return galleryCursor{}, badInput("pageToken")
	}
	text := string(raw)
	switch sort {
	case "created_desc":
		rest, ok := strings.CutPrefix(text, "c:")
		if !ok {
			return galleryCursor{}, badInput("pageToken")
		}
		tsPart, id, ok := strings.Cut(rest, ":")
		if !ok || id == "" {
			return galleryCursor{}, badInput("pageToken")
		}
		ts, err := strconv.ParseInt(tsPart, 10, 64)
		if err != nil {
			return galleryCursor{}, badInput("pageToken")
		}
		return galleryCursor{ts: ts, id: id}, nil
	case "title_asc":
		rest, ok := strings.CutPrefix(text, "t:")
		if !ok {
			return galleryCursor{}, badInput("pageToken")
		}
		title, id, ok := strings.Cut(rest, "\x00")
		if !ok || id == "" {
			return galleryCursor{}, badInput("pageToken")
		}
		return galleryCursor{title: title, id: id}, nil
	default:
		rest, ok := strings.CutPrefix(text, "u:")
		if !ok {
			return galleryCursor{}, badInput("pageToken")
		}
		tsPart, id, ok := strings.Cut(rest, ":")
		if !ok || id == "" {
			return galleryCursor{}, badInput("pageToken")
		}
		ts, err := strconv.ParseInt(tsPart, 10, 64)
		if err != nil {
			return galleryCursor{}, badInput("pageToken")
		}
		return galleryCursor{ts: ts, id: id}, nil
	}
}

func gallerySearchClause(projectID, query string) (string, []any) {
	terms := searchTerms(query)
	if len(terms) == 0 {
		return "", nil
	}
	long := []string{}
	short := []string{}
	for _, term := range terms {
		if len([]rune(term)) >= 3 {
			long = append(long, term)
		} else {
			short = append(short, term)
		}
	}
	matchWhere, matchArgs := buildSearchMatchFilters(long, short)
	subWhere := make([]string, 0, len(matchWhere)+2)
	subArgs := make([]any, 0, len(matchArgs)+1)
	if projectID != "" {
		subWhere = append(subWhere, "d.project_id = ?")
		subArgs = append(subArgs, projectID)
	}
	subWhere = append(subWhere, "d.kind = 'artifact'")
	subWhere = append(subWhere, matchWhere...)
	subArgs = append(subArgs, matchArgs...)

	projLikes := make([]string, 0, len(terms))
	projArgs := make([]any, 0, len(terms)+1)
	if projectID != "" {
		projLikes = append(projLikes, "p.id = ?")
		projArgs = append(projArgs, projectID)
	}
	for _, term := range terms {
		like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(term) + "%"
		projLikes = append(projLikes, `p.name LIKE ? ESCAPE '\'`)
		projArgs = append(projArgs, like)
	}

	clause := "(a.id IN (SELECT d.ref_id FROM search_text t JOIN search_docs d ON d.id = t.rowid WHERE " +
		strings.Join(subWhere, " AND ") +
		") OR a.project_id IN (SELECT p.id FROM projects p WHERE " +
		strings.Join(projLikes, " AND ") + "))"
	args := make([]any, 0, len(subArgs)+len(projArgs))
	args = append(args, subArgs...)
	args = append(args, projArgs...)
	return clause, args
}

func appendGalleryFilters(where []string, args []any, q GalleryArtifactsQuery, includeProject, includeKind, includeStatus bool) ([]string, []any) {
	if includeProject && q.ProjectID != "" {
		where = append(where, "a.project_id = ?")
		args = append(args, q.ProjectID)
	}
	if includeKind {
		if q.OnlyPrototypes {
			if q.Kind != "" && q.Kind != "whiteboard" {
				where = append(where, "1 = 0")
			} else {
				where = append(where, "a.kind = 'whiteboard' AND a.has_prototype = 1")
			}
		} else if q.Kind != "" {
			where = append(where, "a.kind = ?")
			args = append(args, q.Kind)
		}
	}
	if includeStatus {
		switch q.Status {
		case "needs_me":
			where = append(where, "(a.pending_actions > 0 OR a.open_comments > 0)")
		case "feedback_pending":
			where = append(where, "EXISTS (SELECT 1 FROM artifact_feedback f WHERE f.artifact_id = a.id AND f.done_at IS NULL AND f.withdrawn_at IS NULL)")
		}
	}
	projForSearch := ""
	if includeProject {
		projForSearch = q.ProjectID
	}
	if searchSQL, searchArgs := gallerySearchClause(projForSearch, q.Query); searchSQL != "" {
		where = append(where, searchSQL)
		args = append(args, searchArgs...)
	}
	return where, args
}

type projectMeta struct {
	name          string
	coordinatorID string
}

func lookupProjectsMeta(ctx context.Context, db queryer, ids []string) (map[string]projectMeta, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := records(ctx, db, "SELECT id, name, coordinator_id FROM projects WHERE id IN ("+inPlaceholders(len(ids))+")", args...)
	if err != nil {
		return nil, err
	}
	out := make(map[string]projectMeta, len(rows))
	for _, row := range rows {
		out[row.text("id")] = projectMeta{
			name:          row.text("name"),
			coordinatorID: row.text("coordinator_id"),
		}
	}
	return out, nil
}

func lookupPlanNodeTitles(ctx context.Context, db queryer, ids []string) (map[string]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := records(ctx, db, "SELECT id, title FROM plan_nodes WHERE id IN ("+inPlaceholders(len(ids))+")", args...)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, row := range rows {
		out[row.text("id")] = row.text("title")
	}
	return out, nil
}

func (reader Reader) ListGalleryArtifacts(ctx context.Context, q GalleryArtifactsQuery) (result GalleryArtifactPage, err error) {
	switch q.Kind {
	case "", "document", "whiteboard", "dashboard":
	default:
		return GalleryArtifactPage{}, badInput("kind")
	}
	switch q.Status {
	case "", "needs_me", "feedback_pending":
	default:
		return GalleryArtifactPage{}, badInput("status")
	}
	sort := q.Sort
	if sort == "" {
		sort = "updated_desc"
	}
	switch sort {
	case "updated_desc", "created_desc", "title_asc":
	default:
		return GalleryArtifactPage{}, badInput("sort")
	}
	limit, err := normalizePageSize(q.PageSize, defaultGalleryPageSize)
	if err != nil {
		return GalleryArtifactPage{}, err
	}
	var cursor galleryCursor
	hasCursor := q.PageToken != ""
	if hasCursor {
		cursor, err = decodeGalleryCursor(sort, q.PageToken)
		if err != nil {
			return GalleryArtifactPage{}, err
		}
	}

	err = reader.snapshot(ctx, func(tx *sql.Tx) error {
		where, args := appendGalleryFilters(nil, nil, q, true, true, true)
		var orderBy string
		switch sort {
		case "created_desc":
			if hasCursor {
				where = append(where, "(a.created_at < ? OR (a.created_at = ? AND a.id < ?))")
				args = append(args, cursor.ts, cursor.ts, cursor.id)
			}
			orderBy = "a.created_at DESC, a.id DESC"
		case "title_asc":
			if hasCursor {
				where = append(where, "(lower(a.title) > ? OR (lower(a.title) = ? AND a.id > ?))")
				args = append(args, cursor.title, cursor.title, cursor.id)
			}
			orderBy = "lower(a.title) ASC, a.id ASC"
		default:
			if hasCursor {
				where = append(where, "(a.updated_at < ? OR (a.updated_at = ? AND a.id < ?))")
				args = append(args, cursor.ts, cursor.ts, cursor.id)
			}
			orderBy = "a.updated_at DESC, a.id DESC"
		}

		sqlQuery := "SELECT " + galleryArtifactColumns + " FROM artifacts a"
		if len(where) > 0 {
			sqlQuery += " WHERE " + strings.Join(where, " AND ")
		}
		sqlQuery += " ORDER BY " + orderBy + " LIMIT ?"
		args = append(args, limit+1)

		rows, err := records(ctx, tx, sqlQuery, args...)
		if err != nil {
			return err
		}
		hasMore := len(rows) > limit
		if hasMore {
			rows = rows[:limit]
		}

		projectIDs := uniqueColumnValues(rows, "project_id")
		projects, err := lookupProjectsMeta(ctx, tx, projectIDs)
		if err != nil {
			return err
		}

		threadIDs := make([]string, 0, len(rows))
		threadSeen := make(map[string]struct{}, len(rows))
		nodeIDs := make([]string, 0, len(rows))
		nodeSeen := make(map[string]struct{}, len(rows))
		for _, row := range rows {
			author := row.text("updated_by")
			proj := projects[row.text("project_id")]
			if author != "" && author != "user" && author != proj.coordinatorID {
				if _, ok := threadSeen[author]; !ok {
					threadSeen[author] = struct{}{}
					threadIDs = append(threadIDs, author)
				}
			}
			if nodeID := row.text("node_id"); nodeID != "" {
				if _, ok := nodeSeen[nodeID]; !ok {
					nodeSeen[nodeID] = struct{}{}
					nodeIDs = append(nodeIDs, nodeID)
				}
			}
		}
		threadTitles, err := lookupThreadTitles(ctx, tx, "", threadIDs)
		if err != nil {
			return err
		}
		nodeTitles, err := lookupPlanNodeTitles(ctx, tx, nodeIDs)
		if err != nil {
			return err
		}

		items := make([]GalleryArtifactSummary, 0, len(rows))
		for _, row := range rows {
			summary, err := artifactSummary(row)
			if err != nil {
				return err
			}
			proj := projects[summary.ProjectID]
			isCoord := summary.UpdatedBy != "" && summary.UpdatedBy == proj.coordinatorID
			authorLabel := ""
			if !isCoord && summary.UpdatedBy != "user" {
				authorLabel = threadTitles[summary.UpdatedBy]
			}
			nodeTitle := ""
			if nodeID := row.text("node_id"); nodeID != "" {
				nodeTitle = nodeTitles[nodeID]
			}
			items = append(items, GalleryArtifactSummary{
				Artifact:             summary,
				ProjectName:          proj.name,
				UpdatedByLabel:       authorLabel,
				UpdatedByCoordinator: isCoord,
				HasPrototype:         row.integer("has_prototype") != 0,
				PendingActions:       row.integer("pending_actions"),
				PendingFeedback:      row.integer("pending_feedback"),
				NodeTitle:            nodeTitle,
			})
		}

		nextToken := ""
		if hasMore && len(items) > 0 {
			last := items[len(items)-1].Artifact
			nextToken = encodeGalleryCursor(sort, last.UpdatedAt, last.CreatedAt, last.Title, last.ID)
		}
		result = GalleryArtifactPage{
			Items:         items,
			NextPageToken: nextToken,
			HasMore:       hasMore,
		}

		if q.IncludeCounts {
			counts, err := computeGalleryCounts(ctx, tx, q)
			if err != nil {
				return err
			}
			result.Counts = &counts
		}
		return nil
	})
	return result, err
}

func computeGalleryCounts(ctx context.Context, db queryer, q GalleryArtifactsQuery) (GalleryCounts, error) {
	var counts GalleryCounts

	// Kind counts under project + query + status scope (independent of kind/only_prototypes).
	kindWhere, kindArgs := appendGalleryFilters(nil, nil, q, true, false, true)
	kindSQL := `SELECT
		COUNT(*) AS total,
		COALESCE(SUM(CASE WHEN a.kind = 'document' THEN 1 ELSE 0 END), 0) AS documents,
		COALESCE(SUM(CASE WHEN a.kind = 'whiteboard' THEN 1 ELSE 0 END), 0) AS whiteboards,
		COALESCE(SUM(CASE WHEN a.kind = 'dashboard' THEN 1 ELSE 0 END), 0) AS dashboards,
		COALESCE(SUM(CASE WHEN a.kind = 'whiteboard' AND a.has_prototype = 1 THEN 1 ELSE 0 END), 0) AS prototypes
		FROM artifacts a`
	if len(kindWhere) > 0 {
		kindSQL += " WHERE " + strings.Join(kindWhere, " AND ")
	}
	kindRows, err := records(ctx, db, kindSQL, kindArgs...)
	if err != nil {
		return GalleryCounts{}, err
	}
	if len(kindRows) > 0 {
		counts.Total = kindRows[0].integer("total")
		counts.Documents = kindRows[0].integer("documents")
		counts.Whiteboards = kindRows[0].integer("whiteboards")
		counts.Dashboards = kindRows[0].integer("dashboards")
		counts.Prototypes = kindRows[0].integer("prototypes")
	}

	// Status counts under project + kind + query scope (independent of status).
	statusWhere, statusArgs := appendGalleryFilters(nil, nil, q, true, true, false)
	statusSQL := `SELECT
		COALESCE(SUM(CASE WHEN a.pending_actions > 0 OR a.open_comments > 0 THEN 1 ELSE 0 END), 0) AS needs_me,
		COALESCE(SUM(CASE WHEN EXISTS (SELECT 1 FROM artifact_feedback f WHERE f.artifact_id = a.id AND f.done_at IS NULL AND f.withdrawn_at IS NULL) THEN 1 ELSE 0 END), 0) AS feedback_pending
		FROM artifacts a`
	if len(statusWhere) > 0 {
		statusSQL += " WHERE " + strings.Join(statusWhere, " AND ")
	}
	statusRows, err := records(ctx, db, statusSQL, statusArgs...)
	if err != nil {
		return GalleryCounts{}, err
	}
	if len(statusRows) > 0 {
		counts.NeedsMe = statusRows[0].integer("needs_me")
		counts.FeedbackPending = statusRows[0].integer("feedback_pending")
	}

	// Project counts under query scope (independent of project, kind, status).
	projWhere, projArgs := appendGalleryFilters(nil, nil, q, false, false, false)
	projSQL := `SELECT a.project_id, p.name AS project_name, COUNT(*) AS cnt
		FROM artifacts a
		JOIN projects p ON p.id = a.project_id`
	if len(projWhere) > 0 {
		projSQL += " WHERE " + strings.Join(projWhere, " AND ")
	}
	projSQL += " GROUP BY a.project_id, p.name ORDER BY cnt DESC, p.updated_at DESC, a.project_id ASC"
	projRows, err := records(ctx, db, projSQL, projArgs...)
	if err != nil {
		return GalleryCounts{}, err
	}
	counts.Projects = make([]GalleryProjectCount, 0, len(projRows))
	for _, row := range projRows {
		counts.Projects = append(counts.Projects, GalleryProjectCount{
			ProjectID:   row.text("project_id"),
			ProjectName: row.text("project_name"),
			Count:       row.integer("cnt"),
		})
	}
	return counts, nil
}
