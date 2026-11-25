package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/local"
	"github.com/daidr/Ariadne/internal/sources"
	"github.com/daidr/Ariadne/internal/workspaces"
	"github.com/daidr/Ariadne/sdk"
)

var (
	runnerBaseRepoOnce sync.Once
	runnerBaseRepoDir  string
	runnerBaseRepoErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if runnerBaseRepoDir != "" {
		_ = os.RemoveAll(runnerBaseRepoDir)
	}
	os.Exit(code)
}

func copyRunnerTree(t *testing.T, src, dst string) {
	t.Helper()
	if err := copyRunnerTreeErr(src, dst); err != nil {
		t.Fatalf("copy %s -> %s: %v", src, dst, err)
	}
}

func copyRunnerTreeErr(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := copyRunnerTreeErr(srcPath, dstPath); err != nil {
				return err
			}
			continue
		}
		data, err := os.ReadFile(srcPath)
		if err != nil {
			return err
		}
		if err := os.WriteFile(dstPath, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func writeRunnerGitRef(t *testing.T, gitDir, relRef, content string) {
	t.Helper()
	full := filepath.Join(gitDir, filepath.FromSlash(relRef))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readRunnerHeadSHA(t *testing.T, repoDir string) string {
	t.Helper()
	dotGit := filepath.Join(repoDir, ".git")
	gitDir := dotGit
	if info, err := os.Stat(dotGit); err == nil && !info.IsDir() {
		raw, err := os.ReadFile(dotGit)
		if err != nil {
			t.Fatal(err)
		}
		line := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(raw)), "gitdir:"))
		if !filepath.IsAbs(line) {
			line = filepath.Join(repoDir, filepath.FromSlash(line))
		}
		gitDir = filepath.Clean(line)
	}
	rawHead, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		t.Fatal(err)
	}
	head := strings.TrimSpace(string(rawHead))
	if ref, ok := strings.CutPrefix(head, "ref:"); ok {
		refName := strings.TrimSpace(ref)
		commonDir := gitDir
		if rawCommon, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
			rel := strings.TrimSpace(string(rawCommon))
			if rel != "" {
				if !filepath.IsAbs(rel) {
					commonDir = filepath.Clean(filepath.Join(gitDir, filepath.FromSlash(rel)))
				} else {
					commonDir = filepath.Clean(filepath.FromSlash(rel))
				}
			}
		}
		rawRef, err := os.ReadFile(filepath.Join(commonDir, filepath.FromSlash(refName)))
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(rawRef))
	}
	return head
}

func writeRunnerGitExclude(t *testing.T, repoDir, patterns string) {
	t.Helper()
	path := filepath.Join(repoDir, ".git", "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	existing, _ := os.ReadFile(path)
	if len(existing) > 0 && existing[len(existing)-1] != '\n' {
		existing = append(existing, '\n')
	}
	if err := os.WriteFile(path, append(existing, []byte(patterns)...), 0o644); err != nil {
		t.Fatal(err)
	}
}

func ensureRunnerBaseRepo(t *testing.T) string {
	t.Helper()
	runnerBaseRepoOnce.Do(func() {
		dir, err := os.MkdirTemp("", "ad-runner-git-*")
		if err != nil {
			runnerBaseRepoErr = err
			return
		}
		git := local.NewGit(dir, "file")
		run := func(args ...string) error {
			res := git(context.Background(), args, local.GitOptions{})
			if res.ExitCode != 0 {
				return fmt.Errorf("git %v failed: %s", args, res.Stderr)
			}
			return nil
		}
		if err := run("init", "-q", "-b", "main"); err != nil {
			runnerBaseRepoErr = err
			return
		}
		if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644); err != nil {
			runnerBaseRepoErr = err
			return
		}
		if err := run("add", "README.md"); err != nil {
			runnerBaseRepoErr = err
			return
		}
		if err := run("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-q", "-m", "initial"); err != nil {
			runnerBaseRepoErr = err
			return
		}
		_ = os.RemoveAll(filepath.Join(dir, ".git", "hooks"))
		runnerBaseRepoDir = dir
	})
	if runnerBaseRepoErr != nil {
		t.Fatalf("build base git template: %v", runnerBaseRepoErr)
	}
	return runnerBaseRepoDir
}

func initTestRemoteRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	copyRunnerTree(t, ensureRunnerBaseRepo(t), dir)
	return dir
}

func TestTurnChangesRecordedForHostedRepository(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	remoteDir := initTestRemoteRepo(t)
	dataDir := t.TempDir()

	repo, err := core.Open(ctx, core.Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	hosts := sources.New()
	if _, err := hosts.Register(sdk.ContextProvider{
		Info: sdk.ContextProviderInfo{Kind: "github", Provides: []string{"workspace", "repo"}},
		Connect: func(context.Context, map[string]any) (sdk.Capabilities, error) {
			hosted, err := local.NewHosted(local.HostedOptions{
				DataDir:      dataDir,
				Path:         "owner/repo",
				Namespace:    []string{"github"},
				RemoteURL:    "file://" + filepath.ToSlash(remoteDir),
				DisableClone: true,
				DefaultBranch: func(context.Context) (string, error) {
					return "main", nil
				},
			})
			if err != nil {
				return sdk.Capabilities{}, err
			}
			return sdk.Capabilities{
				Workspace: hosted.Capability(),
				Close:     hosted.Close,
			}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	manager, err := workspaces.New(workspaces.Options{Repository: repo, Sources: hosts})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())

	project, err := repo.CreateProject(ctx, new("Hosted"), core.ProjectStart{
		Source: core.Source{Kind: "github", Settings: map[string]any{"repository": "owner/repo"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "Edit hosted file"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PostMessage(ctx, project.ID, core.MessageInput{
		ConversationID: thread.ID, Author: "user", Content: "Update README",
	}); err != nil {
		t.Fatal(err)
	}
	turnID, err := repo.StartScheduledTurn(ctx, thread.ID, "mock")
	if err != nil || turnID == "" {
		t.Fatalf("StartScheduledTurn: id=%q err=%v", turnID, err)
	}

	runnerInstance := &Runner{Repository: repo, Workspaces: manager}
	conv := core.ConversationView{ID: thread.ID, ProjectID: project.ID, Role: "thread"}
	discussion := &turn{
		Runner:       runnerInstance,
		conversation: conv,
		id:           turnID,
		changes:      newTurnChanges(runnerInstance, conv),
	}

	if _, err := discussion.writeFile(ctx, args{
		"path":    "README.md",
		"content": "hello\nworld\n",
	}); err != nil {
		t.Fatal(err)
	}
	discussion.changes.record(ctx, turnID)
	if err := repo.FinishTurn(ctx, thread.ID, turnID, "completed", nil); err != nil {
		t.Fatal(err)
	}

	updated, err := repo.GetThread(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	totals, ok := updated.Changes.(map[string]any)
	if !ok {
		t.Fatalf("expected thread.Changes to be recorded, got %#v", updated.Changes)
	}
	if totals["files"] != float64(1) || totals["added"] != float64(1) || totals["removed"] != float64(0) {
		t.Fatalf("unexpected thread.Changes totals: %#v", totals)
	}

	// Second turn that makes no edits should not overwrite totals or emit another notice.
	if _, err := repo.PostMessage(ctx, project.ID, core.MessageInput{
		ConversationID: thread.ID, Author: "user", Content: "Read only check",
	}); err != nil {
		t.Fatal(err)
	}
	turnID2, err := repo.StartScheduledTurn(ctx, thread.ID, "mock")
	if err != nil || turnID2 == "" {
		t.Fatalf("StartScheduledTurn 2: id=%q err=%v", turnID2, err)
	}
	discussion2 := &turn{
		Runner:       runnerInstance,
		conversation: conv,
		id:           turnID2,
		changes:      newTurnChanges(runnerInstance, conv),
	}
	discussion2.changes.record(ctx, turnID2)
	if err := repo.FinishTurn(ctx, thread.ID, turnID2, "completed", nil); err != nil {
		t.Fatal(err)
	}

	updated2, err := repo.GetThread(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	totals2, ok := updated2.Changes.(map[string]any)
	if !ok || totals2["files"] != float64(1) {
		t.Fatalf("expected thread.Changes to stay intact, got %#v", updated2.Changes)
	}

	// Simulate an existing thread whose branch already has committed changes
	// while threads.changes is still NULL (legacy backfill on editless turn).
	ws, err := manager.ForThread(ctx, project.ID, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.WriteFile(ctx, "README.md", "hello\nline2\nline3\n"); err != nil {
		t.Fatal(err)
	}
	wsGit := local.NewGit(ws.Info().Label, "file")
	if res := wsGit(ctx, []string{"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qam", "legacy commit"}, local.GitOptions{}); res.ExitCode != 0 {
		t.Fatalf("legacy commit: exit=%d stderr=%s", res.ExitCode, res.Stderr)
	}
	if _, err := repo.Store.DB().ExecContext(ctx, "UPDATE threads SET changes = NULL WHERE id = ?", thread.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PostMessage(ctx, project.ID, core.MessageInput{
		ConversationID: thread.ID, Author: "user", Content: "Push branch",
	}); err != nil {
		t.Fatal(err)
	}
	turnIDLegacy, err := repo.StartScheduledTurn(ctx, thread.ID, "mock")
	if err != nil || turnIDLegacy == "" {
		t.Fatalf("StartScheduledTurn legacy: id=%q err=%v", turnIDLegacy, err)
	}
	legacyDiscussion := &turn{
		Runner:       runnerInstance,
		conversation: conv,
		id:           turnIDLegacy,
		changes:      newTurnChanges(runnerInstance, conv),
	}
	if _, _, _, err := legacyDiscussion.repoWorkspace(ctx); err != nil {
		t.Fatal(err)
	}
	var beforeLegacyTurnChanged int
	if err := repo.Store.DB().QueryRowContext(ctx, "SELECT count(*) FROM events WHERE type = 'turn.changed'").Scan(&beforeLegacyTurnChanged); err != nil {
		t.Fatal(err)
	}
	legacyDiscussion.changes.record(ctx, turnIDLegacy)
	if err := repo.FinishTurn(ctx, thread.ID, turnIDLegacy, "completed", nil); err != nil {
		t.Fatal(err)
	}
	afterLegacy, err := repo.GetThread(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	legacyTotals, ok := afterLegacy.Changes.(map[string]any)
	if !ok {
		t.Fatalf("expected thread.Changes to be backfilled, got %#v", afterLegacy.Changes)
	}
	if legacyTotals["files"] != float64(1) || legacyTotals["added"] != float64(2) || legacyTotals["removed"] != float64(0) {
		t.Fatalf("unexpected backfilled totals: %#v", legacyTotals)
	}
	var afterLegacyTurnChanged int
	if err := repo.Store.DB().QueryRowContext(ctx, "SELECT count(*) FROM events WHERE type = 'turn.changed'").Scan(&afterLegacyTurnChanged); err != nil {
		t.Fatal(err)
	}
	if afterLegacyTurnChanged != beforeLegacyTurnChanged {
		t.Fatalf("expected no turn.changed event for editless turn, got %d -> %d", beforeLegacyTurnChanged, afterLegacyTurnChanged)
	}
}

func TestTurnChangesAfterMergingOriginMainMatchesBranchChangesAndReconcilesStaleTotals(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	repoDir := initTestRemoteRepo(t)
	dataDir := t.TempDir()
	baseSHA := readRunnerHeadSHA(t, repoDir)
	runWorkGit := func(dir string, gitArgs ...string) string {
		t.Helper()
		res := local.NewGit(dir, "file")(ctx, gitArgs, local.GitOptions{})
		if res.ExitCode != 0 {
			t.Fatalf("git %v in %s failed: %s", gitArgs, dir, res.Stderr)
		}
		return strings.TrimSpace(res.Stdout)
	}

	repo, err := core.Open(ctx, core.Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	hosts := sources.New()
	if _, err := hosts.Register(local.DirectoryProvider(dataDir, nil, true)); err != nil {
		t.Fatal(err)
	}
	manager, err := workspaces.New(workspaces.Options{Repository: repo, Sources: hosts})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())

	project, err := repo.CreateProject(ctx, new("MergeOrigin"), core.ProjectStart{
		Source: core.Source{Kind: "local-directory", Settings: map[string]any{"path": repoDir}},
	})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "Merge origin/main"})
	if err != nil {
		t.Fatal(err)
	}
	ws, err := manager.ForThread(ctx, project.ID, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	workDir := ws.Info().Label
	if err := ws.WriteFile(ctx, "README.md", "hello\nbranch line\n"); err != nil {
		t.Fatal(err)
	}
	runWorkGit(workDir, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qam", "branch change")

	// Advance origin/main with a large upstream file while local main lags at baseSHA.
	if err := os.WriteFile(filepath.Join(repoDir, "upstream.txt"), []byte(strings.Repeat("upstream\n", 80)), 0644); err != nil {
		t.Fatal(err)
	}
	runWorkGit(repoDir, "add", "upstream.txt")
	runWorkGit(repoDir, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "large upstream commit")
	upstreamSHA := readRunnerHeadSHA(t, repoDir)
	writeRunnerGitRef(t, filepath.Join(repoDir, ".git"), "refs/remotes/origin/main", upstreamSHA)
	writeRunnerGitRef(t, filepath.Join(repoDir, ".git"), "refs/heads/main", baseSHA)

	runnerInstance := &Runner{Repository: repo, Workspaces: manager}
	conv := core.ConversationView{ID: thread.ID, ProjectID: project.ID, Role: "thread"}
	if _, err := repo.PostMessage(ctx, project.ID, core.MessageInput{
		ConversationID: thread.ID, Author: "user", Content: "Merge origin/main",
	}); err != nil {
		t.Fatal(err)
	}
	turnID2, err := repo.StartScheduledTurn(ctx, thread.ID, "mock")
	if err != nil || turnID2 == "" {
		t.Fatalf("StartScheduledTurn 2: id=%q err=%v", turnID2, err)
	}
	turn2 := &turn{Runner: runnerInstance, conversation: conv, id: turnID2, changes: newTurnChanges(runnerInstance, conv)}
	currentThread, err := repo.GetThread(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	turn2.changes.begin(ctx, currentThread)
	runWorkGit(workDir, "-c", "core.autocrlf=true", "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "merge", "--no-ff", "-m", "Merge origin/main", "refs/remotes/origin/main")
	turn2.changes.record(ctx, turnID2)
	if err := repo.FinishTurn(ctx, thread.ID, turnID2, "completed", nil); err != nil {
		t.Fatal(err)
	}

	afterMerge, err := repo.GetThread(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	totals, ok := afterMerge.Changes.(map[string]any)
	if !ok {
		t.Fatalf("expected thread.Changes after merge, got %#v", afterMerge.Changes)
	}
	if totals["files"] != float64(1) || totals["added"] != float64(1) || totals["removed"] != float64(0) {
		t.Fatalf("expected branch totals {files:1 added:1 removed:0} after merging origin/main, got %#v", totals)
	}

	// Simulate an already-stale cached thread.Changes (as from before this fix)
	// and verify that ThreadChanges reconciles it back to the true branch diff.
	if err := repo.SyncBranchChanges(ctx, thread.ID, core.ChangeTotals{Files: 311, Added: 115384, Removed: 114771}); err != nil {
		t.Fatal(err)
	}
	stale, err := repo.GetThread(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !core.SameChangeTotals(stale.Changes, core.ChangeTotals{Files: 311, Added: 115384, Removed: 114771}) {
		t.Fatalf("expected stale totals to be seeded, got %#v", stale.Changes)
	}
	if _, err := manager.ThreadChanges(ctx, thread.ID); err != nil {
		t.Fatal(err)
	}
	reconciled, err := repo.GetThread(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !core.SameChangeTotals(reconciled.Changes, core.ChangeTotals{Files: 1, Added: 1, Removed: 0}) {
		t.Fatalf("expected ThreadChanges to reconcile stale thread.Changes to {1, 1, 0}, got %#v", reconciled.Changes)
	}
}

func TestTurnChangesMultiRepoOuterAndEmbedded(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	runGit := func(dir string, args ...string) string {
		t.Helper()
		res := local.NewGit(dir, "file")(ctx, args, local.GitOptions{})
		if res.ExitCode != 0 {
			t.Fatalf("git %v in %s failed: %s", args, dir, res.Stderr)
		}
		return strings.TrimSpace(res.Stdout)
	}
	outerDir := initTestRemoteRepo(t)
	writeRunnerGitExclude(t, outerDir, "packages/\n")
	dataDir := t.TempDir()

	repo, err := core.Open(ctx, core.Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	hosts := sources.New()
	if _, err := hosts.Register(local.DirectoryProvider(dataDir, nil, true)); err != nil {
		t.Fatal(err)
	}
	manager, err := workspaces.New(workspaces.Options{Repository: repo, Sources: hosts})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())

	project, err := repo.CreateProject(ctx, new("MultiRepo"), core.ProjectStart{
		Source: core.Source{Kind: "local-directory", Settings: map[string]any{"path": outerDir}},
	})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "Outer and embedded edits"})
	if err != nil {
		t.Fatal(err)
	}
	ws, err := manager.ForThread(ctx, project.ID, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	workDir := ws.Info().Label

	// Seed an embedded git repo inside an ignored subdirectory of the thread worktree before Turn 1 starts.
	subDir := filepath.Join(workDir, "packages", "sub")
	copyRunnerTree(t, ensureRunnerBaseRepo(t), subDir)
	subBaseHead := readRunnerHeadSHA(t, subDir)

	if _, err := repo.PostMessage(ctx, project.ID, core.MessageInput{
		ConversationID: thread.ID, Author: "user", Content: "Edit outer and sub",
	}); err != nil {
		t.Fatal(err)
	}
	turnID, err := repo.StartScheduledTurn(ctx, thread.ID, "mock")
	if err != nil {
		t.Fatal(err)
	}

	runnerInstance := &Runner{Repository: repo, Workspaces: manager}
	conv := core.ConversationView{ID: thread.ID, ProjectID: project.ID, Role: "thread"}
	discussion := &turn{
		Runner:       runnerInstance,
		conversation: conv,
		id:           turnID,
		changes:      newTurnChanges(runnerInstance, conv),
	}

	// Edit outer repo file via writeFile (triggers begin across outer + embedded repos).
	if _, err := discussion.writeFile(ctx, args{
		"path":    "README.md",
		"content": "hello\nouter edit\n",
	}); err != nil {
		t.Fatal(err)
	}
	// Commit in outer repo and edit + commit in embedded repo.
	runGit(workDir, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qam", "outer change")
	if err := os.WriteFile(filepath.Join(subDir, "lib.go"), []byte("package sub\nfunc Hello() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(subDir, "add", "lib.go")
	runGit(subDir, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-q", "-m", "sub change")

	discussion.changes.record(ctx, turnID)
	if err := repo.FinishTurn(ctx, thread.ID, turnID, "completed", nil); err != nil {
		t.Fatal(err)
	}

	// 1. Verify first-touch baseline recorded for packages/sub.
	baselines, err := repo.RepoBaselines(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(baselines) != 1 || baselines[0].RepoPath != "packages/sub" || baselines[0].BaseHead != subBaseHead {
		t.Fatalf("expected baseline for packages/sub at %s, got %+v", subBaseHead, baselines)
	}

	// 2. Verify turn-changes notice contains both repositories.
	notices, err := repo.ListNotices(ctx, thread.ID, core.NoticeRange{})
	if err != nil {
		t.Fatal(err)
	}
	var turnNotice map[string]any
	for _, n := range notices {
		if n["kind"] == "turn-changes" {
			turnNotice = n
		}
	}
	if turnNotice == nil {
		t.Fatalf("expected turn-changes notice, got %+v", notices)
	}
	reposRaw, ok := turnNotice["repos"].([]any)
	if !ok || len(reposRaw) != 2 {
		t.Fatalf("expected 2 repos in turn-changes notice, got %#v", turnNotice["repos"])
	}
	outerTurnRepo, _ := reposRaw[0].(map[string]any)
	if branch, _ := outerTurnRepo["branch"].(string); branch == "" {
		t.Fatalf("expected outer root repo branch to be backfilled in turn-changes notice, got %#v", outerTurnRepo)
	}

	// 3. Verify ThreadChanges returns both repos with embedded repo marked non-mergeable.
	tc, err := manager.ThreadChanges(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tc.Repos) != 2 {
		t.Fatalf("expected 2 repos in ThreadChanges, got %+v", tc.Repos)
	}
	if tc.Repos[0].Embedded || !tc.Repos[0].Mergeable {
		t.Fatalf("expected primary outer repo mergeable and non-embedded, got %+v", tc.Repos[0])
	}
	if !tc.Repos[1].Embedded || tc.Repos[1].Mergeable || tc.Repos[1].RepoPath != "packages/sub" {
		t.Fatalf("expected embedded repo packages/sub non-mergeable, got %+v", tc.Repos[1])
	}
	if len(tc.Repos[1].Changes.Files) != 1 || tc.Repos[1].Changes.Files[0].Path != "lib.go" {
		t.Fatalf("expected lib.go in embedded repo changes, got %+v", tc.Repos[1].Changes)
	}

	// 4. Verify thread.Changes totals aggregate outer (1 file, +1) + embedded (1 file, +2) = 2 files, +3.
	updated, err := repo.GetThread(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !core.SameChangeTotals(updated.Changes, core.ChangeTotals{Files: 2, Added: 3, Removed: 0}) {
		t.Fatalf("expected aggregated totals {2, 3, 0}, got %#v", updated.Changes)
	}
}

func TestTurnChangesMidTurnCreatedWorktreeRecordsForkBaselineAndCommits(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	runGit := func(dir string, args ...string) string {
		t.Helper()
		res := local.NewGit(dir, "file")(ctx, args, local.GitOptions{})
		if res.ExitCode != 0 {
			t.Fatalf("git %v in %s failed: %s", args, dir, res.Stderr)
		}
		return strings.TrimSpace(res.Stdout)
	}

	// Build outer umbrella repo and external host repo from cached template.
	outerDir := initTestRemoteRepo(t)
	writeRunnerGitExclude(t, outerDir, "nested-worktrees/\nsubrepos/\n")

	hostRepo := initTestRemoteRepo(t)
	forkSHA := readRunnerHeadSHA(t, hostRepo)
	writeRunnerGitRef(t, filepath.Join(hostRepo, ".git"), "refs/remotes/origin/main", forkSHA)
	writeRunnerGitRef(t, filepath.Join(hostRepo, ".git"), "refs/remotes/origin/HEAD", "ref: refs/remotes/origin/main")

	// Set up Ariadne core + workspaces + runner.
	dataDir, err := os.MkdirTemp("", "ad-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dataDir) })
	repo, err := core.Open(ctx, core.Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	hosts := sources.New()
	if _, err := hosts.Register(local.DirectoryProvider(dataDir, nil, true)); err != nil {
		t.Fatal(err)
	}
	manager, err := workspaces.New(workspaces.Options{Repository: repo, Sources: hosts})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())

	project, err := repo.CreateProject(ctx, new("MidTurnEmbedded"), core.ProjectStart{
		Source: core.Source{Kind: "local-directory", Settings: map[string]any{"path": outerDir}},
	})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "Mid-turn worktrees and clone"})
	if err != nil {
		t.Fatal(err)
	}
	ws, err := manager.ForThread(ctx, project.ID, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	workDir := ws.Info().Label

	if _, err := repo.PostMessage(ctx, project.ID, core.MessageInput{
		ConversationID: thread.ID, Author: "user", Content: "Create nested worktrees mid-turn",
	}); err != nil {
		t.Fatal(err)
	}
	turnID, err := repo.StartScheduledTurn(ctx, thread.ID, "mock")
	if err != nil {
		t.Fatal(err)
	}

	runnerInstance := &Runner{Repository: repo, Workspaces: manager}
	conv := core.ConversationView{ID: thread.ID, ProjectID: project.ID, Role: "thread"}
	tc := newTurnChanges(runnerInstance, conv)
	tc.begin(ctx, thread)

	// 1. Mid-turn created worktree + commit C1 (clean working tree).
	mid1Dir := filepath.Join(workDir, "nested-worktrees", "feat-mid1")
	if err := os.MkdirAll(filepath.Dir(mid1Dir), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(hostRepo, "worktree", "add", "-qb", "feat/mid1", mid1Dir, forkSHA)
	if err := os.WriteFile(filepath.Join(mid1Dir, "README.md"), []byte("hello\nmid1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(mid1Dir, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qam", "mid1 commit")
	mid1Head := readRunnerHeadSHA(t, mid1Dir)

	// 2. Mid-turn created worktree + commit C1 + uncommitted edit.
	mid2Dir := filepath.Join(workDir, "nested-worktrees", "feat-mid2")
	runGit(hostRepo, "worktree", "add", "-qb", "feat/mid2", mid2Dir, forkSHA)
	if err := os.WriteFile(filepath.Join(mid2Dir, "README.md"), []byte("hello\nmid2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(mid2Dir, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qam", "mid2 commit")
	if err := os.WriteFile(filepath.Join(mid2Dir, "wip.go"), []byte("package pkg\n// wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 3. Mid-turn clean git clone -> must produce 0 turn-changes and 0 baselines.
	cleanCloneDir := filepath.Join(workDir, "subrepos", "clean-clone")
	copyRunnerTree(t, hostRepo, cleanCloneDir)
	_ = os.RemoveAll(filepath.Join(cleanCloneDir, ".git", "worktrees"))

	tc.record(ctx, turnID)
	if err := repo.FinishTurn(ctx, thread.ID, turnID, "completed", nil); err != nil {
		t.Fatal(err)
	}

	// Verify baselines: recorded for feat-mid1 and feat-mid2 at forkSHA (NOT mid1Head/mid2Head),
	// and NOT recorded for clean-clone or its clean orphan submodule.
	baselines, err := repo.RepoBaselines(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	baselineByPath := map[string]core.RepoBaseline{}
	for _, b := range baselines {
		baselineByPath[b.RepoPath] = b
	}
	if len(baselines) != 2 {
		t.Fatalf("expected exactly 2 baselines (feat-mid1 and feat-mid2), got %+v", baselines)
	}
	if b := baselineByPath["nested-worktrees/feat-mid1"]; b.BaseHead != forkSHA {
		t.Fatalf("expected feat-mid1 baseline at forkSHA %s (not %s), got %+v", forkSHA, mid1Head, b)
	}
	if b := baselineByPath["nested-worktrees/feat-mid2"]; b.BaseHead != forkSHA {
		t.Fatalf("expected feat-mid2 baseline at forkSHA %s, got %+v", forkSHA, b)
	}

	// Verify turn-changes notice contains exactly feat-mid1 (pkg.go) and feat-mid2 (pkg.go + wip.go).
	notices, err := repo.ListNotices(ctx, thread.ID, core.NoticeRange{})
	if err != nil {
		t.Fatal(err)
	}
	var turnNotice map[string]any
	for _, n := range notices {
		if n["kind"] == "turn-changes" {
			turnNotice = n
		}
	}
	if turnNotice == nil {
		t.Fatalf("expected turn-changes notice, got %+v", notices)
	}
	reposRaw, ok := turnNotice["repos"].([]any)
	if !ok || len(reposRaw) != 2 {
		t.Fatalf("expected 2 repos in turn-changes notice (no clean-clone or orphan submodule), got %#v", turnNotice["repos"])
	}
	turnRepoByPath := map[string]map[string]any{}
	for _, item := range reposRaw {
		m, _ := item.(map[string]any)
		p, _ := m["repoPath"].(string)
		turnRepoByPath[p] = m
	}
	mid1Turn := turnRepoByPath["nested-worktrees/feat-mid1"]
	if mid1Turn == nil || mid1Turn["from"] != forkSHA || mid1Turn["to"] != mid1Head {
		t.Fatalf("unexpected feat-mid1 turn-changes entry: %#v", mid1Turn)
	}
	if files, _ := mid1Turn["files"].([]any); len(files) != 1 {
		t.Fatalf("expected 1 changed file for feat-mid1, got %#v", mid1Turn["files"])
	}
	mid2Turn := turnRepoByPath["nested-worktrees/feat-mid2"]
	if mid2Turn == nil || mid2Turn["from"] != forkSHA {
		t.Fatalf("unexpected feat-mid2 turn-changes entry: %#v", mid2Turn)
	}
	if files, _ := mid2Turn["files"].([]any); len(files) != 2 {
		t.Fatalf("expected 2 changed files (committed pkg.go + uncommitted wip.go) for feat-mid2, got %#v", mid2Turn["files"])
	}

	// Verify subsequent ThreadChanges preserves committed C1 for both feat-mid1 and feat-mid2
	// and reports 0 changes for clean-clone and its orphan submodule.
	changes, err := manager.ThreadChanges(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes.Repos) != 2 {
		t.Fatalf("expected 2 active repos in ThreadChanges, got %+v", changes.Repos)
	}
	for _, r := range changes.Repos {
		if r.RepoPath != "nested-worktrees/feat-mid1" && r.RepoPath != "nested-worktrees/feat-mid2" {
			t.Fatalf("unexpected active repo %s in ThreadChanges", r.RepoPath)
		}
		if r.Changes == nil || r.Changes.From != forkSHA || len(r.Changes.Files) != 1 || r.Changes.Files[0].Path != "README.md" {
			t.Fatalf("expected committed README.md change relative to forkSHA %s for %s, got %+v", forkSHA, r.RepoPath, r.Changes)
		}
		if r.RepoPath == "nested-worktrees/feat-mid2" && (len(r.Changes.Uncommitted) != 1 || r.Changes.Uncommitted[0] != "wip.go") {
			t.Fatalf("expected uncommitted wip.go for feat-mid2, got %+v", r.Changes)
		}
	}
}
