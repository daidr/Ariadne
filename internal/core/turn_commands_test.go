package core

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/eventstore"
)

var variableID = regexp.MustCompile(`[a-z]{3}_[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

func normalizedIDs(raw any) any {
	value := normalize(raw)
	seen := map[string]string{}
	counts := map[string]int{}
	var visit func(any) any
	visit = func(value any) any {
		switch node := value.(type) {
		case map[string]any:
			keys := []string{}
			for key := range node {
				keys = append(keys, key)
			}
			slices.Sort(keys)
			for _, key := range keys {
				node[key] = visit(node[key])
			}
		case []any:
			for i := range node {
				node[i] = visit(node[i])
			}
		case string:
			return variableID.ReplaceAllStringFunc(node, func(id string) string {
				if named, ok := seen[id]; ok {
					return named
				}
				prefix := id[:3]
				counts[prefix]++
				named := prefix + "_" + encoded(counts[prefix])
				seen[id] = named
				return named
			})
		}
		return value
	}
	return visit(value)
}
func decodeInput(t *testing.T, raw any, target any) {
	t.Helper()
	if err := json.Unmarshal([]byte(encoded(raw)), target); err != nil {
		t.Fatal(err)
	}
}

func TestTurnCommandsAndRecoveryMatchLegacy(t *testing.T) {
	seed := []eventstore.NewEvent{{Type: "project.created", ProjectID: "prj_a", Data: json.RawMessage(`{"name":"项目","coordinatorId":"crd_a"}`)}, {Type: "thread.created", ProjectID: "prj_a", Data: json.RawMessage(`{"threadId":"thr_a","title":"任务","nodeId":null,"model":null,"branch":null}`)}}
	commands := []object{}
	add := func(op string, args object) {
		if args["id"] == nil {
			args["id"] = "thr_a"
		}
		commands = append(commands, object{"op": op, "args": args})
	}
	input := object{"system": "固定说明", "tools": "[]", "call": "{}", "images": true}
	timing := object{"startedAt": 1800000000000, "firstOutputAt": nil}
	usage := object{"input": 100, "output": 20, "cached": 80, "reasoning": 4, "cost": object{"currency": "USD", "amount": 0.003}}
	step := func(calls []object) object {
		return object{"text": "正在读取文件", "reasoning": []object{{"text": "思考", "signature": "signature_a"}}, "toolCalls": calls, "sawSeq": 0, "usage": usage, "input": input, "timing": timing}
	}
	add("request", object{})
	add("changes", object{"input": object{"from": "a", "to": "b", "files": []object{}}})
	add("request", object{})
	add("start", object{"model": "model_a"})
	add("start", object{"model": "model_b"})
	add("request", object{})
	add("step", object{"step": step([]object{{"id": "call_a", "name": "read", "arguments": "{}"}})})
	changedFiles := []object{{"path": "main.go", "previousPath": nil, "status": "modified", "added": 4, "removed": 1}, {"path": "image.png", "previousPath": nil, "status": "added", "added": nil, "removed": nil}}
	add("changes", object{"input": object{"from": "head_a", "to": "head_b", "files": changedFiles, "branch": object{"files": 2, "added": 4, "removed": 1}}})
	manyFiles := []object{}
	for i := 0; i < 201; i++ {
		manyFiles = append(manyFiles, object{"path": "file_" + encoded(i), "previousPath": nil, "status": "added", "added": 1, "removed": 0})
	}
	add("changes", object{"input": object{"from": "head_b", "to": "head_c", "files": manyFiles}})
	add("changes", object{"wrong": true, "input": object{"from": "a", "to": "b", "files": changedFiles}})
	add("retry", object{"attempt": 2, "delayMs": 1000, "detail": "繁忙"})
	add("fallback", object{"from": "model_a", "to": "model_b", "detail": "切换"})
	add("search", object{"model": "search_model", "usage": nil})
	add("research", object{"step": object{"toolCallId": "call_research", "text": "调查", "reasoning": []any{}, "toolCalls": []any{}, "results": []any{}, "usage": object{"input": 3, "output": 1, "cost": object{"currency": "CNY", "amount": 0.001}}, "input": object{"system": "研究说明", "tools": "[]", "call": "{}"}, "timing": timing}})
	for _, file := range []object{{"path": "AGENTS.md", "hash": "hash_a", "content": "说明", "uncommitted": false}, {"path": "AGENTS.md", "hash": "hash_a", "content": "说明", "uncommitted": false}, {"path": "AGENTS.md", "hash": "hash_b", "content": "新说明", "uncommitted": true}, {"path": "AGENTS.md", "hash": "removed", "content": nil}} {
		add("instructions", object{"file": file})
	}
	add("prompt", object{"base": true, "sections": [][2]string{{"base", "说明"}}, "tools": "[]"})
	add("prompt", object{"base": false, "sections": [][2]string{}, "removedTools": []string{"tool_a"}})
	add("compact", object{"compaction": object{"summary": "  对话摘要  ", "upTo": 1, "sawSeq": 4, "usage": nil, "call": "{}", "timing": timing, "reasoning": []object{{"text": "保留", "signature": "strip"}, {"text": "redacted", "redacted": true}, {"text": ""}}, "state": "{}", "userTokens": 100}})
	add("compact", object{"compaction": object{"summary": " ", "upTo": 1, "sawSeq": 4, "usage": nil, "call": "{}", "timing": timing, "reasoning": []any{}, "state": "{}"}})
	add("compaction-failed", object{"detail": "上下文过长"})
	add("clear", object{"upTo": 2, "sawSeq": 4})
	add("tool", object{"result": object{"toolCallId": "call_a", "name": "read", "content": "文件内容", "isError": false, "files": []object{{"fileId": "fil_a", "name": "image.png", "mime": "image/png", "size": 1}}, "timing": object{"startedAt": 1800000000000, "finishedAt": 1800000000001}}})
	add("step", object{"step": step([]object{{"id": "call_b", "name": "write", "arguments": "{}"}}), "all": true})
	add("finish", object{"outcome": "cancelled"})
	add("step", object{"step": step([]object{}), "wrong": true})
	add("finish", object{"outcome": "failed", "wrong": true})
	add("start", object{"model": "model_a"})
	add("finish", object{"outcome": "interrupted"})
	for repeat := 0; repeat < 2; repeat++ {
		add("start", object{"model": "model_a"})
		empty := step([]object{})
		empty["usage"] = nil
		empty["text"] = ""
		add("step", object{"step": empty, "all": true})
		add("finish", object{"outcome": "completed"})
	}
	add("wait", object{"until": 1800000010000, "note": "检查一次"})
	add("wait", object{"until": 1800000020000, "note": "检查两次"})
	add("end-wait", object{"how": "now", "by": "user", "wrong": true})
	add("end-wait", object{"how": "now", "by": "user"})
	add("end-wait", object{"how": "due", "by": "thr_a"})
	add("wait", object{"until": 1800000030000, "note": "取消检查"})
	add("end-wait", object{"how": "cancelled", "by": "user"})
	add("wait", object{"until": 1800000040000, "note": "自行取消"})
	add("end-wait", object{"how": "cancelled", "by": "thr_a"})
	add("fail", object{"id": "crd_a"})
	add("fail", object{"id": "crd_a", "error": object{"key": "error.modelRequest", "params": object{"detail": "失败"}}})
	var expected []any
	oracle(t, "packages/core/src/testing/go-turns-fixture.ts", object{"seed": seed, "commands": commands}, &expected)
	repo, err := Open(t.Context(), Options{Path: filepath.Join(t.TempDir(), "turns.sqlite"), Now: func() time.Time { return time.UnixMilli(1800000000000) }})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if _, err := repo.Store.Append(t.Context(), seed); err != nil {
		t.Fatal(err)
	}
	turns, waits := map[string]string{}, map[string]string{}
	for i, command := range commands {
		a := asObject(jsonValue(command["args"]))
		id := a.str("id")
		turn := turns[id]
		if a.yes("wrong") {
			turn = "wrong_turn"
		}
		var value any
		var err error
		switch command.str("op") {
		case "request":
			err = repo.RequestTurn(t.Context(), id)
		case "start":
			var started string
			started, err = repo.StartTurn(t.Context(), id, a.str("model"))
			if err == nil {
				turns[id] = started
				value = started
			}
		case "step":
			var step Step
			decodeInput(t, a["step"], &step)
			if a.yes("all") {
				step.SawSeq, err = repo.Store.LastSeq(t.Context())
				if err != nil {
					t.Fatal(err)
				}
			}
			err = repo.RecordStep(t.Context(), id, turn, step)
		case "retry":
			err = repo.RecordModelRetry(t.Context(), id, turn, int(a["attempt"].(float64)), int64(a["delayMs"].(float64)), a.str("detail"))
		case "changes":
			var input struct {
				From   string
				To     string
				Files  []TurnFile
				Branch *ChangeTotals
			}
			decodeInput(t, a["input"], &input)
			err = repo.RecordTurnChanges(t.Context(), id, turn, input.From, input.To, input.Files, input.Branch)
		case "fallback":
			err = repo.RecordModelFallback(t.Context(), id, turn, a.str("from"), a.str("to"), a.str("detail"))
		case "search":
			var usage *TokenUsage
			decodeInput(t, a["usage"], &usage)
			err = repo.RecordSearch(t.Context(), id, turn, a.str("model"), usage)
		case "research":
			err = repo.RecordResearchStep(t.Context(), id, turn, a.obj("step"))
		case "instructions":
			var file InstructionFile
			decodeInput(t, a["file"], &file)
			value, err = repo.RecordInstructions(t.Context(), id, turn, file)
		case "prompt":
			var sections [][2]string
			var removed []string
			decodeInput(t, a["sections"], &sections)
			decodeInput(t, a["removedTools"], &removed)
			err = repo.UpdatePrompt(t.Context(), id, turn, a.yes("base"), sections, a.str("tools"), removed)
		case "compact":
			var compact Compaction
			decodeInput(t, a["compaction"], &compact)
			err = repo.CompactConversation(t.Context(), id, turn, compact)
		case "compaction-failed":
			err = repo.RecordCompactionFailure(t.Context(), id, turn, a.str("detail"))
		case "clear":
			err = repo.ClearToolResults(t.Context(), id, turn, a["upTo"].(float64), int64(a["sawSeq"].(float64)))
		case "tool":
			var result ToolResult
			decodeInput(t, a["result"], &result)
			err = repo.RecordToolResult(t.Context(), id, turn, result)
		case "finish":
			var failure *domainerr.Error
			decodeInput(t, a["error"], &failure)
			err = repo.FinishTurn(t.Context(), id, turn, a.str("outcome"), failure)
		case "fail":
			var failure *domainerr.Error
			decodeInput(t, a["error"], &failure)
			err = repo.FailBeforeTurn(t.Context(), id, failure)
		case "wait":
			var wait string
			wait, err = repo.SetWait(t.Context(), id, int64(a["until"].(float64)), a.str("note"))
			if err == nil {
				waits[id] = wait
				value = wait
			}
		case "end-wait":
			wait := waits[id]
			if a.yes("wrong") {
				wait = "wrong_wait"
			}
			value, err = repo.EndWait(t.Context(), id, a.str("how"), a.str("by"), &wait)
		default:
			t.Fatal(command)
		}
		var localized any
		if err != nil {
			var domain *domainerr.Error
			if !errors.As(err, &domain) {
				t.Fatalf("command %s: %v", encoded(command), err)
			}
			localized = domain
			value = nil
		}
		stored, err := repo.Store.Read(t.Context(), eventstore.ReadOptions{})
		if err != nil {
			t.Fatal(err)
		}
		events := []object{}
		for _, e := range stored {
			events = append(events, object{"seq": e.Seq, "type": e.Type, "projectId": e.ProjectID, "data": e.Data, "at": e.At})
		}
		thread, err := repo.GetThread(t.Context(), "thr_a")
		if err != nil {
			t.Fatal(err)
		}
		reads := []object{}
		for _, id := range []string{"crd_a", "thr_a"} {
			conversation, err := repo.GetConversation(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			transcript, err := repo.ListTranscript(t.Context(), id, TranscriptOptions{})
			if err != nil {
				t.Fatal(err)
			}
			todo, err := repo.GetTodo(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			triage, err := repo.PendingIsTriage(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			userMessage, err := repo.PendingIsUserMessage(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			instructions, err := repo.GetInstructions(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			count, readErr := repo.CountRequests(t.Context(), id, maxSafeInteger)
			if readErr != nil {
				t.Fatal(readErr)
			}
			last, readErr := repo.LastStepBefore(t.Context(), id, maxSafeInteger)
			if readErr != nil {
				t.Fatal(readErr)
			}
			research, readErr := repo.ResearchSteps(t.Context(), id, "call_research", maxSafeInteger)
			if readErr != nil {
				t.Fatal(readErr)
			}
			var before *int64
			for _, event := range stored {
				var data object
				decodeInput(t, event.Data, &data)
				if event.Type == "turn.started" && data.str("conversationId") == id {
					seq := event.Seq
					before = &seq
				}
			}
			one, two := 1, 2
			trace := []TurnEventPage{}
			for _, options := range []TurnEventOptions{{}, {Turns: &one}, {Turns: &two, Before: before}} {
				page, readErr := repo.ListTurnEvents(t.Context(), id, options)
				if readErr != nil {
					t.Fatal(readErr)
				}
				trace = append(trace, page)
			}
			reads = append(reads, object{"conversation": conversation, "transcript": transcript, "todo": todo, "triage": triage, "userMessage": userMessage, "instructions": instructions, "count": count, "last": last, "research": research, "trace": trace})
		}
		prompts, err := records(t.Context(), repo.Store.DB(), "SELECT * FROM prompts ORDER BY hash")
		if err != nil {
			t.Fatal(err)
		}
		waiting, err := repo.ListWaits(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		pending, err := repo.ListPendingConversations(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		running, err := repo.ListRunningConversations(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		actual := object{"value": value, "error": localized, "events": events, "thread": thread, "reads": reads, "prompts": prompts, "waits": waiting, "pending": pending, "running": running}
		if !reflect.DeepEqual(normalizedIDs(actual), normalizedIDs(expected[i])) {
			t.Fatalf("command %d %s differs\ngot %s\nwant%s", i, encoded(command), encoded(normalizedIDs(actual)), encoded(normalizedIDs(expected[i])))
		}
	}
}
