package collab

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/antst/go-yjs/crdt"
)

type contentObject = map[string]any

type whiteboardContent struct {
	Version int             `json:"version"`
	Nodes   []contentObject `json:"nodes"`
	Edges   []contentObject `json:"edges"`
}

type dashboardLayout struct {
	Version int             `json:"version"`
	Widgets []contentObject `json:"widgets"`
}

func ReadBoard(doc *crdt.Doc, kind string) (string, error) {
	var content any
	switch kind {
	case "whiteboard":
		content = readWhiteboard(doc)
	case "dashboard":
		content = readDashboard(doc)
	default:
		return "", fmt.Errorf("unsupported board kind %q", kind)
	}
	encoded, err := json.Marshal(content)
	return string(encoded), err
}

func readWhiteboard(doc *crdt.Doc) whiteboardContent {
	return whiteboardContent{Version: 1, Nodes: readItems(doc.GetMap("nodes")), Edges: readItems(doc.GetMap("edges"))}
}

func readDashboard(doc *crdt.Doc) contentObject {
	snapshot := normalizeJSON(doc.GetMap("snapshot").ToJSON()).(contentObject)
	widgets := orderedWidgets(doc.GetMap("widgets"))
	for _, widget := range widgets {
		delete(widget, "$order")
		if toObject(widget["place"]) == nil {
			widget["place"] = contentObject{}
		}
	}
	snapshot["version"] = 1
	snapshot["dashboard"] = dashboardLayout{Version: 1, Widgets: widgets}
	return snapshot
}

func orderedWidgets(shared *crdt.YMap) []contentObject {
	widgets := slices.DeleteFunc(readItems(shared), func(item contentObject) bool {
		_, ok := item["type"].(string)
		return !ok
	})
	slices.SortFunc(widgets, func(a, b contentObject) int {
		first, _ := a["$order"].(string)
		second, _ := b["$order"].(string)
		if n := strings.Compare(first, second); n != 0 {
			return n
		}
		return strings.Compare(itemID(a), itemID(b))
	})
	return widgets
}

func readItems(shared *crdt.YMap) []contentObject {
	entries := shared.Entries()
	result := make([]contentObject, 0, len(entries))
	for _, id := range slices.Sorted(maps.Keys(entries)) {
		if fields, ok := entries[id].(*crdt.YMap); ok {
			value := normalizeJSON(fields.ToJSON()).(contentObject)
			value["id"] = id
			result = append(result, value)
		}
	}
	return result
}

func toObject(value any) contentObject {
	switch fields := value.(type) {
	case contentObject:
		return fields
	case crdt.Object:
		return fields.ToMap()
	default:
		return nil
	}
}

func itemID(item contentObject) string {
	id, _ := item["id"].(string)
	return id
}

func indexItems(items []contentObject) map[string]contentObject {
	index := make(map[string]contentObject, len(items))
	for _, item := range items {
		index[itemID(item)] = item
	}
	return index
}

func equalJSON(left, right any) bool {
	return reflect.DeepEqual(normalizeJSON(left), normalizeJSON(right))
}

func normalizeJSON(value any) any {
	switch fields := value.(type) {
	case contentObject, crdt.Object:
		object := toObject(fields)
		copy := make(contentObject, len(object))
		for key, field := range object {
			copy[key] = normalizeJSON(field)
		}
		return copy
	case []any:
		copy := make([]any, len(fields))
		for i, field := range fields {
			copy[i] = normalizeJSON(field)
		}
		return copy
	case crdt.NullType:
		return nil
	case int:
		return float64(fields)
	case int64:
		return float64(fields)
	case float32:
		return float64(fields)
	default:
		return value
	}
}

func sharedJSON(value any) any {
	switch fields := value.(type) {
	case contentObject:
		object := crdt.MakeObject()
		for _, key := range slices.Sorted(maps.Keys(fields)) {
			object.Set(key, sharedJSON(fields[key]))
		}
		return object
	case []any:
		copy := make([]any, len(fields))
		for i, entry := range fields {
			copy[i] = sharedJSON(entry)
		}
		return copy
	case nil:
		return crdt.NullType{}
	default:
		return value
	}
}

func parseWhiteboard(content string) (whiteboardContent, error) {
	var board whiteboardContent
	if err := json.Unmarshal([]byte(content), &board); err != nil {
		return board, err
	}
	if board.Version != 1 || board.Nodes == nil || board.Edges == nil {
		return board, errors.New("whiteboard requires version 1, nodes and edges")
	}
	for _, node := range board.Nodes {
		if data := toObject(node["data"]); data != nil {
			// Folding a group is local to an editor.
			delete(data, "collapsed")
		}
	}
	return board, nil
}

// UpdateWhiteboard applies only the difference from base, preserving edits made
// since that version. A missing base replaces the current board.
func UpdateWhiteboard(doc *crdt.Doc, content string, base *string, origin any) error {
	next, err := parseWhiteboard(content)
	if err != nil {
		return err
	}
	before := readWhiteboard(doc)
	if base != nil {
		before, err = parseWhiteboard(*base)
		if err != nil {
			return err
		}
	}
	nodes, edges := doc.GetMap("nodes"), doc.GetMap("edges")
	doc.Transact(func(*crdt.Transaction) {
		syncItems(nodes, next.Nodes, before.Nodes, nil, false)
		syncItems(edges, next.Edges, before.Edges, func(edge contentObject) bool {
			if base == nil {
				return true
			}
			for _, end := range []string{"source", "target"} {
				id, _ := toObject(edge[end])["node"].(string)
				if _, exists := nodes.Get(id).(*crdt.YMap); !exists {
					return false
				}
			}
			return true
		}, false)
	}, origin)
	return nil
}

func syncItems(shared *crdt.YMap, next, before []contentObject, fits func(contentObject) bool, widget bool) {
	current, previous := indexItems(next), indexItems(before)
	for id := range previous {
		if current[id] == nil {
			shared.Delete(id)
		}
	}
	for _, item := range next {
		id := itemID(item)
		fields, active := shared.Get(id).(*crdt.YMap)
		old := previous[id]
		if old == nil {
			if fits != nil && !fits(item) {
				continue
			}
			if !active {
				fields = crdt.NewYMap(nil)
				shared.Set(id, fields)
			}
			if active {
				old = normalizeJSON(fields.ToJSON()).(contentObject)
			}
		}
		if fields != nil {
			changeFields(fields, old, item, widget)
		}
	}
}

func changeFields(fields *crdt.YMap, before, after contentObject, widget bool) {
	for key := range before {
		if _, keep := after[key]; !keep && key != "id" && (!widget || !strings.HasPrefix(key, "$")) {
			fields.Delete(key)
		}
	}
	for key, value := range after {
		if key == "id" || widget && strings.HasPrefix(key, "$") {
			continue
		}
		previous, existed := before[key]
		if existed && equalJSON(previous, value) {
			continue
		}
		if widget && key == "place" {
			places, ok := fields.Get(key).(*crdt.YMap)
			if !ok {
				places = crdt.NewYMap(nil)
				fields.Set(key, places)
			}
			changeFields(places, toObject(previous), toObject(value), false)
		} else {
			fields.Set(key, sharedJSON(value))
		}
	}
}

func parseDashboard(content string) (dashboardLayout, contentObject, error) {
	var layout dashboardLayout
	var snapshot map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content), &snapshot); err != nil {
		return layout, nil, err
	}
	if err := json.Unmarshal(snapshot["dashboard"], &layout); err != nil {
		return layout, nil, err
	}
	if layout.Version != 1 || layout.Widgets == nil {
		return layout, nil, errors.New("dashboard requires a version 1 layout and widgets")
	}
	delete(snapshot, "dashboard")
	value := make(contentObject, len(snapshot))
	for key, raw := range snapshot {
		var decoded any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return layout, nil, err
		}
		value[key] = decoded
	}
	return layout, value, nil
}

// UpdateDashboard applies layout changes from the supplied base and updates the
// data snapshot. Individual breakpoints of one widget can be edited concurrently.
func UpdateDashboard(doc *crdt.Doc, content string, base *string, origin any) error {
	next, snapshot, err := parseDashboard(content)
	if err != nil {
		return err
	}
	widgets, data := doc.GetMap("widgets"), doc.GetMap("snapshot")
	before := orderedWidgets(widgets)
	var beforeSnapshot contentObject
	if base != nil {
		layout, oldSnapshot, err := parseDashboard(*base)
		if err != nil {
			return err
		}
		before = layout.Widgets
		beforeSnapshot = oldSnapshot
	}
	doc.Transact(func(*crdt.Transaction) {
		syncItems(widgets, next.Widgets, before, nil, true)
		applyWidgetOrder(widgets, next.Widgets, before)
		if beforeSnapshot == nil {
			writeSnapshot(data, snapshot)
		} else {
			// Treat data sets individually so concurrent refreshes and layout
			// edits do not overwrite data untouched by this revision.
			beforeData, nextData := toObject(beforeSnapshot["data"]), toObject(snapshot["data"])
			currentData := toObject(normalizeJSON(data.Get("data")))
			if currentData == nil {
				currentData = contentObject{}
			}
			mergeFields(currentData, beforeData, nextData)
			merged := toObject(normalizeJSON(data.ToJSON()))
			delete(beforeSnapshot, "data")
			delete(snapshot, "data")
			mergeFields(merged, beforeSnapshot, snapshot)
			merged["data"] = currentData
			writeSnapshot(data, merged)
		}
	}, origin)
	return nil
}

func mergeFields(current, before, after contentObject) {
	for key := range before {
		if _, keep := after[key]; !keep {
			delete(current, key)
		}
	}
	for key, value := range after {
		if old, existed := before[key]; !existed || !equalJSON(old, value) {
			current[key] = value
		}
	}
}

func writeSnapshot(data *crdt.YMap, snapshot contentObject) {
	for _, key := range data.Keys() {
		if _, exists := snapshot[key]; !exists {
			data.Delete(key)
		}
	}
	for key, value := range snapshot {
		if !data.Has(key) || !equalJSON(data.Get(key), value) {
			data.Set(key, sharedJSON(value))
		}
	}
}
