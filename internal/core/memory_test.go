package core

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestTodoPositionMovesOnlyWhenStepsChange(t *testing.T) {
	ctx := t.Context()
	now := time.UnixMilli(1800000000000)
	repo, err := Open(ctx, Options{Path: filepath.Join(t.TempDir(), "todo.sqlite"), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	project, err := repo.CreateProject(ctx, new("Todo test"), ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	id := project.CoordinatorID
	message := func(content string) string {
		t.Helper()
		value, err := repo.PostMessage(ctx, project.ID, MessageInput{ConversationID: id, Author: "user", Content: content})
		if err != nil {
			t.Fatal(err)
		}
		return value.ID
	}
	update := func(items []TodoItem, at time.Time, after string) {
		t.Helper()
		value, err := repo.UpdateTodo(ctx, project.ID, id, items)
		if err != nil {
			t.Fatal(err)
		}
		want := object{"at": at.UnixMilli(), "afterMessageId": after}
		if !reflect.DeepEqual(value.obj("position"), want) || value["updatedAt"] != now.UnixMilli() {
			t.Fatalf("updated todo at %d: %+v; want position %+v", now.UnixMilli(), value, want)
		}
	}
	start := now
	first := message("Check the rollout.")
	task := TodoItem{Label: "Read the rollout notes", Status: "doing"}
	update([]TodoItem{task}, start, first)

	now = now.Add(time.Second)
	second := message("Anything new?")
	task.Status, task.Detail = "done", new("Read all the notes.")
	update([]TodoItem{task}, start, first)

	now = now.Add(time.Second)
	revisedAt := now
	items := []TodoItem{task, {Label: "Verify the metrics", Status: "doing"}}
	update(items, revisedAt, second)

	now = now.Add(time.Second)
	third := message("Send the metrics when ready.")
	items[1].Status = "waiting"
	update(items, revisedAt, second)

	now = now.Add(time.Second)
	items[0], items[1] = items[1], items[0]
	update(items, now, third)
	value, err := repo.GetTodo(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	replayed, err := repo.GetTodo(ctx, id)
	if err != nil || !reflect.DeepEqual(replayed, value) {
		t.Fatalf("todo after replay: %+v, %v; want %+v", replayed, err, value)
	}
}
