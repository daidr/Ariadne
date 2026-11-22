package realtime

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/eventstore"
)

type watchedLog struct {
	*eventstore.Store
	watchers atomic.Int64
	reads    atomic.Int64
}

func (log *watchedLog) Read(ctx context.Context, options eventstore.ReadOptions) ([]eventstore.Event, error) {
	log.reads.Add(1)
	return log.Store.Read(ctx, options)
}

func (log *watchedLog) Watch() (<-chan struct{}, func()) {
	log.watchers.Add(1)
	wake, stop := log.Store.Watch()
	return wake, func() { stop(); log.watchers.Add(-1) }
}

func TestStreamReplayNewEventsAndDisconnectReleaseSubscriptions(t *testing.T) {
	store, err := eventstore.Open(t.Context(), eventstore.Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	log := &watchedLog{Store: store}
	hub := NewHub()
	defer hub.Close()
	appendEvent := func(project string) {
		t.Helper()
		if _, err := store.Append(t.Context(), []eventstore.NewEvent{{Type: "test", ProjectID: project, Data: json.RawMessage(`{}`)}}); err != nil {
			t.Fatal(err)
		}
	}
	appendEvent("p1")
	appendEvent("p2")
	appendEvent("p1")

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	readyCh := make(chan int64, 1)
	eventCh := make(chan eventstore.Event, 8)
	updateCh := make(chan Update, 8)
	done := make(chan error, 1)

	go func() {
		done <- Stream(ctx, log, hub, StreamOptions{AfterSeq: 1, ProjectID: "p1", Heartbeat: time.Second}, StreamSink{
			OnReady:  func(lastSeq int64) error { readyCh <- lastSeq; return nil },
			OnEvent:  func(ev eventstore.Event) error { eventCh <- ev; return nil },
			OnUpdate: func(u Update) error { updateCh <- u; return nil },
		})
	}()

	select {
	case seq := <-readyCh:
		if seq != 1 {
			t.Fatalf("ready seq: got %d, want 1", seq)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for ready")
	}

	select {
	case ev := <-eventCh:
		if ev.Seq != 3 || ev.ProjectID != "p1" {
			t.Fatalf("replay: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for replay event")
	}

	appendEvent("p1")
	select {
	case ev := <-eventCh:
		if ev.Seq != 4 || ev.ProjectID != "p1" {
			t.Fatalf("new event: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for new event")
	}

	if err := hub.Publish(update("c1", "latest")); err != nil {
		t.Fatal(err)
	}
	select {
	case u := <-updateCh:
		if u.Kind != "draft" || !strings.Contains(string(u.Data), "latest") {
			t.Fatalf("transient: %+v", u)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for transient update")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("disconnect retained stream")
	}
	if log.watchers.Load() != 0 {
		t.Fatal("disconnect retained durable watcher")
	}
	hub.mu.Lock()
	count := len(hub.clients)
	hub.mu.Unlock()
	if count != 0 {
		t.Fatal("disconnect retained transient watcher")
	}
}

func TestStreamReplayReadsEventsInBatches(t *testing.T) {
	store, err := eventstore.Open(t.Context(), eventstore.Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const total = replayBatchSize*2 + 5
	batch := make([]eventstore.NewEvent, total)
	for i := range batch {
		batch[i] = eventstore.NewEvent{Type: "test", ProjectID: "p1", Data: json.RawMessage(`{}`)}
	}
	if _, err := store.Append(t.Context(), batch); err != nil {
		t.Fatal(err)
	}
	log := &watchedLog{Store: store}
	hub := NewHub()
	defer hub.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	eventCh := make(chan eventstore.Event, total)
	done := make(chan error, 1)
	go func() {
		done <- Stream(ctx, log, hub, StreamOptions{AfterSeq: 0, ProjectID: "p1", Heartbeat: time.Second}, StreamSink{
			OnEvent: func(ev eventstore.Event) error { eventCh <- ev; return nil },
		})
	}()

	for seq := int64(1); seq <= total; seq++ {
		select {
		case ev := <-eventCh:
			if ev.Seq != seq {
				t.Fatalf("event %d: got seq %d", seq, ev.Seq)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for event %d", seq)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("disconnect retained stream")
	}
	if reads := log.reads.Load(); reads != 3 {
		t.Fatalf("expected 3 batched reads for %d events (batch=%d), got %d", total, replayBatchSize, reads)
	}
}
