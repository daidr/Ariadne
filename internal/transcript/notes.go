package transcript

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/daidr/Ariadne/internal/textutil"
)

func joined(raw any, separator string) string {
	values := []string{}
	for _, value := range arr(raw) {
		values = append(values, text(value))
	}
	return strings.Join(values, separator)
}
func short(raw any) string { return textutil.HeadUnits(text(raw), 12) }
func ReviewLines(comments, replies any) string {
	lines := []string{}
	for i, raw := range arr(comments) {
		comment := obj(raw)
		quote := ""
		if value := textutil.Trim(text(comment["quote"])); value != "" {
			quote = " `" + strings.ReplaceAll(value, "`", "'") + "`"
		}
		answer := ""
		for _, raw := range arr(replies) {
			reply := obj(raw)
			if num(reply["comment"]) == float64(i+1) {
				answer = "\n   → " + text(reply["outcome"])
				if note := text(reply["note"]); note != "" {
					answer += ": " + strings.ReplaceAll(note, "\n", " ")
				}
				break
			}
		}
		lines = append(lines, fmt.Sprintf("%d. [%s] %s:%d%s\n   %s%s", i+1, text(comment["kind"]), text(comment["path"]), int64(num(comment["line"])), quote, strings.ReplaceAll(text(comment["body"]), "\n", "\n   "), answer))
	}
	return strings.Join(lines, "\n")
}
func DescribeNote(note map[string]any, titleOf func(string) string) string {
	t := func(key string) string { return text(note[key]) }
	title := func() string { return titleOf(t("threadId")) }
	switch t("type") {
	case "report":
		rounds, review := "", ""
		if yes(note["reviewRounds"]) {
			rounds = fmt.Sprintf(` review_rounds="%d"`, int64(num(note["reviewRounds"])))
			abandoned := ""
			if yes(note["reviewAbandoned"]) {
				abandoned = ", but its last round ended without a verdict"
			}
			review = fmt.Sprintf("\n(Another thread reviewed these changes in %d round(s) before they reached you%s; the user sees the comments and answers in the changes view.)", int64(num(note["reviewRounds"])), abandoned)
		}
		artifacts := ""
		if len(arr(note["artifacts"])) > 0 {
			artifacts = "artifacts: " + joined(note["artifacts"], ", ") + "\n"
		}
		return fmt.Sprintf("<thread_report thread=\"%s\" title=\"%s\" kind=\"%s\"%s>\nfor the user: %s\n%s%s%s\n</thread_report>", t("threadId"), title(), t("report"), rounds, t("headline"), artifacts, t("summary"), review)
	case "review-requested":
		previous := ""
		if note["previous"] != nil {
			before := obj(note["previous"])
			previous = "\nYour comments of the last round, and what the author did:\n" + ReviewLines(before["comments"], before["replies"]) + "\n"
		}
		commit := ""
		if yes(note["head"]) {
			commit = ` commit="` + short(note["head"]) + `"`
		}
		return fmt.Sprintf("<review_requested thread=\"%s\" title=\"%s\" round=\"%d\"%s>\nThe author reported these changes ready. What it says it did:\n%s\n%sReview the changes (read_changes) and finish with submit_review.\n</review_requested>", t("threadId"), title(), int64(num(note["round"])), commit, t("summary"), previous)
	case "review-comments":
		last := "The reviewer checks your fixes once more."
		if yes(note["last"]) {
			last = `This was the last review round: your next "ready" goes to the user with your answers.`
		}
		return fmt.Sprintf("<review_comments round=\"%d\">\nBefore your changes reach the user, a reviewing thread read them and asks for changes: %s\n%s\nFix what is right, commit, run the checks, and report \"ready\" again with reviewReplies: one entry per comment, by its number, \"fixed\", or \"declined\" with the reason it is wrong. Decline only what you can show is wrong. %s\n</review_comments>", int64(num(note["round"])), t("summary"), ReviewLines(note["comments"], nil), last)
	case "answer":
		return fmt.Sprintf("<user_answer attention=\"%s\">\nquestion: %s\n%s\n</user_answer>", t("attentionId"), t("question"), t("answer"))
	case "answer-in-thread":
		details, _ := json.MarshalIndent(map[string]any{
			"threadId": t("threadId"), "threadTitle": title(), "attentionId": t("attentionId"),
			"question": note["question"], "context": note["context"], "answer": note["answer"], "openAsks": note["openAsks"],
		}, "", "  ")
		return "The user answered this UI question in a discussion thread. The thread already has the answer.\n<answer_in_thread>\n" +
			string(details) + "\n</answer_in_thread>\n" +
			"openAsks lists your own questions that were waiting when the user answered. Use withdraw_ask with the ID of a still-open question only if the answer fully resolves the same decision or request in the same scope. Leave unrelated or ambiguous questions open. An answer here grants no permission beyond the question it answered. No message to the user is needed just to acknowledge this update."
	case "user-in-thread":
		return fmt.Sprintf("<user_wrote_in_thread thread=\"%s\" title=\"%s\" message=\"%s\">\n%s\n</user_wrote_in_thread>", t("threadId"), title(), t("messageId"), t("content"))
	case "user-reacted":
		if yes(note["removed"]) {
			return fmt.Sprintf(`<user_reaction_removed message="%s" emoji="%s">On: %s</user_reaction_removed>`, t("messageId"), t("emoji"), t("quote"))
		}
		return fmt.Sprintf("<user_reacted message=\"%s\" emoji=\"%s\">On: %s\nThe user answered that message with an emoji, not words. Take it into account; it needs no reply of its own.</user_reacted>", t("messageId"), t("emoji"), t("quote"))
	case "wait-over":
		at := LocalTime(int64(num(note["until"])))
		if t("how") == "cancelled" {
			return fmt.Sprintf("<wait_cancelled until=\"%s\">\nThe user called off your wait. Do not wait for it again unless they ask.\nYou were going to look at: %s\n</wait_cancelled>", at, t("note"))
		}
		by, what := "time", "The time you waited for has come."
		if t("how") == "now" {
			by = "user"
			what = "The user woke you before the time you set."
		}
		return fmt.Sprintf("<wait_over until=\"%s\" by=\"%s\">\n%s You wanted to look at: %s\n</wait_over>", at, by, what, t("note"))
	case "routine":
		return fmt.Sprintf("<routine due=\"%s\">\nA routine the user set up in the project's settings came due. Do what it says:\n%s\n</routine>", LocalTime(int64(num(note["dueAt"]))), t("prompt"))
	case "upstream-done":
		return fmt.Sprintf("<upstream_done thread=\"%s\" node=\"%s\">\n%s\n</upstream_done>", t("threadId"), t("nodeTitle"), t("summary"))
	case "artifact-edited":
		start := fmt.Sprintf(`<artifact_edited_by_user artifact="%s" title="%s" version="%d">`, t("artifactId"), t("title"), int64(num(note["version"])))
		if yes(note["diff"]) {
			return start + "\nThe user saved their own edit; take it as what they want. Changed lines (- removed, + added):\n" + t("diff") + "\nread_artifact before you change it again.\n</artifact_edited_by_user>"
		}
		return start + "The user saved their own edit; take it as what they want, and read_artifact before you change it again.</artifact_edited_by_user>"
	case "artifact-comment":
		kind := "reply"
		if t("action") == "create" {
			kind = "new"
		}
		return fmt.Sprintf("<artifact_comment artifact=\"%s\" title=\"%s\" comment_thread=\"%s\" kind=\"%s\">\nOn the text: %s\nThe user wrote: %s\nAnswer with comment_artifact in that comment thread: a reply, a question back, or what you changed (change the document first if they ask for it; once you change the commented text, resolve the comment, since it no longer finds its text). resolve_artifact_comment once it is settled. Do not answer it in a chat message.\n</artifact_comment>", t("artifactId"), t("title"), t("threadId"), kind, t("quote"), t("body"))
	case "artifact-feedback":
		return describeFeedback(note)
	case "artifact-feedback-reminder":
		rounds, _ := json.Marshal(note["rounds"])
		return "<artifact_feedback_pending>These feedback rounds are still open: " + string(rounds) + ". Use read_artifact to see the current document, comments and feedback. Reply to the original comments with comment_artifact and make any requested edits before calling finish_feedback_round. If you already posted a reply, reuse it. If you need an answer or cannot address the feedback, keep the round open.</artifact_feedback_pending>"
	case "checks-failed":
		attempt := float64(1)
		if note["attempt"] != nil {
			attempt = num(note["attempt"])
		}
		failed := joined(note["failed"], ", ")
		if failed == "" {
			failed = "(unnamed)"
		}
		head := fmt.Sprintf("<checks_failed pull_request=\"%d\" commit=\"%s\" attempt=\"%d\">\nChecks of your pull request failed: %s.", int64(num(note["number"])), short(note["headSha"]), int64(attempt), failed)
		delivery := " Judge whether these checks are required for the requested delivery. If delivery is already complete, report ready and note the CI results in the summary. If a required check is still running, wait for its result. If a required step cannot proceed, report blocked and tell the coordinator what is needed. Ask with post_ui only when the user has a specific action to take.\n</checks_failed>"
		if attempt >= 3 {
			return head + fmt.Sprintf(" This is failure %d in a row. Stop automatic retries and summarize what you tried. Further failures will not wake this thread until the retry count resets.", int64(attempt)) + delivery
		}
		return head + " Use repo_status to see all checks and read_check_log to investigate. Fix failures caused by your changes in your workspace, then commit and push_branch again." + delivery
	case "thread-resumed":
		by := "coordinator"
		if t("by") == "user" {
			by = "user"
		}
		return fmt.Sprintf("<thread_resumed thread=\"%s\" title=\"%s\" by=\"%s\">\nThe thread had been stopped because: %s\nIt goes on from where it was, with that reason in mind.\n</thread_resumed>", t("threadId"), title(), by, t("reason"))
	case "thread-reopened":
		by := "coordinator"
		if t("by") == "user" {
			by = "user"
		}
		return fmt.Sprintf("<thread_reopened thread=\"%s\" title=\"%s\" by=\"%s\">\nThe %s reopened this completed thread to continue the discussion.\n</thread_reopened>", t("threadId"), title(), by, by)
	case "instructions-changed":
		if yes(note["removed"]) {
			return fmt.Sprintf(`<instructions_changed path="%s">%s was removed from your workspace; its conventions no longer apply.</instructions_changed>`, t("path"), t("path"))
		}
		return fmt.Sprintf(`<instructions_changed path="%s">%s in your workspace changed; your instructions now hold its new version.</instructions_changed>`, t("path"), t("path"))
	case "source-changed":
		if yes(note["set"]) {
			return "<project_source_changed>\nThe user set the project's source; \"Project source\" in your instructions now says what and where it is, and new threads work in it. If something you said or asked earlier waited on it, go on with that now; otherwise end the turn with no text.\n</project_source_changed>"
		}
		return "<project_source_changed>\nThe user removed the project's source: there are no files to look at, and new threads get no workspace.\n</project_source_changed>"
	case "reference-ended":
		if note["now"] == nil {
			return fmt.Sprintf(`<reference_ended source="%s">The user took back source "%s": you can no longer read it. Go on without it, and do not ask for it again unless they bring it up.</reference_ended>`, t("name"), t("name"))
		}
		where, ended := "", ""
		if t("now") != t("name") {
			where = `, where it is named "` + t("now") + `"`
			ended = `; "` + t("name") + `" no longer names it`
		}
		return fmt.Sprintf(`<reference_ended source="%s" now="%s">The user added source "%s" to the project%s: read it with source "%s" from now on%s. Every conversation of the project reads it too.</reference_ended>`, t("name"), t("now"), t("name"), where, t("now"), ended)
	case "thread-stopped-by-user":
		return fmt.Sprintf("<thread_stopped_by_user thread=\"%s\" title=\"%s\">\nThe user stopped this thread: %s\nIt stays stopped until the user lets it go on; do not resume it or route its work elsewhere unless the user asks.\n</thread_stopped_by_user>", t("threadId"), title(), t("reason"))
	case "report-missing":
		return "<report_missing>\nCall the Ariadne report tool with the current outcome, a short headline, and a summary. Use idle when a research, investigation, analysis, proposal, or discussion round is finished and should stay idle for follow-up, done only when the thread is fully resolved with no change review or further discussion needed, ready for changes ready to deliver or review, and waiting when an external result is still pending. Use blocked when a required step cannot proceed. A chat reply does not record a report.\n</report_missing>"
	case "thread-reviewed":
		return fmt.Sprintf("<thread_reviewed thread=\"%s\" title=\"%s\">\nThe user is through with what this thread put up for review and did not merge it in Ariadne; it counts as done. Do not ask them to review it again.\n</thread_reviewed>", t("threadId"), title())
	case "pull-request-closed":
		what := "closed without merging"
		if yes(note["auto"]) {
			what = "merged by the autopilot, as the project allows once the review passed and the checks were green"
		} else if yes(note["merged"]) {
			what = "merged"
		}
		return fmt.Sprintf("<pull_request_closed number=\"%d\" thread=\"%s\">\nThe pull request was %s.\n</pull_request_closed>", int64(num(note["number"])), t("threadId"), what)
	case "thread-auto-started":
		node := ""
		if yes(note["nodeId"]) {
			node = ` node="` + t("nodeId") + `"`
		}
		return fmt.Sprintf("<thread_auto_started thread=\"%s\" title=\"%s\"%s>\nThe autopilot opened this thread on its plan node, whose dependencies are done, and briefed it with the node. Steer it with message_thread if it needs more than the node says.\n</thread_auto_started>", t("threadId"), t("title"), node)
	case "branch-merged":
		who := "The user merged"
		if yes(note["auto"]) {
			who = "The autopilot (the review passed) merged"
		}
		how, next := "", ""
		if t("method") == "squash" {
			how = " as one squashed commit"
			next = fmt.Sprintf("Before working on the branch again, merge %s into it, so later changes are compared and merged on their own.", t("base"))
		}
		return fmt.Sprintf("<branch_merged thread=\"%s\" title=\"%s\" branch=\"%s\" base=\"%s\" method=\"%s\" commit=\"%s\">\n%s %s into %s%s, up to %s. %s\n</branch_merged>", t("threadId"), title(), t("branch"), t("base"), t("method"), short(note["sha"]), who, t("branch"), t("base"), how, short(note["head"]), next)
	case "merge-undone":
		return fmt.Sprintf("<merge_undone thread=\"%s\" title=\"%s\" branch=\"%s\" base=\"%s\" commit=\"%s\">\nThe user took that merge back: %s is where it was before, without the changes of %s. The branch still holds them and waits to be merged again; nothing on it was lost. Do not merge it or ask for it again on your own: the user decides when.\n</merge_undone>", t("threadId"), title(), t("branch"), t("base"), short(note["sha"]), t("base"), t("branch"))
	case "merge-conflict":
		if yes(note["pullRequest"]) {
			return fmt.Sprintf("<merge_conflict pull_request=\"%d\" branch=\"%s\" base=\"%s\">\nThe autopilot tried to merge your pull request into %s, and it conflicts with it. Nothing was merged. Run sync_branch, then in your workspace git merge origin/%s, resolve the conflicts keeping both sides' intent, run the project's checks, commit, push_branch, and report \"ready\" again.\n</merge_conflict>", int64(num(note["pullRequest"])), t("branch"), t("base"), t("base"), t("base"))
		}
		return fmt.Sprintf("<merge_conflict branch=\"%s\" base=\"%s\">\nMerging your branch into %s stopped: these files changed on both sides: %s. Nothing was merged. In your workspace, run git merge %s, resolve the conflicts keeping both sides' intent, run the project's checks, commit, and report \"ready\" again. Do not merge into %s yourself.\n</merge_conflict>", t("branch"), t("base"), t("base"), joined(note["files"], ", "), t("base"), t("base"))
	case "proposal-stale":
		return fmt.Sprintf("<plan_proposal_not_applied proposal=\"%s\">\nThe user agreed to \"%s\", but the plan had changed and it no longer applied (%s). Nothing changed. Read the plan and propose again if it is still needed.\n</plan_proposal_not_applied>", t("proposalId"), t("reason"), t("staleBecause"))
	case "issue-write-shared":
		head := fmt.Sprintf(`<shared_issue_change issue="%s#%d" action="%s" thread="%s" title="%s">`, t("tracker"), int64(num(note["number"])), t("action"), t("threadId"), title())
		if t("outcome") == "made" {
			url := ""
			if yes(note["url"]) {
				url = ": " + t("url")
			}
			return head + "\nThe change you waited on with that thread was made by it" + url + ". The yes was used for it; do not make it again.\n</shared_issue_change>"
		}
		return head + "\nThe attempt at this change, made with the yes you waited on, was cut off or failed, and it may have gone through. The yes is spent. Check the issue with read_issue before asking for it again.\n</shared_issue_change>"
	case "plan-changed-by-user":
		revert, after := "", ""
		if yes(note["revertOf"]) {
			revert = ` reverts="` + t("revertOf") + `"`
			after = "\nThe user undid that revision; threads on nodes it removed were stopped. Read the plan and adjust your work to it. Do not make the change again unless the user asks."
		}
		return fmt.Sprintf("<plan_changed_by_user revision=\"%s\"%s>\n%s%s\n</plan_changed_by_user>", t("revisionId"), revert, t("reason"), after)
	}
	return ""
}
func describeFeedback(note map[string]any) string {
	lines := []string{}
	for _, raw := range arr(note["items"]) {
		item := obj(raw)
		switch text(item["kind"]) {
		case "edit":
			lines = append(lines, "- They edited the text (- removed, + added):\n"+text(item["diff"]))
		case "choice":
			if len(arr(item["chosen"])) == 0 {
				lines = append(lines, "- They picked no option now.")
			} else {
				chosen := []string{}
				for _, raw := range arr(item["chosen"]) {
					value := obj(raw)
					label := `"` + text(value["title"]) + `" (` + text(value["id"]) + `)`
					if groupTitle := text(value["groupTitle"]); groupTitle != "" {
						label = "[" + groupTitle + "] " + label
					} else if groupID := text(value["group"]); groupID != "" {
						label = "[" + groupID + "] " + label
					}
					chosen = append(chosen, label)
				}
				lines = append(lines, "- They picked: "+strings.Join(chosen, ", "))
			}
		case "comment":
			switch item["action"] {
			case "reopen":
				lines = append(lines, fmt.Sprintf("- Reopened comment_thread %s, %s.", text(item["threadId"]), text(item["where"])))
			case "edit":
				lines = append(lines, fmt.Sprintf("- Edited comment in comment_thread %s, %s: %s", text(item["threadId"]), text(item["where"]), text(item["body"])))
			default:
				kind := "Reply"
				if item["action"] == "create" {
					kind = "New comment"
				}
				lines = append(lines, fmt.Sprintf("- %s in comment_thread %s, %s: %s", kind, text(item["threadId"]), text(item["where"]), text(item["body"])))
			}
		case "mark":
			extra := ""
			if yes(item["text"]) {
				extra = ": " + text(item["text"])
			}
			lines = append(lines, fmt.Sprintf("- They put a %s (%s) %s%s", text(item["type"]), text(item["nodeId"]), text(item["where"]), extra))
		case "board":
			lines = append(lines, "- "+joined(item["changes"], "\n- "))
		case "answer":
			for _, raw := range arr(item["answers"]) {
				value := obj(raw)
				parts := []string{}
				if vals := arr(value["values"]); len(vals) > 0 {
					parts = append(parts, joined(vals, ", "))
				}
				if noteText := text(value["note"]); noteText != "" {
					parts = append(parts, "note: "+noteText)
				}
				answerText := "(cleared)"
				if len(parts) > 0 {
					answerText = strings.Join(parts, " | ")
				}
				lines = append(lines, fmt.Sprintf("- Answered %q (%s): %s", text(value["question"]), text(value["id"]), answerText))
			}
		case "tweak":
			for _, raw := range arr(item["tweaks"]) {
				entry := obj(raw)
				where := fmt.Sprintf("prototype %q (%s)", text(entry["title"]), text(entry["nodeId"]))
				if optTitle := text(entry["optionTitle"]); optTitle != "" {
					where += fmt.Sprintf(" in option %q (%s)", optTitle, text(entry["optionId"]))
				}
				parts := []string{}
				for _, chRaw := range arr(entry["changes"]) {
					ch := obj(chRaw)
					key := text(ch["key"])
					label := text(ch["label"])
					unit := text(ch["unit"])
					val := text(ch["value"]) + unit
					prev := text(ch["previous"])
					name := key
					if label != "" && label != key {
						name = fmt.Sprintf("%s (%s)", label, key)
					}
					if prev != "" {
						parts = append(parts, fmt.Sprintf("%s = %s (was %s%s)", name, val, prev, unit))
					} else {
						parts = append(parts, fmt.Sprintf("%s = %s", name, val))
					}
				}
				if len(parts) > 0 {
					lines = append(lines, fmt.Sprintf("- Adjusted %s: %s", where, strings.Join(parts, ", ")))
				}
			}
		}
	}
	pictures := ""
	if len(arr(note["images"])) > 0 {
		pictures = "\nPictures of the options they marked, with their marks, are shown below."
	}
	return fmt.Sprintf("<artifact_feedback artifact=\"%s\" title=\"%s\" kind=\"%s\" round=\"%d\">\nThe user worked in the %s while you could see it; this is what they did since the last round, sent once they stopped. Take it as what they want.\n%s%s\nAnswer comments with comment_artifact in their comment thread, not in chat, and resolve_artifact_comment once settled. Change the %s where they ask for it; on a whiteboard add a new version of an option next to the one they marked rather than drawing over their marks. When you are done with this round, call finish_feedback_round with one line on what you did.\n</artifact_feedback>", text(note["artifactId"]), text(note["title"]), text(note["kind"]), int64(num(note["round"])), text(note["kind"]), strings.Join(lines, "\n"), pictures, text(note["kind"]))
}
