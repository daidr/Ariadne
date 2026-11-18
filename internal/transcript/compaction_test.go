package transcript

import (
	"encoding/json"
	"github.com/daidr/Ariadne/internal/legacytest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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

func TestCompactionRequestAndBudgetsMatchLegacy(t *testing.T) {
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
	native := map[string]any{"thresholds": []int{CompactAt(DefaultContextTokens, .6), CompactAt(200000, .6), CompactAt(1000000, .9)}, "estimate": EstimateTokens(messages), "cuts": nativeCuts, "summaries": summaries}
	bun, err := exec.LookPath("bun")
	if err != nil {
		if os.Getenv("ARIADNE_REQUIRE_LEGACY") == "1" {
			t.Fatal(err)
		}
		t.Skip("Bun unavailable")
	}
	input, _ := json.Marshal(map[string]any{"entries": entries, "messages": messages, "budgets": budgets, "cuts": cuts})
	command := legacytest.Command(t, bun, "packages/agent/src/testing/go-compaction-fixture.ts")
	command.Dir = filepath.Join("..", "..")
	command.Stdin = strings.NewReader(string(input))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatal(err, string(output))
	}
	data, _ := json.Marshal(native)
	var actual, want map[string]any
	if json.Unmarshal(data, &actual) != nil || json.Unmarshal(output, &want) != nil {
		t.Fatal("invalid fixture JSON", string(output))
	}
	for key, value := range actual {
		if !reflect.DeepEqual(value, want[key]) {
			got, _ := json.Marshal(value)
			expected, _ := json.Marshal(want[key])
			t.Errorf("%s\nGo %s\nTS %s", key, got, expected)
		}
	}
}
