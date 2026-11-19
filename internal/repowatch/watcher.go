// Package repowatch refreshes open pull requests without keeping an in-memory
// thread index. Fetch failures leave the last observed status intact.
package repowatch

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/workspaces"
	"github.com/daidr/Ariadne/sdk"
)

type Watcher struct {
	Repository *core.Repository
	Repo       func(context.Context, string) (*sdk.RepoCapability, error)
	ThreadRepo func(context.Context, core.ThreadView) (*sdk.RepoCapability, error)
	OnError    func(error)
}

func (watcher Watcher) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			watcher.report(ctx, watcher.Poll(ctx))
		}
	}
}

func (watcher Watcher) report(ctx context.Context, err error) {
	if err == nil || ctx.Err() != nil || watcher.OnError == nil {
		return
	}
	var domain *domainerr.Error
	if errors.As(err, &domain) && (domain.Key == "error.notFound" || domain.Key == "error.noRepo" || domain.Key == "error.noWorkspace") {
		return
	}
	watcher.OnError(err)
}

func (watcher Watcher) Poll(ctx context.Context) error {
	after := ""
	const pageSize = 64
	for {
		page, err := watcher.Repository.OpenPullRequests(ctx, after, pageSize)
		if err != nil {
			return err
		}
		for _, item := range page {
			if err := ctx.Err(); err != nil {
				return err
			}
			watcher.report(ctx, watcher.refresh(ctx, item))
		}
		if len(page) < pageSize {
			return nil
		}
		after = page[len(page)-1].ThreadID
	}
}

func (watcher Watcher) refresh(ctx context.Context, item core.OpenPullRequest) error {
	number, _ := item.Request["number"].(float64)
	if number < 1 || number > math.MaxInt32 || number != math.Trunc(number) {
		return errors.New("stored pull request has an invalid number")
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	var repo *sdk.RepoCapability
	var err error
	if watcher.ThreadRepo != nil {
		thread, readErr := watcher.Repository.GetThread(ctx, item.ThreadID)
		if readErr != nil {
			return readErr
		}
		repo, err = watcher.ThreadRepo(ctx, thread)
	} else {
		repo, err = watcher.Repo(ctx, item.ProjectID)
	}
	if err != nil {
		return err
	}
	if repo == nil || repo.PullRequest == nil || repo.Checks == nil {
		return nil
	}
	info, err := repo.PullRequest(ctx, int(number))
	if err != nil {
		return err
	}
	if info.Number != int(number) || info.HeadSHA == "" ||
		(info.State != "open" && info.State != "merged" && info.State != "closed") {
		return errors.New("repository returned unexpected pull request data")
	}
	checks, err := repo.Checks(ctx, info.HeadSHA)
	if err != nil {
		return err
	}
	next := map[string]any{
		"number": info.Number, "url": info.URL, "title": info.Title,
		"state": info.State, "headSha": info.HeadSHA, "checks": workspaces.SummarizeChecks(checks),
	}
	if info.Reference != "" {
		next["reference"] = info.Reference
	}
	return watcher.Repository.SyncPullRequestIfCurrent(ctx, item.ThreadID, item.Request, next)
}
