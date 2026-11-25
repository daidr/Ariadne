package core

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/eventstore"
)

func TestListAllNeedsMeAggregatesAndMatchesProjectCounts(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	nowMs := int64(1_800_000_000_000)
	now := func() time.Time {
		nowMs += 1_000
		return time.UnixMilli(nowMs)
	}
	repo, err := Open(ctx, Options{Path: ":memory:", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	_, err = repo.Store.Append(ctx, []eventstore.NewEvent{
		{Type: "project.created", ProjectID: "prj_a", Data: json.RawMessage(`{"name":"Alpha","coordinatorId":"crd_a"}`)},
		{Type: "project.created", ProjectID: "prj_b", Data: json.RawMessage(`{"name":"Beta","coordinatorId":"crd_b"}`)},
		{Type: "project.created", ProjectID: "prj_c", Data: json.RawMessage(`{"name":"Gamma","coordinatorId":"crd_c"}`)},

		// Alpha: thread with open attention + joined waiter, plus an idle noise thread.
		{Type: "thread.created", ProjectID: "prj_a", Data: json.RawMessage(`{"threadId":"thr_a_ask","title":"审批发布"}`)},
		{Type: "thread.created", ProjectID: "prj_a", Data: json.RawMessage(`{"threadId":"thr_a_join","title":"跟随等待"}`)},
		{Type: "thread.created", ProjectID: "prj_a", Data: json.RawMessage(`{"threadId":"thr_a_idle","title":"空闲讨论串"}`)},
		{Type: "attention.opened", ProjectID: "prj_a", Data: json.RawMessage(`{"attentionId":"att_a_1","threadId":"thr_a_ask","conversationId":"thr_a_ask","block":{"type":"confirm","title":"确认部署？","tone":"warning"}}`)},
		{Type: "attention.joined", ProjectID: "prj_a", Data: json.RawMessage(`{"attentionId":"att_a_1","threadId":"thr_a_join"}`)},
		// Even if thr_a_join fails a turn, it already waits on att_a_1 so it must not be double-counted.
		{Type: "turn.started", ProjectID: "prj_a", Data: json.RawMessage(`{"conversationId":"thr_a_join","turnId":"trn_a_join","model":"m"}`)},
		{Type: "turn.finished", ProjectID: "prj_a", Data: json.RawMessage(`{"conversationId":"thr_a_join","turnId":"trn_a_join","outcome":"failed"}`)},
		// Closed attention must be ignored.
		{Type: "attention.opened", ProjectID: "prj_a", Data: json.RawMessage(`{"attentionId":"att_a_closed","conversationId":"crd_a","block":{"type":"confirm","title":"已关闭","tone":"neutral"}}`)},
		{Type: "attention.answered", ProjectID: "prj_a", Data: json.RawMessage(`{"attentionId":"att_a_closed","answer":{"type":"confirm","approved":true}}`)},

		// Beta: coordinator attention, review thread with artifact, halted thread, aside-due thread, and reviewer thread (excluded).
		{Type: "artifact.created", ProjectID: "prj_b", Data: json.RawMessage(`{"artifactId":"art_b_1","kind":"document","title":"发布说明","content":"正文","author":"thr_b_review"}`)},
		{Type: "thread.created", ProjectID: "prj_b", Data: json.RawMessage(`{"threadId":"thr_b_review","title":"完成审查项"}`)},
		{Type: "thread.reported", ProjectID: "prj_b", Data: json.RawMessage(`{"threadId":"thr_b_review","report":"ready","headline":"已完成并附文档","summary":"详情","artifacts":["art_b_1","art_missing"]}`)},
		{Type: "thread.created", ProjectID: "prj_b", Data: json.RawMessage(`{"threadId":"thr_b_halted","title":"失败待继续"}`)},
		{Type: "turn.started", ProjectID: "prj_b", Data: json.RawMessage(`{"conversationId":"thr_b_halted","turnId":"trn_b_1","model":"m"}`)},
		{Type: "turn.finished", ProjectID: "prj_b", Data: json.RawMessage(`{"conversationId":"thr_b_halted","turnId":"trn_b_1","outcome":"failed"}`)},
		{Type: "thread.created", ProjectID: "prj_b", Data: json.RawMessage(`{"threadId":"thr_b_aside","title":"搁置到期"}`)},
		{Type: "thread.set_aside", ProjectID: "prj_b", Data: json.RawMessage(`{"threadId":"thr_b_aside","until":1800000005000,"reason":"等上游发布","by":"user"}`)},
		{Type: "thread.aside_due", ProjectID: "prj_b", Data: json.RawMessage(`{"threadId":"thr_b_aside"}`)},
		{Type: "thread.created", ProjectID: "prj_b", Data: json.RawMessage(`{"threadId":"thr_b_reviewer","title":"评审者","reviewOf":"thr_b_review"}`)},
		{Type: "thread.reported", ProjectID: "prj_b", Data: json.RawMessage(`{"threadId":"thr_b_reviewer","report":"ready","headline":"评审完成"}`)},
		{Type: "attention.opened", ProjectID: "prj_b", Data: json.RawMessage(`{"attentionId":"att_b_coord","conversationId":"crd_b","block":{"type":"choice","question":"选哪套方案？","options":[{"label":"A","consequence":""}]}}`)},
	})
	if err != nil {
		t.Fatal(err)
	}

	projects, err := repo.Reader.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	expectedTotal := 0
	expectedByProject := map[string]int{}
	for _, p := range projects {
		expectedTotal += p.NeedsMe
		expectedByProject[p.ID] = p.NeedsMe
	}

	all, err := repo.Reader.ListAllNeedsMe(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if len(all.Items) != expectedTotal {
		t.Fatalf("ListAllNeedsMe items len = %d, want %d (byProject=%v)", len(all.Items), expectedTotal, expectedByProject)
	}
	if expectedByProject["prj_a"] != 1 || expectedByProject["prj_b"] != 4 || expectedByProject["prj_c"] != 0 {
		t.Fatalf("unexpected project counts: %v", expectedByProject)
	}

	gotByProject := map[string]int{}
	for i, entry := range all.Items {
		gotByProject[entry.ProjectID]++
		if i > 0 && all.Items[i-1].At < entry.At {
			t.Fatalf("items not sorted by At DESC: prev=%d curr=%d", all.Items[i-1].At, entry.At)
		}
		if entry.ProjectID == "prj_a" && entry.ProjectName != "Alpha" {
			t.Fatalf("prj_a projectName = %q, want Alpha", entry.ProjectName)
		}
		if entry.ProjectID == "prj_b" && entry.ProjectName != "Beta" {
			t.Fatalf("prj_b projectName = %q, want Beta", entry.ProjectName)
		}
	}
	if gotByProject["prj_a"] != 1 || gotByProject["prj_b"] != 4 || gotByProject["prj_c"] != 0 {
		t.Fatalf("item counts by project = %v, want %v", gotByProject, expectedByProject)
	}

	// Unrelated idle thread and reviewer thread must not be loaded into the lightweight summary map.
	if _, ok := all.Threads["thr_a_idle"]; ok {
		t.Fatalf("unexpected idle thread in ListAllNeedsMe.Threads")
	}
	if _, ok := all.Threads["thr_b_reviewer"]; ok {
		t.Fatalf("unexpected reviewer thread in ListAllNeedsMe.Threads")
	}
	// Owner + joined asker threads and matched thread items must all have summaries.
	for _, id := range []string{"thr_a_ask", "thr_a_join", "thr_b_review", "thr_b_halted", "thr_b_aside"} {
		if _, ok := all.Threads[id]; !ok {
			t.Fatalf("missing summary for thread %s", id)
		}
	}
	reviewSummary := all.Threads["thr_b_review"]
	if reviewSummary.Stage != "review" || reviewSummary.LastHeadline != "已完成并附文档" {
		t.Fatalf("unexpected review summary: %+v", reviewSummary)
	}
	artifacts, ok := reviewSummary.LastArtifacts.([]any)
	if !ok || len(artifacts) != 1 {
		t.Fatalf("resolved lastArtifacts = %#v, want 1 item", reviewSummary.LastArtifacts)
	}
	art := artifacts[0].(object)
	if art["id"] != "art_b_1" || art["kind"] != "document" || art["title"] != "发布说明" {
		t.Fatalf("unexpected resolved artifact: %+v", art)
	}
	asideSummary := all.Threads["thr_b_aside"]
	if asideSummary.Aside == nil || !asideSummary.Aside.Due || asideSummary.Aside.Reason != "等上游发布" {
		t.Fatalf("unexpected aside summary: %+v", asideSummary.Aside)
	}
}

func TestSearchCrossProjectAndPreferProjectOrdering(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	nowMs := int64(1_800_000_000_000)
	now := func() time.Time {
		nowMs += 1_000
		return time.UnixMilli(nowMs)
	}
	repo, err := Open(ctx, Options{Path: ":memory:", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	// Alpha events happen earlier; Beta events happen later.
	_, err = repo.Store.Append(ctx, []eventstore.NewEvent{
		{Type: "project.created", ProjectID: "prj_alpha", Data: json.RawMessage(`{"name":"Alpha","coordinatorId":"crd_alpha"}`)},
		{Type: "project.created", ProjectID: "prj_beta", Data: json.RawMessage(`{"name":"Beta","coordinatorId":"crd_beta"}`)},

		{Type: "thread.created", ProjectID: "prj_alpha", Data: json.RawMessage(`{"threadId":"thr_alpha","title":"跨项目搜索 Alpha 方案"}`)},
		{Type: "thread.reported", ProjectID: "prj_alpha", Data: json.RawMessage(`{"threadId":"thr_alpha","report":"ready","headline":"alpha_global_token 检索摘要"}`)},
		{Type: "artifact.created", ProjectID: "prj_alpha", Data: json.RawMessage(`{"artifactId":"art_alpha","kind":"whiteboard","title":"搜索架构 Alpha","content":"{\"nodes\":[{\"data\":{\"text\":\"alpha_global_token 搜索白板\"}}]}","author":"user"}`)},
		{Type: "message.posted", ProjectID: "prj_alpha", Data: json.RawMessage(`{"conversationId":"crd_alpha","messageId":"msg_alpha_coord","author":"user","kind":"chat","content":"alpha_global_token 搜索讨论"}`)},

		{Type: "thread.created", ProjectID: "prj_beta", Data: json.RawMessage(`{"threadId":"thr_beta","title":"跨项目搜索 Beta 实现"}`)},
		{Type: "turn.started", ProjectID: "prj_beta", Data: json.RawMessage(`{"conversationId":"thr_beta","turnId":"trn_beta","model":"m"}`)},
		{Type: "turn.finished", ProjectID: "prj_beta", Data: json.RawMessage(`{"conversationId":"thr_beta","turnId":"trn_beta","outcome":"cancelled"}`)},
		{Type: "artifact.created", ProjectID: "prj_beta", Data: json.RawMessage(`{"artifactId":"art_beta","kind":"document","title":"搜索文档 Beta","content":"alpha_global_token 搜索正文","author":"user"}`)},
		{Type: "message.posted", ProjectID: "prj_beta", Data: json.RawMessage(`{"conversationId":"thr_beta","messageId":"msg_beta_thr","author":"assistant","kind":"chat","content":"alpha_global_token 搜索回复"}`)},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Empty global search returns empty slices cleanly.
	empty, err := repo.Reader.Search(ctx, "", "   ")
	if err != nil {
		t.Fatal(err)
	}
	if len(empty["threads"].([]object)) != 0 || len(empty["artifacts"].([]object)) != 0 || len(empty["messages"].([]object)) != 0 {
		t.Fatalf("empty global search returned hits: %+v", empty)
	}

	// 1. Global search without preferProjectID: newer Beta comes first.
	globalPlain, err := repo.Reader.Search(ctx, "", "跨项目搜索")
	if err != nil {
		t.Fatal(err)
	}
	threads := globalPlain["threads"].([]object)
	if len(threads) != 2 {
		t.Fatalf("global threads len = %d, want 2", len(threads))
	}
	if threads[0]["id"] != "thr_beta" || threads[0]["projectId"] != "prj_beta" || threads[0]["projectName"] != "Beta" || threads[0]["stage"] != "halted" {
		t.Fatalf("threads[0] = %+v, want thr_beta/Beta/halted", threads[0])
	}
	if threads[1]["id"] != "thr_alpha" || threads[1]["projectId"] != "prj_alpha" || threads[1]["projectName"] != "Alpha" || threads[1]["stage"] != "review" {
		t.Fatalf("threads[1] = %+v, want thr_alpha/Alpha/review", threads[1])
	}

	// 2. Global search WITH preferProjectID = prj_alpha (long FTS5 term): Alpha pinned first despite older timestamp.
	globalPrefLong, err := repo.Reader.Search(ctx, "", "alpha_global_token", "prj_alpha")
	if err != nil {
		t.Fatal(err)
	}
	prefArtifacts := globalPrefLong["artifacts"].([]object)
	if len(prefArtifacts) != 2 || prefArtifacts[0]["id"] != "art_alpha" || prefArtifacts[0]["projectName"] != "Alpha" || prefArtifacts[1]["id"] != "art_beta" || prefArtifacts[1]["projectName"] != "Beta" {
		t.Fatalf("preferred long-term artifacts = %+v", prefArtifacts)
	}
	prefMessages := globalPrefLong["messages"].([]object)
	if len(prefMessages) != 2 || prefMessages[0]["id"] != "msg_alpha_coord" || prefMessages[0]["where"] != nil || prefMessages[1]["id"] != "msg_beta_thr" || prefMessages[1]["where"] != "跨项目搜索 Beta 实现" {
		t.Fatalf("preferred long-term messages = %+v", prefMessages)
	}

	// 3. Global search WITH preferProjectID = prj_alpha (short 2-char term -> LIKE path): Alpha pinned first too.
	globalPrefShort, err := repo.Reader.Search(ctx, "", "搜索", "prj_alpha")
	if err != nil {
		t.Fatal(err)
	}
	shortThreads := globalPrefShort["threads"].([]object)
	if len(shortThreads) != 2 || shortThreads[0]["id"] != "thr_alpha" || shortThreads[1]["id"] != "thr_beta" {
		t.Fatalf("preferred short-term threads = %+v", shortThreads)
	}
	shortArtifacts := globalPrefShort["artifacts"].([]object)
	if len(shortArtifacts) != 2 || shortArtifacts[0]["id"] != "art_alpha" || shortArtifacts[1]["id"] != "art_beta" {
		t.Fatalf("preferred short-term artifacts = %+v", shortArtifacts)
	}
	shortMessages := globalPrefShort["messages"].([]object)
	if len(shortMessages) != 2 || shortMessages[0]["id"] != "msg_alpha_coord" || shortMessages[1]["id"] != "msg_beta_thr" {
		t.Fatalf("preferred short-term messages = %+v", shortMessages)
	}

	// 4. Single-project search remains scoped to that project and includes project metadata + stage.
	scoped, err := repo.Reader.Search(ctx, "prj_alpha", "搜索")
	if err != nil {
		t.Fatal(err)
	}
	scopedThreads := scoped["threads"].([]object)
	if len(scopedThreads) != 1 || scopedThreads[0]["id"] != "thr_alpha" || scopedThreads[0]["projectName"] != "Alpha" || scopedThreads[0]["stage"] != "review" {
		t.Fatalf("scoped threads = %+v", scopedThreads)
	}

	// 5. Verify SQLite query planner uses search_docs_kind_at for global short-term scan.
	planRows, err := records(ctx, repo.Store.DB(), `EXPLAIN QUERY PLAN
	 SELECT d.project_id, d.kind, d.ref_id, d.conversation_id, d.at, t.title, t.body
	 FROM search_text t JOIN search_docs d ON d.id = t.rowid
	 WHERE d.kind = ? AND (t.title LIKE ? ESCAPE '\' OR t.body LIKE ? ESCAPE '\')
	 ORDER BY d.at DESC LIMIT 10`, "message", "%搜索%", "%搜索%")
	if err != nil {
		t.Fatal(err)
	}
	usedIndex := false
	for _, row := range planRows {
		if strings.Contains(row.text("detail"), "search_docs_kind_at") {
			usedIndex = true
		}
	}
	if !usedIndex {
		t.Fatalf("expected EXPLAIN QUERY PLAN to use search_docs_kind_at, got: %+v", planRows)
	}
}
