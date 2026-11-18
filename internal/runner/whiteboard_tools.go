package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"unicode/utf8"

	"github.com/daidr/Ariadne/internal/jsonutil"
)

type whiteboard struct {
	Version int    `json:"version"`
	Nodes   []args `json:"nodes"`
	Edges   []args `json:"edges"`
}

func (turn *turn) createWhiteboard(ctx context.Context, input args) (string, error) {
	if input["items"] == nil && input["options"] == nil {
		return "", errors.New("give items or options for the new whiteboard")
	}
	change, err := turn.whiteboardChange(ctx, input)
	if err != nil {
		return "", err
	}
	content, err := change(`{"version":1,"nodes":[],"edges":[]}`)
	if err != nil {
		return "", err
	}
	return turn.createArtifact(ctx, "whiteboard", input, content)
}

func (turn *turn) updateWhiteboard(ctx context.Context, input args) (string, error) {
	artifact, err := turn.projectArtifact(ctx, text(input, "artifactId"))
	if err != nil {
		return "", err
	}
	if artifact.Kind != "whiteboard" {
		return "", errors.New("a whiteboard is required")
	}
	change, err := turn.whiteboardChange(ctx, input)
	if err != nil {
		return "", err
	}
	return turn.editArtifact(ctx, "whiteboard", input, input["baseVersion"] != nil, change)
}

func (turn *turn) whiteboardChange(ctx context.Context, input args) (func(string) (string, error), error) {
	items, err := artifactObjects(input["items"])
	if err != nil {
		return nil, err
	}
	connectors, err := artifactObjects(input["connectors"])
	if err != nil {
		return nil, err
	}
	removeItems, err := stringsOf(input["removeItems"])
	if err != nil {
		return nil, err
	}
	removeEdges, err := stringsOf(input["removeConnectors"])
	if err != nil {
		return nil, err
	}
	direction, pick := text(input, "direction"), text(input, "pick")
	if direction != "" && direction != "down" && direction != "right" {
		return nil, errors.New("direction must be down or right")
	}
	if pick != "" && pick != "one" && pick != "many" {
		return nil, errors.New("pick must be one or many")
	}
	options, err := turn.boardOptions(ctx, input["options"])
	if err != nil {
		return nil, err
	}
	return func(base string) (string, error) {
		var board whiteboard
		if err := json.Unmarshal([]byte(base), &board); err != nil {
			return "", err
		}
		if err := board.change(items, connectors, removeItems, removeEdges, direction, input["relayout"] == true); err != nil {
			return "", err
		}
		if err := board.placeOptions(options, pick); err != nil {
			return "", err
		}
		encoded, err := jsonutil.Encode(board)
		return string(encoded), err
	}, nil
}

var boardSizes = map[string][2]float64{
	"rect": {160, 90}, "ellipse": {160, 90}, "diamond": {160, 110}, "sticky": {160, 160}, "text": {200, 40}, "pending": {160, 90},
}

func boardItems(values []args) map[string]args {
	result := make(map[string]args, len(values))
	for _, value := range values {
		result[text(value, "id")] = value
	}
	return result
}

func objectField(value args, key string) args {
	object, _ := value[key].(args)
	if object == nil {
		object = args{}
	}
	return object
}

func boardKind(node args) string {
	kind := text(node, "type")
	if kind == "shape" {
		kind = text(objectField(node, "data"), "shape")
		if kind == "" {
			kind = "rect"
		}
	}
	return kind
}

func applyBoardItem(node, input args) error {
	before, kind := boardKind(node), text(input, "kind")
	if kind == "" {
		kind = before
		if kind == "" {
			kind = "rect"
		}
	}
	size, supported := boardSizes[kind]
	if _, editable := boardSizes[before]; before != "" && !editable {
		if input["kind"] != nil || input["text"] != nil || input["tone"] != nil {
			return errors.New("a user-drawn item can be moved or removed")
		}
	} else if !supported {
		return fmt.Errorf("unknown item kind %q", kind)
	}
	for _, key := range []string{"x", "y", "width", "height"} {
		if value, ok := input[key].(float64); ok && (key == "x" || key == "y" || value > 0) {
			node[key] = value
		}
	}
	if !supported {
		return nil
	}
	data := objectField(node, "data")
	node["data"], node["type"] = data, kind
	if kind == "rect" || kind == "ellipse" || kind == "diamond" {
		node["type"], data["shape"] = "shape", kind
	} else {
		delete(data, "shape")
	}
	if label, ok := input["text"].(string); ok {
		data["text"] = label
	}
	if tone := text(input, "tone"); tone != "" {
		if !slices.Contains([]string{"neutral", "primary", "success", "warning", "danger"}, tone) {
			return fmt.Errorf("unknown item tone %q", tone)
		}
		data["tone"] = tone
	}
	if kind == "pending" || kind == "text" {
		delete(data, "tone")
	}
	width := asNumber(node["width"])
	_, widthSpecified := input["width"].(float64)
	if width <= 0 {
		width = size[0]
	}
	node["width"], node["height"] = boardTextSize(kind, text(data, "text"), width, max(asNumber(node["height"]), size[1]), widthSpecified)
	return nil
}

func boardEndpoint(edge args, end string) string {
	return text(objectField(edge, end), "node")
}

func (board *whiteboard) change(items, connectors []args, removeItems, removeEdges []string, direction string, relayout bool) error {
	removed := map[string]bool{}
	for _, id := range removeItems {
		removed[id] = true
	}
	// Removing a frame also removes its descendants.
	for changed := true; changed; {
		changed = false
		for _, node := range board.Nodes {
			id := text(node, "id")
			if removed[text(node, "parent")] && !removed[id] {
				removed[id], changed = true, true
			}
		}
	}
	board.Nodes = slices.DeleteFunc(board.Nodes, func(node args) bool { return removed[text(node, "id")] })
	nodes := boardItems(board.Nodes)
	seen, moved := map[string]bool{}, map[string]bool{}
	for _, item := range items {
		id := text(item, "id")
		if id == "" || seen[id] {
			return errors.New("each item needs a unique id")
		}
		seen[id] = true
		node := nodes[id]
		if node == nil {
			node = args{"id": id}
			nodes[id] = node
			board.Nodes = append(board.Nodes, node)
		}
		if err := applyBoardItem(node, item); err != nil {
			return fmt.Errorf("item %q: %w", id, err)
		}
		moved[id] = true
	}
	board.Edges = slices.DeleteFunc(board.Edges, func(edge args) bool {
		return slices.Contains(removeEdges, text(edge, "id")) || nodes[boardEndpoint(edge, "source")] == nil || nodes[boardEndpoint(edge, "target")] == nil
	})
	edges, touched := boardItems(board.Edges), map[string]bool{}
	for _, input := range connectors {
		id, from, to := text(input, "id"), text(input, "from"), text(input, "to")
		edge := edges[id]
		if id == "" {
			for _, candidate := range board.Edges {
				if boardEndpoint(candidate, "source") == from && boardEndpoint(candidate, "target") == to {
					id, edge = text(candidate, "id"), candidate
					break
				}
			}
		}
		if from == "" {
			from = boardEndpoint(edge, "source")
		}
		if to == "" {
			to = boardEndpoint(edge, "target")
		}
		if nodes[from] == nil || nodes[to] == nil {
			return fmt.Errorf("connector requires existing items at both ends (%q, %q)", from, to)
		}
		if edge == nil {
			if id == "" {
				id = availableBoardID(edges, from+"-"+to)
			}
			edge = args{"id": id, "data": args{"route": "curve"}}
			board.Edges = append(board.Edges, edge)
			edges[id] = edge
		}
		edge["source"], edge["target"] = args{"node": from}, args{"node": to}
		if label, ok := input["label"].(string); ok {
			if label == "" {
				delete(edge, "label")
			} else {
				edge["label"] = label
			}
		}
		data := objectField(edge, "data")
		for key, allowed := range map[string][]string{"route": {"straight", "elbow", "curve"}, "dash": {"solid", "dashed", "dotted", "dash-dot"}, "arrow": {"none", "end", "both"}} {
			if value := text(input, key); value != "" {
				if !slices.Contains(allowed, value) {
					return fmt.Errorf("connector %q has invalid %s", id, key)
				}
				data[key] = value
			}
		}
		edge["data"] = data
		touched[id] = true
	}
	waypoints, relaid, err := board.layout(nodes, direction, relayout)
	if err != nil {
		return err
	}
	for _, edge := range board.Edges {
		from, to := boardEndpoint(edge, "source"), boardEndpoint(edge, "target")
		if relaid || touched[text(edge, "id")] || moved[from] || moved[to] {
			fromPort, toPort := boardPorts(nodes[from], nodes[to])
			delete(edge, "points")
			if route := waypoints[[2]string{from, to}]; len(route) != 0 {
				edge["points"] = route
			}
			objectField(edge, "source")["port"], objectField(edge, "target")["port"] = fromPort, toPort
		}
	}
	return nil
}

func availableBoardID(index map[string]args, preferred string) string {
	if index[preferred] == nil {
		return preferred
	}
	for i := 2; ; i++ {
		id := fmt.Sprintf("%s-%d", preferred, i)
		if index[id] == nil {
			return id
		}
	}
}

type boardBox struct{ x, y, w, h float64 }

func bounds(node args) boardBox {
	w, h := asNumber(node["width"]), asNumber(node["height"])
	if w <= 0 {
		w = 160
	}
	if h <= 0 {
		h = 90
	}
	return boardBox{asNumber(node["x"]), asNumber(node["y"]), w, h}
}

func (a boardBox) touches(b boardBox) bool {
	return a.x < b.x+b.w+20 && b.x < a.x+a.w+20 && a.y < b.y+b.h+20 && b.y < a.y+a.h+20
}

func boardPorts(from, to args) (string, string) {
	a, b := bounds(from), bounds(to)
	dx, dy := b.x+b.w/2-a.x-a.w/2, b.y+b.h/2-a.y-a.h/2
	switch {
	case math.Abs(dx) >= math.Abs(dy) && dx >= 0:
		return "right", "left-in"
	case math.Abs(dx) >= math.Abs(dy):
		return "left", "right-in"
	case dy >= 0:
		return "bottom", "top-in"
	default:
		return "top", "bottom-in"
	}
}

func (board *whiteboard) layout(nodes map[string]args, direction string, relayout bool) (map[[2]string][]args, bool, error) {
	if direction == "" {
		direction = "down"
		right, down := 0, 0
		for _, edge := range board.Edges {
			switch text(objectField(edge, "source"), "port") {
			case "right":
				right++
			case "bottom":
				down++
			}
		}
		if right > down {
			direction = "right"
		}
	}
	var boxes []boardBox
	var pending []string
	placed := map[string]bool{}
	bottom := 0.0
	for _, node := range board.Nodes {
		id := text(node, "id")
		_, editable := boardSizes[boardKind(node)]
		if node["x"] == nil || node["y"] == nil || relayout && editable && node["parent"] == nil {
			pending = append(pending, id)
		} else if node["parent"] == nil {
			at := bounds(node)
			boxes = append(boxes, at)
			bottom = max(bottom, at.y+at.h+64)
			placed[id] = true
		}
	}
	if len(pending) > 0 && (relayout || len(pending) == len(board.Nodes)) {
		return board.layOutRanks(pending, nodes, boxes, direction), true, nil
	}
	incoming := map[string][]args{}
	for _, edge := range board.Edges {
		to := boardEndpoint(edge, "target")
		incoming[to] = append(incoming[to], edge)
	}
	for len(pending) > 0 {
		best := 0
		var predecessor args
		for index, id := range pending {
			for _, edge := range incoming[id] {
				if placed[boardEndpoint(edge, "source")] {
					best, predecessor = index, edge
					break
				}
			}
			if predecessor != nil {
				break
			}
		}
		id := pending[best]
		pending = slices.Delete(pending, best, best+1)
		at := bounds(nodes[id])
		at.x, at.y = 0, bottom
		if predecessor != nil {
			from := bounds(nodes[boardEndpoint(predecessor, "source")])
			gap := max(72.0, float64(utf8.RuneCountInString(text(predecessor, "label"))*8+28))
			at.x, at.y = from.x, from.y+from.h+gap
			if direction == "right" {
				at.x, at.y = from.x+from.w+gap, from.y
			}
		}
		for attempt := 0; ; attempt++ {
			if attempt > len(boxes) {
				return nil, false, errors.New("could not find space to place a board item")
			}
			hit := false
			for _, box := range boxes {
				if at.touches(box) {
					hit = true
					if direction == "down" {
						at.x = box.x + box.w + 40
					} else {
						at.y = box.y + box.h + 40
					}
				}
			}
			if !hit {
				break
			}
		}
		nodes[id]["x"], nodes[id]["y"] = at.x, at.y
		boxes = append(boxes, at)
		placed[id] = true
	}
	return nil, false, nil
}
