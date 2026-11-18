package core

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/projectsettings"
)

type ProjectView struct {
	ID              string                   `json:"id"`
	Name            string                   `json:"name"`
	Unnamed         bool                     `json:"unnamed"`
	CoordinatorID   string                   `json:"coordinatorId"`
	CreatedAt       int64                    `json:"createdAt"`
	UpdatedAt       int64                    `json:"updatedAt"`
	NeedsMe         int                      `json:"needsMe"`
	Source          any                      `json:"source"`
	References      any                      `json:"references"`
	Slots           any                      `json:"slots"`
	MCP             any                      `json:"mcp"`
	Settings        projectsettings.Settings `json:"settings"`
	Detected        any                      `json:"detected"`
	AutopilotPaused bool                     `json:"autopilotPaused"`
	Usage           map[string]any           `json:"usage"`
}

type WorkspaceView struct {
	Label   string `json:"label"`
	Branch  any    `json:"branch"`
	Base    any    `json:"base"`
	Removed bool   `json:"removed"`
	Parked  bool   `json:"parked"`
}
type AsideView struct {
	Until  int64  `json:"until"`
	Reason string `json:"reason"`
	By     string `json:"by"`
	Due    bool   `json:"due"`
}
type StoppedView struct {
	Reason string `json:"reason"`
	By     string `json:"by"`
	At     int64  `json:"at"`
}
type ReviewView struct {
	ReviewerID string `json:"reviewerId"`
	Rounds     int64  `json:"rounds"`
	Reviewing  bool   `json:"reviewing"`
}
type Progress struct {
	Done  int `json:"done"`
	Total int `json:"total"`
}
type ThreadView struct {
	ID              string         `json:"id"`
	ProjectID       string         `json:"projectId"`
	Title           string         `json:"title"`
	NodeID          any            `json:"nodeId"`
	Stage           string         `json:"stage"`
	LastReport      any            `json:"lastReport"`
	LastSummary     any            `json:"lastSummary"`
	LastHeadline    any            `json:"lastHeadline"`
	LastArtifacts   any            `json:"lastArtifacts"`
	Workspace       *WorkspaceView `json:"workspace"`
	Merged          any            `json:"merged"`
	Archived        bool           `json:"archived"`
	Changes         any            `json:"changes"`
	PullRequest     any            `json:"pullRequest"`
	Aside           *AsideView     `json:"aside"`
	Stopped         *StoppedView   `json:"stopped"`
	References      any            `json:"references"`
	EndedReferences any            `json:"endedReferences"`
	Model           any            `json:"model"`
	BranchSlug      any            `json:"branchSlug"`
	Usage           map[string]any `json:"usage"`
	Progress        *Progress      `json:"progress"`
	Doing           any            `json:"doing"`
	CreatedAt       int64          `json:"createdAt"`
	UpdatedAt       int64          `json:"updatedAt"`
	ActiveAt        int64          `json:"activeAt"`
	ReviewOf        any            `json:"reviewOf"`
	Review          *ReviewView    `json:"review"`
	Auto            bool           `json:"auto"`
	AutopilotHeld   any            `json:"autopilotHeld"`
}
type AttentionView struct {
	ID             string   `json:"id"`
	ProjectID      string   `json:"projectId"`
	ThreadID       any      `json:"threadId"`
	JoinedBy       []string `json:"joinedBy"`
	Block          any      `json:"block"`
	MessageID      any      `json:"messageId"`
	ConversationID any      `json:"conversationId"`
	CreatedAt      int64    `json:"createdAt"`
}
type ProjectDetail struct {
	Project ProjectView  `json:"project"`
	Threads []ThreadView `json:"threads"`
	NeedsMe []object     `json:"needsMe"`
}

// Reader queries a consistent SQLite snapshot across the related views. It owns
// no caches, so deleting projects also releases their data from the read path.
type Reader struct {
	DB  *sql.DB
	Now func() time.Time
}

func (reader Reader) now() int64 {
	if reader.Now != nil {
		return reader.Now().UnixMilli()
	}
	return time.Now().UnixMilli()
}

func (reader Reader) snapshot(ctx context.Context, read func(*sql.Tx) error) error {
	tx, err := reader.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	err = read(tx)
	if err == nil {
		err = tx.Commit()
	}
	if errors.Is(err, sql.ErrTxDone) && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

const threadSelect = `SELECT t.*, c.turn_id, c.pending_seq, c.last_outcome, c.last_turn_at,
 c.wait_id, c.input_tokens, c.output_tokens, c.cached_tokens, c.cache_write_tokens,
 c.reasoning_tokens, c.cost_usd, c.cost_cny, c.turn_started_at,
 td.items AS todo_items, td.updated_at AS todo_at,
 (SELECT max(m.created_at) FROM messages m WHERE m.conversation_id = t.id) AS message_at,
 (SELECT e.at FROM events e WHERE e.seq = (SELECT tr.seq FROM transcript tr
 WHERE tr.conversation_id = t.id ORDER BY tr.id DESC LIMIT 1)) AS transcript_at,
 (SELECT json_group_array(json_object('id', ar.id, 'kind', ar.kind, 'title', ar.title))
 FROM (SELECT ar.id, ar.kind, ar.title FROM json_each(t.last_artifacts) j
 JOIN artifacts ar ON ar.id = j.value AND ar.project_id = t.project_id ORDER BY j.key) ar) AS report_artifacts,
 (SELECT count(*) FROM attentions a WHERE a.closed = 0 AND (a.thread_id = t.id OR EXISTS
 (SELECT 1 FROM attention_waiters w WHERE w.attention_id = a.id AND w.thread_id = t.id))) AS open_attentions
 FROM threads t LEFT JOIN conversations c ON c.id = t.id LEFT JOIN todos td ON td.conversation_id = t.id`

func toThread(row record, now int64) (ThreadView, error) {
	view := ThreadView{
		ID: row.text("id"), ProjectID: row.text("project_id"), Title: row.text("title"), NodeID: row["node_id"], LastReport: row["last_report"], LastSummary: row["last_summary"], LastHeadline: row["last_headline"], Model: row["model"], BranchSlug: row["branch_slug"], Usage: usage(row), CreatedAt: row.integer("created_at"), UpdatedAt: row.integer("updated_at"), ReviewOf: row["review_of"], Auto: row.flag("auto"), AutopilotHeld: row["autopilot_held"],
	}
	for _, field := range []struct {
		key      string
		target   *any
		fallback any
	}{
		{"merged", &view.Merged, nil}, {"pull_request", &view.PullRequest, nil}, {"report_artifacts", &view.LastArtifacts, []any{}},
		{"changes", &view.Changes, nil}, {"reference_sources", &view.References, []any{}}, {"reference_ended", &view.EndedReferences, []any{}},
	} {
		value, err := row.json(field.key, field.fallback)
		if err != nil {
			return ThreadView{}, err
		}
		*field.target = value
	}
	merged, _ := view.Merged.(map[string]any)
	pullRequest, _ := view.PullRequest.(map[string]any)
	mergedAt, _ := merged["at"].(float64)
	unreported := func(outcome string) bool {
		return !row.present("turn_id") && row.text("last_outcome") == outcome && row.present("last_turn_at") && row.integer("reported_at") < row.integer("last_turn_at")
	}
	view.Stage = ThreadStage(StageFacts{
		OpenAttentions: int(row.integer("open_attentions")), Started: row.flag("started"), LastReport: row.text("last_report"),
		TurnRunning: row.present("turn_id"), TurnPending: !row.present("turn_id") && row.integer("pending_seq") > 0,
		TurnFailed: !row.present("turn_id") && row.text("last_outcome") == "failed", TurnCancelled: unreported("cancelled"), Unreported: unreported("completed"), Waiting: row.present("wait_id"),
		ChecksGaveUp: row.integer("check_failures") >= 3, Stopped: row.present("stopped_reason"),
		Landed:   merged != nil && (int64(mergedAt) >= row.integer("reported_at") || merged["head"] == row["reported_head"]) || pullRequest["state"] == "merged",
		Reviewed: row.present("reviewed_at") && row.integer("reviewed_at") >= row.integer("reported_at"), UnderReview: row.flag("reviewing"), Reviewer: row.present("review_of"),
	})
	view.ActiveAt = max(row.integer("updated_at"), row.integer("message_at"), row.integer("transcript_at"), row.integer("todo_at"), row.integer("turn_started_at"))
	view.Archived = view.Stage == "done" && (row.present("landed_at") && row.integer("landed_at") >= row.integer("reported_at") && row.integer("user_at") <= row.integer("landed_at") && now-row.integer("landed_at") >= int64(24*time.Hour/time.Millisecond) || now-view.ActiveAt >= int64(7*24*time.Hour/time.Millisecond))
	if row.present("workspace_label") {
		view.Workspace = &WorkspaceView{Label: row.text("workspace_label"), Branch: row["workspace_branch"], Base: row["workspace_base"], Removed: row.flag("workspace_removed"), Parked: row.flag("workspace_parked")}
	}
	if row.present("aside_until") {
		view.Aside = &AsideView{Until: row.integer("aside_until"), Reason: row.text("aside_reason"), By: row.text("aside_by"), Due: row.flag("aside_due")}
	}
	if row.present("stopped_reason") {
		by := row.text("stopped_by")
		if !row.present("stopped_by") {
			by = "user"
		}
		view.Stopped = &StoppedView{Reason: row.text("stopped_reason"), By: by, At: row.integer("stopped_at")}
	}
	if row.text("reviewer_id") != "" {
		view.Review = &ReviewView{ReviewerID: row.text("reviewer_id"), Rounds: row.integer("review_rounds"), Reviewing: row.flag("reviewing")}
	}
	todos, err := row.json("todo_items", []any{})
	if err != nil {
		return ThreadView{}, err
	}
	if items, ok := todos.([]any); ok && len(items) > 0 {
		view.Progress = &Progress{Total: len(items)}
		for _, item := range items {
			todo, _ := item.(map[string]any)
			if todo["status"] == "done" {
				view.Progress.Done++
			}
			if todo["status"] == "doing" && view.Doing == nil {
				view.Doing = todo["label"]
			}
		}
	}
	return view, nil
}

func listThreads(ctx context.Context, db queryer, projectID string, now int64) ([]ThreadView, error) {
	rows, err := records(ctx, db, threadSelect+" WHERE t.project_id = ? ORDER BY t.id", projectID)
	if err != nil {
		return nil, err
	}
	views := make([]ThreadView, 0, len(rows))
	for _, row := range rows {
		view, err := toThread(row, now)
		if err != nil {
			return nil, err
		}
		views = append(views, view)
	}
	return views, nil
}
func (reader Reader) ListThreads(ctx context.Context, projectID string) ([]ThreadView, error) {
	return listThreads(ctx, reader.DB, projectID, reader.now())
}
func (reader Reader) GetThread(ctx context.Context, threadID string) (ThreadView, error) {
	rows, err := records(ctx, reader.DB, threadSelect+" WHERE t.id = ?", threadID)
	if err != nil {
		return ThreadView{}, err
	}
	if len(rows) == 0 {
		return ThreadView{}, domainerr.NotFound(threadID)
	}
	return toThread(rows[0], reader.now())
}

func openAttentions(ctx context.Context, db queryer, projectID string) ([]AttentionView, error) {
	rows, err := records(ctx, db, "SELECT * FROM attentions WHERE project_id = ? AND closed = 0 ORDER BY id", projectID)
	if err != nil {
		return nil, err
	}
	views := make([]AttentionView, 0, len(rows))
	for _, row := range rows {
		block, err := row.json("block", nil)
		if err != nil {
			return nil, err
		}
		waiters, err := records(ctx, db, "SELECT thread_id FROM attention_waiters WHERE attention_id = ? ORDER BY rowid", row["id"])
		if err != nil {
			return nil, err
		}
		joined := []string{}
		for _, waiter := range waiters {
			if waiter["thread_id"] != row["thread_id"] {
				joined = append(joined, waiter.text("thread_id"))
			}
		}
		views = append(views, AttentionView{ID: row.text("id"), ProjectID: row.text("project_id"), ThreadID: row["thread_id"], JoinedBy: joined, Block: block, MessageID: row["message_id"], ConversationID: row["conversation_id"], CreatedAt: row.integer("created_at")})
	}
	return views, nil
}

func needsMe(ctx context.Context, db queryer, projectID string, threads []ThreadView) ([]object, error) {
	attentions, err := openAttentions(ctx, db, projectID)
	if err != nil {
		return nil, err
	}
	byID := map[string]ThreadView{}
	asked := map[string]bool{}
	for _, thread := range threads {
		byID[thread.ID] = thread
	}
	items := []object{}
	for _, attention := range attentions {
		var thread any
		if id, ok := attention.ThreadID.(string); ok {
			asked[id] = true
			if value, ok := byID[id]; ok {
				thread = value
			}
		}
		joined := []ThreadView{}
		for _, id := range attention.JoinedBy {
			asked[id] = true
			if value, ok := byID[id]; ok {
				joined = append(joined, value)
			}
		}
		items = append(items, object{"kind": "attention", "attention": attention, "thread": thread, "joined": joined})
	}
	for _, thread := range threads {
		if (NeedsUser(thread.Stage) || thread.Aside != nil && thread.Aside.Due) && !asked[thread.ID] && (thread.ReviewOf == nil || thread.ReviewOf == "") {
			items = append(items, object{"kind": "thread", "thread": thread})
		}
	}
	return items, nil
}

func toProject(ctx context.Context, db queryer, row record, needs int) (ProjectView, error) {
	view := ProjectView{ID: row.text("id"), Name: row.text("name"), Unnamed: row.flag("unnamed"), CoordinatorID: row.text("coordinator_id"), CreatedAt: row.integer("created_at"), UpdatedAt: row.integer("updated_at"), NeedsMe: needs, Slots: row["slots"], AutopilotPaused: row.flag("autopilot_paused")}
	settings, err := projectsettings.Stored(row.text("settings"))
	if err != nil {
		return ProjectView{}, err
	}
	view.Settings = settings
	for _, field := range []struct {
		key      string
		target   *any
		fallback any
	}{
		{"source", &view.Source, nil}, {"reference_sources", &view.References, []any{}}, {"mcp", &view.MCP, []any{}}, {"detected", &view.Detected, object{"setup": nil, "check": nil}},
	} {
		value, err := row.json(field.key, field.fallback)
		if err != nil {
			return ProjectView{}, err
		}
		*field.target = value
	}
	rows, err := records(ctx, db, `SELECT sum(input_tokens) AS input_tokens, sum(output_tokens) AS output_tokens,
	 sum(cached_tokens) AS cached_tokens, sum(cache_write_tokens) AS cache_write_tokens,
	 sum(reasoning_tokens) AS reasoning_tokens, sum(cost_usd) AS cost_usd, sum(cost_cny) AS cost_cny
	 FROM conversations WHERE project_id = ?`, row["id"])
	if err != nil {
		return ProjectView{}, err
	}
	view.Usage = usage(rows[0])
	return view, nil
}

func projectDetail(ctx context.Context, db queryer, projectID string, now int64) (ProjectDetail, error) {
	rows, err := records(ctx, db, "SELECT * FROM projects WHERE id = ?", projectID)
	if err != nil {
		return ProjectDetail{}, err
	}
	if len(rows) == 0 {
		return ProjectDetail{}, domainerr.NotFound(projectID)
	}
	threads, err := listThreads(ctx, db, projectID, now)
	if err != nil {
		return ProjectDetail{}, err
	}
	needs, err := needsMe(ctx, db, projectID, threads)
	if err != nil {
		return ProjectDetail{}, err
	}
	project, err := toProject(ctx, db, rows[0], len(needs))
	if err != nil {
		return ProjectDetail{}, err
	}
	return ProjectDetail{Project: project, Threads: threads, NeedsMe: needs}, nil
}
func (reader Reader) GetProjectDetail(ctx context.Context, projectID string) (result ProjectDetail, err error) {
	err = reader.snapshot(ctx, func(tx *sql.Tx) error { result, err = projectDetail(ctx, tx, projectID, reader.now()); return err })
	return
}
func (reader Reader) GetProject(ctx context.Context, projectID string) (ProjectView, error) {
	detail, err := reader.GetProjectDetail(ctx, projectID)
	return detail.Project, err
}
func (reader Reader) ListProjects(ctx context.Context) (result []ProjectView, err error) {
	result = []ProjectView{}
	now := reader.now()
	err = reader.snapshot(ctx, func(tx *sql.Tx) error {
		rows, err := records(ctx, tx, "SELECT * FROM projects ORDER BY updated_at DESC")
		if err != nil {
			return err
		}
		for _, row := range rows {
			threads, err := listThreads(ctx, tx, row.text("id"), now)
			if err != nil {
				return err
			}
			needs, err := needsMe(ctx, tx, row.text("id"), threads)
			if err != nil {
				return err
			}
			view, err := toProject(ctx, tx, row, len(needs))
			if err != nil {
				return err
			}
			result = append(result, view)
		}
		return nil
	})
	return
}
