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

	// Ink node allows recoloring via color (clearing legacy tone) and clearing/updating legacy tone, while rejecting shape fill/stroke.
	if err := board.change([]args{{"id": "draw", "tone": ""}}, nil, nil, nil, "", false, nil, nil); err != nil {
		t.Fatalf("expected ink tone clear to succeed: %v", err)
	}
	if data := objectField(boardItems(board.Nodes)["draw"], "data"); text(data, "tone") != "" {
		t.Fatalf("ink tone was not cleared: %+v", data)
	}
	if err := board.change([]args{{"id": "draw", "tone": "primary"}}, nil, nil, nil, "", false, nil, nil); err != nil {
		t.Fatalf("expected ink tone update to succeed: %v", err)
	}
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

	// Converting a colored shape to a prototype drops stale shape/color fields.
	protoBoard := whiteboard{
		Version: 1,
		Nodes: []args{
			{"id": "card", "type": "shape", "x": 0, "y": 0, "width": 160, "height": 90, "data": args{"shape": "rect", "text": "Old", "fill": "blue", "stroke": "none", "textColor": "#0369a1"}},
		},
		Edges: []args{},
	}
	resolved := map[string]boardPrototype{
		"card": {title: "Preview", html: "<div>hi</div>", viewportWidth: 400, viewportHeight: 300},
	}
	if err := protoBoard.change([]args{{"id": "card", "kind": "prototype"}}, nil, nil, nil, "", false, nil, resolved); err != nil {
		t.Fatal(err)
	}
	protoData := objectField(boardItems(protoBoard.Nodes)["card"], "data")
	for _, stale := range []string{"shape", "tone", "fill", "stroke", "textColor", "color"} {
		if _, exists := protoData[stale]; exists {
			t.Fatalf("prototype conversion kept stale field %q: %+v", stale, protoData)
		}
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

	// Embedded board handles ink tone updates, color->tone switches, explicit null clear,
	// text->shape/sticky color migration, and legacy connector tone cleanup.
	edgeCasesBase := "# Edge Cases\n\n" + boardFence("dou-whiteboard", `{"height":360,"board":{"version":1,"nodes":[{"id":"t1","type":"text","x":0,"y":0,"width":140,"height":40,"data":{"text":"Title","color":"purple"}},{"id":"t2","type":"text","x":0,"y":80,"width":140,"height":40,"data":{"text":"Memo","color":"#0369a1"}},{"id":"ink1","type":"ink","x":200,"y":0,"data":{"points":[{"x":0,"y":0}],"tone":"danger"}},{"id":"ink2","type":"ink","x":200,"y":80,"data":{"points":[{"x":0,"y":0}],"color":"blue"}},{"id":"ink3","type":"ink","x":200,"y":160,"data":{"points":[{"x":0,"y":0}],"color":"green"}}],"edges":[{"id":"e1","type":"connector","source":{"node":"t1","port":"bottom"},"target":{"node":"t2","port":"top-in"},"data":{"arrow":"end","tone":"warning"}}]}}`)
	edgeView := documentView(edgeCasesBase)
	if strings.Contains(edgeView, `"tone":"warning"`) {
		t.Fatalf("connector summary should not expose unsupported tone field:\n%s", edgeView)
	}
	mutatedEdgeView := strings.Replace(edgeView, `"id":"t1","kind":"text"`, `"id":"t1","kind":"rect"`, 1)
	mutatedEdgeView = strings.Replace(mutatedEdgeView, `"id":"t2","kind":"text"`, `"id":"t2","kind":"sticky"`, 1)
	mutatedEdgeView = strings.Replace(mutatedEdgeView, `"tone":"danger"`, `"tone":"primary"`, 1)
	mutatedEdgeView = strings.Replace(mutatedEdgeView, `"color":"blue"`, `"tone":"success"`, 1)
	mutatedEdgeView = strings.Replace(mutatedEdgeView, `"color":"green"`, `"color":null`, 1)
	edgeUpdated, err := documentFromView(mutatedEdgeView, edgeCasesBase, false)
	if err != nil {
		t.Fatal(err)
	}
	var edgeBoard embeddedBoard
	if err := json.Unmarshal([]byte(markdownParts(edgeUpdated)[1].body), &edgeBoard); err != nil {
		t.Fatal(err)
	}
	edgeNodes := boardItems(edgeBoard.Board.Nodes)
	if d := objectField(edgeNodes["t1"], "data"); text(d, "textColor") != "purple" || text(d, "color") != "" {
		t.Fatalf("embedded text->rect lost color: %+v", d)
	}
	if d := objectField(edgeNodes["t2"], "data"); text(d, "textColor") != "#0369a1" || text(d, "color") != "" {
		t.Fatalf("embedded text->sticky lost color: %+v", d)
	}
	if d := objectField(edgeNodes["ink1"], "data"); text(d, "tone") != "primary" || text(d, "color") != "" {
		t.Fatalf("embedded ink tone update failed: %+v", d)
	}
	if d := objectField(edgeNodes["ink2"], "data"); text(d, "tone") != "success" || text(d, "color") != "" {
		t.Fatalf("embedded ink color->tone switch failed: %+v", d)
	}
	if d := objectField(edgeNodes["ink3"], "data"); text(d, "color") != "" || text(d, "tone") != "" {
		t.Fatalf("embedded ink null clear failed: %+v", d)
	}
	if d := objectField(boardItems(edgeBoard.Board.Edges)["e1"], "data"); text(d, "tone") != "" || text(d, "color") != "" {
		t.Fatalf("legacy connector tone was not cleaned up: %+v", d)
	}
}
