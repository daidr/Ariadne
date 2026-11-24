package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/daidr/Ariadne/internal/background"
	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/eventstore"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
	"github.com/daidr/Ariadne/internal/realtime"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/internal/update"
	"github.com/daidr/Ariadne/sdk"
)

func TestEventServiceStreamEventsLiveAndUpdateStatus(t *testing.T) {
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(t.TempDir(), "events.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	hub := realtime.NewHub()
	defer hub.Close()
	state := realtime.NewState(hub)
	defer state.Close()

	// Seed an initial durable event and an initial update status snapshot.
	if _, err := repo.Store.Append(t.Context(), []eventstore.NewEvent{
		{Type: "project.created", ProjectID: "prj_1", Data: json.RawMessage(`{"name":"One","coordinatorId":"crd_1"}`)},
	}); err != nil {
		t.Fatal(err)
	}
	initialUpdate := update.View{
		Version:         "1.0.0",
		Preferences:     settings.UpdatePreferences{Channel: "stable", Automatic: true},
		CanInstall:      true,
		State:           "downloading",
		DownloadPercent: 25,
	}
	initialUpdateRaw, _ := json.Marshal(initialUpdate)
	if err := state.Set(realtime.Update{Kind: "update", Data: initialUpdateRaw}, true); err != nil {
		t.Fatal(err)
	}

	api, err := New(Options{Repository: repo, Token: "stream-secret", State: state})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	srv := httptest.NewServer(api)
	defer srv.Close()

	client := ariadnev1connect.NewEventServiceClient(
		srv.Client(),
		srv.URL+rpcPrefix,
		connect.WithInterceptors(bearerClientInterceptor("stream-secret")),
	)

	// 1. Global subscriber (empty project_id) receives ready, replayed event, initial updateStatus, and live pushes.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	zero := int64(0)
	stream, err := client.StreamEvents(ctx, connect.NewRequest(&ariadnev1.StreamEventsRequest{
		AfterSeq: &zero,
	}))
	if err != nil {
		t.Fatal(err)
	}

	// Frame 1: ready
	if !stream.Receive() || stream.Msg().GetReady() == nil {
		t.Fatalf("expected ready frame, msg=%+v err=%v", stream.Msg(), stream.Err())
	}

	// Next two frames: replayed event + initial updateStatus (order: event replay then initial snapshots).
	if !stream.Receive() {
		t.Fatalf("expected replayed event, err=%v", stream.Err())
	}
	ev := stream.Msg().GetEvent()
	if ev == nil || ev.GetSeq() != 1 || ev.GetType() != "project.created" || !strings.Contains(string(ev.GetRawJson()), `"One"`) {
		t.Fatalf("unexpected replayed event: %+v", stream.Msg())
	}
	if !stream.Receive() {
		t.Fatalf("expected initial updateStatus, err=%v", stream.Err())
	}
	up := stream.Msg().GetUpdateStatus()
	if up == nil || up.GetState() != "downloading" || up.GetDownloadPercent() != 25 {
		t.Fatalf("unexpected initial updateStatus: %+v", stream.Msg())
	}

	// Push live durable event and live update progress.
	if _, err := repo.Store.Append(t.Context(), []eventstore.NewEvent{
		{Type: "project.renamed", ProjectID: "prj_1", Data: json.RawMessage(`{"name":"Renamed"}`)},
	}); err != nil {
		t.Fatal(err)
	}

	if !stream.Receive() {
		t.Fatalf("expected live event, err=%v", stream.Err())
	}
	ev2 := stream.Msg().GetEvent()
	if ev2 == nil || ev2.GetSeq() != 2 || ev2.GetType() != "project.renamed" {
		t.Fatalf("unexpected live event: %+v", stream.Msg())
	}

	nextUpdate := initialUpdate
	nextUpdate.State = "ready"
	nextUpdate.DownloadPercent = 100
	nextUpdateRaw, _ := json.Marshal(nextUpdate)
	if err := state.Set(realtime.Update{Kind: "update", Data: nextUpdateRaw}, true); err != nil {
		t.Fatal(err)
	}
	if !stream.Receive() {
		t.Fatalf("expected live updateStatus, err=%v", stream.Err())
	}
	up2 := stream.Msg().GetUpdateStatus()
	if up2 == nil || up2.GetState() != "ready" {
		t.Fatalf("unexpected live updateStatus: %+v", stream.Msg())
	}

	// Push a cleared live-command update (command == nil).
	clearedRaw, _ := json.Marshal(realtime.LiveUpdate{ProjectID: "prj_1", ConversationID: "crd_1", Command: nil})
	if err := state.Set(realtime.Update{Kind: "live", ProjectID: "prj_1", ConversationID: "crd_1", Data: clearedRaw}, false); err != nil {
		t.Fatal(err)
	}
	if !stream.Receive() {
		t.Fatalf("expected cleared live update, err=%v", stream.Err())
	}
	liveCleared := stream.Msg().GetLive()
	if liveCleared == nil || liveCleared.GetProjectId() != "prj_1" || liveCleared.GetCommand() != nil {
		t.Fatalf("unexpected cleared live update: %+v", stream.Msg())
	}

	cancel()
	_ = stream.Close()

	// 2. Project-scoped subscriber does NOT receive global updateStatus or other-project events.
	ctx2, cancel2 := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel2()
	prj2 := "prj_2"
	stream2, err := client.StreamEvents(ctx2, connect.NewRequest(&ariadnev1.StreamEventsRequest{
		AfterSeq:  &zero,
		ProjectId: &prj2,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !stream2.Receive() || stream2.Msg().GetReady() == nil {
		t.Fatalf("expected ready on project stream, err=%v", stream2.Err())
	}
	// Publish an event for prj_1 (must be skipped) and then for prj_2 (must be received).
	if _, err := repo.Store.Append(t.Context(), []eventstore.NewEvent{
		{Type: "project.renamed", ProjectID: "prj_1", Data: json.RawMessage(`{"name":"SkipMe"}`)},
		{Type: "project.created", ProjectID: "prj_2", Data: json.RawMessage(`{"name":"Two","coordinatorId":"crd_2"}`)},
	}); err != nil {
		t.Fatal(err)
	}

	if !stream2.Receive() {
		t.Fatalf("expected prj_2 event, err=%v", stream2.Err())
	}
	evPrj2 := stream2.Msg().GetEvent()
	if evPrj2 == nil || evPrj2.GetProjectId() != "prj_2" || evPrj2.GetType() != "project.created" {
		t.Fatalf("project-scoped stream received wrong frame: %+v", stream2.Msg())
	}
	cancel2()
	_ = stream2.Close()
}

func TestThreadServiceStreamCommandOutput(t *testing.T) {
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(t.TempDir(), "cmdstream.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	commands := background.New(background.Options{Now: func() time.Time { return time.UnixMilli(1_800_000_000_000) }})
	defer commands.Close(context.Background())

	firstWritten := make(chan struct{})
	emitSecond := make(chan struct{})
	if _, err := commands.Start("thr_1", "build", func(ctx context.Context, options sdk.ExecOptions) (sdk.ExecResult, error) {
		options.OnOutput("step 1\n", "stdout")
		close(firstWritten)
		select {
		case <-emitSecond:
		case <-ctx.Done():
			return sdk.ExecResult{ExitCode: 130}, nil
		}
		options.OnOutput("step 2\n", "stdout")
		return sdk.ExecResult{ExitCode: 0}, nil
	}, background.StartOptions{Local: true}); err != nil {
		t.Fatal(err)
	}
	<-firstWritten

	api, err := New(Options{Repository: repo, Token: "cmd-secret", Background: commands})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	srv := httptest.NewServer(api)
	defer srv.Close()

	// Unauthenticated request fails with CodeUnauthenticated.
	unauth := ariadnev1connect.NewThreadServiceClient(srv.Client(), srv.URL+rpcPrefix)
	unauthStream, err := unauth.StreamCommandOutput(t.Context(), connect.NewRequest(&ariadnev1.StreamCommandOutputRequest{
		ThreadId:  "thr_1",
		CommandId: "cmd-1",
	}))
	if err == nil {
		if unauthStream.Receive() {
			t.Fatal("unauthenticated StreamCommandOutput yielded a frame")
		}
		err = unauthStream.Err()
		_ = unauthStream.Close()
	}
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("expected CodeUnauthenticated, got %v", err)
	}

	client := ariadnev1connect.NewThreadServiceClient(
		srv.Client(),
		srv.URL+rpcPrefix,
		connect.WithInterceptors(bearerClientInterceptor("cmd-secret")),
	)

	// Missing command fails with CodeNotFound.
	missingStream, err := client.StreamCommandOutput(t.Context(), connect.NewRequest(&ariadnev1.StreamCommandOutputRequest{
		ThreadId:  "thr_1",
		CommandId: "missing",
	}))
	if err == nil {
		if missingStream.Receive() {
			t.Fatal("missing command yielded a frame")
		}
		err = missingStream.Err()
		_ = missingStream.Close()
	}
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("expected CodeNotFound for missing command, got %v", err)
	}

	// Valid command streams initial output, live update, and completes on exit.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	stream, err := client.StreamCommandOutput(ctx, connect.NewRequest(&ariadnev1.StreamCommandOutputRequest{
		ThreadId:  "thr_1",
		CommandId: "cmd-1",
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	if !stream.Receive() {
		t.Fatalf("expected initial command output frame, err=%v", stream.Err())
	}
	first := stream.Msg()
	if first.GetCommand().GetId() != "cmd-1" || !strings.Contains(first.GetTail(), "step 1") || first.GetCommand().GetResult() != nil {
		t.Fatalf("unexpected initial frame: %+v", first)
	}

	close(emitSecond)

	var lastTail string
	var gotResult *ariadnev1.BackgroundCommandResult
	for stream.Receive() {
		lastTail = stream.Msg().GetTail()
		if r := stream.Msg().GetCommand().GetResult(); r != nil {
			gotResult = r
		}
	}
	if err := stream.Err(); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected stream error: %v", err)
	}
	if !strings.Contains(lastTail, "step 2") {
		t.Fatalf("final tail missing step 2: %q", lastTail)
	}
	if gotResult == nil || gotResult.GetExitCode() != 0 {
		t.Fatalf("expected exit code 0 result on completion, got %+v", gotResult)
	}
}

type deadlineRecorderResponseWriter struct {
	httptest.ResponseRecorder
	deadlines []time.Time
}

func (w *deadlineRecorderResponseWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadlines = append(w.deadlines, deadline)
	return nil
}

func TestStreamWriteGuardPreservesDeadlineOnErrorAndSlowClose(t *testing.T) {
	rw := &deadlineRecorderResponseWriter{ResponseRecorder: *httptest.NewRecorder()}
	guard := &streamWriteGuard{rc: http.NewResponseController(rw)}

	// 1. Successful Send arms a finite deadline, then clears it to zero.
	if err := guard.Send(func() error { return nil }); err != nil {
		t.Fatalf("successful Send returned error: %v", err)
	}
	if len(rw.deadlines) != 2 || rw.deadlines[0].IsZero() || !rw.deadlines[1].IsZero() {
		t.Fatalf("unexpected deadlines after successful Send: %+v", rw.deadlines)
	}

	// 2. ArmCloseDeadline after successful Send arms a finite close deadline (e.g. ErrSlowConsumer).
	guard.ArmCloseDeadline()
	if len(rw.deadlines) != 3 || rw.deadlines[2].IsZero() {
		t.Fatalf("expected finite close deadline after ArmCloseDeadline, got %+v", rw.deadlines)
	}

	// 3. Failed Send leaves the active deadline intact (never clears to zero), and
	//    subsequent ArmCloseDeadline does not extend the already-failed deadline.
	rw2 := &deadlineRecorderResponseWriter{ResponseRecorder: *httptest.NewRecorder()}
	guard2 := &streamWriteGuard{rc: http.NewResponseController(rw2)}
	sendErr := errors.New("i/o timeout")
	if err := guard2.Send(func() error { return sendErr }); !errors.Is(err, sendErr) {
		t.Fatalf("expected sendErr, got %v", err)
	}
	guard2.ArmCloseDeadline()
	if len(rw2.deadlines) != 1 || rw2.deadlines[0].IsZero() {
		t.Fatalf("failed Send must keep single finite deadline without clearing or extending, got %+v", rw2.deadlines)
	}
}

func TestEventServiceStreamEventsIncrementalDrafts(t *testing.T) {
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(t.TempDir(), "drafts.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	hub := realtime.NewHub()
	defer hub.Close()
	state := realtime.NewState(hub)
	defer state.Close()
	drafts := realtime.NewDrafts(state, realtime.LiveOptions{
		Now:      func() time.Time { return time.UnixMilli(1_800_000_000_000) },
		Interval: 10 * time.Millisecond,
	})
	defer drafts.Close()

	api, err := New(Options{Repository: repo, Token: "draft-secret", State: state})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	srv := httptest.NewServer(api)
	defer srv.Close()

	client := ariadnev1connect.NewEventServiceClient(
		srv.Client(),
		srv.URL+rpcPrefix,
		connect.WithInterceptors(bearerClientInterceptor("draft-secret")),
	)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	stream, err := client.StreamEvents(ctx, connect.NewRequest(&ariadnev1.StreamEventsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if !stream.Receive() || stream.Msg().GetReady() == nil {
		t.Fatalf("expected ready frame, err=%v", stream.Err())
	}

	handle, err := drafts.Start("prj_1", "thr_1", "msg_1")
	if err != nil {
		t.Fatal(err)
	}
	blocks1 := json.RawMessage(`[{"type":"steps","items":[]}]`)
	blocks2 := json.RawMessage(`[{"type":"confirm","title":"Go?"}]`)

	// 1. Initial frame is full (append=false).
	if err := handle.WriteUI("你好", blocks1); err != nil {
		t.Fatal(err)
	}
	if !stream.Receive() {
		t.Fatalf("expected initial draft frame, err=%v", stream.Err())
	}
	d1 := stream.Msg().GetDraft().GetDraft()
	if d1 == nil || d1.GetAppend() || d1.GetKeepBlocks() || d1.GetText() != "你好" || string(d1.GetBlocksJson()) != string(blocks1) {
		t.Fatalf("unexpected initial draft: %+v", d1)
	}

	// 2. Prefix append with unchanged blocks sends only the delta and keep_blocks=true.
	if err := handle.WriteUI("你好，世界", blocks1); err != nil {
		t.Fatal(err)
	}
	if !stream.Receive() {
		t.Fatalf("expected incremental draft frame, err=%v", stream.Err())
	}
	d2 := stream.Msg().GetDraft().GetDraft()
	if d2 == nil || !d2.GetAppend() || !d2.GetKeepBlocks() || d2.GetText() != "，世界" || len(d2.GetBlocksJson()) != 0 {
		t.Fatalf("unexpected incremental draft: %+v", d2)
	}

	// 3. Prefix append with changed blocks sends delta + new blocks_json with keep_blocks=false.
	if err := handle.WriteUI("你好，世界！", blocks2); err != nil {
		t.Fatal(err)
	}
	if !stream.Receive() {
		t.Fatalf("expected blocks-changed draft frame, err=%v", stream.Err())
	}
	d3 := stream.Msg().GetDraft().GetDraft()
	if d3 == nil || !d3.GetAppend() || d3.GetKeepBlocks() || d3.GetText() != "！" || string(d3.GetBlocksJson()) != string(blocks2) {
		t.Fatalf("unexpected blocks-changed draft: %+v", d3)
	}

	// 4. Mid-stream reconnecting client receives the full snapshot (append=false).
	ctx2, cancel2 := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel2()
	stream2, err := client.StreamEvents(ctx2, connect.NewRequest(&ariadnev1.StreamEventsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if !stream2.Receive() || stream2.Msg().GetReady() == nil {
		t.Fatalf("expected ready on reconnect stream, err=%v", stream2.Err())
	}
	if !stream2.Receive() {
		t.Fatalf("expected full reconnect draft snapshot, err=%v", stream2.Err())
	}
	snap := stream2.Msg().GetDraft().GetDraft()
	if snap == nil || snap.GetAppend() || snap.GetText() != "你好，世界！" || string(snap.GetBlocksJson()) != string(blocks2) {
		t.Fatalf("unexpected reconnect draft snapshot: %+v", snap)
	}
	cancel2()
	_ = stream2.Close()

	// 5. Non-prefix rewrite falls back to a full frame (append=false).
	if err := handle.WriteUI("重写后的文本", blocks2); err != nil {
		t.Fatal(err)
	}
	if !stream.Receive() {
		t.Fatalf("expected full rewrite frame, err=%v", stream.Err())
	}
	d4 := stream.Msg().GetDraft().GetDraft()
	if d4 == nil || d4.GetAppend() || d4.GetText() != "重写后的文本" || string(d4.GetBlocksJson()) != string(blocks2) {
		t.Fatalf("unexpected rewrite draft frame: %+v", d4)
	}

	// 6. Posted emits the final incremental delta with posted=true.
	if err := handle.WriteUI("重写后的文本。", blocks2); err != nil {
		t.Fatal(err)
	}
	handle.Posted()
	if !stream.Receive() {
		t.Fatalf("expected posted draft frame, err=%v", stream.Err())
	}
	d5 := stream.Msg().GetDraft().GetDraft()
	if d5 == nil || !d5.GetAppend() || !d5.GetKeepBlocks() || !d5.GetPosted() || d5.GetText() != "。" {
		t.Fatalf("unexpected posted draft frame: %+v", d5)
	}

	cancel()
	_ = stream.Close()
}
