// Package realtime delivers durable events by cursor and bounds transient
// updates by both count and bytes, including when a client stops reading.
package realtime

import (
	"container/list"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
)

const MaxPendingBytes = 512 * 1024
const MaxPendingUpdates = 256

var ErrSlowConsumer = errors.New("event stream consumer is too slow; reconnect with afterSeq")

type Update struct {
	Kind           string
	ProjectID      string
	ConversationID string
	Data           json.RawMessage
}

type queued struct {
	key    string
	update Update
	size   int
}

type Subscription struct {
	mu      sync.Mutex
	project string
	queue   *list.List
	byKey   map[string]*list.Element
	bytes   int
	wake    chan struct{}
	closed  bool
	err     error
}

type Hub struct {
	mu      sync.Mutex
	next    uint64
	clients map[uint64]*Subscription
	closed  bool
}

func NewHub() *Hub { return &Hub{clients: make(map[uint64]*Subscription)} }

func (hub *Hub) Subscribe(project string) (*Subscription, func()) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	sub := &Subscription{project: project, queue: list.New(), byKey: make(map[string]*list.Element), wake: make(chan struct{}, 1)}
	if hub.closed {
		sub.finish(nil)
		return sub, func() {}
	}
	hub.next++
	id := hub.next
	hub.clients[id] = sub
	var once sync.Once
	return sub, func() {
		once.Do(func() {
			hub.mu.Lock()
			delete(hub.clients, id)
			hub.mu.Unlock()
			sub.finish(nil)
		})
	}
}

func isValidKind(kind string) bool {
	return kind == "live" || kind == "background" || kind == "draft" || kind == "update"
}

func updateKey(update Update) string {
	if update.Kind == "update" {
		return "update"
	}
	return update.Kind + ":" + update.ConversationID
}

func normalizeUpdate(update Update) (Update, int, error) {
	if !isValidKind(update.Kind) {
		return Update{}, 0, errors.New("unknown transient update kind")
	}
	if update.Kind == "update" {
		if update.ProjectID != "" || update.ConversationID != "" || !json.Valid(update.Data) {
			return Update{}, 0, errors.New("system transient update needs valid JSON and empty scope")
		}
	} else if update.ProjectID == "" || update.ConversationID == "" || !json.Valid(update.Data) {
		return Update{}, 0, errors.New("transient update needs projectId, conversationId and valid JSON")
	}
	// Compact & clone caller bytes so mutations cannot leak into queued snapshots.
	data, err := json.Marshal(update.Data)
	if err != nil {
		return Update{}, 0, err
	}
	normalized := Update{
		Kind:           strings.Clone(update.Kind),
		ProjectID:      strings.Clone(update.ProjectID),
		ConversationID: strings.Clone(update.ConversationID),
		Data:           data,
	}
	size := len(normalized.Kind) + len(normalized.ProjectID) + len(normalized.ConversationID) + len(normalized.Data) + 16
	return normalized, size, nil
}

func cloneUpdate(update Update) Update {
	return Update{
		Kind:           update.Kind,
		ProjectID:      update.ProjectID,
		ConversationID: update.ConversationID,
		Data:           slices.Clone(update.Data),
	}
}

func (hub *Hub) Publish(update Update) error {
	normalized, size, err := normalizeUpdate(update)
	if err != nil {
		return err
	}
	key := updateKey(normalized)
	hub.mu.Lock()
	clients := make([]*Subscription, 0, len(hub.clients))
	for _, client := range hub.clients {
		clients = append(clients, client)
	}
	hub.mu.Unlock()
	for _, client := range clients {
		if client.project == "" || (normalized.ProjectID != "" && client.project == normalized.ProjectID) {
			client.put(key, normalized, size)
		}
	}
	return nil
}

func (sub *Subscription) signal() {
	select {
	case sub.wake <- struct{}{}:
	default:
	}
}

func (sub *Subscription) put(key string, update Update, size int) {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	if sub.closed {
		return
	}
	if old := sub.byKey[key]; old != nil {
		sub.bytes -= old.Value.(queued).size
		sub.queue.Remove(old)
		delete(sub.byKey, key)
	}
	if sub.queue.Len() >= MaxPendingUpdates || sub.bytes+size > MaxPendingBytes {
		sub.finishLocked(ErrSlowConsumer)
		return
	}
	sub.byKey[key] = sub.queue.PushBack(queued{key: key, update: update, size: size})
	sub.bytes += size
	sub.signal()
}

// takeInitial returns a newer queued snapshot when one arrived while the initial
// state was being enumerated, avoiding a stale snapshot after a fresh update.
func (sub *Subscription) takeInitial(update Update) Update {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	key := updateKey(update)
	if item := sub.byKey[key]; item != nil {
		value := item.Value.(queued)
		sub.queue.Remove(item)
		delete(sub.byKey, key)
		sub.bytes -= value.size
		return cloneUpdate(value.update)
	}
	normalized, _, err := normalizeUpdate(update)
	if err != nil {
		return cloneUpdate(update)
	}
	return normalized
}

func (sub *Subscription) next() (Update, bool, bool, error) {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	if sub.closed {
		return Update{}, false, true, sub.err
	}
	if item := sub.queue.Front(); item != nil {
		value := item.Value.(queued)
		sub.queue.Remove(item)
		delete(sub.byKey, value.key)
		sub.bytes -= value.size
		return cloneUpdate(value.update), true, false, nil
	}
	return Update{}, false, false, nil
}

func (sub *Subscription) finish(err error) {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	sub.finishLocked(err)
}

func (sub *Subscription) finishLocked(err error) {
	if sub.closed {
		return
	}
	sub.closed = true
	sub.err = err
	sub.queue.Init()
	clear(sub.byKey)
	sub.bytes = 0
	sub.signal()
}

func (hub *Hub) Close() {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	hub.closed = true
	for id, client := range hub.clients {
		client.finish(nil)
		delete(hub.clients, id)
	}
}
