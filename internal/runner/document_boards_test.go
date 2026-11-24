package runner

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/internal/collab"
	"github.com/daidr/Ariadne/internal/core"
)

func TestModelChangesEmbeddedBoardWithoutReplacingDrawings(t *testing.T) {
	base := "# Plan\n\n" + boardFence("dou-whiteboard", `{"height":390,"board":{"version":1,"nodes":[{"id":"ink","type":"ink","x":30,"y":60,"data":{"points":[{"x":0,"y":0}]}},{"id":"todo","type":"sticky","x":160,"y":80,"data":{"text":"Before"}}],"edges":[]}}`)
	view := documentView(base)
	if strings.Contains(view, "points") || !strings.Contains(view, `"kind":"drawing"`) || strings.Contains(view, "dou-whiteboard") {
		t.Fatal(view)
	}
	unchanged, err := documentFromView(view, base, false)
	if err != nil || unchanged != base {
		t.Fatal(unchanged, err)
	}
	changed, err := documentFromView(strings.Replace(view, "Before", "After", 1), base, false)
	if err != nil {
		t.Fatal(err)
	}
	part := markdownParts(changed)[1]
	var board embeddedBoard
	if err := json.Unmarshal([]byte(part.body), &board); err != nil {
		t.Fatal(err)
	}
	items := boardItems(board.Board.Nodes)
	if board.Height != 390 || text(objectField(items["todo"], "data"), "text") != "After" ||
		objectField(items["ink"], "data")["points"] == nil || asNumber(items["ink"]["x"]) != 30 {
		t.Fatalf("edit lost saved board fields: %s", part.body)
	}
	preview, err := documentFromView("Start\n\n```whiteboard\n{", "", true)
	if err != nil || preview != "Start\n\n"+boardPlaceholder {
		t.Fatal(preview, err)
	}
	if _, err := documentFromView("Start\n\n```whiteboard\n{", "", false); err == nil {
		t.Fatal("unfinished whiteboard accepted as a saved edit")
	}
}

func TestBoardCommentAndPinCommitTogether(t *testing.T) {
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
	agent := &turn{Runner: &Runner{Repository: repo, Rooms: rooms}, conversation: core.ConversationView{
		ID: project.CoordinatorID, ProjectID: project.ID, Role: "coordinator",
	}}
	artifact, err := repo.CreateArtifact(ctx, project.ID, core.NewArtifact{Kind: "whiteboard", Title: "Board", Author: "user",
		Content: `{"version":1,"nodes":[{"id":"note","type":"sticky","x":30,"y":60,"data":{"text":"todo"}}],"edges":[]}`})
	if err != nil {
		t.Fatal(err)
	}
	input := args{"artifactId": artifact.ID, "on": "note", "at": args{"x": 0.8, "y": 0.1}}
	if _, err := agent.commentArtifact(ctx, input); err == nil {
		t.Fatal("empty comment accepted")
	}
	current, err := repo.GetArtifact(ctx, artifact.ID, nil)
	if err != nil || current.Version != artifact.Version {
		t.Fatal("invalid comment changed the board", current, err)
	}
	input["body"] = "Please review this point"
	result, err := agent.commentArtifact(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	var reply args
	if err := json.Unmarshal([]byte(result), &reply); err != nil {
		t.Fatal(err)
	}
	id := text(reply, "commentThread")
	current, err = repo.GetArtifact(ctx, artifact.ID, nil)
	if err != nil || current.Version != artifact.Version+1 || !strings.Contains(current.Content, id) || !strings.Contains(current.Content, `"x":0.8`) {
		t.Fatal("pin did not persist", current, err)
	}
	threads, err := repo.ListArtifactComments(ctx, artifact.ID)
	if err != nil || len(threads) != 1 || threads[0]["id"] != id {
		t.Fatal("comment did not persist", threads, err)
	}
}

func TestBoardLayoutRanksAndRoutesPastIntermediateItems(t *testing.T) {
	board := whiteboard{Version: 1, Nodes: []args{}, Edges: []args{}}
	err := board.change([]args{{"id": "review"}, {"id": "publish"}, {"id": "draft"}},
		[]args{{"id": "skip", "from": "draft", "to": "publish"}, {"from": "draft", "to": "review"}, {"from": "review", "to": "publish"}},
		nil, nil, "down", false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	items := boardItems(board.Nodes)
	if bounds(items["draft"]).y >= bounds(items["review"]).y || bounds(items["review"]).y >= bounds(items["publish"]).y {
		t.Fatal("items are not ranked along their connectors", board.Nodes)
	}
	if points, ok := boardItems(board.Edges)["skip"]["points"].([]args); !ok || len(points) != 2 || asNumber(points[0]["x"]) <= bounds(items["review"]).x+bounds(items["review"]).w {
		t.Fatal("long connector was not routed outside the middle rank", board.Edges)
	}
}
