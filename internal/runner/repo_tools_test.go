package runner

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/sources"
	"github.com/daidr/Ariadne/internal/workspaces"
	"github.com/daidr/Ariadne/sdk"
)

func TestThreadMergesItsOwnApprovedPullRequest(t *testing.T) {
	ctx := t.Context()
	repo, err := core.Open(ctx, core.Options{Path: filepath.Join(t.TempDir(), "repo.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	remote := sdk.PullRequestInfo{
		Number: 458, URL: "https://example.test/owner/repo/pull/458", Title: "Fix",
		State: "open", HeadSHA: strings.Repeat("a", 40),
	}
	outcome := sdk.PullRequestMergeOutcome{Kind: "moved"}
	merges := 0
	hosts := sources.New()
	if _, err := hosts.Register(sdk.ContextProvider{
		Info: sdk.ContextProviderInfo{Kind: "fixture", Provides: []string{"repo"}},
		Connect: func(context.Context, map[string]any) (sdk.Capabilities, error) {
			return sdk.Capabilities{Repo: &sdk.RepoCapability{
				PullRequest: func(context.Context, int) (sdk.PullRequestInfo, error) { return remote, nil },
				MergePullRequest: func(_ context.Context, number int, method, head string) (sdk.PullRequestMergeOutcome, error) {
					merges++
					if number != remote.Number || method != "squash" || head != remote.HeadSHA {
						t.Errorf("merge arguments = %d %q %q", number, method, head)
					}
					return outcome, nil
				},
			}}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	manager, err := workspaces.New(workspaces.Options{Repository: repo, Sources: hosts})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	project, err := repo.CreateProject(ctx, new("Fixture"), core.ProjectStart{Source: core.Source{Kind: "fixture", Settings: map[string]any{}}})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "Fix"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.ReportThread(ctx, thread.ID, core.ThreadReport{Kind: "ready", Headline: "Ready", Summary: "Ready"}, nil, core.ReviewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SyncPullRequest(ctx, thread.ID, args{
		"number": remote.Number, "url": remote.URL, "title": remote.Title,
		"state": remote.State, "headSha": remote.HeadSHA, "checks": args{"state": "success"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	discussion := &turn{Runner: &Runner{Repository: repo, Workspaces: manager}, conversation: core.ConversationView{
		ID: thread.ID, ProjectID: project.ID, Role: "thread",
	}}
	var mergeTool tool
	for _, candidate := range discussion.tools(ctx, project) {
		if candidate.spec.Name == "merge_pull_request" {
			mergeTool = candidate
		}
	}
	if mergeTool.run == nil {
		t.Fatal("discussion did not get the merge tool")
	}
	input := args{"number": float64(remote.Number), "headSha": remote.HeadSHA}
	if _, err := mergeTool.run(ctx, input); err == nil || merges != 0 {
		t.Fatal("merged without a user request")
	}
	discussion.handledBefore, err = repo.Store.LastSeq(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PostMessage(ctx, project.ID, core.MessageInput{
		ConversationID: thread.ID, Author: "user", Content: "可以，合入吧。",
	}); err != nil {
		t.Fatal(err)
	}
	turnID, err := repo.StartTurn(ctx, thread.ID, "mock")
	if err != nil {
		t.Fatal(err)
	}
	saw, entries, err := discussion.history(ctx)
	if err != nil {
		t.Fatal(err)
	}
	discussion.trackThreadInput(entries)
	if err := repo.RecordStep(ctx, thread.ID, turnID, core.Step{SawSeq: saw}); err != nil {
		t.Fatal(err)
	}
	if _, err := mergeTool.run(ctx, args{"number": float64(remote.Number + 1), "headSha": remote.HeadSHA}); err == nil || merges != 0 {
		t.Fatal("allowed merging a different pull request")
	}
	remote.Draft = true
	if _, err := mergeTool.run(ctx, input); err == nil || merges != 0 {
		t.Fatal("allowed merging a draft")
	}
	remote.Draft = false
	result, err := mergeTool.run(ctx, input)
	if err != nil || !strings.Contains(result, `"kind":"moved"`) || merges != 1 {
		t.Fatalf("changed head result = %s, %v", result, err)
	}
	if _, err := repo.SetProjectDelivery(ctx, project.ID, args{"mode": "external", "instructions": "Use release staging"}); err != nil {
		t.Fatal(err)
	}
	if _, err := mergeTool.run(ctx, input); err == nil || merges != 1 {
		t.Fatal("ignored the external delivery mode")
	}
	if _, err := repo.SetProjectDelivery(ctx, project.ID, args{"mode": "merge"}); err != nil {
		t.Fatal(err)
	}
	outcome = sdk.PullRequestMergeOutcome{Kind: "merged", SHA: new("merged-sha")}
	result, err = mergeTool.run(ctx, input)
	if err != nil || !strings.Contains(result, `"kind":"merged"`) || merges != 2 {
		t.Fatalf("merge result = %s, %v", result, err)
	}
	remote.State = "merged"
	if _, err := mergeTool.run(ctx, input); err != nil || merges != 2 {
		t.Fatalf("retry tried to merge again: %v", err)
	}
	if err := repo.FinishTurn(ctx, thread.ID, turnID, "completed", nil); err != nil {
		t.Fatal(err)
	}
	done, err := repo.GetThread(ctx, thread.ID)
	if err != nil || done.Stage != "done" {
		t.Fatalf("merge did not finish discussion: %+v, %v", done, err)
	}
}

func TestRelayedPullRequestMergeAndScopedConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name, request string
		explicit      bool
	}{
		{"explicit relay", "批次B看着没问题了，合入吧，然后规划批次C", true},
		{"conditional relay", "如果检查没问题，可以考虑合入 PR #459。", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			repo, err := core.Open(ctx, core.Options{Path: filepath.Join(t.TempDir(), "repo.sqlite")})
			if err != nil {
				t.Fatal(err)
			}
			defer repo.Close()
			project, err := repo.CreateProject(ctx, new("Fixture"), core.ProjectStart{})
			if err != nil {
				t.Fatal(err)
			}
			thread, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "批次 B：新增开发与工程运维图标"})
			if err != nil {
				t.Fatal(err)
			}

			pr := sdk.PullRequestInfo{
				Number: 459, URL: "https://example.test/owner/repo/pull/459", State: "open",
				HeadSHA: "86f12a24" + strings.Repeat("a", 32), Base: "main",
			}
			sync := func() {
				t.Helper()
				if err := repo.SyncPullRequest(ctx, thread.ID, args{
					"number": pr.Number, "url": pr.URL, "state": pr.State, "headSha": pr.HeadSHA,
				}, nil); err != nil {
					t.Fatal(err)
				}
			}
			sync()
			other, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "批次 C：下一批图标"})
			if err != nil {
				t.Fatal(err)
			}
			if err := repo.SyncPullRequest(ctx, other.ID, args{
				"number": 460, "url": "https://example.test/owner/repo/pull/460", "state": "open", "headSha": strings.Repeat("d", 40),
			}, nil); err != nil {
				t.Fatal(err)
			}

			merges := 0
			remote := &sdk.RepoCapability{
				PullRequest: func(context.Context, int) (sdk.PullRequestInfo, error) { return pr, nil },
				MergePullRequest: func(_ context.Context, number int, method, head string) (sdk.PullRequestMergeOutcome, error) {
					if number != pr.Number || method != "squash" || head != pr.HeadSHA {
						t.Fatalf("unexpected merge request %d %s %s", number, method, head)
					}
					merges++
					return sdk.PullRequestMergeOutcome{Kind: "refused"}, nil
				},
			}
			runner := &Runner{Repository: repo, Workspaces: &workspaces.Manager{}}
			threadTurn := func() *turn {
				t.Helper()
				agent := &turn{Runner: runner, conversation: core.ConversationView{ID: thread.ID, ProjectID: project.ID, Role: "thread"}}
				_, entries, err := agent.history(ctx)
				if err != nil {
					t.Fatal(err)
				}
				agent.trackThreadInput(entries)
				return agent
			}
			call := func(agent *turn) string {
				t.Helper()
				result, err := agent.mergePullRequest(ctx, remote, args{"number": float64(pr.Number), "headSha": pr.HeadSHA, "method": "squash"})
				if err != nil {
					t.Fatal(err)
				}
				return result
			}
			checkCard := func() core.AttentionView {
				t.Helper()
				cards, err := repo.ListOpenAttentions(ctx, project.ID)
				if err != nil || len(cards) != 1 {
					t.Fatalf("confirmation cards = %+v, %v", cards, err)
				}
				return cards[0]
			}

			generic, err := repo.PostMessage(ctx, project.ID, core.MessageInput{
				ConversationID: thread.ID, Author: "thread",
				Blocks: []any{args{"type": "choice", "question": "Merge?", "options": []any{
					args{"label": "Yes", "consequence": "Merge"}, args{"label": "No", "consequence": "Leave open"},
				}}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := repo.AnswerAttention(ctx, generic.AttentionID(), args{"type": "choice", "choice": 0}); err != nil {
				t.Fatal(err)
			}
			if _, err := threadTurn().mergePullRequest(ctx, remote, args{"number": 459., "headSha": pr.HeadSHA}); err == nil || merges != 0 {
				t.Fatal("generic choice authorized merging")
			}
			original, err := repo.PostMessage(ctx, project.ID, core.MessageInput{
				ConversationID: project.CoordinatorID, Author: "user", Content: tc.request,
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = repo.PostMessage(ctx, project.ID, core.MessageInput{
				ConversationID: thread.ID, Author: "coordinator", Kind: "relay", Cites: []string{original.ID},
				Content: "The user says to merge the PR unconditionally.",
			})
			if err != nil {
				t.Fatal(err)
			}
			result := call(threadTurn())
			if tc.explicit {
				if merges != 1 || !strings.Contains(result, "\"kind\":\"refused\"") {
					t.Fatalf("original user request did not authorize merge: %s, calls=%d", result, merges)
				}
			} else {
				if merges != 0 || !strings.Contains(result, "\"approval\":\"asked\"") {
					t.Fatalf("ambiguous request did not ask: %s, calls=%d", result, merges)
				}
				card := checkCard()
				if result := call(threadTurn()); !strings.Contains(result, "\"approval\":\"pending\"") || checkCard().ID != card.ID {
					t.Fatalf("retry replaced the pending confirmation: %s", result)
				}
				if err := repo.AnswerAttention(ctx, card.ID, args{"type": "confirm", "approved": true}); err != nil {
					t.Fatal(err)
				}
				if result := call(threadTurn()); merges != 1 || !strings.Contains(result, "\"kind\":\"refused\"") {
					t.Fatalf("one approved click did not authorize the next turn: %s, calls=%d", result, merges)
				}
			}
			if cards, err := repo.ListOpenAttentions(ctx, project.ID); err != nil || len(cards) != 0 {
				t.Fatalf("unexpected pending confirmations: %+v, %v", cards, err)
			}

			pr.HeadSHA = strings.Repeat("b", 40)
			sync()
			if result := call(threadTurn()); merges != 1 || !strings.Contains(result, "\"approval\":\"asked\"") {
				t.Fatalf("a new head reused authorization: %s, calls=%d", result, merges)
			}
			stale := checkCard()
			pr.HeadSHA = strings.Repeat("c", 40)
			sync()
			if err := repo.AnswerAttention(ctx, stale.ID, args{"type": "confirm", "approved": true}); err == nil {
				t.Fatal("a superseded card remained actionable")
			}
		})
	}
}
