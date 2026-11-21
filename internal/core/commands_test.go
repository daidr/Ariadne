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

func TestProjectAndThreadCommandsMatchLegacy(t *testing.T) {
	seed := []eventstore.NewEvent{
		{Type: "project.created", ProjectID: "prj_a", Data: json.RawMessage(`{"name":"项目","coordinatorId":"crd_a"}`)},
		{Type: "thread.created", ProjectID: "prj_a", Data: json.RawMessage(`{"threadId":"thr_a","title":"作者","nodeId":null,"model":null,"branch":null}`)},
		{Type: "thread.created", ProjectID: "prj_a", Data: json.RawMessage(`{"threadId":"thr_review","title":"审查","nodeId":null,"model":null,"branch":null,"reviewOf":"thr_a"}`)},
		{Type: "review.requested", ProjectID: "prj_a", Data: json.RawMessage(`{"threadId":"thr_a","reviewerId":"thr_review","round":1,"head":"head_a"}`)},
		{Type: "conversation.wait_set", ProjectID: "prj_a", Data: json.RawMessage(`{"conversationId":"thr_a","waitId":"wait_a","until":1800000010000,"note":"稍后检查"}`)},
	}
	commands := []object{}
	add := func(op string, args object) { commands = append(commands, object{"op": op, "args": args}) }
	for _, name := range []string{"  新名称  ", "新名称", " "} {
		add("rename-project", object{"id": "prj_a", "name": name})
	}
	for _, slots := range []any{2, 2, 0, 17, nil} {
		add("slots", object{"id": "prj_a", "slots": slots})
	}
	for _, servers := range []any{[]string{"b", "a", "b"}, []string{"b", "a"}, []string{""}, []string{}, nil} {
		add("mcp", object{"id": "prj_a", "servers": servers})
	}
	add("pause", object{"id": "prj_a", "paused": true})
	for _, input := range []object{{"instructions": "  中文说明  ", "workspace": object{"check": "go test ./...", "retention": "keep"}, "tools": []string{"commands"}, "autopilot": object{"enabled": true}}, {"instructions": "中文说明", "workspace": object{"check": "go test ./...", "retention": "keep"}, "tools": []string{"commands"}, "autopilot": object{"enabled": true}}} {
		add("settings", object{"id": "prj_a", "input": input})
	}
	add("pause", object{"id": "prj_a", "paused": true})
	add("pause", object{"id": "prj_a", "paused": true})
	add("pause", object{"id": "prj_a", "paused": false})
	for _, input := range []object{{"mode": "external", "instructions": "  提票后交付  "}, {"instructions": "提票后交付"}, {"mode": nil}, {"instructions": 3}, {"mode": "bad"}, {"mode": "merge"}} {
		add("delivery", object{"id": "prj_a", "input": input})
	}
	add("routines", object{"id": "prj_a", "input": []object{{"id": "daily", "prompt": "检查", "days": []int{1, 2}, "time": "09:30"}}, "change": object{"by": "crd_a", "action": "added", "prompt": "检查"}})
	add("routines", object{"id": "prj_a", "input": []object{}})
	add("merge-method", object{"id": "prj_a", "method": "merge"})
	add("merge-method", object{"id": "prj_a", "method": "merge"})
	add("source", object{"id": "prj_a", "source": object{"kind": "local", "settings": object{"path": "D:/项目"}}})
	add("source", object{"id": "prj_a", "source": object{"settings": object{"path": "D:/项目"}, "kind": "local"}})
	add("source", object{"id": "prj_a", "source": nil})
	for _, model := range []any{"model_a", "model_a", nil} {
		add("model", object{"id": "thr_a", "model": model, "by": "crd_a"})
	}
	add("start", object{"id": "thr_a"})
	add("start", object{"id": "thr_a"})
	for _, title := range []string{"  新  标题\n后续 ", "新 标题 后续", " ", strings.Repeat("中", 81)} {
		add("rename-thread", object{"id": "thr_a", "title": title, "by": "thr_a"})
	}
	add("aside", object{"id": "thr_a", "days": 0, "reason": "稍后", "by": "user"})
	add("aside", object{"id": "thr_a", "days": 2, "reason": "  等待\n验证  ", "by": "crd_a"})
	add("aside-due", object{"id": "thr_a"})
	add("aside-due", object{"id": "thr_a"})
	add("end-aside", object{"id": "thr_a", "by": "user"})
	add("end-aside", object{"id": "thr_a", "by": "user"})
	add("stop", object{"id": "thr_a", "reason": " ", "by": "crd_a"})
	add("stop", object{"id": "thr_a", "reason": "  暂停  ", "by": "crd_a"})
	add("stop", object{"id": "thr_a", "reason": "忽略", "by": "crd_a"})
	add("stop", object{"id": "thr_a", "reason": "", "by": "user"})
	add("resume", object{"id": "thr_a", "by": "user"})
	add("resume", object{"id": "thr_a", "by": "user"})
	add("stop", object{"id": "thr_review", "reason": "停止审查", "by": "user"})
	add("complete", object{"id": "thr_a"})
	add("rename-project", object{"id": "missing", "name": "新名称"})
	add("rename-thread", object{"id": "missing", "title": "新标题", "by": "user"})
	add("settings", object{"id": "prj_a", "input": object{}})
	add("can-note", object{"id": "thr_a"})
	add("can-note", object{"id": "thr_review"})
	add("note-setup", object{"id": "thr_a", "command": " "})
	add("note-setup", object{"id": "thr_a", "command": ""})
	add("note-check", object{"id": "thr_a", "command": "  go test ./...  "})
	add("note-check", object{"id": "thr_a", "command": "go test ./..."})
	add("note-check", object{"id": "thr_review", "command": "invalid"})
	add("source", object{"id": "prj_a", "source": object{"kind": "local", "settings": object{"path": "D:/new"}}})
	add("note-setup", object{"id": "thr_a", "command": "go mod download"})
	add("note-check", object{"id": "thr_a", "command": strings.Repeat("x", 2001)})
	add("settings", object{"id": "prj_a", "input": object{"tools": []string{"commands"}}})
	add("can-note", object{"id": "thr_a"})
	add("note-setup", object{"id": "thr_a", "command": "go mod download"})
	add("settings", object{"id": "prj_a", "input": object{}})
	workspace := object{"label": "temporary", "branch": "work/feature", "base": "main"}
	add("workspace", object{"id": "thr_a", "input": workspace})
	add("workspace", object{"id": "thr_a", "input": workspace})
	add("removed", object{"id": "thr_a", "reason": "idle"})
	add("removed", object{"id": "thr_a", "reason": "idle"})
	add("removed", object{"id": "thr_a", "reason": "merged"})
	add("workspace", object{"id": "thr_a", "input": workspace})
	add("merge", object{"id": "thr_a", "input": object{"branch": "work/feature", "base": "main", "method": "squash", "sha": "base_sha", "head": "head_a"}})
	add("undo-merge", object{"id": "thr_a"})
	add("undo-merge", object{"id": "thr_a"})
	add("conflict", object{"id": "thr_a", "input": object{"branch": "work/feature", "base": "main", "files": []string{"main.go"}, "pullRequest": 1}})
	pr := func(head, state, checks, title string) object {
		return object{"number": 1, "url": "https://example.com/1", "title": title, "state": state, "headSha": head, "checks": object{"state": checks, "failed": []string{}}}
	}
	for _, input := range []object{pr("head_a", "open", "pending", "功能"), pr("head_a", "open", "pending", "功能"), pr("head_a", "open", "failure", "功能"), pr("head_a", "open", "failure", "更新标题"), pr("head_b", "open", "failure", "更新标题"), pr("head_b", "open", "success", "更新标题"), pr("head_b", "open", "failure", "更新标题")} {
		add("pull-request", object{"id": "thr_a", "input": input})
	}
	add("pull-request", object{"id": "thr_a", "input": pr("head_b", "merged", "success", "更新标题"), "auto": object{"method": "squash", "base": "main", "sha": nil}})
	var expected []any
	oracle(t, "packages/core/src/testing/go-commands-fixture.ts", object{"seed": seed, "commands": commands}, &expected)
	repo, err := Open(t.Context(), Options{Path: filepath.Join(t.TempDir(), "commands.sqlite"), Now: func() time.Time { return time.UnixMilli(1800000000000) }})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if _, err := repo.Store.Append(t.Context(), seed); err != nil {
		t.Fatal(err)
	}
	for i, command := range commands {
		// JSON input is intentional: command methods receive the same types as HTTP decoding.
		a := asObject(jsonValue(command["args"]))
		id := a.str("id")
		var value any
		var err error
		switch command.str("op") {
		case "rename-project":
			value, err = repo.RenameProject(t.Context(), id, a.str("name"))
		case "slots":
			var slots *int
			if a["slots"] != nil {
				n := int(a["slots"].(float64))
				slots = &n
			}
			value, err = repo.SetProjectSlots(t.Context(), id, slots)
		case "mcp":
			var servers []string
			if a["servers"] != nil {
				servers = []string{}
				for _, v := range array(a, "servers") {
					servers = append(servers, v.(string))
				}
			}
			value, err = repo.SetProjectMCP(t.Context(), id, servers)
		case "settings":
			value, err = repo.SetProjectSettings(t.Context(), id, a["input"], []string{"fetch", "commands"})
		case "delivery":
			value, err = repo.SetProjectDelivery(t.Context(), id, a.obj("input"))
		case "routines":
			var changed *RoutineChange
			if c := a.obj("change"); c != nil {
				changed = &RoutineChange{By: c.str("by"), Action: c.str("action"), Prompt: c.str("prompt")}
			}
			value, err = repo.SetRoutines(t.Context(), id, a["input"], changed)
		case "merge-method":
			err = repo.RememberMergeMethod(t.Context(), id, a.str("method"))
		case "pause":
			value, err = repo.SetAutopilotPaused(t.Context(), id, a.yes("paused"))
		case "source":
			value, err = repo.SetProjectSource(t.Context(), id, a["source"])
		case "model":
			var model *string
			if s, ok := a["model"].(string); ok {
				model = &s
			}
			value, err = repo.SetThreadModel(t.Context(), id, model, a.str("by"))
		case "start":
			value, err = repo.StartThread(t.Context(), id)
		case "rename-thread":
			value, err = repo.RenameThread(t.Context(), id, a.str("title"), a.str("by"))
		case "aside":
			value, err = repo.SetAside(t.Context(), id, int(a["days"].(float64)), a.str("reason"), a.str("by"))
		case "end-aside":
			value, err = repo.EndAside(t.Context(), id, a.str("by"))
		case "aside-due":
			err = repo.AsideDue(t.Context(), id)
		case "stop":
			value, err = repo.StopThread(t.Context(), id, a.str("reason"), a.str("by"))
		case "resume":
			value, err = repo.ResumeThread(t.Context(), id, a.str("by"))
		case "complete":
			value, err = repo.CompleteThread(t.Context(), id)
		case "workspace":
			var input struct {
				Label  string
				Branch *string
				Base   *string
			}
			decodeInput(t, a["input"], &input)
			err = repo.RecordWorkspace(t.Context(), id, input.Label, input.Branch, input.Base)
		case "removed":
			err = repo.RecordWorktreeRemoved(t.Context(), id, a.str("reason"))
		case "merge":
			value, err = repo.RecordMerge(t.Context(), id, a.obj("input"))
		case "undo-merge":
			value, err = repo.RecordMergeUndone(t.Context(), id)
		case "conflict":
			value, err = repo.RecordMergeConflict(t.Context(), id, a.obj("input"))
		case "pull-request":
			err = repo.SyncPullRequest(t.Context(), id, a.obj("input"), a.obj("auto"))
		case "note-setup":
			value, err = repo.NoteSetup(t.Context(), id, a.str("command"))
		case "note-check":
			value, err = repo.NoteCheck(t.Context(), id, a.str("command"))
		case "can-note":
			var setup, check bool
			setup, err = repo.CanNoteSetup(t.Context(), id)
			if err == nil {
				check, err = repo.CanNoteCheck(t.Context(), id)
			}
			value = object{"setup": setup, "check": check}
		default:
			t.Fatal(command)
		}
		var localized any
		if err != nil {
			var domain *domainerr.Error
			if !errors.As(err, &domain) {
				t.Fatalf("command %s: %v", encoded(command), err)
			}
			localized = domain
			value = nil
		}
		detail, err := repo.GetProjectDetail(t.Context(), "prj_a")
		if err != nil {
			t.Fatal(err)
		}
		stored, err := repo.Store.Read(t.Context(), eventstore.ReadOptions{})
		if err != nil {
			t.Fatal(err)
		}
		events := []object{}
		for _, e := range stored {
			events = append(events, object{"seq": e.Seq, "type": e.Type, "projectId": e.ProjectID, "data": e.Data, "at": e.At})
		}
		actual := object{"value": value, "error": localized, "detail": detail, "events": events}
		want := normalizeLegacyExpected(expected[i])
		if !reflect.DeepEqual(normalize(actual), want) {
			t.Fatalf("command %d %s differs\ngot %s\nwant%s", i, encoded(command), encoded(normalize(actual)), encoded(want))
		}
	}
}

func TestCreateThreadValidatesProjectAndNodeAtomically(t *testing.T) {
	repo, err := Open(t.Context(), Options{Path: filepath.Join(t.TempDir(), "new-thread.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if _, err := repo.CreateThread(t.Context(), "missing", NewThread{Title: "任务"}); err == nil {
		t.Fatal("missing project accepted")
	}
	if _, err := repo.Store.Append(t.Context(), []eventstore.NewEvent{{Type: "project.created", ProjectID: "prj_a", Data: json.RawMessage(`{"name":"项目","coordinatorId":"crd_a"}`)}}); err != nil {
		t.Fatal(err)
	}
	node := "missing"
	if _, err := repo.CreateThread(t.Context(), "prj_a", NewThread{Title: "任务", NodeID: &node}); err == nil {
		t.Fatal("missing node accepted")
	}
	branch := "Fix Export!"
	thread, err := repo.CreateThread(t.Context(), "prj_a", NewThread{Title: "  自动任务  ", Branch: &branch, Auto: true})
	if err != nil {
		t.Fatal(err)
	}
	if thread.Title != "自动任务" || thread.BranchSlug != "fix-export" || !thread.Auto || thread.Stage != "queued" {
		t.Fatalf("new thread: %+v", thread)
	}
	var notices, indexed, conversation int
	for _, test := range []struct {
		sql    string
		target *int
	}{{"SELECT count(*) FROM notices WHERE kind='thread-auto-started'", &notices}, {"SELECT count(*) FROM search_docs WHERE ref_id=?", &indexed}, {"SELECT count(*) FROM conversations WHERE id=?", &conversation}} {
		args := []any{}
		if strings.Contains(test.sql, "?") {
			args = append(args, thread.ID)
		}
		if err := repo.Store.DB().QueryRowContext(t.Context(), test.sql, args...).Scan(test.target); err != nil {
			t.Fatal(err)
		}
	}
	if notices != 1 || indexed != 1 || conversation != 1 {
		t.Fatal(notices, indexed, conversation)
	}
}

func TestSetThreadNode(t *testing.T) {
	ctx := t.Context()
	repo, err := Open(ctx, Options{Path: filepath.Join(t.TempDir(), "set-node.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	projectA, err := repo.CreateProject(ctx, new("项目 A"), ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	projectB, err := repo.CreateProject(ctx, new("项目 B"), ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.RevisePlan(ctx, projectA.ID, PlanRevision{Author: "user", Reason: "初始化计划", Ops: []map[string]any{
		{"op": "add", "id": "nod_a", "kind": "task", "title": "节点 A", "description": "", "acceptance": ""},
		{"op": "add", "id": "nod_b", "kind": "task", "title": "节点 B", "description": "", "acceptance": ""},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.RevisePlan(ctx, projectB.ID, PlanRevision{Author: "user", Reason: "项目 B 计划", Ops: []map[string]any{
		{"op": "add", "id": "nod_other", "kind": "task", "title": "其他节点", "description": "", "acceptance": ""},
	}}); err != nil {
		t.Fatal(err)
	}

	thread, err := repo.CreateThread(ctx, projectA.ID, NewThread{Title: "先建的讨论串"})
	if err != nil {
		t.Fatal(err)
	}
	if thread.NodeID != nil {
		t.Fatalf("expected unlinked thread, got %v", thread.NodeID)
	}

	// Reject cross-project thread, missing node, cross-project node, and review thread.
	if _, err := repo.SetThreadNode(ctx, projectB.ID, thread.ID, new("nod_a"), projectB.CoordinatorID); err == nil {
		t.Fatal("cross-project thread accepted")
	}
	if _, err := repo.SetThreadNode(ctx, projectA.ID, thread.ID, new("nod_missing"), projectA.CoordinatorID); err == nil {
		t.Fatal("missing node accepted")
	}
	if _, err := repo.SetThreadNode(ctx, projectA.ID, thread.ID, new("nod_other"), projectA.CoordinatorID); err == nil {
		t.Fatal("cross-project node accepted")
	}
	reviewThreadID := "thr_review_1"
	if _, err := repo.Store.Append(ctx, []eventstore.NewEvent{{
		Type:      "thread.created",
		ProjectID: projectA.ID,
		Data:      json.RawMessage(encoded(object{"threadId": reviewThreadID, "title": "审查", "nodeId": nil, "model": nil, "branch": nil, "reviewOf": thread.ID})),
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SetThreadNode(ctx, projectA.ID, reviewThreadID, new("nod_a"), projectA.CoordinatorID); err == nil {
		t.Fatal("review thread accepted plan node link")
	}

	countEvents := func() int {
		var count int
		if err := repo.Store.DB().QueryRowContext(ctx, "SELECT count(*) FROM events WHERE project_id = ? AND type = 'thread.node.set'", projectA.ID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	nodeThreadIDs := func(nodeID string) []string {
		plan, err := repo.GetPlan(ctx, projectA.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, node := range plan["nodes"].([]object) {
			if node["id"] == nodeID {
				ids := []string{}
				for _, item := range node["threads"].([]object) {
					ids = append(ids, item.str("id"))
				}
				return ids
			}
		}
		t.Fatalf("node %s not found in plan", nodeID)
		return nil
	}

	// Bind to nod_a.
	linked, err := repo.SetThreadNode(ctx, projectA.ID, thread.ID, new(" nod_a "), projectA.CoordinatorID)
	if err != nil {
		t.Fatal(err)
	}
	if linked.NodeID != "nod_a" {
		t.Fatalf("expected nod_a, got %v", linked.NodeID)
	}
	if countEvents() != 1 {
		t.Fatalf("expected 1 thread.node.set event, got %d", countEvents())
	}
	if got := nodeThreadIDs("nod_a"); !reflect.DeepEqual(got, []string{thread.ID}) {
		t.Fatalf("nod_a threads = %v", got)
	}

	// Idempotent bind emits no additional event.
	same, err := repo.SetThreadNode(ctx, projectA.ID, thread.ID, new("nod_a"), projectA.CoordinatorID)
	if err != nil || same.NodeID != "nod_a" {
		t.Fatalf("idempotent bind failed: %+v, %v", same, err)
	}
	if countEvents() != 1 {
		t.Fatalf("idempotent bind emitted event: %d", countEvents())
	}

	// Rebind to nod_b.
	moved, err := repo.SetThreadNode(ctx, projectA.ID, thread.ID, new("nod_b"), projectA.CoordinatorID)
	if err != nil || moved.NodeID != "nod_b" {
		t.Fatalf("rebind failed: %+v, %v", moved, err)
	}
	if countEvents() != 2 {
		t.Fatalf("expected 2 events after rebind, got %d", countEvents())
	}
	if got := nodeThreadIDs("nod_a"); len(got) != 0 {
		t.Fatalf("nod_a still has threads after rebind: %v", got)
	}
	if got := nodeThreadIDs("nod_b"); !reflect.DeepEqual(got, []string{thread.ID}) {
		t.Fatalf("nod_b threads = %v", got)
	}

	// Unlink with empty string.
	unlinked, err := repo.SetThreadNode(ctx, projectA.ID, thread.ID, new(""), "user")
	if err != nil || unlinked.NodeID != nil {
		t.Fatalf("unlink failed: %+v, %v", unlinked, err)
	}
	if countEvents() != 3 {
		t.Fatalf("expected 3 events after unlink, got %d", countEvents())
	}
	if got := nodeThreadIDs("nod_b"); len(got) != 0 {
		t.Fatalf("nod_b still has threads after unlink: %v", got)
	}

	// Idempotent unlink emits no additional event.
	if _, err := repo.SetThreadNode(ctx, projectA.ID, thread.ID, nil, "user"); err != nil {
		t.Fatal(err)
	}
	if countEvents() != 3 {
		t.Fatalf("idempotent unlink emitted event: %d", countEvents())
	}
}
