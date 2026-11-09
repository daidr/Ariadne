import { api } from '../api'
import { sendLineComments, state } from '../store'
import { draftsOf, sendable } from './line-comments'

/**
 * Line comments reach the thread on their own, a moment after the user stops writing
 * them, as the feedback on an artifact does: no one has to remember to send them. Leaving
 * the changes, the tab or the page sends what waits at once.
 */
export const QUIET_MS = 10_000

const timers = new Map<string, ReturnType<typeof setTimeout>>()
const sending = new Set<string>()

/** The thread's drafts changed: they go once nothing more changes for a while. */
export function draftsChanged(threadId: string) {
  const timer = timers.get(threadId)
  if (timer) clearTimeout(timer)
  timers.delete(threadId)
  if (!draftsOf(threadId).length) return
  timers.set(
    threadId,
    setTimeout(() => void flushComments(threadId), QUIET_MS),
  )
}

/** Send the thread's drafts now; `false` when there were none or they did not go. */
export async function flushComments(threadId: string): Promise<boolean> {
  const timer = timers.get(threadId)
  if (timer) clearTimeout(timer)
  timers.delete(threadId)
  const drafts = draftsOf(threadId)
  if (!drafts.length || sending.has(threadId)) return false
  sending.add(threadId)
  try {
    return await sendLineComments(
      threadId,
      sendable(drafts),
      drafts.map((draft) => draft.id),
    )
  } finally {
    sending.delete(threadId)
  }
}

/** Whether the thread has comments waiting to go. */
export const waiting = (threadId: string) => draftsOf(threadId).length > 0

function flushAll() {
  for (const threadId of Object.keys(state.lineComments)) void flushComments(threadId)
}

// Another tab or window is leaving for a while: what waits goes now.
document.addEventListener('visibilitychange', () => {
  if (document.visibilityState === 'hidden') flushAll()
})
// Closing the page would cancel a request in flight: what waits goes in one that outlives it.
window.addEventListener('pagehide', () => {
  const projectId = state.detail?.project.id
  if (!projectId) return
  for (const threadId of Object.keys(state.lineComments)) {
    const drafts = draftsOf(threadId)
    if (!drafts.length || sending.has(threadId)) continue
    api.postCommentsLeaving(projectId, threadId, sendable(drafts))
    delete state.lineComments[threadId]
  }
})
