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

type byteSizer interface {
	ByteSize() int
}

type Update struct {
	Kind           string
	ProjectID      string
	ConversationID string
	Data           json.RawMessage
	// Value holds an immutable typed snapshot payload (for example *LiveUpdate,
	// *DraftUpdate, *background.Snapshot, or update.View) so State, Hub, and
	// ConnectRPC consumers can pass updates without redundant JSON round-trips.
	Value any
	// Size optionally supplies the byte weight of Value for bounded queues.
	Size int

	normalized bool
}

// RawData returns the update's JSON bytes, encoding Value on demand when Data
// was omitted on the typed fast path.
func (update Update) RawData() (json.RawMessage, error) {
	if len(update.Data) > 0 {
		return slices.Clone(update.Data), nil
	}
	if update.Value != nil {
		return json.Marshal(update.Value)
	}
	return nil, nil
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
	mu        sync.RWMutex
	next      uint64
	all       map[uint64]*Subscription
	byProject map[string]map[uint64]*Subscription
	closed    bool
}

func NewHub() *Hub {
	return &Hub{
		all:       make(map[uint64]*Subscription),
		byProject: make(map[string]map[uint64]*Subscription),
	}
}

func (hub *Hub) clientCount() int {
	hub.mu.RLock()
	defer hub.mu.RUnlock()
	count := len(hub.all)
	for _, group := range hub.byProject {
		count += len(group)
	}
	return count
}

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
	if project == "" {
		hub.all[id] = sub
	} else {
		group := hub.byProject[project]
		if group == nil {
			group = make(map[uint64]*Subscription)
			hub.byProject[project] = group
		}
		group[id] = sub
	}
	var once sync.Once
	return sub, func() {
		once.Do(func() {
			hub.mu.Lock()
			if project == "" {
				delete(hub.all, id)
			} else if group := hub.byProject[project]; group != nil {
				delete(group, id)
				if len(group) == 0 {
					delete(hub.byProject, project)
				}
			}
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

func estimateValueSize(value any) int {
	switch v := value.(type) {
	case nil:
		return 0
	case byteSizer:
		if n := v.ByteSize(); n > 0 {
			return n
		}
		return 64
	case *DraftUpdate:
		if v == nil {
			return 32
		}
		size := len(v.ProjectID) + len(v.ConversationID) + 32
		if v.Draft != nil {
			size += len(v.Draft.MessageID) + len(v.Draft.Text) + len(v.Draft.Blocks) + 64
		}
		return size
	case DraftUpdate:
		size := len(v.ProjectID) + len(v.ConversationID) + 32
		if v.Draft != nil {
			size += len(v.Draft.MessageID) + len(v.Draft.Text) + len(v.Draft.Blocks) + 64
		}
		return size
	case *LiveUpdate:
		if v == nil {
			return 32
		}
		size := len(v.ProjectID) + len(v.ConversationID) + 32
		if v.Command != nil {
			size += len(v.Command.ProjectID) + len(v.Command.ConversationID) + len(v.Command.Command) + len(v.Command.CWD) + len(v.Command.Tail) + 64
		}
		return size
	case LiveUpdate:
		size := len(v.ProjectID) + len(v.ConversationID) + 32
		if v.Command != nil {
			size += len(v.Command.ProjectID) + len(v.Command.ConversationID) + len(v.Command.Command) + len(v.Command.CWD) + len(v.Command.Tail) + 64
		}
		return size
	default:
		return 256
	}
}

func normalizeUpdate(update Update) (Update, int, error) {
	if !isValidKind(update.Kind) {
		return Update{}, 0, errors.New("unknown transient update kind")
	}
	if update.Kind == "update" {
		if update.ProjectID != "" || update.ConversationID != "" {
			return Update{}, 0, errors.New("system transient update needs empty scope")
		}
	} else if update.ProjectID == "" || update.ConversationID == "" {
		return Update{}, 0, errors.New("transient update needs projectId and conversationId")
	}
	if update.Value != nil {
		payloadSize := update.Size
		if payloadSize <= 0 {
			payloadSize = estimateValueSize(update.Value)
		}
		normalized := Update{
			Kind:           strings.Clone(update.Kind),
			ProjectID:      strings.Clone(update.ProjectID),
			ConversationID: strings.Clone(update.ConversationID),
			Value:          update.Value,
			Size:           payloadSize,
			normalized:     true,
		}
		size := len(normalized.Kind) + len(normalized.ProjectID) + len(normalized.ConversationID) + payloadSize + 16
		return normalized, size, nil
	}
	if !json.Valid(update.Data) {
		if update.Kind == "update" {
			return Update{}, 0, errors.New("system transient update needs valid JSON and empty scope")
		}
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
		Size:           len(data),
		normalized:     true,
	}
	size := len(normalized.Kind) + len(normalized.ProjectID) + len(normalized.ConversationID) + len(normalized.Data) + 16
	return normalized, size, nil
}

func cloneUpdate(update Update) Update {
	out := Update{
		Kind:           update.Kind,
		ProjectID:      update.ProjectID,
		ConversationID: update.ConversationID,
		Value:          update.Value,
		Size:           update.Size,
		normalized:     update.normalized,
	}
	if len(update.Data) > 0 {
		out.Data = slices.Clone(update.Data)
	}
	return out
}

func (hub *Hub) Publish(update Update) error {
	if update.normalized {
		payloadSize := update.Size
		if payloadSize <= 0 {
			if update.Value != nil {
				payloadSize = estimateValueSize(update.Value)
			} else {
				payloadSize = len(update.Data)
			}
		}
		size := len(update.Kind) + len(update.ProjectID) + len(update.ConversationID) + payloadSize + 16
		hub.publishNormalized(update, size)
		return nil
	}
	normalized, size, err := normalizeUpdate(update)
	if err != nil {
		return err
	}
	hub.publishNormalized(normalized, size)
	return nil
}

func (hub *Hub) publishNormalized(normalized Update, size int) {
	key := updateKey(normalized)
	var stack [16]*Subscription
	targets := stack[:0]
	hub.mu.RLock()
	for _, client := range hub.all {
		targets = append(targets, client)
	}
	if normalized.ProjectID != "" {
		for _, client := range hub.byProject[normalized.ProjectID] {
			targets = append(targets, client)
		}
	}
	hub.mu.RUnlock()
	for _, client := range targets {
		client.put(key, normalized, size)
	}
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
	if update.normalized {
		return cloneUpdate(update)
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
	for id, client := range hub.all {
		client.finish(nil)
		delete(hub.all, id)
	}
	for project, group := range hub.byProject {
		for id, client := range group {
			client.finish(nil)
			delete(group, id)
		}
		delete(hub.byProject, project)
	}
}
