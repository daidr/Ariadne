package core

import (
	"context"
	"database/sql"
	"slices"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/eventstore"
	"github.com/daidr/Ariadne/internal/projectsettings"
)

func (repo *Repository) RecordWorkspace(ctx context.Context, id, label string, branch, base *string) error {
	return repo.RecordWorkspaceSource(ctx, id, "", label, branch, base)
}

func (repo *Repository) RecordWorkspaceSource(ctx context.Context, id, sourceID, label string, branch, base *string) error {
	_, err := repo.mutateThread(ctx, id, func(_ *eventstore.Transaction, current ThreadView) ([]change, error) {
		known := current.Workspace
		if known != nil && !known.Removed && (sourceID == "" || known.SourceID == sourceID) && known.Label == label && encoded(known.Branch) == encoded(branch) && encoded(known.Base) == encoded(base) {
			return nil, nil
		}
		data := object{"threadId": id, "label": label, "branch": branch, "base": base}
		if sourceID != "" {
			data["sourceId"] = sourceID
		}
		return []change{{"workspace.opened", data}}, nil
	})
	return err
}

func (reader Reader) SecondaryWorkspace(ctx context.Context, thread, source string) (*WorkspaceView, error) {
	return secondaryWorkspace(ctx, reader.DB, thread, source)
}

// SecondaryWorkspaces batch-loads the latest secondary workspace state for the
// given thread IDs under a project and source in a single query.
func (reader Reader) SecondaryWorkspaces(ctx context.Context, projectID, source string, threadIDs []string) (map[string]*WorkspaceView, error) {
	result := map[string]*WorkspaceView{}
	if len(threadIDs) == 0 {
		return result, nil
	}
	wanted := make(map[string]bool, len(threadIDs))
	for _, id := range threadIDs {
		if id != "" {
			wanted[id] = true
		}
	}
	if len(wanted) == 0 {
		return result, nil
	}
	rows, err := records(ctx, reader.DB, `SELECT type, data FROM events WHERE project_id = ?
	 AND type IN ('workspace.source.opened', 'workspace.source.removed')
	 AND json_extract(data, '$.sourceId') = ? ORDER BY seq DESC`, projectID, source)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		value, err := row.json("data", nil)
		if err != nil {
			return nil, err
		}
		data := asObject(value)
		tid := data.str("threadId")
		if !wanted[tid] {
			continue
		}
		if _, exists := result[tid]; exists {
			continue
		}
		result[tid] = &WorkspaceView{
			SourceID: source,
			Label:    data.str("label"),
			Branch:   data["branch"],
			Base:     data["base"],
			Removed:  row.text("type") == "workspace.source.removed",
		}
		if len(result) == len(wanted) {
			break
		}
	}
	return result, nil
}

func secondaryWorkspace(ctx context.Context, db queryer, thread, source string) (*WorkspaceView, error) {
	rows, err := records(ctx, db, `SELECT type, data FROM events WHERE type IN ('workspace.source.opened', 'workspace.source.removed')
	 AND json_extract(data, '$.threadId') = ? AND json_extract(data, '$.sourceId') = ? ORDER BY seq DESC LIMIT 1`, thread, source)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	value, err := rows[0].json("data", nil)
	data := asObject(value)
	return &WorkspaceView{SourceID: source, Label: data.str("label"), Branch: data["branch"], Base: data["base"], Removed: rows[0].text("type") == "workspace.source.removed"}, err
}

func (repo *Repository) RecordSecondaryWorkspace(ctx context.Context, thread, source, label string, branch, base *string) error {
	_, err := repo.mutateThread(ctx, thread, func(tx *eventstore.Transaction, _ ThreadView) ([]change, error) {
		known, err := secondaryWorkspace(ctx, tx.SQL(), thread, source)
		if err != nil || known != nil && !known.Removed && known.Label == label && encoded(known.Branch) == encoded(branch) && encoded(known.Base) == encoded(base) {
			return nil, err
		}
		return []change{{"workspace.source.opened", object{"threadId": thread, "sourceId": source, "label": label, "branch": branch, "base": base}}}, nil
	})
	return err
}

func (repo *Repository) RecordSecondaryWorktreeRemoved(ctx context.Context, thread, source string) error {
	_, err := repo.mutateThread(ctx, thread, func(tx *eventstore.Transaction, _ ThreadView) ([]change, error) {
		known, err := secondaryWorkspace(ctx, tx.SQL(), thread, source)
		if err != nil || known == nil || known.Removed {
			return nil, err
		}
		return []change{{"workspace.source.removed", object{"threadId": thread, "sourceId": source, "label": known.Label, "branch": known.Branch, "base": known.Base}}}, nil
	})
	return err
}
func (repo *Repository) RecordWorktreeRemoved(ctx context.Context, id, reason string) error {
	_, err := repo.mutateThread(ctx, id, func(_ *eventstore.Transaction, current ThreadView) ([]change, error) {
		known := current.Workspace
		if known == nil || known.Removed && !(known.Parked && reason != "idle") {
			return nil, nil
		}
		return []change{{"workspace.removed", object{"threadId": id, "reason": reason}}}, nil
	})
	return err
}
func (repo *Repository) RecordMerge(ctx context.Context, id string, merge map[string]any) (ThreadView, error) {
	return repo.mutateThread(ctx, id, func(_ *eventstore.Transaction, current ThreadView) ([]change, error) {
		data := except(merge, "")
		data["threadId"] = id
		return []change{{"branch.merged", data}}, nil
	})
}

// RecordUserMerge remembers the user's merge method and its result atomically,
// matching the existing transaction around these two events.
func (repo *Repository) RecordUserMerge(ctx context.Context, id string, merge map[string]any, method string) (ThreadView, error) {
	if method != "squash" && method != "merge" {
		return ThreadView{}, badInput("autopilot.method")
	}
	return repo.mutateThread(ctx, id, func(tx *eventstore.Transaction, current ThreadView) ([]change, error) {
		detail, err := projectDetail(ctx, tx.SQL(), current.ProjectID, repo.Reader.now())
		if err != nil {
			return nil, err
		}
		changes := []change{}
		if detail.Project.Settings.Autopilot.Method != method {
			settings := detail.Project.Settings
			settings.Autopilot.Method = method
			changes = append(changes, change{"project.settings.set", object{"settings": settings}})
		}
		data := except(merge, "")
		data["threadId"] = id
		return append(changes, change{"branch.merged", data}), nil
	})
}
func (repo *Repository) RecordMergeUndone(ctx context.Context, id string) (ThreadView, error) {
	return repo.mutateThread(ctx, id, func(_ *eventstore.Transaction, current ThreadView) ([]change, error) {
		merge := asObject(current.Merged)
		if merge == nil {
			return nil, domainerr.New("error.undoMerge.none", nil)
		}
		data := pick(merge, "branch", "base", "sha", "head")
		data["threadId"] = id
		return []change{{"branch.merge_undone", data}}, nil
	})
}
func (repo *Repository) RecordMergeConflict(ctx context.Context, id string, conflict map[string]any) (ThreadView, error) {
	return repo.mutateThread(ctx, id, func(_ *eventstore.Transaction, current ThreadView) ([]change, error) {
		data := except(conflict, "")
		data["threadId"] = id
		return []change{{"branch.conflicted", data}}, nil
	})
}
func (repo *Repository) SyncPullRequest(ctx context.Context, id string, pullRequest map[string]any, auto map[string]any) error {
	return repo.syncPullRequest(ctx, id, nil, pullRequest, auto)
}

func (repo *Repository) SyncPullRequestIfCurrent(ctx context.Context, id string, previous, next map[string]any) error {
	return repo.syncPullRequest(ctx, id, previous, next, nil)
}

const PullRequestMergeApprovalPrefix = "merge-pr:"

type MergeRequestTarget struct {
	ThreadID string
	Title    string
	Number   int
	URL      string
	HeadSHA  string
}

// MergeRequestTargets returns the PRs the user could have authorized when they
// wrote the message. Later pushes and PRs cannot inherit that authorization.
func (reader Reader) MergeRequestTargets(ctx context.Context, projectID, threadID string, message MessageView) ([]MergeRequestTarget, error) {
	rows, err := records(ctx, reader.DB, `WITH request AS (
	 SELECT t.seq FROM transcript t JOIN messages m ON m.id = t.ref
	 WHERE t.conversation_id = ? AND t.kind = 'message' AND m.id = ? AND m.project_id = ? AND m.author = 'user'
	 AND NOT EXISTS (SELECT 1 FROM transcript n JOIN messages u ON u.id = n.ref
	   WHERE n.conversation_id = t.conversation_id AND n.kind = 'message' AND n.seq > t.seq AND u.author = 'user')
	), pull_requests AS (
	 SELECT json_extract(data, '$.threadId') AS thread_id, max(seq) AS seq FROM events
	 WHERE project_id = ? AND type = 'pull_request.synced' AND seq < (SELECT seq FROM request)
	 AND (? = '' OR json_extract(data, '$.threadId') = ?) GROUP BY thread_id
	)
	SELECT t.id, (SELECT json_extract(h.data, '$.title') FROM events h
	 WHERE h.project_id = e.project_id AND h.type IN ('thread.created', 'thread.renamed')
	 AND json_extract(h.data, '$.threadId') = t.id AND h.seq < (SELECT seq FROM request)
	 ORDER BY h.seq DESC LIMIT 1) AS title, json_extract(e.data, '$.number') AS number,
	 json_extract(e.data, '$.url') AS url, json_extract(e.data, '$.headSha') AS head
	 FROM pull_requests p JOIN events e ON e.seq = p.seq JOIN threads t ON t.id = p.thread_id
	 WHERE json_extract(e.data, '$.state') = 'open' AND t.review_of IS NULL`, message.ConversationID, message.ID, projectID, projectID, threadID, threadID)
	if err != nil {
		return nil, err
	}
	targets := make([]MergeRequestTarget, 0, len(rows))
	for _, row := range rows {
		targets = append(targets, MergeRequestTarget{ThreadID: row.text("id"), Title: row.text("title"),
			Number: int(row.integer("number")), URL: row.text("url"), HeadSHA: row.text("head")})
	}
	return targets, nil
}

func (repo *Repository) syncPullRequest(ctx context.Context, id string, previous, pullRequest, auto map[string]any) error {
	_, err := repo.mutateThread(ctx, id, func(tx *eventstore.Transaction, current ThreadView) ([]change, error) {
		before := asObject(current.PullRequest)
		if previous != nil && encoded(before) != encoded(previous) {
			return nil, nil
		}
		if encoded(before) == encoded(pullRequest) {
			return nil, nil
		}
		data := except(pullRequest, "")
		data["threadId"] = id
		changes := []change{{"pull_request.synced", data}}
		if after := object(pullRequest); before.str("headSha") != after.str("headSha") ||
			before.str("url") != after.str("url") || after.str("state") != "open" {
			rows, err := records(ctx, tx.SQL(), `SELECT id FROM attentions WHERE conversation_id = ? AND closed = 0 AND purpose GLOB ?`, id, PullRequestMergeApprovalPrefix+"*")
			if err != nil {
				return nil, err
			}
			for _, row := range rows {
				changes = append(changes, change{"attention.withdrawn", object{"attentionId": row.text("id"), "reason": "Pull request changed"}})
			}
		}
		after := object(pullRequest)
		failedBefore := before != nil && before["headSha"] == after["headSha"] && before.obj("checks").str("state") == "failure"
		if after.obj("checks").str("state") == "failure" && !failedBefore {
			rows, err := records(ctx, tx.SQL(), "SELECT check_failures FROM threads WHERE id = ?", id)
			if err != nil {
				return nil, err
			}
			attempt := int64(1)
			if len(rows) > 0 {
				attempt += rows[0].integer("check_failures")
			}
			changes = append(changes, change{"checks.failed", object{"threadId": id, "number": after["number"], "headSha": after["headSha"], "failed": after.obj("checks")["failed"], "attempt": attempt}})
		}
		if after.str("state") != "open" && before.str("state") == "open" {
			closed := object{"threadId": id, "number": after["number"], "merged": after.str("state") == "merged"}
			if auto != nil && after.str("state") == "merged" {
				closed["auto"] = auto
			}
			changes = append(changes, change{"pull_request.closed", closed})
		}
		return changes, nil
	})
	return err
}

type OpenPullRequest struct {
	ThreadID  string
	ProjectID string
	Request   map[string]any
}

func (reader Reader) OpenPullRequests(ctx context.Context, after string, limit int) ([]OpenPullRequest, error) {
	rows, err := records(ctx, reader.DB, `SELECT id, project_id, pull_request FROM threads
		WHERE id > ? AND pull_request IS NOT NULL AND json_extract(pull_request, '$.state') = 'open'
		ORDER BY id LIMIT ?`, after, min(max(limit, 1), 128))
	if err != nil {
		return nil, err
	}
	result := make([]OpenPullRequest, 0, len(rows))
	for _, row := range rows {
		raw, err := row.json("pull_request", nil)
		if err != nil {
			return nil, err
		}
		result = append(result, OpenPullRequest{ThreadID: row.text("id"), ProjectID: row.text("project_id"), Request: asObject(raw)})
	}
	return result, nil
}

type TurnFile struct {
	Path         string  `json:"path"`
	PreviousPath *string `json:"previousPath"`
	Status       string  `json:"status"`
	Added        *int64  `json:"added"`
	Removed      *int64  `json:"removed"`
}
type ChangeTotals struct {
	Files   int64 `json:"files"`
	Added   int64 `json:"added"`
	Removed int64 `json:"removed"`
}

func changeCount(value any) (int64, bool) {
	switch v := value.(type) {
	case int64:
		return v, true
	case int:
		return int64(v), true
	case float64:
		return int64(v), float64(int64(v)) == v
	default:
		return 0, false
	}
}

// SameChangeTotals reports whether a stored ThreadView.Changes JSON value
// already matches the given ChangeTotals.
func SameChangeTotals(stored any, totals ChangeTotals) bool {
	if stored == nil {
		return false
	}
	switch v := stored.(type) {
	case ChangeTotals:
		return v == totals
	case *ChangeTotals:
		return v != nil && *v == totals
	}
	m := asObject(stored)
	if m == nil {
		return false
	}
	files, okF := changeCount(m["files"])
	added, okA := changeCount(m["added"])
	removed, okR := changeCount(m["removed"])
	return okF && okA && okR && files == totals.Files && added == totals.Added && removed == totals.Removed
}

// SyncBranchChanges reconciles the cached thread.changes totals with a freshly
// computed branch diff when they diverge.
func (repo *Repository) SyncBranchChanges(ctx context.Context, id string, totals ChangeTotals) error {
	_, err := repo.mutateThread(ctx, id, func(_ *eventstore.Transaction, current ThreadView) ([]change, error) {
		if truthy(current.ReviewOf) {
			return nil, nil
		}
		if current.Changes == nil && totals.Files == 0 {
			return nil, nil
		}
		if SameChangeTotals(current.Changes, totals) {
			return nil, nil
		}
		return []change{{"branch.changes_synced", object{"threadId": id, "changes": totals}}}, nil
	})
	return err
}

func (repo *Repository) RecordTurnChanges(ctx context.Context, id, turnID, from, to string, files []TurnFile, branch *ChangeTotals) error {
	if len(files) == 0 {
		return nil
	}
	return repo.withTurn(ctx, id, turnID, func(tx *eventstore.Transaction, projectID string) error {
		current, err := conversation(ctx, tx.SQL(), id)
		if err != nil {
			return err
		}
		total := ChangeTotals{Files: int64(len(files))}
		for _, file := range files {
			if file.Added != nil {
				total.Added += *file.Added
			}
			if file.Removed != nil {
				total.Removed += *file.Removed
			}
		}
		data := object{"conversationId": id, "turnId": turnID, "from": from, "to": to, "files": files[:min(len(files), 200)], "total": total}
		if current.Turn != nil && current.Turn.StartedAt != 0 {
			data["startedAt"] = current.Turn.StartedAt
		}
		if branch != nil {
			data["branch"] = branch
		}
		return appendChanges(tx, projectID, []change{{"turn.changed", data}})
	})
}
func canNote(thread ThreadView, project ProjectView, kind string) bool {
	if truthy(thread.ReviewOf) || slices.Contains(project.Settings.Tools, "commands") {
		return false
	}
	allowed := project.Settings.Workspace.Auto.Setup
	if kind == "check" {
		allowed = project.Settings.Workspace.Auto.Check
	}
	detected := asObject(project.Detected).obj(kind)
	return allowed && (detected == nil || detected.str("threadId") == thread.ID)
}
func (reader Reader) canNote(ctx context.Context, id, kind string) (allowed bool, err error) {
	err = reader.snapshot(ctx, func(tx *sql.Tx) error {
		thread, err := readThread(ctx, tx, id, reader.now())
		if err != nil {
			return err
		}
		if truthy(thread.ReviewOf) {
			return nil
		}
		detail, err := projectDetail(ctx, tx, thread.ProjectID, reader.now())
		if err != nil {
			return err
		}
		allowed = canNote(thread, detail.Project, kind)
		return nil
	})
	return
}
func (reader Reader) CanNoteSetup(ctx context.Context, id string) (bool, error) {
	return reader.canNote(ctx, id, "setup")
}
func (reader Reader) CanNoteCheck(ctx context.Context, id string) (bool, error) {
	return reader.canNote(ctx, id, "check")
}
func (repo *Repository) noteCommand(ctx context.Context, id, kind, command string) (result ProjectView, err error) {
	err = repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		thread, err := readThread(ctx, tx.SQL(), id, repo.Reader.now())
		if err != nil {
			return err
		}
		detail, err := projectDetail(ctx, tx.SQL(), thread.ProjectID, repo.Reader.now())
		if err != nil {
			return err
		}
		if !canNote(thread, detail.Project, kind) {
			return badInput(kind)
		}
		noted := projectsettings.Trim(command)
		limit := projectsettings.MaxSetup
		if kind == "check" {
			limit = projectsettings.MaxCheck
		}
		if utf16Length(noted) > limit {
			return badInput("command")
		}
		detected := asObject(detail.Project.Detected).obj(kind)
		if detected != nil && detected.str("command") == noted {
			result = detail.Project
			return nil
		}
		if err := appendChanges(tx, thread.ProjectID, []change{{"project." + kind + ".noted", object{"command": noted, "threadId": id}}}); err != nil {
			return err
		}
		detail, err = projectDetail(ctx, tx.SQL(), thread.ProjectID, repo.Reader.now())
		result = detail.Project
		return err
	})
	return
}
func (repo *Repository) NoteSetup(ctx context.Context, id, command string) (ProjectView, error) {
	return repo.noteCommand(ctx, id, "setup", command)
}
func (repo *Repository) NoteCheck(ctx context.Context, id, command string) (ProjectView, error) {
	return repo.noteCommand(ctx, id, "check", command)
}
