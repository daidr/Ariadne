package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/dashboard"
	"github.com/daidr/Ariadne/internal/models"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/sdk"
)

func TestCallGroups(t *testing.T) {
	byName := map[string]tool{
		"read":  {spec: sdk.Tool{Name: "read"}, readOnly: true},
		"write": {spec: sdk.Tool{Name: "write"}},
	}
	makeCalls := func(names ...string) []sdk.ToolCall {
		out := make([]sdk.ToolCall, len(names))
		for i, name := range names {
			out[i] = sdk.ToolCall{ID: fmt.Sprintf("c%d", i), Name: name, Arguments: "{}"}
		}
		return out
	}

	cases := []struct {
		name  string
		calls []sdk.ToolCall
		want  [][]int
	}{
		{
			name:  "empty",
			calls: nil,
			want:  [][]int{},
		},
		{
			name:  "all read-only merge into one group",
			calls: makeCalls("read", "read", "read"),
			want:  [][]int{{0, 1, 2}},
		},
		{
			name:  "all mutating run alone",
			calls: makeCalls("write", "write"),
			want:  [][]int{{0}, {1}},
		},
		{
			name:  "mixed adjacent read-only, mutating and unknown",
			calls: makeCalls("read", "read", "write", "read", "unknown", "read", "read"),
			want:  [][]int{{0, 1}, {2}, {3}, {4}, {5, 6}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := callGroups(tc.calls, byName)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("callGroups() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBuiltinToolReadOnlyAnnotations(t *testing.T) {
	fix := newFileStateFixture(t, "", nil)
	ctx := t.Context()

	fix.runner.Dashboards = dashboard.New()

	project, err := fix.repo.GetProjectForRun(ctx, fix.project.ID)
	if err != nil {
		t.Fatal(err)
	}

	threadTools := fix.turn.tools(ctx, project)
	threadTools = append(threadTools, fix.turn.fetchTool(), fix.turn.browserTool(), fix.turn.researchTool(threadTools, nil, nil))
	threadTools = append(threadTools, fix.turn.repoTools(&sdk.RepoCapability{
		CheckLog: func(context.Context, string) (string, error) { return "", nil },
	}, false)...)
	threadTools = append(threadTools, fix.turn.issueTools(&sdk.TrackerCapability{
		List:  func(context.Context, sdk.IssueQuery) ([]sdk.IssueSummary, error) { return nil, nil },
		Issue: func(context.Context, int) (sdk.IssueDetail, error) { return sdk.IssueDetail{}, nil },
	}, true)...)
	threadByName := make(map[string]tool, len(threadTools))
	for _, entry := range threadTools {
		threadByName[entry.spec.Name] = entry
	}

	wantReadOnlyThread := []string{
		"list_dir", "read_file", "search", "find_files", "read_attachment",
		"search_messages", "list_messages", "read_plan",
		"list_artifacts", "read_artifact", "list_dashboard_sources",
		"fetch_url", "research", "repo_status", "read_check_log",
		"list_issues", "read_issue",
	}
	for _, name := range wantReadOnlyThread {
		entry, ok := threadByName[name]
		if !ok {
			t.Fatalf("expected thread tool %q to be registered", name)
		}
		if !entry.readOnly {
			t.Errorf("expected tool %q to be readOnly=true", name)
		}
	}

	wantMutatingThread := []string{
		"browser", "write_file", "edit_file", "apply_patch", "save_attachment",
		"run_command", "read_command_output", "stop_command",
		"send_message", "post_ui", "update_todo", "edit_message", "react",
		"create_document", "update_document", "refresh_dashboard", "report",
		"sync_branch", "push_branch", "open_pull_request", "link_issue",
	}
	for _, name := range wantMutatingThread {
		entry, ok := threadByName[name]
		if !ok {
			t.Fatalf("expected thread tool %q to be registered", name)
		}
		if entry.readOnly {
			t.Errorf("expected tool %q to be readOnly=false", name)
		}
	}

	// Verify reviewer reporting tools.
	reviewTools := fix.turn.reportingTools(core.ThreadView{ID: "thr_rev", ReviewOf: fix.thread.ID})
	reviewByName := make(map[string]tool, len(reviewTools))
	for _, entry := range reviewTools {
		reviewByName[entry.spec.Name] = entry
	}
	if !reviewByName["read_changes"].readOnly {
		t.Error("expected read_changes to be readOnly=true")
	}
	if reviewByName["submit_review"].readOnly {
		t.Error("expected submit_review to be readOnly=false")
	}

	coordTurn := &turn{
		Runner:       fix.runner,
		conversation: core.ConversationView{ID: fix.project.CoordinatorID, ProjectID: fix.project.ID, Role: "coordinator"},
	}
	coordTools := coordTurn.tools(ctx, project)
	coordByName := make(map[string]tool, len(coordTools))
	for _, entry := range coordTools {
		coordByName[entry.spec.Name] = entry
	}
	for _, name := range []string{"list_threads", "read_thread"} {
		entry, ok := coordByName[name]
		if !ok {
			t.Fatalf("expected coordinator tool %q to be registered", name)
		}
		if !entry.readOnly {
			t.Errorf("expected coordinator tool %q to be readOnly=true", name)
		}
	}
	for _, name := range []string{"create_thread", "message_thread", "update_plan", "routine"} {
		entry, ok := coordByName[name]
		if !ok {
			t.Fatalf("expected coordinator tool %q to be registered", name)
		}
		if entry.readOnly {
			t.Errorf("expected coordinator tool %q to be readOnly=false", name)
		}
	}
}

func TestParallelReadOnlyExecutionAndModelOrder(t *testing.T) {
	repo, store, project := openTestEnv(t)
	conv := core.ConversationView{ID: project.CoordinatorID, ProjectID: project.ID, Role: "coordinator"}

	var (
		mu         sync.Mutex
		log        []string
		recordLine = func(line string) {
			mu.Lock()
			log = append(log, line)
			mu.Unlock()
		}
	)
	slowStarted := make(chan struct{})
	fastDone := make(chan struct{})
	slowDone := make(chan struct{})

	slow := readOnlySpec("slow_read", "slow", args{}, nil, func(ctx context.Context, _ args) (string, error) {
		recordLine("start slow")
		close(slowStarted)
		// Wait until fast_read has actually run concurrently and finished.
		select {
		case <-fastDone:
		case <-time.After(3 * time.Second):
			return "", errors.New("timed out waiting for concurrent fast_read")
		case <-ctx.Done():
			return "", ctx.Err()
		}
		recordLine("end slow")
		close(slowDone)
		return "slow-result", nil
	})
	fast := readOnlySpec("fast_read", "fast", args{}, nil, func(ctx context.Context, _ args) (string, error) {
		select {
		case <-slowStarted:
		case <-time.After(3 * time.Second):
			return "", errors.New("timed out waiting for slow_read to start")
		case <-ctx.Done():
			return "", ctx.Err()
		}
		recordLine("start fast")
		recordLine("end fast")
		close(fastDone)
		return "fast-result", nil
	})
	change := specification("mutate_state", "mutate", args{}, nil, func(ctx context.Context, _ args) (string, error) {
		select {
		case <-slowDone:
		default:
			return "", errors.New("mutate_state started before read-only group finished")
		}
		recordLine("mutate")
		return "mutated", nil
	})

	var requests []sdk.Request
	step := 0
	results := runToolTurn(t, repo, store, conv, []tool{slow, fast, change}, func(req sdk.Request) iter.Seq[sdk.Part] {
		requests = append(requests, req)
		return func(yield func(sdk.Part) bool) {
			step++
			if step == 1 {
				yield(makeCall("call_a", "slow_read", args{}))
				yield(makeCall("call_b", "fast_read", args{}))
				yield(makeCall("call_c", "mutate_state", args{}))
				yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
				return
			}
			yield(sdk.Part{Type: "finish", Reason: "stop"})
		}
	})

	// Verify execution actually overlapped and mutate ran strictly after both reads completed.
	mu.Lock()
	gotLog := append([]string(nil), log...)
	mu.Unlock()
	wantLog := []string{"start slow", "start fast", "end fast", "end slow", "mutate"}
	if !reflect.DeepEqual(gotLog, wantLog) {
		t.Fatalf("execution log = %v, want %v", gotLog, wantLog)
	}

	// Verify recorded transcript order strictly matches model's call order.
	if len(results) != 3 {
		t.Fatalf("expected 3 tool results, got %d: %+v", len(results), results)
	}
	wantNames := []string{"slow_read", "fast_read", "mutate_state"}
	wantContents := []string{"slow-result", "fast-result", "mutated"}
	for i := range wantNames {
		if results[i].name != wantNames[i] || results[i].content != wantContents[i] || results[i].isError {
			t.Fatalf("result[%d] = %+v, want name=%q content=%q isError=false", i, results[i], wantNames[i], wantContents[i])
		}
	}

	// Verify messages delivered back to the model in step 2 keep original tool call ID order.
	if len(requests) < 2 {
		t.Fatalf("expected at least 2 model requests, got %d", len(requests))
	}
	var toolMsgs []sdk.Message
	for _, msg := range requests[1].Messages {
		if msg.Role == "tool" {
			toolMsgs = append(toolMsgs, msg)
		}
	}
	if len(toolMsgs) != 3 || toolMsgs[0].ToolCallID != "call_a" || toolMsgs[1].ToolCallID != "call_b" || toolMsgs[2].ToolCallID != "call_c" {
		t.Fatalf("model received tool messages out of order: %+v", toolMsgs)
	}
}

func TestParallelBatchDigestDeterministicRegardlessOfCompletionOrder(t *testing.T) {
	runWithOrder := func(firstFinishesFirst bool) batchDigest {
		repo, store, project := openTestEnv(t)
		ctx := t.Context()
		conv := core.ConversationView{ID: project.CoordinatorID, ProjectID: project.ID, Role: "coordinator"}
		if _, err := repo.PostMessage(ctx, project.ID, core.MessageInput{
			ConversationID: conv.ID, Author: "user", Content: "check digest",
		}); err != nil {
			t.Fatal(err)
		}
		turnID, err := repo.StartScheduledTurn(ctx, conv.ID, "mock")
		if err != nil {
			t.Fatal(err)
		}
		gate := make(chan struct{})
		t1 := readOnlySpec("ro_one", "one", args{}, nil, func(ctx context.Context, _ args) (string, error) {
			if !firstFinishesFirst {
				<-gate
			} else {
				close(gate)
			}
			return "one", nil
		})
		t2 := readOnlySpec("ro_two", "two", args{}, nil, func(ctx context.Context, _ args) (string, error) {
			if firstFinishesFirst {
				<-gate
			} else {
				close(gate)
			}
			return "two", nil
		})
		active := &turn{
			Runner:       &Runner{Repository: repo, Settings: store},
			id:           turnID,
			conversation: conv,
		}
		calls := []sdk.ToolCall{
			{ID: "c1", Name: "ro_one", Arguments: `{}`},
			{ID: "c2", Name: "ro_two", Arguments: `{}`},
		}
		digest, err := active.execute(ctx, []tool{t1, t2}, calls)
		if err != nil {
			t.Fatal(err)
		}
		return digest
	}

	d1 := runWithOrder(true)
	d2 := runWithOrder(false)
	if d1.hash != d2.hash {
		t.Fatalf("batchDigest hash depended on completion order: %x != %x", d1.hash, d2.hash)
	}
}

func TestParallelCoordinatorLooksQuotaAndHintDeterministic(t *testing.T) {
	repo, store, project := openTestEnv(t)
	conv := core.ConversationView{ID: project.CoordinatorID, ProjectID: project.ID, Role: "coordinator"}

	var executed atomic.Int32
	allAllowedStarted := make(chan struct{})
	var startedCount atomic.Int32
	var closeOnce sync.Once

	readStub := readOnlySpec("read_file", "read", args{"path": field("string")}, nil, func(ctx context.Context, input args) (string, error) {
		n := startedCount.Add(1)
		if n == coordinatorLookLimit {
			closeOnce.Do(func() { close(allAllowedStarted) })
		}
		select {
		case <-allAllowedStarted:
		case <-time.After(3 * time.Second):
			return "", errors.New("timed out waiting for parallel read_file batch")
		case <-ctx.Done():
			return "", ctx.Err()
		}
		executed.Add(1)
		return "file:" + text(input, "path"), nil
	})

	step := 0
	const totalCalls = 14
	results := runToolTurn(t, repo, store, conv, []tool{readStub}, func(_ sdk.Request) iter.Seq[sdk.Part] {
		return func(yield func(sdk.Part) bool) {
			step++
			if step == 1 {
				for i := 1; i <= totalCalls; i++ {
					yield(makeCall(fmt.Sprintf("r_%d", i), "read_file", args{"path": fmt.Sprintf("f%d.go", i)}))
				}
				yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
				return
			}
			yield(sdk.Part{Type: "finish", Reason: "stop"})
		}
	})

	if executed.Load() != coordinatorLookLimit {
		t.Fatalf("expected exactly %d executed calls, got %d", coordinatorLookLimit, executed.Load())
	}
	if len(results) != totalCalls {
		t.Fatalf("expected %d recorded results, got %d", totalCalls, len(results))
	}
	for i := 0; i < coordinatorLookLimit; i++ {
		if results[i].isError {
			t.Fatalf("call %d should succeed, got error: %q", i+1, results[i].content)
		}
		wantPrefix := fmt.Sprintf("file:f%d.go", i+1)
		if !strings.HasPrefix(results[i].content, wantPrefix) {
			t.Fatalf("call %d content = %q, want prefix %q", i+1, results[i].content, wantPrefix)
		}
		hasHint := strings.Contains(results[i].content, coordinatorLookHintText)
		if i+1 == coordinatorLookHint && !hasHint {
			t.Fatalf("call %d should deterministically carry coordinator look hint, got %q", i+1, results[i].content)
		}
		if i+1 != coordinatorLookHint && hasHint {
			t.Fatalf("call %d should not carry coordinator look hint, got %q", i+1, results[i].content)
		}
	}
	for i := coordinatorLookLimit; i < totalCalls; i++ {
		if !results[i].isError || results[i].content != coordinatorLookRefusedText {
			t.Fatalf("call %d should be refused, got isError=%v content=%q", i+1, results[i].isError, results[i].content)
		}
	}
}

func TestParallelCoordinatorResearchWeightPreDeduction(t *testing.T) {
	repo, store, project := openTestEnv(t)
	conv := core.ConversationView{ID: project.CoordinatorID, ProjectID: project.ID, Role: "coordinator"}

	var researchRuns, readRuns atomic.Int32
	researchStub := readOnlySpec("research", "research", args{"question": field("string")}, nil, func(_ context.Context, input args) (string, error) {
		researchRuns.Add(1)
		return "ans:" + text(input, "question"), nil
	})
	readStub := readOnlySpec("read_file", "read", args{"path": field("string")}, nil, func(_ context.Context, input args) (string, error) {
		readRuns.Add(1)
		return "read:" + text(input, "path"), nil
	})

	step := 0
	results := runToolTurn(t, repo, store, conv, []tool{researchStub, readStub}, func(_ sdk.Request) iter.Seq[sdk.Part] {
		return func(yield func(sdk.Part) bool) {
			step++
			if step == 1 {
				// Step 1: 10 reads -> looks == 10
				for i := 1; i <= 10; i++ {
					yield(makeCall(fmt.Sprintf("s1_%d", i), "read_file", args{"path": fmt.Sprintf("a%d.go", i)}))
				}
				yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
				return
			}
			if step == 2 {
				// Step 2 (concurrent group): research (10+3=13 > 12 -> refused), read_file (10+1=11 -> ok), read_file (11+1=12 -> ok), read_file (12+1=13 -> refused)
				yield(makeCall("s2_res", "research", args{"question": "q"}))
				yield(makeCall("s2_r1", "read_file", args{"path": "b1.go"}))
				yield(makeCall("s2_r2", "read_file", args{"path": "b2.go"}))
				yield(makeCall("s2_r3", "read_file", args{"path": "b3.go"}))
				yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
				return
			}
			yield(sdk.Part{Type: "finish", Reason: "stop"})
		}
	})

	if researchRuns.Load() != 0 {
		t.Fatalf("expected research to be refused at looks=10, ran %d times", researchRuns.Load())
	}
	if readRuns.Load() != 12 {
		t.Fatalf("expected 12 total read_file runs, got %d", readRuns.Load())
	}
	if len(results) != 14 {
		t.Fatalf("expected 14 total results, got %d", len(results))
	}
	if !results[10].isError || results[10].content != coordinatorLookRefusedText {
		t.Fatalf("expected step 2 research refused, got %+v", results[10])
	}
	if results[11].isError || results[11].content != "read:b1.go" {
		t.Fatalf("expected step 2 first read_file to succeed, got %+v", results[11])
	}
	if results[12].isError || results[12].content != "read:b2.go" {
		t.Fatalf("expected step 2 second read_file to succeed, got %+v", results[12])
	}
	if !results[13].isError || results[13].content != coordinatorLookRefusedText {
		t.Fatalf("expected step 2 third read_file refused, got %+v", results[13])
	}
}

func TestParallelReadFilesAndFileStateRaceSafety(t *testing.T) {
	fix := newFileStateFixture(t, "", nil)
	ctx := t.Context()

	const fileCount = 8
	for i := 0; i < fileCount; i++ {
		name := fmt.Sprintf("file_%d.txt", i)
		body := fmt.Sprintf("line1-%d\nline2-%d\n", i, i)
		if err := os.WriteFile(filepath.Join(fix.dir, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := fix.repo.PostMessage(ctx, fix.project.ID, core.MessageInput{
		ConversationID: fix.thread.ID, Author: "user", Content: "read all files in parallel",
	}); err != nil {
		t.Fatal(err)
	}
	turnID, err := fix.repo.StartScheduledTurn(ctx, fix.thread.ID, "mock")
	if err != nil {
		t.Fatal(err)
	}

	const providerName = "mock"
	info := sdk.ModelProviderInfo{Kind: providerName, Program: true}
	registry := models.New()
	step := 0
	if _, err := registry.Register(sdk.ModelProvider{
		Info: info,
		Stream: func(_ context.Context, _ map[string]any, _ sdk.Request) iter.Seq[sdk.Part] {
			return func(yield func(sdk.Part) bool) {
				step++
				switch step {
				case 1:
					// Mix list_dir, find_files, search, and multiple read_file calls (including same file) in one parallel read-only group.
					yield(makeCall("c_list", "list_dir", args{}))
					yield(makeCall("c_find", "find_files", args{"pattern": "file_*.txt"}))
					yield(makeCall("c_search", "search", args{"pattern": "line1-"}))
					for i := 0; i < fileCount; i++ {
						yield(makeCall(fmt.Sprintf("c_read_%d", i), "read_file", args{"path": fmt.Sprintf("file_%d.txt", i)}))
					}
					// Overlapping same-file read in the same parallel batch to stress fileState locking.
					yield(makeCall("c_read_dup", "read_file", args{"path": "file_0.txt"}))
					yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
				default:
					yield(sdk.Part{Type: "finish", Reason: "stop"})
				}
			}
		},
	}); err != nil {
		t.Fatal(err)
	}

	fix.runner.Models = registry
	active := &turn{
		Runner:       fix.runner,
		id:           turnID,
		conversation: core.ConversationView{ID: fix.thread.ID, ProjectID: fix.project.ID, Role: "thread"},
		changes:      newTurnChanges(fix.runner, core.ConversationView{ID: fix.thread.ID, ProjectID: fix.project.ID, Role: "thread"}),
	}
	active.instructionsDirty.Store(true)
	if err := active.run(ctx, &settings.ResolvedModel{Provider: info, Model: providerName, Label: providerName}); err != nil {
		t.Fatalf("parallel read turn failed: %v", err)
	}

	// Verify all 8 files were recorded in fileState and detect subsequent external modifications.
	for i := 0; i < fileCount; i++ {
		name := fmt.Sprintf("file_%d.txt", i)
		if err := os.WriteFile(filepath.Join(fix.dir, name), []byte("externally-modified\n"), 0644); err != nil {
			t.Fatal(err)
		}
		_, err := active.writeFile(ctx, args{"path": name, "content": "overwrite"})
		if err == nil || !strings.Contains(err.Error(), "changed since you last read or wrote it") {
			t.Fatalf("expected external modification rejection for %s after parallel read, got %v", name, err)
		}
	}

	// Verify recorded tool result order in transcript matches model order.
	entries, err := fix.repo.ListTranscript(ctx, fix.thread.ID, core.TranscriptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var callIDs []string
	for _, entry := range entries {
		if entry["kind"] == "tool" {
			callIDs = append(callIDs, text(entry, "toolCallId"))
		}
	}
	wantIDs := []string{"c_list", "c_find", "c_search"}
	for i := 0; i < fileCount; i++ {
		wantIDs = append(wantIDs, fmt.Sprintf("c_read_%d", i))
	}
	wantIDs = append(wantIDs, "c_read_dup")
	if !reflect.DeepEqual(callIDs, wantIDs) {
		t.Fatalf("recorded toolCallIds = %v, want %v", callIDs, wantIDs)
	}
}

func TestParallelReadOnlyGroupCancellation(t *testing.T) {
	repo, store, project := openTestEnv(t)
	baseCtx := t.Context()
	conv := core.ConversationView{ID: project.CoordinatorID, ProjectID: project.ID, Role: "coordinator"}
	if _, err := repo.PostMessage(baseCtx, project.ID, core.MessageInput{
		ConversationID: conv.ID, Author: "user", Content: "cancel test",
	}); err != nil {
		t.Fatal(err)
	}
	turnID, err := repo.StartScheduledTurn(baseCtx, conv.ID, "mock")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(baseCtx)
	var started atomic.Int32
	blocker := readOnlySpec("block_read", "block", args{"id": field("string")}, nil, func(ctx context.Context, _ args) (string, error) {
		if started.Add(1) == 2 {
			cancel()
		}
		<-ctx.Done()
		return "", ctx.Err()
	})
	active := &turn{
		Runner:       &Runner{Repository: repo, Settings: store},
		id:           turnID,
		conversation: conv,
	}
	arg1, _ := json.Marshal(args{"id": "1"})
	arg2, _ := json.Marshal(args{"id": "2"})
	_, err = active.execute(ctx, []tool{blocker}, []sdk.ToolCall{
		{ID: "b1", Name: "block_read", Arguments: string(arg1)},
		{ID: "b2", Name: "block_read", Arguments: string(arg2)},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	entries, err := repo.ListTranscript(baseCtx, conv.ID, core.TranscriptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry["kind"] == "tool" {
			t.Fatalf("cancelled parallel group should not record tool results, found %+v", entry)
		}
	}
}
