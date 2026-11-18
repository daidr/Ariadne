package runner

import (
	"context"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/sdk"
)

var reportKinds = []string{"progress", "waiting", "ready", "blocked", "failed", "done"}

const reportGuide = "Use ready for checked changes ready for review or when the configured external delivery is complete. If the requested delivery ends at a target branch or ticket update, report ready once those requirements are met and note any unrelated CI failures in the summary. Use done for results with no change review needed, waiting for an outstanding external result, and blocked only when a required step cannot proceed. When the user must provide information, approval or an action before work can continue, use post_ui to ask for that specific thing."

func (turn *turn) reportingTools(thread core.ThreadView) []tool {
	if author, ok := thread.ReviewOf.(string); ok && author != "" {
		return []tool{
			specification("read_changes", "List the author's changes. Give a path to see the text changes in one file.",
				args{"path": field("string")}, nil, func(ctx context.Context, input args) (string, error) {
					return turn.readChanges(ctx, author, text(input, "path"))
				}),
			specification("submit_review", "Submit the verdict on the current review round. Add a comment for each issue that needs a change.",
				args{"verdict": args{"type": "string", "enum": []string{"pass", "changes"}}, "summary": field("string"),
					"comments": arrayOf(args{"type": "object", "properties": args{"path": field("string"), "line": field("integer"), "kind": field("string"), "body": field("string")}, "required": []string{"path", "line", "kind", "body"}})},
				[]string{"verdict", "summary"}, func(ctx context.Context, input args) (string, error) {
					return reply(turn.Repository.SubmitReview(ctx, thread.ID, core.ReviewSubmission{Verdict: text(input, "verdict"), Summary: text(input, "summary"), Comments: items(input["comments"])}))
				}),
		}
	}
	return []tool{specification("report", "Report the thread's result to the coordinator. "+reportGuide,
		args{"kind": args{"type": "string", "enum": reportKinds}, "summary": field("string"), "headline": field("string"),
			"artifacts": arrayOf(field("string")), "reviewReplies": arrayOf(args{"type": "object", "properties": args{"comment": field("integer"), "outcome": field("string"), "note": field("string")}})},
		[]string{"kind", "summary", "headline"}, turn.report)}
}

func (turn *turn) report(ctx context.Context, input args) (string, error) {
	kind, summary, headline := text(input, "kind"), text(input, "summary"), text(input, "headline")
	if !slices.Contains(reportKinds, kind) || summary == "" || headline == "" || strings.ContainsAny(headline, "\r\n") || utf8.RuneCountInString(headline) > 60 {
		return "", errors.New("report requires a valid kind, a summary, and a headline of one short line")
	}
	ids, err := stringsOf(input["artifacts"])
	if err != nil {
		return "", err
	}
	thread, err := turn.Repository.GetThread(ctx, turn.conversation.ID)
	if err != nil {
		return "", err
	}
	project, err := turn.Repository.GetProject(ctx, thread.ProjectID)
	if err != nil {
		return "", err
	}
	options := core.ReviewOptions{Replies: items(input["reviewReplies"]), ReviewerTitle: "评审：" + thread.Title}
	if turn.Settings.Locale() == "en" {
		options.ReviewerTitle = "Review " + thread.Title
	}
	var head *string
	if turn.Workspaces != nil && thread.Workspace != nil && (kind == "ready" || thread.Merged != nil) {
		status, err := turn.Workspaces.ThreadChanges(ctx, thread.ID)
		if err == nil && status.Changes != nil {
			head = new(status.Changes.Head)
			if kind == "ready" {
				if len(status.Changes.Files) != 0 {
					options.Reviewable, options.Head = true, head
				} else if len(status.Changes.Uncommitted) != 0 && project.Settings.Delivery.Mode != "external" {
					return "", errors.New("commit the workspace changes before reporting ready")
				}
			}
		}
	}
	if kind == "ready" && !options.Reviewable && project.Settings.Delivery.Mode != "external" {
		pull, _ := thread.PullRequest.(map[string]any)
		branch := thread.Workspace != nil && thread.Workspace.Branch != nil && thread.Workspace.Base != nil
		if !branch && pull["state"] != "open" {
			kind = "done"
		}
	}
	after, err := turn.Repository.ReportThread(ctx, thread.ID, core.ThreadReport{Kind: kind, Summary: summary, Headline: headline, Artifacts: ids}, head, options)
	if err != nil {
		return "", err
	}
	if after.Review != nil && after.Review.Reviewing {
		return "Reported. A reviewer will read the branch. End this turn.", nil
	}
	return "Reported.", nil
}

func (turn *turn) readChanges(ctx context.Context, author, path string) (string, error) {
	if turn.Workspaces == nil {
		return "", errors.New("this project has no branch to review")
	}
	thread, err := turn.Repository.GetThread(ctx, author)
	if err != nil {
		return "", err
	}
	if thread.ProjectID != turn.conversation.ProjectID {
		return "", domainerr.NotFound(author)
	}
	value, err := turn.Workspaces.ThreadChanges(ctx, author)
	if err != nil {
		return "", err
	}
	changes := value.Changes
	if changes == nil {
		return "", errors.New("the author has no branch to review")
	}
	if path == "" {
		return reply(changes, nil)
	}
	for _, file := range changes.Files {
		if file.Path != path {
			continue
		}
		versions, err := turn.Workspaces.ThreadFile(ctx, author, sdk.FileVersionRequest{From: changes.From, Head: changes.Head, Path: path, PreviousPath: file.PreviousPath})
		if err != nil {
			return "", err
		}
		if versions.Skipped != nil {
			return "This file could not be shown: " + *versions.Skipped, nil
		}
		before, after := "", ""
		if versions.Before != nil {
			before = *versions.Before
		}
		if versions.After != nil {
			after = *versions.After
		}
		if diff := core.LineDiff(before, after, 60000); diff != nil {
			return *diff, nil
		}
		return "The file has no text changes.", nil
	}
	return "", errors.New("the path is not in the changes under review")
}
