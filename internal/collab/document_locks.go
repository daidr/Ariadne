package collab

import (
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"time"

	"github.com/antst/go-yjs/crdt"
)

// The IDs fit in the bounded awareness frame even when a change spans many blocks.
const maxDocumentLocks = 256

var errDocumentBlockLocked = errors.New("document block is being edited by AI")

// Record affected blocks, including whole blocks that disappear during a replacement.
func changedDocumentBlocks(doc *crdt.Doc, change func() error) (map[string]bool, error) {
	changed := map[string]bool{}
	root := doc.GetMap("document")
	listener := func(payload, _ any) {
		events, _ := payload.([]crdt.IEventType)
		for _, event := range events {
			path := event.Path()
			if len(path) > 0 {
				if id, ok := path[0].(string); ok {
					changed[id] = true
				}
			} else if event, ok := event.(*crdt.YMapEvent); ok {
				for id := range event.GetKeys() {
					changed[id] = true
				}
			}
		}
	}
	root.ObserveDeep(listener)
	defer root.UnobserveDeep(listener)
	return changed, change()
}

func (draft *Draft) lockDocumentBlocks(changed map[string]bool) error {
	size := len(draft.locked)
	for id := range changed {
		if !draft.locked[id] {
			size++
		}
	}
	if size > maxDocumentLocks {
		return errors.New("document preview affects too many blocks")
	}
	if size == len(draft.locked) {
		return nil
	}
	locked := make(map[string]bool, size)
	maps.Copy(locked, draft.locked)
	maps.Copy(locked, changed)
	encoded, err := json.Marshal(slices.Collect(maps.Keys(locked)))
	if err != nil || len(encoded) > 12<<10 {
		return errors.New("document preview block IDs are too large")
	}
	draft.locked = locked
	draft.publishDocumentWriter()
	return nil
}

func (draft *Draft) publishDocumentWriter() {
	state := maps.Clone(draft.cursor)
	if len(draft.locked) != 0 {
		if state == nil {
			state = map[string]any{}
		}
		state["documentLocks"] = slices.Sorted(maps.Keys(draft.locked))
	}
	draft.room.publishWriter(draft.writer, state)
	if len(draft.locked) != 0 && draft.lockPulse == nil {
		draft.lockPulse = time.AfterFunc(10*time.Second, func() {
			active := draft.room
			active.mu.Lock()
			defer active.mu.Unlock()
			if active.draft == draft {
				draft.lockPulse = nil
				draft.publishDocumentWriter()
			}
		})
	}
}

func documentLocksChanged(before, after *crdt.Doc, ids, changed map[string]bool) bool {
	old, next := before.GetMap("document"), after.GetMap("document")
	ancestors := map[string]bool{}
	for id := range ids {
		if changed[id] {
			return true
		}
		left, _ := old.Get(id).(*crdt.YMap)
		if left == nil {
			continue
		}
		// Moving or deleting a parent also moves or deletes its locked descendants.
		for parent := stringField(left, "parent"); parent != "" && !ancestors[parent]; {
			ancestors[parent] = true
			left, _ := old.Get(parent).(*crdt.YMap)
			right, _ := next.Get(parent).(*crdt.YMap)
			if left == nil {
				break
			}
			if changed[parent] && (right == nil || !equalJSON(left.Get("parent"), right.Get("parent")) ||
				!equalJSON(left.Get("order"), right.Get("order"))) {
				return true
			}
			parent = stringField(left, "parent")
		}
	}
	return false
}
