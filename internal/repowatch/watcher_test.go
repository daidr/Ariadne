package repowatch

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/domainerr"
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

func TestPollingRunsConcurrentlyWithLimitAndSlowRequestIsolation(t *testing.T) {
	repository, err := core.Open(t.Context(), core.Options{Path: filepath.Join(t.TempDir(), "ariadne.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	project, err := repository.CreateProject(t.Context(), new("Fixture"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	const total = 6
	const limit = 3
	threadIDs := make([]string, total)
	for i := range total {
		thread, err := repository.CreateThread(t.Context(), project.ID, core.NewThread{Title: fmt.Sprintf("PR %d", i+1)})
		if err != nil {
			t.Fatal(err)
		}
		threadIDs[i] = thread.ID
		initial := map[string]any{
			"number":  i + 1,
			"state":   "open",
			"headSha": fmt.Sprintf("old-%d", i+1),
			"checks":  workspaces.SummarizeChecks(nil),
		}
		if err := repository.SyncPullRequest(t.Context(), thread.ID, initial, nil); err != nil {
			t.Fatal(err)
		}
	}

	slowEntered := make(chan struct{})
	releaseSlow := make(chan struct{})
	var fastDone atomic.Int32
	var inFlight atomic.Int32
	var maxInFlight atomic.Int32
	allFastDone := make(chan struct{})

	recordMax := func(cur int32) {
		for {
			prev := maxInFlight.Load()
			if cur <= prev || maxInFlight.CompareAndSwap(prev, cur) {
				return
			}
		}
	}

	watch := Watcher{
		Repository:  repository,
		Concurrency: limit,
		OnError:     func(err error) { t.Error(err) },
		Repo: func(context.Context, string) (*sdk.RepoCapability, error) {
			return &sdk.RepoCapability{
				PullRequest: func(ctx context.Context, number int) (sdk.PullRequestInfo, error) {
					cur := inFlight.Add(1)
					recordMax(cur)
					defer inFlight.Add(-1)
					if number == 1 {
						close(slowEntered)
						select {
						case <-releaseSlow:
						case <-ctx.Done():
							return sdk.PullRequestInfo{}, ctx.Err()
						}
					} else {
						time.Sleep(15 * time.Millisecond)
					}
					return sdk.PullRequestInfo{
						Number:  number,
						State:   "open",
						HeadSHA: fmt.Sprintf("new-%d", number),
					}, nil
				},
				Checks: func(context.Context, string) ([]sdk.CheckInfo, error) {
					return []sdk.CheckInfo{}, nil
				},
			}, nil
		},
	}

	pollErr := make(chan error, 1)
	go func() {
		pollErr <- watch.Poll(t.Context())
	}()

	select {
	case <-slowEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for slow PR request to start")
	}

	// Wait until all other PRs finish syncing even while PR #1 is still blocked.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		syncedFast := 0
		for i := 1; i < total; i++ {
			th, err := repository.GetThread(t.Context(), threadIDs[i])
			if err != nil {
				t.Fatal(err)
			}
			if pr, _ := th.PullRequest.(map[string]any); pr["headSha"] == fmt.Sprintf("new-%d", i+1) {
				syncedFast++
			}
		}
		fastDone.Store(int32(syncedFast))
		if syncedFast == total-1 {
			close(allFastDone)
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case <-allFastDone:
	default:
		close(releaseSlow)
		t.Fatalf("slow PR #1 blocked fast PRs: only %d/%d fast PRs completed", fastDone.Load(), total-1)
	}

	close(releaseSlow)
	if err := <-pollErr; err != nil {
		t.Fatal(err)
	}

	if got := maxInFlight.Load(); got < 2 || got > limit {
		t.Fatalf("max concurrent PR requests = %d, want in [2, %d]", got, limit)
	}
	for i, id := range threadIDs {
		th, err := repository.GetThread(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if pr, _ := th.PullRequest.(map[string]any); pr["headSha"] != fmt.Sprintf("new-%d", i+1) {
			t.Fatalf("thread %s headSha = %v, want new-%d", id, pr["headSha"], i+1)
		}
	}
}

func TestPollingIsolatesErrorsAndSerializesOnError(t *testing.T) {
	repository, err := core.Open(t.Context(), core.Options{Path: filepath.Join(t.TempDir(), "ariadne.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	project, err := repository.CreateProject(t.Context(), new("Fixture"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	threadIDs := make([]string, 5)
	for i := range threadIDs {
		thread, err := repository.CreateThread(t.Context(), project.ID, core.NewThread{Title: fmt.Sprintf("PR %d", i+1)})
		if err != nil {
			t.Fatal(err)
		}
		threadIDs[i] = thread.ID
		initial := map[string]any{
			"number":  i + 1,
			"state":   "open",
			"headSha": "old",
			"checks":  workspaces.SummarizeChecks(nil),
		}
		if err := repository.SyncPullRequest(t.Context(), thread.ID, initial, nil); err != nil {
			t.Fatal(err)
		}
	}

	// Intentionally unsynchronized slice to prove under -race that Watcher serializes OnError.
	var reported []error
	watch := Watcher{
		Repository:  repository,
		Concurrency: 4,
		OnError: func(err error) {
			reported = append(reported, err)
		},
		Repo: func(context.Context, string) (*sdk.RepoCapability, error) {
			return &sdk.RepoCapability{
				PullRequest: func(_ context.Context, number int) (sdk.PullRequestInfo, error) {
					switch number {
					case 2, 4:
						return sdk.PullRequestInfo{}, fmt.Errorf("upstream failed for #%d", number)
					case 3:
						return sdk.PullRequestInfo{}, domainerr.NotFound("ignored")
					default:
						return sdk.PullRequestInfo{Number: number, State: "open", HeadSHA: "updated"}, nil
					}
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
	if len(reported) != 2 {
		t.Fatalf("reported errors = %v (len %d), want 2", reported, len(reported))
	}
	for i, id := range threadIDs {
		th, err := repository.GetThread(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		want := "updated"
		if i+1 == 2 || i+1 == 3 || i+1 == 4 {
			want = "old"
		}
		if pr, _ := th.PullRequest.(map[string]any); pr["headSha"] != want {
			t.Fatalf("PR #%d headSha = %v, want %s", i+1, pr["headSha"], want)
		}
	}
}

func TestPollingRespectsContextCancellation(t *testing.T) {
	repository, err := core.Open(t.Context(), core.Options{Path: filepath.Join(t.TempDir(), "ariadne.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	project, err := repository.CreateProject(t.Context(), new("Fixture"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 6 {
		thread, err := repository.CreateThread(t.Context(), project.ID, core.NewThread{Title: fmt.Sprintf("PR %d", i+1)})
		if err != nil {
			t.Fatal(err)
		}
		initial := map[string]any{
			"number":  i + 1,
			"state":   "open",
			"headSha": "old",
			"checks":  workspaces.SummarizeChecks(nil),
		}
		if err := repository.SyncPullRequest(t.Context(), thread.ID, initial, nil); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started := make(chan struct{}, 6)
	var totalStarted atomic.Int32
	var inFlight atomic.Int32
	var reported atomic.Int32

	watch := Watcher{
		Repository:  repository,
		Concurrency: 2,
		OnError:     func(error) { reported.Add(1) },
		Repo: func(context.Context, string) (*sdk.RepoCapability, error) {
			return &sdk.RepoCapability{
				PullRequest: func(ctx context.Context, number int) (sdk.PullRequestInfo, error) {
					totalStarted.Add(1)
					inFlight.Add(1)
					defer inFlight.Add(-1)
					started <- struct{}{}
					<-ctx.Done()
					return sdk.PullRequestInfo{}, ctx.Err()
				},
				Checks: func(context.Context, string) ([]sdk.CheckInfo, error) {
					return []sdk.CheckInfo{}, nil
				},
			}, nil
		},
	}

	done := make(chan error, 1)
	go func() {
		done <- watch.Poll(ctx)
	}()

	for range 2 {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for initial workers")
		}
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Poll error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Poll did not return promptly after cancellation")
	}
	if got := inFlight.Load(); got != 0 {
		t.Fatalf("in-flight workers after Poll return = %d, want 0", got)
	}
	if got := totalStarted.Load(); got != 2 {
		t.Fatalf("started workers = %d, want 2 (no new dispatches after cancel)", got)
	}
	if got := reported.Load(); got != 0 {
		t.Fatalf("OnError called %d times on cancelled context, want 0", got)
	}
}

func TestPollingMultiplePagesConcurrently(t *testing.T) {
	repository, err := core.Open(t.Context(), core.Options{Path: filepath.Join(t.TempDir(), "ariadne.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	project, err := repository.CreateProject(t.Context(), new("Fixture"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	const total = 70 // exceeds pageSize (64)
	for i := range total {
		thread, err := repository.CreateThread(t.Context(), project.ID, core.NewThread{Title: fmt.Sprintf("PR %d", i+1)})
		if err != nil {
			t.Fatal(err)
		}
		initial := map[string]any{
			"number":  i + 1,
			"state":   "open",
			"headSha": "old",
			"checks":  workspaces.SummarizeChecks(nil),
		}
		if err := repository.SyncPullRequest(t.Context(), thread.ID, initial, nil); err != nil {
			t.Fatal(err)
		}
	}

	var polled atomic.Int32
	var inFlight atomic.Int32
	var maxInFlight atomic.Int32
	watch := Watcher{
		Repository:  repository,
		Concurrency: 4,
		OnError:     func(err error) { t.Error(err) },
		Repo: func(context.Context, string) (*sdk.RepoCapability, error) {
			return &sdk.RepoCapability{
				PullRequest: func(_ context.Context, number int) (sdk.PullRequestInfo, error) {
					cur := inFlight.Add(1)
					for {
						prev := maxInFlight.Load()
						if cur <= prev || maxInFlight.CompareAndSwap(prev, cur) {
							break
						}
					}
					defer inFlight.Add(-1)
					polled.Add(1)
					return sdk.PullRequestInfo{Number: number, State: "open", HeadSHA: "synced"}, nil
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
	if got := polled.Load(); got != total {
		t.Fatalf("polled %d PRs, want %d", got, total)
	}
	if got := maxInFlight.Load(); got > 4 {
		t.Fatalf("max in-flight = %d, want <= 4", got)
	}
}
