package runner

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/internal/collab"
	"github.com/daidr/Ariadne/internal/core"
)

func TestWhiteboardColorsAndBorderlessCreateUpdateAndRead(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	repo, err := core.Open(ctx, core.Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	rooms := collab.NewRooms(repo, nil)
	defer rooms.Close()
	project, err := repo.CreateProject(ctx, new("BoardColors"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	agent := &turn{Runner: &Runner{Repository: repo, Rooms: rooms}, conversation: core.ConversationView{
		ID: project.CoordinatorID, ProjectID: project.ID, Role: "coordinator",
	}}

	createdRaw, err := agent.createWhiteboard(ctx, args{
		"title": "Color Board",
		"items": []any{
			args{"id": "card", "kind": "rect", "text": "Borderless Card", "fill": "Blue", "stroke": "transparent", "textColor": "#0369a1"},
			args{"id": "legacy", "kind": "ellipse", "text": "Legacy Tone", "tone": "primary"},
			args{"id": "note", "kind": "sticky", "text": "Sticky", "fill": "pink", "textColor": "red"},
			args{"id": "label", "kind": "text", "text": "Title", "color": "oklch(0.55 0.2 260)"},
		},
		"connectors": []any{
			args{"id": "link", "from": "card", "to": "legacy", "color": "#dc2626", "arrow": "end"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var created args
	if err := json.Unmarshal([]byte(createdRaw), &created); err != nil {
		t.Fatal(err)
	}
	artifactID := text(created, "artifactId")
	if artifactID == "" {
		t.Fatalf("missing artifactId: %s", createdRaw)
	}

	readRaw, err := agent.readArtifact(ctx, args{"artifactId": artifactID})
	if err != nil {
		t.Fatal(err)
	}
	var summary struct {
		Version    int    `json:"version"`
		Items      []args `json:"items"`
		Connectors []args `json:"connectors"`
	}
	if err := json.Unmarshal([]byte(readRaw), &summary); err != nil {
		t.Fatal(err)
	}
	byID := boardItems(summary.Items)
	if text(byID["card"], "fill") != "blue" || text(byID["card"], "stroke") != "none" || text(byID["card"], "textColor") != "#0369a1" {
		t.Fatalf("unexpected card summary: %+v", byID["card"])
	}
	if text(byID["note"], "fill") != "pink" || text(byID["note"], "textColor") != "red" {
		t.Fatalf("unexpected sticky summary: %+v", byID["note"])
	}
	if text(byID["label"], "color") != "oklch(0.55 0.2 260)" {
		t.Fatalf("unexpected text summary: %+v", byID["label"])
	}
	edgeByID := boardItems(summary.Connectors)
	if text(edgeByID["link"], "color") != "#dc2626" {
		t.Fatalf("unexpected connector summary: %+v", edgeByID["link"])
	}

	// Update text only on card (preserves colors), remove border on legacy tone node
	// (migrating tone into fill), clear label and connector colors, and test ink recoloring.
	if _, err := agent.updateWhiteboard(ctx, args{
		"artifactId":  artifactID,
		"baseVersion": float64(summary.Version),
		"reason":      "adjust colors and borderless state",
		"items": []any{
			args{"id": "card", "text": "Still Borderless"},
			args{"id": "legacy", "stroke": "none"},
			args{"id": "note", "fill": "", "textColor": ""},
			args{"id": "label", "color": ""},
		},
		"connectors": []any{
			args{"id": "link", "from": "card", "to": "legacy", "color": ""},
		},
	}); err != nil {
		t.Fatal(err)
	}

	stored, err := repo.GetArtifact(ctx, artifactID, nil)
	if err != nil {
		t.Fatal(err)
	}
	var board whiteboard
	if err := json.Unmarshal([]byte(stored.Content), &board); err != nil {
		t.Fatal(err)
	}
	nodes := boardItems(board.Nodes)
	cardData := objectField(nodes["card"], "data")
	if text(cardData, "text") != "Still Borderless" || text(cardData, "fill") != "blue" || text(cardData, "stroke") != "none" || text(cardData, "textColor") != "#0369a1" {
		t.Fatalf("text edit lost card colors: %+v", cardData)
	}
	legacyData := objectField(nodes["legacy"], "data")
	if text(legacyData, "tone") != "" || text(legacyData, "fill") != "primary" || text(legacyData, "stroke") != "none" {
		t.Fatalf("legacy tone did not migrate to fill when clearing border: %+v", legacyData)
	}
	noteData := objectField(nodes["note"], "data")
	if text(noteData, "fill") != "" || text(noteData, "textColor") != "" {
		t.Fatalf("sticky colors were not cleared: %+v", noteData)
	}
	labelData := objectField(nodes["label"], "data")
	if text(labelData, "color") != "" {
		t.Fatalf("text color was not cleared: %+v", labelData)
	}
	linkData := objectField(boardItems(board.Edges)["link"], "data")
	if text(linkData, "color") != "" || text(linkData, "tone") != "" {
		t.Fatalf("connector color was not cleared: %+v", linkData)
	}
}

func TestWhiteboardColorValidationAndInkRecolor(t *testing.T) {
	t.Parallel()
	board := whiteboard{
		Version: 1,
		Nodes: []args{
			{"id": "draw", "type": "ink", "x": 10, "y": 20, "data": args{"points": []args{{"x": 0, "y": 0}}, "tone": "danger"}},
		},
		Edges: []args{},
	}

	// Ink node allows recoloring via color (clearing legacy tone), while rejecting shape fill/stroke.
	if err := board.change([]args{{"id": "draw", "color": "purple"}}, nil, nil, nil, "", false, nil, nil); err != nil {
		t.Fatal(err)
	}
	drawData := objectField(boardItems(board.Nodes)["draw"], "data")
	if text(drawData, "color") != "purple" || text(drawData, "tone") != "" {
		t.Fatalf("ink recolor failed: %+v", drawData)
	}
	if err := board.change([]args{{"id": "draw", "fill": "blue"}}, nil, nil, nil, "", false, nil, nil); err == nil {
		t.Fatal("expected ink fill edit to fail")
	}

	// Reject unsafe CSS colors and "none" on text/connector channels.
	invalidCases := []struct {
		name       string
		items      []args
		connectors []args
	}{
		{name: "unsafe url()", items: []args{{"id": "bad1", "kind": "rect", "fill": "url(https://evil.example/x)"}}},
		{name: "css-wide keyword", items: []args{{"id": "bad2", "kind": "rect", "stroke": "inherit"}}},
		{name: "css injection", items: []args{{"id": "bad3", "kind": "rect", "textColor": "red; background: blue"}}},
		{name: "none text color", items: []args{{"id": "bad4", "kind": "text", "color": "none"}}},
		{name: "none connector color", items: []args{{"id": "a"}, {"id": "b"}}, connectors: []args{{"id": "e", "from": "a", "to": "b", "color": "none"}}},
	}
	for _, tc := range invalidCases {
		b := whiteboard{Version: 1, Nodes: []args{}, Edges: []args{}}
		if err := b.change(tc.items, tc.connectors, nil, nil, "", false, nil, nil); err == nil {
			t.Errorf("%s: expected validation error, got nil", tc.name)
		}
	}
}

func TestEmbeddedDocumentBoardColorsAndBorderlessRoundTrip(t *testing.T) {
	t.Parallel()
	base := "# Flow\n\n" + boardFence("dou-whiteboard", `{"height":360,"board":{"version":1,"nodes":[{"id":"a","type":"shape","x":40,"y":60,"width":160,"height":90,"data":{"shape":"rect","text":"API","fill":"blue","stroke":"none","textColor":"#0369a1"}},{"id":"note","type":"text","x":260,"y":80,"width":140,"height":40,"data":{"text":"Label","color":"purple"}},{"id":"ink","type":"ink","x":20,"y":200,"data":{"points":[{"x":0,"y":0}],"tone":"danger"}}],"edges":[{"id":"e1","type":"connector","source":{"node":"a","port":"right"},"target":{"node":"note","port":"left-in"},"data":{"arrow":"end","color":"#dc2626"}}]}}`)

	view := documentView(base)
	for _, expected := range []string{`"fill":"blue"`, `"stroke":"none"`, `"textColor":"#0369a1"`, `"color":"purple"`, `"tone":"danger"`, `"color":"#dc2626"`} {
		if !strings.Contains(view, expected) {
			t.Fatalf("documentView missing %s:\n%s", expected, view)
		}
	}

	// Modify shape border/fill/textColor, connector color, and keep ink tone untouched.
	editedView := strings.Replace(view, `"fill":"blue"`, `"fill":"green"`, 1)
	editedView = strings.Replace(editedView, `"stroke":"none"`, `"stroke":"green"`, 1)
	editedView = strings.Replace(editedView, `,"textColor":"#0369a1"`, ``, 1)
	editedView = strings.Replace(editedView, `"color":"#dc2626"`, `"color":"orange"`, 1)
	if editedView == view {
		t.Fatalf("failed to mutate view:\n%s", view)
	}
	changed, err := documentFromView(editedView, base, false)
	if err != nil {
		t.Fatal(err)
	}
	part := markdownParts(changed)[1]
	var board embeddedBoard
	if err := json.Unmarshal([]byte(part.body), &board); err != nil {
		t.Fatal(err)
	}
	nodes := boardItems(board.Board.Nodes)
	aData := objectField(nodes["a"], "data")
	if text(aData, "fill") != "green" || text(aData, "stroke") != "green" || text(aData, "textColor") != "" {
		t.Fatalf("shape colors did not update/clear cleanly: %+v", aData)
	}
	inkData := objectField(nodes["ink"], "data")
	if text(inkData, "tone") != "danger" || inkData["points"] == nil {
		t.Fatalf("untouched ink tone/points were lost: %+v", inkData)
	}
	edgeData := objectField(boardItems(board.Board.Edges)["e1"], "data")
	if text(edgeData, "color") != "orange" {
		t.Fatalf("connector color did not update: %+v", edgeData)
	}

	// Clearing connector color and ink tone from the embedded fence resets them to defaults.
	nextView := documentView(changed)
	clearedView := strings.Replace(nextView, `,"color":"orange"`, ``, 1)
	clearedView = strings.Replace(clearedView, `,"tone":"danger"`, ``, 1)
	cleared, err := documentFromView(clearedView, changed, false)
	if err != nil {
		t.Fatal(err)
	}
	var clearedBoard embeddedBoard
	if err := json.Unmarshal([]byte(markdownParts(cleared)[1].body), &clearedBoard); err != nil {
		t.Fatal(err)
	}
	if data := objectField(boardItems(clearedBoard.Board.Nodes)["ink"], "data"); text(data, "tone") != "" || text(data, "color") != "" {
		t.Fatalf("ink tone was not cleared: %+v", data)
	}
	if data := objectField(boardItems(clearedBoard.Board.Edges)["e1"], "data"); text(data, "color") != "" || text(data, "tone") != "" {
		t.Fatalf("connector color was not cleared: %+v", data)
	}
}
