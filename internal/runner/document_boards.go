package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/daidr/Ariadne/internal/jsonutil"
)

const boardPlaceholder = "<pending>画板</pending>"

var documentFence = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})[ \t]*([^\\s`]*)[^`]*$")
var closingFence = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})[ \t]*$")
var jsonStringOrTrailingComma = regexp.MustCompile(`"(?:[^"\\]|\\.)*"|,\s*([}\]])`)

type markdownPart struct {
	raw, lang, body string
	closed          bool
}

func markdownParts(value string) []markdownPart {
	lines := strings.Split(value, "\n")
	var parts []markdownPart
	start := 0
	for i := 0; i < len(lines); i++ {
		open := documentFence.FindStringSubmatch(lines[i])
		if open == nil {
			continue
		}
		if start < i {
			parts = append(parts, markdownPart{raw: strings.Join(lines[start:i], "\n")})
		}
		end := i + 1
		for ; end < len(lines); end++ {
			close := closingFence.FindStringSubmatch(lines[end])
			if close != nil && close[1][0] == open[1][0] && len(close[1]) >= len(open[1]) {
				break
			}
		}
		closed := end < len(lines)
		body := strings.Join(lines[i+1:end], "\n")
		if closed {
			end++
		}
		parts = append(parts, markdownPart{raw: strings.Join(lines[i:end], "\n"), lang: open[2], body: body, closed: closed})
		i, start = end-1, end
	}
	if start < len(lines) {
		parts = append(parts, markdownPart{raw: strings.Join(lines[start:], "\n")})
	}
	return parts
}

type embeddedBoard struct {
	Height int        `json:"height"`
	Board  whiteboard `json:"board"`
}

func (board whiteboard) summary(positions bool) (items, connectors []args) {
	items, connectors = make([]args, 0, len(board.Nodes)), make([]args, 0, len(board.Edges))
	for _, node := range board.Nodes {
		kind, data := boardKind(node), objectField(node, "data")
		if kind == "ink" {
			kind = "drawing"
		}
		item := args{"id": node["id"], "kind": kind}
		if parent := text(node, "parent"); parent != "" {
			item["in"] = parent
		}
		for _, key := range []string{"text", "tone", "fill", "stroke", "textColor", "color"} {
			if value := text(data, key); value != "" {
				item[key] = value
			}
		}
		switch kind {
		case "group":
			if title := text(data, "label"); title != "" {
				item["title"] = title
			}
			for _, key := range []string{"note", "pick"} {
				if value := text(data, key); value != "" {
					item[key] = value
				}
			}
			if round := int(asNumber(data["round"])); round > 0 {
				item["round"] = round
			}
			if confirmed, ok := data["confirmed"].(bool); ok && confirmed {
				item["confirmed"] = true
			}
		case "option":
			for _, key := range []string{"title", "note", "chosen", "group", "recommended", "pick"} {
				if value := data[key]; value != nil && value != false && value != "" {
					item[key] = value
				}
			}
		case "image":
			if src := text(data, "src"); src != "" {
				item["src"] = src
			}
			if alt := text(data, "alt"); alt != "" {
				item["alt"] = alt
			}
		case "prototype":
			for _, key := range []string{"title", "note", "src", "preview"} {
				if value := text(data, key); value != "" {
					item[key] = value
				}
			}
			if html := text(data, "html"); html != "" {
				item["htmlBytes"] = len(html)
			}
			if vw := int(asNumber(data["viewportWidth"])); vw > 0 {
				item["viewportWidth"] = vw
			}
			if vh := int(asNumber(data["viewportHeight"])); vh > 0 {
				item["viewportHeight"] = vh
			}
			if tweaks, ok := data["tweaks"].([]any); ok && len(tweaks) > 0 {
				item["tweaks"] = tweaks
			}
			if tweakValues, ok := data["tweakValues"].(map[string]any); ok && len(tweakValues) > 0 {
				item["tweakValues"] = tweakValues
			}
		case "node":
			if label := text(data, "label"); label != "" {
				item["text"] = label
			}
		}
		if positions {
			box := bounds(node)
			item["x"], item["y"], item["width"], item["height"] = box.x, box.y, box.w, box.h
		}
		items = append(items, item)
	}
	for _, edge := range board.Edges {
		connector := args{"id": edge["id"], "from": boardEndpoint(edge, "source"), "to": boardEndpoint(edge, "target")}
		if label := text(edge, "label"); label != "" {
			connector["label"] = label
		}
		data := objectField(edge, "data")
		for _, key := range []string{"route", "dash", "arrow", "color"} {
			if value := text(data, key); value != "" {
				connector[key] = value
			}
		}
		connectors = append(connectors, connector)
	}
	return items, connectors
}

func boardFence(lang, body string) string {
	return "```" + lang + "\n" + body + "\n```"
}

func (board embeddedBoard) modelText() string {
	encode := func(values []args) string {
		if len(values) == 0 {
			return "[]"
		}
		lines := make([]string, 0, len(values))
		for _, value := range values {
			encoded, _ := jsonutil.Encode(value)
			lines = append(lines, string(encoded))
		}
		return "[\n" + strings.Join(lines, ",\n") + "\n]"
	}
	items, connectors := board.Board.summary(false)
	return `{"items": ` + encode(items) + `, "connectors": ` + encode(connectors) + "}"
}

func storedBoard(part markdownPart) (embeddedBoard, bool) {
	var board embeddedBoard
	if part.lang != "dou-whiteboard" || !part.closed || json.Unmarshal([]byte(part.body), &board) != nil {
		return board, false
	}
	return board, board.Board.Nodes != nil && board.Board.Edges != nil
}

func documentView(content string) string {
	if !strings.Contains(content, "dou-whiteboard") {
		return content
	}
	var out []string
	for _, part := range markdownParts(content) {
		if board, ok := storedBoard(part); ok {
			out = append(out, boardFence("whiteboard", board.modelText()))
		} else {
			out = append(out, part.raw)
		}
	}
	return strings.Join(out, "\n")
}

type boardFenceSpec struct {
	Items      []args  `json:"items"`
	Connectors []args  `json:"connectors"`
	Direction  string  `json:"direction"`
	Relayout   bool    `json:"relayout"`
	Height     float64 `json:"height"`
}

func readBoardSpec(body string) (boardFenceSpec, error) {
	var spec boardFenceSpec
	body = jsonStringOrTrailingComma.ReplaceAllStringFunc(body, func(value string) string {
		if value[0] == ',' {
			return value[len(value)-1:]
		}
		return value
	})
	if !strings.HasPrefix(strings.TrimSpace(body), "{") || json.Unmarshal([]byte(body), &spec) != nil {
		return spec, errors.New("the fence must contain JSON with items and connectors")
	}
	if spec.Direction != "" && spec.Direction != "down" && spec.Direction != "right" {
		return spec, errors.New("direction must be down or right")
	}
	if len(spec.Items)+len(spec.Connectors) > 300 {
		return spec, errors.New("the board may contain at most 300 items and connectors")
	}
	return spec, nil
}

type savedBoard struct {
	raw, view string
	board     embeddedBoard
}

func documentFromView(view, base string, live bool) (string, error) {
	if len(view) > 8<<20 {
		return "", errors.New("document is too large")
	}
	if !strings.Contains(view, "whiteboard") {
		return view, nil
	}
	var boards []savedBoard
	plain, stored := map[string]bool{}, map[string]bool{}
	for _, part := range markdownParts(base) {
		if part.closed && part.lang == "whiteboard" {
			plain[part.body] = true
		}
		if part.closed && part.lang == "dou-whiteboard" {
			stored[part.raw] = true
		}
		if board, ok := storedBoard(part); ok {
			boards = append(boards, savedBoard{part.raw, board.modelText(), board})
		}
	}
	if len(boards) > 128 {
		return "", errors.New("the document contains too many embedded whiteboards")
	}
	parts := markdownParts(view)
	out := make([]string, len(parts))
	matches, taken := map[int]int{}, map[int]bool{}
	specs, invalid := map[int]boardFenceSpec{}, map[int]error{}
	for i, part := range parts {
		out[i] = part.raw
		if part.lang == "dou-whiteboard" && (!part.closed || !stored[part.raw]) {
			if !live {
				return "", errors.New("write an embedded board with a whiteboard fence containing items and connectors")
			}
			out[i] = boardPlaceholder
		}
		if part.lang != "whiteboard" {
			continue
		}
		if !part.closed {
			invalid[i] = errors.New("close the fence with three backticks")
			continue
		}
		exact := slices.IndexFunc(boards, func(board savedBoard) bool { return board.view == strings.TrimSpace(part.body) })
		for exact >= 0 && taken[exact] {
			next := slices.IndexFunc(boards[exact+1:], func(board savedBoard) bool { return board.view == strings.TrimSpace(part.body) })
			if next < 0 {
				exact = -1
			} else {
				exact += next + 1
			}
		}
		if exact >= 0 {
			matches[i], taken[exact], out[i] = exact, true, boards[exact].raw
			continue
		}
		if !plain[part.body] {
			specs[i], invalid[i] = readBoardSpec(part.body)
			if len(specs) > 128 {
				return "", errors.New("the document contains too many embedded whiteboards")
			}
		}
	}
	type candidate struct{ part, board, shared int }
	var candidates []candidate
	for i := range parts {
		spec, ok := specs[i]
		if !ok || invalid[i] != nil {
			continue
		}
		ids := boardItems(slices.Concat(spec.Items, spec.Connectors))
		for k, entry := range boards {
			if taken[k] {
				continue
			}
			common := 0
			for _, item := range slices.Concat(entry.board.Board.Nodes, entry.board.Board.Edges) {
				if ids[text(item, "id")] != nil {
					common++
				}
			}
			if common > 0 {
				candidates = append(candidates, candidate{i, k, common})
			}
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].shared > candidates[j].shared })
	for _, choice := range candidates {
		if _, matched := matches[choice.part]; !matched && !taken[choice.board] {
			matches[choice.part], taken[choice.board] = choice.board, true
		}
	}
	for i, part := range parts {
		spec, hasSpec := specs[i]
		bad, hasError := invalid[i]
		if !hasSpec && !hasError {
			continue
		}
		original := savedBoard{board: embeddedBoard{Board: whiteboard{Version: 1, Nodes: []args{}, Edges: []args{}}}}
		if k, ok := matches[i]; ok {
			original = boards[k]
		} else {
			low, high := -1, len(boards)
			for at := i - 1; at >= 0; at-- {
				if k, ok := matches[at]; ok {
					low = k
					break
				}
			}
			for at := i + 1; at < len(parts); at++ {
				if k, ok := matches[at]; ok {
					high = k
					break
				}
			}
			for k := low + 1; k < high; k++ {
				if !taken[k] {
					original, matches[i], taken[k] = boards[k], k, true
					break
				}
			}
		}
		if bad == nil {
			// Decode the stored board again so each preview starts from the saved state.
			board := original.board
			if original.raw != "" {
				board, _ = storedBoard(markdownParts(original.raw)[0])
			}
			out[i], bad = rewriteEmbeddedBoard(board, spec)
		}
		if bad != nil {
			if !live {
				return "", fmt.Errorf("whiteboard near %q: %w", strings.SplitN(part.raw, "\n", 2)[0], bad)
			}
			out[i] = boardPlaceholder
			if part.closed && original.raw != "" {
				out[i] = original.raw
			}
		}
	}
	return strings.Join(out, "\n"), nil
}

func rewriteEmbeddedBoard(stored embeddedBoard, spec boardFenceSpec) (string, error) {
	board := &stored.Board
	before := boardItems(board.Nodes)
	keepItems := map[string]bool{}
	for i, item := range spec.Items {
		id := text(item, "id")
		keepItems[id] = true
		if old := before[id]; old != nil {
			oldKind := boardKind(old)
			if _, writable := boardSizes[oldKind]; !writable {
				rewritten := args{"id": id, "x": item["x"], "y": item["y"]}
				if oldKind == "ink" {
					if raw, exists := item["color"]; exists {
						if raw == nil {
							rewritten["color"] = ""
						} else {
							rewritten["color"] = raw
						}
					} else if rawTone, hasTone := item["tone"]; hasTone {
						if rawTone == nil {
							rewritten["color"] = ""
						} else {
							rewritten["tone"] = rawTone
						}
					} else {
						rewritten["color"] = ""
					}
				}
				spec.Items[i] = rewritten
				continue
			}
			// Embedded fences carry the full summary styling of each node, so
			// removing a color/border field in the fence clears it on the node.
			if data := objectField(old, "data"); len(data) > 0 {
				for _, key := range []string{"tone", "fill", "stroke", "textColor", "color"} {
					delete(data, key)
				}
				old["data"] = data
			}
		}
		if kind := text(item, "kind"); kind != "" {
			if _, allowed := boardSizes[kind]; !allowed || kind == "pending" {
				return "", fmt.Errorf("unsupported item kind %q in a document", kind)
			}
		}
	}
	beforeEdges := boardItems(board.Edges)
	keepEdges, pairs := map[string]bool{}, map[[2]string]bool{}
	for _, edge := range spec.Connectors {
		var existing args
		if id := text(edge, "id"); id != "" {
			keepEdges[id] = true
			existing = beforeEdges[id]
		} else {
			from, to := text(edge, "from"), text(edge, "to")
			pairs[[2]string{from, to}] = true
			for _, candidate := range board.Edges {
				if boardEndpoint(candidate, "source") == from && boardEndpoint(candidate, "target") == to {
					existing = candidate
					break
				}
			}
		}
		if existing != nil {
			if data := objectField(existing, "data"); len(data) > 0 {
				delete(data, "color")
				delete(data, "tone")
				existing["data"] = data
			}
		}
	}
	var removeItems, removeEdges []string
	for _, item := range board.Nodes {
		if id := text(item, "id"); !keepItems[id] {
			removeItems = append(removeItems, id)
		}
	}
	for _, edge := range board.Edges {
		if id := text(edge, "id"); !keepEdges[id] && !pairs[[2]string{boardEndpoint(edge, "source"), boardEndpoint(edge, "target")}] {
			removeEdges = append(removeEdges, id)
		}
	}
	if err := board.change(spec.Items, spec.Connectors, removeItems, removeEdges, spec.Direction, spec.Relayout, nil, nil); err != nil {
		return "", err
	}
	if spec.Height != 0 {
		stored.Height = int(math.Round(min(1200, max(160, spec.Height))))
	}
	if stored.Height == 0 {
		stored.Height = 360
		if len(board.Nodes) > 0 {
			first := bounds(board.Nodes[0])
			left, right, top, bottom := first.x, first.x+first.w, first.y, first.y+first.h
			for _, item := range board.Nodes[1:] {
				box := bounds(item)
				left, right = min(left, box.x), max(right, box.x+box.w)
				top, bottom = min(top, box.y), max(bottom, box.y+box.h)
			}
			stored.Height = int(math.Round(min(480, max(160, (bottom-top)*min(1, 632/max(1, right-left))+48))))
		}
	}
	raw, err := jsonutil.Encode(stored)
	return boardFence("dou-whiteboard", string(raw)), err
}
