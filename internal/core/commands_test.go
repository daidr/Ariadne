package core

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/internal/eventstore"
)

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
