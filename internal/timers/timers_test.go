package timers

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/projectsettings"
)

func fixture(t *testing.T) (*Timers, *core.Repository, core.ProjectView, *atomic.Int64) {
	t.Helper()
	now := &atomic.Int64{}
	now.Store(time.Date(2026, 11, 5, 8, 59, 0, 0, time.UTC).UnixMilli())
	clock := func() time.Time { return time.UnixMilli(now.Load()) }
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(t.TempDir(), "timers.sqlite"), Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repo.Close() })
	name := "project"
	project, err := repo.CreateProject(t.Context(), &name, core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	timers, err := New(Options{Repository: repo, Now: clock, Location: time.UTC, OnError: func(err error) { t.Error(err) }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := timers.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	return timers, repo, project, now
}
func setRoutine(t *testing.T, repo *core.Repository, project, timeOfDay string) {
	t.Helper()
	routines := []projectsettings.Routine{{ID: "r1", Prompt: "read new issues", Time: timeOfDay, Days: []int{1, 2, 3, 4, 5}, Enabled: true}}
	if _, err := repo.SetRoutines(t.Context(), project, routines, nil); err != nil {
		t.Fatal(err)
	}
}
func check(t *testing.T, timers *Timers) {
	t.Helper()
	if _, err := timers.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
}
func countRuns(t *testing.T, repo *core.Repository) int {
	t.Helper()
	var count int
	if err := repo.Store.DB().QueryRowContext(t.Context(), "SELECT count(*) FROM events WHERE type='routine.ran'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
func TestDueWaitWakesOnRestartAndReplacementIsNotEnded(t *testing.T) {
	timers, repo, project, now := fixture(t)
	if _, err := repo.SetWait(t.Context(), project.CoordinatorID, now.Load()-1000, "check deployment"); err != nil {
		t.Fatal(err)
	}
	if err := timers.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	check(t, timers)
	conversation, err := repo.GetConversation(t.Context(), project.CoordinatorID)
	if err != nil || conversation.Wait != nil || conversation.PendingSeq == 0 {
		t.Fatal("due wait not woken", conversation, err)
	}
	old, err := repo.SetWait(t.Context(), project.CoordinatorID, now.Load()-1000, "old")
	if err != nil {
		t.Fatal(err)
	}
	newWait, err := repo.SetWait(t.Context(), project.CoordinatorID, now.Load()+60000, "replacement")
	if err != nil {
		t.Fatal(err)
	}
	ended, err := repo.EndWait(t.Context(), project.CoordinatorID, "due", "time", &old)
	if err != nil || ended {
		t.Fatal("stale wait ended replacement", err)
	}
	check(t, timers)
	conversation, _ = repo.GetConversation(t.Context(), project.CoordinatorID)
	if conversation.Wait == nil || conversation.Wait.ID != newWait {
		t.Fatal("replacement wait not preserved")
	}
}
func TestRoutineDueOnceAndMissedRunsAreNotMadeUp(t *testing.T) {
	timers, repo, project, now := fixture(t)
	setRoutine(t, repo, project.ID, "09:00")
	if err := timers.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	check(t, timers)
	now.Add(time.Minute.Milliseconds())
	check(t, timers)
	check(t, timers)
	if count := countRuns(t, repo); count != 1 {
		t.Fatal("routine not run exactly once", count)
	}
	// Next day, a sleeping machine missed the grace period.
	now.Add((24*time.Hour + 6*time.Minute).Milliseconds())
	check(t, timers)
	if count := countRuns(t, repo); count != 1 {
		t.Fatal("stale run made up", count)
	}
	if err := timers.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	now.Add(24 * time.Hour.Milliseconds())
	if err := timers.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	check(t, timers)
	if count := countRuns(t, repo); count != 1 {
		t.Fatal("restart made up missed run", count)
	}
}
func TestChangedScheduleIsCountedFromNowAndStaleSnapshotCannotRun(t *testing.T) {
	timers, repo, project, now := fixture(t)
	setRoutine(t, repo, project.ID, "09:00")
	if err := timers.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	check(t, timers)
	before, err := repo.ReadTimers(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Projects) != 1 {
		t.Fatal("missing schedule")
	}
	now.Add(3 * time.Minute.Milliseconds())
	setRoutine(t, repo, project.ID, "08:58")
	check(t, timers)
	if count := countRuns(t, repo); count != 0 {
		t.Fatal("edited routine ran late", count)
	}
	ran, err := repo.RunRoutineIfCurrent(t.Context(), project.ID, "r1", now.Load(), before.Projects[0].SettingsSeq)
	if err != nil || ran {
		t.Fatal("stale schedule ran", ran, err)
	}
	current, err := repo.ReadTimers(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for range 10 {
		group.Go(func() {
			if _, err := repo.RunRoutineIfCurrent(t.Context(), project.ID, "r1", now.Load(), current.Projects[0].SettingsSeq); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
	if count := countRuns(t, repo); count != 1 {
		t.Fatal("concurrent duplicate runs", count)
	}
}
func TestAsideDueIsConditionalAndStoppingReleasesSchedulingState(t *testing.T) {
	timers, repo, project, now := fixture(t)
	thread, err := repo.CreateThread(t.Context(), project.ID, core.NewThread{Title: "later"})
	if err != nil {
		t.Fatal(err)
	}
	thread, err = repo.SetAside(t.Context(), thread.ID, 1, "tomorrow", "user")
	if err != nil {
		t.Fatal(err)
	}
	old := thread.Aside.Until
	if err := timers.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	check(t, timers)
	thread, err = repo.SetAside(t.Context(), thread.ID, 2, "later", "user")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.AsideDueIfCurrent(t.Context(), thread.ID, old); err != nil {
		t.Fatal(err)
	}
	thread, _ = repo.GetThread(t.Context(), thread.ID)
	if thread.Aside.Due {
		t.Fatal("stale aside was marked due")
	}
	now.Store(thread.Aside.Until)
	check(t, timers)
	thread, _ = repo.GetThread(t.Context(), thread.ID)
	if !thread.Aside.Due {
		t.Fatal("aside did not come due")
	}
	if err := timers.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	timers.mu.Lock()
	defer timers.mu.Unlock()
	if timers.life != nil || timers.cancel != nil || timers.done != nil || !timers.from.IsZero() || timers.seq != 0 {
		t.Fatal("stopped timer retains scheduling state")
	}
}
