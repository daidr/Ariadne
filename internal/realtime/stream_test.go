package realtime

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	done := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		done <- Serve(w, r, log, hub, StreamOptions{AfterSeq: 1, ProjectID: "p1", Heartbeat: time.Second})
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, "GET", server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	readFrame := func() string {
		t.Helper()
		var frame strings.Builder
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			frame.WriteString(line)
			if line == "\n" {
				return frame.String()
			}
		}
	}
	if frame := readFrame(); !strings.HasPrefix(frame, "id: 3\nevent: ariadne\n") {
		t.Fatalf("replay: %q", frame)
	}
	appendEvent("p1")
	if frame := readFrame(); !strings.HasPrefix(frame, "id: 4\nevent: ariadne\n") {
		t.Fatalf("new event: %q", frame)
	}
	if err := hub.Publish(update("c1", "latest")); err != nil {
		t.Fatal(err)
	}
	if frame := readFrame(); !strings.HasPrefix(frame, "event: draft\n") || !strings.Contains(frame, "latest") {
		t.Fatalf("transient: %q", frame)
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
	done := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		done <- Serve(w, r, log, hub, StreamOptions{AfterSeq: 0, ProjectID: "p1", Heartbeat: time.Second})
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, "GET", server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	for seq := 1; seq <= total; seq++ {
		var frame strings.Builder
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			frame.WriteString(line)
			if line == "\n" {
				break
			}
		}
		wantPrefix := fmt.Sprintf("id: %d\nevent: ariadne\n", seq)
		if !strings.HasPrefix(frame.String(), wantPrefix) {
			t.Fatalf("event %d: got %q, want prefix %q", seq, frame.String(), wantPrefix)
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
