package core

import (
	"encoding/json"
	"testing"
	"time"
)

func TestArtifactGalleryPreviewSearchAndFilters(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	now := time.UnixMilli(1800000000000)
	repo, err := Open(ctx, Options{
		Path: ":memory:",
		Now: func() time.Time {
			now = now.Add(time.Minute)
			return now
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	prjA, err := repo.CreateProject(ctx, new("Ariadne 主工程"), ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	prjB, err := repo.CreateProject(ctx, new("DouUI 组件库"), ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	threadA, err := repo.CreateThread(ctx, prjA.ID, NewThread{Title: "画廊架构设计串"})
	if err != nil {
		t.Fatal(err)
	}
	nodeID := "node_gallery"
	if _, err := repo.RevisePlan(ctx, prjA.ID, PlanRevision{
		Author: prjA.CoordinatorID,
		Reason: "add gallery node",
		Ops:    []map[string]any{planAdd(nodeID, "产物画廊节点")},
	}); err != nil {
		t.Fatal(err)
	}

	// 1. Document with an active (unconfirmed) inline ask block -> pending_actions = 1 (needs_me).
	docContent := "# 画廊方案\n\n正文内容。\n\n```ask\n{\"id\":\"entry\",\"question\":\"入口放哪里？\",\"pick\":\"one\",\"options\":[{\"id\":\"a\",\"label\":\"侧栏顶部\"}]}\n```\n"
	docArt, err := repo.CreateArtifact(ctx, prjA.ID, NewArtifact{
		Kind:    "document",
		Title:   "Beta 画廊设计方案",
		NodeID:  new(nodeID),
		Author:  threadA.ID,
		Content: docContent,
	})
	if err != nil {
		t.Fatal(err)
	}

	// 2. Whiteboard with prototype node, preview image, option node, and unconfirmed decision group.
	protoBoard := object{
		"version": 1,
		"nodes": []any{
			object{"id": "grp-1", "type": "group", "x": 0, "y": 0, "width": 860, "height": 620, "data": object{"title": "主视觉方案", "mode": "choose", "round": 1}},
			object{"id": "opt-1", "type": "option", "parent": "grp-1", "x": 20, "y": 60, "width": 520, "height": 420, "data": object{"title": "方案 A 极简网格", "note": "强调低心智负担卡片密度"}},
			object{"id": "proto-1", "type": "prototype", "parent": "opt-1", "x": 40, "y": 110, "width": 480, "height": 336, "data": object{"title": "桌面端画廊交互原型", "note": "支持深浅主题切换与速览抽屉", "html": "<div>ok</div>", "preview": "/api/files/fil_preview_ok"}},
		},
		"edges": []any{},
	}
	protoBoardBytes, _ := json.Marshal(protoBoard)
	protoArt, err := repo.CreateArtifact(ctx, prjA.ID, NewArtifact{
		Kind:    "whiteboard",
		Title:   "Alpha 交互设计稿",
		Author:  prjA.CoordinatorID,
		Content: string(protoBoardBytes),
	})
	if err != nil {
		t.Fatal(err)
	}

	// 3. Regular architecture whiteboard (no prototype) in Project B with a comment & feedback round.
	archBoard := object{
		"version": 1,
		"nodes": []any{
			object{"id": "n1", "type": "shape", "x": 10, "y": 20, "width": 160, "height": 80, "data": object{"text": "VirtualList"}},
			object{"id": "n2", "type": "shape", "x": 240, "y": 20, "width": 160, "height": 80, "data": object{"text": "RowChunker"}},
		},
		"edges": []any{
			object{"id": "e1", "source": object{"node": "n1", "port": "right"}, "target": object{"node": "n2", "port": "left"}},
		},
	}
	archBoardBytes, _ := json.Marshal(archBoard)
	archArt, err := repo.CreateArtifact(ctx, prjB.ID, NewArtifact{
		Kind:    "whiteboard",
		Title:   "Gamma 虚拟列表架构图",
		Author:  "user",
		Content: string(archBoardBytes),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CommentOnArtifact(ctx, archArt.ID, "user", map[string]any{
		"type":      "create",
		"threadId":  "thr-c1",
		"commentId": "cmt-1",
		"body":      "补充一下 overscan 边界",
		"quote":     "",
		"anchor": map[string]any{
			"node": "n1",
		},
	}); err != nil {
		t.Fatal(err)
	}

	// 4. Live dashboard in Project B with active pending feedback round.
	dashContent := `{"version":1,"dashboard":{"version":1,"widgets":[{"id":"w1","type":"stat","title":"P95 延迟","props":{"source":"m"},"place":{"lg":{"x":0,"y":0,"w":4,"h":2}}}]},"data":{"m":[]},"source":{"kind":"metrics","settings":{}},"takenAt":1800000005000}`
	dashArt, err := repo.CreateArtifact(ctx, prjB.ID, NewArtifact{
		Kind:    "dashboard",
		Title:   "Delta 推理成本看板",
		Author:  prjB.CoordinatorID,
		Content: dashContent,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SendArtifactFeedback(ctx, dashArt.ID, ArtifactFeedback{
		Items: []any{object{"kind": "comment", "body": "增加按项目分组维度"}},
	}); err != nil {
		t.Fatal(err)
	}

	// Verify enhanced whiteboard preview & dashboard isLive.
	gotProto, err := repo.GetArtifact(ctx, protoArt.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	protoPrev := asObject(gotProto.Preview)
	if int(protoPrev["prototypeCount"].(float64)) != 1 {
		t.Fatalf("prototypeCount = %v, want 1", protoPrev["prototypeCount"])
	}
	if int(protoPrev["optionCount"].(float64)) != 1 {
		t.Fatalf("optionCount = %v, want 1", protoPrev["optionCount"])
	}
	if protoPrev.str("previewImage") != "/api/files/fil_preview_ok" {
		t.Fatalf("previewImage = %q, want /api/files/fil_preview_ok", protoPrev.str("previewImage"))
	}
	if boxes, _ := protoPrev["minimap"].([]any); len(boxes) != 3 {
		t.Fatalf("minimap len = %d, want 3", len(boxes))
	}
	if texts, _ := protoPrev["texts"].([]any); len(texts) != 3 {
		t.Fatalf("texts = %v, want 3 titles", texts)
	}

	gotDash, err := repo.GetArtifact(ctx, dashArt.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !asObject(gotDash.Preview).yes("isLive") {
		t.Fatalf("expected dashboard preview isLive = true, got %+v", gotDash.Preview)
	}

	// Verify cross-project listing with counts and metadata resolution.
	allPage, err := repo.ListGalleryArtifacts(ctx, GalleryArtifactsQuery{IncludeCounts: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(allPage.Items) != 4 || allPage.HasMore {
		t.Fatalf("allPage len=%d hasMore=%v, want 4/false", len(allPage.Items), allPage.HasMore)
	}
	if allPage.Counts == nil ||
		allPage.Counts.Total != 4 ||
		allPage.Counts.Documents != 1 ||
		allPage.Counts.Whiteboards != 2 ||
		allPage.Counts.Dashboards != 1 ||
		allPage.Counts.Prototypes != 1 ||
		allPage.Counts.NeedsMe != 3 || // docArt (ask) + protoArt (decision group) + archArt (open comment)
		allPage.Counts.FeedbackPending != 1 ||
		len(allPage.Counts.Projects) != 2 {
		t.Fatalf("unexpected gallery counts: %+v", allPage.Counts)
	}

	byID := map[string]GalleryArtifactSummary{}
	for _, item := range allPage.Items {
		byID[item.Artifact.ID] = item
	}
	if byID[docArt.ID].UpdatedByLabel != "画廊架构设计串" || byID[docArt.ID].NodeTitle != "产物画廊节点" || byID[docArt.ID].PendingActions != 1 {
		t.Fatalf("doc metadata mismatch: %+v", byID[docArt.ID])
	}
	if !byID[protoArt.ID].HasPrototype || !byID[protoArt.ID].UpdatedByCoordinator || byID[protoArt.ID].PendingActions != 1 || byID[protoArt.ID].ProjectName != "Ariadne 主工程" {
		t.Fatalf("proto metadata mismatch: %+v", byID[protoArt.ID])
	}
	if byID[dashArt.ID].PendingFeedback != 1 || byID[dashArt.ID].ProjectName != "DouUI 组件库" {
		t.Fatalf("dash metadata mismatch: %+v", byID[dashArt.ID])
	}

	// Virtual prototype filter returns only protoArt.
	protoOnly, err := repo.ListGalleryArtifacts(ctx, GalleryArtifactsQuery{OnlyPrototypes: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(protoOnly.Items) != 1 || protoOnly.Items[0].Artifact.ID != protoArt.ID {
		t.Fatalf("OnlyPrototypes = %+v, want only %s", protoOnly.Items, protoArt.ID)
	}

	// Status filters.
	needsMePage, err := repo.ListGalleryArtifacts(ctx, GalleryArtifactsQuery{Status: "needs_me"})
	if err != nil {
		t.Fatal(err)
	}
	if len(needsMePage.Items) != 3 {
		t.Fatalf("needs_me count = %d, want 3", len(needsMePage.Items))
	}
	commentsPage, err := repo.ListGalleryArtifacts(ctx, GalleryArtifactsQuery{Status: "has_comments"})
	if err != nil {
		t.Fatal(err)
	}
	if len(commentsPage.Items) != 1 || commentsPage.Items[0].Artifact.ID != archArt.ID {
		t.Fatalf("has_comments = %+v, want only %s", commentsPage.Items, archArt.ID)
	}
	fbPage, err := repo.ListGalleryArtifacts(ctx, GalleryArtifactsQuery{Status: "feedback_pending"})
	if err != nil {
		t.Fatal(err)
	}
	if len(fbPage.Items) != 1 || fbPage.Items[0].Artifact.ID != dashArt.ID {
		t.Fatalf("feedback_pending = %+v, want only %s", fbPage.Items, dashArt.ID)
	}
	if _, err := repo.ListGalleryArtifacts(ctx, GalleryArtifactsQuery{Status: "unknown"}); err == nil {
		t.Fatalf("expected error for invalid status filter")
	}

	// Search by prototype node note (previously unindexed in boardWords) and by project name.
	searchProto, err := repo.ListGalleryArtifacts(ctx, GalleryArtifactsQuery{Query: "速览抽屉"})
	if err != nil {
		t.Fatal(err)
	}
	if len(searchProto.Items) != 1 || searchProto.Items[0].Artifact.ID != protoArt.ID {
		t.Fatalf("search by prototype note = %+v, want only %s", searchProto.Items, protoArt.ID)
	}
	searchProj, err := repo.ListGalleryArtifacts(ctx, GalleryArtifactsQuery{Query: "DouUI"})
	if err != nil {
		t.Fatal(err)
	}
	if len(searchProj.Items) != 2 {
		t.Fatalf("search by project name count = %d, want 2", len(searchProj.Items))
	}

	// Multi-page pagination across all sort modes: updated_desc, created_desc, title_asc.
	for _, tc := range []struct {
		sort      string
		wantOrder []string
	}{
		{"updated_desc", []string{dashArt.ID, archArt.ID, protoArt.ID, docArt.ID}},
		{"created_desc", []string{dashArt.ID, archArt.ID, protoArt.ID, docArt.ID}},
		{"title_asc", []string{protoArt.ID, docArt.ID, dashArt.ID, archArt.ID}}, // Alpha, Beta, Delta, Gamma
	} {
		var collected []string
		token := ""
		for {
			page, err := repo.ListGalleryArtifacts(ctx, GalleryArtifactsQuery{
				Sort:      tc.sort,
				PageSize:  2,
				PageToken: token,
			})
			if err != nil {
				t.Fatalf("sort=%s: %v", tc.sort, err)
			}
			for _, item := range page.Items {
				collected = append(collected, item.Artifact.ID)
			}
			if !page.HasMore {
				break
			}
			token = page.NextPageToken
		}
		if len(collected) != len(tc.wantOrder) {
			t.Fatalf("sort=%s collected=%v want=%v", tc.sort, collected, tc.wantOrder)
		}
		for i := range tc.wantOrder {
			if collected[i] != tc.wantOrder[i] {
				t.Fatalf("sort=%s at %d: got %s want %s (all=%v)", tc.sort, i, collected[i], tc.wantOrder[i], collected)
			}
		}
	}
}
