import type { ThreadView } from '@ariadne/core'

/**
 * Whether a thread's review ends in merging here: changes on a branch Ariadne merges
 * itself, not through a pull request. Looking through them is then "go merge".
 */
export function mergeOffered(thread: ThreadView | null | undefined): boolean {
  const workspace = thread?.workspace
  return !!workspace?.branch && !!workspace.base && !workspace.removed && !thread?.pullRequest
}
