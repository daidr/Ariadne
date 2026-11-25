package scheduler

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/eventstore"
)

func intPointer(value int) *int { return &value }
func testRepository(t *testing.T) *core.Repository {
	t.Helper()
	repo, err := core.Open(t.Context(), core.Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repo.Close() })
	return repo
}
func project(t *testing.T, repo *core.Repository, name string) core.ProjectView {
	t.Helper()
	value, err := repo.CreateProject(t.Context(), &name, core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	return value
}
func post(t *testing.T, repo *core.Repository, projectID, id, author string) {
	t.Helper()
	input := core.MessageInput{ConversationID: id, Author: author, Content: "执行"}
	if author == "coordinator" {
		input.Kind = "relay"
	}
	if _, err := repo.PostMessage(t.Context(), projectID, input); err != nil {
		t.Fatal(err)
	}
}
func worker(t *testing.T, repo *core.Repository, projectID, author string) core.ThreadView {
	t.Helper()
	thread, err := repo.CreateThread(t.Context(), projectID, core.NewThread{Title: "工作"})
	if err != nil {
		t.Fatal(err)
	}
	post(t, repo, projectID, thread.ID, author)
	return thread
}

type started struct {
	id, turn string
	previous any
}

func blockedRunner(repo *core.Repository, starts chan<- started) func(context.Context, core.ConversationView) error {
	return func(ctx context.Context, conversation core.ConversationView) error {
		turn, err := repo.StartTurn(ctx, conversation.ID, "model")
		if err != nil {
			return err
		}
		select {
		case starts <- started{conversation.ID, turn, conversation.LastOutcome}:
		case <-ctx.Done():
		}
		<-ctx.Done()
		return ctx.Err()
	}
}
func start(t *testing.T, repo *core.Repository, slots, reserved int, run func(context.Context, core.ConversationView) error) *Scheduler {
	t.Helper()
	scheduler, err := New(Options{Repository: repo, Slots: intPointer(slots), ReservedSlots: intPointer(reserved), Run: run,
		OnError: func(err error) { t.Error(err) }})
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := scheduler.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	return scheduler
}
func await(t *testing.T, starts <-chan started, count int) map[string]started {
	t.Helper()
	result := map[string]started{}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for len(result) < count {
		select {
		case value := <-starts:
			result[value.id] = value
		case <-timer.C:
			t.Fatalf("only %d of %d turns started: %v", len(result), count, result)
		}
	}
	return result
}
func eventually(t *testing.T, predicate func() bool) {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if predicate() {
			return
		}
		select {
		case <-timer.C:
			t.Fatal("condition did not become true")
		case <-ticker.C:
		}
	}
}

func TestCoordinatorsStartWithEveryWorkerSlotOccupied(t *testing.T) {
	t.Parallel()
	repo := testRepository(t)
	a, b := project(t, repo, "A"), project(t, repo, "B")
	if _, err := repo.SetProjectSlots(t.Context(), a.ID, intPointer(1)); err != nil {
		t.Fatal(err)
	}
	general := worker(t, repo, a.ID, "coordinator")
	reserved := worker(t, repo, a.ID, "user")
	queued := worker(t, repo, a.ID, "coordinator")
	starts := make(chan started, 10)
	scheduler := start(t, repo, 1, 1, blockedRunner(repo, starts))
	initial := await(t, starts, 3)
	if initial[general.ID].id == "" || initial[reserved.ID].id == "" || scheduler.IsRunning(queued.ID) {
		t.Fatal(initial)
	}
	if !scheduler.Cancel(a.CoordinatorID) {
		t.Fatal("initial triage did not start")
	}
	eventually(t, func() bool { return !scheduler.IsRunning(a.CoordinatorID) })
	post(t, repo, a.ID, a.CoordinatorID, "user")
	report, err := repo.CreateThread(t.Context(), b.ID, core.NewThread{Title: "汇报"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ReportThread(t.Context(), report.ID, core.ThreadReport{Kind: "waiting", Headline: "关键信息", Summary: "需要立即协调"}, nil, core.ReviewOptions{}); err != nil {
		t.Fatal(err)
	}
	coordinators := await(t, starts, 2)
	if coordinators[a.CoordinatorID].id == "" || coordinators[b.CoordinatorID].id == "" || scheduler.IsRunning(queued.ID) {
		t.Fatal(coordinators)
	}
	post(t, repo, a.ID, a.CoordinatorID, "user")
	conversation, err := repo.GetConversation(t.Context(), a.CoordinatorID)
	if err != nil {
		t.Fatal(err)
	}
	if conversation.Turn == nil || conversation.Turn.ID != coordinators[a.CoordinatorID].turn {
		t.Fatal("parallel coordinator turn")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := scheduler.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	running, err := repo.ListRunningConversations(t.Context())
	if err != nil || len(running) != 0 {
		t.Fatalf("running turns after stop: %v %v", running, err)
	}
	for id := range initial {
		conversation, err := repo.GetConversation(t.Context(), id)
		if err != nil || conversation.LastOutcome != "interrupted" {
			t.Fatalf("%+v %v", conversation, err)
		}
	}
}
func TestProjectWorkerCapAndRuntimeSlotChanges(t *testing.T) {
	t.Parallel()
	repo := testRepository(t)
	p := project(t, repo, "P")
	if _, err := repo.SetProjectSlots(t.Context(), p.ID, intPointer(1)); err != nil {
		t.Fatal(err)
	}
	post(t, repo, p.ID, p.CoordinatorID, "user")
	a, b := worker(t, repo, p.ID, "coordinator"), worker(t, repo, p.ID, "coordinator")
	starts := make(chan started, 10)
	scheduler := start(t, repo, 2, 0, blockedRunner(repo, starts))
	initial := await(t, starts, 2)
	if initial[a.ID].id == "" || initial[p.CoordinatorID].id == "" || scheduler.IsRunning(b.ID) {
		t.Fatal(initial)
	}
	if _, err := repo.SetProjectSlots(t.Context(), p.ID, nil); err != nil {
		t.Fatal(err)
	}
	if next := await(t, starts, 1); next[b.ID].id == "" {
		t.Fatal(next)
	}
	c := worker(t, repo, p.ID, "coordinator")
	if err := scheduler.SetSlots(3, 0); err != nil {
		t.Fatal(err)
	}
	if next := await(t, starts, 1); next[c.ID].id == "" {
		t.Fatal(next)
	}
}
func TestHeldProjectInterruptsAndResumesCoordinatorWithZeroWorkerSlots(t *testing.T) {
	t.Parallel()
	repo := testRepository(t)
	p := project(t, repo, "P")
	post(t, repo, p.ID, p.CoordinatorID, "user")
	starts := make(chan started, 10)
	scheduler := start(t, repo, 0, 0, blockedRunner(repo, starts))
	initial := await(t, starts, 1)[p.CoordinatorID]
	release, err := scheduler.HoldProject(t.Context(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := scheduler.HoldProject(t.Context(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := repo.GetConversation(t.Context(), p.CoordinatorID)
	if err != nil || conversation.Turn != nil || conversation.LastOutcome != "interrupted" {
		t.Fatalf("%+v %v", conversation, err)
	}
	release()
	release()
	post(t, repo, p.ID, p.CoordinatorID, "user")
	scheduler.mu.Lock()
	held := scheduler.held[p.ID]
	scheduler.mu.Unlock()
	if held != 1 || scheduler.IsRunning(p.CoordinatorID) {
		t.Fatal("nested hold released early")
	}
	second()
	next := await(t, starts, 1)[p.CoordinatorID]
	if next.id == "" || next.turn == initial.turn {
		t.Fatal(next)
	}
	if !scheduler.Cancel(p.CoordinatorID) {
		t.Fatal("cancel failed")
	}
	eventually(t, func() bool { return !scheduler.IsRunning(p.CoordinatorID) })
	conversation, err = repo.GetConversation(t.Context(), p.CoordinatorID)
	if err != nil || conversation.LastOutcome != "cancelled" {
		t.Fatalf("%+v %v", conversation, err)
	}
}
func TestRecoveryAndUnhandledRunnerFailure(t *testing.T) {
	t.Parallel()
	repo := testRepository(t)
	p := project(t, repo, "P")
	post(t, repo, p.ID, p.CoordinatorID, "user")
	old, err := repo.StartTurn(t.Context(), p.CoordinatorID, "old")
	if err != nil {
		t.Fatal(err)
	}
	starts := make(chan started, 10)
	scheduler := start(t, repo, 0, 0, blockedRunner(repo, starts))
	next := await(t, starts, 1)[p.CoordinatorID]
	if next.previous != "interrupted" || next.turn == old {
		t.Fatal(next)
	}
	if err := scheduler.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var errorsSeen []error
	failing, err := New(Options{Repository: repo, Run: func(context.Context, core.ConversationView) error { panic("runner failed") }, OnError: func(err error) { mu.Lock(); errorsSeen = append(errorsSeen, err); mu.Unlock() }})
	if err != nil {
		t.Fatal(err)
	}
	if err := failing.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer failing.Stop(t.Context())
	eventually(t, func() bool {
		conversation, err := repo.GetConversation(t.Context(), p.CoordinatorID)
		return err == nil && conversation.LastOutcome == "failed"
	})
	if err := failing.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(errorsSeen) != 1 {
		t.Fatal(errorsSeen)
	}
}

func TestCancellationBeforeModelTurnDoesNotBecomeNoModelOrRestart(t *testing.T) {
	t.Parallel()
	repo := testRepository(t)
	p := project(t, repo, "P")
	post(t, repo, p.ID, p.CoordinatorID, "user")
	entered := make(chan struct{})
	scheduler := start(t, repo, 0, 0, func(ctx context.Context, _ core.ConversationView) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	})
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not enter")
	}
	if !scheduler.Cancel(p.CoordinatorID) {
		t.Fatal("cancel failed")
	}
	eventually(t, func() bool { return !scheduler.IsRunning(p.CoordinatorID) })
	conversation, err := repo.GetConversation(t.Context(), p.CoordinatorID)
	if err != nil || conversation.LastOutcome != "cancelled" || conversation.LastError != nil || conversation.PendingSeq != 0 {
		t.Fatalf("%+v %v", conversation, err)
	}
}

func TestSchedulerDrainBlocksNewTurnsAndWaitIdle(t *testing.T) {
	t.Parallel()
	repo := testRepository(t)
	p := project(t, repo, "Drain")
	post(t, repo, p.ID, p.CoordinatorID, "user")

	entered := make(chan started, 4)
	release := make(chan struct{})
	var mu sync.Mutex
	activeChanges := []int{}

	sched, err := New(Options{
		Repository:    repo,
		Slots:         intPointer(2),
		ReservedSlots: intPointer(0),
		Run: func(ctx context.Context, conversation core.ConversationView) error {
			turn, err := repo.StartTurn(ctx, conversation.ID, "model")
			if err != nil {
				return err
			}
			entered <- started{conversation.ID, turn, conversation.LastOutcome}
			if conversation.ID == p.CoordinatorID {
				<-release
			}
			return repo.FinishTurn(ctx, conversation.ID, turn, "cancelled", nil)
		},
		OnError: func(err error) { t.Error(err) },
	})
	if err != nil {
		t.Fatal(err)
	}
	sched.SetOnActiveChange(func(count int) {
		mu.Lock()
		activeChanges = append(activeChanges, count)
		mu.Unlock()
	})
	if err := sched.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = sched.Stop(ctx)
	})

	await(t, entered, 1)
	if sched.ActiveCount() != 1 {
		t.Fatalf("expected 1 active turn, got %d", sched.ActiveCount())
	}
	if sched.TryDrainIfIdle(DrainSafePoint) {
		t.Fatal("TryDrainIfIdle must fail while a turn is running")
	}

	sched.BeginDrain(DrainSafePoint)
	if sched.DrainMode() != DrainSafePoint {
		t.Fatalf("expected drain mode %q, got %q", DrainSafePoint, sched.DrainMode())
	}

	// Queue a second worker while draining: it must not start yet.
	second := worker(t, repo, p.ID, "user")
	select {
	case startedTurn := <-entered:
		t.Fatalf("turn %s started while draining", startedTurn.id)
	case <-time.After(150 * time.Millisecond):
	}

	waitDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		waitDone <- sched.WaitIdle(ctx)
	}()

	close(release)
	if err := <-waitDone; err != nil {
		t.Fatalf("WaitIdle failed: %v", err)
	}
	if sched.ActiveCount() != 0 {
		t.Fatalf("expected 0 active turns after WaitIdle, got %d", sched.ActiveCount())
	}

	// Cancelling drain must immediately schedule the waiting worker.
	sched.CancelDrain()
	if sched.DrainMode() != "" {
		t.Fatalf("expected empty drain mode after cancel, got %q", sched.DrainMode())
	}
	foundSecond := false
	deadline := time.After(5 * time.Second)
	for !foundSecond {
		select {
		case item := <-entered:
			if item.id == second.ID {
				foundSecond = true
			}
		case <-deadline:
			t.Fatalf("expected second worker %s to run after CancelDrain", second.ID)
		}
	}
	eventually(t, func() bool { return sched.ActiveCount() == 0 })

	if !sched.TryDrainIfIdle(DrainSafePoint) {
		t.Fatal("TryDrainIfIdle should succeed when idle")
	}
	if sched.DrainMode() != DrainSafePoint {
		t.Fatalf("expected drain mode after TryDrainIfIdle, got %q", sched.DrainMode())
	}
	mu.Lock()
	if len(activeChanges) == 0 {
		mu.Unlock()
		t.Fatal("expected OnActiveChange callback to fire")
	}
	mu.Unlock()
}

func TestHandleEventFiltersTypeBeforeUnmarshal(t *testing.T) {
	repo := testRepository(t)

	type finishCall struct {
		threadID  string
		keepAsked bool
	}
	var stopped []string
	var finished []finishCall
	sched, err := New(Options{
		Repository:       repo,
		Run:              func(context.Context, core.ConversationView) error { return nil },
		OnThreadStopped:  func(id string) { stopped = append(stopped, id) },
		OnThreadFinished: func(id string, keepAsked bool) { finished = append(finished, finishCall{id, keepAsked}) },
	})
	if err != nil {
		t.Fatal(err)
	}

	// Unrelated events must skip JSON deserialization entirely (malformed JSON must not fail).
	for _, eventType := range []string{"turn.step", "message.posted", "artifact.revised", "todo.updated"} {
		if err := sched.handleEvent(eventstore.Event{Type: eventType, Data: json.RawMessage(`not-json`)}); err != nil {
			t.Fatalf("unrelated event %q should not be unmarshaled: %v", eventType, err)
		}
	}

	// Large unrelated payload should allocate nothing during handleEvent.
	largePayload := json.RawMessage(`{"payload":"` + strings.Repeat("x", 64*1024) + `"}`)
	allocs := testing.AllocsPerRun(20, func() {
		if err := sched.handleEvent(eventstore.Event{Type: "turn.step", Data: largePayload}); err != nil {
			t.Fatal(err)
		}
	})
	if allocs != 0 {
		t.Fatalf("unrelated event allocated %.0f times, want 0", allocs)
	}

	// thread.stopped must unmarshal and fire OnThreadStopped (and fail on invalid JSON).
	if err := sched.handleEvent(eventstore.Event{Type: "thread.stopped", Data: json.RawMessage(`{"threadId":"thr_1"}`)}); err != nil {
		t.Fatal(err)
	}
	if err := sched.handleEvent(eventstore.Event{Type: "thread.stopped", Data: json.RawMessage(`not-json`)}); err == nil {
		t.Fatal("thread.stopped with invalid JSON should return error")
	}

	// Finish-triggering events must unmarshal and fire OnThreadFinished with accurate keepAsked.
	cases := []struct {
		eventType string
		data      string
	}{
		{"branch.merged", `{"threadId":"thr_2"}`},
		{"pull_request.closed", `{"threadId":"thr_3"}`},
		{"thread.reviewed", `{"threadId":"thr_4"}`},
		{"thread.completed", `{"threadId":"thr_5"}`},
		{"thread.reported", `{"threadId":"thr_6","report":"ready"}`},
		{"thread.reported", `{"threadId":"thr_7","report":"done"}`},
		{"thread.reported", `{"threadId":"thr_8","report":"failed"}`},
		{"thread.reported", `{"threadId":"thr_9","report":"blocked"}`},
		{"thread.reported", `{"threadId":"thr_10","report":"progress"}`},
		{"thread.reported", `{"threadId":"thr_11","report":"waiting"}`},
	}
	for _, tc := range cases {
		if err := sched.handleEvent(eventstore.Event{Type: tc.eventType, Data: json.RawMessage(tc.data)}); err != nil {
			t.Fatalf("%s failed: %v", tc.eventType, err)
		}
	}
	if err := sched.handleEvent(eventstore.Event{Type: "thread.reported", Data: json.RawMessage(`not-json`)}); err == nil {
		t.Fatal("thread.reported with invalid JSON should return error when OnThreadFinished is set")
	}

	if len(stopped) != 1 || stopped[0] != "thr_1" {
		t.Fatalf("stopped = %v, want [thr_1]", stopped)
	}
	wantFinished := []finishCall{
		{"thr_2", false},
		{"thr_3", false},
		{"thr_4", false},
		{"thr_5", false},
		{"thr_6", true},
		{"thr_7", false},
		{"thr_8", false},
		{"thr_9", false},
	}
	if len(finished) != len(wantFinished) {
		t.Fatalf("finished = %+v, want %+v", finished, wantFinished)
	}
	for i := range wantFinished {
		if finished[i] != wantFinished[i] {
			t.Fatalf("finished[%d] = %+v, want %+v", i, finished[i], wantFinished[i])
		}
	}

	// Without OnThreadFinished callback, finish-only events skip JSON unmarshaling as well.
	noFinishCb, err := New(Options{
		Repository: repo,
		Run:        func(context.Context, core.ConversationView) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, eventType := range []string{"branch.merged", "pull_request.closed", "thread.reviewed", "thread.completed", "thread.reported"} {
		if err := noFinishCb.handleEvent(eventstore.Event{Type: eventType, Data: json.RawMessage(`not-json`)}); err != nil {
			t.Fatalf("event %q without OnThreadFinished should skip unmarshal: %v", eventType, err)
		}
	}

	// End-to-end consume batch: irrelevant events advance cursor while relevant events fire callbacks.
	if _, err := repo.Store.Append(t.Context(), []eventstore.NewEvent{
		{Type: "project.created", ProjectID: "prj_c", Data: json.RawMessage(`{"name":"C","coordinatorId":"crd_c"}`)},
		{Type: "thread.created", ProjectID: "prj_c", Data: json.RawMessage(`{"threadId":"thr_c","title":"C"}`)},
		{Type: "message.posted", ProjectID: "prj_c", Data: json.RawMessage(`{"messageId":"m1","conversationId":"thr_c","author":"user","kind":"message","content":"hi"}`)},
		{Type: "thread.reported", ProjectID: "prj_c", Data: json.RawMessage(`{"threadId":"thr_c","report":"ready","headline":"done","summary":"ok","artifacts":[]}`)},
		{Type: "thread.stopped", ProjectID: "prj_c", Data: json.RawMessage(`{"threadId":"thr_c","reason":"user"}`)},
	}); err != nil {
		t.Fatal(err)
	}
	sched.ctx = t.Context()
	stopped = nil
	finished = nil
	lastSeq, err := repo.Store.LastSeq(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	nextSeq, err := sched.consume(0)
	if err != nil {
		t.Fatal(err)
	}
	if nextSeq != lastSeq {
		t.Fatalf("consume advanced seq to %d, want %d", nextSeq, lastSeq)
	}
	if len(stopped) != 1 || stopped[0] != "thr_c" {
		t.Fatalf("consume stopped = %v, want [thr_c]", stopped)
	}
	if len(finished) != 1 || finished[0] != (finishCall{"thr_c", true}) {
		t.Fatalf("consume finished = %+v, want [{thr_c true}]", finished)
	}
}
