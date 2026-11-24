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

func (log *watchedLog) WatchProject(projectID string) (<-chan struct{}, func()) {
	log.watchers.Add(1)
	wake, stop := log.Store.WatchProject(projectID)
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
	if hub.clientCount() != 0 {
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

func TestStreamProjectWakeupIsolation(t *testing.T) {
	store, err := eventstore.Open(t.Context(), eventstore.Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	hub := NewHub()
	defer hub.Close()

	logP1 := &watchedLog{Store: store}
	logP2 := &watchedLog{Store: store}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	p1Ready := make(chan struct{}, 1)
	p2Ready := make(chan struct{}, 1)
	p1Events := make(chan eventstore.Event, 32)
	p2Events := make(chan eventstore.Event, 8)
	done := make(chan error, 2)

	go func() {
		done <- Stream(ctx, logP1, hub, StreamOptions{AfterSeq: 0, ProjectID: "p1", Heartbeat: time.Hour}, StreamSink{
			OnReady: func(int64) error { p1Ready <- struct{}{}; return nil },
			OnEvent: func(ev eventstore.Event) error { p1Events <- ev; return nil },
		})
	}()
	go func() {
		done <- Stream(ctx, logP2, hub, StreamOptions{AfterSeq: 0, ProjectID: "p2", Heartbeat: time.Hour}, StreamSink{
			OnReady: func(int64) error { p2Ready <- struct{}{}; return nil },
			OnEvent: func(ev eventstore.Event) error { p2Events <- ev; return nil },
		})
	}()

	<-p1Ready
	<-p2Ready
	// Wait briefly for initial empty replay on p2 to settle.
	deadline := time.Now().Add(time.Second)
	for logP2.reads.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	p2ReadsBefore := logP2.reads.Load()

	// Append 20 events for p1; p2 must not wake up or issue any extra Read calls.
	for range 20 {
		if _, err := store.Append(t.Context(), []eventstore.NewEvent{{Type: "test", ProjectID: "p1", Data: json.RawMessage(`{}`)}}); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 20 {
		select {
		case ev := <-p1Events:
			if ev.ProjectID != "p1" {
				t.Fatalf("unexpected event on p1 stream: %+v", ev)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for p1 event %d", i)
		}
	}
	if p2ReadsAfter := logP2.reads.Load(); p2ReadsAfter != p2ReadsBefore {
		t.Fatalf("p2 stream woke up and read %d extra times during p1 traffic", p2ReadsAfter-p2ReadsBefore)
	}

	// Now append one event for p2 and verify p2 receives it.
	if _, err := store.Append(t.Context(), []eventstore.NewEvent{{Type: "test", ProjectID: "p2", Data: json.RawMessage(`{}`)}}); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-p2Events:
		if ev.ProjectID != "p2" {
			t.Fatalf("unexpected event on p2 stream: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for p2 event")
	}

	cancel()
	<-done
	<-done
}
