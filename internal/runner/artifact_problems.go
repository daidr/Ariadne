package runner

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"
)

func artifactProblems(kind, content string) []string {
	switch kind {
	case "whiteboard":
		var board whiteboard
		if json.Unmarshal([]byte(content), &board) == nil {
			return board.problems()
		}
	case "document":
		if !strings.Contains(content, "dou-whiteboard") {
			return nil
		}
		var all []string
		number := 0
		for _, part := range markdownParts(content) {
			if board, ok := storedBoard(part); ok {
				number++
				for _, problem := range board.Board.problems() {
					all = append(all, fmt.Sprintf("whiteboard %d: %s", number, problem))
				}
			}
			if len(all) >= 20 {
				return all[:20]
			}
		}
		return all
	case "dashboard":
		return dashboardProblems(content)
	}
	return nil
}

func (a boardBox) overlaps(b boardBox) bool {
	return a.x < b.x+b.w && b.x < a.x+a.w && a.y < b.y+b.h && b.y < a.y+a.h
}

type boardPoint struct{ x, y float64 }

func (b boardBox) anchor(port string) boardPoint {
	x, y := b.x+b.w/2, b.y+b.h/2
	switch strings.TrimSuffix(port, "-in") {
	case "left":
		x = b.x
	case "right":
		x = b.x + b.w
	case "top":
		y = b.y
	default:
		y = b.y + b.h
	}
	return boardPoint{x, y}
}

func (b boardBox) crosses(from, to boardPoint) bool {
	x0, x1, y0, y1 := b.x+4, b.x+b.w-4, b.y+4, b.y+b.h-4
	if x0 >= x1 || y0 >= y1 {
		return false
	}
	first, last := 0.0, 1.0
	for _, v := range [][4]float64{{from.x, to.x - from.x, x0, x1}, {from.y, to.y - from.y, y0, y1}} {
		position, distance, low, high := v[0], v[1], v[2], v[3]
		if math.Abs(distance) < 1e-9 {
			if position < low || position > high {
				return false
			}
			continue
		}
		one, two := (low-position)/distance, (high-position)/distance
		first, last = max(first, min(one, two)), min(last, max(one, two))
		if first > last {
			return false
		}
	}
	return true
}

func (board whiteboard) problems() []string {
	if len(board.Nodes)+len(board.Edges) > 768 {
		return []string{"the whiteboard has too many items to check its layout"}
	}
	nodes, boxes := boardItems(board.Nodes), map[string]boardBox{}
	var ids, problems []string
	for _, node := range board.Nodes {
		if kind := boardKind(node); kind == "drawing" || kind == "ink" {
			continue
		}
		id := text(node, "id")
		ids, boxes[id] = append(ids, id), bounds(node)
	}
	inside := func(id, ancestor string) bool {
		for depth := 0; depth < 64 && nodes[id] != nil; depth++ {
			id = text(nodes[id], "parent")
			if id == ancestor {
				return true
			}
		}
		return false
	}
	for i, id := range ids {
		box := boxes[id]
		node := nodes[id]
		for _, other := range ids[i+1:] {
			if !box.overlaps(boxes[other]) || inside(id, other) || inside(other, id) ||
				(boardKind(node) == "image" && text(node, "parent") != "") ||
				(boardKind(nodes[other]) == "image" && text(nodes[other], "parent") != "") {
				continue
			}
			problems = append(problems, fmt.Sprintf("items %q and %q overlap", id, other))
			if len(problems) >= 20 {
				return problems
			}
		}
		kind := boardKind(node)
		if _, supported := boardSizes[kind]; supported && kind != "text" {
			label := text(objectField(node, "data"), "text")
			_, height := boardTextSize(kind, label, box.w, 0, true)
			if label != "" && height > box.h+2 {
				problems = append(problems, fmt.Sprintf("the text of %q is cut off; shorten it", id))
			}
		}
		if len(problems) >= 20 {
			return problems
		}
	}
	type boardLabel struct {
		id string
		at boardBox
	}
	var labels []boardLabel
	for _, edge := range board.Edges {
		id, from, to := text(edge, "id"), boardEndpoint(edge, "source"), boardEndpoint(edge, "target")
		start, hasStart := boxes[from]
		end, hasEnd := boxes[to]
		if !hasStart || !hasEnd || from == to {
			continue
		}
		first, last := start.anchor(text(objectField(edge, "source"), "port")), end.anchor(text(objectField(edge, "target"), "port"))
		raw, _ := edge["points"].([]any)
		points := make([]boardPoint, 0, min(len(raw), 256))
		for _, entry := range raw[:min(len(raw), 256)] {
			at, ok := entry.(args)
			if ok {
				points = append(points, boardPoint{asNumber(at["x"]), asNumber(at["y"])})
			}
		}
		run := append(append([]boardPoint{first}, points...), last)
		for _, item := range ids {
			if item == from || item == to || inside(item, from) || inside(item, to) {
				continue
			}
			for i, p := range run[1:] {
				if boxes[item].crosses(run[i], p) {
					problems = append(problems, fmt.Sprintf("connector %q runs through item %q", id, item))
					break
				}
			}
			if len(problems) >= 20 {
				return problems
			}
		}
		label := text(edge, "label")
		if label == "" {
			continue
		}
		if len(points) == 0 {
			points = []boardPoint{first, last}
		}
		a, b := points[(len(points)-1)/2], points[len(points)/2]
		w := float64(utf8.RuneCountInString(label)*8 + 12)
		labelBox := boardBox{(a.x + b.x - w) / 2, (a.y+b.y)/2 - 10, w, 20}
		for _, item := range ids {
			if labelBox.overlaps(boxes[item]) {
				problems = append(problems, fmt.Sprintf("the label of connector %q lies on item %q", id, item))
				if len(problems) >= 20 {
					return problems
				}
			}
		}
		for _, prior := range labels {
			if labelBox.overlaps(prior.at) {
				problems = append(problems, fmt.Sprintf("the labels of connectors %q and %q overlap", prior.id, id))
				if len(problems) >= 20 {
					return problems
				}
			}
		}
		labels = append(labels, boardLabel{id, labelBox})
	}
	return problems
}

func dashboardProblems(content string) []string {
	var snapshot struct {
		Data      args
		Dashboard struct{ Widgets []args }
	}
	if json.Unmarshal([]byte(content), &snapshot) != nil {
		return nil
	}
	var problems []string
	for _, widget := range snapshot.Dashboard.Widgets {
		id, kind := text(widget, "id"), text(widget, "type")
		props := objectField(widget, "props")
		if err := checkWidgetProps(kind, props); err != nil {
			problems = append(problems, fmt.Sprintf("widget %q: %s", id, err))
			continue
		}
		name, hasSource := props["source"].(string)
		if !hasSource {
			continue
		}
		data, exists := snapshot.Data[name]
		if !exists {
			problems = append(problems, fmt.Sprintf("widget %q reads data set %q, which is missing", id, name))
			continue
		}
		if kind == "stat" {
			for _, key := range []string{"field", "delta", "trend"} {
				if path, present := props[key].(string); present {
					value := widgetValueAt(data, path)
					_, number := value.(float64)
					_, array := value.([]any)
					if (key == "trend" && !array) || (key != "trend" && !number) {
						problems = append(problems, fmt.Sprintf("widget %q: props.%s %q has the wrong value type in data set %q", id, key, path, name))
					}
				}
			}
			continue
		}
		if kind == "pending" || kind == "text" {
			continue
		}
		path := text(props, "path")
		rows, ok := widgetValueAt(data, path).([]any)
		if !ok {
			problems = append(problems, fmt.Sprintf("widget %q: %q in data set %q is not a list of rows", id, path, name))
			continue
		}
		if len(rows) == 0 {
			continue
		}
		first, ok := rows[0].(args)
		if !ok {
			continue
		}
		var keys []string
		switch kind {
		case "chart":
			keys, _ = stringsOf(props["y"])
			keys = append([]string{text(props, "x")}, keys...)
		case "table":
			columns, _ := artifactObjects(props["columns"])
			for _, column := range columns {
				keys = append(keys, text(column, "key"))
			}
		}
		for _, key := range keys {
			root, _, _ := strings.Cut(key, ".")
			if _, exists := first[root]; !exists {
				problems = append(problems, fmt.Sprintf("widget %q: rows of data set %q have no %q", id, name, key))
			}
		}
	}
	return problems[:min(len(problems), 20)]
}

func widgetValueAt(value any, path string) any {
	if path != "" {
		for key := range strings.SplitSeq(path, ".") {
			fields, _ := value.(args)
			value = fields[key]
		}
	}
	return value
}
