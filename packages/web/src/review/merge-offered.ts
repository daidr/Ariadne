import type { ThreadView } from '@ariadne/core'

/**
 * Whether a thread's review ends in merging here: changes on a branch Ariadne merges
 * itself, not through a pull request. Looking through them is then "go merge".
 */
export function mergeOffered(thread: ThreadView | null | undefined): boolean {
  const workspace = thread?.workspace
  return !!workspace?.branch && !!workspace.base && !workspace.removed && !thread?.pullRequest
}

/**
 * The thread's changes on its branch while they can still be opened: none once it has no
 * branch or its workspace is gone (dropped, or removed after merging).
 */
export function branchChanges(thread: ThreadView | null | undefined): ThreadView['changes'] {
  const workspace = thread?.workspace
  if (!workspace?.branch || !workspace.base || workspace.removed) return null
  return thread?.changes ?? null
}
