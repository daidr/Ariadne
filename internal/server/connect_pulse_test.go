package server

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/eventstore"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
	"github.com/daidr/Ariadne/internal/realtime"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/internal/sources"
)

func TestProjectServiceGetPulseRPC(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	home := t.TempDir()
	nowMs := int64(1_850_000_000_000)
	now := func() time.Time { return time.UnixMilli(nowMs) }

	repo, err := core.Open(ctx, core.Options{Path: ":memory:", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	settingsStore, err := settings.Open(filepath.Join(home, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := settingsStore.UpdateConcurrency(object{"slots": float64(5), "reserved": float64(1)}); err != nil {
		t.Fatal(err)
	}

	hub := realtime.NewHub()
	defer hub.Close()
	state := realtime.NewState(hub)
	defer state.Close()
	live := realtime.NewLive(state, realtime.LiveOptions{Now: now, Interval: time.Millisecond})
	defer live.Close()

	api, err := New(Options{
		Repository: repo,
		Settings:   settingsStore,
		Sources:    sources.New(),
		State:      state,
		Token:      "pulse-token",
		Now:        now,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()

	srv := httptest.NewServer(api)
	defer srv.Close()

	anon := ariadnev1connect.NewProjectServiceClient(srv.Client(), srv.URL+rpcPrefix)
	if _, err := anon.GetPulse(ctx, connect.NewRequest(&ariadnev1.GetPulseRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("unauthenticated GetPulse err = %v, want CodeUnauthenticated", err)
	}

	projects := ariadnev1connect.NewProjectServiceClient(
		srv.Client(),
		srv.URL+rpcPrefix,
		connect.WithInterceptors(bearerClientInterceptor("pulse-token")),
	)

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

	base := nowMs
	appendEv(base, "prj_a", "project.created", map[string]any{"name": "Alpha", "coordinatorId": "crd_a"})
	appendEv(base, "prj_b", "project.created", map[string]any{"name": "Beta", "coordinatorId": "crd_b"})
	appendEv(base+1_000, "prj_a", "thread.created", map[string]any{"threadId": "thr_a1", "title": "跑测试"})
	appendEv(base+2_000, "prj_a", "turn.started", map[string]any{"conversationId": "thr_a1", "turnId": "trn_a1", "model": "m"})
	appendEv(base+2_500, "prj_a", "thread.created", map[string]any{"threadId": "thr_done", "title": "已完成任务"})
	appendEv(base+2_600, "prj_a", "turn.started", map[string]any{"conversationId": "thr_done", "turnId": "trn_d1", "model": "m"})
	appendEv(base+3_000, "prj_a", "turn.finished", map[string]any{"conversationId": "thr_done", "turnId": "trn_d1", "outcome": "completed"})
	appendEv(base+3_100, "prj_a", "thread.completed", map[string]any{"threadId": "thr_done"})
	appendEv(base+4_000, "prj_b", "thread.created", map[string]any{"threadId": "thr_b1", "title": "跨项目工作"})
	appendEv(base+4_500, "prj_b", "turn.started", map[string]any{"conversationId": "thr_b1", "turnId": "trn_b1", "model": "m"})
	appendEv(base+5_000, "prj_a", "user.returned", map[string]any{"since": base + 1_500, "done": []any{}, "merged": []any{}, "waiting": []any{}, "asks": []any{}})

	// Attach an active live command to thr_a1 in realtime.State.
	nowMs = base + 6_000
	handle, err := live.Start(realtime.LiveCommand{
		ProjectID:      "prj_a",
		ConversationID: "thr_a1",
		Command:        "go test ./...",
		CWD:            ".",
	})
	if err != nil {
		t.Fatal(err)
	}
	handle.Output("ok  github.com/daidr/Ariadne/internal/core 0.5s\n")
	// Wait briefly for throttled live tail flush.
	time.Sleep(15 * time.Millisecond)

	// 1. Single-project query (include_completed=false).
	singleResp, err := projects.GetPulse(ctx, connect.NewRequest(&ariadnev1.GetPulseRequest{
		ProjectId:        "prj_a",
		FromMs:           base,
		ToMs:             nowMs,
		IncludeCompleted: false,
	}))
	if err != nil {
		t.Fatalf("GetPulse prj_a: %v", err)
	}
	msg := singleResp.Msg
	if msg.Concurrency == nil || msg.Concurrency.Slots != 5 || msg.Concurrency.Peak != 2 || msg.Concurrency.Current != 1 {
		t.Fatalf("prj_a concurrency = %+v, want slots=5 peak=2 current=1", msg.Concurrency)
	}
	if msg.LastReturnedAt == nil || *msg.LastReturnedAt != base+1_500 {
		t.Fatalf("last_returned_at = %v, want %d", msg.LastReturnedAt, base+1_500)
	}
	if msg.CursorSeq <= 0 || msg.ServerNowMs != nowMs {
		t.Fatalf("cursor/now invalid: cursor=%d serverNow=%d", msg.CursorSeq, msg.ServerNowMs)
	}
	if len(msg.Lanes) != 1 || msg.Lanes[0].ConversationId != "thr_a1" {
		t.Fatalf("expected only active thr_a1 lane, got %+v", msg.Lanes)
	}
	lane := msg.Lanes[0]
	if lane.LiveCommand == nil || *lane.LiveCommand != "go test ./..." {
		t.Fatalf("lane.LiveCommand = %v, want go test ./...", lane.LiveCommand)
	}
	if lane.LiveTail == nil || !strings.Contains(*lane.LiveTail, "internal/core") {
		t.Fatalf("lane.LiveTail = %v, want output containing internal/core", lane.LiveTail)
	}
	// Open working span should inherit command fallback label when DB label is empty.
	foundLiveSpan := false
	for _, span := range msg.Spans {
		if span.ConversationId == "thr_a1" && span.EndedAt == nil && span.Stage == core.PulseStageWorking {
			foundLiveSpan = true
			if span.Label != "go test ./..." {
				t.Fatalf("open working span label = %q, want %q", span.Label, "go test ./...")
			}
		}
	}
	if !foundLiveSpan {
		t.Fatalf("missing open working span for thr_a1: %+v", msg.Spans)
	}

	// 2. All active projects query (project_id = "", include_completed = true).
	allResp, err := projects.GetPulse(ctx, connect.NewRequest(&ariadnev1.GetPulseRequest{
		ProjectId:        "",
		FromMs:           base,
		ToMs:             nowMs,
		IncludeCompleted: true,
	}))
	if err != nil {
		t.Fatalf("GetPulse all projects: %v", err)
	}
	if allResp.Msg.Concurrency.Slots != 5 || allResp.Msg.Concurrency.Current != 2 {
		t.Fatalf("global concurrency = %+v, want slots=5 current=2", allResp.Msg.Concurrency)
	}
	seenProjects := map[string]bool{}
	seenDone := false
	for _, l := range allResp.Msg.Lanes {
		seenProjects[l.ProjectId] = true
		if l.ConversationId == "thr_done" && l.Completed {
			seenDone = true
		}
	}
	if !seenProjects["prj_a"] || !seenProjects["prj_b"] || !seenDone {
		t.Fatalf("global lanes missing expected entries: projects=%+v seenDone=%v lanes=%+v", seenProjects, seenDone, allResp.Msg.Lanes)
	}

	// 3. Error mapping checks.
	if _, err := projects.GetPulse(ctx, connect.NewRequest(&ariadnev1.GetPulseRequest{
		ProjectId: "prj_unknown",
	})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown project err = %v, want CodeNotFound", err)
	}
	if _, err := projects.GetPulse(ctx, connect.NewRequest(&ariadnev1.GetPulseRequest{
		FromMs: base + 10_000,
		ToMs:   base + 1_000,
	})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("invalid window err = %v, want CodeInvalidArgument", err)
	}
}
