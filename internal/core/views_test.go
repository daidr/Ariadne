package core

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestProjectListCountsOnlyRunningConversations(t *testing.T) {
	ctx := t.Context()
	repo, err := Open(ctx, Options{Path: filepath.Join(t.TempDir(), "projects.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	active, err := repo.CreateProject(ctx, new("Active"), ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	idle, err := repo.CreateProject(ctx, new("Idle"), ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := repo.CreateThread(ctx, active.ID, NewThread{Title: "Discussion"})
	if err != nil {
		t.Fatal(err)
	}
	check := func(want int64) {
		t.Helper()
		rows, err := repo.ListProjectsByID(ctx, []string{active.ID, idle.ID})
		if err != nil || len(rows) != 2 {
			t.Fatalf("project list: %+v %v", rows, err)
		}
		for _, row := range rows {
			expected := int64(0)
			if row.ID == active.ID {
				expected = want
			}
			if row.RunningTurns != expected {
				t.Fatalf("%s: got %d running turns, want %d", row.Name, row.RunningTurns, expected)
			}
		}
	}
	if err := repo.RequestTurn(ctx, thread.ID); err != nil {
		t.Fatal(err)
	}
	check(0)
	coordinatorTurn, err := repo.StartTurn(ctx, active.CoordinatorID, "mock")
	if err != nil {
		t.Fatal(err)
	}
	check(1)
	threadTurn, err := repo.StartTurn(ctx, thread.ID, "mock")
	if err != nil {
		t.Fatal(err)
	}
	check(2)
	if err := repo.FinishTurn(ctx, active.CoordinatorID, coordinatorTurn, "completed", nil); err != nil {
		t.Fatal(err)
	}
	check(1)
	if err := repo.FinishTurn(ctx, thread.ID, threadTurn, "cancelled", nil); err != nil {
		t.Fatal(err)
	}
	check(0)
}

func TestProjectActivityShowsOnlyCurrentWork(t *testing.T) {
	ctx := t.Context()
	repo, err := Open(ctx, Options{Path: filepath.Join(t.TempDir(), "activity.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	project, err := repo.CreateProject(ctx, new("Activity"), ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	thread := func(title string) ThreadView {
		t.Helper()
		view, err := repo.CreateThread(ctx, project.ID, NewThread{Title: title})
		if err != nil {
			t.Fatal(err)
		}
		return view
	}
	running, queued, waiting, paused := thread("Running"), thread("Queued"), thread("Waiting"), thread("Paused")
	thread("Idle")
	if _, err := repo.StartTurn(ctx, running.ID, "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := repo.RequestTurn(ctx, queued.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.RequestTurn(ctx, paused.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.StopThread(ctx, paused.ID, "User paused it", "user"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{project.CoordinatorID, waiting.ID} {
		if _, err := repo.SetWait(ctx, id, time.Now().Add(time.Hour).UnixMilli(), "Scheduled check"); err != nil {
			t.Fatal(err)
		}
	}
	items := []TodoItem{{Label: "Read", Status: "done"}, {Label: "Check", Status: "doing"}, {Label: "Report", Status: "next"}}
	for _, id := range []string{project.CoordinatorID, running.ID, queued.ID} {
		if _, err := repo.UpdateTodo(ctx, project.ID, id, items); err != nil {
			t.Fatal(err)
		}
	}
	actual, err := repo.ProjectActivity(ctx, project.ID)
	want := ProjectActivity{Coordinator: ConversationActivity{Status: "waiting"}, Threads: []ThreadActivity{
		{ID: running.ID, Title: running.Title, ConversationActivity: ConversationActivity{Status: "running", Progress: &Progress{Done: 1, Total: 3}}},
		{ID: queued.ID, Title: queued.Title, ConversationActivity: ConversationActivity{Status: "queued"}},
		{ID: waiting.ID, Title: waiting.Title, ConversationActivity: ConversationActivity{Status: "waiting"}},
	}}
	if err != nil || !reflect.DeepEqual(actual, want) {
		t.Fatalf("activity = %+v, want %+v, error: %v", actual, want, err)
	}
	if _, err := repo.StartTurn(ctx, project.CoordinatorID, "fixture"); err != nil {
		t.Fatal(err)
	}
	items[1].Status = "done"
	if _, err := repo.UpdateTodo(ctx, project.ID, running.ID, items); err != nil {
		t.Fatal(err)
	}
	actual, err = repo.ProjectActivity(ctx, project.ID)
	want.Coordinator = ConversationActivity{Status: "running", Progress: &Progress{Done: 1, Total: 3}}
	want.Threads[0].Progress.Done = 2
	if err != nil || !reflect.DeepEqual(actual, want) {
		t.Fatalf("updated activity = %+v, want %+v, error: %v", actual, want, err)
	}
	if _, err := repo.ProjectActivity(ctx, "missing"); err == nil {
		t.Fatal("missing project should fail")
	}
}

func TestStoredViewJSONErrorsSurface(t *testing.T) {
	_, err := toThread(record{"merged": "broken"}, 0)
	if err == nil {
		t.Fatal("bad persisted JSON silently discarded")
	}
	// A zero count is distinct from null when serialized to the existing UI.
	bytes, err := json.Marshal(usage(record{}))
	if err != nil || string(bytes) == "null" {
		t.Fatal(string(bytes), err)
	}
}
