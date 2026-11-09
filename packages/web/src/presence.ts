import { watch } from 'vue'
import { api } from './api'
import { state } from './store'

/**
 * How long the user is gone from a project before coming back counts as a return. The
 * server decides with the same number; here it only spares a request.
 */
const AWAY_MS = 10 * 60_000
/** While the user is at it, when they were last seen is written down this often. */
const SEEN_EVERY_MS = 15_000

const KEY = (projectId: string) => `ariadne.seen.${projectId}`

/** When the user last touched the project in this browser, in any tab; `null` if never. */
function lastSeen(projectId: string): number | null {
  try {
    const at = Number(localStorage.getItem(KEY(projectId)))
    return at > 0 ? at : null
  } catch {
    return null
  }
}

/**
 * Where the user was last seen and when it was written down, so a busy pointer costs
 * nothing between two writes.
 */
let seen = { projectId: '', at: 0 }

/**
 * The user is at the open project: they moved, typed, or came back to the page. After a
 * long enough gap the server is told how long they were gone, and writes the recap of
 * what happened meanwhile if anything did.
 */
export function touch(now: number): void {
  const projectId = state.currentId
  if (!projectId || document.visibilityState !== 'visible') return
  if (seen.projectId === projectId && now - seen.at < SEEN_EVERY_MS) return
  const last = lastSeen(projectId)
  seen = { projectId, at: now }
  try {
    localStorage.setItem(KEY(projectId), String(now))
  } catch {
    // Not remembered: the next visit starts counting afresh.
    return
  }
  if (last !== null && now - last >= AWAY_MS) {
    // Missing one recap is not worth an error on the user's first move back.
    void api.noteReturn(projectId, now - last).catch(() => {})
  }
}

const seenNow = () => touch(Date.now())

/** Follow the user's presence from now on. */
export function watchPresence(): void {
  for (const type of ['pointerdown', 'pointermove', 'keydown', 'wheel'] as const) {
    window.addEventListener(type, seenNow, { passive: true, capture: true })
  }
  window.addEventListener('focus', seenNow)
  document.addEventListener('visibilitychange', seenNow)
  watch(() => state.currentId, seenNow, { immediate: true })
}
