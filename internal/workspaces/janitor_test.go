package workspaces

import (
	"context"
	"os"
	"os/exec"
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

func initLocalRepo(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	runGitOK(t, dir, "init", "-b", "main")
	runGitOK(t, dir, "config", "user.name", "Ariadne Test")
	runGitOK(t, dir, "config", "user.email", "test@example.invalid")
	for name, content := range files {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	runGitOK(t, dir, "add", "--all")
	runGitOK(t, dir, "commit", "-qm", "init")
}

func localJanitorFixture(t *testing.T, files map[string]string) (*Manager, *core.Repository, core.ProjectView, string) {
	t.Helper()
	root := t.TempDir()
	repoDir := filepath.Join(root, "repo")
	initLocalRepo(t, repoDir, files)
	store, err := core.Open(t.Context(), core.Options{Path: filepath.Join(root, "data.sqlite")})
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
	t.Run("nested worktree protected until merged then cleaned without dangling", func(t *testing.T) {
		manager, repo, project, _ := localJanitorFixture(t, map[string]string{
			".gitignore": "nested/\n",
			"README.md":  "umbrella\n",
		})
		hostDir := filepath.Join(t.TempDir(), "host-subrepo")
		initLocalRepo(t, hostDir, map[string]string{"app.go": "package app\n"})

		thread, wtDir := openAndReportThread(t, manager, repo, project.ID, "ready")
		nestedDir := filepath.Join(wtDir, "nested", "svc")
		runGitOK(t, hostDir, "worktree", "add", "-qb", "feat/svc", nestedDir)
		if err := os.WriteFile(filepath.Join(nestedDir, "app.go"), []byte("package app\n// updated\n"), 0644); err != nil {
			t.Fatal(err)
		}
		runGitOK(t, nestedDir, "add", "--all")
		runGitOK(t, nestedDir, "commit", "-qm", "subrepo change")

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
		wtList := runGitOK(t, hostDir, "worktree", "list", "--porcelain")
		if strings.Count(wtList, "worktree ") != 1 {
			t.Fatalf("host repo still has dangling worktree:\n%s", wtList)
		}
	})

	t.Run("standalone unpushed embedded repo protected under merged and idle", func(t *testing.T) {
		manager, repo, project, _ := localJanitorFixture(t, map[string]string{
			".gitignore": "subrepos/\n",
			"README.md":  "umbrella\n",
		})
		if _, err := repo.SetProjectSettings(t.Context(), project.ID, map[string]any{
			"workspace": map[string]any{"retention": "auto"},
		}, nil); err != nil {
			t.Fatal(err)
		}
		thread, wtDir := openAndReportThread(t, manager, repo, project.ID, "ready")
		subDir := filepath.Join(wtDir, "subrepos", "standalone")
		initLocalRepo(t, subDir, map[string]string{"main.go": "package main\n"})

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
		if _, err := os.Stat(filepath.Join(subDir, "main.go")); err != nil {
			t.Fatalf("standalone embedded repo disappeared: %v", err)
		}
	})

	t.Run("research thread with clean readonly embedded clone is reclaimed", func(t *testing.T) {
		manager, repo, project, _ := localJanitorFixture(t, map[string]string{
			".gitignore": "subrepos/\n",
			"README.md":  "umbrella\n",
		})
		seedDir := filepath.Join(t.TempDir(), "seed")
		initLocalRepo(t, seedDir, map[string]string{"lib.go": "package lib\n"})
		bareDir := filepath.Join(t.TempDir(), "origin.git")
		runGitOK(t, seedDir, "clone", "--bare", seedDir, bareDir)

		thread, wtDir := openAndReportThread(t, manager, repo, project.ID, "done")
		subDir := filepath.Join(wtDir, "subrepos", "readonly")
		if err := os.MkdirAll(filepath.Dir(subDir), 0755); err != nil {
			t.Fatal(err)
		}
		runGitOK(t, filepath.Dir(subDir), "clone", bareDir, subDir)

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
		manager, repo, project, _ := localJanitorFixture(t, map[string]string{
			".gitignore": "dist/\n",
			"index.ts":   "export const x = 1\n",
		})
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
		manager, repo, project, _ := localJanitorFixture(t, map[string]string{
			".gitignore": "nested/\n",
			"README.md":  "umbrella\n",
		})
		hostDir := filepath.Join(t.TempDir(), "host-subrepo")
		initLocalRepo(t, hostDir, map[string]string{"app.go": "package app\n"})

		thread, wtDir := openAndReportThread(t, manager, repo, project.ID, "ready")
		nestedDir := filepath.Join(wtDir, "nested", "svc")
		runGitOK(t, hostDir, "worktree", "add", "-qb", "feat/pr-guard", nestedDir)
		if err := os.WriteFile(filepath.Join(nestedDir, "app.go"), []byte("package app\n// unmerged\n"), 0644); err != nil {
			t.Fatal(err)
		}
		runGitOK(t, nestedDir, "add", "--all")
		runGitOK(t, nestedDir, "commit", "-qm", "unmerged subrepo commit")

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
