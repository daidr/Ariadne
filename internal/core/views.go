package core

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
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
	RunningTurns    int64                    `json:"runningTurns,omitempty"`
	Source          any                      `json:"source"`
	FileSources     *FileSourceSet           `json:"fileSources,omitempty"`
	References      any                      `json:"references"`
	Slots           any                      `json:"slots"`
	MCP             any                      `json:"mcp"`
	Settings        projectsettings.Settings `json:"settings"`
	Detected        any                      `json:"detected"`
	AutopilotPaused bool                     `json:"autopilotPaused"`
	Usage           map[string]any           `json:"usage"`
}

type WorkspaceView struct {
	SourceID string `json:"sourceId,omitempty"`
	Label    string `json:"label"`
	Branch   any    `json:"branch"`
	Base     any    `json:"base"`
	Removed  bool   `json:"removed"`
	Parked   bool   `json:"parked"`
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
	Effort          any            `json:"effort"`
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
	needsUser       bool
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

type ProjectDetailDelta struct {
	Project   ProjectView
	Threads   []ThreadView
	NeedsMe   []object
	Partial   bool
	Seq       int64
	At        int64
	ThreadIDs []string
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
		ID: row.text("id"), ProjectID: row.text("project_id"), Title: row.text("title"), NodeID: row["node_id"], LastReport: row["last_report"], LastSummary: row["last_summary"], LastHeadline: row["last_headline"], Model: row["model"], Effort: row["effort"], BranchSlug: row["branch_slug"], Usage: usage(row), CreatedAt: row.integer("created_at"), UpdatedAt: row.integer("updated_at"), ReviewOf: row["review_of"], Auto: row.flag("auto"), AutopilotHeld: row["autopilot_held"],
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
	facts := threadStageFacts(row, merged, pullRequest)
	view.Stage = ThreadStage(facts)
	view.needsUser = facts.needsUser(view.Stage)
	view.ActiveAt = max(row.integer("updated_at"), row.integer("message_at"), row.integer("transcript_at"), row.integer("todo_at"), row.integer("turn_started_at"))
	view.Archived = view.Stage == "done" && (row.present("landed_at") && row.integer("landed_at") >= row.integer("reported_at") && row.integer("user_at") <= row.integer("landed_at") && now-row.integer("landed_at") >= int64(24*time.Hour/time.Millisecond) || now-view.ActiveAt >= int64(7*24*time.Hour/time.Millisecond))
	if row.present("workspace_label") {
		view.Workspace = &WorkspaceView{SourceID: row.text("workspace_source_id"), Label: row.text("workspace_label"), Branch: row["workspace_branch"], Base: row["workspace_base"], Removed: row.flag("workspace_removed"), Parked: row.flag("workspace_parked")}
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
	progress, doing, err := readTodoProgress(row)
	if err != nil {
		return ThreadView{}, err
	}
	view.Progress, view.Doing = progress, doing
	return view, nil
}

func readTodoProgress(row record) (*Progress, any, error) {
	todos, err := row.json("todo_items", []any{})
	if err != nil {
		return nil, nil, err
	}
	items, _ := todos.([]any)
	if len(items) == 0 {
		return nil, nil, nil
	}
	progress := &Progress{Total: len(items)}
	var doing any
	for _, item := range items {
		todo, _ := item.(map[string]any)
		switch todo["status"] {
		case "done":
			progress.Done++
		case "doing":
			if doing == nil {
				doing = todo["label"]
			}
		}
	}
	return progress, doing, nil
}

func threadStageFacts(row record, merged, pullRequest map[string]any) StageFacts {
	mergedAt, _ := merged["at"].(float64)
	unreported := func(outcome string) bool {
		return !row.present("turn_id") && row.text("last_outcome") == outcome && row.present("last_turn_at") && row.integer("reported_at") < row.integer("last_turn_at")
	}
	return StageFacts{
		OpenAttentions: int(row.integer("open_attentions")), Started: row.flag("started"), LastReport: row.text("last_report"),
		TurnRunning: row.present("turn_id"), TurnPending: !row.present("turn_id") && row.integer("pending_seq") > 0,
		TurnFailed: !row.present("turn_id") && row.text("last_outcome") == "failed", TurnCancelled: unreported("cancelled"), Unreported: unreported("completed"), Waiting: row.present("wait_id"),
		ChecksGaveUp: row.integer("check_failures") >= 3, Stopped: row.present("stopped_reason"),
		Landed:   merged != nil && (int64(mergedAt) >= row.integer("reported_at") || merged["head"] == row["reported_head"]) || pullRequest["state"] == "merged",
		HostedPR: pullRequest["state"] == "open" || pullRequest["state"] == "closed",
		Reviewed: row.present("reviewed_at") && row.integer("reviewed_at") >= row.integer("reported_at"), UnderReview: row.flag("reviewing"), Reviewer: row.present("review_of"),
	}
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

// ListThreadsByIDs loads a batch of threads in a single read-only snapshot,
// avoiding per-thread N+1 lookups across worktree and listing flows.
func (reader Reader) ListThreadsByIDs(ctx context.Context, ids []string) (map[string]ThreadView, error) {
	result := map[string]ThreadView{}
	if len(ids) == 0 {
		return result, nil
	}
	unique := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		unique = append(unique, id)
	}
	if len(unique) == 0 {
		return result, nil
	}
	const chunkSize = 200
	now := reader.now()
	err := reader.snapshot(ctx, func(tx *sql.Tx) error {
		for start := 0; start < len(unique); start += chunkSize {
			end := min(start+chunkSize, len(unique))
			chunk := unique[start:end]
			placeholders := make([]string, len(chunk))
			args := make([]any, len(chunk))
			for i, id := range chunk {
				placeholders[i] = "?"
				args[i] = id
			}
			rows, err := records(ctx, tx, threadSelect+" WHERE t.id IN ("+strings.Join(placeholders, ",")+")", args...)
			if err != nil {
				return err
			}
			for _, row := range rows {
				view, err := toThread(row, now)
				if err != nil {
					return err
				}
				result[view.ID] = view
			}
		}
		return nil
	})
	return result, err
}

func openAttentions(ctx context.Context, db queryer, projectID string) ([]AttentionView, error) {
	rows, err := records(ctx, db, "SELECT * FROM attentions WHERE project_id = ? AND closed = 0 ORDER BY id", projectID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return []AttentionView{}, nil
	}
	waiterRows, err := records(ctx, db, `SELECT w.attention_id, w.thread_id
	 FROM attention_waiters w
	 JOIN attentions a ON a.id = w.attention_id
	 WHERE a.project_id = ? AND a.closed = 0
	 ORDER BY w.rowid`, projectID)
	if err != nil {
		return nil, err
	}
	waitersByAttention := make(map[string][]string, len(rows))
	for _, waiter := range waiterRows {
		attID := waiter.text("attention_id")
		waitersByAttention[attID] = append(waitersByAttention[attID], waiter.text("thread_id"))
	}
	views := make([]AttentionView, 0, len(rows))
	for _, row := range rows {
		block, err := row.json("block", nil)
		if err != nil {
			return nil, err
		}
		attID := row.text("id")
		ownerID := row.text("thread_id")
		hasOwner := row.present("thread_id")
		joined := []string{}
		for _, threadID := range waitersByAttention[attID] {
			if !hasOwner || threadID != ownerID {
				joined = append(joined, threadID)
			}
		}
		views = append(views, AttentionView{ID: attID, ProjectID: row.text("project_id"), ThreadID: row["thread_id"], JoinedBy: joined, Block: block, MessageID: row["message_id"], ConversationID: row["conversation_id"], CreatedAt: row.integer("created_at")})
	}
	return views, nil
}

func needsMe(ctx context.Context, db queryer, projectID string, threads []ThreadView) ([]object, error) {
	attentions, err := openAttentions(ctx, db, projectID)
	if err != nil {
		return nil, err
	}
	known := make(map[string]bool, len(threads))
	asked := map[string]bool{}
	for _, thread := range threads {
		known[thread.ID] = true
	}
	items := attentionNeedsItems(attentions, known, asked)
	for _, thread := range threads {
		if (thread.needsUser || thread.Aside != nil && thread.Aside.Due) && !asked[thread.ID] && (thread.ReviewOf == nil || thread.ReviewOf == "") {
			items = append(items, object{"kind": "thread", "threadId": thread.ID})
		}
	}
	return items, nil
}

func needsMeLightweight(ctx context.Context, db queryer, projectID string, known map[string]bool) ([]object, error) {
	attentions, err := openAttentions(ctx, db, projectID)
	if err != nil {
		return nil, err
	}
	asked := map[string]bool{}
	items := attentionNeedsItems(attentions, known, asked)
	candidates, err := records(ctx, db, `SELECT t.id, t.started, t.last_report, t.reported_at, t.reported_head,
	 t.merged, t.pull_request, t.check_failures, t.stopped_reason, t.reviewed_at, t.reviewing, t.aside_until, t.aside_due, t.review_of,
	 c.turn_id, c.pending_seq, c.last_turn_at, c.last_outcome, c.wait_id
	 FROM threads t LEFT JOIN conversations c ON c.id = t.id
	 WHERE t.project_id = ? AND coalesce(t.review_of, '') = ''
	 AND (t.last_report = 'ready' OR c.last_outcome IN ('completed','cancelled','failed') OR t.aside_due = 1)
	 ORDER BY t.id`, projectID)
	if err != nil {
		return nil, err
	}
	for _, row := range candidates {
		id := row.text("id")
		if asked[id] {
			continue
		}
		merged, err := row.json("merged", nil)
		if err != nil {
			return nil, err
		}
		pull, err := row.json("pull_request", nil)
		if err != nil {
			return nil, err
		}
		facts := threadStageFacts(row, asObject(merged), asObject(pull))
		if facts.needsUser(ThreadStage(facts)) || row.present("aside_until") && row.flag("aside_due") {
			items = append(items, object{"kind": "thread", "threadId": id})
		}
	}
	return items, nil
}

func attentionNeedsItems(attentions []AttentionView, known, asked map[string]bool) []object {
	items := make([]object, 0, len(attentions))
	for _, attention := range attentions {
		var threadID any
		if id, ok := attention.ThreadID.(string); ok {
			asked[id] = true
			if known[id] {
				threadID = id
			}
		}
		joinedIDs := []string{}
		for _, id := range attention.JoinedBy {
			asked[id] = true
			if known[id] {
				joinedIDs = append(joinedIDs, id)
			}
		}
		items = append(items, object{"kind": "attention", "attention": attention, "threadId": threadID, "joinedIds": joinedIDs})
	}
	return items
}

func toProject(ctx context.Context, db queryer, row record, needs int) (ProjectView, error) {
	rows, err := records(ctx, db, `SELECT sum(input_tokens) AS input_tokens, sum(output_tokens) AS output_tokens,
	 sum(cached_tokens) AS cached_tokens, sum(cache_write_tokens) AS cache_write_tokens,
	 sum(reasoning_tokens) AS reasoning_tokens, sum(cost_usd) AS cost_usd, sum(cost_cny) AS cost_cny,
	 count(turn_id) AS running_turns
	 FROM conversations WHERE project_id = ?`, row["id"])
	if err != nil {
		return ProjectView{}, err
	}
	return toProjectWithUsage(row, needs, rows[0])
}

func toProjectWithUsage(row record, needs int, usageRow record) (ProjectView, error) {
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
	view.FileSources, view.Source, err = readFileSources(view.Source)
	if err != nil {
		return ProjectView{}, err
	}
	view.Usage = usage(usageRow)
	view.RunningTurns = usageRow.integer("running_turns")
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

func listThreadsChangedSince(ctx context.Context, db queryer, projectID string, sinceSeq, sinceAt, now int64) ([]ThreadView, error) {
	rows, err := records(ctx, db, threadSelect+" WHERE t.project_id = ? AND t.changed_seq > ? ORDER BY t.id", projectID, sinceSeq)
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
	if now > sinceAt {
		dayMs := int64(24 * time.Hour / time.Millisecond)
		archivedRows, err := records(ctx, db, `SELECT id FROM threads
		 WHERE project_id = ? AND changed_seq <= ? AND last_report IN ('done', 'ready')
		 AND landed_at IS NOT NULL AND landed_at > ? AND landed_at <= ?
		 ORDER BY id`, projectID, sinceSeq, sinceAt-dayMs, now-dayMs)
		if err != nil {
			return nil, err
		}
		if len(archivedRows) > 0 {
			ids := make([]string, 0, len(archivedRows))
			for _, row := range archivedRows {
				ids = append(ids, row.text("id"))
			}
			encodedIDs, err := json.Marshal(ids)
			if err != nil {
				return nil, err
			}
			extraRows, err := records(ctx, db, threadSelect+" WHERE t.id IN (SELECT value FROM json_each(?)) ORDER BY t.id", string(encodedIDs))
			if err != nil {
				return nil, err
			}
			for _, row := range extraRows {
				view, err := toThread(row, now)
				if err != nil {
					return nil, err
				}
				views = append(views, view)
			}
			slices.SortFunc(views, func(a, b ThreadView) int { return cmp.Compare(a.ID, b.ID) })
		}
	}
	return views, nil
}

func (reader Reader) GetProjectDetailSince(ctx context.Context, projectID string, sinceSeq, sinceAt int64, canPartial bool) (result ProjectDetailDelta, err error) {
	err = reader.snapshot(ctx, func(tx *sql.Tx) error {
		now := reader.now()
		rows, err := records(ctx, tx, "SELECT * FROM projects WHERE id = ?", projectID)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return domainerr.NotFound(projectID)
		}
		var currentSeq int64
		if err := tx.QueryRowContext(ctx, "SELECT coalesce(max(seq), 0) FROM events").Scan(&currentSeq); err != nil {
			return err
		}
		idRows, err := records(ctx, tx, "SELECT id FROM threads WHERE project_id = ? ORDER BY id", projectID)
		if err != nil {
			return err
		}
		threadIDs := make([]string, 0, len(idRows))
		known := make(map[string]bool, len(idRows))
		for _, row := range idRows {
			id := row.text("id")
			threadIDs = append(threadIDs, id)
			known[id] = true
		}
		const hourMs = int64(time.Hour / time.Millisecond)
		partial := canPartial && sinceSeq >= 0 && sinceSeq <= currentSeq && now >= sinceAt && now/hourMs == sinceAt/hourMs
		var threads []ThreadView
		var needs []object
		if partial {
			threads, err = listThreadsChangedSince(ctx, tx, projectID, sinceSeq, sinceAt, now)
			if err != nil {
				return err
			}
			needs, err = needsMeLightweight(ctx, tx, projectID, known)
			if err != nil {
				return err
			}
		} else {
			threads, err = listThreads(ctx, tx, projectID, now)
			if err != nil {
				return err
			}
			needs, err = needsMe(ctx, tx, projectID, threads)
			if err != nil {
				return err
			}
		}
		project, err := toProject(ctx, tx, rows[0], len(needs))
		if err != nil {
			return err
		}
		result = ProjectDetailDelta{Project: project, Threads: threads, NeedsMe: needs, Partial: partial, Seq: currentSeq, At: now, ThreadIDs: threadIDs}
		return nil
	})
	return
}
func (reader Reader) GetProject(ctx context.Context, projectID string) (ProjectView, error) {
	return reader.readProject(ctx, projectID, true)
}

// Project settings and usage are sufficient for running a model step.
func (reader Reader) GetProjectForRun(ctx context.Context, projectID string) (ProjectView, error) {
	return reader.readProject(ctx, projectID, false)
}

func (reader Reader) readProject(ctx context.Context, projectID string, includeNeeds bool) (result ProjectView, err error) {
	err = reader.snapshot(ctx, func(tx *sql.Tx) error {
		rows, err := records(ctx, tx, "SELECT * FROM projects WHERE id = ?", projectID)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return domainerr.NotFound(projectID)
		}
		count := 0
		if includeNeeds {
			count, err = projectNeedsCount(ctx, tx, projectID)
			if err != nil {
				return err
			}
		}
		result, err = toProject(ctx, tx, rows[0], count)
		return err
	})
	return
}

func projectNeedsCount(ctx context.Context, db queryer, projectID string) (int, error) {
	rows, err := records(ctx, db, `SELECT a.id, a.thread_id, w.thread_id AS joined
	 FROM attentions a LEFT JOIN attention_waiters w ON w.attention_id = a.id
	 WHERE a.project_id = ? AND a.closed = 0`, projectID)
	if err != nil {
		return 0, err
	}
	attentions, asked := map[string]bool{}, map[string]bool{}
	for _, row := range rows {
		attentions[row.text("id")] = true
		asked[row.text("thread_id")] = true
		asked[row.text("joined")] = true
	}
	count := len(attentions)
	// Only stage and aside fields contribute to the badge. Message recency,
	// workspace data, artifacts and todos belong to the full thread view.
	candidates, err := records(ctx, db, `SELECT t.id, t.started, t.last_report, t.reported_at, t.reported_head,
	 t.merged, t.pull_request, t.check_failures, t.stopped_reason, t.reviewed_at, t.reviewing, t.aside_until, t.aside_due, t.review_of,
	 c.turn_id, c.pending_seq, c.last_turn_at, c.last_outcome, c.wait_id
	 FROM threads t LEFT JOIN conversations c ON c.id = t.id
	 WHERE t.project_id = ? AND coalesce(t.review_of, '') = ''
	 AND (t.last_report = 'ready' OR c.last_outcome IN ('completed','cancelled','failed') OR t.aside_due = 1)`, projectID)
	if err != nil {
		return 0, err
	}
	for _, row := range candidates {
		if asked[row.text("id")] {
			continue
		}
		merged, err := row.json("merged", nil)
		if err != nil {
			return 0, err
		}
		pull, err := row.json("pull_request", nil)
		if err != nil {
			return 0, err
		}
		facts := threadStageFacts(row, asObject(merged), asObject(pull))
		if facts.needsUser(ThreadStage(facts)) || row.present("aside_until") && row.flag("aside_due") {
			count++
		}
	}
	return count, nil
}

func (reader Reader) ListProjects(ctx context.Context) (result []ProjectView, err error) {
	return reader.listProjects(ctx, "", "SELECT * FROM projects ORDER BY updated_at DESC")
}

func (reader Reader) ListProjectsByID(ctx context.Context, ids []string) ([]ProjectView, error) {
	values, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	return reader.listProjects(ctx, string(values), "SELECT * FROM projects WHERE id IN (SELECT value FROM json_each(?)) ORDER BY updated_at DESC", string(values))
}

func (reader Reader) listProjects(ctx context.Context, filterIDsJSON, query string, args ...any) (result []ProjectView, err error) {
	result = []ProjectView{}
	err = reader.snapshot(ctx, func(tx *sql.Tx) error {
		rows, err := records(ctx, tx, query, args...)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		var usageRows []record
		if filterIDsJSON == "" {
			usageRows, err = records(ctx, tx, `SELECT project_id, sum(input_tokens) AS input_tokens, sum(output_tokens) AS output_tokens,
			 sum(cached_tokens) AS cached_tokens, sum(cache_write_tokens) AS cache_write_tokens,
			 sum(reasoning_tokens) AS reasoning_tokens, sum(cost_usd) AS cost_usd, sum(cost_cny) AS cost_cny,
			 count(turn_id) AS running_turns
			 FROM conversations GROUP BY project_id`)
		} else {
			usageRows, err = records(ctx, tx, `SELECT project_id, sum(input_tokens) AS input_tokens, sum(output_tokens) AS output_tokens,
			 sum(cached_tokens) AS cached_tokens, sum(cache_write_tokens) AS cache_write_tokens,
			 sum(reasoning_tokens) AS reasoning_tokens, sum(cost_usd) AS cost_usd, sum(cost_cny) AS cost_cny,
			 count(turn_id) AS running_turns
			 FROM conversations WHERE project_id IN (SELECT value FROM json_each(?)) GROUP BY project_id`, filterIDsJSON)
		}
		if err != nil {
			return err
		}
		usageByProject := make(map[string]record, len(usageRows))
		for _, row := range usageRows {
			usageByProject[row.text("project_id")] = row
		}
		var attentionRows []record
		if filterIDsJSON == "" {
			attentionRows, err = records(ctx, tx, `SELECT a.project_id, a.id, a.thread_id, w.thread_id AS joined
			 FROM attentions a LEFT JOIN attention_waiters w ON w.attention_id = a.id
			 WHERE a.closed = 0`)
		} else {
			attentionRows, err = records(ctx, tx, `SELECT a.project_id, a.id, a.thread_id, w.thread_id AS joined
			 FROM attentions a LEFT JOIN attention_waiters w ON w.attention_id = a.id
			 WHERE a.closed = 0 AND a.project_id IN (SELECT value FROM json_each(?))`, filterIDsJSON)
		}
		if err != nil {
			return err
		}
		attentionsByProject := map[string]map[string]bool{}
		askedByProject := map[string]map[string]bool{}
		for _, row := range attentionRows {
			pid := row.text("project_id")
			if attentionsByProject[pid] == nil {
				attentionsByProject[pid] = map[string]bool{}
				askedByProject[pid] = map[string]bool{}
			}
			attentionsByProject[pid][row.text("id")] = true
			if id := row.text("thread_id"); id != "" {
				askedByProject[pid][id] = true
			}
			if id := row.text("joined"); id != "" {
				askedByProject[pid][id] = true
			}
		}
		needsByProject := make(map[string]int, len(rows))
		for pid, atts := range attentionsByProject {
			needsByProject[pid] = len(atts)
		}
		var candidateRows []record
		if filterIDsJSON == "" {
			candidateRows, err = records(ctx, tx, `SELECT t.project_id, t.id, t.started, t.last_report, t.reported_at, t.reported_head,
			 t.merged, t.pull_request, t.check_failures, t.stopped_reason, t.reviewed_at, t.reviewing, t.aside_until, t.aside_due, t.review_of,
			 c.turn_id, c.pending_seq, c.last_turn_at, c.last_outcome, c.wait_id
			 FROM threads t LEFT JOIN conversations c ON c.id = t.id
			 WHERE coalesce(t.review_of, '') = ''
			 AND (t.last_report = 'ready' OR c.last_outcome IN ('completed','cancelled','failed') OR t.aside_due = 1)`)
		} else {
			candidateRows, err = records(ctx, tx, `SELECT t.project_id, t.id, t.started, t.last_report, t.reported_at, t.reported_head,
			 t.merged, t.pull_request, t.check_failures, t.stopped_reason, t.reviewed_at, t.reviewing, t.aside_until, t.aside_due, t.review_of,
			 c.turn_id, c.pending_seq, c.last_turn_at, c.last_outcome, c.wait_id
			 FROM threads t LEFT JOIN conversations c ON c.id = t.id
			 WHERE coalesce(t.review_of, '') = ''
			 AND (t.last_report = 'ready' OR c.last_outcome IN ('completed','cancelled','failed') OR t.aside_due = 1)
			 AND t.project_id IN (SELECT value FROM json_each(?))`, filterIDsJSON)
		}
		if err != nil {
			return err
		}
		for _, row := range candidateRows {
			pid := row.text("project_id")
			if askedByProject[pid][row.text("id")] {
				continue
			}
			merged, err := row.json("merged", nil)
			if err != nil {
				return err
			}
			pull, err := row.json("pull_request", nil)
			if err != nil {
				return err
			}
			facts := threadStageFacts(row, asObject(merged), asObject(pull))
			if facts.needsUser(ThreadStage(facts)) || row.present("aside_until") && row.flag("aside_due") {
				needsByProject[pid]++
			}
		}
		result = make([]ProjectView, 0, len(rows))
		for _, row := range rows {
			pid := row.text("id")
			usageRow := usageByProject[pid]
			if usageRow == nil {
				usageRow = record{}
			}
			view, err := toProjectWithUsage(row, needsByProject[pid], usageRow)
			if err != nil {
				return err
			}
			result = append(result, view)
		}
		return nil
	})
	return
}
