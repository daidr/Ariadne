package runner

import (
	"fmt"
	"iter"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/sdk"
)

func runThreadToolSteps(t *testing.T, totalSteps int, pick func(step int) sdk.Part) []recordedToolResult {
	t.Helper()
	repo, store, project := openTestEnv(t)
	thread, err := repo.CreateThread(t.Context(), project.ID, core.NewThread{Title: "执行任务"})
	if err != nil {
		t.Fatal(err)
	}
	conv := core.ConversationView{ID: thread.ID, ProjectID: project.ID, Role: "thread"}
	step := 0
	return runToolTurn(t, repo, store, conv, nil, func(_ sdk.Request) iter.Seq[sdk.Part] {
		current := step
		step++
		return func(yield func(sdk.Part) bool) {
			if current < totalSteps {
				yield(pick(current))
				yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
				return
			}
			yield(sdk.Part{Type: "finish", Reason: "stop"})
		}
	})
}

func lookMessageCall(step int) sdk.Part {
	return makeCall(fmt.Sprintf("m%d", step), "list_messages", args{"limit": step + 1})
}

func updateTodoCall(step int, status string) sdk.Part {
	return makeCall(fmt.Sprintf("t%d", step), "update_todo", args{
		"items": []args{{"label": "执行步骤", "status": status}},
	})
}

func TestThreadTodoMissingHintOnce(t *testing.T) {
	results := runThreadToolSteps(t, todoMissingAfter+3, lookMessageCall)
	var hintedAt []int
	for i, res := range results {
		if strings.Contains(res.content, todoMissingHint) {
			hintedAt = append(hintedAt, i)
		}
		if strings.Contains(res.content, todoStaleHint) || strings.Contains(res.content, todoFinishedHint) {
			t.Fatalf("unexpected hint at index %d: %q", i, res.content)
		}
	}
	if len(hintedAt) != 1 || hintedAt[0] != todoMissingAfter-1 {
		t.Fatalf("missing hint indices = %v, want [%d]", hintedAt, todoMissingAfter-1)
	}
	if strings.Contains(results[hintedAt[0]].content, "<current_todo>") {
		t.Fatalf("missing hint with empty todo must not include <current_todo>: %q", results[hintedAt[0]].content)
	}
}

func TestThreadTodoWrittenFirstNoMissingHint(t *testing.T) {
	results := runThreadToolSteps(t, todoMissingAfter+3, func(step int) sdk.Part {
		if step == 0 {
			return updateTodoCall(step, "doing")
		}
		return lookMessageCall(step)
	})
	for i, res := range results {
		if strings.Contains(res.content, todoMissingHint) || strings.Contains(res.content, todoFinishedHint) {
			t.Fatalf("unexpected missing/finished hint at index %d: %q", i, res.content)
		}
	}
}

func TestThreadTodoStaleHintAtTenSteps(t *testing.T) {
	results := runThreadToolSteps(t, todoStaleAfter+4, func(step int) sdk.Part {
		if step == 0 {
			return updateTodoCall(step, "doing")
		}
		return lookMessageCall(step)
	})
	var hintedAt []int
	for i, res := range results {
		if strings.Contains(res.content, todoStaleHint) {
			hintedAt = append(hintedAt, i)
		}
		if strings.Contains(res.content, todoMissingHint) || strings.Contains(res.content, todoFinishedHint) {
			t.Fatalf("unexpected missing/finished hint at index %d: %q", i, res.content)
		}
	}
	if len(hintedAt) != 1 || hintedAt[0] != todoStaleAfter {
		t.Fatalf("stale hint indices = %v, want [%d]", hintedAt, todoStaleAfter)
	}
	if !strings.Contains(results[hintedAt[0]].content, "<current_todo>") ||
		!strings.Contains(results[hintedAt[0]].content, "- [doing] 执行步骤") {
		t.Fatalf("stale hint should include current todo items: %q", results[hintedAt[0]].content)
	}
}

func TestThreadTodoFinishedThisTurnNotStale(t *testing.T) {
	results := runThreadToolSteps(t, todoStaleAfter+4, func(step int) sdk.Part {
		if step == 0 {
			return updateTodoCall(step, "done")
		}
		return lookMessageCall(step)
	})
	for i, res := range results {
		if strings.Contains(res.content, todoMissingHint) ||
			strings.Contains(res.content, todoFinishedHint) ||
			strings.Contains(res.content, todoStaleHint) {
			t.Fatalf("finished todo from this turn should not trigger any hint at index %d: %q", i, res.content)
		}
	}
}

func TestThreadTodoFinishedEarlierTurnReminded(t *testing.T) {
	repo, store, project := openTestEnv(t)
	thread, err := repo.CreateThread(t.Context(), project.ID, core.NewThread{Title: "多回合任务"})
	if err != nil {
		t.Fatal(err)
	}
	conv := core.ConversationView{ID: thread.ID, ProjectID: project.ID, Role: "thread"}

	// Turn 1: write a finished todo list.
	step1 := 0
	runToolTurn(t, repo, store, conv, nil, func(_ sdk.Request) iter.Seq[sdk.Part] {
		current := step1
		step1++
		return func(yield func(sdk.Part) bool) {
			if current == 0 {
				yield(updateTodoCall(0, "done"))
				yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
				return
			}
			yield(sdk.Part{Type: "finish", Reason: "stop"})
		}
	})

	// Turn 2: work without writing a fresh todo list; step 3 gets todoFinishedHint once, never stale.
	step2 := 0
	results := runToolTurn(t, repo, store, conv, nil, func(_ sdk.Request) iter.Seq[sdk.Part] {
		current := step2
		step2++
		return func(yield func(sdk.Part) bool) {
			if current < todoStaleAfter+2 {
				yield(lookMessageCall(current))
				yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
				return
			}
			yield(sdk.Part{Type: "finish", Reason: "stop"})
		}
	})

	var hintedAt []int
	for i, res := range results {
		if strings.Contains(res.content, todoFinishedHint) {
			hintedAt = append(hintedAt, i)
		}
		if strings.Contains(res.content, todoMissingHint) || strings.Contains(res.content, todoStaleHint) {
			t.Fatalf("unexpected missing/stale hint at index %d: %q", i, res.content)
		}
	}
	if len(hintedAt) != 1 || hintedAt[0] != todoMissingAfter-1 {
		t.Fatalf("finished hint indices = %v, want [%d]", hintedAt, todoMissingAfter-1)
	}
}

func TestReviewThreadSkipsTodoHints(t *testing.T) {
	repo, store, project := openTestEnv(t)
	author, err := repo.CreateThread(t.Context(), project.ID, core.NewThread{Title: "作者讨论串"})
	if err != nil {
		t.Fatal(err)
	}
	reviewerID := createReviewerThread(t, repo, author.ID, "评审讨论串")
	conv := core.ConversationView{ID: reviewerID, ProjectID: project.ID, Role: "thread"}

	step := 0
	results := runToolTurn(t, repo, store, conv, nil, func(_ sdk.Request) iter.Seq[sdk.Part] {
		current := step
		step++
		return func(yield func(sdk.Part) bool) {
			if current < todoStaleAfter+2 {
				yield(lookMessageCall(current))
				yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
				return
			}
			yield(sdk.Part{Type: "finish", Reason: "stop"})
		}
	})

	for i, res := range results {
		if strings.Contains(res.content, todoMissingHint) ||
			strings.Contains(res.content, todoFinishedHint) ||
			strings.Contains(res.content, todoStaleHint) {
			t.Fatalf("review thread should skip todo hints, got one at index %d: %q", i, res.content)
		}
	}
}

func runCoordinatorToolSteps(t *testing.T, totalSteps int, pick func(step int) sdk.Part) []recordedToolResult {
	t.Helper()
	repo, store, project := openTestEnv(t)
	conv := core.ConversationView{ID: project.CoordinatorID, ProjectID: project.ID, Role: "coordinator"}
	step := 0
	return runToolTurn(t, repo, store, conv, nil, func(_ sdk.Request) iter.Seq[sdk.Part] {
		current := step
		step++
		return func(yield func(sdk.Part) bool) {
			if current < totalSteps {
				yield(pick(current))
				yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
				return
			}
			yield(sdk.Part{Type: "finish", Reason: "stop"})
		}
	})
}

func clearTodoCall(step int) sdk.Part {
	return makeCall(fmt.Sprintf("t%d", step), "update_todo", args{"items": []args{}})
}

func TestCoordinatorTodoMissingHintUsesCoordinatorWording(t *testing.T) {
	results := runCoordinatorToolSteps(t, todoMissingAfter+3, lookMessageCall)
	var hintedAt []int
	for i, res := range results {
		if strings.Contains(res.content, coordinatorTodoMissingHint) {
			hintedAt = append(hintedAt, i)
		}
		if strings.Contains(res.content, todoMissingHint) ||
			strings.Contains(res.content, todoFinishedHint) ||
			strings.Contains(res.content, todoStaleHint) ||
			strings.Contains(res.content, coordinatorTodoFinishedHint) ||
			strings.Contains(res.content, coordinatorTodoStaleHint) {
			t.Fatalf("unexpected hint at index %d: %q", i, res.content)
		}
	}
	if len(hintedAt) != 1 || hintedAt[0] != todoMissingAfter-1 {
		t.Fatalf("coordinator missing hint indices = %v, want [%d]", hintedAt, todoMissingAfter-1)
	}
}

func TestCoordinatorTodoStaleHintUsesCoordinatorWording(t *testing.T) {
	results := runCoordinatorToolSteps(t, todoStaleAfter+4, func(step int) sdk.Part {
		if step == 0 {
			return updateTodoCall(step, "doing")
		}
		return lookMessageCall(step)
	})
	var hintedAt []int
	for i, res := range results {
		if strings.Contains(res.content, coordinatorTodoStaleHint) {
			hintedAt = append(hintedAt, i)
		}
		if strings.Contains(res.content, todoStaleHint) ||
			strings.Contains(res.content, todoMissingHint) ||
			strings.Contains(res.content, todoFinishedHint) ||
			strings.Contains(res.content, coordinatorTodoMissingHint) ||
			strings.Contains(res.content, coordinatorTodoFinishedHint) {
			t.Fatalf("unexpected hint at index %d: %q", i, res.content)
		}
	}
	if len(hintedAt) != 1 || hintedAt[0] != todoStaleAfter {
		t.Fatalf("coordinator stale hint indices = %v, want [%d]", hintedAt, todoStaleAfter)
	}
	if !strings.Contains(results[hintedAt[0]].content, "<current_todo>") ||
		!strings.Contains(results[hintedAt[0]].content, "- [doing] 执行步骤") {
		t.Fatalf("coordinator stale hint should include current todo items: %q", results[hintedAt[0]].content)
	}
}

func TestCoordinatorTodoClearedThisTurnNotReminded(t *testing.T) {
	results := runCoordinatorToolSteps(t, todoStaleAfter+4, func(step int) sdk.Part {
		if step == 0 {
			return clearTodoCall(step)
		}
		return lookMessageCall(step)
	})
	for i, res := range results {
		if strings.Contains(res.content, coordinatorTodoMissingHint) ||
			strings.Contains(res.content, coordinatorTodoFinishedHint) ||
			strings.Contains(res.content, coordinatorTodoStaleHint) ||
			strings.Contains(res.content, todoMissingHint) ||
			strings.Contains(res.content, todoFinishedHint) ||
			strings.Contains(res.content, todoStaleHint) {
			t.Fatalf("cleared coordinator todo from this turn should not trigger any hint at index %d: %q", i, res.content)
		}
	}
}

func TestCoordinatorTodoFinishedEarlierTurnRemindedToDropDone(t *testing.T) {
	repo, store, project := openTestEnv(t)
	conv := core.ConversationView{ID: project.CoordinatorID, ProjectID: project.ID, Role: "coordinator"}

	step1 := 0
	runToolTurn(t, repo, store, conv, nil, func(_ sdk.Request) iter.Seq[sdk.Part] {
		current := step1
		step1++
		return func(yield func(sdk.Part) bool) {
			if current == 0 {
				yield(updateTodoCall(0, "done"))
				yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
				return
			}
			yield(sdk.Part{Type: "finish", Reason: "stop"})
		}
	})

	step2 := 0
	results := runToolTurn(t, repo, store, conv, nil, func(_ sdk.Request) iter.Seq[sdk.Part] {
		current := step2
		step2++
		return func(yield func(sdk.Part) bool) {
			if current < todoStaleAfter+2 {
				yield(lookMessageCall(current))
				yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
				return
			}
			yield(sdk.Part{Type: "finish", Reason: "stop"})
		}
	})

	var hintedAt []int
	for i, res := range results {
		if strings.Contains(res.content, coordinatorTodoFinishedHint) {
			hintedAt = append(hintedAt, i)
		}
		if strings.Contains(res.content, todoFinishedHint) ||
			strings.Contains(res.content, todoMissingHint) ||
			strings.Contains(res.content, todoStaleHint) ||
			strings.Contains(res.content, coordinatorTodoMissingHint) ||
			strings.Contains(res.content, coordinatorTodoStaleHint) {
			t.Fatalf("unexpected hint at index %d: %q", i, res.content)
		}
	}
	if len(hintedAt) != 1 || hintedAt[0] != todoMissingAfter-1 {
		t.Fatalf("coordinator finished hint indices = %v, want [%d]", hintedAt, todoMissingAfter-1)
	}
	if !strings.Contains(results[hintedAt[0]].content, "<current_todo>") ||
		!strings.Contains(results[hintedAt[0]].content, "- [done] 执行步骤") {
		t.Fatalf("coordinator finished hint should include current todo items: %q", results[hintedAt[0]].content)
	}
}
