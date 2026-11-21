package realtime

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"net/http"
	"time"

	"github.com/daidr/Ariadne/internal/eventstore"
)

type Log interface {
	Read(context.Context, eventstore.ReadOptions) ([]eventstore.Event, error)
	Watch() (<-chan struct{}, func())
}

type StreamOptions struct {
	AfterSeq     int64
	ProjectID    string
	Initial      iter.Seq[Update]
	Heartbeat    time.Duration
	WriteTimeout time.Duration
}

// replayBatchSize caps durable events fetched per SQLite query during SSE replay.
const replayBatchSize = 64

// Serve subscribes before replay, then streams the durable log in batches.
// Context cancellation, a write deadline or queue overflow releases both subscriptions.
func Serve(writer http.ResponseWriter, request *http.Request, log Log, hub *Hub, options StreamOptions) error {
	changed, stopLog := log.Watch()
	defer stopLog()
	sub, stopUpdates := hub.Subscribe(options.ProjectID)
	defer stopUpdates()
	controller := http.NewResponseController(writer)
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Accel-Buffering", "no")
	heartbeat := options.Heartbeat
	if heartbeat <= 0 {
		heartbeat = 15 * time.Second
	}
	writeTimeout := options.WriteTimeout
	if writeTimeout <= 0 {
		writeTimeout = 10 * time.Second
	}
	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()
	write := func(frame []byte) error {
		// A slow socket cannot retain a subscriber forever during shutdown.
		if err := controller.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil && err != http.ErrNotSupported {
			return err
		}
		if _, err := writer.Write(frame); err != nil {
			return err
		}
		return controller.Flush()
	}
	if err := write(nil); err != nil {
		return err
	}
	lastSeq := options.AfterSeq
	dirty := true
	var next func() (Update, bool)
	if options.Initial != nil {
		var stop func()
		next, stop = iter.Pull(options.Initial)
		defer stop()
	}
	for {
		if err := request.Context().Err(); err != nil {
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
			events, err := log.Read(request.Context(), eventstore.ReadOptions{AfterSeq: lastSeq, ProjectID: options.ProjectID, Limit: replayBatchSize})
			if err != nil {
				return err
			}
			if len(events) > 0 {
				for _, event := range events {
					if err := request.Context().Err(); err != nil {
						return err
					}
					sub.mu.Lock()
					closed, failure := sub.closed, sub.err
					sub.mu.Unlock()
					if closed {
						return failure
					}
					data, err := json.Marshal(event)
					if err != nil {
						return err
					}
					if err := write([]byte(fmt.Sprintf("id: %d\nevent: ariadne\ndata: %s\n\n", event.Seq, data))); err != nil {
						return err
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
				if options.ProjectID == "" || options.ProjectID == update.ProjectID {
					if err := write(sub.takeInitial(update)); err != nil {
						return err
					}
				}
				continue
			}
			next = nil
		}
		frame, closed, err := sub.next()
		if closed {
			return err
		}
		if frame != nil {
			if err := write(frame); err != nil {
				return err
			}
			continue
		}
		select {
		case <-request.Context().Done():
			return request.Context().Err()
		case _, open := <-changed:
			if !open {
				return nil
			}
			dirty = true
		case <-sub.wake:
		case <-ticker.C:
			if err := write([]byte(": ping\n\n")); err != nil {
				return err
			}
		}
	}
}
