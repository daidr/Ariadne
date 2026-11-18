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
	_, err := repo.mutateThread(ctx, id, func(_ *eventstore.Transaction, current ThreadView) ([]change, error) {
		known := current.Workspace
		if known != nil && !known.Removed && known.Label == label && encoded(known.Branch) == encoded(branch) && encoded(known.Base) == encoded(base) {
			return nil, nil
		}
		return []change{{"workspace.opened", object{"threadId": id, "label": label, "branch": branch, "base": base}}}, nil
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
