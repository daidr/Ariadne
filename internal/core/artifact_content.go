package core

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/projectsettings"
)

const maxArtifactJSON = 8 * 1024 * 1024

var whiteboardNodeTypes = []string{"shape", "sticky", "text", "ink", "image", "default", "group", "option", "pending", "prototype"}
var dashboardWidgetTypes = []string{"stat", "text", "table", "chart", "pivot", "pending"}
var prototypeFileURLRe = regexp.MustCompile(`^/api/files/fil_[\w-]+$`)

// ECMAScript whitespace also includes NBSP and FEFF, but excludes U+0085.
const jsSpace = `[\t\n\v\f\r \x{00a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]`

var documentBoard = regexp.MustCompile("(?m)^```dou-whiteboard\n([^\n\r\\x{2028}\\x{2029}]*)\n```$")
var pendingText = regexp.MustCompile(`(?s)<pending>(.*?)</pending>`)
var escapedBrackets = regexp.MustCompile(`\\([[\]])`)
var codeBlocks = regexp.MustCompile("(?s)```.*?```")
var markdownLinks = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)`)
var listMarks = regexp.MustCompile(`(?m)^` + jsSpace + `*(?:[-+]|[0-9]+\.)` + jsSpace + `+`)
var horizontalRule = regexp.MustCompile(`(?m)^` + jsSpace + `*-{3,}` + jsSpace + `*$`)
var markdownMarks = regexp.MustCompile("[#>*_`~|]+")
var spaces = regexp.MustCompile(jsSpace + `+`)
var fenceStart = regexp.MustCompile("(?m)^" + jsSpace + "*```")

func utf16Length(value string) int { return len(utf16.Encode([]rune(value))) }
func utf16Slice(value string, start, end int) string {
	units := utf16.Encode([]rune(value))
	start = min(max(start, 0), len(units))
	end = min(max(end, start), len(units))
	return string(utf16.Decode(units[start:end]))
}
func artifactInvalid(reason string) error {
	return domainerr.New("error.artifactInvalid", map[string]any{"reason": reason})
}
func asObject(value any) object {
	switch value := value.(type) {
	case map[string]any:
		return value
	case object:
		return value
	}
	return nil
}
func number(value any) (float64, bool) {
	var n float64
	var ok bool
	switch value := value.(type) {
	case float64:
		n, ok = value, true
	case float32:
		n, ok = float64(value), true
	case int:
		n, ok = float64(value), true
	case int64:
		n, ok = float64(value), true
	case int32:
		n, ok = float64(value), true
	case uint:
		n, ok = float64(value), true
	case uint64:
		n, ok = float64(value), true
	case json.Number:
		var err error
		n, err = value.Float64()
		ok = err == nil
	}
	return n, ok && !math.IsNaN(n) && !math.IsInf(n, 0)
}
func jsString(value any) string {
	if value == nil {
		return "null"
	}
	switch value := value.(type) {
	case string:
		return value
	case bool:
		if value {
			return "true"
		}
		return "false"
	case map[string]any, object:
		return "[object Object]"
	case []any:
		parts := []string{}
		for _, item := range value {
			if item == nil {
				parts = append(parts, "")
			} else {
				parts = append(parts, jsString(item))
			}
		}
		return strings.Join(parts, ",")
	}
	return fmt.Sprint(value)
}
func jsFieldString(value object, key string) string {
	if v, has := value[key]; has {
		return jsString(v)
	}
	return "undefined"
}

func EmptyContent(kind string) string { return emptyContent(kind) }
func ValidateArtifactContent(kind, content string) error {
	return checkArtifactContent(kind, content)
}
func emptyContent(kind string) string {
	if kind == "whiteboard" {
		return `{"version":1,"nodes":[],"edges":[]}`
	}
	if kind == "dashboard" {
		return `{"version":1,"dashboard":{"version":1,"widgets":[]},"data":{}}`
	}
	return ""
}
func checkArtifactContent(kind, content string) error {
	if kind != "whiteboard" && kind != "dashboard" {
		return nil
	}
	if utf16Length(content) > maxArtifactJSON {
		return artifactInvalid(fmt.Sprintf("larger than %d bytes", maxArtifactJSON))
	}
	var raw any
	if json.Unmarshal([]byte(content), &raw) != nil {
		return artifactInvalid("not JSON")
	}
	if kind == "whiteboard" {
		return checkWhiteboard(raw)
	}
	return checkDashboard(raw, content)
}
func checkWhiteboard(raw any) error {
	value := asObject(raw)
	if value == nil || value["version"] != float64(1) {
		return artifactInvalid("a whiteboard is { version: 1, nodes, edges }")
	}
	nodes, okNodes := value["nodes"].([]any)
	edges, okEdges := value["edges"].([]any)
	if !okNodes || !okEdges {
		return artifactInvalid("a whiteboard needs nodes and edges arrays")
	}
	ids := map[string]bool{}
	containers := map[string]bool{}
	for i, item := range nodes {
		node := asObject(item)
		id := node.str("id")
		if node == nil || id == "" {
			return artifactInvalid(fmt.Sprintf("nodes[%d] has no id", i))
		}
		if ids[id] {
			return artifactInvalid(fmt.Sprintf("node id %q is used twice", id))
		}
		ids[id] = true
		if !slices.Contains(whiteboardNodeTypes, node.str("type")) {
			return artifactInvalid(fmt.Sprintf("node %q has type %s; use %s", id, jsFieldString(node, "type"), strings.Join(whiteboardNodeTypes, ", ")))
		}
		if _, ok := number(node["x"]); !ok {
			return artifactInvalid(fmt.Sprintf("node %q needs numeric x and y", id))
		}
		if _, ok := number(node["y"]); !ok {
			return artifactInvalid(fmt.Sprintf("node %q needs numeric x and y", id))
		}
		if data, has := node["data"]; has && asObject(data) == nil {
			return artifactInvalid(fmt.Sprintf("node %q data is not an object", id))
		}
		if node.str("type") == "option" {
			if _, ok := node.obj("data")["title"].(string); !ok {
				return artifactInvalid(fmt.Sprintf("option %q needs data.title", id))
			}
		}
		if node.str("type") == "prototype" {
			data := node.obj("data")
			html := data.str("html")
			src := data.str("src")
			if html == "" && src == "" {
				return artifactInvalid(fmt.Sprintf("prototype %q needs data.html or data.src", id))
			}
			if src != "" && !prototypeFileURLRe.MatchString(src) {
				return artifactInvalid(fmt.Sprintf("prototype %q data.src must be a project file URL", id))
			}
			if rawW, has := data["viewportWidth"]; has {
				if w, ok := number(rawW); !ok || w <= 0 || w > 10000 {
					return artifactInvalid(fmt.Sprintf("prototype %q data.viewportWidth must be a positive number", id))
				}
			}
			if rawH, has := data["viewportHeight"]; has {
				if h, ok := number(rawH); !ok || h <= 0 || h > 10000 {
					return artifactInvalid(fmt.Sprintf("prototype %q data.viewportHeight must be a positive number", id))
				}
			}
		}
		if node.str("type") == "group" || node.str("type") == "option" {
			containers[id] = true
		}
	}
	for _, item := range nodes {
		node := asObject(item)
		if parent, has := node["parent"]; has {
			if s, ok := parent.(string); !ok || !containers[s] {
				return artifactInvalid(fmt.Sprintf("node %q sits in \"%s\", which is not a group or option", node.str("id"), jsString(parent)))
			}
		}
	}
	edgeIDs := map[string]bool{}
	for i, item := range edges {
		edge := asObject(item)
		id := edge.str("id")
		if edge == nil || id == "" {
			return artifactInvalid(fmt.Sprintf("edges[%d] has no id", i))
		}
		if edgeIDs[id] {
			return artifactInvalid(fmt.Sprintf("edge id %q is used twice", id))
		}
		edgeIDs[id] = true
		for _, end := range []string{"source", "target"} {
			at := edge.obj(end)
			_, nodeOK := at["node"].(string)
			_, portOK := at["port"].(string)
			if at == nil || !nodeOK || !portOK {
				return artifactInvalid(fmt.Sprintf("edge %q %s needs { node, port }", id, end))
			}
			if !ids[at.str("node")] {
				return artifactInvalid(fmt.Sprintf("edge %q %s names a missing node %q", id, end, at.str("node")))
			}
		}
	}
	return nil
}
func checkDashboard(raw any, content string) error {
	value := asObject(raw)
	if value == nil || value["version"] != float64(1) {
		return artifactInvalid("a dashboard is { version: 1, dashboard, data }")
	}
	dashboard, data := value.obj("dashboard"), value.obj("data")
	widgets, ok := dashboard["widgets"].([]any)
	if dashboard == nil || dashboard["version"] != float64(1) || !ok {
		return artifactInvalid("dashboard must be { version: 1, widgets: [] }")
	}
	if data == nil {
		return artifactInvalid("data must be an object of named data sources")
	}
	if at, has := value["takenAt"]; has {
		if _, ok := number(at); !ok {
			return artifactInvalid("takenAt is not a number")
		}
	}
	if source, has := value["source"]; has {
		source := asObject(source)
		if source == nil || source.str("kind") == "" {
			return artifactInvalid("source must be { kind, settings }")
		}
		if settings, has := source["settings"]; has && asObject(settings) == nil {
			return artifactInvalid("source.settings is not an object")
		}
	}
	ids := map[string]bool{}
	var rootJSON, dashboardJSON map[string]json.RawMessage
	var widgetJSON []json.RawMessage
	_ = json.Unmarshal([]byte(content), &rootJSON)
	_ = json.Unmarshal(rootJSON["dashboard"], &dashboardJSON)
	_ = json.Unmarshal(dashboardJSON["widgets"], &widgetJSON)
	for i, item := range widgets {
		widget := asObject(item)
		id := widget.str("id")
		if widget == nil || id == "" {
			return artifactInvalid(fmt.Sprintf("widgets[%d] has no id", i))
		}
		if ids[id] {
			return artifactInvalid(fmt.Sprintf("widget id %q is used twice", id))
		}
		ids[id] = true
		if !slices.Contains(dashboardWidgetTypes, widget.str("type")) {
			return artifactInvalid(fmt.Sprintf("widget %q has type %s; use %s", id, jsFieldString(widget, "type"), strings.Join(dashboardWidgetTypes, ", ")))
		}
		props := widget.obj("props")
		if props == nil {
			return artifactInvalid(fmt.Sprintf("widget %q props is not an object", id))
		}
		if source, has := props["source"]; has {
			key, ok := source.(string)
			_, exists := data[key]
			if !ok || !exists && !slices.Contains([]string{"constructor", "__defineGetter__", "__defineSetter__", "hasOwnProperty", "__lookupGetter__", "__lookupSetter__", "isPrototypeOf", "propertyIsEnumerable", "toString", "valueOf", "__proto__", "toLocaleString"}, key) {
				return artifactInvalid(fmt.Sprintf("widget %q reads source \"%s\", which is not in data", id, jsString(source)))
			}
		}
		if raw, has := widget["place"]; has {
			place := asObject(raw)
			if place == nil {
				return artifactInvalid(fmt.Sprintf("widget %q place is not an object", id))
			}
			var widgetFields map[string]json.RawMessage
			_ = json.Unmarshal(widgetJSON[i], &widgetFields)
			for _, breakpoint := range objectKeys(widgetFields["place"]) {
				item := place[breakpoint]
				position := asObject(item)
				valid := position != nil
				for _, key := range []string{"x", "y", "w", "h"} {
					n, ok := number(position[key])
					valid = valid && ok && n >= 0 && n == math.Trunc(n)
				}
				if !valid {
					return artifactInvalid(fmt.Sprintf("widget %q place.%s needs whole x, y, w, h", id, breakpoint))
				}
			}
		}
	}
	return nil
}

// Object.entries visits integer property names first, then other keys in their
// original order. Preserve that order when reporting the first invalid layout.
func objectKeys(raw []byte) []string {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return nil
	}
	indices, others := []string{}, []string{}
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil
		}
		key, ok := token.(string)
		if !ok {
			return nil
		}
		var skip json.RawMessage
		if decoder.Decode(&skip) != nil {
			return nil
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		if index, err := strconv.ParseUint(key, 10, 32); err == nil && index < 4294967295 && strconv.FormatUint(index, 10) == key {
			indices = append(indices, key)
		} else {
			others = append(others, key)
		}
	}
	slices.SortFunc(indices, func(a, b string) int {
		x, _ := strconv.ParseUint(a, 10, 32)
		y, _ := strconv.ParseUint(b, 10, 32)
		if x < y {
			return -1
		}
		if x > y {
			return 1
		}
		return 0
	})
	return append(indices, others...)
}

func documentLead(content string) string {
	units := utf16.Encode([]rune(content))
	if len(units) <= 600 {
		return content
	}
	cut := 600
	line := -1
	for i := 600; i >= 0; i-- {
		if units[i] == '\n' {
			line = i
			break
		}
	}
	if line > 300 {
		cut = line
	}
	lead := string(utf16.Decode(units[:cut]))
	if len(fenceStart.FindAllStringIndex(lead, -1))%2 == 1 {
		return lead + "\n```"
	}
	return lead
}
type artifactMetadata struct {
	preview        any
	hasPrototype   bool
	pendingActions int
}

func defaultNodeSize(nodeType string) (float64, float64) {
	switch nodeType {
	case "prototype":
		return 480, 336
	case "option", "group":
		return 360, 280
	case "sticky":
		return 200, 140
	case "text":
		return 180, 40
	default:
		return 180, 80
	}
}

func whiteboardMinimap(nodes []any) []object {
	const maxBoxes = 24
	type rawBox struct {
		x, y, w, h float64
		kind       string
	}
	boxes := make([]rawBox, 0, len(nodes))
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for _, item := range nodes {
		node := asObject(item)
		if node == nil {
			continue
		}
		x, xOK := number(node["x"])
		y, yOK := number(node["y"])
		if !xOK || !yOK {
			continue
		}
		nodeType := node.str("type")
		defW, defH := defaultNodeSize(nodeType)
		w, wOK := number(node["width"])
		if !wOK || w <= 0 {
			w = defW
		}
		h, hOK := number(node["height"])
		if !hOK || h <= 0 {
			h = defH
		}
		if x < minX {
			minX = x
		}
		if y < minY {
			minY = y
		}
		if x+w > maxX {
			maxX = x + w
		}
		if y+h > maxY {
			maxY = y + h
		}
		boxes = append(boxes, rawBox{x: x, y: y, w: w, h: h, kind: nodeType})
	}
	if len(boxes) == 0 || math.IsInf(minX, 1) || math.IsInf(minY, 1) {
		return []object{}
	}
	spanX := math.Max(maxX-minX, 1)
	spanY := math.Max(maxY-minY, 1)
	round3 := func(v float64) float64 {
		return math.Round(math.Max(0, math.Min(1, v))*1000) / 1000
	}
	selected := boxes
	if len(boxes) > maxBoxes {
		selected = make([]rawBox, 0, maxBoxes)
		step := float64(len(boxes)) / float64(maxBoxes)
		for i := 0; i < maxBoxes; i++ {
			idx := int(float64(i) * step)
			if idx >= len(boxes) {
				idx = len(boxes) - 1
			}
			selected = append(selected, boxes[idx])
		}
	}
	out := make([]object, 0, len(selected))
	for _, box := range selected {
		out = append(out, object{
			"x":    round3((box.x - minX) / spanX),
			"y":    round3((box.y - minY) / spanY),
			"w":    round3(box.w / spanX),
			"h":    round3(box.h / spanY),
			"kind": box.kind,
		})
	}
	return out
}

func computeArtifactMetadata(kind, content string) artifactMetadata {
	preview := artifactPreview(kind, content)
	switch kind {
	case "whiteboard":
		hasProto := false
		if obj := asObject(preview); obj != nil {
			if n, ok := obj["prototypeCount"].(int); ok && n > 0 {
				hasProto = true
			}
		}
		return artifactMetadata{
			preview:        preview,
			hasPrototype:   hasProto,
			pendingActions: CountPendingDecisionGroups(content),
		}
	case "document":
		return artifactMetadata{
			preview:        preview,
			hasPrototype:   false,
			pendingActions: CountPendingDocumentAsks(content),
		}
	default:
		return artifactMetadata{
			preview:        preview,
			hasPrototype:   false,
			pendingActions: 0,
		}
	}
}

func artifactPreview(kind, content string) any {
	if kind == "whiteboard" || kind == "dashboard" {
		var raw any
		if json.Unmarshal([]byte(content), &raw) == nil {
			value := asObject(raw)
			if kind == "whiteboard" {
				nodes, nodesOK := value["nodes"].([]any)
				edges, edgesOK := value["edges"].([]any)
				valid := nodesOK && edgesOK
				texts := []string{}
				prototypeCount := 0
				optionCount := 0
				previewImage := ""
				for _, item := range nodes {
					if item == nil {
						valid = false
						break
					}
					node := asObject(item)
					nodeType := node.str("type")
					if nodeType == "prototype" {
						prototypeCount++
					} else if nodeType == "option" {
						optionCount++
					}
					data := node.obj("data")
					if nodeType == "prototype" && previewImage == "" {
						if pic, ok := data["preview"].(string); ok && prototypeFileURLRe.MatchString(pic) {
							previewImage = pic
						}
					}
					if len(texts) < 6 {
						for _, field := range []string{"text", "title", "label"} {
							if text, ok := data[field].(string); ok && projectsettings.Trim(text) != "" {
								texts = append(texts, text)
								break
							}
						}
					}
				}
				if valid {
					out := object{
						"kind":           kind,
						"items":          len(nodes),
						"connectors":     len(edges),
						"texts":          texts,
						"prototypeCount": prototypeCount,
						"optionCount":    optionCount,
						"minimap":        whiteboardMinimap(nodes),
					}
					if previewImage != "" {
						out["previewImage"] = previewImage
					}
					return out
				}
			} else {
				widgets, widgetsOK := value.obj("dashboard")["widgets"].([]any)
				valid := widgetsOK
				titles := []any{}
				tiles := []any{}
				bottom := float64(0)
				for _, item := range widgets {
					if item == nil {
						valid = false
						break
					}
					widget := asObject(item)
					if title, has := widget["title"]; has && truthy(title) && len(titles) < 6 {
						titles = append(titles, title)
					}
				}
				limited := widgets[:min(len(widgets), 60)]
				for _, item := range limited {
					widget := asObject(item)
					if place := widget.obj("place")["lg"]; truthy(place) {
						tile := object{"title": widget["title"]}
						if value, has := widget["type"]; has {
							tile["type"] = value
						}
						for key, value := range asObject(place) {
							tile[key] = value
						}
						tiles = append(tiles, tile)
						p := asObject(place)
						y, yOK := number(p["y"])
						h, hOK := number(p["h"])
						if !yOK || !hOK {
							valid = false
							break
						}
						bottom = math.Max(bottom, y+h)
					}
				}
				free := 0
				for _, item := range limited {
					widget := asObject(item)
					if truthy(widget.obj("place")["lg"]) {
						continue
					}
					tile := object{"title": widget["title"], "x": (free % 2) * 6, "y": bottom + float64(free/2)*3, "w": 6, "h": 3}
					if value, has := widget["type"]; has {
						tile["type"] = value
					}
					tiles = append(tiles, tile)
					free++
				}
				if valid {
					isLive := value.obj("source").str("kind") != ""
					return object{"kind": kind, "widgets": len(widgets), "takenAt": value["takenAt"], "titles": titles, "tiles": tiles, "isLive": isLive}
				}
			}
		}
		if kind == "whiteboard" {
			return object{"kind": kind, "items": 0, "connectors": 0, "texts": []any{}, "prototypeCount": 0, "optionCount": 0, "minimap": []any{}}
		}
		return object{"kind": "dashboard", "widgets": 0, "takenAt": nil, "titles": []any{}, "tiles": []any{}, "isLive": false}
	}
	shown := documentBoard.ReplaceAllString(content, "")
	shown = pendingText.ReplaceAllString(shown, `\[${1}\]`)
	text := escapedBrackets.ReplaceAllString(shown, "${1}")
	text = codeBlocks.ReplaceAllString(text, " ")
	text = markdownLinks.ReplaceAllString(text, "${1}")
	text = listMarks.ReplaceAllString(text, " ")
	text = horizontalRule.ReplaceAllString(text, " ")
	text = markdownMarks.ReplaceAllString(text, " ")
	text = projectsettings.Trim(spaces.ReplaceAllString(text, " "))
	if utf16Length(text) > 160 {
		text = utf16Slice(text, 0, 160) + "…"
	}
	return object{"kind": "document", "excerpt": text, "lead": documentLead(shown)}
}
// LightweightArtifactContent trims bulky inline payloads that are not needed
// when rendering a thumbnail (currently inline HTML inside whiteboard prototype
// nodes). Original content is returned unchanged when nothing needs trimming.
func LightweightArtifactContent(kind, content string) string {
	if kind != "whiteboard" || content == "" {
		return content
	}
	var doc object
	if err := json.Unmarshal([]byte(content), &doc); err != nil || doc == nil {
		return content
	}
	nodes, ok := doc["nodes"].([]any)
	if !ok || len(nodes) == 0 {
		return content
	}
	changed := false
	for _, item := range nodes {
		node := asObject(item)
		if node == nil || node.str("type") != "prototype" {
			continue
		}
		data := node.obj("data")
		if data == nil {
			continue
		}
		if html, ok := data["html"].(string); ok && html != "" {
			delete(data, "html")
			changed = true
		}
	}
	if !changed {
		return content
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return content
	}
	return string(out)
}

func truthy(value any) bool {
	if value == nil {
		return false
	}
	switch value := value.(type) {
	case bool:
		return value
	case string:
		return value != ""
	case float64:
		return value != 0 && !math.IsNaN(value)
	}
	return true
}
