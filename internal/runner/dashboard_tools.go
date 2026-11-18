package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/daidr/Ariadne/internal/dashboard"
	"github.com/daidr/Ariadne/internal/jsonutil"
)

func (turn *turn) dashboardSource(ctx context.Context, raw any) (args, args, error) {
	spec, _ := raw.(args)
	kind := text(spec, "kind")
	provider := turn.Dashboards.Get(kind)
	if provider == nil {
		return nil, nil, fmt.Errorf("dashboard source %q is unavailable", kind)
	}
	settings, err := dashboard.CheckSettings(provider.Info, spec["settings"])
	if err != nil {
		return nil, nil, err
	}
	data, err := provider.Fetch(ctx, settings, turn.conversation.ProjectID)
	return args{"kind": kind, "settings": settings}, data, err
}

func (turn *turn) createDashboard(ctx context.Context, input args) (string, error) {
	input = maps.Clone(input)
	if input["data"] == nil {
		input["data"] = args{}
	}
	merge, err := turn.dashboardChange(ctx, input)
	if err != nil {
		return "", err
	}
	content, err := merge(`{"version":1,"dashboard":{"version":1,"widgets":[]},"data":{}}`)
	if err != nil {
		return "", err
	}
	return turn.createArtifact(ctx, "dashboard", input, content)
}

func (turn *turn) updateDashboard(ctx context.Context, input args) (string, error) {
	artifact, err := turn.projectArtifact(ctx, text(input, "artifactId"))
	if err != nil {
		return "", err
	}
	if artifact.Kind != "dashboard" {
		return "", errors.New("a dashboard is required")
	}
	merge, err := turn.dashboardChange(ctx, input)
	if err != nil {
		return "", err
	}
	return turn.editArtifact(ctx, "dashboard", input, true, merge)
}

func (turn *turn) dashboardChange(ctx context.Context, input args) (func(string) (string, error), error) {
	var data args
	if raw, supplied := input["data"]; supplied {
		var ok bool
		data, ok = raw.(args)
		if !ok || data == nil {
			return nil, errors.New("data must be an object of named data sets")
		}
		data = maps.Clone(data)
	}
	sourceValue, setSource := input["source"]
	var source args
	if sourceValue != nil {
		var fetched args
		var err error
		source, fetched, err = turn.dashboardSource(ctx, sourceValue)
		if err != nil {
			return nil, err
		}
		if data == nil {
			data = args{}
		}
		maps.Copy(data, fetched)
	}
	removeData, err := stringsOf(input["removeData"])
	if err != nil {
		return nil, err
	}
	removeWidgets, err := stringsOf(input["removeWidgets"])
	if err != nil {
		return nil, err
	}
	widgets, err := artifactObjects(input["widgets"])
	if err != nil {
		return nil, err
	}
	return func(base string) (string, error) {
		var content struct {
			Version   int `json:"version"`
			Dashboard struct {
				Version int    `json:"version"`
				Widgets []args `json:"widgets"`
			} `json:"dashboard"`
			Data    args   `json:"data"`
			Source  args   `json:"source,omitempty"`
			TakenAt *int64 `json:"takenAt,omitempty"`
		}
		if err := json.Unmarshal([]byte(base), &content); err != nil {
			return "", err
		}
		content.Dashboard.Widgets, err = packDashboard(content.Dashboard.Widgets, widgets, removeWidgets)
		if err != nil {
			return "", err
		}
		for _, key := range removeData {
			delete(content.Data, key)
		}
		if data != nil {
			maps.Copy(content.Data, data)
			content.TakenAt = new(time.Now().UnixMilli())
		}
		if setSource {
			content.Source = source
		}
		encoded, err := jsonutil.Encode(content)
		return string(encoded), err
	}, nil
}

func artifactObjects(raw any) ([]args, error) {
	if raw == nil {
		return nil, nil
	}
	values, ok := raw.([]any)
	if !ok || len(values) > 512 {
		return nil, errors.New("expected an array of at most 512 objects")
	}
	out := make([]args, 0, len(values))
	for _, value := range values {
		item, ok := value.(args)
		if !ok || item == nil {
			return nil, errors.New("array entries must be objects")
		}
		out = append(out, item)
	}
	return out, nil
}

func widgetSize(raw any, fallback, upper int) (int, error) {
	if raw == nil {
		return fallback, nil
	}
	n, ok := raw.(float64)
	if !ok || n != math.Trunc(n) || math.IsInf(n, 0) {
		return 0, errors.New("widget width and height must be whole numbers")
	}
	return int(min(float64(upper), max(1, n))), nil
}

func packDashboard(existing, changes []args, remove []string) ([]args, error) {
	current := make([]args, 0, len(existing)+len(changes))
	indexes := map[string]int{}
	for _, widget := range existing {
		id := text(widget, "id")
		if !slices.Contains(remove, id) {
			indexes[id] = len(current)
			current = append(current, widget)
		}
	}
	original := len(current)
	sizes := map[string][2]int{"stat": {3, 2}, "text": {6, 2}, "table": {6, 4}, "chart": {6, 4}, "pivot": {12, 5}, "pending": {6, 3}}
	seen := map[string]bool{}
	for _, change := range changes {
		id, kind := text(change, "id"), text(change, "type")
		if id == "" || seen[id] {
			return nil, errors.New("each widget needs a unique id")
		}
		seen[id] = true
		index, existed := indexes[id]
		widget := args{"id": id, "props": args{}}
		if existed {
			widget = current[index]
			last := text(widget, "type")
			if kind == "" {
				kind = last
			}
			if kind != last && last != "pending" {
				return nil, fmt.Errorf("remove widget %q before changing its type", id)
			}
			if kind != last {
				widget["props"] = args{}
			}
		}
		size, known := sizes[kind]
		if !known {
			return nil, fmt.Errorf("widget %q has an unknown type %q", id, kind)
		}
		widget["type"] = kind
		if raw := change["props"]; raw != nil {
			props, ok := raw.(args)
			if !ok {
				return nil, fmt.Errorf("widget %q props must be an object", id)
			}
			widget["props"] = props
		}
		if title, ok := change["title"].(string); ok {
			if title == "" {
				delete(widget, "title")
			} else {
				widget["title"] = title
			}
		}
		props, _ := widget["props"].(args)
		if err := checkWidgetProps(kind, props); err != nil {
			return nil, fmt.Errorf("widget %q: %w", id, err)
		}
		if existed {
			place, _ := widget["place"].(args)
			large, _ := place["lg"].(args)
			if large != nil && (change["w"] != nil || change["h"] != nil) {
				w, err := widgetSize(change["w"], int(asNumber(large["w"])), 12)
				if err != nil {
					return nil, err
				}
				h, err := widgetSize(change["h"], int(asNumber(large["h"])), 40)
				if err != nil {
					return nil, err
				}
				large["w"], large["h"] = w, h
				widget["place"] = args{"lg": large}
			}
			continue
		}
		w, err := widgetSize(change["w"], size[0], 12)
		if err != nil {
			return nil, err
		}
		h, err := widgetSize(change["h"], size[1], 40)
		if err != nil {
			return nil, err
		}
		widget["place"] = args{"lg": args{"w": w, "h": h}}
		current = append(current, widget)
	}
	x, y, row := 0, 0, 0
	for _, widget := range current[:original] {
		place, _ := widget["place"].(args)
		large, _ := place["lg"].(args)
		y = max(y, int(asNumber(large["y"])+asNumber(large["h"])))
	}
	for _, widget := range current[original:] {
		position := widget["place"].(args)["lg"].(args)
		w, h := position["w"].(int), position["h"].(int)
		if x+w > 12 {
			x, y, row = 0, y+row, 0
		}
		position["x"], position["y"] = x, y
		x, row = x+w, max(row, h)
	}
	return current, nil
}

var widgetProps = map[string]map[string]string{
	"stat":    {"source": "text", "field": "text", "value": "number", "prefix": "text", "suffix": "text", "precision": "precision", "trend": "text", "delta": "text"},
	"text":    {"text": "text", "size": "sm md lg"},
	"table":   {"source": "text", "path": "text", "columns": "columns", "rowKey": "text"},
	"chart":   {"source": "text", "path": "text", "kind": "line area bar pie", "x": "text", "y": "texts", "labels": "names", "stacked": "flag", "horizontal": "flag", "filter": "text"},
	"pivot":   {"source": "text", "path": "text", "config": "pivot", "filter": "flag"},
	"pending": {"text": "text"},
}

func checkWidgetProps(kind string, props args) error {
	for _, key := range slices.Sorted(maps.Keys(props)) {
		want, ok := widgetProps[kind][key]
		if !ok || !widgetPropFits(want, props[key]) {
			return fmt.Errorf("invalid prop %q for %s", key, kind)
		}
	}
	switch kind {
	case "stat":
		if props["field"] == nil && props["value"] == nil {
			return errors.New("a stat needs value or source and field")
		}
		if props["source"] == nil && (props["field"] != nil || props["trend"] != nil || props["delta"] != nil) {
			return errors.New("field, trend and delta need a source")
		}
	case "chart":
		if props["source"] == nil || props["x"] == nil || props["y"] == nil {
			return errors.New("a chart needs source, x and y")
		}
	case "table", "pivot":
		if props["source"] == nil {
			return fmt.Errorf("a %s needs a source", kind)
		}
	}
	return nil
}

func widgetPropFits(want string, raw any) bool {
	switch want {
	case "text":
		_, ok := raw.(string)
		return ok
	case "number", "precision":
		n, ok := raw.(float64)
		return ok && (want == "number" || n >= 0 && n <= 20 && n == math.Trunc(n))
	case "flag":
		_, ok := raw.(bool)
		return ok
	case "names":
		names, ok := raw.(args)
		for _, value := range names {
			if !widgetPropFits("text", value) {
				return false
			}
		}
		return ok
	case "texts", "columns":
		list, ok := raw.([]any)
		for _, value := range list {
			if want == "texts" && !widgetPropFits("text", value) {
				return false
			}
			if want == "columns" {
				item, _ := value.(args)
				if !widgetPropFits("text", item["key"]) || item["title"] != nil && !widgetPropFits("text", item["title"]) {
					return false
				}
			}
		}
		return ok
	case "pivot":
		return checkPivot(raw)
	default:
		value, ok := raw.(string)
		return ok && slices.Contains(strings.Fields(want), value)
	}
}

func checkPivot(raw any) bool {
	config, ok := raw.(args)
	if !ok {
		return false
	}
	for _, key := range []string{"rows", "columns", "values"} {
		value, exists := config[key]
		if !exists {
			continue
		}
		entries, ok := value.([]any)
		if !ok {
			return false
		}
		for _, entry := range entries {
			if _, ok := entry.(string); ok {
				continue
			}
			field, ok := entry.(args)
			if !ok || (key != "values" || field["field"] != nil) && !widgetPropFits("text", field["field"]) {
				return false
			}
			if key == "values" && (field["aggregate"] != nil && !widgetPropFits("sum count avg min max distinct median", field["aggregate"]) ||
				field["label"] != nil && !widgetPropFits("text", field["label"])) {
				return false
			}
		}
	}
	return true
}
