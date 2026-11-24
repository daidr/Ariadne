package runner

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/internal/collab"
	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/files"
)

func TestWhiteboardDecisionGroupsAndRounds(t *testing.T) {
	ctx := t.Context()
	repo, err := core.Open(ctx, core.Options{Path: filepath.Join(t.TempDir(), "app.sqlite")})
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
	ctx := t.Context()
	tmp := t.TempDir()
	repo, err := core.Open(ctx, core.Options{Path: filepath.Join(tmp, "app.sqlite")})
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
