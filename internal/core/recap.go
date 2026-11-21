package core

import (
	"context"
	"encoding/json"
	"math"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/eventstore"
)

const AwayMilliseconds = 10 * 60_000

// firstSeqAfter uses the log's chronological order, including gaps after a purge.
func firstSeqAfter(w *projectionWriter, at float64) int64 {
	low := int64(1)
	high := w.one("SELECT coalesce(max(seq),0) AS seq FROM events").integer("seq") + 1
	for low < high && w.err == nil {
		middle := low + (high-low)/2
		event := w.one("SELECT seq, at FROM events WHERE seq >= ? ORDER BY seq LIMIT 1", middle)
		if event == nil || float64(event.integer("at")) > at {
			high = middle
		} else {
			low = event.integer("seq") + 1
		}
	}
	return low
}
func (repo *Repository) NoteReturn(ctx context.Context, projectID string, away float64) error {
	return repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		w := &projectionWriter{ctx: ctx, tx: tx.SQL()}
		project := w.one("SELECT coordinator_id FROM projects WHERE id = ?", projectID)
		if w.err != nil {
			return w.err
		}
		if project == nil {
			return domainerr.NotFound(projectID)
		}
		if math.IsNaN(away) || math.IsInf(away, 0) || away < AwayMilliseconds {
			return nil
		}
		now := repo.Reader.now()
		told := w.one("SELECT created_at FROM notices WHERE conversation_id = ? AND kind = 'recap' ORDER BY created_at DESC LIMIT 1", project.text("coordinator_id")).integer("created_at")
		if w.err != nil {
			return w.err
		}
		since := math.Max(float64(now)-away, float64(told))
		if float64(now)-since < AwayMilliseconds {
			return nil
		}
		threads, err := listThreads(ctx, tx.SQL(), projectID, now)
		if err != nil {
			return err
		}
		byID := map[string]ThreadView{}
		for _, thread := range threads {
			byID[thread.ID] = thread
		}
		first := firstSeqAfter(w, since)
		reported, landed := []string{}, []string{}
		seenReport, seenLanded := map[string]bool{}, map[string]bool{}
		for _, row := range w.all(`SELECT type,data FROM events WHERE project_id = ? AND seq >= ? AND type IN ('thread.reported','branch.merged','pull_request.closed') ORDER BY seq`, projectID, first) {
			var data object
			if err := json.Unmarshal([]byte(row.text("data")), &data); err != nil {
				return err
			}
			id := data.str("threadId")
			if row.text("type") == "thread.reported" {
				if data.str("report") == "done" && !seenReport[id] {
					reported = append(reported, id)
					seenReport[id] = true
				}
			} else if (row.text("type") == "branch.merged" || data.yes("merged")) && !seenLanded[id] {
				landed = append(landed, id)
				seenLanded[id] = true
			}
		}
		if w.err != nil {
			return w.err
		}
		needs, err := needsMe(ctx, tx.SQL(), projectID, threads)
		if err != nil {
			return err
		}
		waiting, asks := []string{}, []string{}
		waitSet := map[string]bool{}
		addWaiting := func(id string) {
			if !waitSet[id] {
				waitSet[id] = true
				waiting = append(waiting, id)
			}
		}
		for _, item := range needs {
			if item.str("kind") == "thread" {
				if thread, ok := byID[item.str("threadId")]; ok && float64(max(thread.ActiveAt, thread.UpdatedAt)) > since {
					addWaiting(thread.ID)
				}
			} else {
				attention := item["attention"].(AttentionView)
				if float64(attention.CreatedAt) <= since {
					continue
				}
				askers := 0
				if id := item.str("threadId"); id != "" {
					if _, ok := byID[id]; ok {
						addWaiting(id)
						askers++
					}
				}
				if joinedIDs, ok := item["joinedIds"].([]string); ok {
					for _, id := range joinedIDs {
						if _, ok := byID[id]; ok {
							addWaiting(id)
							askers++
						}
					}
				}
				if askers == 0 {
					asks = append(asks, attention.ID)
				}
			}
		}
		tells := func(id string) bool { thread, ok := byID[id]; return ok && !truthy(thread.ReviewOf) && !waitSet[id] }
		merged, done := []string{}, []string{}
		for _, id := range landed {
			if tells(id) {
				merged = append(merged, id)
			}
		}
		for _, id := range reported {
			if tells(id) && !seenLanded[id] && byID[id].Stage == "done" {
				done = append(done, id)
			}
		}
		if len(merged)+len(done)+len(waiting)+len(asks) == 0 {
			return nil
		}
		return appendChanges(tx, projectID, []change{{"user.returned", object{"since": since, "done": done, "merged": merged, "waiting": waiting, "asks": asks}}})
	})
}
