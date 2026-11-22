package runner

import (
	"context"
	"fmt"
	"iter"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/eventstore"
	"github.com/daidr/Ariadne/internal/models"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/sdk"
)

func TestDocumentFeedbackSurvivesAnEmptyModelReply(t *testing.T) {
	for _, respond := range []bool{false, true} {
		t.Run(fmt.Sprint(respond), func(t *testing.T) {
			ctx := t.Context()
			home := t.TempDir()
			now := time.UnixMilli(1900000000000)
			repo, err := core.Open(ctx, core.Options{Path: filepath.Join(home, "test.sqlite"), Now: func() time.Time { return now }})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = repo.Close() })
			project, err := repo.CreateProject(ctx, new("Document feedback"), core.ProjectStart{})
			if err != nil {
				t.Fatal(err)
			}
			thread, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "Draft"})
			if err != nil {
				t.Fatal(err)
			}
			doc, err := repo.CreateArtifact(ctx, project.ID, core.NewArtifact{Title: "Draft", Kind: "document", Content: "Original text.", Author: thread.ID})
			if err != nil {
				t.Fatal(err)
			}
			now = now.Add(time.Second)
			if _, err := repo.CommentOnArtifact(ctx, doc.ID, "user", map[string]any{
				"type": "create", "threadId": "comment_thread", "commentId": "user_comment", "quote": "Original text.", "body": "Where did this come from?",
			}); err != nil {
				t.Fatal(err)
			}
			// An AI edit before the feedback is sent must not hide the comment.
			now = now.Add(time.Second)
			if _, err := repo.ReviseArtifact(ctx, doc.ID, core.ArtifactRevision{Author: thread.ID, BaseVersion: &doc.Version, Content: "Original text. More context."}); err != nil {
				t.Fatal(err)
			}
			candidates, err := repo.FeedbackCandidates(ctx, "", now)
			if err != nil || !slices.Contains(candidates, doc.ID) {
				t.Fatalf("comment missing from scheduled feedback: %v %v", candidates, err)
			}
			feedback, err := repo.SendPendingArtifactFeedback(ctx, doc.ID, nil)
			if err != nil || feedback == nil {
				t.Fatalf("comment was not delivered: %v %v", feedback, err)
			}
			id, err := repo.StartScheduledTurn(ctx, thread.ID, "fixture")
			if err != nil || id == "" {
				t.Fatalf("feedback did not wake the discussion: %q %v", id, err)
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
			requests := 0
			_, err = registry.Register(sdk.ModelProvider{Info: info, Stream: func(_ context.Context, _ map[string]any, request sdk.Request) iter.Seq[sdk.Part] {
				return func(yield func(sdk.Part) bool) {
					requests++
					call := sdk.Part{Type: "tool-call"}
					switch requests {
					case 1:
						if !slices.ContainsFunc(request.Messages, func(message sdk.Message) bool {
							return strings.Contains(message.Content, "Where did this come from?") && strings.Contains(message.Content, "comment_thread")
						}) {
							t.Error("model did not receive the comment")
						}
					case 2:
						last := request.Messages[len(request.Messages)-1].Content
						if !strings.Contains(last, "<artifact_feedback_pending>") || !strings.Contains(last, doc.ID) {
							t.Error("missing targeted reminder")
						}
						if respond {
							call.ID, call.Name = "reply", "comment_artifact"
							call.Arguments = fmt.Sprintf(`{"artifactId":%q,"commentThread":"comment_thread","body":"It comes from our first draft."}`, doc.ID)
						}
					case 3:
						call.ID, call.Name = "finish", "finish_feedback_round"
						call.Arguments = fmt.Sprintf(`{"artifactId":%q,"round":1,"summary":"Answered in the original comment."}`, doc.ID)
					case 4:
					default:
						t.Error("extra model request")
					}
					if call.Name != "" {
						if yield(call) {
							yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
						}
						return
					}
					yield(sdk.Part{Type: "finish", Reason: "stop"})
				}
			}})
			if err != nil {
				t.Fatal(err)
			}
			active := &turn{Runner: &Runner{Repository: repo, Settings: store, Models: registry}, id: id, conversation: conversation}
			if err := active.run(ctx, &settings.ResolvedModel{Provider: info, Model: "fixture", Label: "fixture"}); err != nil {
				t.Fatal(err)
			}
			rounds, err := repo.ListArtifactFeedback(ctx, doc.ID)
			if err != nil || len(rounds) != 1 || (rounds[0]["doneAt"] != nil) != respond {
				t.Fatalf("unexpected feedback result: %v %v", rounds, err)
			}
			wantRequests := 2
			if respond {
				wantRequests = 4
			}
			if requests != wantRequests {
				t.Fatalf("got %d model requests, want %d", requests, wantRequests)
			}
			traceEvents := map[string]int{}
			err = repo.ReadTraceSnapshot(ctx, thread.ID, func(snapshot core.TraceSnapshot) error {
				return snapshot.Events(func(event eventstore.Event) error {
					traceEvents[event.Type]++
					return nil
				})
			})
			if err != nil || traceEvents["artifact.feedback"] != 1 || traceEvents["conversation.feedback_reminder"] != 1 ||
				(traceEvents["artifact.feedback_done"] == 1) != respond {
				t.Fatalf("missing feedback events in diagnostic trace: %v %v", traceEvents, err)
			}
		})
	}
}

func TestUnreportedDiscussionReceivesActionableReminder(t *testing.T) {
	ctx := t.Context()
	home := t.TempDir()
	repo, err := core.Open(ctx, core.Options{Path: filepath.Join(home, "test.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	project, err := repo.CreateProject(ctx, new("Reporting"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "Check progress"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PostMessage(ctx, project.ID, core.MessageInput{ConversationID: thread.ID, Author: "user", Content: "Check the result."}); err != nil {
		t.Fatal(err)
	}
	store, err := settings.Open(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	registry := models.New()
	info := sdk.ModelProviderInfo{Kind: "fixture", Program: true}
	requests := 0
	if _, err := registry.Register(sdk.ModelProvider{Info: info, Stream: func(_ context.Context, _ map[string]any, request sdk.Request) iter.Seq[sdk.Part] {
		return func(yield func(sdk.Part) bool) {
			requests++
			switch requests {
			case 1:
				if !yield(sdk.Part{Type: "text", Text: "The check passed."}) {
					return
				}
			case 2:
				hint := request.Messages[len(request.Messages)-1].Content
				if !strings.Contains(hint, "<not_sent>") || strings.Contains(hint, "otherwise end the turn") {
					t.Errorf("unexpected hint after text-only answer: %q", hint)
				}
			case 3:
				hint := request.Messages[len(request.Messages)-1].Content
				if !strings.Contains(hint, "<report_missing>") || !strings.Contains(hint, "Use done for completed work") {
					t.Errorf("expected an actionable report reminder, got %q", hint)
				}
				if !slices.ContainsFunc(request.Tools, func(tool sdk.Tool) bool { return tool.Name == "report" }) {
					t.Error("the discussion did not receive the report tool")
				}
				if yield(sdk.Part{Type: "tool-call", ID: "report-call", Name: "report", Arguments: `{"kind":"done","headline":"Check passed","summary":"The check passed."}`}) {
					yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
				}
				return
			case 4:
			default:
				t.Error("unexpected extra model request")
			}
			yield(sdk.Part{Type: "finish", Reason: "stop"})
		}
	}}); err != nil {
		t.Fatal(err)
	}
	runner := &Runner{Repository: repo, Settings: store, Models: registry}
	for range 2 {
		id, err := repo.StartScheduledTurn(ctx, thread.ID, "fixture")
		if err != nil || id == "" {
			t.Fatalf("expected a scheduled turn, got %q: %v", id, err)
		}
		conversation, err := repo.GetConversation(ctx, thread.ID)
		if err != nil {
			t.Fatal(err)
		}
		active := &turn{Runner: runner, id: id, conversation: conversation}
		if err := active.run(ctx, &settings.ResolvedModel{Provider: info, Model: "fixture", Label: "fixture"}); err != nil {
			t.Fatal(err)
		}
	}
	result, err := repo.GetThread(ctx, thread.ID)
	if err != nil || result.Stage != "done" || requests != 4 {
		t.Fatalf("expected a completed report after four requests, got %s after %d: %v", result.Stage, requests, err)
	}
}

func TestAgentContextLimitCompactsAndContinuesSameDiscussion(t *testing.T) {
	ctx := t.Context()
	home := t.TempDir()
	repo, err := core.Open(ctx, core.Options{Path: filepath.Join(home, "test.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	project, err := repo.CreateProject(ctx, new("Trace review"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "Summarize the trace"})
	if err != nil {
		t.Fatal(err)
	}
	for range 10 {
		_, err := repo.PostMessage(ctx, project.ID, core.MessageInput{ConversationID: thread.ID, Author: "user", Content: strings.Repeat("earlier trace evidence\n", 100)})
		if err != nil {
			t.Fatal(err)
		}
	}
	id, err := repo.StartScheduledTurn(ctx, thread.ID, "fixture")
	if err != nil || id == "" {
		t.Fatalf("expected a scheduled turn, got %q: %v", id, err)
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
	const summary = "The raw trace is recorded on disk. Its earlier runs showed repeated requests."
	if _, err := registry.Register(sdk.ModelProvider{Info: info, Stream: func(_ context.Context, _ map[string]any, request sdk.Request) iter.Seq[sdk.Part] {
		return func(yield func(sdk.Part) bool) {
			if request.Conversation == "" {
				summaries++
				if yield(sdk.Part{Type: "text", Text: summary}) {
					yield(sdk.Part{Type: "finish", Reason: "stop"})
				}
				return
			}
			if request.Conversation != thread.ID {
				t.Errorf("request moved to a different discussion: %q", request.Conversation)
			}
			requests++
			switch requests {
			case 1:
				yield(sdk.Part{Type: "finish", Reason: "error", Error: "Error 400: The input token count (1000001) exceeds the maximum number of tokens allowed (1000000)."})
			case 2:
				if summaries != 1 || !slices.ContainsFunc(request.Messages, func(msg sdk.Message) bool {
					return strings.Contains(msg.Content, summary)
				}) {
					t.Errorf("expected exactly one summary in the next prompt, got %d summaries", summaries)
				}
				if yield(sdk.Part{Type: "tool-call", ID: "continued-report", Name: "report", Arguments: `{"kind":"done","headline":"Trace reviewed","summary":"The trace report is ready."}`}) {
					yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
				}
			case 3:
				yield(sdk.Part{Type: "finish", Reason: "stop"})
			default:
				t.Error("unexpected extra model request")
				yield(sdk.Part{Type: "finish", Reason: "stop"})
			}
		}
	}}); err != nil {
		t.Fatal(err)
	}
	active := &turn{Runner: &Runner{Repository: repo, Settings: store, Models: registry}, id: id, conversation: conversation}
	if err := active.run(ctx, &settings.ResolvedModel{Provider: info, Model: "fixture", Label: "fixture", ContextTokens: new(32_000.0)}); err != nil {
		t.Fatal(err)
	}
	result, err := repo.GetThread(ctx, thread.ID)
	if err != nil || result.Stage != "done" || requests != 3 || summaries != 1 {
		t.Fatalf("expected one recovered turn in the original discussion, got %s after %d requests and %d summaries: %v", result.Stage, requests, summaries, err)
	}
	page, err := repo.ListTurnEvents(ctx, thread.ID, core.TurnEventOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range page.Events {
		if event.Type == "conversation.model_failed" && strings.Contains(string(event.Data), "exceeds the maximum number of tokens") {
			return
		}
	}
	t.Fatal("the failed request must be in the trace even after recovery")
}

func TestThreadContextUsageUpdatesBeforeFirstVisibleMessage(t *testing.T) {
	ctx := t.Context()
	home := t.TempDir()
	repo, err := core.Open(ctx, core.Options{Path: filepath.Join(home, "test.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	project, err := repo.CreateProject(ctx, new("Context meter"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "Inspect context"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := repo.StartTurn(ctx, thread.ID, "fixture")
	if err != nil || id == "" {
		t.Fatalf("expected turn, got %q: %v", id, err)
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
	requests := 0
	if _, err := registry.Register(sdk.ModelProvider{Info: info, Stream: func(_ context.Context, _ map[string]any, _ sdk.Request) iter.Seq[sdk.Part] {
		return func(yield func(sdk.Part) bool) {
			requests++
			switch requests {
			case 1:
				if !yield(sdk.Part{Type: "tool-call", ID: "todo-1", Name: "update_todo", Arguments: `{"items":[{"label":"Check context","status":"doing"}]}`}) {
					return
				}
				if !yield(sdk.Part{Type: "usage", Input: new(int64(0)), Output: new(int64(0)), ContextInput: new(int64(42000))}) {
					return
				}
				yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
			case 2:
				conv, err := repo.GetConversation(ctx, thread.ID)
				if err != nil {
					t.Errorf("load conversation before first message: %v", err)
				} else if conv.ContextTokens != 42000 {
					t.Errorf("expected contextTokens 42000 after first tool step before first message, got %d", conv.ContextTokens)
				}
				messages, err := repo.ListMessages(ctx, thread.ID)
				if err != nil || len(messages) != 0 {
					t.Errorf("expected no visible messages yet, got %d (err=%v)", len(messages), err)
				}
				if !yield(sdk.Part{Type: "tool-call", ID: "report-1", Name: "report", Arguments: `{"kind":"done","headline":"Checked","summary":"Done."}`}) {
					return
				}
				if !yield(sdk.Part{Type: "usage", Input: new(int64(0)), Output: new(int64(35))}) {
					return
				}
				yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
			case 3:
				conv, err := repo.GetConversation(ctx, thread.ID)
				if err != nil {
					t.Errorf("load conversation after output-only step: %v", err)
				} else if conv.ContextTokens != 42000 {
					t.Errorf("output-only step must preserve contextTokens 42000, got %d", conv.ContextTokens)
				}
				if !yield(sdk.Part{Type: "usage", Input: new(int64(170000)), Output: new(int64(47)), ContextInput: new(int64(55000))}) {
					return
				}
				yield(sdk.Part{Type: "finish", Reason: "stop"})
			default:
				t.Errorf("unexpected extra request %d", requests)
				yield(sdk.Part{Type: "finish", Reason: "stop"})
			}
		}
	}}); err != nil {
		t.Fatal(err)
	}
	active := &turn{Runner: &Runner{Repository: repo, Settings: store, Models: registry}, id: id, conversation: conversation}
	if err := active.run(ctx, &settings.ResolvedModel{Provider: info, Model: "fixture", Label: "fixture", ContextTokens: new(200_000.0)}); err != nil {
		t.Fatal(err)
	}
	conv, err := repo.GetConversation(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if conv.ContextTokens != 55000 {
		t.Fatalf("expected final contextTokens 55000, got %d", conv.ContextTokens)
	}
}

func TestLongTurnCompactsDirectlyWithoutClearingOldToolResults(t *testing.T) {
	ctx := t.Context()
	home := t.TempDir()
	repo, err := core.Open(ctx, core.Options{Path: filepath.Join(home, "test.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	project, err := repo.CreateProject(ctx, new("Append-only context"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "Long tool session"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := repo.StartTurn(ctx, thread.ID, "fixture")
	if err != nil || id == "" {
		t.Fatalf("expected turn, got %q: %v", id, err)
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
	if _, err := registry.Register(sdk.ModelProvider{Info: info, Stream: func(_ context.Context, _ map[string]any, request sdk.Request) iter.Seq[sdk.Part] {
		return func(yield func(sdk.Part) bool) {
			if request.Conversation == "" {
				summaries++
				if yield(sdk.Part{Type: "text", Text: fmt.Sprintf("Compacted summary #%d.", summaries)}) {
					yield(sdk.Part{Type: "finish", Reason: "stop"})
				}
				return
			}
			requests++
			// Report ContextInput above threshold (20000 * 0.6 = 12000) after building
			// enough tool steps so CompactionCut has a valid cut point twice in one turn.
			switch requests {
			case 1, 2:
				if yield(sdk.Part{Type: "tool-call", ID: fmt.Sprintf("todo-%d", requests), Name: "update_todo", Arguments: fmt.Sprintf(`{"items":[{"label":"Step %d","detail":%q,"status":"doing"}]}`, requests, strings.Repeat("x", 9000))}) {
					if requests == 2 {
						yield(sdk.Part{Type: "usage", Input: new(int64(15000)), Output: new(int64(50)), ContextInput: new(int64(15000))})
					}
					yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
				}
			case 3, 4:
				if requests == 3 && summaries != 1 {
					t.Errorf("expected first compaction before step 3, got %d summaries", summaries)
				}
				if yield(sdk.Part{Type: "tool-call", ID: fmt.Sprintf("todo-%d", requests), Name: "update_todo", Arguments: fmt.Sprintf(`{"items":[{"label":"Step %d","detail":%q,"status":"doing"}]}`, requests, strings.Repeat("y", 9000))}) {
					if requests == 4 {
						yield(sdk.Part{Type: "usage", Input: new(int64(16000)), Output: new(int64(50)), ContextInput: new(int64(16000))})
					}
					yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
				}
			case 5:
				if summaries != 2 {
					t.Errorf("expected second compaction before step 5 in the same long turn, got %d summaries", summaries)
				}
				if yield(sdk.Part{Type: "tool-call", ID: "report-done", Name: "report", Arguments: `{"kind":"done","headline":"Finished","summary":"Completed long turn."}`}) {
					yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
				}
			case 6:
				yield(sdk.Part{Type: "finish", Reason: "stop"})
			default:
				t.Errorf("unexpected extra request %d", requests)
				yield(sdk.Part{Type: "finish", Reason: "stop"})
			}
		}
	}}); err != nil {
		t.Fatal(err)
	}
	active := &turn{Runner: &Runner{Repository: repo, Settings: store, Models: registry}, id: id, conversation: conversation}
	if err := active.run(ctx, &settings.ResolvedModel{Provider: info, Model: "fixture", Label: "fixture", ContextTokens: new(20_000.0)}); err != nil {
		t.Fatal(err)
	}
	if summaries != 2 {
		t.Fatalf("expected 2 direct compactions in one turn, got %d", summaries)
	}
	page, err := repo.ListTurnEvents(ctx, thread.ID, core.TurnEventOptions{})
	if err != nil {
		t.Fatal(err)
	}
	compacted := 0
	for _, event := range page.Events {
		if event.Type == "conversation.results_cleared" {
			t.Fatalf("unexpected conversation.results_cleared event: %s", string(event.Data))
		}
		if event.Type == "conversation.compacted" {
			compacted++
		}
	}
	if compacted != 2 {
		t.Fatalf("expected 2 conversation.compacted events, got %d", compacted)
	}
}
