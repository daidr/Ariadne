package runner

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/local"
	"github.com/daidr/Ariadne/internal/sources"
	"github.com/daidr/Ariadne/internal/workspaces"
	"github.com/daidr/Ariadne/sdk"
)

func initTestRemoteRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git := local.NewGit(dir, "file")
	run := func(args ...string) {
		t.Helper()
		res := git(t.Context(), args, local.GitOptions{})
		if res.ExitCode != 0 {
			t.Fatalf("git %v failed: %s", args, res.Stderr)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.name", "Test")
	run("config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-q", "-m", "initial")
	return dir
}

func TestTurnChangesRecordedForHostedRepository(t *testing.T) {
	ctx := t.Context()
	remoteDir := initTestRemoteRepo(t)
	dataDir := t.TempDir()

	repo, err := core.Open(ctx, core.Options{Path: filepath.Join(t.TempDir(), "repo.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	hosts := sources.New()
	if _, err := hosts.Register(sdk.ContextProvider{
		Info: sdk.ContextProviderInfo{Kind: "github", Provides: []string{"workspace", "repo"}},
		Connect: func(context.Context, map[string]any) (sdk.Capabilities, error) {
			hosted, err := local.NewHosted(local.HostedOptions{
				DataDir:   dataDir,
				Path:      "owner/repo",
				Namespace: []string{"github"},
				RemoteURL: "file://" + filepath.ToSlash(remoteDir),
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
}

func TestTurnChangesBackfillsTotalsWithoutFileEdits(t *testing.T) {
	ctx := t.Context()
	remoteDir := initTestRemoteRepo(t)
	dataDir := t.TempDir()

	repo, err := core.Open(ctx, core.Options{Path: filepath.Join(t.TempDir(), "repo.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	hosts := sources.New()
	if _, err := hosts.Register(sdk.ContextProvider{
		Info: sdk.ContextProviderInfo{Kind: "github", Provides: []string{"workspace", "repo"}},
		Connect: func(context.Context, map[string]any) (sdk.Capabilities, error) {
			hosted, err := local.NewHosted(local.HostedOptions{
				DataDir:   dataDir,
				Path:      "owner/repo",
				Namespace: []string{"github"},
				RemoteURL: "file://" + filepath.ToSlash(remoteDir),
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
	thread, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "Legacy thread with commits"})
	if err != nil {
		t.Fatal(err)
	}

	// Simulate an existing thread whose branch already has committed changes
	// while threads.changes is still NULL (e.g., created before PR #179).
	ws, err := manager.ForThread(ctx, project.ID, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.WriteFile(ctx, "README.md", "hello\nline2\nline3\n"); err != nil {
		t.Fatal(err)
	}
	for _, argv := range [][]string{
		{"git", "config", "user.name", "Test"},
		{"git", "config", "user.email", "test@example.invalid"},
		{"git", "add", "README.md"},
		{"git", "commit", "-q", "-m", "legacy commit"},
	} {
		res, err := ws.ExecFile(ctx, argv, sdk.ExecOptions{})
		if err != nil || res.ExitCode != 0 {
			t.Fatalf("exec %v: err=%v exit=%d stderr=%s", argv, err, res.ExitCode, res.Stderr)
		}
	}
	before, err := repo.GetThread(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.Changes != nil {
		t.Fatalf("expected nil changes before backfill, got %#v", before.Changes)
	}

	if _, err := repo.PostMessage(ctx, project.ID, core.MessageInput{
		ConversationID: thread.ID, Author: "user", Content: "Push branch",
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
	if _, _, _, err := discussion.repoWorkspace(ctx); err != nil {
		t.Fatal(err)
	}
	discussion.changes.record(ctx, turnID)
	if err := repo.FinishTurn(ctx, thread.ID, turnID, "completed", nil); err != nil {
		t.Fatal(err)
	}

	after, err := repo.GetThread(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	totals, ok := after.Changes.(map[string]any)
	if !ok {
		t.Fatalf("expected thread.Changes to be backfilled, got %#v", after.Changes)
	}
	if totals["files"] != float64(1) || totals["added"] != float64(2) || totals["removed"] != float64(0) {
		t.Fatalf("unexpected backfilled totals: %#v", totals)
	}
	var turnChangedEvents int
	if err := repo.Store.DB().QueryRowContext(ctx, "SELECT count(*) FROM events WHERE type = 'turn.changed'").Scan(&turnChangedEvents); err != nil {
		t.Fatal(err)
	}
	if turnChangedEvents != 0 {
		t.Fatalf("expected no turn.changed event for editless turn, got %d", turnChangedEvents)
	}
}

func TestTurnChangesAfterMergingOriginMainMatchesBranchChangesAndReconcilesStaleTotals(t *testing.T) {
	ctx := t.Context()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[core]\n\tautocrlf = true\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	remoteDir := initTestRemoteRepo(t)
	dataDir := t.TempDir()

	repo, err := core.Open(ctx, core.Options{Path: filepath.Join(t.TempDir(), "repo.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	var hosted *local.Hosted
	hosts := sources.New()
	if _, err := hosts.Register(sdk.ContextProvider{
		Info: sdk.ContextProviderInfo{Kind: "github", Provides: []string{"workspace", "repo"}},
		Connect: func(context.Context, map[string]any) (sdk.Capabilities, error) {
			var err error
			hosted, err = local.NewHosted(local.HostedOptions{
				DataDir:   dataDir,
				Path:      "owner/repo",
				Namespace: []string{"github"},
				RemoteURL: "file://" + filepath.ToSlash(remoteDir),
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
	thread, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "Merge origin/main"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PostMessage(ctx, project.ID, core.MessageInput{
		ConversationID: thread.ID, Author: "user", Content: "Make a branch edit",
	}); err != nil {
		t.Fatal(err)
	}
	turnID1, err := repo.StartScheduledTurn(ctx, thread.ID, "mock")
	if err != nil || turnID1 == "" {
		t.Fatalf("StartScheduledTurn 1: id=%q err=%v", turnID1, err)
	}

	runnerInstance := &Runner{Repository: repo, Workspaces: manager}
	conv := core.ConversationView{ID: thread.ID, ProjectID: project.ID, Role: "thread"}
	turn1 := &turn{Runner: runnerInstance, conversation: conv, id: turnID1, changes: newTurnChanges(runnerInstance, conv)}
	if _, err := turn1.writeFile(ctx, args{"path": "README.md", "content": "hello\nbranch line\n"}); err != nil {
		t.Fatal(err)
	}
	ws, err := manager.ForThread(ctx, project.ID, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	workDir := ws.Info().Label
	runShellGit := func(dir string, gitArgs ...string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, "git", gitArgs...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v in %s failed: %v\n%s", gitArgs, dir, err, string(out))
		}
	}
	runShellGit(workDir, "config", "user.name", "Test")
	runShellGit(workDir, "config", "user.email", "test@example.invalid")
	runShellGit(workDir, "add", "README.md")
	runShellGit(workDir, "commit", "-qm", "branch change")
	turn1.changes.record(ctx, turnID1)
	if err := repo.FinishTurn(ctx, thread.ID, turnID1, "completed", nil); err != nil {
		t.Fatal(err)
	}

	// Advance remote main with a large upstream file while the branch is open.
	if err := os.WriteFile(filepath.Join(remoteDir, "upstream.txt"), []byte(strings.Repeat("upstream\n", 80)), 0644); err != nil {
		t.Fatal(err)
	}
	remoteGit := local.NewGit(remoteDir, "file")
	if res := remoteGit(ctx, []string{"add", "upstream.txt"}, local.GitOptions{}); res.ExitCode != 0 {
		t.Fatalf("remote add: %s", res.Stderr)
	}
	if res := remoteGit(ctx, []string{"commit", "-qm", "large upstream commit"}, local.GitOptions{}); res.ExitCode != 0 {
		t.Fatalf("remote commit: %s", res.Stderr)
	}

	// Second turn: fetch origin/main and merge it in the user shell (under core.autocrlf=true).
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
	if err := hosted.Fetch(ctx); err != nil {
		t.Fatal(err)
	}
	runShellGit(workDir, "merge", "--no-ff", "-m", "Merge origin/main", "refs/remotes/origin/main")
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
	ctx := t.Context()
	outerDir := initTestRemoteRepo(t)
	if err := os.WriteFile(filepath.Join(outerDir, ".gitignore"), []byte("packages/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	initCmd := exec.CommandContext(ctx, "git", "add", ".gitignore")
	initCmd.Dir = outerDir
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("git add .gitignore: %v\n%s", err, string(out))
	}
	initCommit := exec.CommandContext(ctx, "git", "commit", "-q", "-m", "ignore packages")
	initCommit.Dir = outerDir
	if out, err := initCommit.CombinedOutput(); err != nil {
		t.Fatalf("git commit .gitignore: %v\n%s", err, string(out))
	}
	dataDir := t.TempDir()

	repo, err := core.Open(ctx, core.Options{Path: filepath.Join(t.TempDir(), "repo.sqlite")})
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
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v in %s failed: %v\n%s", args, dir, err, string(out))
		}
		return strings.TrimSpace(string(out))
	}
	runGit(workDir, "config", "user.name", "Test")
	runGit(workDir, "config", "user.email", "test@example.invalid")
	runGit(subDir, "init", "-q", "-b", "main")
	runGit(subDir, "config", "user.name", "Test")
	runGit(subDir, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(subDir, "lib.go"), []byte("package sub\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(subDir, "add", "lib.go")
	runGit(subDir, "commit", "-q", "-m", "init sub")
	subBaseHead := runGit(subDir, "rev-parse", "HEAD")

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
	runGit(workDir, "add", "README.md")
	runGit(workDir, "commit", "-q", "-m", "outer change")
	if err := os.WriteFile(filepath.Join(subDir, "lib.go"), []byte("package sub\n\nfunc Hello() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(subDir, "add", "lib.go")
	runGit(subDir, "commit", "-q", "-m", "sub change")

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

func testFileURL(p string) string {
	p = filepath.ToSlash(p)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}

func TestTurnChangesMidTurnCreatedWorktreeRecordsForkBaselineAndCommits(t *testing.T) {
	ctx := t.Context()

	runGit := func(dir string, args ...string) string {
		t.Helper()
		res := local.NewGit(dir, "file")(ctx, args, local.GitOptions{})
		if res.ExitCode != 0 {
			t.Fatalf("git %v in %s failed: %s", args, dir, res.Stderr)
		}
		return strings.TrimSpace(res.Stdout)
	}

	// Build outer umbrella repo.
	outerDir := initTestRemoteRepo(t)
	if err := os.WriteFile(filepath.Join(outerDir, ".gitignore"), []byte("nested-worktrees/\nsubrepos/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(outerDir, "add", ".gitignore")
	runGit(outerDir, "commit", "-q", "-m", "ignore nested-worktrees and subrepos")

	// Build external bare remote with an unrelated orphan branch "vendor-orphan" and a "main" branch
	// referencing itself as submodule "vendor-bundle" tracking "vendor-orphan".
	bareRemote := filepath.Join(t.TempDir(), "remote.git")
	if err := os.MkdirAll(bareRemote, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(bareRemote, "init", "--bare", "-q", "-b", "main")
	bareURL := testFileURL(bareRemote)

	orphanSeed := t.TempDir()
	runGit(orphanSeed, "init", "-q", "-b", "vendor-orphan")
	runGit(orphanSeed, "config", "user.name", "Test")
	runGit(orphanSeed, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(orphanSeed, "bundle.js"), []byte("export const v = 1;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(orphanSeed, "add", "bundle.js")
	runGit(orphanSeed, "commit", "-q", "-m", "orphan init")
	runGit(orphanSeed, "remote", "add", "origin", bareURL)
	runGit(orphanSeed, "push", "-q", "origin", "vendor-orphan")

	hostRepo := t.TempDir()
	runGit(hostRepo, "init", "-q", "-b", "main")
	runGit(hostRepo, "config", "user.name", "Test")
	runGit(hostRepo, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(hostRepo, "pkg.go"), []byte("package pkg\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(hostRepo, "add", "pkg.go")
	runGit(hostRepo, "commit", "-q", "-m", "host init")
	runGit(hostRepo, "-c", "protocol.file.allow=always", "submodule", "add", "-q", "-b", "vendor-orphan", bareURL, "vendor-bundle")
	runGit(hostRepo, "commit", "-q", "-m", "add orphan submodule")
	runGit(hostRepo, "remote", "add", "origin", bareURL)
	runGit(hostRepo, "push", "-q", "-u", "origin", "main")
	forkSHA := runGit(hostRepo, "rev-parse", "HEAD")

	// Set up Ariadne core + workspaces + runner.
	dataDir, err := os.MkdirTemp("", "ad-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dataDir) })
	repo, err := core.Open(ctx, core.Options{Path: filepath.Join(t.TempDir(), "repo.sqlite")})
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
	if err := os.WriteFile(filepath.Join(mid1Dir, "pkg.go"), []byte("package pkg\n\nfunc Mid1() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(mid1Dir, "add", "pkg.go")
	runGit(mid1Dir, "commit", "-q", "-m", "mid1 commit")
	mid1Head := runGit(mid1Dir, "rev-parse", "HEAD")

	// 2. Mid-turn created worktree + commit C1 + uncommitted edit.
	mid2Dir := filepath.Join(workDir, "nested-worktrees", "feat-mid2")
	runGit(hostRepo, "worktree", "add", "-qb", "feat/mid2", mid2Dir, forkSHA)
	if err := os.WriteFile(filepath.Join(mid2Dir, "pkg.go"), []byte("package pkg\n\nfunc Mid2() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(mid2Dir, "add", "pkg.go")
	runGit(mid2Dir, "commit", "-q", "-m", "mid2 commit")
	if err := os.WriteFile(filepath.Join(mid2Dir, "wip.go"), []byte("package pkg\n// wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 3. Mid-turn clean git clone + initialized clean orphan submodule inside it -> must produce 0 turn-changes and 0 baselines.
	cleanCloneDir := filepath.Join(workDir, "subrepos", "clean-clone")
	if err := os.MkdirAll(filepath.Dir(cleanCloneDir), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(filepath.Dir(cleanCloneDir), "clone", "-q", bareURL, cleanCloneDir)
	runGit(cleanCloneDir, "-c", "protocol.file.allow=always", "submodule", "update", "--init", "-q", "vendor-bundle")

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
		if r.Changes == nil || r.Changes.From != forkSHA || len(r.Changes.Files) != 1 || r.Changes.Files[0].Path != "pkg.go" {
			t.Fatalf("expected committed pkg.go change relative to forkSHA %s for %s, got %+v", forkSHA, r.RepoPath, r.Changes)
		}
		if r.RepoPath == "nested-worktrees/feat-mid2" && (len(r.Changes.Uncommitted) != 1 || r.Changes.Uncommitted[0] != "wip.go") {
			t.Fatalf("expected uncommitted wip.go for feat-mid2, got %+v", r.Changes)
		}
	}
}
