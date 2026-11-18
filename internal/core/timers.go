package core

import (
	"context"
	"database/sql"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/eventstore"
	"github.com/daidr/Ariadne/internal/projectsettings"
)

type DueWait struct {
	ConversationID, WaitID string
	Until                  int64
}
type DueAside struct {
	ThreadID string
	Until    int64
}
type RoutineProject struct {
	ID          string
	SettingsSeq int64
	Routines    []projectsettings.Routine
}
type TimerSnapshot struct {
	Seq      int64
	Waits    []DueWait
	Asides   []DueAside
	Projects []RoutineProject
}

// ReadTimers reads scheduling metadata from one snapshot, without reading chat
// payloads or retaining one in-memory entry for every project or routine.
func (reader Reader) ReadTimers(ctx context.Context) (result TimerSnapshot, err error) {
	err = reader.snapshot(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, "SELECT coalesce(max(seq),0) FROM events").Scan(&result.Seq); err != nil {
			return err
		}
		waits, err := records(ctx, tx, "SELECT id,wait_id,wait_until FROM conversations WHERE wait_id IS NOT NULL ORDER BY wait_until")
		if err != nil {
			return err
		}
		for _, row := range waits {
			result.Waits = append(result.Waits, DueWait{row.text("id"), row.text("wait_id"), row.integer("wait_until")})
		}
		asides, err := records(ctx, tx, "SELECT id,aside_until FROM threads WHERE aside_until IS NOT NULL AND aside_due = 0 ORDER BY aside_until")
		if err != nil {
			return err
		}
		for _, row := range asides {
			result.Asides = append(result.Asides, DueAside{row.text("id"), row.integer("aside_until")})
		}
		projects, err := records(ctx, tx, `SELECT id,settings,coalesce((SELECT max(seq) FROM events e WHERE e.project_id=p.id AND type IN ('project.created','project.settings.set')),0) AS settings_seq FROM projects p ORDER BY created_at`)
		if err != nil {
			return err
		}
		for _, row := range projects {
			settings, err := projectsettings.Stored(row.text("settings"))
			if err != nil {
				return err
			}
			if len(settings.Routines) > 0 {
				result.Projects = append(result.Projects, RoutineProject{row.text("id"), row.integer("settings_seq"), settings.Routines})
			}
		}
		return nil
	})
	return
}

// RunRoutineIfCurrent checks the schedule again under the domain write lock.
// A concurrent edit/deletion or another timer cannot produce a stale/duplicate run.
func (repo *Repository) RunRoutineIfCurrent(ctx context.Context, projectID, routineID string, dueAt, settingsSeq int64) (ran bool, err error) {
	err = repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		rows, err := records(ctx, tx.SQL(), `SELECT settings,coalesce((SELECT max(seq) FROM events e WHERE e.project_id=p.id AND type IN ('project.created','project.settings.set')),0) AS settings_seq FROM projects p WHERE id=?`, projectID)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return domainerr.NotFound(projectID)
		}
		if rows[0].integer("settings_seq") != settingsSeq {
			return nil
		}
		settings, err := projectsettings.Stored(rows[0].text("settings"))
		if err != nil {
			return err
		}
		var routine *projectsettings.Routine
		for i := range settings.Routines {
			if settings.Routines[i].ID == routineID && settings.Routines[i].Enabled {
				routine = &settings.Routines[i]
				break
			}
		}
		if routine == nil {
			return nil
		}
		rows, err = records(ctx, tx.SQL(), `SELECT 1 AS one FROM events WHERE project_id=? AND type='routine.ran' AND json_extract(data,'$.routineId')=? AND json_extract(data,'$.dueAt')=? LIMIT 1`, projectID, routineID, dueAt)
		if err != nil || len(rows) > 0 {
			return err
		}
		if err := appendChanges(tx, projectID, []change{{"routine.ran", object{"routineId": routineID, "prompt": routine.Prompt, "dueAt": dueAt}}}); err != nil {
			return err
		}
		ran = true
		return nil
	})
	return
}
func (repo *Repository) AsideDueIfCurrent(ctx context.Context, threadID string, until int64) error {
	_, err := repo.mutateThread(ctx, threadID, func(_ *eventstore.Transaction, current ThreadView) ([]change, error) {
		if current.Aside == nil || current.Aside.Due || current.Aside.Until != until {
			return nil, nil
		}
		return []change{{"thread.aside_due", object{"threadId": threadID}}}, nil
	})
	return err
}
