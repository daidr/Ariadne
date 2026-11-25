package notifications

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/eventstore"
	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/sdk"
)

const maxBodyRunes = 200

type eventObject map[string]any

func (obj eventObject) str(key string) string {
	value, _ := obj[key].(string)
	return value
}

func (obj eventObject) flag(key string) bool {
	value, _ := obj[key].(bool)
	return value
}

func (obj eventObject) mapOf(key string) eventObject {
	value, _ := obj[key].(map[string]any)
	return eventObject(value)
}

func isEnglishLocale(locale string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(locale)), "en")
}

func localizedAction(locale, key string) string {
	en := isEnglishLocale(locale)
	switch key {
	case "answer":
		if en {
			return "Waiting for your answer"
		}
		return "等你回答"
	case "review":
		if en {
			return "Waiting for your review"
		}
		return "等你审阅"
	case "done":
		if en {
			return "Done"
		}
		return "做完了"
	case "failed":
		if en {
			return "Failed"
		}
		return "出错了"
	case "aside":
		if en {
			return "Set aside until now"
		}
		return "放一放到期了"
	case "coordinator":
		if en {
			return "Coordinator"
		}
		return "协调器"
	case "testTitle":
		if en {
			return "Test notification · Ariadne"
		}
		return "测试通知 · Ariadne"
	case "testBody":
		if en {
			return "Offline notification channel is working."
		}
		return "离线通知通道工作正常。"
	default:
		return key
	}
}

func formatAutopilotHeld(locale, reason, detail string) string {
	en := isEnglishLocale(locale)
	switch reason {
	case "moved":
		if en {
			return "New commits after the review; not merged automatically"
		}
		return "审阅之后又有新提交，没有自动合并"
	case "no-pull-request":
		if en {
			return "No open pull request; not merged automatically"
		}
		return "还没有打开的 PR，没有自动合并"
	case "refused":
		if en {
			if detail != "" {
				return "Not merged automatically: " + detail
			}
			return "Not merged automatically"
		}
		if detail != "" {
			return "没有自动合并：" + detail
		}
		return "没有自动合并"
	case "failed":
		if en {
			if detail != "" {
				return "Merging automatically failed: " + detail
			}
			return "Merging automatically failed"
		}
		if detail != "" {
			return "自动合并出错：" + detail
		}
		return "自动合并出错"
	default:
		return detail
	}
}

func formatTurnError(raw any) string {
	if raw == nil {
		return ""
	}
	switch value := raw.(type) {
	case string:
		return textutil.Trim(value)
	case map[string]any:
		obj := eventObject(value)
		if message := textutil.Trim(obj.str("message")); message != "" {
			return message
		}
		if detail := textutil.Trim(obj.str("detail")); detail != "" {
			return detail
		}
		params := obj.mapOf("params")
		for _, candidate := range []string{"detail", "reason", "message", "error"} {
			if text := textutil.Trim(params.str(candidate)); text != "" {
				return text
			}
		}
		if key := textutil.Trim(obj.str("key")); key != "" {
			return key
		}
	}
	return ""
}

func askTitleOf(block eventObject) string {
	if block.str("type") == "choice" {
		if question := textutil.Trim(block.str("question")); question != "" {
			return question
		}
	}
	if title := textutil.Trim(block.str("title")); title != "" {
		return title
	}
	return textutil.Trim(block.str("question"))
}

func truncateRunes(raw string, limit int) string {
	cleaned := strings.Join(strings.Fields(raw), " ")
	runes := []rune(cleaned)
	if len(runes) <= limit {
		return cleaned
	}
	return string(runes[:limit])
}

func workspaceURL(projectID, threadID string) string {
	if projectID == "" {
		return "/"
	}
	base := "/projects/" + url.PathEscape(projectID)
	if threadID != "" {
		return base + "?pane=side&tab=" + url.QueryEscape("thread:"+threadID)
	}
	return base
}

type threadState struct {
	exists       bool
	title        string
	reviewOf     string
	merged       bool
	reviewing    bool
	lastHeadline string
	lastSummary  string
	asideDue     bool
	asideUntil   bool
	asideReason  string
}

func queryThreadState(ctx context.Context, db *sql.DB, threadID string) (threadState, error) {
	if db == nil || threadID == "" {
		return threadState{}, nil
	}
	var (
		title        string
		reviewOf     sql.NullString
		merged       sql.NullString
		reviewing    int
		lastHeadline sql.NullString
		lastSummary  sql.NullString
		asideDue     int
		asideUntil   sql.NullInt64
		asideReason  sql.NullString
	)
	err := db.QueryRowContext(ctx, `SELECT title, review_of, merged, reviewing, last_headline, last_summary,
		aside_due, aside_until, aside_reason FROM threads WHERE id = ?`, threadID).Scan(
		&title, &reviewOf, &merged, &reviewing, &lastHeadline, &lastSummary,
		&asideDue, &asideUntil, &asideReason,
	)
	if err == sql.ErrNoRows {
		return threadState{}, nil
	}
	if err != nil {
		return threadState{}, err
	}
	return threadState{
		exists:       true,
		title:        title,
		reviewOf:     reviewOf.String,
		merged:       merged.Valid && merged.String != "",
		reviewing:    reviewing != 0,
		lastHeadline: lastHeadline.String,
		lastSummary:  lastSummary.String,
		asideDue:     asideDue != 0,
		asideUntil:   asideUntil.Valid,
		asideReason:  asideReason.String,
	}, nil
}

func attentionStillOpen(ctx context.Context, db *sql.DB, attentionID string) (bool, error) {
	if db == nil || attentionID == "" {
		return false, nil
	}
	var closed int
	err := db.QueryRowContext(ctx, `SELECT closed FROM attentions WHERE id = ?`, attentionID).Scan(&closed)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return closed == 0, nil
}

func conversationStillFailed(ctx context.Context, db *sql.DB, conversationID string) (bool, error) {
	if db == nil || conversationID == "" {
		return false, nil
	}
	var turnID sql.NullString
	var lastOutcome sql.NullString
	err := db.QueryRowContext(ctx, `SELECT turn_id, last_outcome FROM conversations WHERE id = ?`, conversationID).Scan(&turnID, &lastOutcome)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return !turnID.Valid && lastOutcome.String == "failed", nil
}

func totalNeedsMe(ctx context.Context, repo *core.Repository) int {
	if repo == nil {
		return 0
	}
	projects, err := repo.ListProjects(ctx)
	if err != nil {
		return 0
	}
	total := 0
	for _, project := range projects {
		total += project.NeedsMe
	}
	return total
}

// evaluateEvent maps a stored event to a NotificationPayload if it matches one of the
// 6+1 high-signal notification rules and passes post-read state verification.
func evaluateEvent(ctx context.Context, repo *core.Repository, event eventstore.Event, locale string) (*sdk.NotificationPayload, error) {
	if repo == nil || event.ProjectID == "" {
		return nil, nil
	}
	switch event.Type {
	case "attention.opened", "thread.reported", "review.submitted",
		"autopilot.held", "review.abandoned", "turn.finished", "thread.aside_due":
	default:
		return nil, nil
	}

	var data eventObject
	if len(event.Data) > 0 {
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return nil, fmt.Errorf("decode %s event %d: %w", event.Type, event.Seq, err)
		}
	}
	if data == nil {
		data = eventObject{}
	}

	project, err := repo.GetProject(ctx, event.ProjectID)
	if err != nil {
		return nil, nil
	}
	db := repo.Store.DB()

	var (
		threadID string
		action   string
		body     string
		thread   threadState
	)

	switch event.Type {
	case "attention.opened":
		attentionID := data.str("attentionId")
		open, err := attentionStillOpen(ctx, db, attentionID)
		if err != nil || !open {
			return nil, err
		}
		threadID = data.str("threadId")
		if threadID != "" {
			thread, err = queryThreadState(ctx, db, threadID)
			if err != nil {
				return nil, err
			}
		}
		action = localizedAction(locale, "answer")
		body = askTitleOf(data.mapOf("block"))

	case "thread.reported":
		if data.flag("reviewing") {
			return nil, nil
		}
		report := data.str("report")
		if report != "ready" && report != "done" {
			return nil, nil
		}
		threadID = data.str("threadId")
		thread, err = queryThreadState(ctx, db, threadID)
		if err != nil || !thread.exists || thread.reviewOf != "" || thread.reviewing || thread.merged {
			return nil, err
		}
		if report == "ready" {
			action = localizedAction(locale, "review")
		} else {
			action = localizedAction(locale, "done")
		}
		body = textutil.Trim(data.str("summary"))
		if body == "" {
			body = textutil.Trim(data.str("headline"))
		}

	case "review.submitted":
		if data.str("verdict") != "pass" {
			return nil, nil
		}
		if project.Settings.Autopilot.Enabled && !project.AutopilotPaused {
			return nil, nil
		}
		threadID = data.str("threadId")
		thread, err = queryThreadState(ctx, db, threadID)
		if err != nil || !thread.exists || thread.merged {
			return nil, err
		}
		action = localizedAction(locale, "review")
		body = textutil.Trim(data.str("summary"))

	case "autopilot.held":
		threadID = data.str("threadId")
		thread, err = queryThreadState(ctx, db, threadID)
		if err != nil || !thread.exists || thread.merged {
			return nil, err
		}
		action = localizedAction(locale, "review")
		body = formatAutopilotHeld(locale, data.str("reason"), textutil.Trim(data.str("detail")))

	case "review.abandoned":
		threadID = data.str("threadId")
		thread, err = queryThreadState(ctx, db, threadID)
		if err != nil || !thread.exists || thread.merged {
			return nil, err
		}
		action = localizedAction(locale, "review")
		body = textutil.Trim(thread.lastHeadline)
		if body == "" {
			body = textutil.Trim(thread.lastSummary)
		}

	case "turn.finished":
		if data.str("outcome") != "failed" {
			return nil, nil
		}
		convID := data.str("conversationId")
		if convID == "" {
			return nil, nil
		}
		if convID != project.CoordinatorID {
			threadID = convID
			thread, err = queryThreadState(ctx, db, threadID)
			if err != nil || !thread.exists || thread.reviewOf != "" {
				return nil, err
			}
		}
		failed, err := conversationStillFailed(ctx, db, convID)
		if err != nil || !failed {
			return nil, err
		}
		action = localizedAction(locale, "failed")
		body = formatTurnError(data["error"])

	case "thread.aside_due":
		threadID = data.str("threadId")
		thread, err = queryThreadState(ctx, db, threadID)
		if err != nil || !thread.exists || !thread.asideDue || !thread.asideUntil {
			return nil, err
		}
		action = localizedAction(locale, "aside")
		body = textutil.Trim(thread.asideReason)
	}

	whereParts := []string{}
	if projectName := textutil.Trim(project.Name); projectName != "" {
		whereParts = append(whereParts, projectName)
	}
	if threadID != "" {
		if title := textutil.Trim(thread.title); title != "" {
			whereParts = append(whereParts, title)
		}
	} else {
		whereParts = append(whereParts, localizedAction(locale, "coordinator"))
	}
	title := action
	if len(whereParts) > 0 {
		title = action + " · " + strings.Join(whereParts, " · ")
	}
	tagTarget := event.ProjectID
	if threadID != "" {
		tagTarget = threadID
	}

	return &sdk.NotificationPayload{
		Event:       event.Type,
		Title:       title,
		Body:        truncateRunes(body, maxBodyRunes),
		URL:         workspaceURL(event.ProjectID, threadID),
		ProjectID:   event.ProjectID,
		ProjectName: project.Name,
		ThreadID:    threadID,
		ThreadTitle: thread.title,
		Tag:         "ariadne:" + tagTarget,
		NeedsMe:     totalNeedsMe(ctx, repo),
		At:          event.At,
	}, nil
}
