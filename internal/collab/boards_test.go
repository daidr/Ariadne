package collab

import (
	"encoding/json"
	"testing"

	"github.com/antst/go-yjs/crdt"
)

func TestBoardUpdatesKeepEditsOutsideTheBaseDiff(t *testing.T) {
	t.Parallel()
	board, err := Load("whiteboard", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer board.Destroy()
	base := `{"version":1,"nodes":[{"id":"n","type":"group","x":0,"y":0,"data":{"collapsed":true,"text":"before"}}],"edges":[]}`
	if err := UpdateWhiteboard(board, base, nil, "seed"); err != nil {
		t.Fatal(err)
	}
	board.GetMap("nodes").Get("n").(*crdt.YMap).Set("x", 80)
	if err := UpdateWhiteboard(board, `{"version":1,"nodes":[{"id":"n","type":"group","x":0,"y":0,"data":{"text":"after"}}],"edges":[]}`, &base, "writer"); err != nil {
		t.Fatal(err)
	}
	result, err := ReadBoard(board, "whiteboard")
	if err != nil {
		t.Fatal(err)
	}
	compareContent(t, result, `{"version":1,"nodes":[{"id":"n","type":"group","x":80,"y":0,"data":{"text":"after"}}],"edges":[]}`)
}

func TestBoardUpdatesPreserveConcurrentColorAndBorderlessEdits(t *testing.T) {
	t.Parallel()
	board, err := Load("whiteboard", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer board.Destroy()
	base := `{"version":1,"nodes":[{"id":"a","type":"shape","x":0,"y":0,"data":{"shape":"rect","text":"A"}},{"id":"b","type":"text","x":220,"y":0,"data":{"text":"B"}}],"edges":[{"id":"e1","type":"connector","source":{"node":"a","port":"right"},"target":{"node":"b","port":"left-in"},"data":{"arrow":"end"}}]}`
	if err := UpdateWhiteboard(board, base, nil, "seed"); err != nil {
		t.Fatal(err)
	}
	// Simulate concurrent position drag on client 1 while client 2 edits fill, borderless stroke, textColor, text color and edge color.
	board.GetMap("nodes").Get("a").(*crdt.YMap).Set("x", 120)
	styled := `{"version":1,"nodes":[{"id":"a","type":"shape","x":0,"y":0,"data":{"shape":"rect","text":"A","fill":"green","stroke":"none","textColor":"#166534"}},{"id":"b","type":"text","x":220,"y":0,"data":{"text":"B","color":"purple"}}],"edges":[{"id":"e1","type":"connector","source":{"node":"a","port":"right"},"target":{"node":"b","port":"left-in"},"data":{"arrow":"end","color":"#dc2626"}}]}`
	if err := UpdateWhiteboard(board, styled, &base, "writer"); err != nil {
		t.Fatal(err)
	}
	result, err := ReadBoard(board, "whiteboard")
	if err != nil {
		t.Fatal(err)
	}
	compareContent(t, result, `{"version":1,"nodes":[{"id":"a","type":"shape","x":120,"y":0,"data":{"shape":"rect","text":"A","fill":"green","stroke":"none","textColor":"#166534"}},{"id":"b","type":"text","x":220,"y":0,"data":{"text":"B","color":"purple"}}],"edges":[{"id":"e1","type":"connector","source":{"node":"a","port":"right"},"target":{"node":"b","port":"left-in"},"data":{"arrow":"end","color":"#dc2626"}}]}`)
}

func TestBoardUpdatesMergeConcurrentNodeAndEdgeDataFields(t *testing.T) {
	t.Parallel()
	board, err := Load("whiteboard", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer board.Destroy()
	base := `{"version":1,"nodes":[{"id":"a","type":"shape","x":0,"y":0,"data":{"shape":"rect","text":"A","tone":"primary"}},{"id":"b","type":"shape","x":220,"y":0,"data":{"shape":"rect","text":"B"}}],"edges":[{"id":"e1","type":"connector","source":{"node":"a","port":"right"},"target":{"node":"b","port":"left-in"},"data":{"arrow":"end","route":"curve"}}]}`
	if err := UpdateWhiteboard(board, base, nil, "seed"); err != nil {
		t.Fatal(err)
	}
	// Browser user edits node a's text and textColor, node b's fill/stroke (borderless), and edge e1's color.
	userEdit := `{"version":1,"nodes":[{"id":"a","type":"shape","x":0,"y":0,"data":{"shape":"rect","text":"A edited","tone":"primary","textColor":"#0369a1"}},{"id":"b","type":"shape","x":220,"y":0,"data":{"shape":"rect","text":"B","fill":"blue","stroke":"none"}}],"edges":[{"id":"e1","type":"connector","source":{"node":"a","port":"right"},"target":{"node":"b","port":"left-in"},"data":{"arrow":"end","route":"curve","color":"#dc2626"}}]}`
	if err := UpdateWhiteboard(board, userEdit, nil, "user"); err != nil {
		t.Fatal(err)
	}
	// Agent concurrently edits based on the older base: removes border on a (migrating tone -> fill),
	// rewrites text on b, and changes dash style on e1.
	agentEdit := `{"version":1,"nodes":[{"id":"a","type":"shape","x":0,"y":0,"data":{"shape":"rect","text":"A","fill":"primary","stroke":"none"}},{"id":"b","type":"shape","x":220,"y":0,"data":{"shape":"rect","text":"B from agent"}}],"edges":[{"id":"e1","type":"connector","source":{"node":"a","port":"right"},"target":{"node":"b","port":"left-in"},"data":{"arrow":"end","route":"curve","dash":"dashed"}}]}`
	if err := UpdateWhiteboard(board, agentEdit, &base, "agent"); err != nil {
		t.Fatal(err)
	}
	result, err := ReadBoard(board, "whiteboard")
	if err != nil {
		t.Fatal(err)
	}
	compareContent(t, result, `{"version":1,"nodes":[{"id":"a","type":"shape","x":0,"y":0,"data":{"shape":"rect","text":"A edited","fill":"primary","stroke":"none","textColor":"#0369a1"}},{"id":"b","type":"shape","x":220,"y":0,"data":{"shape":"rect","text":"B from agent","fill":"blue","stroke":"none"}}],"edges":[{"id":"e1","type":"connector","source":{"node":"a","port":"right"},"target":{"node":"b","port":"left-in"},"data":{"arrow":"end","route":"curve","dash":"dashed","color":"#dc2626"}}]}`)
}

func TestDashboardPreservesConcurrentBreakpointAndWidget(t *testing.T) {
	t.Parallel()
	dashboard, err := Load("dashboard", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer dashboard.Destroy()
	base := `{"version":1,"dashboard":{"version":1,"widgets":[{"id":"a","type":"stat","place":{"sm":{"x":0}}},{"id":"b","type":"table"}]},"data":{"count":4}}`
	if err := UpdateDashboard(dashboard, base, nil, "seed"); err != nil {
		t.Fatal(err)
	}
	user := `{"version":1,"dashboard":{"version":1,"widgets":[{"id":"a","type":"stat","place":{"sm":{"x":0},"lg":{"x":5}}},{"id":"b","type":"table"},{"id":"user","type":"text"}]},"data":{"count":4}}`
	if err := UpdateDashboard(dashboard, user, nil, "user"); err != nil {
		t.Fatal(err)
	}
	before := dashboard.GetMap("widgets").Get("user").(*crdt.YMap).Get("$order")
	writer := `{"version":1,"dashboard":{"version":1,"widgets":[{"id":"b","type":"table"},{"id":"a","type":"stat","place":{"sm":{"x":1}}}]},"data":{"count":5}}`
	if err := UpdateDashboard(dashboard, writer, &base, "writer"); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, widget := range orderedWidgets(dashboard.GetMap("widgets")) {
		ids = append(ids, itemID(widget))
	}
	if len(ids) != 3 || ids[0] != "b" || ids[1] != "a" || ids[2] != "user" {
		t.Fatal("unexpected merged order", ids)
	}
	if now := dashboard.GetMap("widgets").Get("user").(*crdt.YMap).Get("$order"); before != now {
		t.Fatal("another editor's order key changed", before, now)
	}
	body, err := ReadBoard(dashboard, "dashboard")
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatal(err)
	}
	compareContent(t, string(decoded["dashboard"]),
		`{"version":1,"widgets":[{"id":"b","type":"table","place":{}},{"id":"a","type":"stat","place":{"sm":{"x":1},"lg":{"x":5}}},{"id":"user","type":"text","place":{}}]}`)
	compareContent(t, string(decoded["data"]), `{"count":5}`)
}
