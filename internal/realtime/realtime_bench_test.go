package realtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/eventstore"
)

func BenchmarkStateHubPublish10Clients(b *testing.B) {
	const clients = 10
	draftText := strings.Repeat("优化 Realtime 推送链路，消除重复 JSON 编解码。", 100) // ~5.5 KB
	blocks := json.RawMessage(`[{"type":"steps","items":[{"title":"分析","status":"done"}]}]`)

	b.Run("TypedZeroJSON", func(b *testing.B) {
		hub := NewHub()
		defer hub.Close()
		state := NewState(hub)
		defer state.Close()
		subs := make([]*Subscription, clients)
		stops := make([]func(), clients)
		for i := range clients {
			subs[i], stops[i] = hub.Subscribe("prj_1")
		}
		defer func() {
			for _, stop := range stops {
				stop()
			}
		}()

		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			payload := &DraftUpdate{
				ProjectID:      "prj_1",
				ConversationID: "thr_1",
				Draft: &ReplyDraft{
					MessageID: "msg_1",
					Text:      draftText,
					Blocks:    blocks,
					StartedAt: 1_800_000_000_000,
				},
			}
			if err := state.Set(Update{
				Kind:           "draft",
				ProjectID:      "prj_1",
				ConversationID: "thr_1",
				Value:          payload,
			}, true); err != nil {
				b.Fatal(err)
			}
			for _, sub := range subs {
				item, ok, _, err := sub.next()
				if !ok || err != nil {
					b.Fatalf("next failed: ok=%v err=%v", ok, err)
				}
				if _, ok := item.Value.(*DraftUpdate); !ok {
					b.Fatal("expected typed *DraftUpdate")
				}
			}
		}
	})

	b.Run("LegacyRawJSONRoundTrip", func(b *testing.B) {
		hub := NewHub()
		defer hub.Close()
		state := NewState(hub)
		defer state.Close()
		subs := make([]*Subscription, clients)
		stops := make([]func(), clients)
		for i := range clients {
			subs[i], stops[i] = hub.Subscribe("prj_1")
		}
		defer func() {
			for _, stop := range stops {
				stop()
			}
		}()

		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			raw, err := json.Marshal(DraftUpdate{
				ProjectID:      "prj_1",
				ConversationID: "thr_1",
				Draft: &ReplyDraft{
					MessageID: "msg_1",
					Text:      draftText,
					Blocks:    blocks,
					StartedAt: 1_800_000_000_000,
				},
			})
			if err != nil {
				b.Fatal(err)
			}
			if err := state.Set(Update{
				Kind:           "draft",
				ProjectID:      "prj_1",
				ConversationID: "thr_1",
				Data:           raw,
			}, true); err != nil {
				b.Fatal(err)
			}
			for _, sub := range subs {
				item, ok, _, err := sub.next()
				if !ok || err != nil {
					b.Fatalf("next failed: ok=%v err=%v", ok, err)
				}
				var decoded DraftUpdate
				if err := json.Unmarshal(item.Data, &decoded); err != nil {
					b.Fatal(err)
				}
			}
		}
	})
}

func BenchmarkDraftWrite100Chunks(b *testing.B) {
	chunks := make([]string, 100)
	var sb strings.Builder
	for i := range chunks {
		sb.WriteString("这是一个流式输出的分片内容，用来模拟模型连续吐出大量文本的场景。\n")
		chunks[i] = sb.String()
	}
	hub := NewHub()
	defer hub.Close()
	state := NewState(hub)
	defer state.Close()
	drafts := NewDrafts(state, LiveOptions{
		Now:      func() time.Time { return time.UnixMilli(1_800_000_000_000) },
		Interval: time.Hour,
	})
	defer drafts.Close()

	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		handle, err := drafts.Start("prj_1", "thr_1", fmt.Sprintf("msg_%d", i))
		if err != nil {
			b.Fatal(err)
		}
		for _, text := range chunks {
			if err := handle.Write(text); err != nil {
				b.Fatal(err)
			}
		}
		handle.Posted()
	}
}

func BenchmarkHubPublishProjectIsolation(b *testing.B) {
	hub := NewHub()
	defer hub.Close()
	stops := make([]func(), 0, 20)
	var activeSubs []*Subscription
	for p := range 10 {
		project := fmt.Sprintf("prj_%d", p)
		for range 2 {
			sub, stop := hub.Subscribe(project)
			stops = append(stops, stop)
			if p == 0 {
				activeSubs = append(activeSubs, sub)
			}
		}
	}
	defer func() {
		for _, stop := range stops {
			stop()
		}
	}()

	payload := &DraftUpdate{
		ProjectID:      "prj_0",
		ConversationID: "thr_0",
		Draft:          &ReplyDraft{MessageID: "msg_1", Text: "delta", StartedAt: 1},
	}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := hub.Publish(Update{
			Kind:           "draft",
			ProjectID:      "prj_0",
			ConversationID: "thr_0",
			Value:          payload,
		}); err != nil {
			b.Fatal(err)
		}
		for _, sub := range activeSubs {
			_, _, _, _ = sub.next()
		}
	}
}

func BenchmarkEventStoreLiveRead10Clients(b *testing.B) {
	ctx := context.Background()
	store, err := eventstore.Open(ctx, eventstore.Options{Path: ":memory:"})
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()

	// Seed 300 events so seq=1..44 are evicted from the 256-entry ring buffer,
	// allowing direct comparison of RingCache vs SQLiteFallback on the same DB.
	seed := make([]eventstore.NewEvent, 300)
	for i := range seed {
		seed[i] = eventstore.NewEvent{
			Type:      "message.posted",
			ProjectID: "prj_1",
			Data:      json.RawMessage(`{"conversationId":"thr_1","content":"hello world"}`),
		}
	}
	stored, err := store.Append(ctx, seed)
	if err != nil {
		b.Fatal(err)
	}
	liveAfterSeq := stored[len(stored)-2].Seq // in ring buffer (1 event behind head)

	b.Run("RingCache10Clients", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			for range 10 {
				events, err := store.Read(ctx, eventstore.ReadOptions{
					AfterSeq:  liveAfterSeq,
					ProjectID: "prj_1",
					Limit:     replayBatchSize,
				})
				if err != nil || len(events) != 1 {
					b.Fatalf("ring read failed: len=%d err=%v", len(events), err)
				}
			}
		}
	})

	b.Run("SQLiteFallback10Clients", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			for range 10 {
				events, err := store.Read(ctx, eventstore.ReadOptions{
					AfterSeq:  0,
					ProjectID: "prj_1",
					Limit:     1,
				})
				if err != nil || len(events) != 1 {
					b.Fatalf("sqlite read failed: len=%d err=%v", len(events), err)
				}
			}
		}
	})
}
