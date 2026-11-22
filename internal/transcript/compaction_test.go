package transcript

import (
	"strings"
	"testing"
)

func TestCompactionNeverSeparatesCallsAndResults(t *testing.T) {
	entries := []Entry{
		{"seq": 1, "kind": "message", "message": map[string]any{"content": "old", "attachments": []any{}}},
		{"seq": 2, "kind": "step", "text": "", "sawSeq": 1, "toolCalls": []any{map[string]any{"id": "x", "name": "t", "arguments": "{}"}}},
		{"seq": 3, "kind": "message", "message": map[string]any{"content": "held", "attachments": []any{}}},
		{"seq": 4, "kind": "tool", "toolCallId": "x", "name": "t", "content": strings.Repeat("r", 400), "isError": false},
		{"seq": 5, "kind": "step", "text": "ok", "sawSeq": 4, "toolCalls": []any{}},
		{"seq": 6, "kind": "message", "message": map[string]any{"content": "new", "attachments": []any{}}},
	}
	if cut := CompactionCut(entries, 1); cut == nil || *cut != 4.5 {
		t.Fatal("cut lost half-step position", cut)
	}
	if CompactionCut(entries, 10000) != nil || CompactionCut(entries, 170) != nil || CompactionCut([]Entry{entries[1], entries[2], entries[4]}, 1) != nil {
		t.Fatal("cut separated call/result or folded insufficient history")
	}
}

func TestCompactionRequestAndBudgets(t *testing.T) {
	entries := transcriptEntries()
	for _, entry := range entries {
		if entry["kind"] != "message" {
			continue
		}
		for _, raw := range arr(obj(entry["message"])["cites"]) {
			cite := obj(raw)
			cite["conversationId"] = "thr_source"
			cite["attachments"] = []any{}
		}
	}
	budgets := []float64{0, 1, 20, 100, 10000}
	cuts := []float64{1.5, 2.5, 5, 10}
	messages := []Message{{Role: "system", Content: "<system>\u2028中文😀"}, {Role: "user", Content: "image", Images: []Attachment{{FileID: "fil_a", MIME: "image/png", Name: "截图", Size: 5}}}}
	nativeCuts := []*float64{}
	for _, budget := range budgets {
		nativeCuts = append(nativeCuts, CompactionCut(entries, budget))
	}
	summaries := [][]Message{}
	for _, cut := range cuts {
		summaries = append(summaries, SummaryRequest(entries, cut, func(id string) string { return "title:" + id }, "coordinator", ChatOptions{Images: true, ClearUpTo: new(float64(100))}, 20))
	}
	if CompactAt(DefaultContextTokens, .6) <= 0 || EstimateTokens(messages) <= 0 || len(nativeCuts) != len(budgets) || len(summaries) != len(cuts) {
		t.Fatalf("unexpected compaction results: cuts=%d summaries=%d", len(nativeCuts), len(summaries))
	}
}
