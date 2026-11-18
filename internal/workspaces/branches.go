package workspaces

import (
	"context"
	"errors"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/sdk"
)

func withBranches[T any](ctx context.Context, manager *Manager, project, key string, work func(context.Context, *sdk.BranchCapability) (T, error)) (T, error) {
	return withSource(ctx, manager, project, key, func(ctx context.Context, caps sdk.Capabilities) (T, error) {
		if caps.Workspace == nil || caps.Workspace.Branches == nil {
			var zero T
			return zero, errors.New("source has no worktree branches")
		}
		return work(ctx, caps.Workspace.Branches)
	})
}
func (manager *Manager) Branches(ctx context.Context, project string) (*sdk.BranchCapability, error) {
	ctx, done, err := manager.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	if !manager.Available(ctx, project) {
		return nil, nil
	}
	config, err := manager.sourceConfig(ctx, project)
	if err != nil {
		return nil, err
	}
	key := config.key
	raw, err := withSource(ctx, manager, project, key, func(_ context.Context, caps sdk.Capabilities) (*sdk.BranchCapability, error) {
		if caps.Workspace == nil {
			return nil, nil
		}
		return caps.Workspace.Branches, nil
	})
	if err != nil || raw == nil {
		return nil, err
	}
	proxy := &sdk.BranchCapability{}
	if raw.Changes != nil {
		proxy.Changes = func(ctx context.Context, branch sdk.ThreadBranch, base string) (*sdk.BranchChanges, error) {
			return withBranches(ctx, manager, project, key, func(ctx context.Context, b *sdk.BranchCapability) (*sdk.BranchChanges, error) {
				return b.Changes(ctx, branch, base)
			})
		}
	}
	if raw.File != nil {
		proxy.File = func(ctx context.Context, request sdk.FileVersionRequest) (sdk.FileVersions, error) {
			return withBranches(ctx, manager, project, key, func(ctx context.Context, b *sdk.BranchCapability) (sdk.FileVersions, error) {
				return b.File(ctx, request)
			})
		}
	}
	if raw.Merged != nil {
		proxy.Merged = func(ctx context.Context, branch sdk.ThreadBranch, base string) (bool, error) {
			return withBranches(ctx, manager, project, key, func(ctx context.Context, b *sdk.BranchCapability) (bool, error) { return b.Merged(ctx, branch, base) })
		}
	}
	if raw.Merge != nil {
		proxy.Merge = func(ctx context.Context, request sdk.MergeRequest) (sdk.MergeOutcome, error) {
			return withBranches(ctx, manager, project, key, func(ctx context.Context, b *sdk.BranchCapability) (sdk.MergeOutcome, error) {
				return b.Merge(ctx, request)
			})
		}
	}
	if raw.UndoMerge != nil {
		proxy.UndoMerge = func(ctx context.Context, request sdk.UndoMergeRequest) error {
			_, err := withBranches(ctx, manager, project, key, func(ctx context.Context, b *sdk.BranchCapability) (bool, error) {
				return true, b.UndoMerge(ctx, request)
			})
			return err
		}
	}
	if raw.Remove != nil {
		proxy.Remove = func(ctx context.Context, thread string, options sdk.WorktreeRemoveOptions) (bool, error) {
			return withBranches(ctx, manager, project, key, func(ctx context.Context, b *sdk.BranchCapability) (bool, error) {
				return b.Remove(ctx, thread, options)
			})
		}
	}
	if raw.Worktrees != nil {
		proxy.Worktrees = func(ctx context.Context) ([]sdk.WorktreeInfo, error) {
			return withBranches(ctx, manager, project, key, func(ctx context.Context, b *sdk.BranchCapability) ([]sdk.WorktreeInfo, error) {
				return b.Worktrees(ctx)
			})
		}
	}
	if raw.SharesStorage != nil {
		proxy.SharesStorage = func(ctx context.Context) (bool, error) {
			return withBranches(ctx, manager, project, key, func(ctx context.Context, b *sdk.BranchCapability) (bool, error) { return b.SharesStorage(ctx) })
		}
	}
	if raw.Snapshot != nil {
		proxy.Snapshot = func(ctx context.Context, thread string) (*string, error) {
			return withBranches(ctx, manager, project, key, func(ctx context.Context, b *sdk.BranchCapability) (*string, error) { return b.Snapshot(ctx, thread) })
		}
	}
	if raw.Diff != nil {
		proxy.Diff = func(ctx context.Context, before, after string) ([]sdk.ChangedFile, error) {
			return withBranches(ctx, manager, project, key, func(ctx context.Context, b *sdk.BranchCapability) ([]sdk.ChangedFile, error) {
				return b.Diff(ctx, before, after)
			})
		}
	}
	return proxy, nil
}

// RemoveWorktree never deletes a directory in use by an active operation. Idle
// cleanup also leaves commands the user kept running, and uncommitted files.
func (manager *Manager) RemoveWorktree(ctx context.Context, project, thread, reason string) (string, error) {
	if reason != "idle" && reason != "merged" && reason != "user" {
		return "", errors.New("invalid worktree removal reason")
	}
	ctx, done, err := manager.begin(ctx)
	if err != nil {
		return "", err
	}
	defer done()
	id := workspaceID(project, thread)
	manager.mentions.Remove(id)
	unlock, err := manager.gates.lock(ctx, id)
	if err != nil {
		return "", err
	}
	defer unlock()
	if owner, err := manager.options.Repository.GetThread(ctx, thread); err == nil && owner.ProjectID != project {
		return "", domainerr.NotFound(thread)
	}
	if reason == "idle" || reason == "merged" {
		projectView, err := manager.options.Repository.GetProject(ctx, project)
		if err != nil {
			return "", err
		}
		retention := projectView.Settings.Workspace.Retention
		if retention == "keep" || reason == "idle" && retention != "auto" {
			return "busy", nil
		}
	}
	branches, err := manager.Branches(ctx, project)
	if err != nil {
		return "", err
	}
	if branches == nil || branches.Remove == nil {
		return "dirty", nil
	}
	threads, err := manager.options.Repository.ListThreads(ctx, project)
	if err != nil {
		return "", err
	}
	known := false
	for _, value := range threads {
		if value.ID == thread {
			known = true
			break
		}
	}
	if known {
		conversation, err := manager.options.Repository.GetConversation(ctx, thread)
		if err != nil {
			return "", err
		}
		if conversation.Turn != nil {
			return "busy", nil
		}
	}
	if reason == "idle" {
		for _, job := range manager.Background.List(thread) {
			if job.Result == nil {
				return "busy", nil
			}
		}
	}
	// Background commands are stopped first for explicit or landed cleanup. Other
	// active readers and commands keep their worktree until a later sweep.
	manager.noteSetupGone(thread)
	if err := manager.Background.StopThread(ctx, thread, false); err != nil {
		return "", err
	}
	if manager.workspaces.Busy(id) {
		return "busy", nil
	}
	if err := manager.workspaces.RemoveWait(ctx, id); err != nil {
		return "", err
	}
	removed, err := branches.Remove(ctx, thread, sdk.WorktreeRemoveOptions{Spare: reason == "idle"})
	if err != nil {
		return "", err
	}
	if !removed {
		return "dirty", nil
	}
	if known {
		if err := manager.options.Repository.RecordWorktreeRemoved(ctx, thread, reason); err != nil {
			return "", err
		}
	}
	return "removed", nil
}
func (manager *Manager) DiscardProject(ctx context.Context, project string) (err error) {
	ctx, done, err := manager.begin(ctx)
	if err != nil {
		return err
	}
	defer done()
	manager.mu.Lock()
	if manager.discarding[project] {
		manager.mu.Unlock()
		return errors.New("project workspaces are already being discarded")
	}
	if len(manager.discarding) >= 1024 {
		manager.mu.Unlock()
		return errors.New("too many pending project deletions")
	}
	manager.discarding[project] = true
	manager.mu.Unlock()
	manager.mentions.RemovePrefix(project + "\x00")
	defer func() {
		if err != nil {
			manager.mu.Lock()
			delete(manager.discarding, project)
			manager.mu.Unlock()
		}
	}()
	threads, err := manager.options.Repository.ListThreads(ctx, project)
	if err != nil {
		return err
	}
	var branches *sdk.BranchCapability
	for _, thread := range threads {
		err = func() error {
			unlock, err := manager.gates.lock(ctx, workspaceID(project, thread.ID))
			if err != nil {
				return err
			}
			defer unlock()
			conversation, err := manager.options.Repository.GetConversation(ctx, thread.ID)
			if err != nil {
				return err
			}
			if conversation.Turn != nil {
				return errors.New("project still has an active turn")
			}
			manager.noteSetupGone(thread.ID)
			if err := manager.Background.StopThread(ctx, thread.ID, false); err != nil {
				return err
			}
			if err := manager.workspaces.RemoveWait(ctx, workspaceID(project, thread.ID)); err != nil {
				return err
			}
			manager.notes.Remove(thread.ID)
			if thread.Workspace == nil || thread.Workspace.Removed {
				return nil
			}
			if branches == nil {
				branches, err = manager.Branches(ctx, project)
				if err != nil {
					return err
				}
			}
			if branches != nil && branches.Remove != nil {
				removed, err := branches.Remove(ctx, thread.ID, sdk.WorktreeRemoveOptions{Discard: true})
				if err != nil {
					return err
				}
				if !removed {
					return domainerr.New("error.workspace", map[string]any{"detail": thread.Workspace.Label})
				}
			}
			return nil
		}()
		if err != nil {
			return err
		}
	}
	manager.connections.Remove(project)
	manager.references.RemovePrefix(project + "\x00")
	return nil
}
