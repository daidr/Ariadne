package autopilot

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/eventstore"
	"github.com/daidr/Ariadne/internal/workspaces"
	"github.com/daidr/Ariadne/sdk"
)

type fakeWorkspaces struct {
	remote *sdk.RepoCapability
	pr     map[string]any
	local  func(string, string) (workspaces.ThreadMerge, error)
}

func (source fakeWorkspaces) HasRepo(context.Context, string) bool { return source.remote != nil }
func (source fakeWorkspaces) Repo(context.Context, string) (*sdk.RepoCapability, error) {
	return source.remote, nil
}
func (source fakeWorkspaces) MergeReviewedThread(_ context.Context, thread, head string) (workspaces.ThreadMerge, error) {
	return source.local(thread, head)
}
func (source fakeWorkspaces) SyncPullRequest(context.Context, string, *sdk.PullRequestInfo, map[string]any) (map[string]any, error) {
	return source.pr, nil
}

func fixture(t *testing.T, source Workspaces) (*Service, core.ProjectView) {
	t.Helper()
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(t.TempDir(), "ariadne.sqlite")})
	require(t, err)
	t.Cleanup(func() { require(t, repo.Close()) })
	project, err := repo.CreateProject(t.Context(), new("Fixture"), core.ProjectStart{})
	require(t, err)
	project, err = repo.SetProjectSettings(t.Context(), project.ID,
		map[string]any{"autopilot": map[string]any{"enabled": true, "method": "merge"}}, nil)
	require(t, err)
	return &Service{Repository: repo, Workspaces: source, Locale: func() string { return "en" },
		Slots: func() int { return 1 }, OnError: func(err error) { t.Error(err) }}, project
}

func require(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestExternalDeliveryAdvancesPlanWithOneMilestoneGateAndNoMerge(t *testing.T) {
	service, project := fixture(t, fakeWorkspaces{
		local: func(string, string) (workspaces.ThreadMerge, error) {
			t.Fatal("autopilot attempted to merge in an external delivery project")
			return workspaces.ThreadMerge{}, nil
		},
	})
	ctx, repo := t.Context(), service.Repository
	// A previously reviewed branch remains in the project, and must be left alone.
	thread := reviewed(t, repo, project.ID)
	_, err := repo.SetProjectDelivery(ctx, project.ID, map[string]any{"mode": "external"})
	require(t, err)
	node := func(id, kind string) map[string]any {
		return map[string]any{"op": "add", "id": id, "kind": kind, "title": id, "description": "", "acceptance": ""}
	}
	_, err = repo.RevisePlan(ctx, project.ID, core.PlanRevision{Author: "user", Reason: "schedule", Ops: []map[string]any{
		node("m", "milestone"), node("a", "task"), node("b", "task"), node("choice", "decision"),
		{"op": "link", "from": "m", "to": "a"},
		{"op": "link", "from": "m", "to": "b"},
	}})
	require(t, err)
	_, err = repo.PostMessage(ctx, project.ID, core.MessageInput{ConversationID: project.CoordinatorID, Author: "user", Content: "Wait for my request"})
	require(t, err)
	require(t, service.Check(ctx, project.ID))
	gates, err := repo.ListOpenAttentions(ctx, project.ID)
	require(t, err)
	if len(gates) != 0 {
		t.Fatal("autopilot stepped in before the coordinator", gates)
	}
	consumeCoordinator(t, repo, project.CoordinatorID)
	require(t, service.Check(ctx, project.ID))
	require(t, service.Check(ctx, project.ID))
	gates, err = repo.ListOpenAttentions(ctx, project.ID)
	require(t, err)
	if len(gates) != 1 {
		t.Fatalf("got %d gates, want one", len(gates))
	}
	require(t, repo.AnswerAttention(ctx, gates[0].ID, map[string]any{"type": "confirm", "approved": true}))
	// The earlier thread occupies the cap. Completion is independent of a merge.
	_, err = repo.CompleteThread(ctx, thread.ID)
	require(t, err)
	coordinator, err := repo.GetConversation(ctx, project.CoordinatorID)
	require(t, err)
	if coordinator.PendingSeq > 0 {
		consumeCoordinator(t, repo, project.CoordinatorID)
	}
	require(t, service.Check(ctx, project.ID))
	require(t, service.Check(ctx, project.ID))
	threads, err := repo.ListThreads(ctx, project.ID)
	require(t, err)
	count := 0
	for _, item := range threads {
		if item.Auto {
			count++
			if item.NodeID != "a" {
				t.Fatal("unexpected plan node", item.NodeID)
			}
		}
	}
	if count != 1 {
		t.Fatalf("started %d auto threads, want one", count)
	}
}

func consumeCoordinator(t *testing.T, repo *core.Repository, id string) {
	t.Helper()
	ctx := t.Context()
	turn, err := repo.StartTurn(ctx, id, "fixture")
	require(t, err)
	seq, err := repo.Store.LastSeq(ctx)
	require(t, err)
	require(t, repo.RecordStep(ctx, id, turn, core.Step{SawSeq: seq}))
	require(t, repo.FinishTurn(ctx, id, turn, "completed", nil))
}

func reviewed(t *testing.T, repo *core.Repository, projectID string) core.ThreadView {
	t.Helper()
	ctx := t.Context()
	thread, err := repo.CreateThread(ctx, projectID, core.NewThread{Title: "Implement change"})
	require(t, err)
	require(t, repo.RecordWorkspace(ctx, thread.ID, "change", new("change"), new("main")))
	thread, err = repo.ReportThread(ctx, thread.ID,
		core.ThreadReport{Kind: "ready", Summary: "implemented", Headline: "Ready"}, new("reviewed-head"), core.ReviewOptions{Reviewable: true})
	require(t, err)
	if thread.Review == nil {
		t.Fatal("review was not opened")
	}
	_, err = repo.SubmitReview(ctx, thread.Review.ReviewerID, core.ReviewSubmission{Verdict: "pass", Summary: "verified", Headline: "Passed"})
	require(t, err)
	return thread
}

func TestPullRequestMergesOnlyReviewedHeadAfterGracePeriod(t *testing.T) {
	source := &fakeWorkspaces{remote: &sdk.RepoCapability{}}
	service, project := fixture(t, source)
	ctx, repo := t.Context(), service.Repository
	thread := reviewed(t, repo, project.ID)
	source.pr = map[string]any{"number": 12, "state": "open", "headSha": "reviewed-head", "checks": workspaces.SummarizeChecks(nil)}
	require(t, repo.SyncPullRequest(ctx, thread.ID, source.pr, nil))
	clock := time.Now()
	service.now = func() time.Time { return clock }
	merges := 0
	source.remote.MergePullRequest = func(_ context.Context, number int, method, sha string) (sdk.PullRequestMergeOutcome, error) {
		merges++
		if number != 12 || method != "merge" || sha != "reviewed-head" {
			t.Fatalf("unexpected merge: %d %s %s", number, method, sha)
		}
		return sdk.PullRequestMergeOutcome{Kind: "merged", SHA: new("merge-sha")}, nil
	}
	require(t, service.Check(ctx, project.ID))
	clock = clock.Add(time.Minute - time.Millisecond)
	require(t, service.Check(ctx, project.ID))
	if merges != 0 {
		t.Fatal("pull request merged during CI grace period")
	}
	clock = clock.Add(time.Millisecond)
	require(t, service.Check(ctx, project.ID))
	require(t, service.Check(ctx, project.ID))
	if merges != 1 {
		t.Fatalf("merge count %d, want one", merges)
	}
	events, err := repo.Store.Read(ctx, eventstore.ReadOptions{ProjectID: project.ID})
	require(t, err)
	for _, event := range events {
		if event.Type == "pull_request.closed" {
			return
		}
	}
	t.Fatal("automatic merge did not persist PR closure")
}
