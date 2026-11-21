package core

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/eventstore"
)

func TestReviewRoundsExternalDeliveryAndAbandonMatchLegacy(t *testing.T) {
	seed := []eventstore.NewEvent{{Type: "project.created", ProjectID: "prj_a", Data: json.RawMessage(`{"name":"项目","coordinatorId":"crd_a"}`)}, {Type: "thread.created", ProjectID: "prj_a", Data: json.RawMessage(`{"threadId":"thr_a","title":"实现","nodeId":null,"model":"model_a","branch":"feature"}`)}}
	commands := []object{}
	add := func(op string, args object) { commands = append(commands, object{"op": op, "args": args}) }
	ready := func(reviewable bool, replies []object) {
		review := object{"reviewable": reviewable, "head": "head_a", "reviewerTitle": "审查实现"}
		if replies != nil {
			review["replies"] = replies
		}
		add("ready", object{"report": object{"kind": "ready", "summary": "实现完成", "headline": "准备交付", "artifacts": []string{}}, "head": "head_a", "review": review})
	}
	comment := object{"path": "main.go", "line": 1, "quote": "code", "body": "需要修复", "kind": "bug"}
	verdict := func(kind string, comments []object) {
		add("verdict", object{"input": object{"verdict": kind, "summary": "审查结论", "headline": "结论", "comments": comments}})
	}
	ready(true, nil)
	add("return", object{"away": 599999})
	add("return", object{"away": 600000})
	add("return", object{"away": 600000})
	verdict("changes", []object{comment})
	ready(true, nil)
	add("handled", object{})
	ready(true, []object{{"comment": 1, "outcome": "fixed", "note": "修复"}})
	verdict("changes", []object{comment})
	add("handled", object{})
	ready(true, []object{{"comment": 1, "outcome": "declined", "note": "已有校验"}})
	add("complete", object{})
	add("delivery", object{"input": object{"mode": "external", "instructions": "按集成平台流程交付"}})
	ready(false, nil)
	ready(true, nil)
	verdict("pass", []object{})
	add("handled", object{"id": "reviewer"})
	add("settings", object{"input": object{"delivery": object{"mode": "external", "instructions": "提票"}, "review": object{"enabled": false}}})
	ready(true, nil)
	add("settings", object{"input": object{"review": object{"enabled": true}}})
	ready(true, nil)
	add("stop", object{"id": "reviewer", "by": "user", "reason": "暂停审查"})
	ready(true, nil)
	add("resume", object{"id": "reviewer", "by": "user"})
	ready(true, nil)
	verdict("pass", []object{comment})
	verdict("bad", []object{})
	add("verdict", object{"input": object{"verdict": "pass", "summary": " ", "headline": "结论", "comments": []object{}}})
	verdict("pass", []object{})
	add("handled", object{"id": "reviewer"})
	add("complete", object{})
	ready(true, nil)
	add("handled", object{"id": "reviewer", "outcome": "failed"})
	add("complete", object{})
	var expected []any
	oracle(t, "packages/core/src/testing/go-review-flow-fixture.ts", object{"seed": seed, "commands": commands}, &expected)
	repo, err := Open(t.Context(), Options{Path: filepath.Join(t.TempDir(), "review-flow.sqlite"), Now: func() time.Time { return time.UnixMilli(1800000000000) }})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if _, err := repo.Store.Append(t.Context(), seed); err != nil {
		t.Fatal(err)
	}
	for i, command := range commands {
		a := asObject(jsonValue(command["args"]))
		thread, err := repo.GetThread(t.Context(), "thr_a")
		if err != nil {
			t.Fatal(err)
		}
		reviewer := ""
		if thread.Review != nil {
			reviewer = thread.Review.ReviewerID
		}
		id := a.str("id")
		if id == "" {
			id = "thr_a"
		}
		if id == "reviewer" {
			id = reviewer
		}
		var value any
		switch command.str("op") {
		case "ready":
			var report ThreadReport
			var review ReviewOptions
			var head *string
			decodeInput(t, a["report"], &report)
			decodeInput(t, a["review"], &review)
			decodeInput(t, a["head"], &head)
			value, err = repo.ReportThread(t.Context(), "thr_a", report, head, review)
		case "verdict":
			var input ReviewSubmission
			decodeInput(t, a["input"], &input)
			value, err = repo.SubmitReview(t.Context(), reviewer, input)
		case "handled":
			var turn string
			turn, err = repo.StartTurn(t.Context(), id, "model_a")
			if err == nil {
				sawSeq, readErr := repo.Store.LastSeq(t.Context())
				if readErr != nil {
					t.Fatal(readErr)
				}
				err = repo.RecordStep(t.Context(), id, turn, Step{SawSeq: sawSeq, Input: StepInput{System: "review_test", Tools: "[]", Call: "{}", Images: true}, Timing: CallTiming{StartedAt: 1800000000000}})
				if err == nil {
					outcome := a.str("outcome")
					if outcome == "" {
						outcome = "completed"
					}
					err = repo.FinishTurn(t.Context(), id, turn, outcome, nil)
				}
			}
		case "complete":
			value, err = repo.CompleteThread(t.Context(), "thr_a")
		case "delivery":
			value, err = repo.SetProjectDelivery(t.Context(), "prj_a", a.obj("input"))
		case "settings":
			value, err = repo.SetProjectSettings(t.Context(), "prj_a", a["input"], []string{})
		case "stop":
			value, err = repo.StopThread(t.Context(), id, a.str("reason"), a.str("by"))
		case "resume":
			value, err = repo.ResumeThread(t.Context(), id, a.str("by"))
		case "return":
			away, _ := number(a["away"])
			err = repo.NoteReturn(t.Context(), "prj_a", away)
		default:
			t.Fatal(command)
		}
		var localized any
		if err != nil {
			var domain *domainerr.Error
			if !errors.As(err, &domain) {
				t.Fatal(err)
			}
			localized = domain
			value = nil
		}
		detail, readErr := repo.GetProjectDetail(t.Context(), "prj_a")
		if readErr != nil {
			t.Fatal(readErr)
		}
		reviews, readErr := repo.ListReviews(t.Context(), "thr_a")
		if readErr != nil {
			t.Fatal(readErr)
		}
		open, readErr := repo.OpenReviewRound(t.Context(), "thr_a")
		if readErr != nil {
			t.Fatal(readErr)
		}
		passed, readErr := repo.PassedReview(t.Context(), "thr_a")
		if readErr != nil {
			t.Fatal(readErr)
		}
		reads := []object{}
		for _, thread := range detail.Threads {
			transcript, readErr := repo.ListTranscript(t.Context(), thread.ID, TranscriptOptions{})
			if readErr != nil {
				t.Fatal(readErr)
			}
			notices, readErr := repo.ListNotices(t.Context(), thread.ID, NoticeRange{})
			if readErr != nil {
				t.Fatal(readErr)
			}
			reads = append(reads, object{"id": thread.ID, "transcript": transcript, "notices": notices})
		}
		coordinator, readErr := repo.ListTranscript(t.Context(), "crd_a", TranscriptOptions{})
		if readErr != nil {
			t.Fatal(readErr)
		}
		stored, readErr := repo.Store.Read(t.Context(), eventstore.ReadOptions{})
		if readErr != nil {
			t.Fatal(readErr)
		}
		events := []object{}
		for _, e := range stored {
			events = append(events, object{"seq": e.Seq, "type": e.Type, "projectId": e.ProjectID, "data": e.Data, "at": e.At})
		}
		actual := object{"value": value, "error": localized, "detail": detail, "reviews": reviews, "open": open, "passed": passed, "reads": reads, "coordinator": coordinator, "events": events}
		want := normalizedIDs(normalizeLegacyExpected(expected[i]))
		if !reflect.DeepEqual(normalizedIDs(actual), want) {
			t.Fatalf("command %d %s differs\ngot %s\nwant%s", i, encoded(command), encoded(normalizedIDs(actual)), encoded(want))
		}
	}
}
