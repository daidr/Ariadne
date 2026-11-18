package scheduler

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/core"
)

func intPointer(value int) *int { return &value }
func testRepository(t *testing.T) *core.Repository {
	t.Helper()
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(t.TempDir(), "scheduler.sqlite")})
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
	scheduler, err := New(Options{Repository: repo, Slots: intPointer(slots), ReservedSlots: intPointer(reserved), Run: run})
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
