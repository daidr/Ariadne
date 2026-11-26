package runner

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/internal/collab"
	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/files"
)

func TestWhiteboardDecisionGroupsAndRounds(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	repo, err := core.Open(ctx, core.Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	rooms := collab.NewRooms(repo, nil)
	defer rooms.Close()

	project, err := repo.CreateProject(ctx, new("Fixture"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	agent := &turn{
		Runner: &Runner{Repository: repo, Rooms: rooms},
		conversation: core.ConversationView{
			ID:        project.CoordinatorID,
			ProjectID: project.ID,
			Role:      "coordinator",
		},
	}

	// Round 1: create board with 2 decision groups and recommended options.
	created, err := agent.createWhiteboard(ctx, args{
		"title": "Design Decisions",
		"groups": []any{
			args{"id": "grp-nav", "title": "Navigation", "note": "Choose top-level nav", "pick": "one", "round": float64(1)},
			args{"id": "grp-accent", "title": "Accents", "pick": "many", "round": float64(1)},
		},
		"options": []any{
			args{"id": "nav-side", "title": "Sidebar", "group": "grp-nav", "recommended": true, "text": "Collapsible rail"},
			args{"id": "nav-top", "title": "Top Bar", "group": "grp-nav", "text": "Horizontal header"},
			args{"id": "acc-warm", "title": "Warm Amber", "group": "grp-accent", "recommended": true},
			args{"id": "acc-cool", "title": "Cool Slate", "group": "grp-accent"},
		},
	})
	if err != nil {
		t.Fatalf("createWhiteboard failed: %v", err)
	}
	var createReply args
	if err := json.Unmarshal([]byte(created), &createReply); err != nil {
		t.Fatal(err)
	}
	artifactID := text(createReply, "artifactId")
	if artifactID == "" {
		t.Fatalf("missing artifactId in reply: %s", created)
	}

	// Round 2: append a new round group via update_whiteboard.
	if _, err := agent.updateWhiteboard(ctx, args{
		"artifactId": artifactID,
		"reason":     "Add round 2 density decision",
		"groups": []any{
			args{"id": "grp-density", "title": "Density", "round": float64(2), "pick": "one"},
		},
		"options": []any{
			args{"id": "den-compact", "title": "Compact", "group": "grp-density", "recommended": true},
			args{"id": "den-airy", "title": "Airy", "group": "grp-density"},
		},
	}); err != nil {
		t.Fatalf("updateWhiteboard round 2 failed: %v", err)
	}

	artifact, err := repo.GetArtifact(ctx, artifactID, nil)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Nodes []struct {
			ID     string         `json:"id"`
			Type   string         `json:"type"`
			Parent string         `json:"parent"`
			Y      float64        `json:"y"`
			Height float64        `json:"height"`
			Data   map[string]any `json:"data"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal([]byte(artifact.Content), &doc); err != nil {
		t.Fatal(err)
	}
	byID := map[string]int{}
	for i, n := range doc.Nodes {
		byID[n.ID] = i
	}
	navGroup := doc.Nodes[byID["grp-nav"]]
	densityGroup := doc.Nodes[byID["grp-density"]]
	if confirmed, _ := navGroup.Data["confirmed"].(bool); !confirmed {
		t.Fatalf("expected round 1 group grp-nav to be auto-confirmed when round 2 is appended, data=%+v", navGroup.Data)
	}
	if confirmed, _ := densityGroup.Data["confirmed"].(bool); confirmed {
		t.Fatalf("expected round 2 group grp-density to remain unconfirmed, data=%+v", densityGroup.Data)
	}
	if densityGroup.Y < navGroup.Y+navGroup.Height {
		t.Fatalf("expected round 2 group to be placed vertically below round 1 (r1 bottom=%.1f, r2 y=%.1f)", navGroup.Y+navGroup.Height, densityGroup.Y)
	}
	navSide := doc.Nodes[byID["nav-side"]]
	if navSide.Parent != "grp-nav" || navSide.Data["recommended"] != true || navSide.Data["group"] != "grp-nav" {
		t.Fatalf("unexpected option node nav-side: %+v", navSide)
	}

	// read_artifact should summarize group metadata and recommended flag cleanly.
	summary, err := agent.readArtifact(ctx, args{"artifactId": artifactID})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"grp-nav", "grp-density", `"recommended":true`} {
		if !strings.Contains(summary, want) {
			t.Fatalf("expected readArtifact summary to contain %q, got:\n%s", want, summary)
		}
	}
}

func TestWhiteboardStandaloneImagesAndBaselineComparison(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	repo, err := core.Open(ctx, core.Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	rooms := collab.NewRooms(repo, nil)
	defer rooms.Close()
	store := files.Store{Events: repo.Store}

	project, err := repo.CreateProject(ctx, new("Fixture"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}

	// Save a tiny valid PNG into the project file store.
	img := image.NewRGBA(image.Rect(0, 0, 120, 80))
	img.Set(0, 0, color.RGBA{R: 60, G: 130, B: 240, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	saved, err := store.Save(ctx, project.ID, "image/png", buf.Bytes(), true)
	if err != nil {
		t.Fatal(err)
	}

	agent := &turn{
		Runner: &Runner{Repository: repo, Rooms: rooms},
		conversation: core.ConversationView{
			ID:        project.CoordinatorID,
			ProjectID: project.ID,
			Role:      "coordinator",
		},
	}

	created, err := agent.createWhiteboard(ctx, args{
		"title": "视觉对照与参考图",
		"items": []any{
			args{"id": "ref-shot", "kind": "image", "file": saved.ID, "alt": "设计参考截图"},
		},
		"groups": []any{
			args{"id": "grp-compare", "title": "首页改动对照", "round": float64(1), "note": "请在图上直接打点评论"},
		},
		"options": []any{
			args{
				"id":             "v-hero",
				"title":          "Hero 区域",
				"group":          "grp-compare",
				"baselineImages": []any{args{"file": saved.ID, "alt": "设计稿"}},
				"images":         []any{args{"file": saved.ID, "alt": "当前实现"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("createWhiteboard failed: %v", err)
	}
	var createReply args
	if err := json.Unmarshal([]byte(created), &createReply); err != nil {
		t.Fatal(err)
	}
	artifactID := text(createReply, "artifactId")
	artifact, err := repo.GetArtifact(ctx, artifactID, nil)
	if err != nil {
		t.Fatal(err)
	}

	var doc struct {
		Nodes []struct {
			ID     string         `json:"id"`
			Type   string         `json:"type"`
			Parent string         `json:"parent"`
			Width  float64        `json:"width"`
			Height float64        `json:"height"`
			Data   map[string]any `json:"data"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal([]byte(artifact.Content), &doc); err != nil {
		t.Fatal(err)
	}
	byID := map[string]int{}
	for i, n := range doc.Nodes {
		byID[n.ID] = i
	}
	imgNode := doc.Nodes[byID["ref-shot"]]
	if imgNode.Type != "image" {
		t.Fatalf("expected ref-shot to be type image, got %+v", imgNode)
	}
	if src, _ := imgNode.Data["src"].(string); !strings.HasPrefix(src, "/api/files/") {
		t.Fatalf("expected uploaded image src, got %q", src)
	}
	if alt, _ := imgNode.Data["alt"].(string); alt != "设计参考截图" {
		t.Fatalf("expected alt to be preserved, got %q", alt)
	}
	if _, ok := byID["v-hero-baseline-1"]; !ok {
		t.Fatalf("expected side-by-side baseline image node v-hero-baseline-1, got nodes=%+v", byID)
	}
	if _, ok := byID["v-hero-image-1"]; !ok {
		t.Fatalf("expected side-by-side actual image node v-hero-image-1, got nodes=%+v", byID)
	}
}

func TestWhiteboardOptionNoteInsetAndPureTextHeight(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	repo, err := core.Open(ctx, core.Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	rooms := collab.NewRooms(repo, nil)
	defer rooms.Close()

	project, err := repo.CreateProject(ctx, new("Fixture"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	agent := &turn{
		Runner: &Runner{Repository: repo, Rooms: rooms},
		conversation: core.ConversationView{
			ID:        project.CoordinatorID,
			ProjectID: project.ID,
			Role:      "coordinator",
		},
	}

	longMixedText := "采用三栏工作台：左侧对话列表 + 中间主内容区 + 右侧产物预览面板，支持 Responsive 自适应折叠与 Keyboard 快捷键切换。\n第二段补充说明：验证中英文混排、全角标点「——」以及多行段落底部不会出现裁切。"
	longNote := "适合高频多任务切换场景，将标题与说明分层展示并支持自动换行，避免溢出到右侧相邻决策卡片。"

	created, err := agent.createWhiteboard(ctx, args{
		"title": "决策选项排版验证",
		"groups": []any{
			args{"id": "grp-1", "title": "方案对比", "pick": "one", "round": float64(1)},
		},
		"options": []any{
			args{
				"id":          "opt-plain",
				"title":       "纯文字方案",
				"group":       "grp-1",
				"recommended": true,
				"text":        longMixedText,
			},
			args{
				"id":    "opt-with-note",
				"title": "带长说明方案",
				"group": "grp-1",
				"note":  longNote,
				"text":  longMixedText,
			},
		},
	})
	if err != nil {
		t.Fatalf("createWhiteboard failed: %v", err)
	}
	var createReply args
	if err := json.Unmarshal([]byte(created), &createReply); err != nil {
		t.Fatal(err)
	}
	artifactID := text(createReply, "artifactId")
	artifact, err := repo.GetArtifact(ctx, artifactID, nil)
	if err != nil {
		t.Fatal(err)
	}

	var board whiteboard
	if err := json.Unmarshal([]byte(artifact.Content), &board); err != nil {
		t.Fatal(err)
	}
	byID := boardItems(board.Nodes)

	optPlain := byID["opt-plain"]
	textPlain := byID["opt-plain-text"]
	optWithNote := byID["opt-with-note"]
	textWithNote := byID["opt-with-note-text"]
	if optPlain == nil || textPlain == nil || optWithNote == nil || textWithNote == nil {
		t.Fatalf("expected option and text child nodes to exist")
	}

	// Without note, child starts at y + 40.
	if got := asNumber(textPlain["y"]) - asNumber(optPlain["y"]); got != 40 {
		t.Fatalf("expected plain option child inset 40, got %.1f", got)
	}
	// With multi-line note, child starts well below 40 (at least 88 for a 2+ line note).
	noteInset := asNumber(textWithNote["y"]) - asNumber(optWithNote["y"])
	if noteInset < 88 {
		t.Fatalf("expected option with multi-line note to reserve >= 88px header inset, got %.1f", noteInset)
	}
	// Text node height must use 24px line height + padding cushion and fit inside its option frame.
	wantTextH := boardTextNodeHeight(longMixedText, asNumber(textPlain["width"]))
	if got := asNumber(textPlain["height"]); got != wantTextH || got < 160 {
		t.Fatalf("expected generous pure-text height %.1f (>=160), got %.1f", wantTextH, got)
	}
	if asNumber(textPlain["y"])+asNumber(textPlain["height"])+16 > asNumber(optPlain["y"])+asNumber(optPlain["height"]) {
		t.Fatalf("expected plain option card height (%.1f) to leave bottom padding below text child", asNumber(optPlain["height"]))
	}
	if asNumber(textWithNote["y"])+asNumber(textWithNote["height"])+16 > asNumber(optWithNote["y"])+asNumber(optWithNote["height"]) {
		t.Fatalf("expected noted option card height (%.1f) to leave bottom padding below text child", asNumber(optWithNote["height"]))
	}
	// Layout problem checker should report zero issues.
	if problems := board.problems(); len(problems) > 0 {
		t.Fatalf("expected zero whiteboard layout problems, got: %v", problems)
	}

	// Updating without passing note preserves existing note inset; clearing note resets inset to 40.
	if _, err := agent.updateWhiteboard(ctx, args{
		"artifactId": artifactID,
		"reason":     "update text while keeping existing note",
		"options": []any{
			args{"id": "opt-with-note", "title": "带长说明方案", "group": "grp-1", "text": "更新后的正文"},
		},
	}); err != nil {
		t.Fatalf("updateWhiteboard preserve note failed: %v", err)
	}
	updatedArt, err := repo.GetArtifact(ctx, artifactID, nil)
	if err != nil {
		t.Fatal(err)
	}
	var updatedBoard whiteboard
	if err := json.Unmarshal([]byte(updatedArt.Content), &updatedBoard); err != nil {
		t.Fatal(err)
	}
	uByID := boardItems(updatedBoard.Nodes)
	if got := asNumber(uByID["opt-with-note-text"]["y"]) - asNumber(uByID["opt-with-note"]["y"]); got != noteInset {
		t.Fatalf("expected preserved note inset %.1f, got %.1f", noteInset, got)
	}

	if _, err := agent.updateWhiteboard(ctx, args{
		"artifactId": artifactID,
		"reason":     "clear note",
		"options": []any{
			args{"id": "opt-with-note", "title": "带长说明方案", "group": "grp-1", "note": "", "text": "更新后的正文"},
		},
	}); err != nil {
		t.Fatalf("updateWhiteboard clear note failed: %v", err)
	}
	clearedArt, err := repo.GetArtifact(ctx, artifactID, nil)
	if err != nil {
		t.Fatal(err)
	}
	var clearedBoard whiteboard
	if err := json.Unmarshal([]byte(clearedArt.Content), &clearedBoard); err != nil {
		t.Fatal(err)
	}
	cByID := boardItems(clearedBoard.Nodes)
	if got := asNumber(cByID["opt-with-note-text"]["y"]) - asNumber(cByID["opt-with-note"]["y"]); got != 40 {
		t.Fatalf("expected cleared note inset 40, got %.1f", got)
	}
	if _, exists := objectField(cByID["opt-with-note"], "data")["note"]; exists {
		t.Fatalf("expected empty note to be deleted from option data")
	}
}
