package core

import (
	"cmp"
	"context"
	"database/sql"
	"slices"
	"time"

	"github.com/daidr/Ariadne/internal/domainerr"
)

const (
	// DefaultPulseWindowMs is used when GetPulseRequest omits from_ms.
	DefaultPulseWindowMs = int64(time.Hour / time.Millisecond)

	pulseSpansProjectOpenSQL = `SELECT id, project_id, conversation_id, stage, occupies_slot, started_at, ended_at, seq_start, seq_end, label
	 FROM pulse_spans
	 WHERE project_id = ? AND ended_at IS NULL AND started_at <= ?`

	pulseSpansProjectClosedSQL = `SELECT id, project_id, conversation_id, stage, occupies_slot, started_at, ended_at, seq_start, seq_end, label
	 FROM pulse_spans
	 WHERE project_id = ? AND ended_at >= ? AND started_at <= ?`

	pulseSpansGlobalOpenSQL = `SELECT id, project_id, conversation_id, stage, occupies_slot, started_at, ended_at, seq_start, seq_end, label
	 FROM pulse_spans
	 WHERE ended_at IS NULL AND started_at <= ?`

	pulseSpansGlobalClosedSQL = `SELECT id, project_id, conversation_id, stage, occupies_slot, started_at, ended_at, seq_start, seq_end, label
	 FROM pulse_spans
	 WHERE ended_at >= ? AND started_at <= ?`

	pulseMarkersProjectSQL = `SELECT seq, project_id, conversation_id, related_thread_id, kind, at, label
	 FROM pulse_markers
	 WHERE project_id = ? AND at >= ? AND at <= ?
	 ORDER BY at ASC, seq ASC`

	pulseMarkersGlobalSQL = `SELECT seq, project_id, conversation_id, related_thread_id, kind, at, label
	 FROM pulse_markers
	 WHERE at >= ? AND at <= ?
	 ORDER BY at ASC, seq ASC`

	pulseLastReturnedProjectSQL = `SELECT at FROM pulse_markers
	 WHERE project_id = ? AND kind = 'user_returned'
	 ORDER BY at DESC LIMIT 1`

	pulseLastReturnedGlobalSQL = `SELECT at FROM pulse_markers
	 WHERE kind = 'user_returned'
	 ORDER BY at DESC LIMIT 1`

	pulseBackfillEventsSQL = `SELECT seq, type, data, at FROM events
	 WHERE project_id = ? AND seq >= ? ORDER BY seq`
)

// PulseQuery parameterizes Reader.GetPulse.
type PulseQuery struct {
	ProjectID        string
	FromMs           int64
	ToMs             int64
	IncludeCompleted bool
	GlobalSlots      int
}

type PulseLane struct {
	ConversationID string  `json:"conversationId"`
	ProjectID      string  `json:"projectId"`
	ProjectName    string  `json:"projectName"`
	Title          string  `json:"title"`
	Role           string  `json:"role"`
	ReviewOf       *string `json:"reviewOf,omitempty"`
	Stage          string  `json:"stage"`
	Completed      bool    `json:"completed"`
	AttentionID    *string `json:"attentionId,omitempty"`
	LiveCommand    *string `json:"liveCommand,omitempty"`
	LiveTail       *string `json:"liveTail,omitempty"`
	LastHeadline   *string `json:"lastHeadline,omitempty"`
	ActiveAt       int64   `json:"activeAt"`
	createdAt      int64
}

type PulseSpan struct {
	ID             int64  `json:"id"`
	ProjectID      string `json:"projectId"`
	ConversationID string `json:"conversationId"`
	Stage          string `json:"stage"`
	OccupiesSlot   bool   `json:"occupiesSlot"`
	StartedAt      int64  `json:"startedAt"`
	EndedAt        *int64 `json:"endedAt,omitempty"`
	SeqStart       int64  `json:"seqStart"`
	SeqEnd         *int64 `json:"seqEnd,omitempty"`
	Label          string `json:"label"`
}

type PulseMarker struct {
	Seq             int64   `json:"seq"`
	ProjectID       string  `json:"projectId"`
	ConversationID  string  `json:"conversationId"`
	RelatedThreadID *string `json:"relatedThreadId,omitempty"`
	Kind            string  `json:"kind"`
	At              int64   `json:"at"`
	Label           string  `json:"label"`
}

type PulseHandoff struct {
	Seq                int64  `json:"seq"`
	ProjectID          string `json:"projectId"`
	FromConversationID string `json:"fromConversationId"`
	ToConversationID   string `json:"toConversationId"`
	Kind               string `json:"kind"`
	At                 int64  `json:"at"`
	Label              string `json:"label"`
}

type PulseConcurrencyPoint struct {
	At     int64 `json:"at"`
	Active int32 `json:"active"`
}

type PulseConcurrency struct {
	Slots   int32                   `json:"slots"`
	Current int32                   `json:"current"`
	Peak    int32                   `json:"peak"`
	Points  []PulseConcurrencyPoint `json:"points"`
}

type PulseSnapshot struct {
	Lanes          []PulseLane      `json:"lanes"`
	Spans          []PulseSpan      `json:"spans"`
	Markers        []PulseMarker    `json:"markers"`
	Handoffs       []PulseHandoff   `json:"handoffs"`
	Concurrency    PulseConcurrency `json:"concurrency"`
	LastReturnedAt *int64           `json:"lastReturnedAt,omitempty"`
	CursorSeq      int64            `json:"cursorSeq"`
	FromMs         int64            `json:"fromMs"`
	ToMs           int64            `json:"toMs"`
	ServerNowMs    int64            `json:"serverNowMs"`
}

// GetPulse returns a consistent snapshot of macro Pulse spans, milestones,
// reviewer handoffs, concurrency watermark series, and active lanes for either
// one project or across all active projects.
func (reader Reader) GetPulse(ctx context.Context, query PulseQuery) (result PulseSnapshot, err error) {
	now := reader.now()
	toMs := query.ToMs
	if toMs == 0 {
		toMs = now
	}
	if toMs < 0 {
		return PulseSnapshot{}, badInput("toMs")
	}
	fromMs := query.FromMs
	if fromMs == 0 {
		fromMs = max(toMs-DefaultPulseWindowMs, 0)
	}
	if fromMs < 0 || fromMs > toMs {
		return PulseSnapshot{}, badInput("fromMs")
	}
	slots := query.GlobalSlots
	if slots <= 0 {
		slots = 3
	}

	result = PulseSnapshot{
		Lanes:       []PulseLane{},
		Spans:       []PulseSpan{},
		Markers:     []PulseMarker{},
		Handoffs:    []PulseHandoff{},
		Concurrency: PulseConcurrency{Slots: int32(slots), Points: []PulseConcurrencyPoint{}},
		FromMs:      fromMs,
		ToMs:        toMs,
		ServerNowMs: now,
	}

	err = reader.snapshot(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, "SELECT coalesce(max(seq), 0) FROM events").Scan(&result.CursorSeq); err != nil {
			return err
		}

		projectNames := map[string]string{}
		coordByProject := map[string]string{}
		if query.ProjectID != "" {
			var name, coordID string
			err := tx.QueryRowContext(ctx, "SELECT name, coordinator_id FROM projects WHERE id = ?", query.ProjectID).Scan(&name, &coordID)
			if err == sql.ErrNoRows {
				return domainerr.NotFound(query.ProjectID)
			}
			if err != nil {
				return err
			}
			projectNames[query.ProjectID] = name
			coordByProject[query.ProjectID] = coordID
		}

		spans, err := queryPulseSpans(ctx, tx, query.ProjectID, fromMs, toMs)
		if err != nil {
			return err
		}
		markers, err := queryPulseMarkers(ctx, tx, query.ProjectID, fromMs, toMs)
		if err != nil {
			return err
		}
		lastReturned, err := queryPulseLastReturned(ctx, tx, query.ProjectID)
		if err != nil {
			return err
		}

		effectiveTo := min(toMs, now)
		result.Concurrency = computePulseConcurrency(spans, fromMs, effectiveTo, slots)
		result.LastReturnedAt = lastReturned

		projectIDs := map[string]bool{}
		convIDs := map[string]bool{}
		hasOpenSpan := map[string]bool{}
		laneActivityAt := map[string]int64{}

		bumpActivity := func(convID string, at int64) {
			if convID == "" {
				return
			}
			if at > laneActivityAt[convID] {
				laneActivityAt[convID] = at
			}
		}

		for _, span := range spans {
			projectIDs[span.ProjectID] = true
			convIDs[span.ConversationID] = true
			if span.EndedAt == nil {
				hasOpenSpan[span.ConversationID] = true
				bumpActivity(span.ConversationID, now)
			} else {
				bumpActivity(span.ConversationID, *span.EndedAt)
			}
			bumpActivity(span.ConversationID, span.StartedAt)
		}
		for _, marker := range markers {
			projectIDs[marker.ProjectID] = true
			if marker.ConversationID != "" {
				convIDs[marker.ConversationID] = true
				bumpActivity(marker.ConversationID, marker.At)
			}
			if marker.RelatedThreadID != nil && *marker.RelatedThreadID != "" {
				convIDs[*marker.RelatedThreadID] = true
				bumpActivity(*marker.RelatedThreadID, marker.At)
			}
		}

		if len(projectIDs) > 0 && query.ProjectID == "" {
			if err := loadPulseProjects(ctx, tx, mapKeys(projectIDs), projectNames, coordByProject); err != nil {
				return err
			}
		}

		lanes, err := buildPulseLanes(ctx, tx, mapKeys(convIDs), projectNames, hasOpenSpan, laneActivityAt, now)
		if err != nil {
			return err
		}

		if !query.IncludeCompleted {
			kept := filterPulseLanesActive(lanes)
			keptSet := make(map[string]bool, len(kept))
			for _, lane := range kept {
				keptSet[lane.ConversationID] = true
			}
			lanes = kept

			filteredSpans := make([]PulseSpan, 0, len(spans))
			for _, span := range spans {
				if keptSet[span.ConversationID] {
					filteredSpans = append(filteredSpans, span)
				}
			}
			spans = filteredSpans

			filteredMarkers := make([]PulseMarker, 0, len(markers))
			for _, marker := range markers {
				if marker.ConversationID == "" || keptSet[marker.ConversationID] {
					filteredMarkers = append(filteredMarkers, marker)
				}
			}
			markers = filteredMarkers
		}

		laneSet := make(map[string]bool, len(lanes))
		for _, lane := range lanes {
			laneSet[lane.ConversationID] = true
		}

		handoffs := make([]PulseHandoff, 0)
		for _, marker := range markers {
			if (marker.Kind == PulseMarkerReviewHandoff || marker.Kind == PulseMarkerReviewSubmitted) &&
				marker.ConversationID != "" &&
				marker.RelatedThreadID != nil && *marker.RelatedThreadID != "" {
				if laneSet[marker.ConversationID] && laneSet[*marker.RelatedThreadID] {
					handoffs = append(handoffs, PulseHandoff{
						Seq:                marker.Seq,
						ProjectID:          marker.ProjectID,
						FromConversationID: marker.ConversationID,
						ToConversationID:   *marker.RelatedThreadID,
						Kind:               marker.Kind,
						At:                 marker.At,
						Label:              marker.Label,
					})
				}
			}
		}

		result.Lanes = lanes
		result.Spans = spans
		result.Markers = markers
		result.Handoffs = handoffs
		return nil
	})
	return result, err
}

func queryPulseSpans(ctx context.Context, tx *sql.Tx, projectID string, fromMs, toMs int64) ([]PulseSpan, error) {
	scanRows := func(query string, args ...any) ([]PulseSpan, error) {
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []PulseSpan{}
		for rows.Next() {
			var span PulseSpan
			var occupies int
			var endedAt, seqEnd sql.NullInt64
			if err := rows.Scan(
				&span.ID, &span.ProjectID, &span.ConversationID, &span.Stage,
				&occupies, &span.StartedAt, &endedAt, &span.SeqStart, &seqEnd, &span.Label,
			); err != nil {
				return nil, err
			}
			span.OccupiesSlot = occupies != 0
			if endedAt.Valid {
				v := endedAt.Int64
				span.EndedAt = &v
			}
			if seqEnd.Valid {
				v := seqEnd.Int64
				span.SeqEnd = &v
			}
			out = append(out, span)
		}
		return out, rows.Err()
	}

	var openSpans, closedSpans []PulseSpan
	var err error
	if projectID != "" {
		openSpans, err = scanRows(pulseSpansProjectOpenSQL, projectID, toMs)
		if err != nil {
			return nil, err
		}
		closedSpans, err = scanRows(pulseSpansProjectClosedSQL, projectID, fromMs, toMs)
		if err != nil {
			return nil, err
		}
	} else {
		openSpans, err = scanRows(pulseSpansGlobalOpenSQL, toMs)
		if err != nil {
			return nil, err
		}
		closedSpans, err = scanRows(pulseSpansGlobalClosedSQL, fromMs, toMs)
		if err != nil {
			return nil, err
		}
	}

	all := make([]PulseSpan, 0, len(openSpans)+len(closedSpans))
	all = append(all, closedSpans...)
	all = append(all, openSpans...)
	slices.SortFunc(all, func(a, b PulseSpan) int {
		if c := cmp.Compare(a.StartedAt, b.StartedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return all, nil
}

func queryPulseMarkers(ctx context.Context, tx *sql.Tx, projectID string, fromMs, toMs int64) ([]PulseMarker, error) {
	var rows *sql.Rows
	var err error
	if projectID != "" {
		rows, err = tx.QueryContext(ctx, pulseMarkersProjectSQL, projectID, fromMs, toMs)
	} else {
		rows, err = tx.QueryContext(ctx, pulseMarkersGlobalSQL, fromMs, toMs)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []PulseMarker{}
	for rows.Next() {
		var marker PulseMarker
		var related sql.NullString
		if err := rows.Scan(
			&marker.Seq, &marker.ProjectID, &marker.ConversationID,
			&related, &marker.Kind, &marker.At, &marker.Label,
		); err != nil {
			return nil, err
		}
		if related.Valid && related.String != "" {
			v := related.String
			marker.RelatedThreadID = &v
		}
		out = append(out, marker)
	}
	return out, rows.Err()
}

func queryPulseLastReturned(ctx context.Context, tx *sql.Tx, projectID string) (*int64, error) {
	var at int64
	var err error
	if projectID != "" {
		err = tx.QueryRowContext(ctx, pulseLastReturnedProjectSQL, projectID).Scan(&at)
	} else {
		err = tx.QueryRowContext(ctx, pulseLastReturnedGlobalSQL).Scan(&at)
	}
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &at, nil
}

func loadPulseProjects(ctx context.Context, tx *sql.Tx, ids []string, names, coords map[string]string) error {
	const chunkSize = 200
	for start := 0; start < len(ids); start += chunkSize {
		end := min(start+chunkSize, len(ids))
		chunk := ids[start:end]
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := records(ctx, tx, "SELECT id, name, coordinator_id FROM projects WHERE id IN ("+inPlaceholders(len(chunk))+")", args...)
		if err != nil {
			return err
		}
		for _, row := range rows {
			id := row.text("id")
			names[id] = row.text("name")
			coords[id] = row.text("coordinator_id")
		}
	}
	return nil
}

func buildPulseLanes(
	ctx context.Context,
	tx *sql.Tx,
	convIDs []string,
	projectNames map[string]string,
	hasOpenSpan map[string]bool,
	laneActivityAt map[string]int64,
	now int64,
) ([]PulseLane, error) {
	if len(convIDs) == 0 {
		return []PulseLane{}, nil
	}

	convs := map[string]record{}
	threads := map[string]record{}
	involvedProjects := map[string]bool{}

	const chunkSize = 200
	for start := 0; start < len(convIDs); start += chunkSize {
		end := min(start+chunkSize, len(convIDs))
		chunk := convIDs[start:end]
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		placeholders := inPlaceholders(len(chunk))

		convRows, err := records(ctx, tx, `SELECT id, project_id, role, turn_id, pending_seq, last_outcome, last_turn_at, wait_id, turn_started_at
		 FROM conversations WHERE id IN (`+placeholders+`)`, args...)
		if err != nil {
			return nil, err
		}
		for _, row := range convRows {
			convs[row.text("id")] = row
			if pid := row.text("project_id"); pid != "" {
				involvedProjects[pid] = true
			}
		}

		threadRows, err := records(ctx, tx, `SELECT id, project_id, title, started, last_report, last_headline, reported_at, reported_head,
		 merged, pull_request, check_failures, stopped_reason, reviewed_at, review_of, reviewer_id, reviewing,
		 aside_until, aside_due, landed_at, user_at, created_at, updated_at
		 FROM threads WHERE id IN (`+placeholders+`)`, args...)
		if err != nil {
			return nil, err
		}
		for _, row := range threadRows {
			threads[row.text("id")] = row
		}
	}

	openAttCount, primaryAttID, err := loadPulseOpenAttentions(ctx, tx, mapKeys(involvedProjects))
	if err != nil {
		return nil, err
	}

	lanes := make([]PulseLane, 0, len(convIDs))
	for _, id := range convIDs {
		conv, ok := convs[id]
		if !ok {
			continue
		}
		pid := conv.text("project_id")
		projName, knownProject := projectNames[pid]
		if !knownProject {
			continue
		}

		lane := PulseLane{
			ConversationID: id,
			ProjectID:      pid,
			ProjectName:    projName,
			ActiveAt:       laneActivityAt[id],
		}
		if attID := primaryAttID[id]; attID != "" {
			v := attID
			lane.AttentionID = &v
		}

		if conv.text("role") == "coordinator" {
			lane.Role = "coordinator"
			switch {
			case openAttCount[id] > 0:
				lane.Stage = PulseStageAnswer
			case conv.present("turn_id"):
				lane.Stage = PulseStageWorking
			case conv.integer("pending_seq") > 0:
				lane.Stage = PulseStageQueued
			case conv.text("last_outcome") == "failed":
				lane.Stage = PulseStageHalted
			case conv.present("wait_id"):
				lane.Stage = PulseStageWaiting
			default:
				lane.Stage = "idle"
			}
			lane.ActiveAt = max(lane.ActiveAt, conv.integer("turn_started_at"), conv.integer("last_turn_at"))
			lanes = append(lanes, lane)
			continue
		}

		thread, hasThread := threads[id]
		if !hasThread {
			continue
		}
		lane.Title = thread.text("title")
		lane.createdAt = thread.integer("created_at")
		if thread.present("review_of") {
			lane.Role = "reviewer"
			v := thread.text("review_of")
			lane.ReviewOf = &v
		} else {
			lane.Role = "worker"
		}
		if hl := thread.text("last_headline"); hl != "" {
			v := hl
			lane.LastHeadline = &v
		}

		merged, err := thread.json("merged", nil)
		if err != nil {
			return nil, err
		}
		pullRequest, err := thread.json("pull_request", nil)
		if err != nil {
			return nil, err
		}

		// Merge conversation + open attention fields into a view row for threadStageFacts.
		factRow := record{}
		for k, v := range thread {
			factRow[k] = v
		}
		factRow["turn_id"] = conv["turn_id"]
		factRow["pending_seq"] = conv["pending_seq"]
		factRow["last_outcome"] = conv["last_outcome"]
		factRow["last_turn_at"] = conv["last_turn_at"]
		factRow["wait_id"] = conv["wait_id"]
		factRow["open_attentions"] = int64(openAttCount[id])

		facts := threadStageFacts(factRow, asObject(merged), asObject(pullRequest))
		lane.Stage = ThreadStage(facts)
		lane.ActiveAt = max(lane.ActiveAt, thread.integer("updated_at"), thread.integer("reported_at"), conv.integer("turn_started_at"))
		lane.Completed = !hasOpenSpan[id] && (lane.Stage == "done" || threadArchivedAt(factRow, lane.Stage, lane.ActiveAt, now))
		lanes = append(lanes, lane)
	}

	return sortPulseLanes(lanes), nil
}

func loadPulseOpenAttentions(ctx context.Context, tx *sql.Tx, projectIDs []string) (count map[string]int, primary map[string]string, err error) {
	count = map[string]int{}
	primary = map[string]string{}
	if len(projectIDs) == 0 {
		return count, primary, nil
	}

	attIDs := []string{}
	ownerSeen := map[string]map[string]bool{}
	recordAtt := func(convID, attID string) {
		if convID == "" || attID == "" {
			return
		}
		if ownerSeen[convID] == nil {
			ownerSeen[convID] = map[string]bool{}
		}
		if !ownerSeen[convID][attID] {
			ownerSeen[convID][attID] = true
			count[convID]++
		}
		if primary[convID] == "" {
			primary[convID] = attID
		}
	}

	const chunkSize = 200
	for start := 0; start < len(projectIDs); start += chunkSize {
		end := min(start+chunkSize, len(projectIDs))
		chunk := projectIDs[start:end]
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := records(ctx, tx, `SELECT id, thread_id, conversation_id, created_at
		 FROM attentions
		 WHERE project_id IN (`+inPlaceholders(len(chunk))+`) AND closed = 0
		 ORDER BY created_at ASC, id ASC`, args...)
		if err != nil {
			return nil, nil, err
		}
		for _, row := range rows {
			attID := row.text("id")
			attIDs = append(attIDs, attID)
			target := row.text("thread_id")
			if target == "" {
				target = row.text("conversation_id")
			}
			recordAtt(target, attID)
		}
	}

	for start := 0; start < len(attIDs); start += chunkSize {
		end := min(start+chunkSize, len(attIDs))
		chunk := attIDs[start:end]
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := records(ctx, tx, `SELECT attention_id, thread_id
		 FROM attention_waiters
		 WHERE attention_id IN (`+inPlaceholders(len(chunk))+`)`, args...)
		if err != nil {
			return nil, nil, err
		}
		for _, row := range rows {
			recordAtt(row.text("thread_id"), row.text("attention_id"))
		}
	}

	return count, primary, nil
}

func filterPulseLanesActive(lanes []PulseLane) []PulseLane {
	activeByID := map[string]bool{}
	for _, lane := range lanes {
		if !lane.Completed {
			activeByID[lane.ConversationID] = true
		}
	}
	kept := make([]PulseLane, 0, len(lanes))
	for _, lane := range lanes {
		if !lane.Completed {
			kept = append(kept, lane)
			continue
		}
		// Keep a reviewer lane attached to an active author so handoff edges stay connected.
		if lane.Role == "reviewer" && lane.ReviewOf != nil && activeByID[*lane.ReviewOf] {
			kept = append(kept, lane)
		}
	}
	return kept
}

func sortPulseLanes(lanes []PulseLane) []PulseLane {
	if len(lanes) <= 1 {
		return lanes
	}

	// Group by project, ordering projects by most recently active first.
	byProject := map[string][]PulseLane{}
	projectActive := map[string]int64{}
	projectOrder := []string{}
	for _, lane := range lanes {
		if _, exists := byProject[lane.ProjectID]; !exists {
			projectOrder = append(projectOrder, lane.ProjectID)
		}
		byProject[lane.ProjectID] = append(byProject[lane.ProjectID], lane)
		if lane.ActiveAt > projectActive[lane.ProjectID] {
			projectActive[lane.ProjectID] = lane.ActiveAt
		}
	}
	slices.SortFunc(projectOrder, func(a, b string) int {
		if c := cmp.Compare(projectActive[b], projectActive[a]); c != 0 {
			return c
		}
		return cmp.Compare(a, b)
	})

	result := make([]PulseLane, 0, len(lanes))
	for _, pid := range projectOrder {
		group := byProject[pid]
		var coordinators []PulseLane
		var activeWorkers []PulseLane
		var completedWorkers []PulseLane
		reviewersByAuthor := map[string][]PulseLane{}
		workerExists := map[string]bool{}

		for _, lane := range group {
			switch lane.Role {
			case "coordinator":
				coordinators = append(coordinators, lane)
			case "worker":
				workerExists[lane.ConversationID] = true
				if lane.Completed {
					completedWorkers = append(completedWorkers, lane)
				} else {
					activeWorkers = append(activeWorkers, lane)
				}
			}
		}

		var orphanActiveReviewers []PulseLane
		var orphanCompletedReviewers []PulseLane
		for _, lane := range group {
			if lane.Role != "reviewer" {
				continue
			}
			if lane.ReviewOf != nil && workerExists[*lane.ReviewOf] {
				reviewersByAuthor[*lane.ReviewOf] = append(reviewersByAuthor[*lane.ReviewOf], lane)
			} else if lane.Completed {
				orphanCompletedReviewers = append(orphanCompletedReviewers, lane)
			} else {
				orphanActiveReviewers = append(orphanActiveReviewers, lane)
			}
		}

		sortActive := func(items []PulseLane) {
			slices.SortFunc(items, func(a, b PulseLane) int {
				if c := cmp.Compare(b.createdAt, a.createdAt); c != 0 {
					return c
				}
				return cmp.Compare(a.ConversationID, b.ConversationID)
			})
		}
		sortCompleted := func(items []PulseLane) {
			slices.SortFunc(items, func(a, b PulseLane) int {
				if c := cmp.Compare(b.ActiveAt, a.ActiveAt); c != 0 {
					return c
				}
				return cmp.Compare(a.ConversationID, b.ConversationID)
			})
		}

		slices.SortFunc(coordinators, func(a, b PulseLane) int {
			return cmp.Compare(a.ConversationID, b.ConversationID)
		})
		sortActive(activeWorkers)
		sortActive(orphanActiveReviewers)
		sortCompleted(completedWorkers)
		sortCompleted(orphanCompletedReviewers)

		appendWithReviewers := func(workers []PulseLane) {
			for _, w := range workers {
				result = append(result, w)
				revs := reviewersByAuthor[w.ConversationID]
				sortActive(revs)
				result = append(result, revs...)
			}
		}

		result = append(result, coordinators...)
		appendWithReviewers(activeWorkers)
		result = append(result, orphanActiveReviewers...)
		appendWithReviewers(completedWorkers)
		result = append(result, orphanCompletedReviewers...)
	}
	return result
}

type slotInterval struct {
	start int64
	end   int64
	open  bool
}

type sweepDelta struct {
	at    int64
	delta int32
}

// computePulseConcurrency runs an O(N log N) sweep line over slot-occupying spans
// clamped to [fromMs, effectiveTo], merging overlapping intervals per conversation
// and coalescing identical timestamps to emit clean step-after watermark points.
func computePulseConcurrency(spans []PulseSpan, fromMs, effectiveTo int64, slots int) PulseConcurrency {
	out := PulseConcurrency{
		Slots:  int32(slots),
		Points: []PulseConcurrencyPoint{},
	}
	if effectiveTo < fromMs {
		return out
	}

	byConv := map[string][]slotInterval{}
	for _, span := range spans {
		if !span.OccupiesSlot {
			continue
		}
		if span.StartedAt > effectiveTo {
			continue
		}
		start := max(span.StartedAt, fromMs)
		var end int64
		var open bool
		if span.EndedAt == nil {
			end = effectiveTo
			open = true
		} else {
			rawEnd := *span.EndedAt
			if rawEnd < fromMs {
				continue
			}
			if rawEnd > effectiveTo {
				end = effectiveTo
				open = true
			} else {
				if rawEnd <= start {
					continue
				}
				end = rawEnd
				open = false
			}
		}
		byConv[span.ConversationID] = append(byConv[span.ConversationID], slotInterval{
			start: start,
			end:   end,
			open:  open,
		})
	}

	events := make([]sweepDelta, 0, len(spans)*2)
	for _, intervals := range byConv {
		slices.SortFunc(intervals, func(a, b slotInterval) int {
			if c := cmp.Compare(a.start, b.start); c != 0 {
				return c
			}
			return cmp.Compare(a.end, b.end)
		})
		merged := intervals[:0]
		for _, iv := range intervals {
			if len(merged) == 0 {
				merged = append(merged, iv)
				continue
			}
			last := &merged[len(merged)-1]
			if iv.start <= last.end {
				if iv.end > last.end {
					last.end = iv.end
					last.open = iv.open
				} else if iv.end == last.end {
					last.open = last.open || iv.open
				}
			} else {
				merged = append(merged, iv)
			}
		}
		for _, iv := range merged {
			events = append(events, sweepDelta{at: iv.start, delta: 1})
			if !iv.open {
				events = append(events, sweepDelta{at: iv.end, delta: -1})
			}
		}
	}

	slices.SortFunc(events, func(a, b sweepDelta) int {
		return cmp.Compare(a.at, b.at)
	})

	points := make([]PulseConcurrencyPoint, 0, len(events)+2)
	var active, peak int32

	if len(events) == 0 || events[0].at > fromMs {
		points = append(points, PulseConcurrencyPoint{At: fromMs, Active: 0})
	}

	for i := 0; i < len(events); {
		t := events[i].at
		var delta int32
		for i < len(events) && events[i].at == t {
			delta += events[i].delta
			i++
		}
		active += delta
		if active < 0 {
			active = 0
		}
		if active > peak {
			peak = active
		}
		if len(points) == 0 {
			points = append(points, PulseConcurrencyPoint{At: t, Active: active})
		} else if points[len(points)-1].At == t {
			points[len(points)-1].Active = active
		} else if points[len(points)-1].Active != active {
			points = append(points, PulseConcurrencyPoint{At: t, Active: active})
		}
	}

	out.Current = active
	out.Peak = peak
	if len(points) == 0 || points[len(points)-1].At < effectiveTo {
		points = append(points, PulseConcurrencyPoint{At: effectiveTo, Active: active})
	}
	out.Points = points
	return out
}

func mapKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
