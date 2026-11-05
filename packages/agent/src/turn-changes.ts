import type { Ariadne } from '@ariadne/core'
import type { WorkspaceManager } from './workspaces'

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
    /** As the turn ends: what changed since `begin`, if anything. */
    async record(app: Ariadne, turnId: string): Promise<void> {
      if (!before) return
      const from = await before
      if (!from) return
      try {
        const branches = await workspaces.branches(projectId)
        if (!branches?.diff) return
        const to = await snapshot()
        if (!to || to === from) return
        const files = await branches.diff(from, to)
        app.recordTurnChanges(threadId, turnId, { from, to, files })
      } catch {
        // A diff that cannot be read leaves the turn as it is.
      }
    },
  }
}
