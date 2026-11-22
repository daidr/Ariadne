package realtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func update(id, text string) Update {
	data, _ := json.Marshal(map[string]string{"projectId": "p1", "conversationId": id, "text": text})
	return Update{Kind: "draft", ProjectID: "p1", ConversationID: id, Data: data}
}

func TestTransientSnapshotsCoalesceAndFilterProjects(t *testing.T) {
	hub := NewHub()
	defer hub.Close()
	all, stopAll := hub.Subscribe("")
	defer stopAll()
	other, stopOther := hub.Subscribe("p2")
	defer stopOther()
	for n := range 1000 {
		if err := hub.Publish(update("c1", fmt.Sprintf("step-%d", n))); err != nil {
			t.Fatal(err)
		}
	}
	if all.queue.Len() != 1 || len(all.wake) != 1 || all.bytes > MaxPendingBytes {
		t.Fatal("snapshot queue is unbounded")
	}
	item, ok, done, err := all.next()
	if !ok || done || err != nil || !strings.Contains(string(item.Data), "step-999") {
		t.Fatalf("latest snapshot: %+v, ok=%v done=%v err=%v", item, ok, done, err)
	}
	if other.queue.Len() != 0 {
		t.Fatal("update crossed project filter")
	}
	// System update snapshots reach all-project subscribers only.
	if err := hub.Publish(Update{Kind: "update", Data: json.RawMessage(`{"state":"downloading","downloadPercent":42}`)}); err != nil {
		t.Fatal(err)
	}
	sys, ok, done, err := all.next()
	if !ok || done || err != nil || sys.Kind != "update" || !strings.Contains(string(sys.Data), "42") {
		t.Fatalf("unexpected system update: %+v ok=%v done=%v err=%v", sys, ok, done, err)
	}
	if other.queue.Len() != 0 {
		t.Fatal("system update leaked into project-scoped subscriber")
	}
}

func TestSlowConsumerDropsPendingMemory(t *testing.T) {
	for _, mode := range []string{"count", "bytes"} {
		t.Run(mode, func(t *testing.T) {
			hub := NewHub()
			defer hub.Close()
			sub, stop := hub.Subscribe("")
			defer stop()
			if mode == "bytes" {
				if err := hub.Publish(update("huge", strings.Repeat("x", MaxPendingBytes))); err != nil {
					t.Fatal(err)
				}
			} else {
				for n := range MaxPendingUpdates + 1 {
					if err := hub.Publish(update(fmt.Sprintf("c%d", n), "text")); err != nil {
						t.Fatal(err)
					}
				}
			}
			_, _, done, err := sub.next()
			if !done || !errors.Is(err, ErrSlowConsumer) {
				t.Fatalf("overflow: done=%v err=%v", done, err)
			}
			if sub.bytes != 0 || sub.queue.Len() != 0 || len(sub.byKey) != 0 {
				t.Fatal("overflow retained pending updates")
			}
			stop()
			if len(hub.clients) != 0 {
				t.Fatal("closed stream retained subscription")
			}
		})
	}
}

func TestFreshUpdateReplacesInitialAndCancellationIsFinal(t *testing.T) {
	hub := NewHub()
	sub, stop := hub.Subscribe("p1")
	if err := hub.Publish(update("c1", "fresh")); err != nil {
		t.Fatal(err)
	}
	item := sub.takeInitial(update("c1", "stale"))
	if !strings.Contains(string(item.Data), "fresh") || sub.bytes != 0 {
		t.Fatal("initial snapshot overwrote fresh update")
	}
	stop()
	stop()
	if err := hub.Publish(update("c1", "after-close")); err != nil {
		t.Fatal(err)
	}
	if sub.queue.Len() != 0 {
		t.Fatal("cancelled stream accepted updates")
	}
	hub.Close()
	closed, stopClosed := hub.Subscribe("")
	defer stopClosed()
	_, _, done, _ := closed.next()
	if !done {
		t.Fatal("hub close accepted a subscriber")
	}
}

func TestNormalizeUpdateCompactsAndIsolatesCallerBytes(t *testing.T) {
	raw := json.RawMessage("{\n  \"text\": \"one\\ntwo\"\n}")
	normalized, _, err := normalizeUpdate(Update{Kind: "draft", ProjectID: "p1", ConversationID: "c1", Data: raw})
	if err != nil {
		t.Fatal(err)
	}
	raw[0] = 'x'
	if !json.Valid(normalized.Data) || strings.Contains(string(normalized.Data), "\n") {
		t.Fatalf("invalid compacted data: %q", string(normalized.Data))
	}
}
