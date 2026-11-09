import { DomainError, type Ariadne, type MergeMethod, type ThreadView } from '@ariadne/core'
import type { BranchChanges, FileVersions, MergeOutcome } from '@ariadne/plugin-sdk'
import { MergeRefused } from '@ariadne/plugin-sdk'
import type { WorkspaceManager } from './workspaces'

function workError(error: unknown): DomainError {
  if (error instanceof DomainError) return error
  if (error instanceof MergeRefused) {
    return new DomainError(`error.merge.${error.reason}`, { detail: error.detail })
  }
  return new DomainError('error.merge.failed', {
    detail: error instanceof Error ? error.message : String(error),
  })
}

function branchOf(thread: ThreadView): { branch: string; base: string } {
  const branch = thread.workspace?.branch
  const base = thread.workspace?.base
  if (!branch || !base) throw new DomainError('error.noBranch')
  return { branch, base }
}

/** What the thread's branch changed against its base; `null` before it has a branch. */
export async function threadChanges(
  app: Ariadne,
  workspaces: WorkspaceManager,
  threadId: string,
): Promise<{ changes: BranchChanges | null; mergeable: boolean }> {
  const thread = app.getThread(threadId)
  const branches = await workspaces.branches(thread.projectId)
  if (!branches || !thread.workspace?.branch || !thread.workspace.base) {
    return { changes: null, mergeable: false }
  }
  try {
    return {
      changes: await branches.changes(
        { threadId, branch: thread.workspace.branch },
        thread.workspace.base,
      ),
      mergeable: !!branches.merge,
    }
  } catch (error) {
    throw workError(error)
  }
}

/** One changed file before and after, between two commits of the thread's branch. */
export async function threadFile(
  app: Ariadne,
  workspaces: WorkspaceManager,
  threadId: string,
  input: { from: string; head: string; path: string; previousPath: string | null },
): Promise<FileVersions> {
  const thread = app.getThread(threadId)
  const branches = await workspaces.branches(thread.projectId)
  if (!branches) throw new DomainError('error.noBranch')
  try {
    return await branches.file(input)
  } catch (error) {
    throw workError(error)
  }
}

/**
 * The user merges the thread's branch into its base. A conflict merges nothing and hands
 * the branch back to the thread to resolve; a merge tells the coordinator.
 */
export async function mergeThread(
  app: Ariadne,
  workspaces: WorkspaceManager,
  threadId: string,
  input: { method: MergeMethod; message: string },
): Promise<{ outcome: MergeOutcome; thread: ThreadView }> {
  const thread = app.getThread(threadId)
  const { branch, base } = branchOf(thread)
  // What is merged is what was committed; a thread at work may be halfway through.
  if (app.getConversation(threadId).turn) throw new DomainError('error.merge.working')
  const branches = await workspaces.branches(thread.projectId)
  if (!branches?.merge) throw new DomainError('error.merge.unsupported')
  const message = input.message.trim() || thread.title
  let outcome: MergeOutcome
  try {
    outcome = await branches.merge({ threadId, branch, base, method: input.method, message })
  } catch (error) {
    throw workError(error)
  }
  if (outcome.kind === 'merged') {
    const { sha, head } = outcome
    return {
      outcome,
      thread: app.transaction(() => {
        app.rememberMergeMethod(thread.projectId, input.method)
        return app.recordMerge(threadId, { branch, base, method: input.method, sha, head })
      }),
    }
  }
  if (outcome.kind === 'conflict') {
    return {
      outcome,
      thread: app.recordMergeConflict(threadId, { branch, base, files: outcome.files }),
    }
  }
  return { outcome, thread }
}

/**
 * The user takes the thread's last merge back. The base returns to where it was, the
 * thread's changes wait to be merged again, and the coordinator hears of it.
 */
export async function undoMerge(
  app: Ariadne,
  workspaces: WorkspaceManager,
  threadId: string,
): Promise<ThreadView> {
  const thread = app.getThread(threadId)
  if (!thread.merged) throw new DomainError('error.undoMerge.none')
  const branches = await workspaces.branches(thread.projectId)
  if (!branches?.undoMerge) throw new DomainError('error.merge.unsupported')
  try {
    const { branch, base, sha } = thread.merged
    await branches.undoMerge({ branch, base, sha })
  } catch (error) {
    throw workError(error)
  }
  const undone = app.recordMergeUndone(threadId)
  if (!undone.workspace?.removed) return undone
  // Its worktree went with the merge; the changes are there to look at and merge again.
  await workspaces.forThread(thread.projectId, threadId).catch(() => {})
  return app.getThread(threadId)
}

/** The worktrees of every project's threads, with the space each takes. */
export async function listWorktrees(app: Ariadne, workspaces: WorkspaceManager) {
  const projects = []
  for (const project of app.listProjects()) {
    if (!workspaces.available(project.id)) continue
    try {
      const branches = await workspaces.branches(project.id)
      if (!branches) continue
      const worktrees = []
      for (const worktree of await branches.worktrees()) {
        const thread = threadOrNull(app, worktree.threadId)
        // Projects on the same repository share the directory; each lists its own.
        if (thread && thread.projectId !== project.id) continue
        worktrees.push({ ...worktree, thread })
      }
      // Only said when there is something it applies to.
      const shared = worktrees.length ? ((await branches.sharesStorage?.()) ?? null) : null
      projects.push({ id: project.id, name: project.name, worktrees, shared, error: null })
    } catch (error) {
      const failure =
        error instanceof DomainError
          ? error
          : new DomainError('error.workspace', {
              detail: error instanceof Error ? error.message : String(error),
            })
      projects.push({
        id: project.id,
        name: project.name,
        worktrees: [],
        shared: null,
        error: failure.toLocalized(),
      })
    }
  }
  return projects
}

function threadOrNull(app: Ariadne, threadId: string): ThreadView | null {
  try {
    return app.getThread(threadId)
  } catch {
    return null
  }
}
