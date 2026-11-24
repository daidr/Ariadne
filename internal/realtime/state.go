package realtime

import (
	"errors"
	"iter"
	"slices"
	"sync"
)

// State retains only current transient updates for reconnects. Durable messages
// and tool results remain in the event log. It does not own its Hub.
type State struct {
	mu      sync.Mutex
	hub     *Hub
	entries map[string]Update
	order   []string
	bytes   int
	closed  bool
}

func NewState(hub *Hub) *State {
	if hub == nil {
		panic("transient state needs a hub")
	}
	return &State{hub: hub, entries: map[string]Update{}}
}

func (state *State) Hub() *Hub { return state.hub }

func stateEntryWeight(entry Update) int {
	if entry.Kind == "" {
		return 0
	}
	payloadSize := entry.Size
	if payloadSize <= 0 {
		payloadSize = len(entry.Data)
	}
	return payloadSize + len(entry.Kind) + len(entry.ProjectID) + len(entry.ConversationID) + 64
}

func (state *State) Set(update Update, retain bool) error {
	if len(update.ProjectID) > 512 || len(update.ConversationID) > 512 {
		return errors.New("transient identity exceeds size limit")
	}
	if update.Value == nil && len(update.Data) > MaxPendingBytes-1024 {
		return errors.New("transient state exceeds frame limit")
	}
	normalized, queueSize, err := normalizeUpdate(update)
	if err != nil {
		return errors.New("invalid transient state")
	}
	if normalized.Size > MaxPendingBytes-1024 {
		return errors.New("transient state exceeds encoded frame limit")
	}
	key := updateKey(normalized)
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed {
		return errors.New("transient state is closed")
	}
	old, exists := state.entries[key]
	weight := state.bytes - stateEntryWeight(old)
	if retain {
		weight += stateEntryWeight(normalized)
		if !exists && len(state.entries) >= 4096 || weight > 32*1024*1024 {
			return errors.New("transient state capacity exceeded")
		}
		state.entries[key] = normalized
		if !exists {
			state.order = append(state.order, key)
		}
	} else {
		delete(state.entries, key)
		if exists {
			for i, entry := range state.order {
				if entry == key {
					copy(state.order[i:], state.order[i+1:])
					state.order[len(state.order)-1] = ""
					state.order = state.order[:len(state.order)-1]
					break
				}
			}
		}
	}
	state.bytes = weight
	state.hub.publishNormalized(normalized, queueSize)
	return nil
}

func (state *State) Snapshots(project string) iter.Seq[Update] {
	return func(yield func(Update) bool) {
		state.mu.Lock()
		keys := slices.Clone(state.order)
		state.mu.Unlock()
		for _, kind := range []string{"live", "background", "draft", "update"} {
			for _, key := range keys {
				state.mu.Lock()
				update, exists := state.entries[key]
				if exists && update.Kind == kind && (project == "" || (update.ProjectID != "" && update.ProjectID == project)) {
					if len(update.Data) > 0 {
						update.Data = slices.Clone(update.Data)
					}
				} else {
					exists = false
				}
				state.mu.Unlock()
				if exists && !yield(update) {
					return
				}
			}
		}
	}
}

func (state *State) DropProject(project string) {
	state.mu.Lock()
	defer state.mu.Unlock()
	order := state.order[:0]
	for _, key := range state.order {
		update := state.entries[key]
		if update.ProjectID == project {
			state.bytes -= stateEntryWeight(update)
			delete(state.entries, key)
		} else {
			order = append(order, key)
		}
	}
	clear(state.order[len(order):])
	state.order = order
}

func (state *State) Close() {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.closed = true
	clear(state.entries)
	state.order = nil
	state.bytes = 0
}
