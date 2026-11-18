package runner

import (
	"context"
	"fmt"
	"iter"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/models"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/sdk"
)

func TestCoordinatorWithdrawsItsMatchingAskAfterThreadAnswer(t *testing.T) {
	ctx := t.Context()
	home := t.TempDir()
	repo, err := core.Open(ctx, core.Options{Path: filepath.Join(home, "answers.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	project, err := repo.CreateProject(ctx, new("Delivery"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "Deliver the fix"})
	if err != nil {
		t.Fatal(err)
	}
	id := project.CoordinatorID
	postAsk := func(conversation, author, context string, block args) core.MessageView {
		t.Helper()
		message, err := repo.PostMessage(ctx, project.ID, core.MessageInput{
			ConversationID: conversation, Author: author, Content: context, Blocks: []any{block},
		})
		if err != nil || message.AttentionID() == "" {
			t.Fatalf("post question: %+v, %v", message, err)
		}
		return message
	}
	matching := postAsk(id, "coordinator", "Target for the fix", args{"type": "confirm", "title": "Use the preview branch?"})
	unrelated := postAsk(id, "coordinator", "Target for a separate migration", args{"type": "confirm", "title": "Use the preview branch?"})
	source := postAsk(thread.ID, "thread", "Fix delivery", args{
		"type": "choice", "question": "Which branch should get this fix?",
		"options": []any{args{"label": "preview", "consequence": "Deliver the fix to preview"}, args{"label": "main", "consequence": "Deliver the fix to main"}},
	})
	system, err := repo.OpenAttention(ctx, project.ID, core.AttentionInput{
		ThreadID: &thread.ID, Block: args{"type": "confirm", "title": "Standalone system confirmation"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.AnswerAttention(ctx, system.ID, args{"type": "confirm", "approved": false}); err != nil {
		t.Fatal(err)
	}
	assertPending := func(want bool) {
		t.Helper()
		pending, err := repo.ListPendingConversations(ctx)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, conversation := range pending {
			found = found || conversation.ID == id
		}
		if found != want {
			t.Fatalf("coordinator pending = %v, want %v", found, want)
		}
	}
	assertPending(false)
	if err := repo.AnswerAttention(ctx, source.AttentionID(), args{"type": "choice", "choice": 0}); err != nil {
		t.Fatal(err)
	}
	assertPending(true)
	if err := repo.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	assertPending(true)

	store, err := settings.Open(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	info := sdk.ModelProviderInfo{Kind: "mock", Program: true}
	registry := models.New()
	var firstRequest []sdk.Message
	calls := 0
	if _, err := registry.Register(sdk.ModelProvider{
		Info: info,
		Stream: func(_ context.Context, _ map[string]any, request sdk.Request) iter.Seq[sdk.Part] {
			return func(yield func(sdk.Part) bool) {
				calls++
				if calls == 1 {
					firstRequest = request.Messages
					if !yield(sdk.Part{Type: "tool-call", ID: "call_withdraw", Name: "withdraw_ask",
						Arguments: fmt.Sprintf(`{"attentionId":%q,"reason":"Already answered in the discussion thread"}`, matching.AttentionID())}) {
						return
					}
					yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
					return
				}
				yield(sdk.Part{Type: "finish", Reason: "stop"})
			}
		},
	}); err != nil {
		t.Fatal(err)
	}
	turnID, err := repo.StartScheduledTurn(ctx, id, "mock")
	if err != nil || turnID == "" {
		t.Fatalf("schedule coordinator: %q, %v", turnID, err)
	}
	agent := &turn{
		Runner: &Runner{Repository: repo, Settings: store, Models: registry},
		id:     turnID, conversation: core.ConversationView{ID: id, ProjectID: project.ID, Role: "coordinator"},
	}
	if err := agent.run(ctx, &settings.ResolvedModel{Provider: info, Model: "mock", Label: "mock"}); err != nil {
		t.Fatal(err)
	}
	var prompt string
	for _, message := range firstRequest {
		if message.Role == "user" && strings.Contains(message.Content, "<answer_in_thread>") {
			prompt += message.Content
		}
	}
	for _, want := range []string{
		"Deliver the fix", "Fix delivery", "Which branch should get this fix?", `"choice": 0`,
		source.AttentionID(), matching.AttentionID(), unrelated.AttentionID(),
		"Target for the fix", "Target for a separate migration", "Use withdraw_ask",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("coordinator context is missing %q: %s", want, prompt)
		}
	}
	if strings.Count(prompt, "<answer_in_thread>") != 1 || strings.Contains(prompt, system.ID) {
		t.Fatalf("wrong answers reached the coordinator: %s", prompt)
	}
	open, err := repo.ListOpenAttentions(ctx, project.ID)
	if err != nil || len(open) != 1 || open[0].ID != unrelated.AttentionID() {
		t.Fatalf("expected only the unrelated question to remain open: %+v, %v", open, err)
	}
}
