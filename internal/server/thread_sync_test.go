package server

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/eventstore"
)

func TestThreadSyncChangedSeqIncrementalAndInvalidCursors(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000)
	repo, err := core.Open(t.Context(), core.Options{
		Path: filepath.Join(t.TempDir(), "sync.sqlite"),
		Now:  func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	appendEvent := func(kind string, data map[string]any) {
		t.Helper()
		raw, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repo.Store.Append(t.Context(), []eventstore.NewEvent{{
			Type: kind, ProjectID: "prj_a", Data: raw,
		}}); err != nil {
			t.Fatal(err)
		}
	}

	appendEvent("project.created", map[string]any{"name": "测试项目", "coordinatorId": "crd_a"})
	appendEvent("thread.created", map[string]any{"threadId": "thr_a", "title": "A"})
	appendEvent("thread.created", map[string]any{"threadId": "thr_b", "title": "B"})

	syncer := NewThreadSync()
	defer syncer.Close()

	// First request without cursor returns full snapshot.
	_, first, err := syncer.ProjectDetail(t.Context(), repo.Reader, "prj_a", "")
	if err != nil {
		t.Fatal(err)
	}
	if first.Partial || len(first.Threads) != 2 || first.IDs != nil {
		t.Fatalf("first: %+v", first)
	}

	// Unchanged request returns empty partial delta without ids.
	_, same, err := syncer.ProjectDetail(t.Context(), repo.Reader, "prj_a", first.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if !same.Partial || len(same.Threads) != 0 || same.IDs != nil {
		t.Fatalf("same: %+v", same)
	}

	// Mutating only thr_a returns only thr_a in partial delta.
	appendEvent("thread.renamed", map[string]any{"threadId": "thr_a", "title": "A2"})
	_, changed, err := syncer.ProjectDetail(t.Context(), repo.Reader, "prj_a", first.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if !changed.Partial || len(changed.Threads) != 1 || changed.Threads[0].ID != "thr_a" || changed.Threads[0].Title != "A2" || changed.IDs != nil {
		t.Fatalf("changed: %+v", changed)
	}

	// Creating a new thread returns the new thread AND updated ids list.
	appendEvent("thread.created", map[string]any{"threadId": "thr_c", "title": "C"})
	_, added, err := syncer.ProjectDetail(t.Context(), repo.Reader, "prj_a", changed.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if !added.Partial || len(added.Threads) != 1 || added.Threads[0].ID != "thr_c" || added.IDs == nil || !reflect.DeepEqual(*added.IDs, []string{"thr_a", "thr_b", "thr_c"}) {
		t.Fatalf("added: %+v", added)
	}

	// Opening and answering an attention touches the thread's changed_seq and keeps needsMe ID-referenced.
	appendEvent("attention.opened", map[string]any{
		"attentionId": "att_1",
		"threadId":    "thr_b",
		"block":       map[string]any{"type": "confirm", "title": "继续？"},
	})
	detailWithAtt, withAtt, err := syncer.ProjectDetail(t.Context(), repo.Reader, "prj_a", added.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if !withAtt.Partial || len(withAtt.Threads) != 1 || withAtt.Threads[0].ID != "thr_b" || withAtt.Threads[0].Stage != "answer" {
		t.Fatalf("withAtt: %+v", withAtt)
	}
	if len(detailWithAtt.NeedsMe) != 1 || detailWithAtt.NeedsMe[0]["kind"] != "attention" || detailWithAtt.NeedsMe[0]["threadId"] != "thr_b" {
		t.Fatalf("needsMe: %+v", detailWithAtt.NeedsMe)
	}
	if _, hasEmbedded := detailWithAtt.NeedsMe[0]["thread"]; hasEmbedded {
		t.Fatalf("needsMe still embeds full thread: %+v", detailWithAtt.NeedsMe[0])
	}

	// Invalid, foreign-epoch, malformed, or future cursors fall back to full snapshots.
	for _, cursor := range []string{
		"",
		"other.0.0000000000000000.0",
		syncer.epoch + ".999999999999.0000000000000000.0",
		syncer.epoch + ".bad.0000000000000000.0",
		syncer.epoch + ".1",
	} {
		_, result, err := syncer.ProjectDetail(t.Context(), repo.Reader, "prj_a", cursor)
		if err != nil {
			t.Fatal(err)
		}
		if result.Partial || len(result.Threads) != 3 {
			t.Fatalf("cursor %q should return full snapshot: %+v", cursor, result)
		}
	}

	// Advancing time into another hour keeps the incremental partial path when no thread crosses an archive boundary.
	now = now.Add(2 * time.Hour)
	_, afterHour, err := syncer.ProjectDetail(t.Context(), repo.Reader, "prj_a", withAtt.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if !afterHour.Partial || len(afterHour.Threads) != 0 || afterHour.IDs != nil {
		t.Fatalf("afterHour should stay incremental with empty delta: %+v", afterHour)
	}

	// Completing a thread and advancing past 7 days across hour/day boundaries returns only that archived thread in partial mode.
	appendEvent("thread.reported", map[string]any{"threadId": "thr_c", "report": "done", "headline": "完成", "summary": "ok", "artifacts": []string{}})
	_, reported, err := syncer.ProjectDetail(t.Context(), repo.Reader, "prj_a", afterHour.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if !reported.Partial || len(reported.Threads) != 1 || reported.Threads[0].ID != "thr_c" || reported.Threads[0].Archived {
		t.Fatalf("reported: %+v", reported)
	}
	now = now.Add(7*24*time.Hour + time.Hour)
	_, archived, err := syncer.ProjectDetail(t.Context(), repo.Reader, "prj_a", reported.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if !archived.Partial || len(archived.Threads) != 1 || archived.Threads[0].ID != "thr_c" || !archived.Threads[0].Archived {
		t.Fatalf("archived across hour/day boundary should return only thr_c in partial delta: %+v", archived)
	}
}
