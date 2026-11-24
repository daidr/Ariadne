import { reactive } from 'vue'
import { sendLineComments, state } from '../store'
import { draftsOf, sendable } from './line-comments'

/**
 * Line comments reach the thread after a visible countdown once the user stops writing them.
 * Leaving the changes view or switching browser tabs keeps local drafts paused instead of
 * silently submitting half-written comments.
 */
export const QUIET_MS = 10_000

const timers = new Map<string, ReturnType<typeof setInterval>>()
const deadlines = new Map<string, number>()
const sending = reactive<Record<string, boolean>>({})
const paused = reactive<Record<string, boolean>>({})
const remaining = reactive<Record<string, number>>({})
/**
 * The project of each thread with drafts, taken while its changes are open: the user may
 * have moved to another project by the time they go.
 */
const projects = new Map<string, string>()

function stopTimer(threadId: string) {
  const timer = timers.get(threadId)
  if (timer) clearInterval(timer)
  timers.delete(threadId)
  deadlines.delete(threadId)
  delete remaining[threadId]
}

function startCountdown(threadId: string) {
  stopTimer(threadId)
  if (!draftsOf(threadId).length || paused[threadId] || sending[threadId]) return
  if (typeof document !== 'undefined' && document.visibilityState === 'hidden') return
  const deadline = Date.now() + QUIET_MS
  deadlines.set(threadId, deadline)
  remaining[threadId] = Math.ceil(QUIET_MS / 1000)
  timers.set(
    threadId,
    setInterval(() => {
      if (typeof document !== 'undefined' && document.visibilityState === 'hidden') {
        stopTimer(threadId)
        return
      }
      const leftMs = (deadlines.get(threadId) ?? 0) - Date.now()
      if (leftMs <= 0) {
        void flushComments(threadId)
        return
      }
      remaining[threadId] = Math.ceil(leftMs / 1000)
    }, 250),
  )
}

/** The thread's drafts changed: restart the visible countdown unless paused. */
export function draftsChanged(threadId: string, projectId = state.currentId) {
  stopTimer(threadId)
  if (!draftsOf(threadId).length) {
    delete paused[threadId]
    projects.delete(threadId)
    return
  }
  if (projectId && !projects.has(threadId)) projects.set(threadId, projectId)
  if (!paused[threadId]) startCountdown(threadId)
}

/** Pause automatic sending for a thread while keeping its local drafts intact. */
export function pauseComments(threadId: string) {
  stopTimer(threadId)
  if (draftsOf(threadId).length) paused[threadId] = true
}

/** Resume automatic sending for a thread's pending drafts. */
export function resumeComments(threadId: string) {
  delete paused[threadId]
  if (draftsOf(threadId).length) startCountdown(threadId)
}

/** Stop the timer when leaving the changes view without silently sending unfinished drafts. */
export function deferComments(threadId: string) {
  stopTimer(threadId)
  if (draftsOf(threadId).length) paused[threadId] = true
}

/** Send the thread's drafts now; `false` when there were none or they did not go. */
export async function flushComments(threadId: string): Promise<boolean> {
  stopTimer(threadId)
  const drafts = draftsOf(threadId)
  const projectId = projects.get(threadId) ?? state.currentId
  if (!drafts.length || !projectId || sending[threadId]) return false
  sending[threadId] = true
  let sent = false
  try {
    sent = await sendLineComments(
      projectId,
      threadId,
      sendable(drafts),
      drafts.map((draft) => draft.id),
    )
    return sent
  } finally {
    delete sending[threadId]
    if (!draftsOf(threadId).length) {
      projects.delete(threadId)
      delete paused[threadId]
    } else if (!sent) {
      paused[threadId] = true
    } else if (!paused[threadId]) {
      startCountdown(threadId)
    }
  }
}

/** Whether the thread has comments waiting to go. */
export const waiting = (threadId: string) => draftsOf(threadId).length > 0

/** Remaining seconds until auto-send, or `null` when paused or idle. */
export const remainingSeconds = (threadId: string): number | null => remaining[threadId] ?? null

/** Whether auto-send is paused for this thread. */
export const isPaused = (threadId: string): boolean => !!paused[threadId]

/** Whether comments for this thread are currently being sent. */
export const isSending = (threadId: string): boolean => !!sending[threadId]

// Switching to another tab pauses active countdowns instead of silently submitting unfinished drafts.
if (typeof document !== 'undefined') {
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'hidden') {
      for (const threadId of Array.from(timers.keys())) stopTimer(threadId)
    } else {
      for (const threadId of Object.keys(state.lineComments)) {
        if (draftsOf(threadId).length && !paused[threadId] && !sending[threadId]) {
          startCountdown(threadId)
        }
      }
    }
  })
}
