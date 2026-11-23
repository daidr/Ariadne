package runner

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"time"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/transcript"
	"github.com/daidr/Ariadne/sdk"
)

func (turn *turn) repoTools(repo *sdk.RepoCapability, canMerge bool) []tool {
	tools := []tool{
		readOnlySpec("repo_status", "Read the current branch's pull request and the checks for its latest commit.",
			args{}, nil, func(ctx context.Context, _ args) (string, error) { return turn.repoStatus(ctx, repo) }),
		specification("sync_branch", "Fetch remote changes and report how far the workspace branch is ahead or behind.",
			args{}, nil, func(ctx context.Context, _ args) (string, error) { return turn.syncBranch(ctx, repo) }),
		specification("push_branch", "Push the commits on your workspace branch. A public repository requires the user's approval.",
			args{}, nil, func(ctx context.Context, _ args) (string, error) { return turn.pushBranch(ctx, repo) }),
		specification("open_pull_request", "Push your branch and open or return its pull request. Provide a title, a description and how you checked your work.",
			args{"title": field("string"), "body": field("string"), "draft": field("boolean")}, []string{"title", "body"},
			func(ctx context.Context, input args) (string, error) { return turn.openPullRequest(ctx, repo, input) }),
	}
	if canMerge && repo.PullRequest != nil && repo.MergePullRequest != nil {
		tools = append(tools, specification("merge_pull_request",
			"Merge this discussion's pull request when the user wants it merged, including when the coordinator relays the original request. Call repo_status first and pass the PR number and full head SHA. This tool checks authorization and opens its own confirmation when needed. If waiting for approval, end the turn without another question. After approval retry the same PR, head and method. If declined, do not retry. Report the outcome to the coordinator.",
			args{"number": field("integer"), "headSha": field("string"), "method": args{"type": "string", "enum": []string{"merge", "squash"}}},
			[]string{"number", "headSha"}, func(ctx context.Context, input args) (string, error) {
				return turn.mergePullRequest(ctx, repo, input)
			}))
	}
	if repo.CheckLog != nil {
		tools = append(tools, readOnlySpec("read_check_log", "Read the end of a CI check log using its ID from repo_status.",
			args{"checkId": field("string")}, []string{"checkId"}, func(ctx context.Context, input args) (string, error) {
				log, err := repo.CheckLog(ctx, text(input, "checkId"))
				if err != nil {
					return "", err
				}
				return transcript.Fenced("check_log", log, "").Text, nil
			}))
	}
	return tools
}

func (turn *turn) mergePullRequest(ctx context.Context, repo *sdk.RepoCapability, input args) (string, error) {
	thread, err := turn.editable(ctx)
	if err != nil {
		return "", err
	}
	project, err := turn.Repository.GetProject(ctx, thread.ProjectID)
	if err != nil {
		return "", err
	}
	if project.Settings.Delivery.Mode == "external" {
		return "", domainerr.New("error.externalDelivery", nil)
	}
	known, _ := thread.PullRequest.(map[string]any)
	number := int(asNumber(known["number"]))
	head := text(input, "headSha")
	if number <= 0 || asNumber(input["number"]) != float64(number) || head == "" || known["headSha"] != head {
		return "", errors.New("use repo_status to get this discussion's pull request number and current head SHA")
	}
	current, err := repo.PullRequest(ctx, number)
	if err != nil {
		return "", err
	}
	if current.Number != number || current.HeadSHA != head || text(known, "url") != current.URL {
		return "", errors.New("the pull request changed. Check its current state before merging")
	}
	if current.State == "merged" {
		return turn.recordPullRequestMerge(ctx, thread.ID, known, current, sdk.PullRequestMergeOutcome{Kind: "merged"})
	}
	if current.State != "open" || current.Draft {
		return "", errors.New("this pull request must be open and ready for review to merge")
	}
	project, err = turn.Repository.GetProject(ctx, thread.ProjectID)
	if err != nil {
		return "", err
	}
	if project.Settings.Delivery.Mode == "external" {
		return "", domainerr.New("error.externalDelivery", nil)
	}
	method := text(input, "method")
	if method == "" {
		method = project.Settings.Autopilot.Method
	}
	if method != "merge" && method != "squash" {
		return "", errors.New("merge method must be merge or squash")
	}
	if state, err := turn.authorizePullRequestMerge(ctx, project, thread.ID, current, method); err != nil || state != "approved" {
		return reply(args{"number": number, "headSha": head, "method": method, "approval": state}, err)
	}
	outcome, err := repo.MergePullRequest(ctx, number, method, head)
	if err != nil {
		return "", err
	}
	if outcome.Kind == "merged" {
		return turn.recordPullRequestMerge(ctx, thread.ID, known, current, outcome)
	}
	return reply(args{"number": number, "url": current.URL, "outcome": outcome}, nil)
}

func (turn *turn) recordPullRequestMerge(ctx context.Context, threadID string, previous args, current sdk.PullRequestInfo, outcome sdk.PullRequestMergeOutcome) (string, error) {
	next := maps.Clone(previous)
	next["state"] = "merged"
	next["title"] = current.Title
	delete(next, "conflicted")
	if err := turn.Repository.SyncPullRequestIfCurrent(ctx, threadID, previous, next); err != nil {
		return "", fmt.Errorf("the host merged PR #%d, but the discussion status could not be updated: %w", current.Number, err)
	}
	return reply(args{"number": current.Number, "url": current.URL, "outcome": outcome}, nil)
}

func (turn *turn) repoWorkspace(ctx context.Context) (core.ThreadView, sdk.Workspace, string, error) {
	thread, err := turn.editable(ctx)
	if err != nil {
		return thread, nil, "", err
	}
	workspace, err := turn.Workspaces.ForThread(ctx, thread.ProjectID, thread.ID)
	if err != nil {
		return thread, nil, "", err
	}
	turn.changes.begin(ctx, thread)
	if thread.Workspace == nil {
		turn.instructionsDirty.Store(true)
	}
	branch := workspace.Info().Branch
	if branch == nil || *branch == "" {
		return thread, nil, "", domainerr.New("error.noRepo", nil)
	}
	return thread, workspace, *branch, nil
}

func (turn *turn) repoStatus(ctx context.Context, repo *sdk.RepoCapability) (string, error) {
	thread, _, branch, err := turn.repoWorkspace(ctx)
	if err != nil {
		return "", err
	}
	if repo.DefaultBranch == nil {
		return "", domainerr.New("error.noRepo", nil)
	}
	pr, err := turn.Workspaces.SyncPullRequest(ctx, thread.ID, nil, nil)
	if err != nil {
		return "", err
	}
	base, err := repo.DefaultBranch(ctx)
	if err != nil {
		return "", err
	}
	checks := []sdk.CheckInfo{}
	if sha := text(pr, "headSha"); sha != "" && repo.Checks != nil {
		checks, err = repo.Checks(ctx, sha)
	}
	return reply(args{"repository": repo.Remote, "branch": branch, "defaultBranch": base, "pullRequest": pr, "checks": checks}, err)
}

func (turn *turn) syncBranch(ctx context.Context, repo *sdk.RepoCapability) (string, error) {
	thread, workspace, branch, err := turn.repoWorkspace(ctx)
	if err != nil {
		return "", err
	}
	if repo.Fetch == nil || repo.DefaultBranch == nil {
		return "", domainerr.New("error.noRepo", nil)
	}
	if err := repo.Fetch(ctx); err != nil {
		return "", err
	}
	base, err := repo.DefaultBranch(ctx)
	if err != nil {
		return "", err
	}
	count := func(target string) args {
		out, err := workspace.ExecFile(ctx, []string{"git", "rev-list", "--left-right", "--count", "HEAD..." + target, "--"},
			sdk.ExecOptions{Timeout: 10 * time.Second})
		fields := strings.Fields(out.Stdout)
		if err != nil || out.ExitCode != 0 || len(fields) != 2 {
			return nil
		}
		ahead, errA := strconv.Atoi(fields[0])
		behind, errB := strconv.Atoi(fields[1])
		if errA != nil || errB != nil {
			return nil
		}
		return args{"ahead": ahead, "behind": behind}
	}
	target := "origin/" + base
	comparison := count(target)
	if comparison == nil {
		comparison = args{}
	}
	comparison["name"] = target
	result := args{"branch": branch, "defaultBranch": comparison, "remoteBranch": count("origin/" + branch)}
	if previous, _ := thread.PullRequest.(args); previous["state"] == "merged" {
		result["hint"] = "The last pull request was merged. Incorporate " + target + " before preparing the next one."
	}
	return reply(result, nil)
}

func (turn *turn) permitPush(ctx context.Context, thread core.ThreadView, repo *sdk.RepoCapability) (bool, string, error) {
	if repo.Push == nil || repo.IsPublic == nil {
		return false, "", domainerr.New("error.noRepo", nil)
	}
	public, err := repo.IsPublic(ctx)
	if err != nil || !public {
		return err == nil, "", err
	}
	state, err := turn.Repository.RequestApproval(ctx, thread.ProjectID, thread.ID, "push-public:"+repo.Remote, func() args {
		title, description, yes, no := "Allow pushing to "+repo.Remote+"?", "A push publishes the commits to this public repository.", "Allow push", "Keep locally"
		if turn.Settings.Locale() != "en" {
			title, description, yes, no = "允许推送到 "+repo.Remote+" 吗？", "推送会把这些提交发布到公开仓库。", "允许推送", "保留在本地"
		}
		return args{"type": "confirm", "title": title, "description": description, "tone": "warning", "approveText": yes, "denyText": no}
	})
	if err != nil {
		return false, "", err
	}
	switch state {
	case "approved":
		return true, "", nil
	case "declined":
		return false, "", errors.New("the user declined the public push")
	default:
		return false, "Waiting for the user's approval to push to the public repository.", nil
	}
}

func (turn *turn) pushBranch(ctx context.Context, repo *sdk.RepoCapability) (string, error) {
	thread, _, branch, err := turn.repoWorkspace(ctx)
	if err != nil {
		return "", err
	}
	ok, note, err := turn.permitPush(ctx, thread, repo)
	if !ok {
		return note, err
	}
	head, err := repo.Push(ctx, branch)
	return reply(args{"branch": branch, "sha": head}, err)
}

func (turn *turn) openPullRequest(ctx context.Context, repo *sdk.RepoCapability, input args) (string, error) {
	project, err := turn.Repository.GetProject(ctx, turn.conversation.ProjectID)
	if err != nil {
		return "", err
	}
	if project.Settings.Delivery.Mode == "external" {
		return "", domainerr.New("error.externalDelivery", nil)
	}
	if repo.PullRequestFor == nil || repo.OpenPullRequest == nil {
		return "", domainerr.New("error.noRepo", nil)
	}
	title := text(input, "title")
	if title == "" {
		return "", errors.New("pull request title is required")
	}
	thread, _, branch, err := turn.repoWorkspace(ctx)
	if err != nil {
		return "", err
	}
	existing, err := repo.PullRequestFor(ctx, branch)
	if err != nil {
		return "", err
	}
	if existing != nil && existing.State == "closed" {
		return "", errors.New("the previous pull request closed without merging. Ask the user before opening another")
	}
	ok, note, err := turn.permitPush(ctx, thread, repo)
	if !ok {
		return note, err
	}
	// Check again after the potential wait for an approval or repository lookup.
	project, err = turn.Repository.GetProject(ctx, project.ID)
	if err != nil {
		return "", err
	}
	if project.Settings.Delivery.Mode == "external" {
		return "", domainerr.New("error.externalDelivery", nil)
	}
	if _, err := repo.Push(ctx, branch); err != nil {
		return "", err
	}
	already := existing != nil && existing.State == "open"
	var pr sdk.PullRequestInfo
	if already {
		if repo.PullRequest != nil {
			pr, err = repo.PullRequest(ctx, existing.Number)
		} else {
			pr = *existing
		}
	} else {
		draft, _ := input["draft"].(bool)
		pr, err = repo.OpenPullRequest(ctx, sdk.OpenPullRequest{Branch: branch, Title: title, Body: text(input, "body"), Draft: draft})
	}
	if err != nil {
		return "", err
	}
	if _, err := turn.Workspaces.SyncPullRequest(ctx, thread.ID, &pr, nil); err != nil {
		return "", err
	}
	ref := pr.Reference
	if ref == "" {
		ref = fmt.Sprintf("#%d", pr.Number)
	}
	return reply(args{"number": pr.Number, "reference": ref, "url": pr.URL, "state": pr.State, "alreadyOpen": already}, nil)
}
