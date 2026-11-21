// Package autopilot advances enabled plans and merges the commits that passed review.
package autopilot

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/eventstore"
	"github.com/daidr/Ariadne/internal/workspaces"
	"github.com/daidr/Ariadne/sdk"
)

type Workspaces interface {
	HasRepoForThread(context.Context, core.ThreadView) bool
	RepoForThread(context.Context, core.ThreadView) (*sdk.RepoCapability, error)
	MergeReviewedThread(context.Context, string, string) (workspaces.ThreadMerge, error)
	SyncPullRequest(context.Context, string, *sdk.PullRequestInfo, map[string]any) (map[string]any, error)
}

type Service struct {
	Repository *core.Repository
	Workspaces Workspaces
	Locale     func() string
	Slots      func() int
	OnError    func(error)

	noChecks map[pullHead]time.Time
	now      func() time.Time
}

type pullHead struct{ thread, head string }

func (service *Service) Run(ctx context.Context) {
	wake, stop := service.Repository.Store.Watch()
	defer stop()
	defer func() { service.noChecks = nil }()
	sequence, err := service.Repository.Store.LastSeq(ctx)
	if err != nil {
		service.report(ctx, err)
		return
	}
	service.report(ctx, service.scan(ctx))
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			service.report(ctx, service.scan(ctx))
		case <-wake:
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			service.report(ctx, service.readWakes(ctx, &sequence))
		}
	}
}

func (service *Service) scan(ctx context.Context) error {
	after := ""
	const pageSize = 64
	for {
		page, err := service.Repository.AutopilotProjects(ctx, after, pageSize)
		if err != nil {
			return err
		}
		for _, id := range page {
			service.report(ctx, service.Check(ctx, id))
		}
		if len(page) < pageSize {
			return nil
		}
		after = page[len(page)-1]
	}
}

func (service *Service) readWakes(ctx context.Context, sequence *int64) error {
	for {
		const pageSize = 128
		page, err := service.Repository.Store.Read(ctx, eventstore.ReadOptions{AfterSeq: *sequence, Limit: pageSize, OmitData: true})
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, event := range page {
			*sequence = event.Seq
			if wakes(event.Type) && !seen[event.ProjectID] {
				seen[event.ProjectID] = true
				service.report(ctx, service.Check(ctx, event.ProjectID))
			}
		}
		if len(page) < pageSize {
			return nil
		}
	}
}

func wakes(kind string) bool {
	switch kind {
	case "project.settings.set", "project.autopilot.paused", "thread.reported", "thread.stopped", "thread.resumed",
		"thread.reviewed", "thread.completed", "thread.node.set", "review.submitted", "review.abandoned", "branch.merged", "pull_request.synced",
		"pull_request.closed", "plan.revised", "attention.answered", "attention.withdrawn", "turn.finished":
		return true
	default:
		return false
	}
}

func (service *Service) report(ctx context.Context, err error) {
	if err != nil && ctx.Err() == nil && code(err) != "error.notFound" && service.OnError != nil {
		service.OnError(err)
	}
}

func code(err error) string {
	var known *domainerr.Error
	if errors.As(err, &known) {
		return known.Key
	}
	return ""
}

func enabled(project core.ProjectView) bool {
	return project.Settings.Autopilot.Enabled && !project.AutopilotPaused
}

func (service *Service) Check(ctx context.Context, id string) error {
	repo := service.Repository
	detail, err := repo.GetProjectDetail(ctx, id)
	if err != nil || !enabled(detail.Project) {
		return err
	}
	locale := service.Locale()
	reason := "里程碑已从计划里移除"
	if locale == "en" {
		reason = "The milestone was removed from the plan"
	}
	stale, err := repo.StaleGates(ctx, id)
	if err != nil {
		return err
	}
	for _, idOfGate := range stale {
		if err := repo.WithdrawSystemAttention(ctx, id, idOfGate, reason); err != nil {
			return err
		}
	}
	if detail.Project.Settings.Delivery.Mode != "external" {
		for _, thread := range detail.Threads {
			if thread.Stage != "review" || thread.ReviewOf != nil {
				continue
			}
			mergeContext, cancel := context.WithTimeout(ctx, 45*time.Second)
			service.report(ctx, service.merge(mergeContext, thread, locale))
			cancel()
		}
	}
	return repo.AdvanceAutopilot(ctx, id, service.Slots(), locale)
}

func (service *Service) merge(ctx context.Context, thread core.ThreadView, locale string) error {
	repo := service.Repository
	round, err := repo.PassedReview(ctx, thread.ID)
	if err != nil || round == nil {
		return err
	}
	head, _ := round["head"].(string)
	if thread.AutopilotHeld == head {
		return nil
	}
	conversation, err := repo.GetConversation(ctx, thread.ID)
	if err != nil || conversation.Turn != nil {
		return err
	}
	if head == "" {
		return repo.HoldAutopilot(ctx, thread.ID, nil, "refused", translated(locale, "没有找到讨论串的分支", "The thread’s branch was not found"))
	}
	reason, detail, err := service.mergeReviewed(ctx, thread, head, locale)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		key := code(err)
		switch key {
		case "error.notFound", "error.externalDelivery", "error.merge.working":
			return nil
		case "error.noBranch":
			reason, detail = "refused", translated(locale, "没有找到讨论串的分支", "The thread’s branch was not found")
		case "error.merge.unsupported":
			reason, detail = "refused", translated(locale, "这个工作区不能合并分支，请手动合并", "This workspace cannot merge branches; merge it yourself")
		default:
			reason, detail = "failed", err.Error()
			if strings.HasPrefix(key, "error.merge.") && key != "error.merge.failed" {
				reason = "refused"
			}
		}
	}
	if reason == "" {
		return nil
	}
	return repo.HoldAutopilot(ctx, thread.ID, &head, reason, detail)
}

func (service *Service) mergeReviewed(ctx context.Context, thread core.ThreadView, head, locale string) (string, string, error) {
	if service.Workspaces.HasRepoForThread(ctx, thread) {
		return service.mergePullRequest(ctx, thread, head, locale)
	}
	result, err := service.Workspaces.MergeReviewedThread(ctx, thread.ID, head)
	if err != nil {
		return "", "", err
	}
	switch result.Outcome.Kind {
	case "moved":
		return "moved", "", nil
	case "empty":
		return "refused", translated(locale, "基线已经包含这些改动", "The base already has these changes"), nil
	default:
		return "", "", nil
	}
}

func (service *Service) mergePullRequest(ctx context.Context, thread core.ThreadView, head, locale string) (string, string, error) {
	remote, err := service.Workspaces.RepoForThread(ctx, thread)
	if err != nil {
		return "", "", err
	}
	pr, err := service.Workspaces.SyncPullRequest(ctx, thread.ID, nil, nil)
	if err != nil {
		return "", "", err
	}
	switch pr["state"] {
	case nil, "closed":
		return "no-pull-request", "", nil
	case "merged":
		return "", "", nil
	case "open":
	default:
		return "", "", errors.New("unexpected pull request state")
	}
	if pr["headSha"] != head {
		return "moved", "", nil
	}
	checks, _ := pr["checks"].(map[string]any)
	key := pullHead{thread.ID, head}
	switch checks["state"] {
	case "success":
		delete(service.noChecks, key)
	case "none":
		if service.waitForChecks(key) {
			return "", "", nil
		}
	default:
		delete(service.noChecks, key)
		return "", "", nil
	}
	if remote.MergePullRequest == nil {
		return "refused", translated(locale, "这个来源不能合并 PR，请到托管平台上合并", "This source cannot merge pull requests; merge it on the hosting platform"), nil
	}
	branch, base := "", ""
	if thread.Workspace != nil {
		branch, _ = thread.Workspace.Branch.(string)
		base, _ = thread.Workspace.Base.(string)
	}
	if base == "" && remote.DefaultBranch != nil {
		base, err = remote.DefaultBranch(ctx)
		if err != nil {
			return "", "", err
		}
	}
	project, err := service.Repository.GetProject(ctx, thread.ProjectID)
	if err != nil || !enabled(project) || project.Settings.Delivery.Mode == "external" {
		return "", "", err
	}
	round, err := service.Repository.PassedReview(ctx, thread.ID)
	if err != nil || round["head"] != head {
		return "", "", err
	}
	conversation, err := service.Repository.GetConversation(ctx, thread.ID)
	if err != nil || conversation.Turn != nil {
		return "", "", err
	}
	number, _ := pr["number"].(int)
	if number <= 0 {
		return "", "", errors.New("unexpected pull request number")
	}
	method := project.Settings.Autopilot.Method
	outcome, err := remote.MergePullRequest(ctx, number, method, head)
	if err != nil {
		return "", "", err
	}
	switch outcome.Kind {
	case "merged":
		delete(service.noChecks, key)
		pr["state"] = "merged"
		err = service.Repository.SyncPullRequest(ctx, thread.ID, pr, map[string]any{"method": method, "base": base, "sha": outcome.SHA})
	case "conflict":
		_, err = service.Repository.RecordMergeConflict(ctx, thread.ID, map[string]any{"branch": branch, "base": base, "files": []string{}, "pullRequest": number})
	case "moved":
		return "moved", "", nil
	default:
		return "refused", outcome.Detail, nil
	}
	return "", "", err
}

func (service *Service) waitForChecks(key pullHead) bool {
	now := time.Now()
	if service.now != nil {
		now = service.now()
	}
	if service.noChecks == nil {
		service.noChecks = make(map[pullHead]time.Time)
	}
	// An entry that fell out of the table waits again. Never bypass the grace
	// period when the table is full or a project disappeared during a poll.
	const limit = 4096
	for id, observed := range service.noChecks {
		if now.Sub(observed) > 15*time.Minute {
			delete(service.noChecks, id)
		}
	}
	if since, ok := service.noChecks[key]; ok {
		return now.Sub(since) < time.Minute
	}
	if len(service.noChecks) < limit {
		service.noChecks[key] = now
	}
	return true
}

func translated(locale, chinese, english string) string {
	if locale == "en" {
		return english
	}
	return chinese
}
