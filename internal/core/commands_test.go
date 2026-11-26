package core

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/eventstore"
)

func TestCreateThreadValidatesProjectAndNodeAtomically(t *testing.T) {
	t.Parallel()
	repo, err := Open(t.Context(), Options{Path: ":memory:"})
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
	t.Parallel()
	ctx := t.Context()
	repo, err := Open(ctx, Options{Path: ":memory:"})
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

func TestIdleReportEntersIdleStageAndWakesCoordinator(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	repo, err := Open(ctx, Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	project, err := repo.CreateProject(ctx, new("调研项目"), ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.RevisePlan(ctx, project.ID, PlanRevision{Author: "user", Reason: "添加计划节点", Ops: []map[string]any{
		{"op": "add", "id": "nod_research", "kind": "task", "title": "方案调研", "description": "", "acceptance": ""},
	}}); err != nil {
		t.Fatal(err)
	}
	thread, err := repo.CreateThread(ctx, project.ID, NewThread{Title: "调研讨论串", NodeID: new("nod_research")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.StartThread(ctx, thread.ID); err != nil {
		t.Fatal(err)
	}

	reported, err := repo.ReportThread(ctx, thread.ID, ThreadReport{
		Kind:     "idle",
		Headline: "已整理调研结论，待确认下一步",
		Summary:  "完成了三套方案对比，等待后续讨论。",
	}, nil, ReviewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if reported.Stage != "idle" || reported.LastReport != "idle" || reported.LastHeadline != "已整理调研结论，待确认下一步" {
		t.Fatalf("unexpected reported thread: %+v", reported)
	}

	coordinator, err := repo.GetConversation(ctx, project.CoordinatorID)
	if err != nil {
		t.Fatal(err)
	}
	if coordinator.PendingSeq == 0 {
		t.Fatal("expected idle report to wake the coordinator")
	}

	plan, err := repo.GetPlan(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	nodes := plan["nodes"].([]object)
	if len(nodes) != 1 || nodes[0].str("status") == "done" {
		t.Fatalf("idle research thread should not mark plan node done: %+v", nodes)
	}
}

func TestReopenThreadReturnsCompletedThreadToIdle(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	now := time.UnixMilli(1900000000000)
	repo, err := Open(ctx, Options{Path: ":memory:", Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	project, err := repo.CreateProject(ctx, new("恢复测试"), ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}

	countReopenEvents := func() int {
		t.Helper()
		var n int
		if err := repo.Store.DB().QueryRowContext(ctx, "SELECT count(*) FROM events WHERE type = 'thread.reopened'").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// 1. Thread completed via report("done") + set aside.
	doneThread, err := repo.CreateThread(ctx, project.ID, NewThread{Title: "已完成调研"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.StartThread(ctx, doneThread.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ReportThread(ctx, doneThread.ID, ThreadReport{
		Kind: "done", Headline: "调研完成", Summary: "结论已输出",
	}, nil, ReviewOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SetAside(ctx, doneThread.ID, 3, "先放一下", "user"); err != nil {
		t.Fatal(err)
	}

	now = now.Add(time.Minute)
	reopened, err := repo.ReopenThread(ctx, doneThread.ID, "user")
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Stage != "idle" || reopened.LastReport != "idle" || reopened.LastHeadline != nil || reopened.Archived || reopened.Aside != nil {
		t.Fatalf("unexpected reopened thread: %+v", reopened)
	}
	conv, err := repo.GetConversation(ctx, doneThread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if conv.PendingSeq != 0 || conv.LastOutcome != nil {
		t.Fatalf("reopening should not wake thread or leave stale outcome: %+v", conv)
	}
	var threadWoke, coordWoke int
	if err := repo.Store.DB().QueryRowContext(ctx, `SELECT woke FROM transcript WHERE conversation_id = ? AND kind = 'note' AND json_extract(payload, '$.type') = 'thread-reopened'`, doneThread.ID).Scan(&threadWoke); err != nil || threadWoke != 0 {
		t.Fatalf("thread-reopened note on thread woke = %d, err = %v", threadWoke, err)
	}
	if err := repo.Store.DB().QueryRowContext(ctx, `SELECT woke FROM transcript WHERE conversation_id = ? AND kind = 'note' AND json_extract(payload, '$.type') = 'thread-reopened'`, project.CoordinatorID).Scan(&coordWoke); err != nil || coordWoke != 0 {
		t.Fatalf("thread-reopened note on coordinator woke = %d, err = %v", coordWoke, err)
	}

	// Idempotent no-op when already idle.
	if _, err := repo.ReopenThread(ctx, doneThread.ID, "user"); err != nil {
		t.Fatal(err)
	}
	if countReopenEvents() != 1 {
		t.Fatalf("expected 1 thread.reopened event, got %d", countReopenEvents())
	}

	// User can continue sending messages after reopening.
	if _, err := repo.PostMessage(ctx, project.ID, MessageInput{
		ConversationID: doneThread.ID, Author: "user", Content: "顺着这个方案继续细化一下",
	}); err != nil {
		t.Fatal(err)
	}
	afterMsg, err := repo.GetThread(ctx, doneThread.ID)
	if err != nil || afterMsg.Stage != "queued" {
		t.Fatalf("expected queued after follow-up message, got %+v, %v", afterMsg, err)
	}

	// 2. Thread completed via FinishThread before ever starting.
	unstarted, err := repo.CreateThread(ctx, project.ID, NewThread{Title: "未启动即完成"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.FinishThread(ctx, unstarted.ID, "user"); err != nil {
		t.Fatal(err)
	}
	reopenedUnstarted, err := repo.ReopenThread(ctx, unstarted.ID, "user")
	if err != nil || reopenedUnstarted.Stage != "idle" {
		t.Fatalf("expected unstarted finished thread to reopen to idle, got %+v, %v", reopenedUnstarted, err)
	}

	// 3. Thread completed via ready + reviewed (CompleteThread).
	reviewed, err := repo.CreateThread(ctx, project.ID, NewThread{Title: "已审阅完成"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.StartThread(ctx, reviewed.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ReportThread(ctx, reviewed.ID, ThreadReport{
		Kind: "ready", Headline: "待审阅", Summary: "完成改动",
	}, nil, ReviewOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CompleteThread(ctx, reviewed.ID); err != nil {
		t.Fatal(err)
	}
	reopenedReviewed, err := repo.ReopenThread(ctx, reviewed.ID, "user")
	if err != nil || reopenedReviewed.Stage != "idle" {
		t.Fatalf("expected reviewed thread to reopen to idle, got %+v, %v", reopenedReviewed, err)
	}

	// 4. Thread completed via merged PR and archived after > 24h.
	merged, err := repo.CreateThread(ctx, project.ID, NewThread{Title: "已合入归档"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.StartThread(ctx, merged.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ReportThread(ctx, merged.ID, ThreadReport{
		Kind: "ready", Headline: "PR 就绪", Summary: "已提 PR",
	}, nil, ReviewOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SyncPullRequest(ctx, merged.ID, object{"number": 7, "state": "open", "headSha": "deadbeef"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.SyncPullRequest(ctx, merged.ID, object{"number": 7, "state": "merged", "headSha": "deadbeef"}, nil); err != nil {
		t.Fatal(err)
	}
	now = now.Add(48 * time.Hour)
	archivedBefore, err := repo.GetThread(ctx, merged.ID)
	if err != nil || archivedBefore.Stage != "done" || !archivedBefore.Archived {
		t.Fatalf("expected archived done thread before reopen, got %+v, %v", archivedBefore, err)
	}
	reopenedMerged, err := repo.ReopenThread(ctx, merged.ID, "user")
	if err != nil || reopenedMerged.Stage != "idle" || reopenedMerged.Archived {
		t.Fatalf("expected archived merged thread to reopen to unarchived idle, got %+v, %v", reopenedMerged, err)
	}
}
