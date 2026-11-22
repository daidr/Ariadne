package runner

import (
	"context"
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
