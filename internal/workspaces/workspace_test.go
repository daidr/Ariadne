package workspaces

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/background"
	"github.com/daidr/Ariadne/internal/commandpolicy"
	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/local"
	"github.com/daidr/Ariadne/internal/sources"
	"github.com/daidr/Ariadne/sdk"
)

type measuredWorkspace struct {
	sdk.Workspace
	closed *atomic.Int32
	exec   background.Executor
}

func (workspace *measuredWorkspace) Close() error {
	err := workspace.Workspace.Close()
	workspace.closed.Add(1)
	return err
}
func (workspace *measuredWorkspace) Exec(ctx context.Context, command string, options sdk.ExecOptions) (sdk.ExecResult, error) {
	if workspace.exec != nil {
		return workspace.exec(ctx, options)
	}
	return workspace.Workspace.Exec(ctx, command, options)
}

type factoryProbe struct {
	mu                      sync.Mutex
	requests                []sdk.WorkspaceRequest
	opened, closed, removed atomic.Int32
	dirty                   atomic.Bool
	exec                    background.Executor
	isolate                 bool
	remove                  func(context.Context, string, sdk.WorktreeRemoveOptions) (bool, error)
}

func workspaceManager(t *testing.T, probe *factoryProbe, configure func(*Options)) (*Manager, *core.Repository, core.ProjectView) {
	t.Helper()
	repo, project, path := testRepository(t)
	registry := sources.New()
	provider := sdk.ContextProvider{Info: sdk.ContextProviderInfo{Kind: "test", Provides: []string{"workspace"}}, Connect: func(ctx context.Context, _ map[string]any) (sdk.Capabilities, error) {
		return sdk.Capabilities{Workspace: &sdk.WorkspaceCapability{Isolated: probe.isolate, Open: func(ctx context.Context, request sdk.WorkspaceRequest) (sdk.Workspace, error) {
			probe.mu.Lock()
			probe.requests = append(probe.requests, request)
			probe.mu.Unlock()
			var base *string
			if request.Base != nil {
				base = *request.Base
			}
			workspace, err := local.NewWorkspace(path, local.WorkspaceOptions{ID: request.ThreadID, Branch: request.Branch, Base: base})
			if err != nil {
				return nil, err
			}
			probe.opened.Add(1)
			return &measuredWorkspace{Workspace: workspace, closed: &probe.closed, exec: probe.exec}, nil
		}, Branches: &sdk.BranchCapability{Remove: func(ctx context.Context, id string, options sdk.WorktreeRemoveOptions) (bool, error) {
			probe.removed.Add(1)
			if probe.remove != nil {
				return probe.remove(ctx, id, options)
			}
			return !probe.dirty.Load(), nil
		}}}}, nil
	}}
	if _, err := registry.Register(provider); err != nil {
		t.Fatal(err)
	}
	options := Options{Repository: repo, Sources: registry}
	if configure != nil {
		configure(&options)
	}
	manager, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeManager(t, manager) })
	return manager, repo, project
}
func newThread(t *testing.T, repo *core.Repository, project string) core.ThreadView {
	t.Helper()
	thread, err := repo.CreateThread(t.Context(), project, core.NewThread{Title: "work"})
	if err != nil {
		t.Fatal(err)
	}
	return thread
}
func TestWorkspaceCacheExpiryPreservesBranchNullBaseAndFiles(t *testing.T) {
	probe := &factoryProbe{isolate: true}
	manager, repo, project := workspaceManager(t, probe, func(options *Options) { options.CacheTTL = 15 * time.Millisecond })
	thread := newThread(t, repo, project.ID)
	workspace, err := manager.ForThread(t.Context(), project.ID, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	info := workspace.Info()
	if info.Branch == nil {
		t.Fatal("missing thread branch")
	}
	branch := *info.Branch
	*info.Branch = "mutated"
	if *workspace.Info().Branch != branch {
		t.Fatal("Info exposed mutable metadata")
	}
	if err := workspace.WriteFile(t.Context(), "kept.txt", "user changes"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for probe.closed.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("idle workspace handle did not expire")
		}
		time.Sleep(time.Millisecond)
	}
	if text, err := workspace.ReadFile(t.Context(), "kept.txt"); err != nil || text != "user changes" {
		t.Fatal("expiry deleted workspace files", err)
	}
	if probe.opened.Load() < 2 || probe.removed.Load() != 0 {
		t.Fatal("expiry did not release/reopen handle independently of worktree deletion")
	}
	probe.mu.Lock()
	requests := append([]sdk.WorkspaceRequest(nil), probe.requests...)
	probe.mu.Unlock()
	if requests[0].Base != nil || requests[1].Base == nil || *requests[1].Base != nil || *requests[1].Branch != branch {
		t.Fatal("reopen changed absent/null base or branch", requests)
	}
	first, err := manager.TakeSetup(t.Context(), thread.ID)
	if err != nil || !strings.Contains(first, "note_setup") {
		t.Fatal("fresh workspace hints missing", first, err)
	}
	second, _ := manager.TakeSetup(t.Context(), thread.ID)
	if second != "" {
		t.Fatal("hint repeated", second)
	}
	if len(manager.gates.gates) != 0 {
		t.Fatal("thread locks retained idle identities")
	}
}
func TestWorkspaceSetupRunsOnceWithProjectShellEnvAndTimeout(t *testing.T) {
	optionsSeen := make(chan sdk.ExecOptions, 1)
	finished := make(chan struct{})
	probe := &factoryProbe{isolate: true, exec: func(ctx context.Context, options sdk.ExecOptions) (sdk.ExecResult, error) {
		optionsSeen <- options
		select {
		case <-ctx.Done():
			return sdk.ExecResult{ExitCode: 130}, ctx.Err()
		case <-finished:
			return sdk.ExecResult{ExitCode: 7}, nil
		}
	}}
	manager, repo, project := workspaceManager(t, probe, func(options *Options) {
		options.Env = func(string) map[string]string { return map[string]string{"CUSTOM": "present"} }
		options.SetupTimeout = func(string) time.Duration { return 123 * time.Second }
	})
	_, err := repo.SetProjectSettings(t.Context(), project.ID, map[string]any{"workspace": map[string]any{"auto": map[string]any{"setup": false}, "setup": "user setup", "shell": "sh"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	thread := newThread(t, repo, project.ID)
	if _, err := manager.ForThread(t.Context(), project.ID, thread.ID); err != nil {
		t.Fatal(err)
	}
	var seen sdk.ExecOptions
	select {
	case seen = <-optionsSeen:
	case <-time.After(time.Second):
		t.Fatal("setup did not start after workspace opened")
	}
	if seen.Env["CUSTOM"] != "present" || seen.Timeout != 123*time.Second || len(seen.Shell) != 2 || seen.Shell[0] != "sh" {
		t.Fatal("setup lost user configuration", seen)
	}
	if manager.SetupRunning(thread.ID) == nil {
		t.Fatal("setup absent from status")
	}
	if _, err := manager.ForThread(t.Context(), project.ID, thread.ID); err != nil {
		t.Fatal(err)
	}
	if len(manager.Background.List(thread.ID)) != 1 {
		t.Fatal("cached workspace reran setup")
	}
	close(finished)
	note, err := manager.SetupDone(t.Context(), thread.ID)
	if err != nil || !strings.Contains(note, "exit code 7") {
		t.Fatal("setup failure not reported", note, err)
	}
	if note, _ := manager.SetupDone(t.Context(), thread.ID); note != "" {
		t.Fatal("setup failure reported twice")
	}
	if _, err := manager.TakeSetup(t.Context(), thread.ID); err != nil {
		t.Fatal(err)
	}
}
func TestModelNotedSetupIsHeldByPolicyAndSkippedForSharedWorkspace(t *testing.T) {
	for _, isolated := range []bool{true, false} {
		t.Run(map[bool]string{true: "isolated", false: "shared"}[isolated], func(t *testing.T) {
			probe := &factoryProbe{isolate: isolated}
			manager, repo, project := workspaceManager(t, probe, func(options *Options) {
				options.CommandRules = func(string) []commandpolicy.Rule { return []commandpolicy.Rule{{Pattern: "ask first", Action: "ask"}} }
			})
			author := newThread(t, repo, project.ID)
			if _, err := repo.NoteSetup(t.Context(), author.ID, "ask first"); err != nil {
				t.Fatal(err)
			}
			thread := newThread(t, repo, project.ID)
			if _, err := manager.ForThread(t.Context(), project.ID, thread.ID); err != nil {
				t.Fatal(err)
			}
			note, _ := manager.TakeSetup(t.Context(), thread.ID)
			if strings.Contains(note, "did not run") != isolated || len(manager.Background.List(thread.ID)) != 0 {
				t.Fatal("setup policy was bypassed", note)
			}
		})
	}
}
func TestWorkspaceRemovalWaitsForActualOperationsAndHonorsDirtyFiles(t *testing.T) {
	entered, finish := make(chan struct{}), make(chan struct{})
	probe := &factoryProbe{exec: func(ctx context.Context, _ sdk.ExecOptions) (sdk.ExecResult, error) {
		close(entered)
		<-finish
		return sdk.ExecResult{}, nil
	}}
	manager, repo, project := workspaceManager(t, probe, nil)
	thread := newThread(t, repo, project.ID)
	workspace, err := manager.ForThread(t.Context(), project.ID, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	operation := make(chan error, 1)
	go func() { _, err := workspace.Exec(t.Context(), "active", sdk.ExecOptions{}); operation <- err }()
	<-entered
	if result, err := manager.RemoveWorktree(t.Context(), project.ID, thread.ID, "user"); err != nil || result != "busy" || probe.removed.Load() != 0 {
		t.Fatal("deleted workspace during operation", result, err)
	}
	close(finish)
	if err := <-operation; err != nil {
		t.Fatal(err)
	}
	probe.dirty.Store(true)
	if result, err := manager.RemoveWorktree(t.Context(), project.ID, thread.ID, "user"); err != nil || result != "dirty" {
		t.Fatal("dirty directory deleted", result, err)
	}
	probe.dirty.Store(false)
	if result, err := manager.RemoveWorktree(t.Context(), project.ID, thread.ID, "idle"); err != nil || result != "removed" {
		t.Fatal("idle removal failed", result, err)
	}
	view, _ := repo.GetThread(t.Context(), thread.ID)
	if view.Workspace == nil || !view.Workspace.Removed || !view.Workspace.Parked {
		t.Fatal("idle removal did not preserve parked branch", view.Workspace)
	}
}
func TestDiscardCancelsCommandsReleasesHandlesAndBlocksReopen(t *testing.T) {
	started := make(chan struct{})
	probe := &factoryProbe{exec: func(ctx context.Context, _ sdk.ExecOptions) (sdk.ExecResult, error) {
		close(started)
		<-ctx.Done()
		return sdk.ExecResult{ExitCode: 130}, ctx.Err()
	}}
	manager, repo, project := workspaceManager(t, probe, nil)
	thread := newThread(t, repo, project.ID)
	workspace, err := manager.ForThread(t.Context(), project.ID, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	manager.Background.Start(thread.ID, "running", func(ctx context.Context, options sdk.ExecOptions) (sdk.ExecResult, error) {
		return workspace.Exec(ctx, "running", options)
	}, background.StartOptions{})
	<-started
	probe.remove = func(ctx context.Context, _ string, options sdk.WorktreeRemoveOptions) (bool, error) {
		if !options.Discard || probe.closed.Load() != probe.opened.Load() {
			return false, errors.New("removed before handle release")
		}
		return true, nil
	}
	if err := manager.DiscardProject(t.Context(), project.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.ReadFile(t.Context(), "file.txt"); err == nil {
		t.Fatal("deleted project workspace reopened")
	}
	if len(manager.Background.List(thread.ID)) != 0 {
		t.Fatal("project retained background commands")
	}
	if err := manager.InvalidateProject(t.Context(), project.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ForThread(t.Context(), project.ID, thread.ID); err != nil {
		t.Fatal("invalidation did not release project guard", err)
	}
}
func TestRealLocalSetupAndStopReleaseWindowsDirectory(t *testing.T) {
	probe := &factoryProbe{}
	manager, repo, project := workspaceManager(t, probe, nil)
	thread := newThread(t, repo, project.ID)
	workspace, err := manager.ForThread(t.Context(), project.ID, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.Exec(t.Context(), "echo native > initialized.txt", sdk.ExecOptions{Timeout: 5 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if data, err := workspace.ReadFile(t.Context(), "initialized.txt"); err != nil || !strings.Contains(data, "native") {
		t.Fatal("native workspace command failed", data, err)
	}
	path := workspace.Info().Label
	if err := manager.StopThread(t.Context(), thread.ID); err != nil {
		t.Fatal(err)
	}
	if err := workspace.Close(); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(filepath.Dir(path), "released")
	if err := os.Rename(path, moved); err != nil {
		t.Fatal("closed workspace still holds directory", err)
	}
}
