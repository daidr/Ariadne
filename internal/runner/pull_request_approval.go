package runner

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/sdk"
)

func (turn *turn) authorizePullRequestMerge(ctx context.Context, project core.ProjectView, threadID string, pr sdk.PullRequestInfo, method string) (string, error) {
	if turn.mergeRequestID != "" {
		explicit, err := turn.hasExplicitMergeRequest(ctx, project, threadID, pr, method)
		if err != nil {
			return "", err
		}
		if explicit {
			return "approved", nil
		}
	}
	identity := fmt.Sprintf("%d\x00%s\x00%s\x00%s\x00%s", pr.Number, pr.URL, pr.HeadSHA, pr.Base, method)
	purpose := fmt.Sprintf("%s%x", core.PullRequestMergeApprovalPrefix, sha256.Sum256([]byte(identity)))
	approval, err := turn.Repository.CallApproval(ctx, project.ID, threadID, purpose, func() args {
		head := pr.HeadSHA[:min(8, len(pr.HeadSHA))]
		title, description := fmt.Sprintf("Merge PR #%d?", pr.Number), fmt.Sprintf("Merge commit %s using %s.", head, method)
		yes, no := "Merge", "Don't merge"
		if turn.Settings != nil && turn.Settings.Locale() != "en" {
			title, description = fmt.Sprintf("合入 PR #%d？", pr.Number), fmt.Sprintf("以 %s 方式合入提交 %s。", method, head)
			yes, no = "合入", "暂不合入"
		}
		return args{"type": "confirm", "title": title, "description": description, "tone": "warning", "approveText": yes, "denyText": no}
	}, core.CallApprovalOptions{InChat: true, ExistingOnly: turn.mergeRequestID == ""})
	if err == nil && approval.State == "missing" {
		return "", errors.New("a user request to merge this pull request is required")
	}
	// An approval can retry the same revision after a transient host error.
	// The host still compares the head when it performs the merge.
	return approval.State, err
}

func (turn *turn) hasExplicitMergeRequest(ctx context.Context, project core.ProjectView, threadID string, pr sdk.PullRequestInfo, method string) (bool, error) {
	message, err := turn.Repository.GetMessage(ctx, turn.mergeRequestID)
	if err != nil {
		return false, err
	}
	if message.Author != "user" || message.ConversationID != threadID && message.ConversationID != project.CoordinatorID {
		return false, nil
	}
	ownThread := threadID
	if message.ConversationID == project.CoordinatorID {
		ownThread = ""
	}
	targets, err := turn.Repository.MergeRequestTargets(ctx, project.ID, ownThread, message)
	if err != nil {
		return false, err
	}
	var matched *core.MergeRequestTarget
	for _, target := range targets {
		if !explicitMergeRequest(message.Content, target.Title, target.Number, method) {
			continue
		}
		if matched != nil {
			return false, nil
		}
		matched = &target
	}
	return matched != nil && matched.ThreadID == threadID &&
		matched.Number == pr.Number && matched.URL == pr.URL && matched.HeadSHA == pr.HeadSHA, nil
}

var mergeClauses = regexp.MustCompile("[,，]")
var mergeDirective = regexp.MustCompile("^(?:可以\\s*|确认\\s*|请\\s*|麻烦\\s*|please\\s+|go ahead and\\s+)?(?:(squash)\\s+)?(?:合入|合并|merge)(?:\\s*(?:(?:pr|mr|pull request)\\s*)?#([0-9]+))?(?:\\s*(?:吧|了|即可))?$")

// Recognize unqualified affirmative commands and a small set of common
// approval phrases. Conditions and other wording use the confirmation card.
func explicitMergeRequest(content, title string, number int, method string) bool {
	content = strings.ToLower(strings.TrimRight(strings.TrimSpace(content), "。.!！"))
	clauses := mergeClauses.Split(content, -1)
	title = strings.Join(strings.Fields(strings.ToLower(title)), "")
	label, _, _ := strings.Cut(strings.ReplaceAll(title, "：", ":"), ":")
	found := false
	for _, part := range clauses {
		clause := strings.Join(strings.Fields(part), " ")
		if found {
			if !(strings.HasPrefix(clause, "然后规划") || strings.HasPrefix(clause, "then plan ")) ||
				strings.Contains(clause, "合") || strings.Contains(clause, "merge") {
				return false
			}
			continue
		}
		if match := mergeDirective.FindStringSubmatch(clause); match != nil {
			if match[2] != "" && match[2] != strconv.Itoa(number) || match[1] != "" && match[1] != method {
				return false
			}
			found = true
			continue
		}
		preamble := strings.ReplaceAll(clause, " ", "")
		if label != "" {
			preamble = strings.TrimPrefix(preamble, label)
		}
		switch preamble {
		case "可以", "好", "好的", "没问题", "没问题了", "看着没问题", "看着没问题了", "ok", "okay", "looks good":
		default:
			return false
		}
	}
	return found
}
