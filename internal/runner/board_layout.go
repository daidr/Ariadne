package runner

import (
	"math"
	"slices"
	"strings"
)

func boardTextWidth(value string, font float64) float64 {
	var em float64
	for _, r := range value {
		switch {
		case r >= 0x1100 && r <= 0x115f, r >= 0x2e80 && r <= 0xa4cf,
			r >= 0xac00 && r <= 0xd7a3, r >= 0xf900 && r <= 0xfaff,
			r >= 0xfe30 && r <= 0xfe4f, r >= 0xff00 && r <= 0xff60,
			r >= 0xffe0 && r <= 0xffe6:
			em++
		default:
			em += 0.6
		}
	}
	return em * font
}

func boardTextSize(kind, content string, w, h float64, fixedWidth bool) (float64, float64) {
	padX, padY, roomy := 16.0, 12.0, 1.0
	switch kind {
	case "sticky":
		padY = 16
	case "ellipse":
		roomy = 1.3
	case "diamond":
		roomy = 1.6
	}
	font := 14.0
	if kind == "text" {
		font = 16
	}
	var longest float64
	for line := range strings.SplitSeq(content, "\n") {
		longest = max(longest, boardTextWidth(line, font))
	}
	if kind == "text" {
		if !fixedWidth {
			w = min(480, max(w, longest+20))
		}
		return math.Round(w), h
	}
	if !fixedWidth {
		w = max(w, min(280, (longest+2*padX)*roomy+2))
	}
	inner := max(font, w/roomy-2*padX)
	lines := boardLineCount(content, font, inner)
	return math.Round(w), math.Round(max(h, (lines*21+2*padY)*roomy+2))
}

func boardLineCount(content string, font, width float64) float64 {
	var lines float64
	for line := range strings.SplitSeq(content, "\n") {
		lines += max(1, math.Ceil(boardTextWidth(line, font)*1.1/width))
	}
	return lines
}

func boardRankGap(label, direction string) float64 {
	if direction == "down" || label == "" {
		return 80
	}
	return max(80, min(200, boardTextWidth(label, 13)+14)+48)
}

func (board *whiteboard) layOutRanks(movable []string, nodes map[string]args, fixed []boardBox, direction string) map[[2]string][]args {
	included := map[string]bool{}
	ranks, incoming := map[string]int{}, map[string]int{}
	adjacent := map[string][]string{}
	var origin boardPoint
	hasOrigin := false
	for _, id := range movable {
		included[id] = true
		node := nodes[id]
		x, xOK := node["x"].(float64)
		y, yOK := node["y"].(float64)
		if xOK && yOK {
			if hasOrigin {
				origin.x, origin.y = min(origin.x, x), min(origin.y, y)
			} else {
				origin, hasOrigin = boardPoint{x, y}, true
			}
		}
	}
	gap := 80.0
	unique := map[[2]string]bool{}
	for _, edge := range board.Edges {
		from, to := boardEndpoint(edge, "source"), boardEndpoint(edge, "target")
		pair := [2]string{from, to}
		if from != to && included[from] && included[to] && !unique[pair] {
			unique[pair] = true
			incoming[to]++
			adjacent[from] = append(adjacent[from], to)
		}
		gap = max(gap, boardRankGap(text(edge, "label"), direction))
	}
	// The original item order breaks a cycle and orders items within a rank.
	// No user connector is removed from the document.
	left := slices.Clone(movable)
	for len(left) != 0 {
		i := slices.IndexFunc(left, func(id string) bool { return incoming[id] == 0 })
		id := left[max(0, i)]
		left = slices.Delete(left, max(0, i), max(0, i)+1)
		included[id] = false
		for _, to := range adjacent[id] {
			if included[to] {
				incoming[to]--
				ranks[to] = max(ranks[to], ranks[id]+1)
			}
		}
	}
	rows := make([][]string, len(movable))
	for _, id := range movable {
		rows[ranks[id]] = append(rows[ranks[id]], id)
	}
	horizontal := direction == "right"
	if len(unique) == 0 {
		// Pack disconnected items into rows of a usable width.
		var total, maxBreadth float64
		for _, id := range movable {
			major, cross := boardAxes(bounds(nodes[id]), horizontal)
			total += (major + 40) * (cross + 40)
			maxBreadth = max(maxBreadth, cross)
		}
		rowWidth := max(maxBreadth, math.Sqrt(total*16/9))
		rows = make([][]string, len(movable))
		var used float64
		row := 0
		for _, id := range movable {
			_, cross := boardAxes(bounds(nodes[id]), horizontal)
			if used > 0 && used+cross > rowWidth {
				row++
				used = 0
			}
			rows[row] = append(rows[row], id)
			used += cross + 40
		}
	}
	widths := make([]float64, len(rows))
	var widest float64
	for rank, ids := range rows {
		for _, id := range ids {
			box := bounds(nodes[id])
			_, cross := boardAxes(box, horizontal)
			widths[rank] += cross + 40
		}
		widest = max(widest, widths[rank])
	}
	offsets := make([]float64, len(rows))
	major := 0.0
	for rank, ids := range rows {
		if len(ids) == 0 {
			break
		}
		offsets[rank] = major
		cross, thickness := (widest-widths[rank])/2, 0.0
		for _, id := range ids {
			node := nodes[id]
			size, breadth := boardAxes(bounds(node), horizontal)
			writeBoardPosition(node, major, cross, horizontal)
			thickness = max(thickness, size)
			cross += breadth + 40
		}
		major += thickness + gap
	}
	// Keep user media and drawings where they were. Put the graph below any
	// fixed content that would overlap it.
	area := boardBox{x: origin.x, y: origin.y, w: widest, h: major}
	if horizontal {
		area.w, area.h = area.h, area.w
	}
	if slices.ContainsFunc(fixed, area.overlaps) {
		for _, box := range fixed {
			area.y = max(area.y, box.y+box.h+80)
		}
	}
	for _, id := range movable {
		node := nodes[id]
		node["x"], node["y"] = asNumber(node["x"])+area.x, asNumber(node["y"])+area.y
	}
	waypoints := map[[2]string][]args{}
	lanes := map[int]int{}
	for _, edge := range board.Edges {
		from, to := boardEndpoint(edge, "source"), boardEndpoint(edge, "target")
		pair := [2]string{from, to}
		if !unique[pair] || ranks[to]-ranks[from] < 2 {
			continue
		}
		// Long connectors pass outside the intermediate ranks, where they
		// cannot cross a box. Offset each connector that shares the same gap.
		first, last := ranks[from]+1, ranks[to]
		lanes[first]++
		lane := widest + 16*float64(lanes[first])
		ends := [2]float64{offsets[first] - gap/2, offsets[last] - gap/2}
		for _, major := range ends {
			point := args{}
			writeBoardPosition(point, major, lane, horizontal)
			point["x"], point["y"] = asNumber(point["x"])+area.x, asNumber(point["y"])+area.y
			waypoints[pair] = append(waypoints[pair], point)
		}
		delete(unique, pair)
	}
	return waypoints
}

func boardAxes(box boardBox, horizontal bool) (major, cross float64) {
	if horizontal {
		return box.w, box.h
	}
	return box.h, box.w
}

func writeBoardPosition(node args, major, cross float64, horizontal bool) {
	node["x"], node["y"] = math.Round(cross), math.Round(major)
	if horizontal {
		node["x"], node["y"] = node["y"], node["x"]
	}
}
