package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/eventstore"
	"github.com/daidr/Ariadne/internal/schema"
)

func TestPulseProjectionStagesAndCoalescing(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	nowMs := int64(1_800_000_000_000)
	now := func() time.Time { return time.UnixMilli(nowMs) }

	repo, err := Open(ctx, Options{Path: ":memory:", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	appendEv := func(at int64, projectID, evType string, data map[string]any) eventstore.Event {
		t.Helper()
		nowMs = at
		raw, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		events, err := repo.Store.Append(ctx, []eventstore.NewEvent{{
			Type:      evType,
			ProjectID: projectID,
			Data:      raw,
		}})
		if err != nil {
			t.Fatalf("append %s: %v", evType, err)
		}
		return events[0]
	}

	base := int64(1_800_000_000_000)
	appendEv(base, "prj_a", "project.created", map[string]any{"name": "Pulse 项目", "coordinatorId": "crd_a"})
	// Coordinator starts and finishes a turn: must NOT occupy a global worker slot.
	appendEv(base+1_000, "prj_a", "turn.started", map[string]any{"conversationId": "crd_a", "turnId": "trn_c1", "model": "m"})
	appendEv(base+5_000, "prj_a", "turn.finished", map[string]any{"conversationId": "crd_a", "turnId": "trn_c1", "outcome": "completed"})

	// Worker created -> queued.
	appendEv(base+10_000, "prj_a", "thread.created", map[string]any{"threadId": "thr_1", "title": "后端投影"})
	// Worker starts working -> occupies_slot=true.
	appendEv(base+12_000, "prj_a", "turn.started", map[string]any{"conversationId": "thr_1", "turnId": "trn_1", "model": "m"})
	// todo.updated updates active working span label.
	appendEv(base+13_000, "prj_a", "todo.updated", map[string]any{
		"conversationId": "thr_1",
		"items":          []any{map[string]any{"label": "运行单元测试", "status": "doing"}},
	})
	// Turn finishes -> idle (closes working span at base+20_000).
	appendEv(base+20_000, "prj_a", "turn.finished", map[string]any{"conversationId": "thr_1", "turnId": "trn_1", "outcome": "completed"})
	// Worker re-enters working within 1.5s (<= 2s) -> must coalesce with previous working span!
	appendEv(base+21_500, "prj_a", "turn.started", map[string]any{"conversationId": "thr_1", "turnId": "trn_2", "model": "m"})
	appendEv(base+25_000, "prj_a", "turn.finished", map[string]any{"conversationId": "thr_1", "turnId": "trn_2", "outcome": "completed"})
	// Re-enter working after 3s (> 2s) -> must start a distinct working span!
	appendEv(base+28_000, "prj_a", "turn.started", map[string]any{"conversationId": "thr_1", "turnId": "trn_3", "model": "m"})
	// Attention opened -> answer stage.
	appendEv(base+30_000, "prj_a", "attention.opened", map[string]any{
		"attentionId":    "att_1",
		"threadId":       "thr_1",
		"conversationId": "thr_1",
		"block":          map[string]any{"type": "confirm", "title": "确认执行数据库迁移？"},
	})
	appendEv(base+30_500, "prj_a", "turn.finished", map[string]any{"conversationId": "thr_1", "turnId": "trn_3", "outcome": "completed"})
	// Attention answered -> wakes thread (queued).
	appendEv(base+40_000, "prj_a", "attention.answered", map[string]any{
		"attentionId": "att_1",
		"answer":      map[string]any{"type": "confirm", "approved": true},
	})
	// Worker runs again and sets a wait -> waiting stage.
	appendEv(base+42_000, "prj_a", "turn.started", map[string]any{"conversationId": "thr_1", "turnId": "trn_4", "model": "m"})
	appendEv(base+43_000, "prj_a", "turn.step", map[string]any{"conversationId": "thr_1", "turnId": "trn_4", "sawSeq": 100, "text": "等待远端部署完成", "finish": "tool-calls"})
	appendEv(base+44_000, "prj_a", "conversation.wait_set", map[string]any{"conversationId": "thr_1", "waitId": "wait_1", "until": base + 90_000, "note": "等待 CI 回调"})
	appendEv(base+45_000, "prj_a", "turn.finished", map[string]any{"conversationId": "thr_1", "turnId": "trn_4", "outcome": "completed"})
	// Wait ends -> queued -> working -> opens PR with pending CI -> ci stage.
	appendEv(base+50_000, "prj_a", "conversation.wait_ended", map[string]any{"conversationId": "thr_1", "waitId": "wait_1"})
	appendEv(base+51_000, "prj_a", "turn.started", map[string]any{"conversationId": "thr_1", "turnId": "trn_5", "model": "m"})
	appendEv(base+52_000, "prj_a", "turn.step", map[string]any{"conversationId": "thr_1", "turnId": "trn_5", "sawSeq": 100, "text": "", "finish": "stop"})
	appendEv(base+53_000, "prj_a", "pull_request.synced", map[string]any{
		"threadId": "thr_1", "number": 42, "title": "Pulse 切片 A", "state": "open", "headSha": "abc",
		"checks": map[string]any{"state": "pending"},
	})
	appendEv(base+54_000, "prj_a", "thread.reported", map[string]any{"threadId": "thr_1", "report": "ready", "headline": "准备审阅", "summary": "done", "artifacts": []any{}})
	appendEv(base+55_000, "prj_a", "turn.finished", map[string]any{"conversationId": "thr_1", "turnId": "trn_5", "outcome": "completed"})
	// CI passes -> review stage.
	appendEv(base+65_000, "prj_a", "pull_request.synced", map[string]any{
		"threadId": "thr_1", "number": 42, "title": "Pulse 切片 A", "state": "open", "headSha": "abc",
		"checks": map[string]any{"state": "success"},
	})
	// Thread set aside -> aside stage.
	appendEv(base+70_000, "prj_a", "thread.set_aside", map[string]any{"threadId": "thr_1", "until": base + 200_000, "reason": "等产品确认", "by": "user"})
	// Thread stopped -> halted stage (open).
	appendEv(base+75_000, "prj_a", "thread.stopped", map[string]any{"threadId": "thr_1", "reason": "发现阻断缺陷", "by": "user"})

	nowMs = base + 80_000
	snap, err := repo.GetPulse(ctx, PulseQuery{
		ProjectID:        "prj_a",
		FromMs:           base,
		ToMs:             nowMs,
		IncludeCompleted: true,
		GlobalSlots:      3,
	})
	if err != nil {
		t.Fatalf("GetPulse: %v", err)
	}

	var coordSpans, workerSpans []PulseSpan
	for _, s := range snap.Spans {
		switch s.ConversationID {
		case "crd_a":
			coordSpans = append(coordSpans, s)
		case "thr_1":
			workerSpans = append(workerSpans, s)
		}
	}
	if len(coordSpans) == 0 || coordSpans[0].Stage != PulseStageWorking || coordSpans[0].OccupiesSlot {
		t.Fatalf("coordinator span should be working with occupies_slot=false, got %+v", coordSpans)
	}

	type wantSpan struct {
		stage    string
		occupies bool
		start    int64
		end      int64 // -1 means open
		label    string
	}
	expected := []wantSpan{
		{PulseStageQueued, false, base + 10_000, base + 12_000, ""},
		// Coalesced [base+12_000 .. base+20_000] + [base+21_500 .. base+25_000]
		{PulseStageWorking, true, base + 12_000, base + 25_000, "运行单元测试"},
		// New span after 3s gap (> 2s)
		{PulseStageWorking, true, base + 28_000, base + 30_000, ""},
		{PulseStageAnswer, false, base + 30_000, base + 40_000, "确认执行数据库迁移？"},
		{PulseStageQueued, false, base + 40_000, base + 42_000, ""},
		{PulseStageWorking, true, base + 42_000, base + 45_000, "等待远端部署完成"},
		{PulseStageWaiting, false, base + 45_000, base + 50_000, "等待 CI 回调"},
		{PulseStageQueued, false, base + 50_000, base + 51_000, ""},
		{PulseStageWorking, true, base + 51_000, base + 55_000, ""},
		{PulseStageCI, false, base + 55_000, base + 65_000, "PR #42"},
		{PulseStageReview, false, base + 65_000, base + 70_000, "准备审阅"},
		{PulseStageAside, false, base + 70_000, base + 75_000, "等产品确认"},
		{PulseStageHalted, false, base + 75_000, -1, "发现阻断缺陷"},
	}
	if len(workerSpans) != len(expected) {
		t.Fatalf("worker spans count = %d, want %d:\ngot=%+v", len(workerSpans), len(expected), workerSpans)
	}
	for i, want := range expected {
		got := workerSpans[i]
		if got.Stage != want.stage || got.OccupiesSlot != want.occupies || got.StartedAt != want.start {
			t.Fatalf("span[%d] = %+v, want stage=%s occupies=%v start=%d", i, got, want.stage, want.occupies, want.start)
		}
		if want.end < 0 {
			if got.EndedAt != nil {
				t.Fatalf("span[%d] expected open EndedAt, got %d", i, *got.EndedAt)
			}
		} else if got.EndedAt == nil || *got.EndedAt != want.end {
			t.Fatalf("span[%d] EndedAt = %v, want %d", i, got.EndedAt, want.end)
		}
		if want.label != "" && got.Label != want.label {
			t.Fatalf("span[%d] Label = %q, want %q", i, got.Label, want.label)
		}
	}

	// Coordinator's working span must not count in concurrency.
	if snap.Concurrency.Peak != 1 || snap.Concurrency.Current != 0 {
		t.Fatalf("concurrency = %+v, want peak=1 current=0", snap.Concurrency)
	}
}

func TestPulseMarkersHandoffsAndReturnLine(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	nowMs := int64(1_800_000_000_000)
	now := func() time.Time { return time.UnixMilli(nowMs) }

	repo, err := Open(ctx, Options{Path: ":memory:", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	appendEv := func(at int64, evType string, data map[string]any) {
		t.Helper()
		nowMs = at
		raw, _ := json.Marshal(data)
		if _, err := repo.Store.Append(ctx, []eventstore.NewEvent{{
			Type:      evType,
			ProjectID: "prj_a",
			Data:      raw,
		}}); err != nil {
			t.Fatalf("append %s: %v", evType, err)
		}
	}

	base := int64(1_800_000_000_000)
	appendEv(base, "project.created", map[string]any{"name": "协作项目", "coordinatorId": "crd_a"})
	appendEv(base+1_000, "thread.created", map[string]any{"threadId": "thr_author", "title": "实现功能"})
	appendEv(base+2_000, "turn.started", map[string]any{"conversationId": "thr_author", "turnId": "trn_a1", "model": "m"})
	appendEv(base+3_000, "artifact.created", map[string]any{
		"artifactId": "art_1", "kind": "document", "title": "设计文档", "content": "v1", "author": "thr_author",
	})
	appendEv(base+4_000, "artifact.revised", map[string]any{
		"artifactId": "art_1", "title": "设计文档 v2", "version": 2, "content": "v2", "author": "thr_author", "reason": "补充细节", "quotes": map[string]any{},
	})
	appendEv(base+5_000, "pull_request.synced", map[string]any{
		"threadId": "thr_author", "number": 108, "title": "Add Pulse", "state": "open", "headSha": "s1",
		"checks": map[string]any{"state": "pending"},
	})
	// Repeated sync for the same open PR must NOT duplicate pr_opened marker.
	appendEv(base+6_000, "pull_request.synced", map[string]any{
		"threadId": "thr_author", "number": 108, "title": "Add Pulse", "state": "open", "headSha": "s1",
		"checks": map[string]any{"state": "failure"},
	})
	appendEv(base+6_100, "checks.failed", map[string]any{
		"threadId": "thr_author", "number": 108, "headSha": "s1", "attempt": 1,
		"failed": []any{map[string]any{"name": "go test"}},
	})
	appendEv(base+7_000, "turn.finished", map[string]any{"conversationId": "thr_author", "turnId": "trn_a1", "outcome": "completed"})

	// Create reviewer and hand off review.
	appendEv(base+8_000, "thread.created", map[string]any{"threadId": "thr_rev", "title": "代码审阅", "reviewOf": "thr_author"})
	appendEv(base+8_100, "review.requested", map[string]any{"threadId": "thr_author", "reviewerId": "thr_rev", "round": 1, "head": "s1"})
	appendEv(base+9_000, "turn.started", map[string]any{"conversationId": "thr_rev", "turnId": "trn_r1", "model": "m"})
	appendEv(base+11_000, "review.submitted", map[string]any{
		"threadId": "thr_author", "reviewerId": "thr_rev", "round": 1, "verdict": "pass", "summary": "测试完备",
	})
	appendEv(base+11_100, "thread.reported", map[string]any{"threadId": "thr_rev", "report": "done", "headline": "通过", "summary": "通过", "artifacts": []any{}})
	appendEv(base+11_200, "turn.finished", map[string]any{"conversationId": "thr_rev", "turnId": "trn_r1", "outcome": "completed"})
	appendEv(base+12_000, "user.returned", map[string]any{"since": base + 3_000, "done": []any{}, "merged": []any{}, "waiting": []any{}, "asks": []any{}})
	appendEv(base+15_000, "pull_request.closed", map[string]any{"threadId": "thr_author", "number": 108, "merged": true})

	nowMs = base + 20_000
	snap, err := repo.GetPulse(ctx, PulseQuery{
		ProjectID:        "prj_a",
		FromMs:           base,
		ToMs:             nowMs,
		IncludeCompleted: false,
	})
	if err != nil {
		t.Fatalf("GetPulse: %v", err)
	}

	// Reviewer finished ("done"), but author is not completed -> reviewer lane stays attached for handoff edges.
	var authorIdx, revIdx = -1, -1
	for i, lane := range snap.Lanes {
		if lane.ConversationID == "thr_author" {
			authorIdx = i
		}
		if lane.ConversationID == "thr_rev" {
			revIdx = i
			if lane.Role != "reviewer" || lane.ReviewOf == nil || *lane.ReviewOf != "thr_author" {
				t.Fatalf("unexpected reviewer lane: %+v", lane)
			}
		}
	}
	if authorIdx < 0 || revIdx != authorIdx+1 {
		t.Fatalf("expected reviewer lane immediately after author lane: authorIdx=%d revIdx=%d lanes=%+v", authorIdx, revIdx, snap.Lanes)
	}

	kindCounts := map[string]int{}
	for _, m := range snap.Markers {
		kindCounts[m.Kind]++
	}
	if kindCounts[PulseMarkerPROpened] != 1 {
		t.Fatalf("pr_opened count = %d, want 1", kindCounts[PulseMarkerPROpened])
	}
	if kindCounts[PulseMarkerCIFailed] != 1 {
		t.Fatalf("ci_failed count = %d, want 1", kindCounts[PulseMarkerCIFailed])
	}
	if kindCounts[PulseMarkerArtifactUpdated] != 2 {
		t.Fatalf("artifact_updated count = %d, want 2", kindCounts[PulseMarkerArtifactUpdated])
	}
	if kindCounts[PulseMarkerReviewHandoff] != 1 || kindCounts[PulseMarkerReviewSubmitted] != 1 {
		t.Fatalf("review markers = %+v", kindCounts)
	}
	if kindCounts[PulseMarkerPRMerged] != 1 || kindCounts[PulseMarkerUserReturned] != 1 {
		t.Fatalf("merge/return markers = %+v", kindCounts)
	}
	if snap.LastReturnedAt == nil || *snap.LastReturnedAt != base+3_000 {
		t.Fatalf("LastReturnedAt = %v, want %d", snap.LastReturnedAt, base+3_000)
	}

	// Verify fallback to event timestamp when user.returned omits since.
	appendEv(base+16_000, "user.returned", map[string]any{"done": []any{}, "merged": []any{}, "waiting": []any{}, "asks": []any{}})
	snap2, err := repo.GetPulse(ctx, PulseQuery{
		ProjectID:        "prj_a",
		FromMs:           base,
		ToMs:             nowMs,
		IncludeCompleted: false,
	})
	if err != nil {
		t.Fatalf("GetPulse after fallback return: %v", err)
	}
	if snap2.LastReturnedAt == nil || *snap2.LastReturnedAt != base+16_000 {
		t.Fatalf("fallback LastReturnedAt = %v, want %d", snap2.LastReturnedAt, base+16_000)
	}

	if len(snap.Handoffs) != 2 {
		t.Fatalf("handoffs = %+v, want 2", snap.Handoffs)
	}
	if snap.Handoffs[0].FromConversationID != "thr_author" || snap.Handoffs[0].ToConversationID != "thr_rev" || snap.Handoffs[0].Kind != PulseMarkerReviewHandoff {
		t.Fatalf("handoffs[0] = %+v", snap.Handoffs[0])
	}
	if snap.Handoffs[1].FromConversationID != "thr_rev" || snap.Handoffs[1].ToConversationID != "thr_author" || snap.Handoffs[1].Kind != PulseMarkerReviewSubmitted {
		t.Fatalf("handoffs[1] = %+v", snap.Handoffs[1])
	}
}

func TestPulseConcurrencySweepLineAndMultiProject(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	nowMs := int64(1_800_000_000_000)
	now := func() time.Time { return time.UnixMilli(nowMs) }

	repo, err := Open(ctx, Options{Path: ":memory:", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	appendEv := func(at int64, projectID, evType string, data map[string]any) {
		t.Helper()
		nowMs = at
		raw, _ := json.Marshal(data)
		if _, err := repo.Store.Append(ctx, []eventstore.NewEvent{{
			Type:      evType,
			ProjectID: projectID,
			Data:      raw,
		}}); err != nil {
			t.Fatalf("append %s: %v", evType, err)
		}
	}

	base := int64(1_800_000_000_000)
	appendEv(base, "prj_a", "project.created", map[string]any{"name": "Alpha", "coordinatorId": "crd_a"})
	appendEv(base, "prj_b", "project.created", map[string]any{"name": "Beta", "coordinatorId": "crd_b"})
	appendEv(base, "prj_a", "thread.created", map[string]any{"threadId": "thr_a1", "title": "A1"})
	appendEv(base, "prj_a", "thread.created", map[string]any{"threadId": "thr_a2", "title": "A2"})
	appendEv(base, "prj_b", "thread.created", map[string]any{"threadId": "thr_b1", "title": "B1"})

	// Coordinator running concurrently should NOT occupy slot.
	appendEv(base+500, "prj_a", "turn.started", map[string]any{"conversationId": "crd_a", "turnId": "trn_c", "model": "m"})
	// A1: [1000, 5000], then completed.
	appendEv(base+1_000, "prj_a", "turn.started", map[string]any{"conversationId": "thr_a1", "turnId": "trn_a1", "model": "m"})
	// A2: [2000, 6000].
	appendEv(base+2_000, "prj_a", "turn.started", map[string]any{"conversationId": "thr_a2", "turnId": "trn_a2", "model": "m"})
	// B1: [3000, open].
	appendEv(base+3_000, "prj_b", "turn.started", map[string]any{"conversationId": "thr_b1", "turnId": "trn_b1", "model": "m"})
	appendEv(base+5_000, "prj_a", "turn.finished", map[string]any{"conversationId": "thr_a1", "turnId": "trn_a1", "outcome": "completed"})
	appendEv(base+5_100, "prj_a", "thread.completed", map[string]any{"threadId": "thr_a1"})
	appendEv(base+6_000, "prj_a", "turn.finished", map[string]any{"conversationId": "thr_a2", "turnId": "trn_a2", "outcome": "completed"})

	nowMs = base + 7_000

	// Global query with IncludeCompleted=false: hides thr_a1 lane/spans, yet concurrency peak is still 3!
	globalSnap, err := repo.GetPulse(ctx, PulseQuery{
		ProjectID:        "",
		FromMs:           base,
		ToMs:             nowMs,
		IncludeCompleted: false,
		GlobalSlots:      4,
	})
	if err != nil {
		t.Fatalf("GetPulse global: %v", err)
	}
	if globalSnap.Concurrency.Slots != 4 || globalSnap.Concurrency.Peak != 3 || globalSnap.Concurrency.Current != 1 {
		t.Fatalf("global concurrency = %+v, want slots=4 peak=3 current=1", globalSnap.Concurrency)
	}
	wantPts := []PulseConcurrencyPoint{
		{At: base, Active: 0},
		{At: base + 1_000, Active: 1},
		{At: base + 2_000, Active: 2},
		{At: base + 3_000, Active: 3},
		{At: base + 5_000, Active: 2},
		{At: base + 6_000, Active: 1},
		{At: base + 7_000, Active: 1},
	}
	if len(globalSnap.Concurrency.Points) != len(wantPts) {
		t.Fatalf("points = %+v, want %+v", globalSnap.Concurrency.Points, wantPts)
	}
	for i := range wantPts {
		if globalSnap.Concurrency.Points[i] != wantPts[i] {
			t.Fatalf("point[%d] = %+v, want %+v", i, globalSnap.Concurrency.Points[i], wantPts[i])
		}
	}
	for _, lane := range globalSnap.Lanes {
		if lane.ConversationID == "thr_a1" {
			t.Fatalf("completed thr_a1 should be filtered when IncludeCompleted=false")
		}
	}

	// IncludeCompleted=true reveals thr_a1.
	withCompleted, err := repo.GetPulse(ctx, PulseQuery{
		ProjectID:        "prj_a",
		FromMs:           base,
		ToMs:             nowMs,
		IncludeCompleted: true,
		GlobalSlots:      4,
	})
	if err != nil {
		t.Fatalf("GetPulse prj_a: %v", err)
	}
	if withCompleted.Concurrency.Peak != 2 || withCompleted.Concurrency.Current != 0 {
		t.Fatalf("prj_a concurrency = %+v, want peak=2 current=0", withCompleted.Concurrency)
	}
	foundA1 := false
	for _, lane := range withCompleted.Lanes {
		if lane.ProjectID != "prj_a" {
			t.Fatalf("single project query leaked project %s", lane.ProjectID)
		}
		if lane.ConversationID == "thr_a1" && lane.Completed {
			foundA1 = true
		}
	}
	if !foundA1 {
		t.Fatalf("expected completed thr_a1 lane when IncludeCompleted=true, got %+v", withCompleted.Lanes)
	}

	// Error cases.
	var domErr *domainerr.Error
	if _, err := repo.GetPulse(ctx, PulseQuery{ProjectID: "prj_missing"}); !errors.As(err, &domErr) || domErr.Key != "error.notFound" {
		t.Fatalf("missing project err = %v, want error.notFound", err)
	}
	domErr = nil
	if _, err := repo.GetPulse(ctx, PulseQuery{FromMs: base + 10_000, ToMs: base + 5_000}); !errors.As(err, &domErr) || domErr.Key != "error.invalidBody" {
		t.Fatalf("invalid window err = %v, want error.invalidBody", err)
	}
}

func TestPulsePrune30DayRollingRetention(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	dayMs := int64(24 * time.Hour / time.Millisecond)
	nowMs := int64(1_900_000_000_000)
	now := func() time.Time { return time.UnixMilli(nowMs) }

	repo, err := Open(ctx, Options{Path: ":memory:", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	appendEv := func(at int64, evType string, data map[string]any) {
		t.Helper()
		nowMs = at
		raw, _ := json.Marshal(data)
		if _, err := repo.Store.Append(ctx, []eventstore.NewEvent{{
			Type:      evType,
			ProjectID: "prj_a",
			Data:      raw,
		}}); err != nil {
			t.Fatalf("append %s: %v", evType, err)
		}
	}

	tNow := int64(1_900_000_000_000)
	tOld := tNow - 35*dayMs
	tRecent := tNow - 5*dayMs

	appendEv(tOld, "project.created", map[string]any{"name": "Retention", "coordinatorId": "crd_a"})
	appendEv(tOld+1_000, "thread.created", map[string]any{"threadId": "thr_old", "title": "过期任务"})
	appendEv(tOld+2_000, "turn.started", map[string]any{"conversationId": "thr_old", "turnId": "trn_old", "model": "m"})
	appendEv(tOld+5_000, "turn.finished", map[string]any{"conversationId": "thr_old", "turnId": "trn_old", "outcome": "completed"})
	appendEv(tOld+6_000, "artifact.created", map[string]any{"artifactId": "art_old", "kind": "document", "title": "旧文档", "content": "x", "author": "thr_old"})

	// Long-lived open span created 35 days ago must survive pruning.
	appendEv(tOld+10_000, "thread.created", map[string]any{"threadId": "thr_open", "title": "长期未答"})
	appendEv(tOld+11_000, "attention.opened", map[string]any{
		"attentionId": "att_old", "threadId": "thr_open", "conversationId": "thr_open",
		"block": map[string]any{"type": "confirm", "title": "请确认"},
	})

	// Recent activity within 30 days must survive pruning.
	appendEv(tRecent, "thread.created", map[string]any{"threadId": "thr_new", "title": "近期任务"})
	appendEv(tRecent+1_000, "turn.started", map[string]any{"conversationId": "thr_new", "turnId": "trn_new", "model": "m"})
	appendEv(tRecent+5_000, "turn.finished", map[string]any{"conversationId": "thr_new", "turnId": "trn_new", "outcome": "completed"})
	appendEv(tRecent+6_000, "artifact.created", map[string]any{"artifactId": "art_new", "kind": "document", "title": "新文档", "content": "y", "author": "thr_new"})

	nowMs = tNow
	pruned, err := repo.PrunePulse(ctx, 0)
	if err != nil {
		t.Fatalf("PrunePulse: %v", err)
	}
	if pruned.SpansDeleted < 2 || pruned.MarkersDeleted < 2 {
		t.Fatalf("expected old spans & markers deleted, got %+v", pruned)
	}

	snap, err := repo.GetPulse(ctx, PulseQuery{
		ProjectID:        "prj_a",
		FromMs:           tOld,
		ToMs:             tNow,
		IncludeCompleted: true,
	})
	if err != nil {
		t.Fatalf("GetPulse after prune: %v", err)
	}

	hasOpen, hasRecentSpan, hasOldSpan := false, false, false
	for _, s := range snap.Spans {
		if s.ConversationID == "thr_open" && s.EndedAt == nil && s.Stage == PulseStageAnswer {
			hasOpen = true
		}
		if s.ConversationID == "thr_new" && s.Stage == PulseStageWorking {
			hasRecentSpan = true
		}
		if s.ConversationID == "thr_old" {
			hasOldSpan = true
		}
	}
	if !hasOpen || !hasRecentSpan || hasOldSpan {
		t.Fatalf("unexpected spans after prune: hasOpen=%v hasRecent=%v hasOld=%v spans=%+v", hasOpen, hasRecentSpan, hasOldSpan, snap.Spans)
	}
	if len(snap.Markers) != 1 || snap.Markers[0].Label != "新文档" {
		t.Fatalf("markers after prune = %+v, want only 新文档", snap.Markers)
	}

	// Verify PulsePruner start/stop lifecycle runs cleanly.
	pruner := &PulsePruner{Repository: repo, Interval: time.Hour}
	pruner.Start(ctx)
	stopCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := pruner.Stop(stopCtx); err != nil {
		t.Fatalf("PulsePruner.Stop: %v", err)
	}
}

func TestPulseForgetAndRebuild(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	nowMs := int64(1_850_000_000_000)
	now := func() time.Time { return time.UnixMilli(nowMs) }

	repo, err := Open(ctx, Options{Path: ":memory:", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	appendEv := func(at int64, projectID, evType string, data map[string]any) {
		t.Helper()
		nowMs = at
		raw, _ := json.Marshal(data)
		if _, err := repo.Store.Append(ctx, []eventstore.NewEvent{{
			Type:      evType,
			ProjectID: projectID,
			Data:      raw,
		}}); err != nil {
			t.Fatal(err)
		}
	}

	base := nowMs
	appendEv(base, "prj_a", "project.created", map[string]any{"name": "Keep", "coordinatorId": "crd_a"})
	appendEv(base, "prj_b", "project.created", map[string]any{"name": "Drop", "coordinatorId": "crd_b"})
	appendEv(base+1_000, "prj_a", "thread.created", map[string]any{"threadId": "thr_a", "title": "保留"})
	appendEv(base+2_000, "prj_a", "turn.started", map[string]any{"conversationId": "thr_a", "turnId": "t1", "model": "m"})
	appendEv(base+1_000, "prj_b", "thread.created", map[string]any{"threadId": "thr_b", "title": "删除"})
	appendEv(base+2_000, "prj_b", "turn.started", map[string]any{"conversationId": "thr_b", "turnId": "t2", "model": "m"})
	appendEv(base+2_500, "prj_b", "artifact.created", map[string]any{"artifactId": "art_b", "kind": "document", "title": "将删", "content": "x", "author": "thr_b"})
	appendEv(base+3_000, "prj_b", "turn.finished", map[string]any{"conversationId": "thr_b", "turnId": "t2", "outcome": "completed"})

	if err := repo.DeleteProject(ctx, "prj_b"); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}

	var spansB, markersB int
	if err := repo.Store.DB().QueryRowContext(ctx, "SELECT count(*) FROM pulse_spans WHERE project_id = 'prj_b'").Scan(&spansB); err != nil || spansB != 0 {
		t.Fatalf("prj_b spans after purge = %d (err=%v)", spansB, err)
	}
	if err := repo.Store.DB().QueryRowContext(ctx, "SELECT count(*) FROM pulse_markers WHERE project_id = 'prj_b'").Scan(&markersB); err != nil || markersB != 0 {
		t.Fatalf("prj_b markers after purge = %d (err=%v)", markersB, err)
	}

	// Rebuild from event log preserves prj_a Pulse projection.
	if err := repo.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	snap, err := repo.GetPulse(ctx, PulseQuery{ProjectID: "prj_a", FromMs: base, ToMs: base + 10_000, IncludeCompleted: true})
	if err != nil {
		t.Fatalf("GetPulse after rebuild: %v", err)
	}
	if len(snap.Spans) != 2 || snap.Spans[1].Stage != PulseStageWorking || snap.Spans[1].EndedAt != nil {
		t.Fatalf("unexpected spans after rebuild: %+v", snap.Spans)
	}
}

func TestSchemaV74UpgradeBackfillsPulseLast30Days(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "v74.sqlite")
	dayMs := int64(24 * time.Hour / time.Millisecond)
	tNow := int64(1_900_000_000_000)
	tOld := tNow - 40*dayMs
	tRecent := tNow - 3*dayMs

	nowMs := tOld
	now := func() time.Time { return time.UnixMilli(nowMs) }

	repo, err := Open(ctx, Options{Path: path, Now: now})
	if err != nil {
		t.Fatal(err)
	}

	appendEv := func(at int64, evType string, data map[string]any) {
		t.Helper()
		nowMs = at
		raw, _ := json.Marshal(data)
		if _, err := repo.Store.Append(ctx, []eventstore.NewEvent{{
			Type:      evType,
			ProjectID: "prj_mig",
			Data:      raw,
		}}); err != nil {
			t.Fatal(err)
		}
	}

	appendEv(tOld, "project.created", map[string]any{"name": "升级回填", "coordinatorId": "crd_mig"})
	// 40-day-old finished span & marker -> outside 30-day backfill window.
	appendEv(tOld+1_000, "thread.created", map[string]any{"threadId": "thr_ancient", "title": "远古任务"})
	appendEv(tOld+2_000, "turn.started", map[string]any{"conversationId": "thr_ancient", "turnId": "t_old", "model": "m"})
	appendEv(tOld+5_000, "turn.finished", map[string]any{"conversationId": "thr_ancient", "turnId": "t_old", "outcome": "completed"})
	appendEv(tOld+6_000, "artifact.created", map[string]any{"artifactId": "art_ancient", "kind": "document", "title": "远古文档", "content": "old", "author": "thr_ancient"})

	// 40-day-old attention STILL open today -> should be reconciled as open answer span!
	appendEv(tOld+10_000, "thread.created", map[string]any{"threadId": "thr_still_open", "title": "一直待答"})
	appendEv(tOld+11_000, "attention.opened", map[string]any{
		"attentionId": "att_open", "threadId": "thr_still_open", "conversationId": "thr_still_open",
		"block": map[string]any{"type": "confirm", "title": "遗留确认"},
	})

	// 3-day-old activity -> inside 30-day backfill window.
	appendEv(tRecent, "thread.created", map[string]any{"threadId": "thr_recent", "title": "近期工作"})
	appendEv(tRecent+1_000, "turn.started", map[string]any{"conversationId": "thr_recent", "turnId": "t_rec", "model": "m"})
	appendEv(tRecent+2_000, "pull_request.synced", map[string]any{
		"threadId": "thr_recent", "number": 7, "title": "Recent PR", "state": "open", "headSha": "h1",
		"checks": map[string]any{"state": "pending"},
	})
	appendEv(tRecent+4_000, "turn.finished", map[string]any{"conversationId": "thr_recent", "turnId": "t_rec", "outcome": "completed"})
	appendEv(tRecent+5_000, "thread.created", map[string]any{"threadId": "thr_rev", "title": "审阅者", "reviewOf": "thr_recent"})
	appendEv(tRecent+5_100, "review.requested", map[string]any{"threadId": "thr_recent", "reviewerId": "thr_rev", "round": 1, "head": "h1"})
	appendEv(tRecent+6_000, "user.returned", map[string]any{"since": tRecent + 2_000, "done": []any{}, "merged": []any{}, "waiting": []any{}, "asks": []any{}})

	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}

	// Simulate an existing v74 database lacking pulse_spans and pulse_markers.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		"DROP TABLE IF EXISTS pulse_spans",
		"DROP TABLE IF EXISTS pulse_markers",
		"PRAGMA user_version = 74",
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	nowMs = tNow
	upgraded, err := Open(ctx, Options{Path: path, Now: now})
	if err != nil {
		t.Fatalf("upgrade v74 -> v75: %v", err)
	}
	defer upgraded.Close()

	var version int
	if err := upgraded.Store.DB().QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil || version != schema.Version {
		t.Fatalf("user_version = %d, want %d (err=%v)", version, schema.Version, err)
	}

	snap, err := upgraded.GetPulse(ctx, PulseQuery{
		ProjectID:        "prj_mig",
		FromMs:           tOld,
		ToMs:             tNow,
		IncludeCompleted: true,
	})
	if err != nil {
		t.Fatalf("GetPulse after upgrade: %v", err)
	}

	var hasAncient, hasRecentWork, hasStillOpen bool
	for _, s := range snap.Spans {
		switch s.ConversationID {
		case "thr_ancient":
			hasAncient = true
		case "thr_recent":
			if s.Stage == PulseStageWorking && s.OccupiesSlot {
				hasRecentWork = true
			}
		case "thr_still_open":
			if s.Stage == PulseStageAnswer && s.EndedAt == nil {
				hasStillOpen = true
			}
		}
	}
	if hasAncient {
		t.Fatalf("40-day-old finished thr_ancient should not be backfilled: %+v", snap.Spans)
	}
	if !hasRecentWork {
		t.Fatalf("expected 3-day-old thr_recent working span backfilled: %+v", snap.Spans)
	}
	if !hasStillOpen {
		t.Fatalf("expected still-open thr_still_open reconciled as open answer span: %+v", snap.Spans)
	}
	if snap.LastReturnedAt == nil || *snap.LastReturnedAt != tRecent+2_000 {
		t.Fatalf("LastReturnedAt after backfill = %v, want %d", snap.LastReturnedAt, tRecent+2_000)
	}
	if len(snap.Handoffs) != 1 || snap.Handoffs[0].Kind != PulseMarkerReviewHandoff {
		t.Fatalf("handoffs after backfill = %+v, want 1 review_handoff", snap.Handoffs)
	}
	for _, m := range snap.Markers {
		if m.Label == "远古文档" {
			t.Fatalf("40-day-old marker should not be backfilled: %+v", snap.Markers)
		}
	}
}
