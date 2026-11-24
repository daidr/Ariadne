package runner

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/internal/collab"
	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/files"
)

func TestWhiteboardPrototypeStandaloneEmbeddedAndLargeFileStorage(t *testing.T) {
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

	smallHTML := `<!doctype html>
<html>
<head>
  <script type="application/json" data-ariadne-tweaks>
  [
    {"key":"accent","label":"主色调","kind":"color","default":"#2563eb","section":"主题","swatches":["#2563eb","#0f766e","#ea580c"]},
    {"key":"density","label":"密度","kind":"enum","default":"comfortable","options":[{"value":"compact","label":"紧凑"},{"value":"comfortable","label":"舒适"}]},
    {"key":"radius","label":"圆角","kind":"range","min":4,"max":20,"step":2,"unit":"px","default":10},
    {"key":"showBadge","label":"显示徽标","kind":"boolean","default":true},
    {"key":"ctaLabel","label":"按钮文案","kind":"text","default":"开始使用"}
  ]
  </script>
</head>
<body><button>开始使用</button></body>
</html>`

	largeHTML := "<!doctype html><html><body><div>" + strings.Repeat("A", inlinePrototypeThreshold+2048) + "</div></body></html>"

	created, err := agent.createWhiteboard(ctx, args{
		"title": "原型工作台",
		"items": []any{
			args{
				"id":             "proto-standalone",
				"kind":           "prototype",
				"title":          "独立移动原型",
				"html":           smallHTML,
				"viewportWidth":  float64(390),
				"viewportHeight": float64(844),
			},
			args{
				"id":    "proto-large",
				"kind":  "prototype",
				"title": "大体积原型",
				"html":  largeHTML,
			},
		},
		"groups": []any{
			args{"id": "grp-home", "title": "首页布局", "pick": "one", "round": float64(1)},
		},
		"options": []any{
			args{
				"id":          "opt-live",
				"title":       "交互工作台",
				"group":       "grp-home",
				"recommended": true,
				"prototype": args{
					"html":           smallHTML,
					"viewportWidth":  float64(1280),
					"viewportHeight": float64(800),
				},
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

	standalone := byID["proto-standalone"]
	if standalone == nil || text(standalone, "type") != "prototype" {
		t.Fatalf("missing standalone prototype node: %+v", standalone)
	}
	sData := objectField(standalone, "data")
	if text(sData, "html") == "" || text(sData, "src") != "" {
		t.Fatalf("expected small prototype to be stored inline, got data=%+v", sData)
	}
	tweaks, _ := sData["tweaks"].([]any)
	if len(tweaks) != 5 {
		t.Fatalf("expected 5 extracted tweaks, got %+v", tweaks)
	}

	large := byID["proto-large"]
	if large == nil {
		t.Fatalf("missing large prototype node")
	}
	lData := objectField(large, "data")
	if text(lData, "html") != "" || !strings.HasPrefix(text(lData, "src"), "/api/files/") {
		t.Fatalf("expected large prototype to spill to project file storage, got data=%+v", lData)
	}

	embedded := byID["opt-live-prototype"]
	if embedded == nil || text(embedded, "parent") != "opt-live" || text(embedded, "type") != "prototype" {
		t.Fatalf("expected embedded prototype inside opt-live, got %+v", embedded)
	}

	// Verify read_artifact summarizes prototypes without dumping raw HTML bodies.
	readOut, err := agent.readArtifact(ctx, args{"artifactId": artifactID})
	if err != nil {
		t.Fatalf("readArtifact failed: %v", err)
	}
	if strings.Contains(readOut, strings.Repeat("A", 100)) {
		t.Fatal("readArtifact should not dump raw prototype HTML body")
	}
	if !strings.Contains(readOut, `"htmlBytes"`) || !strings.Contains(readOut, `"accent"`) {
		t.Fatalf("expected readArtifact summary to include htmlBytes and tweaks, got:\n%s", readOut)
	}

	// Verify update_whiteboard preserves prototype kind when updating an existing prototype via file without specifying kind.
	updatedHTML := `<!doctype html><html><body><button>V2 原型</button></body></html>`
	fileStore := files.Store{Events: repo.Store}
	savedFile, err := fileStore.Save(ctx, project.ID, "text/plain", []byte(updatedHTML), true)
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	if _, err := agent.updateWhiteboard(ctx, args{
		"artifactId": artifactID,
		"reason":     "update prototype from project file without repeating kind",
		"items": []any{
			args{
				"id":   "proto-standalone",
				"file": savedFile.ID,
			},
		},
	}); err != nil {
		t.Fatalf("updateWhiteboard prototype via file without kind failed: %v", err)
	}
	updatedArtifact, err := repo.GetArtifact(ctx, artifactID, nil)
	if err != nil {
		t.Fatal(err)
	}
	var updatedBoard whiteboard
	if err := json.Unmarshal([]byte(updatedArtifact.Content), &updatedBoard); err != nil {
		t.Fatal(err)
	}
	updatedNode := boardItems(updatedBoard.Nodes)["proto-standalone"]
	if updatedNode == nil || text(updatedNode, "type") != "prototype" {
		t.Fatalf("expected proto-standalone to remain a prototype node, got %+v", updatedNode)
	}
	uData := objectField(updatedNode, "data")
	if !strings.Contains(text(uData, "html"), "V2 原型") {
		t.Fatalf("expected updated prototype HTML to contain V2 原型, got data=%+v", uData)
	}
}
