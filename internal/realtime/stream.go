package realtime

import (
	"context"
	"iter"
	"time"

	"github.com/daidr/Ariadne/internal/eventstore"
)

type Log interface {
	Read(context.Context, eventstore.ReadOptions) ([]eventstore.Event, error)
	Watch() (<-chan struct{}, func())
}

type projectWatcher interface {
	WatchProject(projectID string) (<-chan struct{}, func())
}

type StreamOptions struct {
	AfterSeq  int64
	ProjectID string
	Initial   iter.Seq[Update]
	Heartbeat time.Duration
}

type StreamSink struct {
	OnReady     func(lastSeq int64) error
	OnEvent     func(eventstore.Event) error
	OnUpdate    func(Update) error
	OnHeartbeat func() error
}

// replayBatchSize caps durable events fetched per SQLite query during replay.
const replayBatchSize = 64

// Stream subscribes before replay, then streams durable events in batches and
// coalesced transient updates to sink. Context cancellation, a sink error, or
// queue overflow releases both subscriptions.
func Stream(ctx context.Context, log Log, hub *Hub, options StreamOptions, sink StreamSink) error {
	var changed <-chan struct{}
	var stopLog func()
	if watcher, ok := log.(projectWatcher); ok {
		changed, stopLog = watcher.WatchProject(options.ProjectID)
	} else {
		changed, stopLog = log.Watch()
	}
	defer stopLog()
	sub, stopUpdates := hub.Subscribe(options.ProjectID)
	defer stopUpdates()

	heartbeat := options.Heartbeat
	if heartbeat <= 0 {
		heartbeat = 15 * time.Second
	}
	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()

	lastSeq := options.AfterSeq
	if sink.OnReady != nil {
		if err := sink.OnReady(lastSeq); err != nil {
			return err
		}
	}

	dirty := true
	var next func() (Update, bool)
	if options.Initial != nil {
		var stop func()
		next, stop = iter.Pull(options.Initial)
		defer stop()
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Check closure before replay too: an overflowing queue ends the stream
		// even when the client still has a long durable backlog to read.
		sub.mu.Lock()
		closed, failure := sub.closed, sub.err
		sub.mu.Unlock()
		if closed {
			return failure
		}
		if dirty {
			events, err := log.Read(ctx, eventstore.ReadOptions{
				AfterSeq:  lastSeq,
				ProjectID: options.ProjectID,
				Limit:     replayBatchSize,
			})
			if err != nil {
				return err
			}
			if len(events) > 0 {
				for _, event := range events {
					if err := ctx.Err(); err != nil {
						return err
					}
					sub.mu.Lock()
					closed, failure := sub.closed, sub.err
					sub.mu.Unlock()
					if closed {
						return failure
					}
					if sink.OnEvent != nil {
						if err := sink.OnEvent(event); err != nil {
							return err
						}
					}
					lastSeq = event.Seq
				}
				if len(events) == replayBatchSize {
					continue
				}
			}
			dirty = false
		}
		if next != nil {
			update, exists := next()
			if exists {
				if options.ProjectID == "" || (update.ProjectID != "" && options.ProjectID == update.ProjectID) {
					if sink.OnUpdate != nil {
						if err := sink.OnUpdate(sub.takeInitial(update)); err != nil {
							return err
						}
					}
				}
				continue
			}
			next = nil
		}
		update, hasUpdate, closed, err := sub.next()
		if closed {
			return err
		}
		if hasUpdate {
			if sink.OnUpdate != nil {
				if err := sink.OnUpdate(update); err != nil {
					return err
				}
			}
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case _, open := <-changed:
			if !open {
				return nil
			}
			dirty = true
		case <-sub.wake:
		case <-ticker.C:
			if sink.OnHeartbeat != nil {
				if err := sink.OnHeartbeat(); err != nil {
					return err
				}
			}
		}
	}
}
