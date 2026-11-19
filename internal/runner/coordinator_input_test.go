package runner

import (
	"context"
	"fmt"
	"iter"
	"path/filepath"
	"testing"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/models"
	"github.com/daidr/Ariadne/internal/realtime"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/sdk"
)

func TestCoordinatorKeepsDiscussionRepliesInTheirThread(t *testing.T) {
	for _, test := range []struct {
		name         string
		relay        bool
		directDuring bool
		calls        int
	}{
		{name: "attempted public reply ends quietly", calls: 1},
		{name: "coordination uses the internal relay", relay: true, calls: 2},
		{name: "direct request arriving during coordination gets a reply", relay: true, directDuring: true, calls: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			home := t.TempDir()
			repo, err := core.Open(ctx, core.Options{Path: filepath.Join(home, "test.sqlite")})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = repo.Close() })
			project, err := repo.CreateProject(ctx, new("Coordination"), core.ProjectStart{})
			if err != nil {
				t.Fatal(err)
			}
			thread, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "Test run"})
			if err != nil {
				t.Fatal(err)
			}
			post := func(id, text string) {
				t.Helper()
				if _, err := repo.PostMessage(ctx, project.ID, core.MessageInput{ConversationID: id, Author: "user", Content: text}); err != nil {
					t.Fatal(err)
				}
			}
			post(thread.ID, "Where are the test results?")
			if _, err := repo.ReportThread(ctx, thread.ID, core.ThreadReport{Kind: "done", Headline: "Tests finished", Summary: "The results are ready"}, nil, core.ReviewOptions{}); err != nil {
				t.Fatal(err)
			}
			conversationID := project.CoordinatorID
			id, err := repo.StartScheduledTurn(ctx, conversationID, "mock")
			if err != nil || id == "" {
				t.Fatal(id, err)
			}
			store, err := settings.Open(filepath.Join(home, "config.json"))
			if err != nil {
				t.Fatal(err)
			}
			hub := realtime.NewHub()
			t.Cleanup(hub.Close)
			state := realtime.NewState(hub)
			t.Cleanup(state.Close)
			drafts := realtime.NewDrafts(state, realtime.LiveOptions{})
			t.Cleanup(drafts.Close)
			info := sdk.ModelProviderInfo{Kind: "mock", Program: true}
			registry := models.New()
			calls := 0
			_, err = registry.Register(sdk.ModelProvider{Info: info, Stream: func(context.Context, map[string]any, sdk.Request) iter.Seq[sdk.Part] {
				return func(yield func(sdk.Part) bool) {
					calls++
					draftShown := func() bool {
						for update := range state.Snapshots(project.ID) {
							if update.Kind == "draft" && update.ConversationID == conversationID {
								return true
							}
						}
						return false
					}
					switch calls {
					case 1:
						for index, call := range []sdk.ToolCall{
							{ID: "duplicate", Name: "send_message", Arguments: `{"text":"The results are ready."}`},
							{ID: "card", Name: "post_ui", Arguments: `{"blocks":[{"type":"confirm","title":"Another question?"}]}`},
						} {
							if !yield(sdk.Part{Type: "tool-input", Call: &index, Name: call.Name, Arguments: call.Arguments}) {
								return
							}
							if draftShown() {
								t.Error("a quiet coordinator turn streamed a reply")
							}
							if !yield(sdk.Part{Type: "tool-call", ID: call.ID, Name: call.Name, Arguments: call.Arguments}) {
								return
							}
						}
						if test.relay && !yield(sdk.Part{Type: "tool-call", ID: "relay", Name: "message_thread", Arguments: fmt.Sprintf(`{"threadId":%q,"content":"Include the test log when you answer."}`, thread.ID)}) {
							return
						}
						if test.directDuring {
							post(conversationID, "Give me a separate project update.")
						}
						if yield(sdk.Part{Type: "text", Text: "The test results are ready."}) {
							yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
						}
						return
					case 2:
						current, err := repo.GetConversation(ctx, conversationID)
						if err != nil || current.Turn == nil || current.Turn.Activity != nil {
							t.Errorf("private text appeared in the coordinator activity line: %+v, %v", current.Turn, err)
						}
						if test.directDuring {
							const reply = `{"text":"The project is on schedule."}`
							if !yield(sdk.Part{Type: "tool-input", Call: new(0), Name: "send_message", Arguments: reply}) {
								return
							}
							if !draftShown() {
								t.Error("the direct request did not get a streamed reply")
							}
							if yield(sdk.Part{Type: "tool-call", ID: "direct", Name: "send_message", Arguments: reply}) {
								yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
							}
							return
						}
						if !yield(sdk.Part{Type: "text", Text: "Coordination handled."}) {
							return
						}
					}
					yield(sdk.Part{Type: "finish", Reason: "stop"})
				}
			}})
			if err != nil {
				t.Fatal(err)
			}
			agent := &turn{Runner: &Runner{Repository: repo, Settings: store, Models: registry, Drafts: drafts}, id: id,
				conversation: core.ConversationView{ID: conversationID, ProjectID: project.ID, Role: "coordinator"}}
			if err := agent.run(ctx, &settings.ResolvedModel{Provider: info, Model: "mock", Label: "mock"}); err != nil {
				t.Fatal(err)
			}
			if calls != test.calls {
				t.Fatalf("provider calls = %d, want %d", calls, test.calls)
			}
			messages, err := repo.ListMessages(ctx, conversationID)
			if err != nil {
				t.Fatal(err)
			}
			if test.directDuring {
				if len(messages) != 2 || messages[1].Content != "The project is on schedule." {
					t.Fatalf("unexpected direct reply: %+v", messages)
				}
			} else if len(messages) != 0 {
				t.Fatalf("unexpected coordinator messages: %+v", messages)
			}
			if open, err := repo.ListOpenAttentions(ctx, project.ID); err != nil || len(open) != 0 {
				t.Fatalf("unexpected interactive card: %+v, %v", open, err)
			}
			if test.relay {
				messages, err := repo.ListMessages(ctx, thread.ID)
				if err != nil || len(messages) != 2 || messages[1].Kind != "relay" || messages[1].Content != "Include the test log when you answer." {
					t.Fatalf("expected a message to the source discussion: %+v, %v", messages, err)
				}
			}
		})
	}
}
