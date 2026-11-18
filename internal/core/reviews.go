package core

import (
	"context"
	"database/sql"
	"math"
	"slices"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/eventstore"
	"github.com/daidr/Ariadne/internal/projectsettings"
)

func reviewRoundView(row record) (object, error) {
	comments, err := row.json("comments", []any{})
	if err != nil {
		return nil, err
	}
	replies, err := row.json("replies", nil)
	if err != nil {
		return nil, err
	}
	return object{"threadId": row["thread_id"], "reviewerId": row["reviewer_id"], "round": row["round"], "head": row["head"], "requestedAt": row["requested_at"], "verdict": row["verdict"], "abandoned": row.present("abandoned_at"), "summary": row["summary"], "comments": comments, "replies": replies, "submittedAt": row["submitted_at"]}, nil
}
func openReviewRound(w *projectionWriter, id string) record {
	delivered := w.one("SELECT review_delivered FROM threads WHERE id = ?", id)
	if delivered == nil || delivered.integer("review_delivered") != 0 {
		return nil
	}
	latest := w.one("SELECT * FROM reviews WHERE thread_id = ? ORDER BY id DESC LIMIT 1", id)
	if latest.text("verdict") == "changes" && !latest.present("replies") {
		return latest
	}
	return nil
}
func (reader Reader) OpenReviewRound(ctx context.Context, id string) (result object, err error) {
	err = reader.snapshot(ctx, func(tx *sql.Tx) error {
		w := &projectionWriter{ctx: ctx, tx: tx}
		row := openReviewRound(w, id)
		if w.err != nil {
			return w.err
		}
		if row == nil {
			return nil
		}
		result, err = reviewRoundView(row)
		return err
	})
	return
}
func (reader Reader) PassedReview(ctx context.Context, id string) (result object, err error) {
	err = reader.snapshot(ctx, func(tx *sql.Tx) error {
		thread, err := readThread(ctx, tx, id, reader.now())
		if err != nil {
			return err
		}
		if thread.Stage != "review" || truthy(thread.ReviewOf) {
			return nil
		}
		w := &projectionWriter{ctx: ctx, tx: tx}
		reported := w.one("SELECT reported_at FROM threads WHERE id = ?", id)
		row := w.one("SELECT * FROM reviews WHERE thread_id = ? ORDER BY id DESC LIMIT 1", id)
		if w.err != nil {
			return w.err
		}
		if row.text("verdict") != "pass" || row.integer("submitted_at") < reported.integer("reported_at") {
			return nil
		}
		result, err = reviewRoundView(row)
		return err
	})
	return
}
func parseReviewComments(given []any) ([]object, error) {
	comments, err := lineComments(given)
	if err != nil {
		return nil, err
	}
	for i, raw := range given {
		kind := asObject(raw).str("kind")
		if !slices.Contains([]string{"bug", "acceptance", "security"}, kind) {
			return nil, badInput("comments")
		}
		comments[i]["kind"] = kind
	}
	return comments, nil
}
func parseReviewReplies(given []any, count int) ([]object, error) {
	if len(given) != count {
		return nil, badInput("replies")
	}
	seen := map[float64]bool{}
	replies := []object{}
	for _, raw := range given {
		item := asObject(raw)
		comment, ok := number(item["comment"])
		if !ok || comment != math.Trunc(comment) || comment < 1 || comment > float64(count) || seen[comment] {
			return nil, badInput("replies")
		}
		seen[comment] = true
		outcome := item.str("outcome")
		if outcome != "fixed" && outcome != "declined" {
			return nil, badInput("replies")
		}
		note := projectsettings.Trim(item.str("note"))
		if outcome == "declined" && note == "" || utf16Length(note) > 4000 {
			return nil, badInput("replies")
		}
		replies = append(replies, object{"comment": item["comment"], "outcome": outcome, "note": note})
	}
	slices.SortFunc(replies, func(a, b object) int {
		x, _ := number(a["comment"])
		y, _ := number(b["comment"])
		if x < y {
			return -1
		}
		if x > y {
			return 1
		}
		return 0
	})
	return replies, nil
}

type ThreadReport struct {
	Kind      string
	Summary   string
	Headline  string
	Artifacts []string
}
type ReviewOptions struct {
	Reviewable    bool
	Head          *string
	Replies       []any
	ReviewerTitle string
}

func nextReviewRound(w *projectionWriter, thread ThreadView, kind string, reviewable, enabled bool, now int64) (bool, int64) {
	if kind != "ready" || truthy(thread.ReviewOf) {
		return false, 0
	}
	row := w.one("SELECT review_rounds, review_delivered, reviewing FROM threads WHERE id = ?", thread.ID)
	rounds := int64(0)
	if row != nil && row.integer("review_delivered") == 0 {
		rounds = row.integer("review_rounds")
	}
	if row.flag("reviewing") && thread.Review != nil && thread.Review.Reviewing {
		return true, rounds
	}
	stopped := false
	if thread.Review != nil {
		reviewer, err := readThread(w.ctx, w.tx, thread.Review.ReviewerID, now)
		if err != nil {
			w.err = err
			return false, 0
		}
		stopped = reviewer.Stopped != nil
	}
	if reviewable && enabled && !stopped && rounds < 2 {
		return true, rounds + 1
	}
	return false, rounds
}
func (repo *Repository) ReportThread(ctx context.Context, id string, input ThreadReport, head *string, review ReviewOptions) (ThreadView, error) {
	return repo.mutateThread(ctx, id, func(tx *eventstore.Transaction, thread ThreadView) ([]change, error) {
		summary, headline := projectsettings.Trim(input.Summary), projectsettings.Trim(input.Headline)
		if summary == "" {
			return nil, badInput("summary")
		}
		if headline == "" {
			return nil, badInput("headline")
		}
		w := &projectionWriter{ctx: ctx, tx: tx.SQL()}
		artifacts := []string{}
		seen := map[string]bool{}
		for _, artifactID := range input.Artifacts {
			if seen[artifactID] {
				continue
			}
			seen[artifactID] = true
			artifact, err := getArtifact(ctx, tx.SQL(), artifactID, nil)
			if err != nil {
				return nil, err
			}
			if artifact.ProjectID != thread.ProjectID {
				return nil, domainerr.NotFound(artifactID)
			}
			artifacts = append(artifacts, artifactID)
		}
		changes := []change{}
		open := openReviewRound(w, id)
		if input.Kind == "ready" && open != nil {
			comments := w.parsed(open, "comments", []any{})
			values, _ := listValue(comments)
			replies, err := parseReviewReplies(review.Replies, len(values))
			if err != nil {
				return nil, err
			}
			changes = append(changes, change{"review.replied", object{"threadId": id, "round": open["round"], "replies": replies}})
		}
		project, err := projectDetail(ctx, tx.SQL(), thread.ProjectID, repo.Reader.now())
		if err != nil {
			return nil, err
		}
		reviewing, round := nextReviewRound(w, thread, input.Kind, review.Reviewable, project.Project.Settings.Review.Enabled, repo.Reader.now())
		kind := input.Kind
		if kind == "ready" && !reviewing && round == 0 && project.Project.Settings.Delivery.Mode == "external" {
			kind = "done"
		}
		data := object{"threadId": id, "report": kind, "summary": summary, "headline": headline, "artifacts": artifacts}
		if head != nil && *head != "" {
			data["head"] = *head
		}
		if reviewing {
			data["reviewing"] = true
		} else if input.Kind == "ready" && round > 0 {
			data["reviewRounds"] = round
		}
		changes = append(changes, change{"thread.reported", data})
		if reviewing && (thread.Review == nil || !thread.Review.Reviewing) {
			reviewerID := ""
			if thread.Review != nil {
				reviewerID = thread.Review.ReviewerID
			}
			if reviewerID == "" {
				reviewerID, err = newID("thr")
				if err != nil {
					return nil, err
				}
				title := projectsettings.Trim(review.ReviewerTitle)
				if title == "" {
					title = thread.Title
				}
				changes = append(changes, change{"thread.created", object{"threadId": reviewerID, "title": title, "nodeId": nil, "model": thread.Model, "branch": nil, "reviewOf": id}}, change{"thread.started", object{"threadId": reviewerID}})
			}
			var reviewHead any
			if review.Head != nil {
				reviewHead = *review.Head
			} else if head != nil {
				reviewHead = *head
			}
			changes = append(changes, change{"review.requested", object{"threadId": id, "reviewerId": reviewerID, "round": round, "head": reviewHead}})
		}
		return changes, w.err
	})
}

type ReviewSubmission struct {
	Verdict  string
	Summary  string
	Comments []any
	Headline string
}

func (repo *Repository) SubmitReview(ctx context.Context, reviewerID string, input ReviewSubmission) (result object, err error) {
	err = repo.Store.Within(ctx, func(tx *eventstore.Transaction) error {
		reviewer, err := readThread(ctx, tx.SQL(), reviewerID, repo.Reader.now())
		if err != nil {
			return err
		}
		authorID, _ := reviewer.ReviewOf.(string)
		if authorID == "" {
			return domainerr.New("error.notReviewer", nil)
		}
		w := &projectionWriter{ctx: ctx, tx: tx.SQL()}
		round := w.one("SELECT * FROM reviews WHERE thread_id = ? ORDER BY id DESC LIMIT 1", authorID)
		if w.err != nil {
			return w.err
		}
		if round == nil || round.present("verdict") || round.present("abandoned_at") || round.text("reviewer_id") != reviewerID {
			return domainerr.New("error.noReviewRound", nil)
		}
		summary := projectsettings.Trim(input.Summary)
		if summary == "" {
			return badInput("summary")
		}
		if input.Verdict != "pass" && input.Verdict != "changes" {
			return badInput("verdict")
		}
		comments, err := parseReviewComments(input.Comments)
		if err != nil {
			return err
		}
		if (input.Verdict == "changes") != (len(comments) > 0) {
			return badInput("comments")
		}
		headline := projectsettings.Trim(input.Headline)
		if headline == "" {
			headline = utf16Slice(summary, 0, 60)
		}
		changes := []change{{"review.submitted", object{"threadId": authorID, "reviewerId": reviewerID, "round": round["round"], "verdict": input.Verdict, "summary": summary, "comments": comments}}, {"thread.reported", object{"threadId": reviewerID, "report": "done", "summary": summary, "headline": headline, "artifacts": []any{}}}}
		project, err := projectDetail(ctx, tx.SQL(), reviewer.ProjectID, repo.Reader.now())
		if err != nil {
			return err
		}
		if input.Verdict == "pass" && project.Project.Settings.Delivery.Mode == "external" {
			author, err := readThread(ctx, tx.SQL(), authorID, repo.Reader.now())
			if err != nil {
				return err
			}
			artifacts := []string{}
			for _, raw := range refs(author.LastArtifacts) {
				artifacts = append(artifacts, raw.str("id"))
			}
			data := object{"threadId": authorID, "report": "done", "summary": fallback(author.LastSummary, summary), "headline": fallback(author.LastHeadline, utf16Slice(summary, 0, 60)), "artifacts": artifacts}
			if round.text("head") != "" {
				data["head"] = round["head"]
			}
			changes = append(changes, change{"thread.reported", data})
		}
		if err := appendChanges(tx, reviewer.ProjectID, changes); err != nil {
			return err
		}
		latest := w.one("SELECT * FROM reviews WHERE thread_id = ? ORDER BY id DESC LIMIT 1", authorID)
		if w.err != nil {
			return w.err
		}
		result, err = reviewRoundView(latest)
		return err
	})
	return
}
func (reader Reader) ListLineComments(ctx context.Context, id string) (result []object, err error) {
	err = reader.snapshot(ctx, func(tx *sql.Tx) error {
		if _, err := readThread(ctx, tx, id, reader.now()); err != nil {
			return err
		}
		rows, err := records(ctx, tx, "SELECT id, comments, created_at FROM messages WHERE conversation_id = ? AND author = 'user' AND comments IS NOT NULL ORDER BY id", id)
		if err != nil {
			return err
		}
		result = []object{}
		for _, row := range rows {
			parsed, err := row.json("comments", []any{})
			if err != nil {
				return err
			}
			for index, raw := range objects(parsed) {
				comment := except(raw, "")
				comment["id"] = row.text("id") + ":" + encoded(index)
				comment["messageId"] = row["id"]
				comment["sentAt"] = row["created_at"]
				result = append(result, comment)
			}
		}
		return nil
	})
	return
}
