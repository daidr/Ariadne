package runner

import (
	"context"
	"sync"
	"time"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/workspaces"
	"github.com/daidr/Ariadne/sdk"
)

// stoppedChangesTimeout caps how long a cancelled or failed turn waits to
// snapshot and diff its workspace before finishing.
const stoppedChangesTimeout = 5 * time.Second

const emptyTreeSHA = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

type sourceTurnBefore struct {
	sourceID   string
	sourceName string
	primary    bool
	repos      map[string]sdk.RepoSnapshot
}

// turnChanges tracks what one turn of a thread changes across its associated
// Git repositories (primary source, secondary sources, and embedded .git repos)
// by taking snapshots before the first workspace edit and diffing them right
// before the turn finishes.
type turnChanges struct {
	mu             sync.Mutex
	threadID       string
	workspaces     *workspaces.Manager
	repository     *core.Repository
	beforeBySource map[string]sourceTurnBefore
	unavailable    map[string]bool
	recorded       bool
}

func newTurnChanges(runner *Runner, conversation core.ConversationView) *turnChanges {
	if conversation.Role != "thread" || runner.Workspaces == nil {
		return nil
	}
	return &turnChanges{
		threadID:       conversation.ID,
		workspaces:     runner.Workspaces,
		repository:     runner.Repository,
		beforeBySource: map[string]sourceTurnBefore{},
		unavailable:    map[string]bool{},
	}
}

func (tc *turnChanges) threadSources(ctx context.Context, thread core.ThreadView) ([]workspaces.ThreadSource, error) {
	sources, err := tc.workspaces.ThreadSources(ctx, thread)
	if err != nil {
		return nil, err
	}
	if len(sources) > 0 {
		return sources, nil
	}
	branches, err := tc.workspaces.BranchesForThread(ctx, thread)
	if err != nil || branches == nil {
		return nil, err
	}
	return []workspaces.ThreadSource{{
		ID:        "",
		Primary:   true,
		Workspace: thread.Workspace,
		Branches:  branches,
	}}, nil
}

func snapshotSourceRepos(ctx context.Context, branches *sdk.BranchCapability, threadID string) ([]sdk.RepoSnapshot, bool) {
	if branches == nil {
		return nil, false
	}
	if branches.RepoSnapshots != nil && branches.RepoDiff != nil {
		snaps, err := branches.RepoSnapshots(ctx, threadID)
		if err != nil {
			return nil, true
		}
		return snaps, true
	}
	if branches.Snapshot == nil || branches.Diff == nil {
		return nil, false
	}
	snap, err := branches.Snapshot(ctx, threadID)
	if err != nil || snap == nil || *snap == "" {
		return nil, true
	}
	return []sdk.RepoSnapshot{{Tree: *snap}}, true
}

func diffSourceRepo(ctx context.Context, branches *sdk.BranchCapability, threadID, repoPath, from, to string) ([]sdk.ChangedFile, error) {
	if branches == nil {
		return nil, nil
	}
	if branches.RepoDiff != nil {
		return branches.RepoDiff(ctx, threadID, repoPath, from, to)
	}
	if repoPath == "" && branches.Diff != nil {
		return branches.Diff(ctx, from, to)
	}
	return nil, nil
}

func toTurnFiles(diffFiles []sdk.ChangedFile) []core.TurnFile {
	files := make([]core.TurnFile, len(diffFiles))
	for i, file := range diffFiles {
		files[i] = core.TurnFile{
			Path:         file.Path,
			PreviousPath: file.PreviousPath,
			Status:       file.Status,
			Added:        file.Added,
			Removed:      file.Removed,
		}
	}
	return files
}

func sumTurnFileTotals(files []core.TurnFile) core.ChangeTotals {
	total := core.ChangeTotals{Files: int64(len(files))}
	for _, file := range files {
		if file.Added != nil {
			total.Added += *file.Added
		}
		if file.Removed != nil {
			total.Removed += *file.Removed
		}
	}
	return total
}

func (tc *turnChanges) begin(ctx context.Context, thread core.ThreadView) {
	if tc == nil || thread.ReviewOf != nil {
		return
	}
	tc.mu.Lock()
	defer tc.mu.Unlock()
	if current, err := tc.repository.GetThread(ctx, thread.ID); err == nil {
		thread = current
		if thread.ReviewOf != nil {
			return
		}
	}
	sources, err := tc.threadSources(ctx, thread)
	if err != nil {
		return
	}
	for _, src := range sources {
		if _, exists := tc.beforeBySource[src.ID]; exists || tc.unavailable[src.ID] {
			continue
		}
		snaps, supported := snapshotSourceRepos(ctx, src.Branches, thread.ID)
		if !supported {
			tc.unavailable[src.ID] = true
			continue
		}
		if len(snaps) == 0 {
			continue
		}
		repoMap := make(map[string]sdk.RepoSnapshot, len(snaps))
		for _, snap := range snaps {
			if snap.Tree == "" {
				continue
			}
			repoMap[snap.RepoPath] = snap
		}
		if len(repoMap) == 0 {
			continue
		}
		tc.beforeBySource[src.ID] = sourceTurnBefore{
			sourceID:   src.ID,
			sourceName: src.Name,
			primary:    src.Primary,
			repos:      repoMap,
		}
	}
}

func (tc *turnChanges) record(ctx context.Context, turnID string) {
	if tc == nil {
		return
	}
	tc.mu.Lock()
	defer tc.mu.Unlock()
	if tc.recorded || len(tc.beforeBySource) == 0 {
		return
	}
	tc.recorded = true
	recordCtx := ctx
	var cancel context.CancelFunc
	if ctx.Err() != nil {
		recordCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), stoppedChangesTimeout)
		defer cancel()
	}
	thread, err := tc.repository.GetThread(recordCtx, tc.threadID)
	if err != nil || thread.ReviewOf != nil {
		return
	}
	sources, err := tc.threadSources(recordCtx, thread)
	if err != nil || len(sources) == 0 {
		return
	}

	var (
		turnRepos        []core.TurnRepoChanges
		touchedBaselines []core.RepoBaseline
		afterBySource    = make(map[string]map[string]sdk.RepoSnapshot, len(sources))
		primaryOnly      = len(sources) == 1 && sources[0].Primary
	)

	for _, src := range sources {
		before, hasBefore := tc.beforeBySource[src.ID]
		if !hasBefore && src.Primary {
			before, hasBefore = tc.beforeBySource[""]
		}
		if !hasBefore {
			continue
		}
		afterSnaps, supported := snapshotSourceRepos(recordCtx, src.Branches, tc.threadID)
		if !supported || len(afterSnaps) == 0 {
			continue
		}
		afterMap := make(map[string]sdk.RepoSnapshot, len(afterSnaps))
		for _, snap := range afterSnaps {
			if snap.Tree == "" {
				continue
			}
			afterMap[snap.RepoPath] = snap
		}
		afterBySource[src.ID] = afterMap

		for _, afterSnap := range afterSnaps {
			if afterSnap.Tree == "" {
				continue
			}
			beforeSnap, existed := before.repos[afterSnap.RepoPath]
			if !existed {
				// Embedded repo appeared mid-turn (for example via git clone or git init).
				if afterSnap.RepoPath == "" {
					continue
				}
				fromTree := afterSnap.Head
				if fromTree == "" {
					fromTree = emptyTreeSHA
				}
				if afterSnap.Tree == fromTree {
					continue
				}
				diffFiles, err := diffSourceRepo(recordCtx, src.Branches, tc.threadID, afterSnap.RepoPath, fromTree, afterSnap.Tree)
				if err != nil || len(diffFiles) == 0 {
					continue
				}
				if afterSnap.Head != "" {
					touchedBaselines = append(touchedBaselines, core.RepoBaseline{
						SourceID:   src.ID,
						RepoPath:   afterSnap.RepoPath,
						BaseHead:   afterSnap.Head,
						BaseBranch: afterSnap.Branch,
					})
				}
				files := toTurnFiles(diffFiles)
				turnRepos = append(turnRepos, core.TurnRepoChanges{
					SourceID:   src.ID,
					SourceName: src.Name,
					RepoPath:   afterSnap.RepoPath,
					Branch:     afterSnap.Branch,
					From:       fromTree,
					To:         afterSnap.Tree,
					Files:      files,
					Total:      sumTurnFileTotals(files),
				})
				continue
			}

			headChanged := afterSnap.Head != "" && beforeSnap.Head != "" && afterSnap.Head != beforeSnap.Head
			treeChanged := afterSnap.Tree != beforeSnap.Tree
			if !headChanged && !treeChanged {
				continue
			}
			if afterSnap.RepoPath != "" && beforeSnap.Head != "" {
				touchedBaselines = append(touchedBaselines, core.RepoBaseline{
					SourceID:   src.ID,
					RepoPath:   afterSnap.RepoPath,
					BaseHead:   beforeSnap.Head,
					BaseBranch: beforeSnap.Branch,
				})
			}
			if !treeChanged {
				continue
			}
			diffFiles, err := diffSourceRepo(recordCtx, src.Branches, tc.threadID, afterSnap.RepoPath, beforeSnap.Tree, afterSnap.Tree)
			if err != nil || len(diffFiles) == 0 {
				continue
			}
			files := toTurnFiles(diffFiles)
			turnRepos = append(turnRepos, core.TurnRepoChanges{
				SourceID:   src.ID,
				SourceName: src.Name,
				RepoPath:   afterSnap.RepoPath,
				Branch:     afterSnap.Branch,
				From:       beforeSnap.Tree,
				To:         afterSnap.Tree,
				Files:      files,
				Total:      sumTurnFileTotals(files),
			})
		}
	}

	if len(touchedBaselines) > 0 {
		_ = tc.repository.RecordRepoBaselines(recordCtx, tc.threadID, touchedBaselines)
	}

	branch := tc.branchTotals(recordCtx, thread, sources, afterBySource)
	if len(turnRepos) == 0 {
		if branch != nil {
			if (thread.Changes != nil || branch.Files > 0) && !core.SameChangeTotals(thread.Changes, *branch) {
				_ = tc.repository.SyncBranchChanges(recordCtx, tc.threadID, *branch)
			}
		}
		return
	}

	if len(turnRepos) == 1 && primaryOnly && turnRepos[0].RepoPath == "" && turnRepos[0].SourceName == "" {
		only := turnRepos[0]
		_ = tc.repository.RecordTurnChanges(recordCtx, tc.threadID, turnID, only.From, only.To, only.Files, branch)
		return
	}
	_ = tc.repository.RecordTurnRepoChanges(recordCtx, tc.threadID, turnID, "", "", nil, turnRepos, branch)
}

func (tc *turnChanges) branchTotals(ctx context.Context, thread core.ThreadView, sources []workspaces.ThreadSource, afterBySource map[string]map[string]sdk.RepoSnapshot) *core.ChangeTotals {
	storedBaselines, err := tc.repository.RepoBaselines(ctx, thread.ID)
	if err != nil {
		storedBaselines = nil
	}
	baselinesBySource := map[string]map[string]sdk.RepoBaseline{}
	for _, item := range storedBaselines {
		m := baselinesBySource[item.SourceID]
		if m == nil {
			m = map[string]sdk.RepoBaseline{}
			baselinesBySource[item.SourceID] = m
		}
		m[item.RepoPath] = sdk.RepoBaseline{
			RepoPath:   item.RepoPath,
			BaseHead:   item.BaseHead,
			BaseBranch: item.BaseBranch,
		}
	}

	var totals core.ChangeTotals
	counted := false

	for _, src := range sources {
		if src.Branches == nil {
			continue
		}
		snapMap := afterBySource[src.ID]
		if src.Workspace != nil && src.Branches.Changes != nil {
			branch, _ := src.Workspace.Branch.(string)
			base, _ := src.Workspace.Base.(string)
			if branch != "" && base != "" {
				changes, err := src.Branches.Changes(ctx, sdk.ThreadBranch{ThreadID: thread.ID, Branch: branch}, base)
				if err == nil && changes != nil && changes.From != "" {
					files := changes.Files
					if len(changes.Uncommitted) > 0 {
						to := snapMap[""].Tree
						if to != "" {
							if diffFiles, err := diffSourceRepo(ctx, src.Branches, thread.ID, "", changes.From, to); err == nil {
								files = diffFiles
							}
						}
					}
					counted = true
					totals.Files += int64(len(files))
					for _, file := range files {
						if file.Added != nil {
							totals.Added += *file.Added
						}
						if file.Removed != nil {
							totals.Removed += *file.Removed
						}
					}
				}
			}
		}
		if src.Branches.EmbeddedChanges != nil && (src.Workspace == nil || !src.Workspace.Removed) {
			embedded, err := src.Branches.EmbeddedChanges(ctx, thread.ID, baselinesBySource[src.ID])
			if err != nil {
				continue
			}
			for _, item := range embedded {
				if item.Changes == nil || item.Changes.From == "" {
					continue
				}
				files := item.Changes.Files
				if len(item.Changes.Uncommitted) > 0 {
					to := snapMap[item.RepoPath].Tree
					if to != "" {
						if diffFiles, err := diffSourceRepo(ctx, src.Branches, thread.ID, item.RepoPath, item.Changes.From, to); err == nil {
							files = diffFiles
						}
					}
				}
				counted = true
				totals.Files += int64(len(files))
				for _, file := range files {
					if file.Added != nil {
						totals.Added += *file.Added
					}
					if file.Removed != nil {
						totals.Removed += *file.Removed
					}
				}
			}
		}
	}

	if !counted {
		return nil
	}
	return &totals
}
