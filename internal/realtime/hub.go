// Package realtime delivers durable events by cursor and bounds transient SSE
// updates by both count and bytes, including when a client stops reading.
package realtime

import (
	"container/list"
	"encoding/json"
	"errors"
	"sync"
)

const MaxPendingBytes = 512 * 1024
const MaxPendingUpdates = 256

var ErrSlowConsumer = errors.New("event stream consumer is too slow; reconnect with Last-Event-ID")

type Update struct {
	Kind           string
	ProjectID      string
	ConversationID string
	Data           json.RawMessage
}

type queued struct {
	key   string
	frame []byte
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

func updateFrame(update Update) []byte {
	frame := make([]byte, 0, len(update.Kind)+len(update.Data)+16)
	frame = append(frame, "event: "...)
	frame = append(frame, update.Kind...)
	frame = append(frame, "\ndata: "...)
	// Compact before framing: RawMessage may contain newlines, which must not
	// become extra SSE fields. Marshal also isolates the caller's mutable bytes.
	data, _ := json.Marshal(update.Data)
	frame = append(frame, data...)
	return append(frame, '\n', '\n')
}

func (hub *Hub) Publish(update Update) error {
	if update.Kind != "live" && update.Kind != "background" && update.Kind != "draft" {
		return errors.New("unknown transient update kind")
	}
	if update.ProjectID == "" || update.ConversationID == "" || !json.Valid(update.Data) {
		return errors.New("transient update needs projectId, conversationId and valid JSON")
	}
	frame := updateFrame(update)
	key := update.Kind + ":" + update.ConversationID
	hub.mu.Lock()
	clients := make([]*Subscription, 0, len(hub.clients))
	for _, client := range hub.clients {
		clients = append(clients, client)
	}
	hub.mu.Unlock()
	for _, client := range clients {
		if client.project == "" || client.project == update.ProjectID {
			client.put(key, frame)
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

func (sub *Subscription) put(key string, frame []byte) {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	if sub.closed {
		return
	}
	if old := sub.byKey[key]; old != nil {
		sub.bytes -= len(old.Value.(queued).frame)
		sub.queue.Remove(old)
		delete(sub.byKey, key)
	}
	if sub.queue.Len() >= MaxPendingUpdates || sub.bytes+len(frame) > MaxPendingBytes {
		sub.finishLocked(ErrSlowConsumer)
		return
	}
	sub.byKey[key] = sub.queue.PushBack(queued{key: key, frame: frame})
	sub.bytes += len(frame)
	sub.signal()
}

// takeInitial returns a newer queued snapshot when one arrived while the initial
// state was being enumerated, avoiding a stale snapshot after a fresh update.
func (sub *Subscription) takeInitial(update Update) []byte {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	key := update.Kind + ":" + update.ConversationID
	if item := sub.byKey[key]; item != nil {
		value := item.Value.(queued)
		sub.queue.Remove(item)
		delete(sub.byKey, key)
		sub.bytes -= len(value.frame)
		return value.frame
	}
	return updateFrame(update)
}

func (sub *Subscription) next() ([]byte, bool, error) {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	if sub.closed {
		return nil, true, sub.err
	}
	if item := sub.queue.Front(); item != nil {
		value := item.Value.(queued)
		sub.queue.Remove(item)
		delete(sub.byKey, value.key)
		sub.bytes -= len(value.frame)
		return value.frame, false, nil
	}
	return nil, false, nil
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
