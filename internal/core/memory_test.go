package core

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/eventstore"
)

func TestMemoryUndoLimitsAndTodoMatchLegacy(t *testing.T) {
	seed := []eventstore.NewEvent{{Type: "project.created", ProjectID: "prj_a", Data: json.RawMessage(`{"name":"项目","coordinatorId":"crd_a"}`)}, {Type: "thread.created", ProjectID: "prj_a", Data: json.RawMessage(`{"threadId":"thr_a","title":"任务","nodeId":null,"model":null,"branch":null}`)}, {Type: "memory.saved", ProjectID: "prj_a", Data: json.RawMessage(`{"memoryId":"mem_a","text":"原始偏好","author":"user"}`)}}
	commands := []object{}
	add := func(op string, args object) {
		if args["projectId"] == nil {
			args["projectId"] = "prj_a"
		}
		commands = append(commands, object{"op": op, "args": args})
	}
	add("save", object{"memoryId": "mem_a", "text": "  新偏好\n继续\u0085验证  ", "author": "thr_a"})
	add("undo", object{"noticeId": "latest"})
	add("undo", object{"noticeId": "latest"})
	add("remove", object{"memoryId": "mem_a", "author": "thr_a"})
	add("undo", object{"noticeId": "latest"})
	add("save", object{"text": "代理添加", "author": "thr_a"})
	add("undo", object{"noticeId": "latest"})
	add("remove", object{"memoryId": "last", "author": "user"})
	add("save", object{"text": " ", "author": "thr_a"})
	add("save", object{"text": strings.Repeat("😀", 251), "author": "user"})
	add("save", object{"memoryId": "missing", "text": "偏好", "author": "user"})
	add("everywhere", object{"id": "thr_a", "action": "saved", "text": "通用偏好"})
	add("notice", object{"noticeId": "latest"})
	add("settle", object{"noticeId": "latest"})
	add("settle", object{"noticeId": "latest"})
	add("notice", object{"noticeId": "latest"})
	add("undo", object{"noticeId": "latest"})
	add("everywhere", object{"id": "missing", "action": "saved", "text": "通用偏好"})
	add("notice", object{"noticeId": "missing"})
	add("todo", object{"id": "thr_a", "items": []object{{"label": "实现", "status": "doing"}, {"label": "等待", "status": "waiting", "detail": "等接口"}}})
	add("todo", object{"id": "thr_a", "items": []object{}})
	add("todo", object{"id": "missing", "items": []object{}})
	for i := 0; i < 51; i++ {
		add("save", object{"text": "偏好 " + encoded(i), "author": "user"})
	}
	var expected []any
	oracle(t, "packages/core/src/testing/go-memory-fixture.ts", object{"seed": seed, "commands": commands}, &expected)
	repo, err := Open(t.Context(), Options{Path: filepath.Join(t.TempDir(), "memory.sqlite"), Now: func() time.Time { return time.UnixMilli(1800000000000) }})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if _, err := repo.Store.Append(t.Context(), seed); err != nil {
		t.Fatal(err)
	}
	lastMemory := "mem_a"
	for i, command := range commands {
		a := asObject(jsonValue(command["args"]))
		projectID := a.str("projectId")
		memoryID := a.str("memoryId")
		if memoryID == "last" {
			memoryID = lastMemory
		}
		noticeID := a.str("noticeId")
		if noticeID == "latest" {
			notices, err := repo.ListNotices(t.Context(), "thr_a", NoticeRange{})
			if err != nil {
				t.Fatal(err)
			}
			noticeID = notices[len(notices)-1].str("id")
		}
		var value any
		var err error
		switch command.str("op") {
		case "save":
			input := MemoryChange{Text: a.str("text"), Author: a.str("author")}
			if _, has := a["memoryId"]; has {
				input.MemoryID = &memoryID
			}
			var memory MemoryView
			memory, err = repo.SaveMemory(t.Context(), projectID, input)
			if err == nil {
				lastMemory = memory.ID
				value = memory
			}
		case "remove":
			err = repo.RemoveMemory(t.Context(), projectID, memoryID, a.str("author"), "")
		case "undo":
			err = repo.UndoMemoryNotice(t.Context(), projectID, noticeID)
		case "everywhere":
			err = repo.NoteEverywhere(t.Context(), projectID, a.str("id"), a.str("action"), a.str("text"))
		case "notice":
			value, err = repo.EverywhereNotice(t.Context(), projectID, noticeID)
		case "settle":
			err = repo.SettleNotice(t.Context(), projectID, noticeID)
		case "todo":
			var items []TodoItem
			decodeInput(t, a["items"], &items)
			var todo object
			todo, err = repo.UpdateTodo(t.Context(), projectID, a.str("id"), items)
			delete(todo, "position")
			value = todo
		default:
			t.Fatal(command)
		}
		var localized any
		if err != nil {
			var domain *domainerr.Error
			if !errors.As(err, &domain) {
				t.Fatal(err)
			}
			localized = domain
			value = nil
		}
		memories, err := repo.ListMemories(t.Context(), "prj_a")
		if err != nil {
			t.Fatal(err)
		}
		notices, err := repo.ListNotices(t.Context(), "thr_a", NoticeRange{})
		if err != nil {
			t.Fatal(err)
		}
		todo, err := repo.GetTodo(t.Context(), "thr_a")
		if err != nil {
			t.Fatal(err)
		}
		delete(todo, "position")
		stored, err := repo.Store.Read(t.Context(), eventstore.ReadOptions{})
		if err != nil {
			t.Fatal(err)
		}
		events := []object{}
		for _, e := range stored {
			var data any = e.Data
			if e.Type == "todo.updated" {
				// Position was added after the legacy oracle. Its behavior has a separate test.
				payload := asObject(jsonValue(e.Data))
				delete(payload, "position")
				data = payload
			}
			events = append(events, object{"seq": e.Seq, "type": e.Type, "projectId": e.ProjectID, "data": data, "at": e.At})
		}
		actual := object{"value": value, "error": localized, "memories": memories, "notices": notices, "todo": todo, "events": events}
		if !reflect.DeepEqual(normalizedIDs(actual), normalizedIDs(expected[i])) {
			t.Fatalf("command %d %s differs\ngot %s\nwant%s", i, encoded(command), encoded(normalizedIDs(actual)), encoded(normalizedIDs(expected[i])))
		}
	}
}

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
