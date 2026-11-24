package workspaces

import (
	"context"
	"errors"
	"path/filepath"
	"sync"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/fsutil"
	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/sdk"
)

func workError(err error) error {
	if err == nil {
		return nil
	}
	var domain *domainerr.Error
	if errors.As(err, &domain) {
		return err
	}
	var refused *sdk.MergeRefused
	if errors.As(err, &refused) {
		return domainerr.New("error.merge."+refused.Reason, map[string]any{"detail": refused.Detail})
	}
	return domainerr.New("error.merge.failed", map[string]any{"detail": err.Error()})
}

type ThreadChanges struct {
	Changes   *sdk.BranchChanges `json:"changes"`
	Repos     []sdk.RepoChanges  `json:"repos,omitempty"`
	Mergeable bool               `json:"mergeable"`
	Unchanged bool               `json:"unchanged,omitempty"`
}

func (manager *Manager) ThreadChanges(ctx context.Context, threadID string) (ThreadChanges, error) {
	return manager.ThreadChangesSince(ctx, threadID, nil)
}

func hasRepoActivity(changes *sdk.BranchChanges) bool {
	if changes == nil {
		return false
	}
	return len(changes.Files) > 0 || len(changes.Commits) > 0 || len(changes.Uncommitted) > 0
}

func baselinesBySource(items []core.RepoBaseline) map[string]map[string]sdk.RepoBaseline {
	if len(items) == 0 {
		return nil
	}
	grouped := make(map[string]map[string]sdk.RepoBaseline)
	for _, item := range items {
		m := grouped[item.SourceID]
		if m == nil {
			m = make(map[string]sdk.RepoBaseline)
			grouped[item.SourceID] = m
		}
		m[item.RepoPath] = sdk.RepoBaseline{
			RepoPath:   item.RepoPath,
			BaseHead:   item.BaseHead,
			BaseBranch: item.BaseBranch,
		}
	}
	return grouped
}

func (manager *Manager) ThreadChangesSince(ctx context.Context, threadID string, known *sdk.BranchRevisions) (ThreadChanges, error) {
	ctx, done, err := manager.begin(ctx)
	if err != nil {
		return ThreadChanges{}, err
	}
	defer done()
	thread, err := manager.options.Repository.GetThread(ctx, threadID)
	if err != nil {
		return ThreadChanges{}, err
	}
	sources, err := manager.ThreadSources(ctx, thread)
	if err != nil {
		return ThreadChanges{}, err
	}
	if len(sources) == 0 {
		return ThreadChanges{}, nil
	}
	storedBaselines, err := manager.options.Repository.RepoBaselines(ctx, threadID)
	if err != nil {
		return ThreadChanges{}, err
	}
	bySource := baselinesBySource(storedBaselines)

	// Fast-path revision check only when there is a single primary source and no
	// recorded embedded baselines; even then, check embedded repos while the
	// worktree is present so newly modified or cloned embedded repos are not missed.
	var (
		prefetchedPrimaryEmbedded    []sdk.RepoChanges
		hasPrefetchedPrimaryEmbedded bool
	)
	if known != nil && len(sources) == 1 && sources[0].Primary && len(storedBaselines) == 0 {
		primary := sources[0]
		if primary.Branches != nil && primary.Branches.Revisions != nil && primary.Workspace != nil {
			branch, base := optionalString(primary.Workspace.Branch), optionalString(primary.Workspace.Base)
			if branch != nil && base != nil && *branch != "" && *base != "" {
				revisions, err := primary.Branches.Revisions(ctx, sdk.ThreadBranch{ThreadID: threadID, Branch: *branch}, *base)
				if err != nil {
					return ThreadChanges{}, workError(err)
				}
				if revisions == *known {
					if primary.Branches.EmbeddedChanges == nil || primary.Workspace.Removed {
						return ThreadChanges{Mergeable: primary.Mergeable, Unchanged: true}, nil
					}
					embedded, err := primary.Branches.EmbeddedChanges(ctx, threadID, bySource[primary.ID])
					if err != nil {
						return ThreadChanges{}, workError(err)
					}
					hasEmbeddedActivity := false
					for _, item := range embedded {
						if hasRepoActivity(item.Changes) {
							hasEmbeddedActivity = true
							break
						}
					}
					if !hasEmbeddedActivity {
						return ThreadChanges{Mergeable: primary.Mergeable, Unchanged: true}, nil
					}
					prefetchedPrimaryEmbedded = embedded
					hasPrefetchedPrimaryEmbedded = true
				}
			}
		}
	}

	var (
		primaryRoot      *sdk.RepoChanges
		firstRoot        *sdk.RepoChanges
		activeRepos      []sdk.RepoChanges
		sourceBranches   = make(map[string]*sdk.BranchCapability, len(sources))
		primaryMergeable bool
	)
	for _, src := range sources {
		if src.Branches == nil {
			continue
		}
		sourceBranches[src.ID] = src.Branches
		if src.Primary {
			primaryMergeable = src.Mergeable
		}
		if src.Branches.Changes != nil && src.Workspace != nil {
			branch, base := optionalString(src.Workspace.Branch), optionalString(src.Workspace.Base)
			if branch != nil && base != nil && *branch != "" && *base != "" {
				changes, err := src.Branches.Changes(ctx, sdk.ThreadBranch{ThreadID: threadID, Branch: *branch}, *base)
				if err != nil {
					if src.Primary {
						return ThreadChanges{}, workError(err)
					}
					changes = nil
				}
				if changes != nil {
					entry := sdk.RepoChanges{
						SourceID:   src.ID,
						SourceName: src.Name,
						RepoPath:   "",
						Embedded:   false,
						Mergeable:  src.Mergeable,
						Changes:    changes,
					}
					if src.Primary {
						entryCopy := entry
						primaryRoot = &entryCopy
					}
					if firstRoot == nil {
						entryCopy := entry
						firstRoot = &entryCopy
					}
					if hasRepoActivity(changes) {
						activeRepos = append(activeRepos, entry)
					}
				}
			}
		}
		if src.Branches.EmbeddedChanges != nil && (src.Workspace == nil || !src.Workspace.Removed) {
			var (
				embedded []sdk.RepoChanges
				err      error
			)
			if src.Primary && hasPrefetchedPrimaryEmbedded {
				embedded = prefetchedPrimaryEmbedded
			} else {
				embedded, err = src.Branches.EmbeddedChanges(ctx, threadID, bySource[src.ID])
			}
			if err != nil {
				if src.Primary {
					return ThreadChanges{}, workError(err)
				}
				continue
			}
			for _, item := range embedded {
				if !hasRepoActivity(item.Changes) {
					continue
				}
				item.SourceID = src.ID
				item.SourceName = src.Name
				item.Embedded = true
				item.Mergeable = false
				activeRepos = append(activeRepos, item)
			}
		}
	}

	if len(activeRepos) == 0 {
		if primaryRoot != nil {
			manager.syncMultiRepoChanges(ctx, thread, sourceBranches, []sdk.RepoChanges{*primaryRoot})
			return ThreadChanges{Changes: primaryRoot.Changes, Mergeable: primaryRoot.Mergeable}, nil
		}
		if firstRoot != nil {
			manager.syncMultiRepoChanges(ctx, thread, sourceBranches, []sdk.RepoChanges{*firstRoot})
			return ThreadChanges{Changes: firstRoot.Changes, Repos: []sdk.RepoChanges{*firstRoot}, Mergeable: false}, nil
		}
		return ThreadChanges{}, nil
	}

	manager.syncMultiRepoChanges(ctx, thread, sourceBranches, activeRepos)
	if len(activeRepos) == 1 {
		only := activeRepos[0]
		return ThreadChanges{
			Changes:   only.Changes,
			Repos:     activeRepos,
			Mergeable: only.Mergeable,
		}, nil
	}
	topChanges := activeRepos[0].Changes
	topMergeable := false
	if primaryRoot != nil && primaryRoot.Changes != nil {
		topChanges = primaryRoot.Changes
		topMergeable = primaryMergeable && len(primaryRoot.Changes.Files) > 0
	}
	return ThreadChanges{
		Changes:   topChanges,
		Repos:     activeRepos,
		Mergeable: topMergeable,
	}, nil
}

func summarizeChangedFiles(files []sdk.ChangedFile) core.ChangeTotals {
	totals := core.ChangeTotals{Files: int64(len(files))}
	for _, file := range files {
		if file.Added != nil {
			totals.Added += *file.Added
		}
		if file.Removed != nil {
			totals.Removed += *file.Removed
		}
	}
	return totals
}

func (manager *Manager) syncBranchChanges(ctx context.Context, thread core.ThreadView, branches *sdk.BranchCapability, changes *sdk.BranchChanges) {
	if changes == nil {
		return
	}
	sourceBranches := map[string]*sdk.BranchCapability{"": branches}
	manager.syncMultiRepoChanges(ctx, thread, sourceBranches, []sdk.RepoChanges{{Changes: changes}})
}

func (manager *Manager) syncMultiRepoChanges(ctx context.Context, thread core.ThreadView, sourceBranches map[string]*sdk.BranchCapability, repos []sdk.RepoChanges) {
	if thread.ReviewOf != nil || len(repos) == 0 {
		return
	}
	anyUncommitted := false
	for _, repo := range repos {
		if repo.Changes != nil && len(repo.Changes.Uncommitted) > 0 {
			anyUncommitted = true
			break
		}
	}
	var totals *core.ChangeTotals
	if !anyUncommitted {
		var sum core.ChangeTotals
		for _, repo := range repos {
			if repo.Changes == nil {
				continue
			}
			part := summarizeChangedFiles(repo.Changes.Files)
			sum.Files += part.Files
			sum.Added += part.Added
			sum.Removed += part.Removed
		}
		totals = &sum
	} else if thread.Changes == nil {
		var sum core.ChangeTotals
		counted := false
		snapshotsBySource := map[string]map[string]sdk.RepoSnapshot{}
		for _, repo := range repos {
			if repo.Changes == nil {
				continue
			}
			files := repo.Changes.Files
			if len(repo.Changes.Uncommitted) > 0 && repo.Changes.From != "" {
				branches := sourceBranches[repo.SourceID]
				if branches != nil {
					if branches.RepoSnapshots != nil && branches.RepoDiff != nil {
						snapMap, loaded := snapshotsBySource[repo.SourceID]
						if !loaded {
							snapMap = map[string]sdk.RepoSnapshot{}
							if snaps, err := branches.RepoSnapshots(ctx, thread.ID); err == nil {
								for _, s := range snaps {
									snapMap[s.RepoPath] = s
								}
							}
							snapshotsBySource[repo.SourceID] = snapMap
						}
						if snap, ok := snapMap[repo.RepoPath]; ok && snap.Tree != "" {
							if diffFiles, err := branches.RepoDiff(ctx, thread.ID, repo.RepoPath, repo.Changes.From, snap.Tree); err == nil {
								files = diffFiles
								counted = true
							}
						}
					} else if repo.RepoPath == "" && branches.Snapshot != nil && branches.Diff != nil {
						if snapshot, err := branches.Snapshot(ctx, thread.ID); err == nil && snapshot != nil && *snapshot != "" {
							if diffFiles, err := branches.Diff(ctx, repo.Changes.From, *snapshot); err == nil {
								files = diffFiles
								counted = true
							}
						}
					}
				}
			} else if len(files) > 0 {
				counted = true
			}
			part := summarizeChangedFiles(files)
			sum.Files += part.Files
			sum.Added += part.Added
			sum.Removed += part.Removed
		}
		if counted || sum.Files > 0 {
			totals = &sum
		}
	}
	if totals != nil && (thread.Changes != nil || totals.Files > 0) && !core.SameChangeTotals(thread.Changes, *totals) {
		_ = manager.options.Repository.SyncBranchChanges(ctx, thread.ID, *totals)
	}
}

func (manager *Manager) refreshBranchChanges(ctx context.Context, thread core.ThreadView) {
	if thread.ReviewOf != nil {
		return
	}
	_, _ = manager.ThreadChangesSince(ctx, thread.ID, nil)
}

func (manager *Manager) ThreadFile(ctx context.Context, threadID string, request sdk.FileVersionRequest) (sdk.FileVersions, error) {
	ctx, done, err := manager.begin(ctx)
	if err != nil {
		return sdk.FileVersions{}, err
	}
	defer done()
	thread, err := manager.options.Repository.GetThread(ctx, threadID)
	if err != nil {
		return sdk.FileVersions{}, err
	}
	branches, err := manager.BranchesForThreadSource(ctx, thread, request.SourceID)
	if err != nil {
		return sdk.FileVersions{}, err
	}
	if branches == nil || branches.File == nil {
		return sdk.FileVersions{}, domainerr.New("error.noBranch", nil)
	}
	request.ThreadID = threadID
	file, err := branches.File(ctx, request)
	return file, workError(err)
}

func (manager *Manager) ThreadFiles(ctx context.Context, threadID string, requests []sdk.FileVersionRequest) ([]sdk.FileVersions, error) {
	if len(requests) == 0 {
		return []sdk.FileVersions{}, nil
	}
	ctx, done, err := manager.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	thread, err := manager.options.Repository.GetThread(ctx, threadID)
	if err != nil {
		return nil, err
	}
	sourceID := requests[0].SourceID
	branches, err := manager.BranchesForThreadSource(ctx, thread, sourceID)
	if err != nil {
		return nil, err
	}
	if branches == nil || (branches.Files == nil && branches.File == nil) {
		return nil, domainerr.New("error.noBranch", nil)
	}
	stamped := make([]sdk.FileVersionRequest, len(requests))
	for i, req := range requests {
		req.ThreadID = threadID
		stamped[i] = req
	}
	if branches.Files != nil {
		files, err := branches.Files(ctx, stamped)
		return files, workError(err)
	}
	results := make([]sdk.FileVersions, len(stamped))
	batchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sem := make(chan struct{}, min(6, len(stamped)))
	var (
		wg       sync.WaitGroup
		errMu    sync.Mutex
		firstErr error
	)
	for i, request := range stamped {
		wg.Add(1)
		go func(index int, req sdk.FileVersionRequest) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-batchCtx.Done():
				return
			}
			file, err := branches.File(batchCtx, req)
			if err != nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = err
					cancel()
				}
				errMu.Unlock()
				return
			}
			results[index] = file
		}(i, request)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, workError(firstErr)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

type ThreadMerge struct {
	Outcome sdk.MergeOutcome `json:"outcome"`
	Thread  core.ThreadView  `json:"thread"`
}

func (manager *Manager) MergeThread(ctx context.Context, threadID, method, message, expectedHead string) (ThreadMerge, error) {
	return manager.mergeThread(ctx, threadID, method, message, expectedHead, false)
}

func (manager *Manager) MergeReviewedThread(ctx context.Context, threadID, head string) (ThreadMerge, error) {
	if head == "" {
		return ThreadMerge{}, domainerr.New("error.noBranch", nil)
	}
	return manager.mergeThread(ctx, threadID, "", "", head, true)
}

func (manager *Manager) mergeThread(ctx context.Context, threadID, method, message, expectedHead string, auto bool) (ThreadMerge, error) {
	ctx, done, err := manager.begin(ctx)
	if err != nil {
		return ThreadMerge{}, err
	}
	defer done()
	if !auto && method != "merge" && method != "squash" {
		return ThreadMerge{}, domainerr.New("error.invalidBody", map[string]any{"reason": "method"})
	}
	repo := manager.options.Repository
	thread, err := repo.GetThread(ctx, threadID)
	if err != nil {
		return ThreadMerge{}, err
	}
	unlock, err := manager.gates.lock(ctx, workspaceID(thread.ProjectID, threadID))
	if err != nil {
		return ThreadMerge{}, err
	}
	defer unlock()
	thread, err = repo.GetThread(ctx, threadID)
	if err != nil {
		return ThreadMerge{}, err
	}
	project, err := repo.GetProject(ctx, thread.ProjectID)
	if err != nil {
		return ThreadMerge{}, err
	}
	if project.Settings.Delivery.Mode == "external" {
		return ThreadMerge{}, domainerr.New("error.externalDelivery", nil)
	}
	if thread.Workspace == nil {
		return ThreadMerge{}, domainerr.New("error.noBranch", nil)
	}
	branch, base := optionalString(thread.Workspace.Branch), optionalString(thread.Workspace.Base)
	if branch == nil || base == nil || *branch == "" || *base == "" {
		return ThreadMerge{}, domainerr.New("error.noBranch", nil)
	}
	conversation, err := repo.GetConversation(ctx, threadID)
	if err != nil {
		return ThreadMerge{}, err
	}
	if conversation.Turn != nil {
		return ThreadMerge{}, domainerr.New("error.merge.working", nil)
	}
	branches, err := manager.BranchesForThread(ctx, thread)
	if err != nil {
		return ThreadMerge{}, err
	}
	if branches == nil || branches.Merge == nil {
		return ThreadMerge{}, domainerr.New("error.merge.unsupported", nil)
	}
	project, err = repo.GetProject(ctx, thread.ProjectID)
	if err != nil {
		return ThreadMerge{}, err
	}
	if project.Settings.Delivery.Mode == "external" {
		return ThreadMerge{}, domainerr.New("error.externalDelivery", nil)
	}
	if auto {
		round, err := repo.PassedReview(ctx, threadID)
		if err != nil {
			return ThreadMerge{}, err
		}
		conversation, err := repo.GetConversation(ctx, threadID)
		if err != nil {
			return ThreadMerge{}, err
		}
		if !project.Settings.Autopilot.Enabled || project.AutopilotPaused || round["head"] != expectedHead || conversation.Turn != nil {
			return ThreadMerge{Thread: thread}, nil
		}
		method = project.Settings.Autopilot.Method
	}
	message = textutil.Trim(message)
	if message == "" {
		message = thread.Title
	}
	outcome, err := branches.Merge(ctx, sdk.MergeRequest{ThreadID: threadID, Branch: *branch, Base: *base, Method: method, Message: message, ExpectedHead: expectedHead})
	if err != nil {
		return ThreadMerge{}, workError(err)
	}
	switch outcome.Kind {
	case "merged":
		data := map[string]any{"branch": *branch, "base": *base, "method": method, "sha": outcome.SHA, "head": outcome.Head}
		if auto {
			data["auto"] = true
			thread, err = repo.RecordMerge(ctx, threadID, data)
		} else {
			thread, err = repo.RecordUserMerge(ctx, threadID, data, method)
		}
	case "conflict":
		thread, err = repo.RecordMergeConflict(ctx, threadID, map[string]any{"branch": *branch, "base": *base, "files": outcome.Files})
	}
	return ThreadMerge{Outcome: outcome, Thread: thread}, err
}
func (manager *Manager) UndoMerge(ctx context.Context, threadID string) (core.ThreadView, error) {
	ctx, done, err := manager.begin(ctx)
	if err != nil {
		return core.ThreadView{}, err
	}
	defer done()
	repo := manager.options.Repository
	thread, err := repo.GetThread(ctx, threadID)
	if err != nil {
		return thread, err
	}
	unlock, err := manager.gates.lock(ctx, workspaceID(thread.ProjectID, threadID))
	if err != nil {
		return thread, err
	}
	defer unlock()
	thread, err = repo.GetThread(ctx, threadID)
	if err != nil {
		return thread, err
	}
	merge, ok := thread.Merged.(map[string]any)
	if !ok {
		return thread, domainerr.New("error.undoMerge.none", nil)
	}
	branches, err := manager.BranchesForThread(ctx, thread)
	if err != nil {
		return thread, err
	}
	if branches == nil || branches.UndoMerge == nil {
		return thread, domainerr.New("error.merge.unsupported", nil)
	}
	branch, _ := merge["branch"].(string)
	base, _ := merge["base"].(string)
	sha, _ := merge["sha"].(string)
	if err := branches.UndoMerge(ctx, sdk.UndoMergeRequest{Branch: branch, Base: base, SHA: sha}); err != nil {
		return thread, workError(err)
	}
	thread, err = repo.RecordMergeUndone(ctx, threadID)
	if err != nil {
		return thread, err
	}
	if thread.Workspace != nil && thread.Workspace.Removed {
		// Reopening takes the same per-thread gate.
		unlock()
		manager.ForThread(ctx, thread.ProjectID, threadID)
		return repo.GetThread(ctx, threadID)
	}
	return thread, nil
}

type ListedWorktree struct {
	sdk.WorktreeInfo
	Thread   *core.ThreadView `json:"thread"`
	SourceID string           `json:"sourceId,omitempty"`
}
type ProjectWorktrees struct {
	ID        string           `json:"id"`
	Name      string           `json:"name"`
	Worktrees []ListedWorktree `json:"worktrees"`
	Shared    *bool            `json:"shared"`
	Error     *domainerr.Error `json:"error"`
}

func (manager *Manager) ListWorktrees(ctx context.Context, measure bool) ([]ProjectWorktrees, error) {
	ctx, done, err := manager.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	repo := manager.options.Repository
	projects, err := repo.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	output := []ProjectWorktrees{}
	for _, project := range projects {
		listed := ProjectWorktrees{ID: project.ID, Name: project.Name, Worktrees: []ListedWorktree{}}
		selections, err := manager.projectSourceSelections(ctx, project)
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for _, selection := range selections {
			err := func() error {
				branches, err := manager.branches(ctx, project.ID, selection)
				if err != nil || branches == nil || branches.Worktrees == nil {
					return err
				}
				worktrees, err := branches.Worktrees(ctx, measure)
				if err != nil {
					return err
				}
				threadIDs := make([]string, 0, len(worktrees))
				for _, worktree := range worktrees {
					if seen[worktree.Path] || selection.owner != "" && selection.owner != worktree.ThreadID {
						continue
					}
					if worktree.ThreadID != "" {
						threadIDs = append(threadIDs, worktree.ThreadID)
					}
				}
				threads, err := repo.ListThreadsByIDs(ctx, threadIDs)
				if err != nil {
					return err
				}
				var needSecondary []string
				for _, worktree := range worktrees {
					if seen[worktree.Path] || selection.owner != "" && selection.owner != worktree.ThreadID {
						continue
					}
					thread, ok := threads[worktree.ThreadID]
					if !ok || thread.ProjectID != project.ID {
						continue
					}
					if primary := threadSourceID(project, thread); primary != selection.id {
						needSecondary = append(needSecondary, thread.ID)
					}
				}
				secondaryByThread := map[string]*core.WorkspaceView{}
				if len(needSecondary) > 0 {
					secondaryByThread, err = repo.SecondaryWorkspaces(ctx, project.ID, selection.id, needSecondary)
					if err != nil {
						return err
					}
				}
				for _, worktree := range worktrees {
					if seen[worktree.Path] || selection.owner != "" && selection.owner != worktree.ThreadID {
						continue
					}
					var own *core.ThreadView
					var workspace *core.WorkspaceView
					sourceID := selection.id
					if thread, ok := threads[worktree.ThreadID]; ok {
						if thread.ProjectID != project.ID {
							continue
						}
						t := thread
						own = &t
						workspace = t.Workspace
						// Different configured subdirectories can share the same checkout.
						if primary := threadSourceID(project, t); primary != selection.id {
							extra := secondaryByThread[t.ID]
							if extra == nil || extra.Removed && !worktree.Orphaned {
								continue
							}
							workspace = extra
						}
					}
					if worktree.Orphaned {
						if branches.RemoveOrphaned == nil || !hasRecordedCheckout(workspace) {
							continue
						}
						if !orphanMatchesRecordedLabel(worktree.Path, workspace.Label) {
							continue
						}
					}
					if project.FileSources == nil && sourceID == core.LegacyPrimarySource {
						sourceID = ""
					}
					seen[worktree.Path] = true
					listed.Worktrees = append(listed.Worktrees, ListedWorktree{WorktreeInfo: worktree, Thread: own, SourceID: sourceID})
				}
				if measure && len(worktrees) > 0 && branches.SharesStorage != nil {
					shared, err := branches.SharesStorage(ctx)
					if err != nil {
						return err
					}
					if listed.Shared == nil || shared {
						listed.Shared = &shared
					}
				}
				return nil
			}()
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				if !errors.As(err, &listed.Error) {
					listed.Error = domainerr.New("error.workspace", map[string]any{"detail": err.Error()})
				}
			}
		}
		if len(selections) != 0 {
			output = append(output, listed)
		}
	}
	return output, nil
}
func SummarizeChecks(checks []sdk.CheckInfo) map[string]any {
	failed := []string{}
	pending := false
	for _, check := range checks {
		if check.Status != "completed" {
			pending = true
			continue
		}
		if check.Conclusion == nil {
			continue
		}
		switch *check.Conclusion {
		case "failure", "timed_out", "cancelled", "action_required", "error", "startup_failure":
			failed = append(failed, check.Name)
		}
	}
	state := "success"
	if len(checks) == 0 {
		state = "none"
	} else if len(failed) > 0 {
		state = "failure"
	} else if pending {
		state = "pending"
	}
	return map[string]any{"state": state, "failed": failed}
}
func (manager *Manager) SyncPullRequest(ctx context.Context, threadID string, known *sdk.PullRequestInfo, auto map[string]any) (map[string]any, error) {
	ctx, done, err := manager.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	thread, err := manager.options.Repository.GetThread(ctx, threadID)
	if err != nil {
		return nil, err
	}
	repo, err := manager.RepoForThread(ctx, thread)
	if err != nil {
		return nil, err
	}
	pr := known
	if pr == nil {
		if current, ok := thread.PullRequest.(map[string]any); ok {
			if repo.PullRequest == nil {
				return nil, domainerr.New("error.noRepo", nil)
			}
			number, _ := current["number"].(float64)
			value, err := repo.PullRequest(ctx, int(number))
			if err != nil {
				return nil, err
			}
			if value.Number != int(number) {
				return nil, errors.New("repository returned a different pull request")
			}
			pr = &value
		} else if thread.Workspace != nil {
			if branch := optionalString(thread.Workspace.Branch); branch != nil && repo.PullRequestFor != nil {
				pr, err = repo.PullRequestFor(ctx, *branch)
				if err != nil {
					return nil, err
				}
				if pr != nil && pr.State == "open" && repo.PullRequest != nil {
					if detail, detailErr := repo.PullRequest(ctx, pr.Number); detailErr == nil && detail.Number == pr.Number {
						pr = &detail
					}
				}
			}
		}
	}
	if pr == nil {
		return nil, nil
	}
	if repo.Checks == nil {
		return nil, domainerr.New("error.noRepo", nil)
	}
	checks, err := repo.Checks(ctx, pr.HeadSHA)
	if err != nil {
		return nil, err
	}
	value := map[string]any{"number": pr.Number, "url": pr.URL, "title": pr.Title, "state": pr.State, "headSha": pr.HeadSHA, "checks": SummarizeChecks(checks)}
	if pr.Reference != "" {
		value["reference"] = pr.Reference
	}
	if pr.State == "open" && pr.Conflicted {
		value["conflicted"] = true
	}
	previousHead := ""
	if current, ok := thread.PullRequest.(map[string]any); ok {
		previousHead, _ = current["headSha"].(string)
	}
	if err := manager.options.Repository.SyncPullRequest(ctx, threadID, value, auto); err != nil {
		return nil, err
	}
	if thread.ReviewOf == nil && thread.Workspace != nil && (thread.Changes == nil || previousHead != pr.HeadSHA) {
		manager.refreshBranchChanges(ctx, thread)
	}
	return value, nil
}

func orphanMatchesRecordedLabel(worktreePath, label string) bool {
	if subdir, err := filepath.Rel(worktreePath, label); err == nil && filepath.IsLocal(subdir) {
		return true
	}
	if !filepath.IsAbs(worktreePath) || !filepath.IsAbs(label) {
		return false
	}
	resolvedWorktree, err := fsutil.CanonicalPath(worktreePath)
	if err != nil {
		return false
	}
	resolvedLabel, err := fsutil.CanonicalPath(label)
	if err != nil {
		return false
	}
	subdir, err := filepath.Rel(resolvedWorktree, resolvedLabel)
	return err == nil && filepath.IsLocal(subdir)
}
