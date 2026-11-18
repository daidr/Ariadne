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
	frame, done, err := all.next()
	if done || err != nil || !strings.Contains(string(frame), "step-999") {
		t.Fatalf("latest snapshot: %s, %v, %v", frame, done, err)
	}
	if other.queue.Len() != 0 {
		t.Fatal("update crossed project filter")
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
			_, done, err := sub.next()
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
	frame := sub.takeInitial(update("c1", "stale"))
	if !strings.Contains(string(frame), "fresh") || sub.bytes != 0 {
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
	_, done, _ := closed.next()
	if !done {
		t.Fatal("hub close accepted a subscriber")
	}
}

func TestMultilineJSONDoesNotInjectSSEFields(t *testing.T) {
	frame := string(updateFrame(Update{Kind: "draft", Data: json.RawMessage("{\n\"text\":\"one\\ntwo\"\n}")}))
	if strings.Count(frame, "\ndata:") != 1 || strings.Count(frame, "\n") != 3 {
		t.Fatalf("invalid frame: %q", frame)
	}
}
