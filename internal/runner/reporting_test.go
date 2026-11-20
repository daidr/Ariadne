package runner

import (
	"context"
	"iter"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/models"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/sdk"
)

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
