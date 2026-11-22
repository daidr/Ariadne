package repowatch

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/eventstore"
	"github.com/daidr/Ariadne/internal/workspaces"
	"github.com/daidr/Ariadne/sdk"
)

func TestPollingRecordsFailureAndClosureOnceAndIgnoresStaleResponse(t *testing.T) {
	repository, err := core.Open(t.Context(), core.Options{Path: filepath.Join(t.TempDir(), "ariadne.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	project, err := repository.CreateProject(t.Context(), new("Fixture"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := repository.CreateThread(t.Context(), project.ID, core.NewThread{Title: "Build"})
	if err != nil {
		t.Fatal(err)
	}
	pr := func(number int, state, head string) map[string]any {
		return map[string]any{"number": number, "state": state, "headSha": head, "checks": workspaces.SummarizeChecks(nil)}
	}
	if err := repository.SyncPullRequest(t.Context(), thread.ID, pr(1, "open", "first"), nil); err != nil {
		t.Fatal(err)
	}
	phase := 0
	lookup := 0
	watch := Watcher{
		Repository: repository,
		OnError:    func(err error) { t.Error(err) },
		Repo: func(ctx context.Context, id string) (*sdk.RepoCapability, error) {
			if id != project.ID {
				t.Fatal("polled different project", id)
			}
			return &sdk.RepoCapability{
				PullRequest: func(ctx context.Context, number int) (sdk.PullRequestInfo, error) {
					lookup++
					if phase == 1 {
						if err := repository.SyncPullRequest(ctx, thread.ID, pr(2, "open", "new"), nil); err != nil {
							t.Fatal(err)
						}
					}
					state := "open"
					if phase > 0 {
						state = "merged"
					}
					return sdk.PullRequestInfo{Number: number, State: state, HeadSHA: "first"}, nil
				},
				Checks: func(context.Context, string) ([]sdk.CheckInfo, error) {
					if phase == 0 {
						return []sdk.CheckInfo{{Name: "build", Status: "completed", Conclusion: new("failure")}, {Name: "test", Status: "queued"}}, nil
					}
					return []sdk.CheckInfo{}, nil
				},
			}, nil
		},
	}
	poll := func() {
		t.Helper()
		if err := watch.Poll(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	poll()
	poll()
	phase = 1
	poll()
	current, err := repository.GetThread(t.Context(), thread.ID)
	if err != nil || current.PullRequest.(map[string]any)["headSha"] != "new" {
		t.Fatal("stale poll overwrote a newer pull request", current.PullRequest, err)
	}
	phase = 2
	poll()
	poll()
	events, err := repository.Store.Read(t.Context(), eventstore.ReadOptions{ProjectID: project.ID, Limit: 256})
	if err != nil {
		t.Fatal(err)
	}
	failed, closed := 0, 0
	for _, event := range events {
		switch event.Type {
		case "checks.failed":
			failed++
		case "pull_request.closed":
			closed++
		}
	}
	if failed != 1 || closed != 1 || lookup != 4 {
		t.Fatalf("checks failures %d, closes %d, polls %d", failed, closed, lookup)
	}
}

func TestPollingSyncsAndClearsPullRequestConflict(t *testing.T) {
	repository, err := core.Open(t.Context(), core.Options{Path: filepath.Join(t.TempDir(), "ariadne.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	project, err := repository.CreateProject(t.Context(), new("Fixture"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := repository.CreateThread(t.Context(), project.ID, core.NewThread{Title: "Resolve conflict"})
	if err != nil {
		t.Fatal(err)
	}
	initial := map[string]any{"number": 12, "url": "https://github.com/acme/app/pull/12", "title": "Fix", "state": "open", "headSha": "sha1", "checks": workspaces.SummarizeChecks(nil)}
	if err := repository.SyncPullRequest(t.Context(), thread.ID, initial, nil); err != nil {
		t.Fatal(err)
	}
	conflicted := true
	state := "open"
	watch := Watcher{
		Repository: repository,
		OnError:    func(err error) { t.Error(err) },
		Repo: func(context.Context, string) (*sdk.RepoCapability, error) {
			return &sdk.RepoCapability{
				PullRequest: func(_ context.Context, number int) (sdk.PullRequestInfo, error) {
					return sdk.PullRequestInfo{Number: number, URL: "https://github.com/acme/app/pull/12", Title: "Fix", State: state, Conflicted: conflicted, HeadSHA: "sha1"}, nil
				},
				Checks: func(context.Context, string) ([]sdk.CheckInfo, error) {
					return []sdk.CheckInfo{}, nil
				},
			}, nil
		},
	}
	if err := watch.Poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, err := repository.GetThread(t.Context(), thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pr, _ := got.PullRequest.(map[string]any); pr["conflicted"] != true {
		t.Fatalf("expected conflicted=true after poll, got %#v", got.PullRequest)
	}
	conflicted = false
	if err := watch.Poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, err = repository.GetThread(t.Context(), thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pr, _ := got.PullRequest.(map[string]any); pr["conflicted"] != nil {
		t.Fatalf("expected conflicted cleared after resolution, got %#v", got.PullRequest)
	}
}
