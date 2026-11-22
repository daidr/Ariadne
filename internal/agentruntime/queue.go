package agentruntime

import (
	"context"
	"errors"
	"sync"
	"time"
)

var errQueueLimit = errors.New("agent event queue exceeded its limit")
var errQueueClosed = errors.New("agent event queue is closed")

// Drivers cannot block their protocol reader behind a slow stream consumer.
// Overflow ends the session rather than losing text or retaining unbounded data.
type eventQueue struct {
	mu      sync.Mutex
	items   []Event
	bytes   int
	err     error
	wake    chan struct{}
	limit   int
	maxSize int
}

func newEventQueue() *eventQueue {
	return &eventQueue{wake: make(chan struct{}, 1), limit: 512, maxSize: 16 << 20}
}
func eventSize(event Event) int {
	return len(event.Text) + len(event.ID) + len(event.Name) + len(event.Arguments) + len(event.Detail) + 128
}
func (q *eventQueue) push(event Event) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.err != nil {
		return false
	}
	if event.Type == "alive" && len(q.items) > 0 && q.items[len(q.items)-1].Type == "alive" {
		return true
	}
	size := eventSize(event)
	if len(q.items) >= q.limit || size > q.maxSize-q.bytes {
		q.items, q.bytes, q.err = nil, 0, errQueueLimit
	} else {
		q.items = append(q.items, event)
		q.bytes += size
	}
	select {
	case q.wake <- struct{}{}:
	default:
	}
	return q.err == nil
}
func (q *eventQueue) close() {
	q.mu.Lock()
	q.items, q.bytes, q.err = nil, 0, errQueueClosed
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}
func (q *eventQueue) next(ctx context.Context, wait time.Duration) (Event, bool, error) {
	var timeout <-chan time.Time
	if wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		timeout = timer.C
	}
	for {
		if err := ctx.Err(); err != nil {
			return Event{}, false, err
		}
		q.mu.Lock()
		if q.err != nil {
			err := q.err
			q.mu.Unlock()
			return Event{}, false, err
		}
		if len(q.items) > 0 {
			event := q.items[0]
			q.items[0] = Event{}
			q.items = q.items[1:]
			q.bytes -= eventSize(event)
			if len(q.items) == 0 {
				q.items = nil
			}
			q.mu.Unlock()
			return event, false, nil
		}
		q.mu.Unlock()
		select {
		case <-ctx.Done():
			return Event{}, false, ctx.Err()
		case <-timeout:
			return Event{}, true, nil
		case <-q.wake:
		}
	}
}
