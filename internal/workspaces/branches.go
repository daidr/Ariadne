package workspaces

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/sdk"
)

func withBranches[T any](ctx context.Context, manager *Manager, project, key string, work func(context.Context, *sdk.BranchCapability) (T, error), selection ...sourceSelection) (T, error) {
	return withSource(ctx, manager, project, key, func(ctx context.Context, caps sdk.Capabilities) (T, error) {
		if caps.Workspace == nil || caps.Workspace.Branches == nil {
			var zero T
			return zero, errors.New("source has no worktree branches")
		}
		return work(ctx, caps.Workspace.Branches)
	}, selection...)
}
func (manager *Manager) Branches(ctx context.Context, project string) (*sdk.BranchCapability, error) {
	return manager.branches(ctx, project)
}

func (manager *Manager) BranchesForThread(ctx context.Context, thread core.ThreadView) (*sdk.BranchCapability, error) {
	ctx, done, err := manager.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	project, err := manager.options.Repository.GetProject(ctx, thread.ProjectID)
	if err != nil {
		return nil, err
	}
	return manager.branches(ctx, thread.ProjectID, sourceSelection{id: threadSourceID(project, thread)})
}

func (manager *Manager) branches(ctx context.Context, project string, selection ...sourceSelection) (*sdk.BranchCapability, error) {
	ctx, done, err := manager.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	if len(selection) == 0 && !manager.Available(ctx, project) {
		return nil, nil
	}
	config, err := manager.sourceConfig(ctx, project, selection...)
	if err != nil {
		return nil, err
	}
	key := config.key
	raw, err := withSource(ctx, manager, project, key, func(_ context.Context, caps sdk.Capabilities) (*sdk.BranchCapability, error) {
		if caps.Workspace == nil {
			return nil, nil
		}
		return caps.Workspace.Branches, nil
	}, selection...)
	if err != nil || raw == nil {
		return nil, err
	}
	proxy := &sdk.BranchCapability{}
	if raw.Changes != nil {
		proxy.Changes = func(ctx context.Context, branch sdk.ThreadBranch, base string) (*sdk.BranchChanges, error) {
			return withBranches(ctx, manager, project, key, func(ctx context.Context, b *sdk.BranchCapability) (*sdk.BranchChanges, error) {
				return b.Changes(ctx, branch, base)
			}, selection...)
		}
	}
	if raw.Revisions != nil {
		proxy.Revisions = func(ctx context.Context, branch sdk.ThreadBranch, base string) (sdk.BranchRevisions, error) {
			return withBranches(ctx, manager, project, key, func(ctx context.Context, b *sdk.BranchCapability) (sdk.BranchRevisions, error) {
				return b.Revisions(ctx, branch, base)
			}, selection...)
		}
	}
	if raw.File != nil {
		proxy.File = func(ctx context.Context, request sdk.FileVersionRequest) (sdk.FileVersions, error) {
			return withBranches(ctx, manager, project, key, func(ctx context.Context, b *sdk.BranchCapability) (sdk.FileVersions, error) {
				return b.File(ctx, request)
			}, selection...)
		}
	}
	if raw.Files != nil {
		proxy.Files = func(ctx context.Context, requests []sdk.FileVersionRequest) ([]sdk.FileVersions, error) {
			return withBranches(ctx, manager, project, key, func(ctx context.Context, b *sdk.BranchCapability) ([]sdk.FileVersions, error) {
				return b.Files(ctx, requests)
			}, selection...)
		}
	}
	if raw.Merged != nil {
		proxy.Merged = func(ctx context.Context, branch sdk.ThreadBranch, base string) (bool, error) {
			return withBranches(ctx, manager, project, key, func(ctx context.Context, b *sdk.BranchCapability) (bool, error) { return b.Merged(ctx, branch, base) }, selection...)
		}
	}
	if raw.Merge != nil {
		proxy.Merge = func(ctx context.Context, request sdk.MergeRequest) (sdk.MergeOutcome, error) {
			return withBranches(ctx, manager, project, key, func(ctx context.Context, b *sdk.BranchCapability) (sdk.MergeOutcome, error) {
				return b.Merge(ctx, request)
			}, selection...)
		}
	}
	if raw.UndoMerge != nil {
		proxy.UndoMerge = func(ctx context.Context, request sdk.UndoMergeRequest) error {
			_, err := withBranches(ctx, manager, project, key, func(ctx context.Context, b *sdk.BranchCapability) (bool, error) {
				return true, b.UndoMerge(ctx, request)
			}, selection...)
			return err
		}
	}
	if raw.Remove != nil {
		proxy.Remove = func(ctx context.Context, thread string, options sdk.WorktreeRemoveOptions) (bool, error) {
			return withBranches(ctx, manager, project, key, func(ctx context.Context, b *sdk.BranchCapability) (bool, error) {
				return b.Remove(ctx, thread, options)
			}, selection...)
		}
	}
	if raw.RemoveOrphaned != nil {
		proxy.RemoveOrphaned = func(ctx context.Context, thread, label string) (bool, error) {
			return withBranches(ctx, manager, project, key, func(ctx context.Context, b *sdk.BranchCapability) (bool, error) {
				return b.RemoveOrphaned(ctx, thread, label)
			}, selection...)
		}
	}
	if raw.Worktrees != nil {
		proxy.Worktrees = func(ctx context.Context, measure bool) ([]sdk.WorktreeInfo, error) {
			return withBranches(ctx, manager, project, key, func(ctx context.Context, b *sdk.BranchCapability) ([]sdk.WorktreeInfo, error) {
				return b.Worktrees(ctx, measure)
			}, selection...)
		}
	}
	if raw.SharesStorage != nil {
		proxy.SharesStorage = func(ctx context.Context) (bool, error) {
			return withBranches(ctx, manager, project, key, func(ctx context.Context, b *sdk.BranchCapability) (bool, error) { return b.SharesStorage(ctx) }, selection...)
		}
	}
	if raw.Snapshot != nil {
		proxy.Snapshot = func(ctx context.Context, thread string) (*string, error) {
			return withBranches(ctx, manager, project, key, func(ctx context.Context, b *sdk.BranchCapability) (*string, error) { return b.Snapshot(ctx, thread) }, selection...)
		}
	}
	if raw.Diff != nil {
		proxy.Diff = func(ctx context.Context, before, after string) ([]sdk.ChangedFile, error) {
			return withBranches(ctx, manager, project, key, func(ctx context.Context, b *sdk.BranchCapability) ([]sdk.ChangedFile, error) {
				return b.Diff(ctx, before, after)
			}, selection...)
		}
	}
	return proxy, nil
}

// RemoveWorktree never deletes a directory in use by an active operation. Idle
// cleanup also leaves commands the user kept running, and uncommitted files.
func (manager *Manager) RemoveWorktree(ctx context.Context, project, thread, reason string) (string, error) {
	return manager.RemoveSourceWorktree(ctx, project, thread, "", reason)
}

func (manager *Manager) RemoveSourceWorktree(ctx context.Context, project, thread, sourceID, reason string) (string, error) {
	return manager.removeSourceWorktree(ctx, project, thread, sourceID, reason, false)
}

func (manager *Manager) RemoveOrphanedWorktree(ctx context.Context, project, thread, sourceID string) (string, error) {
	return manager.removeSourceWorktree(ctx, project, thread, sourceID, "user", true)
}

func hasRecordedCheckout(workspace *core.WorkspaceView) bool {
	if workspace == nil || !filepath.IsAbs(workspace.Label) {
		return false
	}
	branch, _ := workspace.Branch.(string)
	return branch != ""
}

func (manager *Manager) removeSourceWorktree(ctx context.Context, project, thread, sourceID, reason string, orphaned bool) (string, error) {
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
	owner, threadErr := manager.options.Repository.GetThread(ctx, thread)
	var missing *domainerr.Error
	if threadErr != nil && (!errors.As(threadErr, &missing) || missing.Key != "error.notFound") {
		return "", threadErr
	}
	known := threadErr == nil
	if known && owner.ProjectID != project {
		return "", domainerr.NotFound(thread)
	}
	projectView, err := manager.options.Repository.GetProject(ctx, project)
	if err != nil {
		return "", err
	}
	primary := threadSourceID(projectView, owner)
	if sourceID == "" {
		sourceID = primary
	}
	secondary := sourceID != primary
	if secondary {
		id += "\x00" + sourceID
	}
	if reason == "idle" || reason == "merged" {
		retention := projectView.Settings.Workspace.Retention
		if retention == "keep" || reason == "idle" && retention != "auto" {
			return "busy", nil
		}
	}
	workspace := owner.Workspace
	if orphaned {
		if !known {
			return "orphan-changed", nil
		}
		if secondary {
			workspace, err = manager.options.Repository.SecondaryWorkspace(ctx, thread, sourceID)
			if err != nil {
				return "", err
			}
		}
		if !hasRecordedCheckout(workspace) {
			return "orphan-changed", nil
		}
	}
	branches, err := manager.branches(ctx, project, sourceSelection{id: sourceID, owner: thread})
	if err != nil {
		return "", err
	}
	if orphaned && (branches == nil || branches.RemoveOrphaned == nil) {
		return "orphan-changed", nil
	}
	if !orphaned && (branches == nil || branches.Remove == nil) {
		return "dirty", nil
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
	if reason == "idle" || orphaned {
		for _, job := range manager.Background.List(thread) {
			if job.Result == nil {
				return "busy", nil
			}
		}
	}
	// Background commands are stopped first for explicit or landed cleanup. Other
	// active readers and commands keep their worktree until a later sweep.
	if !secondary {
		manager.noteSetupGone(thread)
	}
	if err := manager.Background.StopThread(ctx, thread, false); err != nil {
		return "", err
	}
	if manager.workspaces.Busy(id) {
		return "busy", nil
	}
	if err := manager.workspaces.RemoveWait(ctx, id); err != nil {
		return "", err
	}
	var removed bool
	if orphaned {
		removed, err = branches.RemoveOrphaned(ctx, thread, workspace.Label)
		if err != nil {
			return "", domainerr.New("error.worktreeOrphanRemove", map[string]any{"detail": err.Error()})
		}
		if !removed {
			return "orphan-changed", nil
		}
	} else {
		removed, err = branches.Remove(ctx, thread, sdk.WorktreeRemoveOptions{Spare: reason == "idle"})
	}
	if err != nil {
		return "", err
	}
	if !removed {
		return "dirty", nil
	}
	if known {
		var err error
		if secondary {
			err = manager.options.Repository.RecordSecondaryWorktreeRemoved(ctx, thread, sourceID)
		} else {
			err = manager.options.Repository.RecordWorktreeRemoved(ctx, thread, reason)
		}
		if err != nil {
			return "", err
		}
	}
	return "removed", nil
}

func (manager *Manager) projectSourceSelections(ctx context.Context, project core.ProjectView) ([]sourceSelection, error) {
	set := project.WritableSources()
	selections := make([]sourceSelection, 0, len(set.Sources))
	for _, source := range set.Sources {
		selections = append(selections, sourceSelection{id: source.ID})
	}
	private, err := manager.options.Repository.ThreadFileSources(ctx, project.ID)
	if err != nil {
		return nil, err
	}
	for _, source := range private {
		selections = append(selections, sourceSelection{id: source.SourceID, owner: source.ThreadID})
	}
	return selections, nil
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
	projectView, err := manager.options.Repository.GetProject(ctx, project)
	if err != nil {
		return err
	}
	selections, err := manager.projectSourceSelections(ctx, projectView)
	if err != nil {
		return err
	}
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
			for _, selection := range selections {
				if selection.owner != "" && selection.owner != thread.ID {
					continue
				}
				workspace := thread.Workspace
				if selection.id != threadSourceID(projectView, thread) {
					workspace, err = manager.options.Repository.SecondaryWorkspace(ctx, thread.ID, selection.id)
					if err != nil {
						return err
					}
					if err := manager.workspaces.RemoveWait(ctx, workspaceID(project, thread.ID)+"\x00"+selection.id); err != nil {
						return err
					}
				}
				if workspace == nil || workspace.Removed {
					continue
				}
				branches, err := manager.branches(ctx, project, selection)
				if err != nil {
					return err
				}
				if branches != nil && branches.Remove != nil {
					removed, err := branches.Remove(ctx, thread.ID, sdk.WorktreeRemoveOptions{Discard: true})
					if err != nil {
						return err
					}
					if !removed {
						return domainerr.New("error.workspace", map[string]any{"detail": workspace.Label})
					}
				}
			}
			return nil
		}()
		if err != nil {
			return err
		}
	}
	manager.connections.Remove(project)
	manager.connections.RemovePrefix(project + "\x00")
	manager.references.RemovePrefix(project + "\x00")
	return nil
}
