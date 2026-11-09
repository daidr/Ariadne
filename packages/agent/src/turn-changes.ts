import type { Ariadne, ChangeTotals } from '@ariadne/core'
import type { BranchCapability } from '@ariadne/plugin-sdk'
import type { WorkspaceManager } from './workspaces'

/** How long a stopped turn waits for its changes; past it they are not shown. */
export const STOPPED_CHANGES_MS = 5_000

/**
 * What one turn of a thread changes in its workspace, however it changed it (its file
 * tools, a command, a script): the workspace is written down as a git tree when the turn
 * first touches it, and again when the turn ends; a difference is recorded with the turn.
 * Workspaces that are not worktrees of a repository record nothing.
 */
export function turnChanges(workspaces: WorkspaceManager, projectId: string, threadId: string) {
  let before: Promise<string | null> | null = null
  const snapshot = async (): Promise<string | null> => {
    try {
      const branches = await workspaces.branches(projectId)
      return (await branches?.snapshot?.(threadId)) ?? null
    } catch {
      // Without a snapshot the turn just shows no changes.
      return null
    }
  }
  return {
    /** Before the turn's first look at its workspace; later calls wait for the same one. */
    begin(): Promise<void> {
      before ??= snapshot()
      return before.then(() => undefined)
    },
    /**
     * As the turn ends: what changed since `begin`, if anything; given `timeoutMs`, it
     * gives up past it and records nothing.
     */
    async record(app: Ariadne, turnId: string, timeoutMs?: number): Promise<void> {
      if (!before) return
      if (timeoutMs === undefined) return this.recordNow(app, turnId)
      let timer: ReturnType<typeof setTimeout> | undefined
      let late = false
      const timeout = new Promise<void>((resolve) => {
        timer = setTimeout(() => {
          late = true
          resolve()
        }, timeoutMs)
      })
      await Promise.race([this.recordNow(app, turnId, () => late), timeout])
      clearTimeout(timer)
    },
    async recordNow(app: Ariadne, turnId: string, late = () => false): Promise<void> {
      if (!before) return
      const from = await before
      if (!from) return
      try {
        const branches = await workspaces.branches(projectId)
        if (!branches?.diff) return
        const to = await snapshot()
        if (!to || to === from) return
        const files = await branches.diff(from, to)
        const branch = await branchTotals(app, branches, threadId, to)
        // The turn ended without waiting for it.
        if (late()) return
        app.recordTurnChanges(threadId, turnId, { from, to, files, ...(branch ? { branch } : {}) })
      } catch {
        // A diff that cannot be read leaves the turn as it is.
      }
    },
  }
}

/**
 * The workspace as a turn left it (`to`, a tree), against where the thread's branch left
 * its base: everything the thread changed so far, committed or not. `null` when it cannot
 * be told.
 */
async function branchTotals(
  app: Ariadne,
  branches: BranchCapability,
  threadId: string,
  to: string,
): Promise<ChangeTotals | null> {
  try {
    const workspace = app.getThread(threadId).workspace
    if (!workspace?.branch || !workspace.base || !branches.diff) return null
    const changes = await branches.changes({ threadId, branch: workspace.branch }, workspace.base)
    if (!changes) return null
    const files = await branches.diff(changes.from, to)
    let added = 0
    let removed = 0
    for (const file of files) {
      added += file.added ?? 0
      removed += file.removed ?? 0
    }
    return { files: files.length, added, removed }
  } catch {
    return null
  }
}
