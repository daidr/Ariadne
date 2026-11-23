package runner

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/local"
	"github.com/daidr/Ariadne/internal/models"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/internal/sources"
	"github.com/daidr/Ariadne/internal/workspaces"
	"github.com/daidr/Ariadne/sdk"
)

func TestFileStateCheckHelper(t *testing.T) {
	if !slices.Contains(os.Args, "--file-state-check-helper") {
		return
	}
	data, err := os.ReadFile("check-mode.txt")
	if err != nil {
		fmt.Fprintln(os.Stderr, "missing check-mode.txt")
		os.Exit(1)
	}
	mode := strings.TrimSpace(string(data))
	if mode == "pass" {
		os.Exit(0)
	}
	fmt.Fprintln(os.Stdout, mode)
	os.Exit(1)
}

func TestFileStateLRUAndNormalization(t *testing.T) {
	var state fileState

	// Path normalization across slash styles and relative prefixes.
	state.noteFile("conv-1", "src-a", `dir\sub\..\file.go`, []byte("v1"))
	if !state.hasSeen("conv-1", "src-a", "./dir/file.go") {
		t.Fatal("expected normalized path to match seen record")
	}
	if state.changedSince("conv-1", "src-a", "dir/file.go", []byte("v1")) {
		t.Fatal("identical content should not be reported as changed")
	}
	if !state.changedSince("conv-1", "src-a", "./dir/file.go", []byte("v2")) {
		t.Fatal("modified content should be reported as changed")
	}

	// Isolation across different sources with the same relative path.
	if state.hasSeen("conv-1", "src-b", "dir/file.go") {
		t.Fatal("different source should not share seen state")
	}
	if state.changedSince("conv-1", "src-b", "dir/file.go", []byte("v2")) {
		t.Fatal("unseen source should not report changed")
	}

	// Read deduplication isolates by source and range, and normalizes paths.
	if state.noteRead("conv-1", "src-a", "./dir/file.go", 1, 240, []byte("v1")) {
		t.Fatal("first read should not be marked duplicate")
	}
	if !state.noteRead("conv-1", "src-a", `dir\file.go`, 1, 240, []byte("v1")) {
		t.Fatal("same range and content should be marked duplicate")
	}
	if state.noteRead("conv-1", "src-b", "dir/file.go", 1, 240, []byte("v1")) {
		t.Fatal("different source should not trigger read deduplication")
	}
	if state.noteRead("conv-1", "src-a", "dir/file.go", 1, 50, []byte("v1")) {
		t.Fatal("different limit should not trigger read deduplication")
	}
	if state.noteRead("conv-1", "src-a", "dir/file.go", 1, 240, []byte("v2")) {
		t.Fatal("changed content should not trigger read deduplication")
	}

	// forgetReads clears reads and failures while preserving seen.
	if state.noteCheckFailure("conv-1", "boom", true) {
		t.Fatal("first failure should not be marked repeated")
	}
	if !state.noteCheckFailure("conv-1", "boom", true) {
		t.Fatal("second identical failure should be marked repeated")
	}
	state.forgetReads("conv-1")
	if state.noteRead("conv-1", "src-a", "dir/file.go", 1, 240, []byte("v2")) {
		t.Fatal("forgetReads should clear previous read records")
	}
	if state.noteCheckFailure("conv-1", "boom", true) {
		t.Fatal("forgetReads should clear previous check failure")
	}
	if !state.changedSince("conv-1", "src-a", "dir/file.go", []byte("v2")) {
		t.Fatal("forgetReads must preserve seen records for external modification detection")
	}

	// LRU eviction caps tracked conversations at maxTrackedConversations (200).
	var lru fileState
	for i := 0; i < maxTrackedConversations; i++ {
		id := fmt.Sprintf("c-%d", i)
		lru.noteFile(id, "s", "a.txt", []byte("v1"))
		lru.noteRead(id, "s", "a.txt", 1, 240, []byte("v1"))
		lru.noteCheckFailure(id, "err", true)
	}
	// Touch c-0 so c-1 becomes the least recently used entry.
	lru.noteFile("c-0", "s", "a.txt", []byte("v1"))
	lru.noteRead("c-0", "s", "a.txt", 1, 240, []byte("v1"))
	lru.noteCheckFailure("c-0", "err", true)

	// Insert conversation #200 -> evicts c-1, retains c-0.
	lru.noteFile("c-extra", "s", "a.txt", []byte("v1"))
	lru.noteRead("c-extra", "s", "a.txt", 1, 240, []byte("v1"))
	lru.noteCheckFailure("c-extra", "err", true)

	if !lru.hasSeen("c-0", "s", "a.txt") {
		t.Fatal("recently touched c-0 should stay in seen LRU")
	}
	if lru.hasSeen("c-1", "s", "a.txt") {
		t.Fatal("oldest c-1 should have been evicted from seen LRU")
	}
	if !lru.noteRead("c-0", "s", "a.txt", 1, 240, []byte("v1")) {
		t.Fatal("recently touched c-0 should stay in reads LRU")
	}
	if lru.noteRead("c-1", "s", "a.txt", 1, 240, []byte("v1")) {
		t.Fatal("oldest c-1 should have been evicted from reads LRU")
	}
	if !lru.noteCheckFailure("c-0", "err", true) {
		t.Fatal("recently touched c-0 should stay in failures LRU")
	}
	if lru.noteCheckFailure("c-1", "err", true) {
		t.Fatal("oldest c-1 should have been evicted from failures LRU")
	}
}

type fileStateFixture struct {
	home       string
	dir        string
	repo       *core.Repository
	store      *settings.Store
	workspaces *workspaces.Manager
	runner     *Runner
	project    core.ProjectView
	thread     core.ThreadView
	turn       *turn
}

func newFileStateFixture(t *testing.T, configJSON string, start *core.ProjectStart) *fileStateFixture {
	t.Helper()
	ctx := t.Context()
	home, dir := t.TempDir(), t.TempDir()
	if configJSON == "" {
		configJSON = `{}`
	}
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(configJSON), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := settings.Open(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	repo, err := core.Open(ctx, core.Options{Path: filepath.Join(home, "ariadne.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	registry := sources.New()
	if _, err := registry.Register(local.DirectoryProvider(filepath.Join(home, "local"), nil, true)); err != nil {
		t.Fatal(err)
	}
	work, err := workspaces.New(workspaces.Options{Repository: repo, Sources: registry})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = work.Close(context.Background()) })

	projectStart := core.ProjectStart{Source: args{"kind": "local-directory", "settings": args{"path": dir}}}
	if start != nil {
		projectStart = *start
	}
	project, err := repo.CreateProject(ctx, new("File state fixture"), projectStart)
	if err != nil {
		t.Fatal(err)
	}
	thread, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "File state thread"})
	if err != nil {
		t.Fatal(err)
	}
	r := &Runner{Repository: repo, Settings: store, Workspaces: work}
	conv := core.ConversationView{ID: thread.ID, ProjectID: project.ID, Role: "thread"}
	active := &turn{Runner: r, conversation: conv, changes: newTurnChanges(r, conv)}
	return &fileStateFixture{
		home: home, dir: dir, repo: repo, store: store, workspaces: work,
		runner: r, project: project, thread: thread, turn: active,
	}
}

func TestFileStateExternalModificationBlocksWrites(t *testing.T) {
	ctx := t.Context()
	fx := newFileStateFixture(t, "", nil)

	// Unread files can be created and edited without false positives.
	if err := os.WriteFile(filepath.Join(fx.dir, "unseen.txt"), []byte("alpha\nbeta\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.turn.writeFile(ctx, args{"path": "new.txt", "content": "fresh\n"}); err != nil {
		t.Fatalf("write_file on new file failed: %v", err)
	}
	if _, err := fx.turn.editFile(ctx, args{"path": "unseen.txt", "old": "alpha", "new": "ALPHA"}); err != nil {
		t.Fatalf("edit_file on unseen file failed: %v", err)
	}
	if _, err := fx.turn.applyPatch(ctx, args{"input": "*** Begin Patch\n*** Update File: unseen.txt\n@@\n ALPHA\n-beta\n+BETA\n*** End Patch"}); err != nil {
		t.Fatalf("apply_patch on just-edited file failed: %v", err)
	}

	// Read a file, then mutate it externally: write_file, edit_file, and apply_patch must all refuse.
	notesPath := filepath.Join(fx.dir, "notes.txt")
	if err := os.WriteFile(notesPath, []byte("line1\nline2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fx.turn.readFile(ctx, args{"path": "notes.txt"}); err != nil {
		t.Fatalf("read_file failed: %v", err)
	}
	external := "line1\nexternal edit\n"
	if err := os.WriteFile(notesPath, []byte(external), 0600); err != nil {
		t.Fatal(err)
	}

	if _, err := fx.turn.writeFile(ctx, args{"path": "notes.txt", "content": "clobber\n"}); err == nil || !strings.Contains(err.Error(), "changed since you last read or wrote it") {
		t.Fatalf("expected write_file refusal after external modification, got %v", err)
	}
	if _, err := fx.turn.editFile(ctx, args{"path": "notes.txt", "old": "line1", "new": "LINE1"}); err == nil || !strings.Contains(err.Error(), "changed since you last read or wrote it") {
		t.Fatalf("expected edit_file refusal after external modification, got %v", err)
	}
	patch := "*** Begin Patch\n*** Update File: notes.txt\n@@\n-line1\n+LINE1\n external edit\n*** End Patch"
	if _, err := fx.turn.applyPatch(ctx, args{"input": patch}); err == nil || !strings.Contains(err.Error(), "changed since you last read or wrote it") {
		t.Fatalf("expected apply_patch update refusal after external modification, got %v", err)
	}
	deletePatch := "*** Begin Patch\n*** Delete File: notes.txt\n*** End Patch"
	if _, err := fx.turn.applyPatch(ctx, args{"input": deletePatch}); err == nil || !strings.Contains(err.Error(), "changed since you last read or wrote it") {
		t.Fatalf("expected apply_patch delete refusal after external modification, got %v", err)
	}
	// Verify disk content stayed untouched.
	got, err := os.ReadFile(notesPath)
	if err != nil || string(got) != external {
		t.Fatalf("external edit was clobbered: got %q, err %v", string(got), err)
	}

	// Re-reading updates the snapshot so subsequent writes, edits and patches succeed.
	if content, _, err := fx.turn.readFile(ctx, args{"path": "notes.txt"}); err != nil || !strings.Contains(content, "external edit") {
		t.Fatalf("expected fresh content on re-read, got %q, err %v", content, err)
	}
	if _, err := fx.turn.writeFile(ctx, args{"path": "notes.txt", "content": "merged\nline2\n"}); err != nil {
		t.Fatalf("write_file after re-read failed: %v", err)
	}
	if _, err := fx.turn.editFile(ctx, args{"path": "notes.txt", "old": "merged", "new": "MERGED"}); err != nil {
		t.Fatalf("edit_file right after write_file failed: %v", err)
	}
	if _, err := fx.turn.applyPatch(ctx, args{"input": "*** Begin Patch\n*** Update File: notes.txt\n@@\n MERGED\n-line2\n+LINE2\n*** End Patch"}); err != nil {
		t.Fatalf("apply_patch right after edit_file failed: %v", err)
	}

	// External growth beyond 1 MiB should also block write_file.
	if err := os.WriteFile(notesPath, []byte(strings.Repeat("a", (1<<20)+64)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.turn.writeFile(ctx, args{"path": "notes.txt", "content": "small\n"}); err == nil || !strings.Contains(err.Error(), "changed since you last read or wrote it") {
		t.Fatalf("expected write_file refusal when file grew past 1 MiB externally, got %v", err)
	}
}

func TestFileStateMultiSourceAndResearchIsolation(t *testing.T) {
	ctx := t.Context()
	firstDir, secondDir := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(firstDir, "README.md"), []byte("same content\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secondDir, "README.md"), []byte("same content\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(firstDir, "secret.txt"), []byte("initial secret\n"), 0600); err != nil {
		t.Fatal(err)
	}

	set := core.FileSourceSet{PrimaryID: "src_first", Sources: []core.FileSource{
		{ID: "src_first", Name: "frontend", Kind: "local-directory", Settings: map[string]any{"path": firstDir}},
		{ID: "src_second", Name: "backend", Kind: "local-directory", Settings: map[string]any{"path": secondDir}},
	}}
	fx := newFileStateFixture(t, "", &core.ProjectStart{FileSources: &set})

	// Reading README.md from primary and secondary sources must not collide in read dedup.
	firstRead, _, err := fx.turn.readFile(ctx, args{"path": "README.md"})
	if err != nil || !strings.Contains(firstRead, "same content") {
		t.Fatalf("primary read failed: %q, %v", firstRead, err)
	}
	secondRead, _, err := fx.turn.readFile(ctx, args{"path": "README.md", "source": "backend"})
	if err != nil || !strings.Contains(secondRead, "same content") || strings.Contains(secondRead, "unchanged") {
		t.Fatalf("secondary source read should not be deduplicated by primary read: %q, %v", secondRead, err)
	}
	// Repeating primary read (even with explicit source name) resolves to the same source identity and deduplicates.
	primaryAgain, _, err := fx.turn.readFile(ctx, args{"path": "README.md", "source": "frontend"})
	if err != nil || !strings.Contains(primaryAgain, "unchanged since you read it") {
		t.Fatalf("expected primary read dedup across default and explicit source name, got %q, %v", primaryAgain, err)
	}

	// External edit in primary only blocks primary writes, not secondary writes.
	if err := os.WriteFile(filepath.Join(firstDir, "README.md"), []byte("external primary\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.turn.writeFile(ctx, args{"path": "README.md", "content": "attempt\n"}); err == nil {
		t.Fatal("expected primary write_file to fail after external edit")
	}
	if _, err := fx.turn.writeFile(ctx, args{"path": "README.md", "source": "backend", "content": "updated backend\n"}); err != nil {
		t.Fatalf("secondary write_file should not be affected by primary external edit: %v", err)
	}

	// Research sub-calls must neither pollute main thread state nor receive main thread dedup hints.
	readable := map[string]tool{"read_file": fx.turn.readFileTool()}
	resContent, resFailed := researchRead(ctx, readable, sdk.ToolCall{ID: "r1", Name: "read_file", Arguments: `{"path":"secret.txt"}`}, false)
	if resFailed || !strings.Contains(resContent, "initial secret") {
		t.Fatalf("research read_file failed: %q, failed=%v", resContent, resFailed)
	}
	// External edit to secret.txt must not block main thread write_file because only research read it.
	if err := os.WriteFile(filepath.Join(firstDir, "secret.txt"), []byte("external secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.turn.writeFile(ctx, args{"path": "secret.txt", "content": "thread wrote\n"}); err != nil {
		t.Fatalf("research read should not have polluted main thread seen state: %v", err)
	}
	// Main thread reading secret.txt for the first time gets full text, not a dedup message.
	mainRead, _, err := fx.turn.readFile(ctx, args{"path": "secret.txt"})
	if err != nil || !strings.Contains(mainRead, "thread wrote") || strings.Contains(mainRead, "unchanged") {
		t.Fatalf("main thread first read should return content, got %q, %v", mainRead, err)
	}
	// Research reading secret.txt after main thread read it still gets full text, never a dedup message.
	resAgain, resAgainFailed := researchRead(ctx, readable, sdk.ToolCall{ID: "r2", Name: "read_file", Arguments: `{"path":"secret.txt"}`}, false)
	if resAgainFailed || !strings.Contains(resAgain, "thread wrote") || strings.Contains(resAgain, "unchanged") {
		t.Fatalf("research read should not receive main thread dedup hint: %q, failed=%v", resAgain, resAgainFailed)
	}
}

func TestReadFileDeduplicationAndStatefulSession(t *testing.T) {
	ctx := t.Context()
	fx := newFileStateFixture(t, "", nil)
	filePath := filepath.Join(fx.dir, "a.ts")
	if err := os.WriteFile(filePath, []byte("const a = 1\nconst b = 2\n"), 0600); err != nil {
		t.Fatal(err)
	}

	first, _, err := fx.turn.readFile(ctx, args{"path": "a.ts"})
	if err != nil || !strings.Contains(first, "const a = 1") {
		t.Fatalf("first read failed: %q, %v", first, err)
	}
	second, _, err := fx.turn.readFile(ctx, args{"path": "./a.ts"})
	if err != nil || !strings.Contains(second, "unchanged since you read it") {
		t.Fatalf("expected dedup hint on repeated read, got %q, %v", second, err)
	}
	// Different range returns full content.
	partial, _, err := fx.turn.readFile(ctx, args{"path": "a.ts", "limit": float64(1)})
	if err != nil || !strings.Contains(partial, "const a = 1") || strings.Contains(partial, "const b = 2") {
		t.Fatalf("expected partial range read, got %q, %v", partial, err)
	}
	// Content change returns new content.
	if err := os.WriteFile(filePath, []byte("const a = 2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	updated, _, err := fx.turn.readFile(ctx, args{"path": "a.ts"})
	if err != nil || !strings.Contains(updated, "const a = 2") || strings.Contains(updated, "unchanged") {
		t.Fatalf("expected full content after modification, got %q, %v", updated, err)
	}
	// forgetReads clears dedup history.
	fx.runner.files.forgetReads(fx.thread.ID)
	afterForget, _, err := fx.turn.readFile(ctx, args{"path": "a.ts"})
	if err != nil || !strings.Contains(afterForget, "const a = 2") || strings.Contains(afterForget, "unchanged") {
		t.Fatalf("expected full content after forgetReads, got %q, %v", afterForget, err)
	}
	// Stateful sessions (agent programs) always receive full content.
	fx.turn.statefulSession = true
	statefulFirst, _, err := fx.turn.readFile(ctx, args{"path": "a.ts"})
	if err != nil || !strings.Contains(statefulFirst, "const a = 2") || strings.Contains(statefulFirst, "unchanged") {
		t.Fatalf("expected full content for stateful session, got %q, %v", statefulFirst, err)
	}
}

func TestCheckAfterEditFoldsRepeatedFailures(t *testing.T) {
	ctx := t.Context()
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fx := newFileStateFixture(t, `{"commands":{"rules":[{"pattern":"*","action":"allow"}]}}`, nil)

	shell, err := fx.workspaces.Shell(ctx, fx.project.ID)
	if err != nil {
		t.Fatal(err)
	}
	var checkCmd string
	switch shell.Name {
	case "pwsh", "powershell", "pwsh.exe", "powershell.exe":
		checkCmd = "& '" + strings.ReplaceAll(program, "'", "''") + "' '-test.run=^TestFileStateCheckHelper$' -- --file-state-check-helper"
	case "cmd", "cmd.exe":
		checkCmd = `"` + program + `" -test.run=^TestFileStateCheckHelper$ -- --file-state-check-helper`
	default:
		checkCmd = "'" + strings.ReplaceAll(program, "'", "'\"'\"'") + "' '-test.run=^TestFileStateCheckHelper$' -- --file-state-check-helper"
	}

	if _, err := fx.repo.SetProjectSettings(ctx, fx.project.ID, map[string]any{
		"workspace": map[string]any{"check": checkCmd},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fx.dir, "a.txt"), []byte("one\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fx.dir, "check-mode.txt"), []byte("old error elsewhere"), 0600); err != nil {
		t.Fatal(err)
	}

	first, err := fx.turn.editFile(ctx, args{"path": "a.txt", "old": "one", "new": "two"})
	if err != nil || !strings.Contains(first, "old error elsewhere") {
		t.Fatalf("expected full check failure on first edit, got %q, err %v", first, err)
	}
	second, err := fx.turn.editFile(ctx, args{"path": "a.txt", "old": "two", "new": "three"})
	if err != nil || !strings.Contains(second, "with the same output as after your previous edit") || strings.Contains(second, "old error elsewhere") {
		t.Fatalf("expected folded repeated check failure, got %q, err %v", second, err)
	}

	// forgetReads resets check failure folding.
	fx.runner.files.forgetReads(fx.thread.ID)
	third, err := fx.turn.editFile(ctx, args{"path": "a.txt", "old": "three", "new": "four"})
	if err != nil || !strings.Contains(third, "old error elsewhere") {
		t.Fatalf("expected full check failure after forgetReads, got %q, err %v", third, err)
	}

	// Passing check clears stored failure so a subsequent identical failure is printed in full again.
	if err := os.WriteFile(filepath.Join(fx.dir, "check-mode.txt"), []byte("pass"), 0600); err != nil {
		t.Fatal(err)
	}
	passed, err := fx.turn.editFile(ctx, args{"path": "a.txt", "old": "four", "new": "five"})
	if err != nil || !strings.Contains(passed, "Workspace check: Passed.") {
		t.Fatalf("expected passing check, got %q, err %v", passed, err)
	}
	if err := os.WriteFile(filepath.Join(fx.dir, "check-mode.txt"), []byte("old error elsewhere"), 0600); err != nil {
		t.Fatal(err)
	}
	afterPass, err := fx.turn.editFile(ctx, args{"path": "a.txt", "old": "five", "new": "six"})
	if err != nil || !strings.Contains(afterPass, "old error elsewhere") || strings.Contains(afterPass, "with the same output") {
		t.Fatalf("expected full check failure after passing check reset, got %q, err %v", afterPass, err)
	}

	// Stateful session never folds repeated failures.
	fx.turn.statefulSession = true
	statefulAgain, err := fx.turn.editFile(ctx, args{"path": "a.txt", "old": "six", "new": "seven"})
	if err != nil || !strings.Contains(statefulAgain, "old error elsewhere") || strings.Contains(statefulAgain, "with the same output") {
		t.Fatalf("expected full check failure for stateful session, got %q, err %v", statefulAgain, err)
	}
}

func TestTurnCompactionAndCancelForgetReads(t *testing.T) {
	t.Run("compaction clears read dedup while preserving seen", func(t *testing.T) {
		ctx := t.Context()
		fx := newFileStateFixture(t, "", nil)
		if err := os.WriteFile(filepath.Join(fx.dir, "notes.txt"), []byte("hello compaction\n"), 0600); err != nil {
			t.Fatal(err)
		}
		turnID, err := fx.repo.StartTurn(ctx, fx.thread.ID, "stateless")
		if err != nil || turnID == "" {
			t.Fatalf("StartTurn failed: %q, %v", turnID, err)
		}
		conv, err := fx.repo.GetConversation(ctx, fx.thread.ID)
		if err != nil {
			t.Fatal(err)
		}

		registry := models.New()
		info := sdk.ModelProviderInfo{Kind: "stateless", Program: false}
		step, summaries := 0, 0
		if _, err := registry.Register(sdk.ModelProvider{Info: info, Stream: func(_ context.Context, _ map[string]any, request sdk.Request) iter.Seq[sdk.Part] {
			return func(yield func(sdk.Part) bool) {
				if request.Conversation == "" {
					summaries++
					if yield(sdk.Part{Type: "text", Text: "Compacted summary."}) {
						yield(sdk.Part{Type: "finish", Reason: "stop"})
					}
					return
				}
				step++
				switch step {
				case 1:
					if yield(sdk.Part{Type: "tool-call", ID: "read-1", Name: "read_file", Arguments: `{"path":"notes.txt"}`}) {
						yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
					}
				case 2:
					// Second read in the same context should hit dedup; also add a large todo payload + ContextInput over threshold.
					if yield(sdk.Part{Type: "tool-call", ID: "read-2", Name: "read_file", Arguments: `{"path":"notes.txt"}`}) {
						if yield(sdk.Part{Type: "tool-call", ID: "todo-1", Name: "update_todo", Arguments: fmt.Sprintf(`{"items":[{"label":"Pad","detail":%q,"status":"doing"}]}`, strings.Repeat("p", 12000))}) {
							yield(sdk.Part{Type: "usage", Input: new(int64(15000)), Output: new(int64(50)), ContextInput: new(int64(15000))})
							yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
						}
					}
				case 3:
					// After compaction, read_file must return full content again.
					if yield(sdk.Part{Type: "tool-call", ID: "read-3", Name: "read_file", Arguments: `{"path":"notes.txt"}`}) {
						yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
					}
				case 4:
					if yield(sdk.Part{Type: "tool-call", ID: "rep-1", Name: "report", Arguments: `{"kind":"done","headline":"Done","summary":"Compacted and reread."}`}) {
						yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
					}
				default:
					yield(sdk.Part{Type: "finish", Reason: "stop"})
				}
			}
		}}); err != nil {
			t.Fatal(err)
		}

		fx.runner.Models = registry
		active := &turn{Runner: fx.runner, id: turnID, conversation: conv, changes: newTurnChanges(fx.runner, conv)}
		if err := active.run(ctx, &settings.ResolvedModel{Provider: info, Model: "stateless", Label: "stateless", ContextTokens: new(20_000.0)}); err != nil {
			t.Fatalf("turn.run failed: %v", err)
		}
		if summaries != 1 {
			t.Fatalf("expected 1 compaction, got %d", summaries)
		}

		results := map[string]string{}
		entries, err := fx.repo.ListTranscript(ctx, fx.thread.ID, core.TranscriptOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry["kind"] == "tool" {
				results[text(entry, "toolCallId")] = text(entry, "content")
			}
		}
		if !strings.Contains(results["read-1"], "hello compaction") {
			t.Fatalf("expected full text on read-1, got %q", results["read-1"])
		}
		if !strings.Contains(results["read-2"], "unchanged since you read it") {
			t.Fatalf("expected dedup hint on read-2 before compaction, got %q", results["read-2"])
		}
		if !strings.Contains(results["read-3"], "hello compaction") || strings.Contains(results["read-3"], "unchanged") {
			t.Fatalf("expected full text on read-3 after compaction, got %q", results["read-3"])
		}

		// Seen state survives compaction: external edit still blocks writes.
		if err := os.WriteFile(filepath.Join(fx.dir, "notes.txt"), []byte("external after compact\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := active.writeFile(ctx, args{"path": "notes.txt", "content": "overwrite\n"}); err == nil {
			t.Fatal("expected external edit after compaction to still be blocked")
		}
	})

	t.Run("cancelled turn clears read dedup and check failure", func(t *testing.T) {
		fx := newFileStateFixture(t, "", nil)
		if err := os.WriteFile(filepath.Join(fx.dir, "cancel.txt"), []byte("cancel content\n"), 0600); err != nil {
			t.Fatal(err)
		}
		turnID, err := fx.repo.StartTurn(t.Context(), fx.thread.ID, "stateless")
		if err != nil || turnID == "" {
			t.Fatalf("StartTurn failed: %q, %v", turnID, err)
		}
		conv, err := fx.repo.GetConversation(t.Context(), fx.thread.ID)
		if err != nil {
			t.Fatal(err)
		}

		// Pre-populate a check failure too so we verify both reads and failures are cleared.
		fx.runner.files.noteCheckFailure(fx.thread.ID, "prior failure", true)

		runCtx, cancel := context.WithCancel(t.Context())
		defer cancel()

		registry := models.New()
		info := sdk.ModelProviderInfo{Kind: "stateless", Program: false}
		step := 0
		if _, err := registry.Register(sdk.ModelProvider{Info: info, Stream: func(_ context.Context, _ map[string]any, _ sdk.Request) iter.Seq[sdk.Part] {
			return func(yield func(sdk.Part) bool) {
				step++
				if step == 1 {
					if yield(sdk.Part{Type: "tool-call", ID: "read-before-cancel", Name: "read_file", Arguments: `{"path":"cancel.txt"}`}) {
						yield(sdk.Part{Type: "finish", Reason: "tool-calls"})
					}
					return
				}
				// Cancel during step 2 before returning.
				cancel()
			}
		}}); err != nil {
			t.Fatal(err)
		}

		fx.runner.Models = registry
		active := &turn{Runner: fx.runner, id: turnID, conversation: conv, changes: newTurnChanges(fx.runner, conv)}
		err = active.run(runCtx, &settings.ResolvedModel{Provider: info, Model: "stateless", Label: "stateless", ContextTokens: new(20_000.0)})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}

		// After cancellation, read dedup and check failure must be forgotten.
		again, _, err := fx.turn.readFile(t.Context(), args{"path": "cancel.txt"})
		if err != nil || !strings.Contains(again, "cancel content") || strings.Contains(again, "unchanged") {
			t.Fatalf("expected full content after cancelled turn called forgetReads, got %q, %v", again, err)
		}
		if fx.runner.files.noteCheckFailure(fx.thread.ID, "prior failure", true) {
			t.Fatal("expected check failure to be cleared after cancelled turn")
		}
	})
}

func TestFileStateConcurrentAccess(t *testing.T) {
	var state fileState
	const workers = 16
	const iterations = 150

	var wg sync.WaitGroup
	wg.Add(workers)
	for worker := range workers {
		go func(id int) {
			defer wg.Done()
			for i := range iterations {
				convShared := "conv-shared"
				convUnique := fmt.Sprintf("conv-%d-%d", id, i)
				sourceID := fmt.Sprintf("src-%d", i%3)
				filePath := fmt.Sprintf("dir/file-%d.go", i%5)
				payload := []byte(fmt.Sprintf("worker=%d iter=%d", id, i))

				state.noteFile(convShared, sourceID, filePath, payload)
				_ = state.hasSeen(convShared, sourceID, filePath)
				_ = state.changedSince(convShared, sourceID, filePath, payload)
				_ = state.noteRead(convShared, sourceID, filePath, 1, 240, payload)
				_ = state.noteCheckFailure(convShared, fmt.Sprintf("err-%d", i%4), i%3 != 0)
				if i%17 == 0 {
					state.forgetReads(convShared)
				}

				// Drive concurrent LRU evictions across >200 conversations.
				state.noteFile(convUnique, sourceID, filePath, payload)
				_ = state.noteRead(convUnique, sourceID, filePath, 1, 240, payload)
				_ = state.noteCheckFailure(convUnique, "err", true)
			}
		}(worker)
	}
	wg.Wait()
}

type stubPatchlessWorkspace struct {
	sdk.Workspace
	readBytes  func(ctx context.Context, path string, max int64) ([]byte, error)
	fileExists func(ctx context.Context, path string) (bool, error)
}

func (w stubPatchlessWorkspace) ReadBytes(ctx context.Context, path string, max int64) ([]byte, error) {
	return w.readBytes(ctx, path, max)
}
func (w stubPatchlessWorkspace) FileExists(ctx context.Context, path string) (bool, error) {
	return w.fileExists(ctx, path)
}
func (w stubPatchlessWorkspace) RemoveFile(context.Context, string) error {
	return errors.New("the workspace source does not support file deletion")
}

func TestReadExistingBytesWithoutWorkspaceFilesSupport(t *testing.T) {
	ctx := t.Context()
	unsupported := errors.New("the workspace source does not support file patches")

	// Existing file succeeds directly via ReadBytes even when FileExists is unsupported.
	fileExistsCalled := false
	ws := stubPatchlessWorkspace{
		readBytes: func(context.Context, string, int64) ([]byte, error) {
			return []byte("current"), nil
		},
		fileExists: func(context.Context, string) (bool, error) {
			fileExistsCalled = true
			return false, unsupported
		},
	}
	data, exists, err := readExistingBytes(ctx, ws, "notes.txt")
	if err != nil || !exists || string(data) != "current" || fileExistsCalled {
		t.Fatalf("expected direct ReadBytes hit without FileExists call, got data=%q exists=%v err=%v fileExistsCalled=%v", data, exists, err, fileExistsCalled)
	}

	// Missing file returning os.ErrNotExist succeeds without calling FileExists.
	ws.readBytes = func(context.Context, string, int64) ([]byte, error) {
		return nil, fmt.Errorf("open notes.txt: %w", os.ErrNotExist)
	}
	data, exists, err = readExistingBytes(ctx, ws, "notes.txt")
	if err != nil || exists || data != nil || fileExistsCalled {
		t.Fatalf("expected missing file on os.ErrNotExist without FileExists call, got data=%q exists=%v err=%v fileExistsCalled=%v", data, exists, err, fileExistsCalled)
	}

	// FileTooLargeError is preserved directly without calling FileExists.
	tooLargeErr := &sdk.FileTooLargeError{Path: "notes.txt", Size: maxEditBytes + 1, Max: maxEditBytes}
	ws.readBytes = func(context.Context, string, int64) ([]byte, error) {
		return nil, tooLargeErr
	}
	_, exists, err = readExistingBytes(ctx, ws, "notes.txt")
	var gotTooLarge *sdk.FileTooLargeError
	if !errors.As(err, &gotTooLarge) || exists || fileExistsCalled {
		t.Fatalf("expected FileTooLargeError without FileExists call, got exists=%v err=%v fileExistsCalled=%v", exists, err, fileExistsCalled)
	}

	// Non-ErrNotExist read error falls back to FileExists and preserves the original ReadBytes error when FileExists fails.
	readFailure := errors.New("permission denied")
	ws.readBytes = func(context.Context, string, int64) ([]byte, error) {
		return nil, readFailure
	}
	_, exists, err = readExistingBytes(ctx, ws, "notes.txt")
	if !errors.Is(err, readFailure) || exists || !fileExistsCalled {
		t.Fatalf("expected original ReadBytes error to be preserved, got exists=%v err=%v fileExistsCalled=%v", exists, err, fileExistsCalled)
	}

	// Non-ErrNotExist read error treats file as missing when FileExists reports (false, nil).
	ws.fileExists = func(context.Context, string) (bool, error) {
		return false, nil
	}
	data, exists, err = readExistingBytes(ctx, ws, "notes.txt")
	if err != nil || exists || data != nil {
		t.Fatalf("expected fallback FileExists=(false,nil) to report missing file, got data=%q exists=%v err=%v", data, exists, err)
	}
}
