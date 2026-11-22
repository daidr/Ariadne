package core

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/daidr/Ariadne/internal/eventstore"
)

func TestFinishTurnOnlyReadsCallMetadataAndClosesEarlierDanglingCalls(t *testing.T) {
	repo, err := Open(t.Context(), Options{Path: filepath.Join(t.TempDir(), "finish.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	name := "project"
	project, err := repo.CreateProject(t.Context(), &name, ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	conversation := project.CoordinatorID
	data, _ := json.Marshal(map[string]any{"conversationId": conversation, "text": "earlier step", "reasoning": []any{}, "toolCalls": []any{map[string]any{"id": "old-call", "name": "earlier", "arguments": "{}"}}, "sawSeq": 0, "input": map[string]any{"call": "old"}})
	if _, err := repo.Store.Append(t.Context(), []eventstore.NewEvent{{Type: "turn.step", ProjectID: project.ID, Data: data}}); err != nil {
		t.Fatal(err)
	}
	message, err := repo.PostMessage(t.Context(), project.ID, MessageInput{ConversationID: conversation, Author: "user", Content: "work"})
	if err != nil {
		t.Fatal(err)
	}
	// Unrelated message metadata must not be decoded while closing tool calls.
	if _, err := repo.Store.DB().ExecContext(t.Context(), "UPDATE messages SET attachments = 'not JSON' WHERE id = ?", message.ID); err != nil {
		t.Fatal(err)
	}
	turn, err := repo.StartTurn(t.Context(), conversation, "model")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.FinishTurn(t.Context(), conversation, turn, "completed", nil); err != nil {
		t.Fatal("finish loaded unrelated chat data", err)
	}
	rows, err := repo.Store.DB().QueryContext(t.Context(), "SELECT json_extract(data,'$.toolCallId') FROM events WHERE project_id = ? AND type = 'tool.finished'", project.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var id string
	if !rows.Next() || rows.Scan(&id) != nil || id != "old-call" || rows.Next() {
		t.Fatal("historical dangling call not closed exactly once")
	}
}
func TestCompactionPreservesFractionalModelPosition(t *testing.T) {
	repo, err := Open(t.Context(), Options{Path: filepath.Join(t.TempDir(), "half-step.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	name := "project"
	project, err := repo.CreateProject(t.Context(), &name, ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := repo.StartTurn(t.Context(), project.CoordinatorID, "model")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompactConversation(t.Context(), project.CoordinatorID, turn, Compaction{Summary: "notes", UpTo: 1.5, SawSeq: 2, Call: "{}"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.ClearToolResults(t.Context(), project.CoordinatorID, turn, 2.5, 3); err != nil {
		t.Fatal(err)
	}
	entries, err := repo.ListTranscript(t.Context(), project.CoordinatorID, TranscriptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, entry := range entries {
		switch entry.str("kind") {
		case "summary":
			if entry["upTo"] != 1.5 {
				t.Fatal("summary position truncated", entry)
			}
			seen++
		case "cleared":
			if entry["upTo"] != 2.5 {
				t.Fatal("cleared position truncated", entry)
			}
			seen++
		}
	}
	if seen != 2 {
		t.Fatal("missing compaction records")
	}
}

func TestTurnStepUpdatesContextTokensWithoutZeroOverwrite(t *testing.T) {
	repo, err := Open(t.Context(), Options{Path: filepath.Join(t.TempDir(), "context.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	name := "project"
	project, err := repo.CreateProject(t.Context(), &name, ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := repo.CreateThread(t.Context(), project.ID, NewThread{Title: "Investigate"})
	if err != nil {
		t.Fatal(err)
	}
	turnID, err := repo.StartTurn(t.Context(), thread.ID, "model")
	if err != nil {
		t.Fatal(err)
	}
	// Step 1: tool-only step before any visible message reports mid-turn contextInput only (ACP style).
	if err := repo.RecordStep(t.Context(), thread.ID, turnID, Step{
		SawSeq: 1,
		Usage:  &TokenUsage{Input: 0, Output: 0, ContextInput: new(int64(42000))},
	}); err != nil {
		t.Fatal(err)
	}
	conv, err := repo.GetConversation(t.Context(), thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if conv.ContextTokens != 42000 {
		t.Fatalf("expected contextTokens 42000 after first tool step, got %d", conv.ContextTokens)
	}
	// Step 2: output/cost-only delta without contextInput must NOT overwrite contextTokens with 0.
	if err := repo.RecordStep(t.Context(), thread.ID, turnID, Step{
		SawSeq: 2,
		Usage:  &TokenUsage{Input: 0, Output: 20, Cost: &Cost{Currency: "USD", Amount: 0.05}},
	}); err != nil {
		t.Fatal(err)
	}
	conv, err = repo.GetConversation(t.Context(), thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if conv.ContextTokens != 42000 {
		t.Fatalf("output/cost-only step wiped contextTokens: got %d, want 42000", conv.ContextTokens)
	}
	// Step 3: explicit zero contextInput must not overwrite contextTokens or fall back to cumulative input.
	if err := repo.RecordStep(t.Context(), thread.ID, turnID, Step{
		SawSeq: 3,
		Usage:  &TokenUsage{Input: 150000, Output: 10, ContextInput: new(int64(0))},
	}); err != nil {
		t.Fatal(err)
	}
	conv, err = repo.GetConversation(t.Context(), thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if conv.ContextTokens != 42000 {
		t.Fatalf("explicit zero contextInput changed contextTokens: got %d, want 42000", conv.ContextTokens)
	}
	// Step 4: direct API style usage without ContextInput updates contextTokens from Input.
	if err := repo.RecordStep(t.Context(), thread.ID, turnID, Step{
		SawSeq: 4,
		Usage:  &TokenUsage{Input: 61000, Output: 120},
	}); err != nil {
		t.Fatal(err)
	}
	conv, err = repo.GetConversation(t.Context(), thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if conv.ContextTokens != 61000 {
		t.Fatalf("expected contextTokens 61000 from direct input, got %d", conv.ContextTokens)
	}
}
