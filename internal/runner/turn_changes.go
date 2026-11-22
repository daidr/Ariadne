package runner

import (
	"context"
	"time"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/workspaces"
	"github.com/daidr/Ariadne/sdk"
)

// stoppedChangesTimeout caps how long a cancelled or failed turn waits to
// snapshot and diff its workspace before finishing.
const stoppedChangesTimeout = 5 * time.Second

// turnChanges tracks what one turn of a thread changes in its workspace by
// taking a git tree snapshot before the first workspace edit and diffing it
// against a second snapshot right before the turn finishes.
type turnChanges struct {
	threadID    string
	workspaces  *workspaces.Manager
	repository  *core.Repository
	before      string
	unavailable bool
	recorded    bool
}

func newTurnChanges(runner *Runner, conversation core.ConversationView) *turnChanges {
	if conversation.Role != "thread" || runner.Workspaces == nil {
		return nil
	}
	return &turnChanges{
		threadID:   conversation.ID,
		workspaces: runner.Workspaces,
		repository: runner.Repository,
	}
}

func (tc *turnChanges) begin(ctx context.Context, thread core.ThreadView) {
	if tc == nil || tc.before != "" || tc.unavailable || thread.ReviewOf != nil {
		return
	}
	branches, err := tc.workspaces.BranchesForThread(ctx, thread)
	if err != nil {
		return
	}
	if branches == nil || branches.Snapshot == nil || branches.Diff == nil {
		tc.unavailable = true
		return
	}
	snapshot, err := branches.Snapshot(ctx, thread.ID)
	if err != nil || snapshot == nil || *snapshot == "" {
		return
	}
	tc.before = *snapshot
}

func (tc *turnChanges) record(ctx context.Context, turnID string) {
	if tc == nil || tc.recorded || tc.before == "" {
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
	branches, err := tc.workspaces.BranchesForThread(recordCtx, thread)
	if err != nil || branches == nil || branches.Snapshot == nil || branches.Diff == nil {
		return
	}
	after, err := branches.Snapshot(recordCtx, tc.threadID)
	if err != nil || after == nil || *after == "" {
		return
	}
	if *after == tc.before {
		if branch := tc.branchTotals(recordCtx, branches, thread, *after); branch != nil {
			_ = tc.repository.RecordBranchChanges(recordCtx, tc.threadID, branch)
		}
		return
	}
	diffFiles, err := branches.Diff(recordCtx, tc.before, *after)
	if err != nil || len(diffFiles) == 0 {
		if branch := tc.branchTotals(recordCtx, branches, thread, *after); branch != nil {
			_ = tc.repository.RecordBranchChanges(recordCtx, tc.threadID, branch)
		}
		return
	}
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
	branch := tc.branchTotals(recordCtx, branches, thread, *after)
	_ = tc.repository.RecordTurnChanges(recordCtx, tc.threadID, turnID, tc.before, *after, files, branch)
}

func (tc *turnChanges) branchTotals(ctx context.Context, branches *sdk.BranchCapability, thread core.ThreadView, to string) *core.ChangeTotals {
	if thread.Workspace == nil || branches.Changes == nil || branches.Diff == nil {
		return nil
	}
	branch, _ := thread.Workspace.Branch.(string)
	base, _ := thread.Workspace.Base.(string)
	if branch == "" || base == "" {
		return nil
	}
	changes, err := branches.Changes(ctx, sdk.ThreadBranch{ThreadID: thread.ID, Branch: branch}, base)
	if err != nil || changes == nil || changes.From == "" {
		return nil
	}
	files, err := branches.Diff(ctx, changes.From, to)
	if err != nil {
		return nil
	}
	totals := &core.ChangeTotals{Files: int64(len(files))}
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
