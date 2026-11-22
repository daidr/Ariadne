package core

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf16"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/projectsettings"
)

type planGraph struct {
	nodes map[string]object
	order []string
	edges []string
}

func emptyGraph() planGraph {
	return planGraph{nodes: map[string]object{}, order: []string{}, edges: []string{}}
}
func (graph planGraph) clone() planGraph {
	next := emptyGraph()
	next.order = append(next.order, graph.order...)
	next.edges = append(next.edges, graph.edges...)
	for key, node := range graph.nodes {
		next.nodes[key] = except(node, "")
	}
	return next
}
func edgeParts(key string) (string, string) {
	parts := strings.Split(key, ">")
	if len(parts) < 2 {
		return parts[0], ""
	}
	return parts[0], parts[1]
}
func reaches(graph planGraph, start, target string) bool {
	seen := map[string]bool{}
	stack := []string{start}
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if current == target {
			return true
		}
		if seen[current] {
			continue
		}
		seen[current] = true
		for _, edge := range graph.edges {
			from, to := edgeParts(edge)
			if from == current {
				stack = append(stack, to)
			}
		}
	}
	return false
}
func invalidPlan(reason string) error {
	return domainerr.New("error.planInvalid", map[string]any{"reason": reason})
}

func applyOps(graph planGraph, ops []object) (planGraph, []object, error) {
	next := graph.clone()
	inverse := []object{}
	prepend := func(values ...object) { inverse = append(values, inverse...) }
	for index, op := range ops {
		where := fmt.Sprintf("ops[%d]", index)
		id := op.str("id")
		node := next.nodes[id]
		switch op.str("op") {
		case "add":
			if node != nil {
				return planGraph{}, nil, invalidPlan(where + ": node " + id + " exists")
			}
			title := projectsettings.Trim(op.str("title"))
			if title == "" {
				return planGraph{}, nil, invalidPlan(where + ": title is empty")
			}
			group := projectsettings.Trim(op.str("group"))
			next.nodes[id] = object{"kind": op["kind"], "title": title, "description": op["description"], "acceptance": op["acceptance"], "group": group}
			next.order = append(next.order, id)
			prepend(object{"op": "remove", "id": id})
		case "update":
			if node == nil {
				return planGraph{}, nil, invalidPlan(where + ": no node " + id)
			}
			for _, field := range []string{"title", "description", "acceptance", "group"} {
				if expected, ok := op.obj("expect")[field]; ok && node[field] != expected {
					return planGraph{}, nil, domainerr.New("error.planConflict", map[string]any{"title": node["title"], "reason": where + ": " + field + " of " + id + " changed since"})
				}
			}
			restore := object{"op": "update", "id": id}
			after := object{}
			for _, field := range []string{"title", "description", "acceptance", "group"} {
				value, ok := op[field]
				if !ok {
					continue
				}
				if field == "title" {
					value = projectsettings.Trim(op.str("title"))
					if value == "" {
						return planGraph{}, nil, invalidPlan(where + ": title is empty")
					}
				} else if field == "group" {
					value = projectsettings.Trim(op.str("group"))
				}
				restore[field] = node[field]
				node[field] = value
				after[field] = value
			}
			restore["expect"] = map[string]any(after)
			prepend(restore)
		case "remove":
			if node == nil {
				return planGraph{}, nil, invalidPlan(where + ": no node " + id)
			}
			add := except(node, "")
			add["op"] = "add"
			add["id"] = id
			undo := []object{add}
			edges := []string{}
			for _, edge := range next.edges {
				from, to := edgeParts(edge)
				if from == id || to == id {
					undo = append(undo, object{"op": "link", "from": from, "to": to})
				} else {
					edges = append(edges, edge)
				}
			}
			next.edges = edges
			delete(next.nodes, id)
			next.order = slices.DeleteFunc(next.order, func(value string) bool { return value == id })
			prepend(undo...)
		case "link":
			from, to := op.str("from"), op.str("to")
			key := from + ">" + to
			if next.nodes[from] == nil {
				return planGraph{}, nil, invalidPlan(where + ": no node " + from)
			}
			if next.nodes[to] == nil {
				return planGraph{}, nil, invalidPlan(where + ": no node " + to)
			}
			if from == to {
				return planGraph{}, nil, invalidPlan(where + ": a node cannot depend on itself")
			}
			if slices.Contains(next.edges, key) {
				return planGraph{}, nil, invalidPlan(where + ": already linked")
			}
			if reaches(next, to, from) {
				return planGraph{}, nil, invalidPlan(where + ": would create a cycle")
			}
			next.edges = append(next.edges, key)
			prepend(object{"op": "unlink", "from": from, "to": to})
		case "unlink":
			key := op.str("from") + ">" + op.str("to")
			if !slices.Contains(next.edges, key) {
				return planGraph{}, nil, invalidPlan(where + ": not linked")
			}
			next.edges = slices.DeleteFunc(next.edges, func(value string) bool { return value == key })
			prepend(object{"op": "link", "from": op["from"], "to": op["to"]})
		}
	}
	return next, inverse, nil
}

func parsePlanOps(raw any) ([]object, error) {
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return nil, invalidPlan("ops must be a non-empty array")
	}
	parsed := []object{}
	for index, value := range items {
		where := fmt.Sprintf("ops[%d]", index)
		fields, ok := value.(map[string]any)
		if !ok {
			if _, array := value.([]any); !array {
				return nil, invalidPlan(where + ": not an object")
			}
			fields = map[string]any{}
		}
		op := object(fields)
		var err error
		id := func(key string) string {
			value, ok := op[key].(string)
			if !ok || value == "" || len(utf16.Encode([]rune(value))) > 80 {
				if err == nil {
					err = invalidPlan(where + ": bad id")
				}
			}
			return value
		}
		text := func(fields object, key string) (string, bool) {
			value, has := fields[key]
			if !has {
				return "", false
			}
			s, ok := value.(string)
			if !ok || len(utf16.Encode([]rune(s))) > 20000 {
				if err == nil {
					err = invalidPlan(where + ": not text")
				}
			}
			return s, true
		}
		switch op.str("op") {
		case "add":
			kind := fallback(op["kind"], "task")
			if kind != "task" && kind != "milestone" && kind != "decision" {
				return nil, invalidPlan(where + ": bad kind")
			}
			clean := object{"op": "add", "id": id("id"), "kind": kind}
			for _, key := range []string{"title", "description", "acceptance", "group"} {
				value, _ := text(op, key)
				clean[key] = value
			}
			parsed = append(parsed, clean)
		case "update":
			clean := object{"op": "update", "id": id("id")}
			for _, key := range []string{"title", "description", "acceptance", "group"} {
				if value, has := text(op, key); has {
					clean[key] = value
				}
			}
			if value, has := op["expect"]; has {
				fields, ok := value.(map[string]any)
				if !ok {
					if _, array := value.([]any); !array {
						if err == nil {
							err = invalidPlan(where + ": bad expect")
						}
					}
					fields = map[string]any{}
				}
				expect := object{}
				for _, key := range []string{"title", "description", "acceptance", "group"} {
					if value, has := text(fields, key); has {
						expect[key] = value
					}
				}
				clean["expect"] = map[string]any(expect)
			}
			parsed = append(parsed, clean)
		case "remove":
			parsed = append(parsed, object{"op": "remove", "id": id("id")})
		case "link", "unlink":
			parsed = append(parsed, object{"op": op["op"], "from": id("from"), "to": id("to")})
		default:
			return nil, invalidPlan(where + ": unknown op")
		}
		if err != nil {
			return nil, err
		}
	}
	return parsed, nil
}

func whyUndoFails(graph planGraph, undo []object, titles map[string]string) any {
	at := graph
	for _, op := range undo {
		next, _, err := applyOps(at, []object{op})
		if err == nil {
			at = next
			continue
		}
		title := func(id string) string {
			if node := at.nodes[id]; node != nil {
				return node.str("title")
			}
			return titles[id]
		}
		gone := func(id string) any {
			if name := title(id); name != "" {
				return object{"key": "error.planUndoGone", "params": object{"title": name}}
			}
			return object{"key": "error.planUndoGoneUnnamed"}
		}
		switch op.str("op") {
		case "add":
			name := title(op.str("id"))
			if name == "" {
				name = op.str("title")
			}
			return object{"key": "error.planUndoChanged", "params": object{"title": name}}
		case "remove", "update":
			if at.nodes[op.str("id")] == nil {
				return gone(op.str("id"))
			}
			return object{"key": "error.planUndoChanged", "params": object{"title": title(op.str("id"))}}
		case "link", "unlink":
			if at.nodes[op.str("from")] == nil {
				return gone(op.str("from"))
			}
			if at.nodes[op.str("to")] == nil {
				return gone(op.str("to"))
			}
			return object{"key": "error.planUndoLink", "params": object{"from": title(op.str("from")), "to": title(op.str("to"))}}
		}
	}
	return nil
}

func previewPlanOps(graph planGraph, ops []object) any {
	after, _, err := applyOps(graph, ops)
	if err != nil {
		return nil
	}
	keys := append([]string{}, graph.edges...)
	for _, key := range after.edges {
		if !slices.Contains(keys, key) {
			keys = append(keys, key)
		}
	}
	edges := []object{}
	for _, key := range keys {
		from, to := edgeParts(key)
		var mark any
		if !slices.Contains(graph.edges, key) {
			mark = "added"
		} else if !slices.Contains(after.edges, key) {
			mark = "removed"
		}
		edges = append(edges, object{"from": from, "to": to, "mark": mark})
	}
	ids := append([]string{}, graph.order...)
	for _, id := range after.order {
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	nodes := []object{}
	for _, id := range ids {
		was, now := graph.nodes[id], after.nodes[id]
		fields := now
		if now == nil {
			fields = was
		}
		node := except(fields, "")
		node["id"] = id
		var mark any
		if was == nil {
			mark = "added"
		} else if now == nil {
			mark = "removed"
		} else if was["title"] != now["title"] || was["description"] != now["description"] || was["acceptance"] != now["acceptance"] || was["group"] != now["group"] {
			mark = "changed"
		}
		node["mark"] = mark
		depends := []string{}
		for _, edge := range edges {
			if edge["to"] == id {
				depends = append(depends, edge.str("from"))
			}
		}
		node["dependsOn"] = depends
		nodes = append(nodes, node)
	}
	return object{"nodes": nodes, "edges": edges}
}

func describePlanOps(graph planGraph, ops []object, threadsOn func(string) int) []object {
	added := map[string]string{}
	for _, op := range ops {
		if op.str("op") == "add" {
			added[op.str("id")] = op.str("title")
		}
	}
	title := func(id string) string {
		if node := graph.nodes[id]; node != nil {
			return node.str("title")
		}
		if value, has := added[id]; has {
			return value
		}
		return id
	}
	message := func(key string, params object) object { return object{"key": key, "params": map[string]any(params)} }
	lines := []object{}
	for _, op := range ops {
		switch op.str("op") {
		case "add":
			key := "plan.op.add"
			if op.str("kind") == "milestone" {
				key = "plan.op.addMilestone"
			}
			lines = append(lines, message(key, object{"title": op["title"]}))
		case "remove":
			count := threadsOn(op.str("id"))
			params := object{"title": title(op.str("id"))}
			key := "plan.op.remove"
			if count > 0 {
				key = "plan.op.removeWithThreads"
				params["count"] = count
			}
			lines = append(lines, message(key, params))
		case "update":
			node := graph.nodes[op.str("id")]
			changes := []object{}
			name := title(op.str("id"))
			if _, has := op["title"]; has && projectsettings.Trim(op.str("title")) != node["title"] {
				changes = append(changes, message("plan.op.rename", object{"title": name, "value": projectsettings.Trim(op.str("title"))}))
			}
			if value, has := op["acceptance"]; has && value != node["acceptance"] {
				if value == "" {
					value = "—"
				}
				changes = append(changes, message("plan.op.acceptance", object{"title": name, "value": value}))
			}
			if _, has := op["group"]; has {
				nextGroup := projectsettings.Trim(op.str("group"))
				if nextGroup != node.str("group") {
					if nextGroup == "" {
						changes = append(changes, message("plan.op.ungroup", object{"title": name}))
					} else {
						changes = append(changes, message("plan.op.group", object{"title": name, "value": nextGroup}))
					}
				}
			}
			if value, has := op["description"]; has && value != node["description"] {
				changes = append(changes, message("plan.op.update", object{"title": name}))
			}
			if len(changes) == 0 {
				changes = append(changes, message("plan.op.update", object{"title": name}))
			}
			lines = append(lines, changes...)
		case "link", "unlink":
			lines = append(lines, message("plan.op."+op.str("op"), object{"from": title(op.str("from")), "to": title(op.str("to"))}))
		}
	}
	return lines
}
