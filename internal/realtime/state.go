package realtime

import (
	"encoding/json"
	"errors"
	"iter"
	"slices"
	"strings"
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
func (state *State) Set(update Update, retain bool) error {
	if update.Kind != "live" && update.Kind != "background" && update.Kind != "draft" || update.ProjectID == "" || update.ConversationID == "" || !json.Valid(update.Data) {
		return errors.New("invalid transient state")
	}
	if len(update.Data) > MaxPendingBytes-1024 {
		return errors.New("transient state exceeds frame limit")
	}
	encoded, err := json.Marshal(update.Data)
	if err != nil {
		return err
	}
	if len(encoded) > MaxPendingBytes-1024 {
		return errors.New("transient state exceeds encoded frame limit")
	}
	update.Data = encoded
	if len(update.ProjectID) > 512 || len(update.ConversationID) > 512 {
		return errors.New("transient identity exceeds size limit")
	}
	key := update.Kind + ":" + update.ConversationID
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed {
		return errors.New("transient state is closed")
	}
	old, exists := state.entries[key]
	entryWeight := func(entry Update) int {
		if entry.Kind == "" {
			return 0
		}
		return len(entry.Data) + len(entry.Kind) + len(entry.ProjectID) + len(entry.ConversationID) + 64
	}
	weight := state.bytes - entryWeight(old)
	if retain {
		weight += entryWeight(update)
		if !exists && len(state.entries) >= 4096 || weight > 32*1024*1024 {
			return errors.New("transient state capacity exceeded")
		}
		update.Data = slices.Clone(update.Data)
		update.ProjectID = strings.Clone(update.ProjectID)
		update.ConversationID = strings.Clone(update.ConversationID)
		state.entries[key] = update
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
	return state.hub.Publish(update)
}
func (state *State) Snapshots(project string) iter.Seq[Update] {
	return func(yield func(Update) bool) {
		state.mu.Lock()
		keys := slices.Clone(state.order)
		state.mu.Unlock()
		for _, kind := range []string{"live", "background", "draft"} {
			for _, key := range keys {
				state.mu.Lock()
				update, exists := state.entries[key]
				if exists && update.Kind == kind && (project == "" || update.ProjectID == project) {
					update.Data = slices.Clone(update.Data)
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
			state.bytes -= len(update.Data) + len(update.Kind) + len(update.ProjectID) + len(update.ConversationID) + 64
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
