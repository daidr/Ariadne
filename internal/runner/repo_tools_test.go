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
