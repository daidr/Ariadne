package workspaces

import (
	"context"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/core"
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
