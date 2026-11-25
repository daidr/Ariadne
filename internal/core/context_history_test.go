package core_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/transcript"
)

func TestModelContextSkipsSummarisedOutputButKeepsUserWordsAndPrompts(t *testing.T) {
	t.Parallel()
	repo, err := core.Open(t.Context(), core.Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	name := "project"
	project, err := repo.CreateProject(t.Context(), &name, core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	message, err := repo.PostMessage(t.Context(), project.ID, core.MessageInput{ConversationID: project.CoordinatorID, Author: "user", Content: "preserve my exact request"})
	if err != nil {
		t.Fatal(err)
	}
	db := repo.Store.DB()
	if _, err := db.ExecContext(t.Context(), "DELETE FROM transcript WHERE conversation_id = ?", project.CoordinatorID); err != nil {
		t.Fatal(err)
	}
	type row struct {
		seq       int
		kind, ref string
		payload   any
	}
	rows := []row{
		{0, "prompt", "", map[string]any{"base": true, "sections": []any{[]any{"instructions", "original"}}, "removedTools": []any{}}},
		{1, "message", message.ID, nil},
		{2, "step", "", map[string]any{"sawSeq": 1, "text": "obsolete", "toolCalls": []any{}}},
		{3, "tool", "", map[string]any{"toolCallId": "old", "content": strings.Repeat("obsolete output", 600000)}},
		{4, "summary", "", map[string]any{"summary": "old summary", "upTo": 1}},
		{5, "prompt", "", map[string]any{"base": false, "sections": []any{[]any{"instructions", "new"}}, "removedTools": []any{"write_file"}}},
		{6, "summary", "", map[string]any{"summary": "current notes", "upTo": 3.5}},
		{7, "cleared", "", map[string]any{"upTo": 2.5}},
		// Its event is later, but its model position belongs to the folded part.
		{8, "step", "", map[string]any{"sawSeq": 3, "text": "arrived late", "toolCalls": []any{}}},
		{9, "step", "", map[string]any{"sawSeq": 8, "text": "new response", "toolCalls": []any{}}},
	}
	for _, row := range rows {
		payload, _ := json.Marshal(row.payload)
		if _, err := db.ExecContext(t.Context(), "INSERT INTO transcript(project_id,conversation_id,seq,kind,ref,payload) VALUES(?,?,?,?,?,?)", project.ID, project.CoordinatorID, row.seq, row.kind, row.ref, string(payload)); err != nil {
			t.Fatal(err)
		}
	}
	read := func(options core.TranscriptOptions) []transcript.Entry {
		t.Helper()
		rows, err := repo.ListTranscript(t.Context(), project.CoordinatorID, options)
		if err != nil {
			t.Fatal(err)
		}
		entries := make([]transcript.Entry, len(rows))
		for i, row := range rows {
			entries[i] = map[string]any(row)
		}
		return entries
	}
	full := read(core.TranscriptOptions{})
	projected := read(core.TranscriptOptions{ModelContext: true})
	if len(projected) != 6 {
		t.Fatal("unexpected projection size", len(projected))
	}
	for _, options := range []transcript.ChatOptions{{}, {PromptUpdates: new(false)}} {
		if !reflect.DeepEqual(transcript.ToMessages(full, nil, options), transcript.ToMessages(projected, nil, options)) {
			t.Fatal("projected model input changed")
		}
	}
	for _, upTo := range []int64{2, 4, 5, 8, 9} {
		a := read(core.TranscriptOptions{UpTo: &upTo})
		b := read(core.TranscriptOptions{UpTo: &upTo, ModelContext: true})
		if !reflect.DeepEqual(transcript.ToMessages(a, nil, transcript.ChatOptions{}), transcript.ToMessages(b, nil, transcript.ChatOptions{})) {
			t.Fatal("historical model input changed", upTo)
		}
	}
	// Obsolete output is filtered before Go decoding, even if it is not JSON.
	if _, err := db.ExecContext(t.Context(), "UPDATE transcript SET payload = 'unreadable obsolete data' WHERE conversation_id = ? AND seq IN (3,4)", project.CoordinatorID); err != nil {
		t.Fatal(err)
	}
	read(core.TranscriptOptions{ModelContext: true})
}
