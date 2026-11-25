package runner

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/models"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/sdk"
)

func TestCrossCompactionLoopHintsThenStops(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	home := t.TempDir()
	repo, err := core.Open(ctx, core.Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	project, err := repo.CreateProject(ctx, new("Compaction loop"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "Read-only loop"})
	if err != nil {
		t.Fatal(err)
	}
	turnID, err := repo.StartTurn(ctx, thread.ID, "fixture")
	if err != nil || turnID == "" {
		t.Fatalf("expected turn, got %q: %v", turnID, err)
	}
	conversation, err := repo.GetConversation(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	store, err := settings.Open(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatal(err)
	}

	registry := models.New()
	info := sdk.ModelProviderInfo{Kind: "fixture", Program: true}
	requests, summaries := 0, 0
	if _, err := registry.Register(sdk.ModelProvider{Info: info, Stream: func(_ context.Context, _ map[string]any, req sdk.Request) iter.Seq[sdk.Part] {
		return func(yield func(sdk.Part) bool) {
			if req.Conversation == "" && len(req.Tools) == 0 {
				summaries++
				if yield(sdk.Part{Type: "text", Text: fmt.Sprintf("Compaction summary %d.", summaries)}) {
					yield(sdk.Part{Type: "finish", Reason: "stop"})
				}
				return
			}
			requests++
			// Call only non-progress tools with large arguments so every step hits
			// the compaction threshold (20_000 * 0.72 = 14_400 tokens).
			if yield(sdk.Part{
				Type:      "tool-call",
				ID:        fmt.Sprintf("todo-%d", requests),
				Name:      "update_todo",
				Arguments: fmt.Sprintf(`{"items":[{"label":"Step %d","detail":%q,"status":"doing"}]}`, requests, strings.Repeat(string(rune('a'+requests%26)), 38000)),
			}) {
				yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
			}
		}
	}}); err != nil {
		t.Fatal(err)
	}

	active := &turn{Runner: &Runner{Repository: repo, Settings: store, Models: registry}, id: turnID, conversation: conversation}
	err = active.run(ctx, &settings.ResolvedModel{Provider: info, Model: "fixture", Label: "fixture", ContextTokens: new(20_000.0)})
	var domain *domainerr.Error
	if !errors.As(err, &domain) || domain.Key != "error.compactionLoop" {
		t.Fatalf("expected error.compactionLoop, got %v", err)
	}
	if domain.Params["count"] != compactionLoopLimit {
		t.Fatalf("expected count=%d, got %v", compactionLoopLimit, domain.Params["count"])
	}
	if summaries != compactionLoopLimit {
		t.Fatalf("expected loop to stop before running a 4th summary; got %d summaries", summaries)
	}

	// Verify tool results recorded in transcript: exactly two progressive hints
	// (first nudge after 2nd compaction, final warning after 3rd compaction).
	entries, err := repo.ListTranscript(ctx, thread.ID, core.TranscriptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var hints []string
	compactionsSeen := 0
	for _, entry := range entries {
		switch entry["kind"] {
		case "summary":
			compactionsSeen++
		case "tool":
			content, _ := entry["content"].(string)
			if strings.Contains(content, "compacted context") {
				if compactionsSeen < compactionLoopHintAfter {
					t.Fatalf("unexpected compaction loop hint after only %d compaction(s): %q", compactionsSeen, content)
				}
				hints = append(hints, content)
			}
		}
	}
	if len(hints) != 2 {
		t.Fatalf("expected 2 progressive compaction loop hints, got %d: %v", len(hints), hints)
	}
	if !strings.Contains(hints[0], "compacted context 2 times in a row") {
		t.Fatalf("expected first compaction loop nudge after 2nd compaction, got %q", hints[0])
	}
	if !strings.Contains(hints[1], "compacted context 3 times in a row") || !strings.Contains(hints[1], "One more compaction") {
		t.Fatalf("expected final compaction loop warning after 3rd compaction, got %q", hints[1])
	}
}

func TestCrossCompactionProgressResetsCounter(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	home := t.TempDir()
	repo, err := core.Open(ctx, core.Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	project, err := repo.CreateProject(ctx, new("Compaction progress reset"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "Progress resets loop count"})
	if err != nil {
		t.Fatal(err)
	}
	turnID, err := repo.StartTurn(ctx, thread.ID, "fixture")
	if err != nil || turnID == "" {
		t.Fatalf("expected turn, got %q: %v", turnID, err)
	}
	conversation, err := repo.GetConversation(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	store, err := settings.Open(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatal(err)
	}

	registry := models.New()
	info := sdk.ModelProviderInfo{Kind: "fixture", Program: true}
	requests, summaries := 0, 0
	if _, err := registry.Register(sdk.ModelProvider{Info: info, Stream: func(_ context.Context, _ map[string]any, req sdk.Request) iter.Seq[sdk.Part] {
		return func(yield func(sdk.Part) bool) {
			if req.Conversation == "" && len(req.Tools) == 0 {
				summaries++
				if yield(sdk.Part{Type: "text", Text: fmt.Sprintf("Summary %d.", summaries)}) {
					yield(sdk.Part{Type: "finish", Reason: "stop"})
				}
				return
			}
			requests++
			switch requests {
			case 1, 2:
				// Two compactions without progress.
				if yield(sdk.Part{
					Type:      "tool-call",
					ID:        fmt.Sprintf("todo-%d", requests),
					Name:      "update_todo",
					Arguments: fmt.Sprintf(`{"items":[{"label":"Step %d","detail":%q,"status":"doing"}]}`, requests, strings.Repeat("x", 38000)),
				}) {
					yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
				}
			case 3:
				// Substantive progress (sending a user update) resets staleCompactions.
				if yield(sdk.Part{
					Type:      "tool-call",
					ID:        "msg-progress",
					Name:      "send_message",
					Arguments: fmt.Sprintf(`{"text":%q}`, "Progress update: "+strings.Repeat("p", 38000)),
				}) {
					yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
				}
			case 4, 5:
				// Two more compactions after progress must still be allowed.
				if yield(sdk.Part{
					Type:      "tool-call",
					ID:        fmt.Sprintf("todo-%d", requests),
					Name:      "update_todo",
					Arguments: fmt.Sprintf(`{"items":[{"label":"Step %d","detail":%q,"status":"doing"}]}`, requests, strings.Repeat("y", 38000)),
				}) {
					yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
				}
			case 6:
				if yield(sdk.Part{
					Type:      "tool-call",
					ID:        "report-done",
					Name:      "report",
					Arguments: `{"kind":"done","headline":"Finished","summary":"Progress reset worked."}`,
				}) {
					yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
				}
			default:
				yield(sdk.Part{Type: "finish", Reason: "stop"})
			}
		}
	}}); err != nil {
		t.Fatal(err)
	}

	active := &turn{Runner: &Runner{Repository: repo, Settings: store, Models: registry}, id: turnID, conversation: conversation}
	if err := active.run(ctx, &settings.ResolvedModel{Provider: info, Model: "fixture", Label: "fixture", ContextTokens: new(20_000.0)}); err != nil {
		t.Fatalf("expected turn to complete after progress reset, got %v", err)
	}
	if summaries < 4 {
		t.Fatalf("expected at least 4 compactions across progress-separated phases, got %d", summaries)
	}
}
