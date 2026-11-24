package server

import (
	"encoding/json"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/daidr/Ariadne/internal/realtime"
)

func BenchmarkConnectEventsDraftStream10Clients(b *testing.B) {
	const (
		clients = 10
		ticks   = 50
	)
	blocks := json.RawMessage(`[{"type":"steps","items":[{"title":"步骤一","status":"done"},{"title":"步骤二","status":"doing"}]}]`)
	snapshots := make([]string, ticks)
	var sb strings.Builder
	for i := range ticks {
		sb.WriteString("增量流式推送草稿正文内容，避免每 100ms 重复传输全量累计文本并消除 JSON 重复编解码。\n")
		snapshots[i] = sb.String()
	}

	b.Run("TypedIncrementalDelta", func(b *testing.B) {
		b.ReportAllocs()
		var wireBytes int64
		b.ResetTimer()
		for range b.N {
			trackers := make([]draftStreamTracker, clients)
			for tIdx, text := range snapshots {
				item := realtime.Update{
					Kind:           "draft",
					ProjectID:      "prj_1",
					ConversationID: "thr_1",
					Value: &realtime.DraftUpdate{
						ProjectID:      "prj_1",
						ConversationID: "thr_1",
						Draft: &realtime.ReplyDraft{
							MessageID: "msg_1",
							Text:      text,
							Blocks:    blocks,
							StartedAt: 1_800_000_000_000,
							Posted:    tIdx == ticks-1,
						},
					},
				}
				for c := range clients {
					msg, ok, commit := toProtoRealtimeUpdateWithDrafts(item, &trackers[c])
					if !ok {
						b.Fatal("expected draft proto frame")
					}
					encoded, err := proto.Marshal(msg)
					if err != nil {
						b.Fatal(err)
					}
					wireBytes += int64(len(encoded))
					if commit != nil {
						commit()
					}
				}
			}
		}
		b.ReportMetric(float64(wireBytes)/float64(b.N), "wire-B/stream")
	})

	b.Run("LegacyRawJSONFullSnapshot", func(b *testing.B) {
		b.ReportAllocs()
		var wireBytes int64
		b.ResetTimer()
		for range b.N {
			for tIdx, text := range snapshots {
				raw, err := json.Marshal(realtime.DraftUpdate{
					ProjectID:      "prj_1",
					ConversationID: "thr_1",
					Draft: &realtime.ReplyDraft{
						MessageID: "msg_1",
						Text:      text,
						Blocks:    blocks,
						StartedAt: 1_800_000_000_000,
						Posted:    tIdx == ticks-1,
					},
				})
				if err != nil {
					b.Fatal(err)
				}
				item := realtime.Update{
					Kind:           "draft",
					ProjectID:      "prj_1",
					ConversationID: "thr_1",
					Data:           raw,
				}
				for range clients {
					msg, ok := toProtoRealtimeUpdate(item)
					if !ok {
						b.Fatal("expected draft proto frame")
					}
					encoded, err := proto.Marshal(msg)
					if err != nil {
						b.Fatal(err)
					}
					wireBytes += int64(len(encoded))
				}
			}
		}
		b.ReportMetric(float64(wireBytes)/float64(b.N), "wire-B/stream")
	})
}
