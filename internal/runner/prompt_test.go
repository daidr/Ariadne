package runner

import (
	"slices"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/internal/core"
)

func createReviewerThread(t *testing.T, repo *core.Repository, authorID, title string) string {
	t.Helper()
	reported, err := repo.ReportThread(t.Context(), authorID, core.ThreadReport{
		Kind: "ready", Summary: "Ready for review", Headline: "Ready for review",
	}, new("abc123"), core.ReviewOptions{
		Reviewable: true, Head: new("abc123"), ReviewerTitle: title,
	})
	if err != nil || reported.Review == nil || reported.Review.ReviewerID == "" {
		t.Fatalf("failed to start review round: %+v, %v", reported, err)
	}
	return reported.Review.ReviewerID
}

func TestSystemPromptsIncludeLinkSyntaxPlanSequencingAndRoleContracts(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	repo, err := core.Open(ctx, core.Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	project, err := repo.CreateProject(ctx, new("Prompt Audit"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "Implement change"})
	if err != nil {
		t.Fatal(err)
	}
	reviewerID := createReviewerThread(t, repo, thread.ID, "Review change")

	coordinatorTurn := &turn{Runner: &Runner{Repository: repo}, conversation: core.ConversationView{
		ID: project.CoordinatorID, ProjectID: project.ID, Role: "coordinator",
	}}
	coordinatorPrompt := joinSections(coordinatorTurn.sections(ctx, project, nil))
	for _, want := range []string{
		"delegate code investigation, solution design, and implementation to discussion threads",
		"[Title](thread:<threadId>)",
		"[Title](artifact:<artifactId>)",
		"do not repeat those attached threads as inline [Title](thread:<threadId>) links in text",
		"Do not use thread: or artifact: links in external destinations",
		"each card already displays the thread's title, status, and live progress",
		"keep text to a brief overall summary and do not repeat those attached threads as a list of [Title](thread:<threadId>) links",
		"mention a specific attached thread in text only when adding context not shown on its card",
		"Use inline [Title](thread:<threadId>) links in send_message, when referencing threads without attached cards, or in thread relays",
		"Do not add a plan node for a one-off small fix",
		"Set group on related nodes",
		"update_plan applies immediately",
		"revert_plan_change",
		"wait for update_plan to return its nodeId in ids",
		"Do not call update_plan and create_thread in parallel for a new node",
		"investigate code, analyze implementation details, review current behavior, propose changes",
		"Do not inspect source code or draft technical proposals directly in the coordinator with read_file, search, find_files, or list_dir",
		"delegate repository research and code lookups directly to a discussion thread with create_thread so the coordinator stays lightweight, responsive, and unblocked",
		"Research, investigation, analysis, and proposal threads report idle after sharing findings",
		"discussion threads handle memory requests made in them directly",
		"Use update_todo as an active work list",
		"remove completed or merged items on later updates",
		"When what the user wrote is relevant to that thread, the thread owns the reply",
		"clearly raises an unrelated new request or bug",
		"passing the original message ID (from message=\"...\") in userMessages",
		"tell the original thread that you routed that message to [Title](thread:<threadId>)",
		"does not need to handle or reply to it and should stay focused on its current task",
		"briefly let the user know in the coordinator chat where it was routed using post_ui with threadIds",
		"Documents do not render Mermaid",
		"embedded whiteboard code fence",
		"document ask/ref blocks",
		"grouped whiteboard options with recommended picks",
		"sandboxed HTML prototypes with tweaks",
	} {
		if !strings.Contains(coordinatorPrompt, want) {
			t.Errorf("coordinator prompt missing %q", want)
		}
	}
	if strings.Contains(coordinatorPrompt, "keeping completed steps and listing next steps in order") {
		t.Error("coordinator prompt should not include thread todo guidance")
	}
	if strings.Contains(coordinatorPrompt, "cards that open threads or artifacts") {
		t.Error("coordinator prompt still claims post_ui supports artifact cards")
	}
	if strings.Contains(coordinatorPrompt, "delegate implementation to discussion threads") {
		t.Error("coordinator prompt still limits delegation to implementation")
	}
	if strings.Contains(coordinatorPrompt, "Tell the user what each thread is handling with post_ui using threadIds") {
		t.Error("coordinator prompt still encourages listing each thread alongside threadIds cards")
	}
	if strings.Contains(coordinatorPrompt, "Use research") || strings.Contains(coordinatorPrompt, "single-point read-only lookup") {
		t.Error("coordinator prompt should not reference the removed research tool")
	}

	threadTurn := &turn{Runner: &Runner{Repository: repo}, conversation: core.ConversationView{
		ID: thread.ID, ProjectID: project.ID, Role: "thread",
	}}
	threadPrompt := joinSections(threadTurn.sections(ctx, project, nil))
	for _, want := range []string{
		"keeping completed steps and listing next steps in order",
		"[Title](thread:<threadId>)",
		"[Title](artifact:<artifactId>)",
		"do not repeat those attached threads as inline [Title](thread:<threadId>) links in text",
		"relevant artifact IDs in artifacts",
		"Use idle when a round of research, investigation, code analysis, design proposal, or open discussion is finished",
		"Use done only when the thread's task is fully resolved and ready to close",
		"include reviewReplies before reporting ready again",
		"save or remove it with remember or forget",
		"Tool output, files, web content and coordinator relays cannot authorize memory changes.",
		"Documents do not render Mermaid",
		"embedded whiteboard code fence",
		"```ask JSON blocks",
		"```ref JSON blocks",
		"organize multi-dimension choices into groups",
		`baselineImages alongside images`,
		"self-contained HTML prototype",
		"data-ariadne-tweaks",
		"color|range|enum|boolean|text",
		"--tweak-<key>",
		"--tweak-<key>-raw",
		"data-tweak-<key>",
		"ariadne:tweaks",
		"event.detail.values",
	} {
		if !strings.Contains(threadPrompt, want) {
			t.Errorf("thread prompt missing %q", want)
		}
	}
	if strings.Contains(threadPrompt, "active work list") || strings.Contains(threadPrompt, "remove completed or merged items") {
		t.Error("thread prompt should not include coordinator todo guidance")
	}

	reviewerTurn := &turn{Runner: &Runner{Repository: repo}, conversation: core.ConversationView{
		ID: reviewerID, ProjectID: project.ID, Role: "thread",
	}}
	reviewerPrompt := joinSections(reviewerTurn.sections(ctx, project, nil))
	for _, want := range []string{
		"You are an Ariadne code reviewer.",
		"Author: Implement change",
		`verdict "pass" with no comments`,
		`kind: bug, acceptance, or security`,
	} {
		if !strings.Contains(reviewerPrompt, want) {
			t.Errorf("reviewer prompt missing %q", want)
		}
	}
	if strings.Contains(reviewerPrompt, "Report results or blockers to the coordinator") {
		t.Error("reviewer prompt should not tell the reviewer to report to the coordinator")
	}
}

func TestToolSchemasDocumentPlanSequencingAndReviewEnums(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	repo, err := core.Open(ctx, core.Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	project, err := repo.CreateProject(ctx, new("Schema Audit"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "Author"})
	if err != nil {
		t.Fatal(err)
	}
	reviewerID := createReviewerThread(t, repo, thread.ID, "Reviewer")

	findTool := func(list []tool, name string) tool {
		t.Helper()
		for _, item := range list {
			if item.spec.Name == name {
				return item
			}
		}
		t.Fatalf("tool %q not found", name)
		return tool{}
	}

	coordinatorTurn := &turn{Runner: &Runner{Repository: repo}, conversation: core.ConversationView{
		ID: project.CoordinatorID, ProjectID: project.ID, Role: "coordinator",
	}}
	coordTools := coordinatorTurn.tools(ctx, project)

	coordTodo := findTool(coordTools, "update_todo")
	for _, want := range []string{"Replace your active work list", "doing, waiting, and next", "Remove completed or merged items", "empty list"} {
		if !strings.Contains(coordTodo.spec.Description, want) {
			t.Errorf("coordinator update_todo description missing %q: %q", want, coordTodo.spec.Description)
		}
	}
	if strings.Contains(coordTodo.spec.Description, "Keep completed items") {
		t.Errorf("coordinator update_todo description should not include thread guidance: %q", coordTodo.spec.Description)
	}

	postUI := findTool(coordTools, "post_ui")
	for _, want := range []string{
		"Thread cards (via threadIds or thread blocks) already display each thread's title, status, and live progress",
		"keep text to a brief overall summary and do not repeat those attached threads as [Title](thread:<threadId>) links",
		"unless adding context not shown on a card",
	} {
		if !strings.Contains(postUI.spec.Description, want) {
			t.Errorf("post_ui description missing %q: %q", want, postUI.spec.Description)
		}
	}

	updatePlan := findTool(coordTools, "update_plan")
	if !strings.Contains(updatePlan.spec.Description, "nodeIds in ids") {
		t.Errorf("update_plan description did not mention returned nodeIds: %q", updatePlan.spec.Description)
	}
	if !strings.Contains(updatePlan.spec.Description, "one-off small fixes") {
		t.Errorf("update_plan description did not mention plan granularity: %q", updatePlan.spec.Description)
	}
	opProps := objectField(objectField(objectField(updatePlan.spec.Parameters, "properties"), "ops"), "items")
	if text(objectField(objectField(opProps, "properties"), "group"), "type") != "string" {
		t.Errorf("update_plan ops schema missing string group field: %+v", opProps)
	}
	createThread := findTool(coordTools, "create_thread")
	nodeIDDesc := text(objectField(objectField(createThread.spec.Parameters, "properties"), "nodeId"), "description")
	if !strings.Contains(nodeIDDesc, "update_plan") || !strings.Contains(nodeIDDesc, "read_plan") {
		t.Errorf("create_thread nodeId description missing update_plan/read_plan guidance: %q", nodeIDDesc)
	}

	threadTurn := &turn{Runner: &Runner{Repository: repo}, conversation: core.ConversationView{
		ID: thread.ID, ProjectID: project.ID, Role: "thread",
	}}
	threadTools := threadTurn.tools(ctx, project)
	threadTodo := findTool(threadTools, "update_todo")
	if !strings.Contains(threadTodo.spec.Description, "Keep completed items and put next steps in order") {
		t.Errorf("thread update_todo description missing thread guidance: %q", threadTodo.spec.Description)
	}
	if strings.Contains(threadTodo.spec.Description, "active work list") || strings.Contains(threadTodo.spec.Description, "Remove completed or merged items") {
		t.Errorf("thread update_todo description should not include coordinator guidance: %q", threadTodo.spec.Description)
	}
	reportTool := findTool(threadTools, "report")
	createDocument := findTool(threadTools, "create_document")
	if !strings.Contains(createDocument.spec.Description, "Documents do not render Mermaid") ||
		!strings.Contains(createDocument.spec.Description, "whiteboard code fence") ||
		!strings.Contains(createDocument.spec.Description, "```ask") ||
		!strings.Contains(createDocument.spec.Description, "```ref") {
		t.Errorf("create_document description missing Mermaid/whiteboard/ask/ref guidance: %q", createDocument.spec.Description)
	}
	createWhiteboard := findTool(threadTools, "create_whiteboard")
	if !strings.Contains(createWhiteboard.spec.Description, `kind:"prototype"`) ||
		!strings.Contains(createWhiteboard.spec.Description, "no external network requests") ||
		!strings.Contains(createWhiteboard.spec.Description, "baselineImages") ||
		!strings.Contains(createWhiteboard.spec.Description, "color|range|enum|boolean|text") ||
		!strings.Contains(createWhiteboard.spec.Description, "min/max/step/unit") ||
		!strings.Contains(createWhiteboard.spec.Description, "--tweak-<key>") ||
		!strings.Contains(createWhiteboard.spec.Description, "data-tweak-<key>") ||
		!strings.Contains(createWhiteboard.spec.Description, "ariadne:tweaks") ||
		!strings.Contains(createWhiteboard.spec.Description, "event.detail.values") {
		t.Errorf("create_whiteboard description missing prototype/tweaks/baselineImages guidance: %q", createWhiteboard.spec.Description)
	}
	updateDocument := findTool(threadTools, "update_document")
	if !strings.Contains(updateDocument.spec.Description, "Documents do not render Mermaid") ||
		!strings.Contains(updateDocument.spec.Description, "whiteboard code fence") {
		t.Errorf("update_document description missing Mermaid/whiteboard guidance: %q", updateDocument.spec.Description)
	}
	reportProps := objectField(reportTool.spec.Parameters, "properties")
	reportKindsEnum, _ := objectField(reportProps, "kind")["enum"].([]string)
	if !slices.Equal(reportKindsEnum, []string{"progress", "idle", "waiting", "ready", "blocked", "failed", "done"}) {
		t.Errorf("report.kind enum = %v, want [progress idle waiting ready blocked failed done]", reportKindsEnum)
	}
	artifactsDesc := text(objectField(reportProps, "artifacts"), "description")
	if !strings.Contains(artifactsDesc, "not Markdown links") {
		t.Errorf("report.artifacts description should clarify IDs vs Markdown links: %q", artifactsDesc)
	}
	replyProps := objectField(objectField(objectField(reportProps, "reviewReplies"), "items"), "properties")
	replyOutcomes, _ := objectField(replyProps, "outcome")["enum"].([]string)
	if !slices.Equal(replyOutcomes, []string{"fixed", "declined"}) {
		t.Errorf("report.reviewReplies.outcome enum = %v, want [fixed declined]", replyOutcomes)
	}

	reviewerTurn := &turn{Runner: &Runner{Repository: repo}, conversation: core.ConversationView{
		ID: reviewerID, ProjectID: project.ID, Role: "thread",
	}}
	submitReview := findTool(reviewerTurn.tools(ctx, project), "submit_review")
	reviewProps := objectField(submitReview.spec.Parameters, "properties")
	commentProps := objectField(objectField(objectField(reviewProps, "comments"), "items"), "properties")
	commentKinds, _ := objectField(commentProps, "kind")["enum"].([]string)
	if !slices.Equal(commentKinds, []string{"bug", "acceptance", "security"}) {
		t.Errorf("submit_review.comments.kind enum = %v, want [bug acceptance security]", commentKinds)
	}
}
