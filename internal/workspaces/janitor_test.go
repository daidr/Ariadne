package workspaces

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/local"
	"github.com/daidr/Ariadne/internal/sources"
)

func finishedThread(t *testing.T, manager *Manager, repo *core.Repository, project string) core.ThreadView {
	t.Helper()
	thread := newThread(t, repo, project)
	if _, err := manager.ForThread(t.Context(), project, thread.ID); err != nil {
		t.Fatal(err)
	}
	base := "release/integration"
	branch := "work/" + thread.ID
	if err := repo.RecordWorkspace(t.Context(), thread.ID, "workspace", &branch, &base); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.StartThread(t.Context(), thread.ID); err != nil {
		t.Fatal(err)
	}
	thread, err := repo.ReportThread(t.Context(), thread.ID, core.ThreadReport{Kind: "ready", Summary: "done", Headline: "complete"}, nil, core.ReviewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !candidate(thread) {
		t.Fatal("finished thread not candidate", thread.Stage)
	}
	return thread
}
func TestJanitorKeepsExternalDeliverySeparateFromWorktreeRetention(t *testing.T) {
	t.Parallel()
	for _, retention := range []string{"auto", "merged", "keep"} {
		t.Run(retention, func(t *testing.T) {
			probe := &factoryProbe{}
			manager, repo, project := workspaceManager(t, probe, nil)
			if _, err := repo.SetProjectSettings(t.Context(), project.ID, map[string]any{"delivery": map[string]any{"mode": "external", "instructions": "submit to platform"}, "workspace": map[string]any{"retention": retention}}, nil); err != nil {
				t.Fatal(err)
			}
			thread := finishedThread(t, manager, repo, project.ID)
			janitor := NewJanitor(manager, JanitorOptions{Now: func() time.Time { return time.UnixMilli(thread.ActiveAt).Add(25 * time.Hour) }})
			if err := janitor.Check(t.Context(), thread.ID); err != nil {
				t.Fatal(err)
			}
			current, _ := repo.GetThread(t.Context(), thread.ID)
			if current.Workspace.Removed != (retention == "auto") {
				t.Fatal("retention changed external completion", retention, current.Workspace)
			}
			if current.Merged != nil || probe.removed.Load() > 1 {
				t.Fatal("external completion triggered merge")
			}
		})
	}
}
func TestJanitorClosedPullRequestLandsAndParkedBranchBecomesMerged(t *testing.T) {
	t.Parallel()
	probe := &factoryProbe{}
	manager, repo, project := workspaceManager(t, probe, nil)
	thread := finishedThread(t, manager, repo, project.ID)
	janitor := NewJanitor(manager, JanitorOptions{Now: func() time.Time { return time.UnixMilli(thread.ActiveAt).Add(25 * time.Hour) }})
	if err := janitor.Check(t.Context(), thread.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.SyncPullRequest(t.Context(), thread.ID, map[string]any{"state": "closed", "number": 1, "headSha": "sha", "base": "integration"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := janitor.Check(t.Context(), thread.ID); err != nil {
		t.Fatal(err)
	}
	current, _ := repo.GetThread(t.Context(), thread.ID)
	if !current.Workspace.Removed || current.Workspace.Parked || probe.removed.Load() != 1 {
		t.Fatal("parked branch not marked landed", current.Workspace, probe.removed.Load())
	}
}
func TestJanitorStopReleasesEventSubscriptionAndPendingDelay(t *testing.T) {
	t.Parallel()
	probe := &factoryProbe{}
	manager, repo, project := workspaceManager(t, probe, nil)
	thread := finishedThread(t, manager, repo, project.ID)
	janitor := NewJanitor(manager, JanitorOptions{Delay: 20 * time.Millisecond, Interval: time.Hour})
	if err := janitor.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := repo.SyncPullRequest(t.Context(), thread.ID, map[string]any{"state": "open", "number": 1}, nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := janitor.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := repo.SyncPullRequest(t.Context(), thread.ID, map[string]any{"state": "closed", "number": 1}, nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if probe.removed.Load() != 0 {
		t.Fatal("stopped janitor still ran delayed cleanup")
	}
	if err := janitor.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := janitor.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func runGitOK(t *testing.T, dir string, args ...string) string {
	t.Helper()
	res := local.NewGit(dir, "file")(t.Context(), args, local.GitOptions{Whole: true})
	if res.ExitCode != 0 {
		t.Fatalf("git %v in %s failed: %s", args, dir, res.Stderr)
	}
	return strings.TrimSpace(res.Stdout)
}

func initLocalRepo(t *testing.T, dir string, exclude string) string {
	t.Helper()
	sha := initTemplateRepo(t, dir)
	if exclude != "" {
		writeWorkspaceGitExclude(t, dir, exclude)
	}
	return sha
}

func localJanitorFixture(t *testing.T, exclude string) (*Manager, *core.Repository, core.ProjectView, string) {
	t.Helper()
	root := t.TempDir()
	repoDir := filepath.Join(root, "repo")
	initLocalRepo(t, repoDir, exclude)
	store, err := core.Open(t.Context(), core.Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	name := "umbrella"
	project, err := store.CreateProject(t.Context(), &name, core.ProjectStart{
		Source: map[string]any{"kind": "local-directory", "settings": map[string]any{"path": repoDir}},
	})
	if err != nil {
		t.Fatal(err)
	}
	registry := sources.New()
	if _, err := registry.Register(local.DirectoryProvider(filepath.Join(root, "data"), nil, true)); err != nil {
		t.Fatal(err)
	}
	manager, err := New(Options{Repository: store, Sources: registry})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeManager(t, manager) })
	return manager, store, project, repoDir
}

func openAndReportThread(t *testing.T, manager *Manager, repo *core.Repository, projectID, kind string) (core.ThreadView, string) {
	t.Helper()
	thread := newThread(t, repo, projectID)
	ws, err := manager.ForThread(t.Context(), projectID, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	dir := ws.Info().Label
	if _, err := repo.StartThread(t.Context(), thread.ID); err != nil {
		t.Fatal(err)
	}
	reported, err := repo.ReportThread(t.Context(), thread.ID, core.ThreadReport{Kind: kind, Summary: "done", Headline: "done"}, nil, core.ReviewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return reported, dir
}

func TestJanitorMultiRepoEmbeddedProtectionAndCleanup(t *testing.T) {
	t.Parallel()
	t.Run("nested worktree protected until merged then cleaned without dangling", func(t *testing.T) {
		t.Parallel()
		manager, repo, project, _ := localJanitorFixture(t, "nested/\n")
		hostDir := filepath.Join(t.TempDir(), "host-subrepo")
		initLocalRepo(t, hostDir, "")

		thread, wtDir := openAndReportThread(t, manager, repo, project.ID, "ready")
		nestedDir := filepath.Join(wtDir, "nested", "svc")
		runGitOK(t, hostDir, "worktree", "add", "-qb", "feat/svc", nestedDir)
		if err := os.WriteFile(filepath.Join(nestedDir, "README.md"), []byte("hello\nupdated\n"), 0644); err != nil {
			t.Fatal(err)
		}
		runGitOK(t, nestedDir, "-c", "user.name=Ariadne Test", "-c", "user.email=test@example.invalid", "commit", "-qam", "subrepo change")

		janitor := NewJanitor(manager, JanitorOptions{})
		if err := janitor.Check(t.Context(), thread.ID); err != nil {
			t.Fatal(err)
		}
		current, err := repo.GetThread(t.Context(), thread.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Workspace.Removed {
			t.Fatal("worktree with unmerged nested worktree was deleted")
		}
		if _, err := os.Stat(nestedDir); err != nil {
			t.Fatalf("nested worktree directory disappeared: %v", err)
		}

		// Merge nested branch into host main -> Janitor cleans up nested + outer cleanly.
		runGitOK(t, hostDir, "merge", "--ff-only", "feat/svc")
		if err := janitor.Check(t.Context(), thread.ID); err != nil {
			t.Fatal(err)
		}
		current, err = repo.GetThread(t.Context(), thread.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !current.Workspace.Removed || current.Workspace.Parked {
			t.Fatalf("merged multi-repo worktree was not removed: %+v", current.Workspace)
		}
		if _, err := os.Stat(wtDir); !os.IsNotExist(err) {
			t.Fatalf("outer worktree still on disk: %v", err)
		}
		if entries, err := os.ReadDir(filepath.Join(hostDir, ".git", "worktrees")); err == nil && len(entries) > 0 {
			t.Fatalf("host repo still has dangling worktree entries: %v", entries)
		}
	})

	t.Run("standalone unpushed embedded repo protected under merged and idle", func(t *testing.T) {
		t.Parallel()
		manager, repo, project, _ := localJanitorFixture(t, "subrepos/\n")
		if _, err := repo.SetProjectSettings(t.Context(), project.ID, map[string]any{
			"workspace": map[string]any{"retention": "auto"},
		}, nil); err != nil {
			t.Fatal(err)
		}
		thread, wtDir := openAndReportThread(t, manager, repo, project.ID, "ready")
		subDir := filepath.Join(wtDir, "subrepos", "standalone")
		initLocalRepo(t, subDir, "")

		janitor := NewJanitor(manager, JanitorOptions{
			Now: func() time.Time { return time.UnixMilli(thread.ActiveAt).Add(25 * time.Hour) },
		})
		if err := janitor.Check(t.Context(), thread.ID); err != nil {
			t.Fatal(err)
		}
		current, err := repo.GetThread(t.Context(), thread.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Workspace.Removed {
			t.Fatal("standalone unpushed embedded repo was deleted under idle")
		}
		if _, err := os.Stat(filepath.Join(subDir, "README.md")); err != nil {
			t.Fatalf("standalone embedded repo disappeared: %v", err)
		}
	})

	t.Run("research thread with clean readonly embedded clone is reclaimed", func(t *testing.T) {
		t.Parallel()
		manager, repo, project, _ := localJanitorFixture(t, "subrepos/\n")

		thread, wtDir := openAndReportThread(t, manager, repo, project.ID, "done")
		subDir := filepath.Join(wtDir, "subrepos", "readonly")
		sha := initLocalRepo(t, subDir, "")
		originRef := filepath.Join(subDir, ".git", "refs", "remotes", "origin", "main")
		if err := os.MkdirAll(filepath.Dir(originRef), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(originRef, []byte(sha+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(subDir, ".git", "refs", "remotes", "origin", "HEAD"), []byte("ref: refs/remotes/origin/main\n"), 0644); err != nil {
			t.Fatal(err)
		}
		cfgPath := filepath.Join(subDir, ".git", "config")
		cfgRaw, err := os.ReadFile(cfgPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cfgPath, append(cfgRaw, []byte("\n[remote \"origin\"]\n\turl = file:///origin.git\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n[branch \"main\"]\n\tremote = origin\n\tmerge = refs/heads/main\n")...), 0644); err != nil {
			t.Fatal(err)
		}

		janitor := NewJanitor(manager, JanitorOptions{})
		if err := janitor.Check(t.Context(), thread.ID); err != nil {
			t.Fatal(err)
		}
		current, err := repo.GetThread(t.Context(), thread.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !current.Workspace.Removed {
			t.Fatalf("clean research thread with readonly embedded clone was not reclaimed: %+v", current.Workspace)
		}
		if _, err := os.Stat(wtDir); !os.IsNotExist(err) {
			t.Fatalf("outer worktree still on disk: %v", err)
		}
	})

	t.Run("single repo with dist artifacts is reclaimed on merged", func(t *testing.T) {
		t.Parallel()
		manager, repo, project, _ := localJanitorFixture(t, "dist/\n")
		thread, wtDir := openAndReportThread(t, manager, repo, project.ID, "done")
		if err := os.MkdirAll(filepath.Join(wtDir, "dist"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(wtDir, "dist", "bundle.js"), []byte("built"), 0644); err != nil {
			t.Fatal(err)
		}

		janitor := NewJanitor(manager, JanitorOptions{})
		if err := janitor.Check(t.Context(), thread.ID); err != nil {
			t.Fatal(err)
		}
		current, err := repo.GetThread(t.Context(), thread.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !current.Workspace.Removed {
			t.Fatalf("single repo with dist/ was not reclaimed: %+v", current.Workspace)
		}
		if _, err := os.Stat(wtDir); !os.IsNotExist(err) {
			t.Fatalf("single repo worktree still on disk: %v", err)
		}
	})

	t.Run("closed outer PR still protects unmerged embedded repo", func(t *testing.T) {
		t.Parallel()
		manager, repo, project, _ := localJanitorFixture(t, "nested/\n")
		hostDir := filepath.Join(t.TempDir(), "host-subrepo")
		initLocalRepo(t, hostDir, "")

		thread, wtDir := openAndReportThread(t, manager, repo, project.ID, "ready")
		nestedDir := filepath.Join(wtDir, "nested", "svc")
		runGitOK(t, hostDir, "worktree", "add", "-qb", "feat/pr-guard", nestedDir)
		if err := os.WriteFile(filepath.Join(nestedDir, "README.md"), []byte("hello\nunmerged\n"), 0644); err != nil {
			t.Fatal(err)
		}
		runGitOK(t, nestedDir, "-c", "user.name=Ariadne Test", "-c", "user.email=test@example.invalid", "commit", "-qam", "unmerged subrepo commit")

		if err := repo.SyncPullRequest(t.Context(), thread.ID, map[string]any{
			"state": "closed", "number": 42, "headSha": "abc", "base": "main",
		}, nil); err != nil {
			t.Fatal(err)
		}

		janitor := NewJanitor(manager, JanitorOptions{})
		if err := janitor.Check(t.Context(), thread.ID); err != nil {
			t.Fatal(err)
		}
		current, err := repo.GetThread(t.Context(), thread.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Workspace.Removed {
			t.Fatal("closed outer PR deleted worktree with unmerged embedded repo")
		}
		if _, err := os.Stat(nestedDir); err != nil {
			t.Fatalf("nested worktree was deleted: %v", err)
		}
	})
}
