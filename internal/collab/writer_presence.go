package collab

import (
	"encoding/json"
	"maps"
	"math/rand/v2"
	"slices"
	"time"
	"unicode/utf16"

	"github.com/antst/go-yjs/crdt"
)

const writerPresenceLinger = 2500 * time.Millisecond

// observeWriter collects positions while Yjs still has the transaction delta.
// Its result is sent after the corresponding document update.
func observeWriter(active *room, run func(), writer string) map[string]any {
	if writer == "" || len(active.peers) == 0 {
		run()
		return nil
	}
	var rootName string
	switch active.artifact.Kind {
	case "document":
		rootName = "document"
	case "whiteboard":
		rootName = "nodes"
	case "dashboard":
		rootName = "widgets"
	}
	root := active.doc.GetMap(rootName)
	var spot map[string]any
	var touched []string
	listener := func(payload, _ any) {
		events, _ := payload.([]crdt.IEventType)
		for _, event := range events {
			path := event.Path()
			if len(path) == 0 {
				if changed, ok := event.(*crdt.YMapEvent); ok {
					for _, id := range slices.Sorted(maps.Keys(changed.GetKeys())) {
						if changed.GetKeys()[id].Action != crdt.ActionDelete {
							touched = append(touched, id)
						}
					}
				}
				continue
			}
			id, ok := path[0].(string)
			if !ok {
				continue
			}
			if rootName == "document" {
				if te, ok := event.(*crdt.YTextEvent); ok {
					if position := changedCursor(id, path, te); position != nil {
						spot = position
					}
				}
			} else if rootName != "nodes" || !geometryOnly(event, path) {
				touched = append(touched, id)
			}
		}
	}
	root.ObserveDeep(listener)
	defer root.UnobserveDeep(listener)
	run()
	switch rootName {
	case "document":
		if spot == nil {
			for i := len(touched) - 1; i >= 0; i-- {
				block, _ := root.Get(touched[i]).(*crdt.YMap)
				if block != nil && block.Get("cells") == nil {
					if text, ok := block.Get("text").(*crdt.YText); ok && text.GetLength() != 0 {
						spot = cursorPosition(touched[i], "", text, text.GetLength())
						break
					}
				}
			}
		}
		if spot != nil {
			return map[string]any{"editor": map[string]any{"anchor": spot, "head": spot}}
		}
	case "nodes":
		var selected []string
		for _, id := range touched {
			if root.Get(id) != nil && !slices.Contains(selected, id) {
				selected = append(selected, id)
			}
			if len(selected) == 128 {
				break
			}
		}
		if len(selected) != 0 {
			return map[string]any{"flow": map[string]any{"moving": []any{}, "path": nil,
				"selection": map[string]any{"nodes": selected, "edges": []any{}}}}
		}
	case "widgets":
		for i := len(touched) - 1; i >= 0; i-- {
			if root.Get(touched[i]) != nil {
				return map[string]any{"dashboard": map[string]any{"widget": touched[i], "action": "focus"}}
			}
		}
	}
	return nil
}

func geometryOnly(event crdt.IEventType, path []any) bool {
	node, ok := event.(*crdt.YMapEvent)
	if !ok || len(path) != 1 {
		return false
	}
	for key := range node.GetKeys() {
		if !slices.Contains([]string{"x", "y", "width", "height", "z"}, key) {
			return false
		}
	}
	return true
}

func changedCursor(id string, path []any, event *crdt.YTextEvent) map[string]any {
	text, ok := event.GetTarget().(*crdt.YText)
	if !ok || len(path) < 2 {
		return nil
	}
	cell := ""
	switch {
	case len(path) == 2 && path[1] == "text":
	case len(path) == 3 && path[1] == "cells":
		cell, _ = path[2].(string)
	default:
		return nil
	}
	var offset crdt.Number
	for _, op := range event.GetDelta() {
		switch op.GetKind() {
		case crdt.EventOperatorInsertText:
			offset += crdt.Number(len(utf16.Encode([]rune(op.InsertText))))
		case crdt.EventOperatorInsertValue:
			offset++
		case crdt.EventOperatorRetain:
			offset += op.OpLength()
		}
	}
	return cursorPosition(id, cell, text, min(offset, text.GetLength()))
}

func cursorPosition(id, cell string, text *crdt.YText, offset crdt.Number) map[string]any {
	relative := crdt.NewRelativePositionFromTypeIndex(text, offset, 0)
	position := map[string]any{"assoc": relative.Assoc}
	for key, at := range map[string]*crdt.ID{"type": relative.Type, "item": relative.Item} {
		if at != nil {
			position[key] = map[string]any{"client": at.Client, "clock": at.Clock}
		}
	}
	if relative.Tname != "" {
		position["tname"] = relative.Tname
	}
	cursor := map[string]any{"block": id, "position": position}
	if cell != "" {
		cursor["cell"] = cell
	}
	return cursor
}

func (active *room) publishWriter(writer string, state map[string]any) {
	if state == nil {
		return
	}
	state["user"] = map[string]any{"name": writer, "color": "var(--dou-color-primary)"}
	encoded, err := json.Marshal(state)
	if err != nil || len(encoded) > 16<<10 {
		return
	}
	if active.writerID == 0 {
		// Browser client IDs are uint32. Yjs awareness also supports larger
		// JavaScript safe integers, leaving a separate range for the server.
		active.writerID = 1<<52 | uint64(rand.Uint32())
	}
	if active.writerTimer != nil {
		active.writerTimer.Stop()
		active.writerTimer = nil
	}
	active.sendWriter(string(encoded))
	if active.draft == nil {
		clock := active.writer.clock
		active.writerTimer = time.AfterFunc(writerPresenceLinger, func() {
			active.mu.Lock()
			defer active.mu.Unlock()
			if active.doc != nil && active.writer.clock == clock {
				active.clearWriter()
			}
		})
	}
}

func (active *room) sendWriter(value string) {
	active.writer = presence{clock: active.writer.clock + 1, json: value}
	active.broadcast(presenceMessage(map[uint64]presence{active.writerID: active.writer}), nil)
}

func (active *room) clearWriter() {
	if active.writerTimer != nil {
		active.writerTimer.Stop()
		active.writerTimer = nil
	}
	if active.writerID != 0 && active.writer.json != "null" {
		active.sendWriter("null")
	}
}
