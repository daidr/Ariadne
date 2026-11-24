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

// collectChangedTypes records every SharedType touched by transactions during
// change via afterTransaction, avoiding ObserveDeep dropping sibling child
// events in the same transaction.
func collectChangedTypes(doc *crdt.Doc, change func() error) (map[crdt.SharedType]crdt.ChangedSubs, error) {
	changed := map[crdt.SharedType]crdt.ChangedSubs{}
	handler := crdt.NewObserverHandler(func(args ...any) {
		if len(args) == 0 {
			return
		}
		trans, ok := args[0].(*crdt.Transaction)
		if !ok {
			return
		}
		for shared, subs := range trans.ChangedTypes() {
			existing := changed[shared]
			if existing == nil {
				changed[shared] = subs
				continue
			}
			maps.Copy(existing, subs)
		}
	})
	doc.On("afterTransaction", handler)
	defer doc.Off("afterTransaction", handler)
	return changed, change()
}

func sharedTypeChanged(value any, changed map[crdt.SharedType]crdt.ChangedSubs) bool {
	switch node := value.(type) {
	case *crdt.YMap:
		if _, ok := changed[node]; ok {
			return true
		}
		hit := false
		node.Range(func(_ string, child any) {
			if !hit && sharedTypeChanged(child, changed) {
				hit = true
			}
		})
		return hit
	case *crdt.YArray:
		if _, ok := changed[node]; ok {
			return true
		}
		for _, child := range node.ToArray() {
			if sharedTypeChanged(child, changed) {
				return true
			}
		}
		return false
	case crdt.SharedType:
		_, ok := changed[node]
		return ok
	default:
		return false
	}
}

func blockEntryChanged(root *crdt.YMap, id string, changed map[crdt.SharedType]crdt.ChangedSubs) bool {
	if subs := changed[root]; subs != nil {
		if _, ok := subs[id]; ok {
			return true
		}
	}
	return sharedTypeChanged(root.Get(id), changed)
}

// Record affected blocks, including whole blocks that disappear during a replacement.
func changedDocumentBlocks(doc *crdt.Doc, change func() error) (map[string]bool, error) {
	changedTypes, err := collectChangedTypes(doc, change)
	changed := map[string]bool{}
	if len(changedTypes) == 0 {
		return changed, err
	}
	root := doc.GetMap("document")
	for id := range changedTypes[root] {
		changed[id] = true
	}
	if len(changedTypes) == 1 && changedTypes[root] != nil {
		return changed, err
	}
	root.Range(func(id string, value any) {
		if !changed[id] && sharedTypeChanged(value, changedTypes) {
			changed[id] = true
		}
	})
	return changed, err
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

func documentLocksChanged(before, after *crdt.Doc, ids map[string]bool, changed map[crdt.SharedType]crdt.ChangedSubs) bool {
	if len(changed) == 0 {
		return false
	}
	old, next := before.GetMap("document"), after.GetMap("document")
	ancestors := map[string]bool{}
	for id := range ids {
		if blockEntryChanged(next, id, changed) {
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
			if blockEntryChanged(next, parent, changed) && (right == nil || !equalJSON(left.Get("parent"), right.Get("parent")) ||
				!equalJSON(left.Get("order"), right.Get("order"))) {
				return true
			}
			parent = stringField(left, "parent")
		}
	}
	return false
}
