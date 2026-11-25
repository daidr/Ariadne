package runner

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/realtime"
)

func TestPostUIDraftStaysTransientUntilValidated(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	repo, err := core.Open(ctx, core.Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	project, err := repo.CreateProject(ctx, new("Drafts"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := repo.GetConversation(ctx, project.CoordinatorID)
	if err != nil {
		t.Fatal(err)
	}
	hub := realtime.NewHub()
	defer hub.Close()
	state := realtime.NewState(hub)
	defer state.Close()
	drafts := realtime.NewDrafts(state, realtime.LiveOptions{})
	defer drafts.Close()
	agent := turn{Runner: &Runner{Repository: repo, Drafts: drafts}, conversation: conversation}

	snapshot := func() *realtime.ReplyDraft {
		t.Helper()
		for update := range state.Snapshots(project.ID) {
			if update.Kind == "draft" {
				if payload, ok := update.Value.(*realtime.DraftUpdate); ok && payload != nil {
					return payload.Draft
				}
				var payload realtime.DraftUpdate
				if err := json.Unmarshal(update.Data, &payload); err != nil {
					t.Fatal(err)
				}
				return payload.Draft
			}
		}
		return nil
	}

	agent.preview(new(0), "post_ui", `{"blocks":[{"type":"choice","question":"Where?","options":[{"label":"Prev`)
	draft := snapshot()
	if draft == nil || draft.Text != "" || !strings.Contains(string(draft.Blocks), `"labels":["Prev"]`) {
		t.Fatalf("partial choice not previewed: %+v", draft)
	}
	var invalid args
	if err := json.Unmarshal([]byte(`{"blocks":[{"type":"choice","question":"Where?"}]}`), &invalid); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.postUI(ctx, invalid); err == nil || snapshot() != nil {
		t.Fatal("invalid card was accepted or its draft was left behind")
	}
	if current, err := repo.GetConversation(ctx, conversation.ID); err != nil || current.PendingSeq != conversation.PendingSeq {
		t.Fatalf("invalid card changed conversation: %+v, %v", current, err)
	}

	agent.preview(new(0), "post_ui", `{"blocks":[{"type":"confirm","title":"Use the preview branch?`)
	draft = snapshot()
	if draft == nil || !strings.Contains(string(draft.Blocks), `"type":"confirm"`) || strings.Contains(string(draft.Blocks), "attentionId") {
		t.Fatalf("confirmation draft: %+v", draft)
	}
	result, err := agent.postUI(ctx, args{"blocks": []any{args{"type": "confirm", "title": "Use the preview branch?"}}})
	if err != nil || !strings.Contains(result, draft.MessageID) || !strings.Contains(result, "attentionId") || snapshot() != nil {
		t.Fatalf("draft was not promoted: %s, %v", result, err)
	}
}

func TestDocumentDraftReplacesOnlyCompleteAnchors(t *testing.T) {
	t.Parallel()
	base := "Introduction\n\nOld paragraph\n\nKeep this."
	for _, test := range []struct{ body, want string }{
		{`{"artifactId":"doc","edits":[{"old":"Old para`, base},
		{`{"artifactId":"doc","edits":[{"new":"New","old":"Old para`, base},
		{`{"artifactId":"doc","edits":[{"old":"Old paragraph","new":"`, base},
		{`{"artifactId":"doc","edits":[{"old":"Old paragraph","new":"New\ntext \uD83D\uDE`, "Introduction\n\nNew\ntext \n\nKeep this."},
		{`{"artifactId":"doc","edits":[{"old":"Introduction","new":"Intro"},{"old":"Old paragraph","new":"Second`, "Intro\n\nSecond\n\nKeep this."},
		{`{"artifactId":"doc","edits":[{"old":"Old paragraph","new":""}`, "Introduction\n\n\n\nKeep this."},
	} {
		input, _ := growingArtifactArguments(test.body)
		got := base
		if edits := input["edits"]; edits != nil {
			var err error
			got, err = documentEdits(base, edits)
			if err != nil {
				t.Fatal(err)
			}
		}
		if got != test.want {
			t.Errorf("%s: got %q, want %q", test.body, got, test.want)
		}
	}
}

func TestDocumentDraftRetainsAnchorBeforeReplacement(t *testing.T) {
	t.Parallel()
	input, _ := growingArtifactArguments(`{"artifactId":"doc","edits":[{"old":" Text with spaces ","new":"`)
	if input["pendingAnchor"] != " Text with spaces " || input["edits"] != nil {
		t.Fatalf("draft did not preserve the complete anchor before the replacement: %+v", input)
	}
}

func TestJSONPrefixRetainsNestedPartialStrings(t *testing.T) {
	t.Parallel()
	full := `{"text":"See below","blocks":[{"type":"choice","question":"选一个","options":[{"label":"\uD83D\uDE00","consequence":"a"},{"label":"B","consequence":"b"}]}]}`
	for end := 1; end <= len(full); end++ {
		if prefixObject(full[:end]) == nil {
			t.Fatalf("valid JSON prefix rejected: %q", full[:end])
		}
	}
	if blocks := previewBlocks(prefixObject(`{"threadIds":["thr_first","thr_incomplete`)); string(blocks) != `[{"threadId":"thr_first","type":"thread"}]` {
		t.Fatalf("partial thread ID was exposed: %s", blocks)
	}
}
