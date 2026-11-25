import { shallowRef, watch } from 'vue'
import { api } from './api'
import * as side from './side/layout'
import { state } from './store'

/**
 * How long the user is gone from a project before coming back counts as a return. The
 * server decides with the same number; here it only spares a request.
 */
const AWAY_MS = 10 * 60_000
/** While the user is at it, when they were last seen is written down this often. */
const SEEN_EVERY_MS = 15_000
/** Foreground heartbeat interval for server-side push suppression (server TTL is 45s). */
export const PRESENCE_HEARTBEAT_MS = 20_000
/** Minimum interval between duplicate heartbeat reports with the same focus state. */
const HEARTBEAT_DEDUPE_MS = 1_500

const KEY = (projectId: string) => `ariadne.seen.${projectId}`

function createClientId(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID()
  }
  return `tab_${Date.now().toString(36)}_${Math.random().toString(36).slice(2, 10)}`
}

const CLIENT_ID = createClientId()

/** Whether this tab is currently visible and focused in the foreground. */
export function isForegroundFocused(): boolean {
  if (typeof document === 'undefined') return false
  if (document.visibilityState !== 'visible') return false
  return typeof document.hasFocus === 'function' ? document.hasFocus() : true
}

function focusedThreadId(): string | null {
  const active = side.activeOf(side.focusedGroup(state.side))
  return typeof active === 'string' && active.startsWith('thread:')
    ? active.slice('thread:'.length)
    : null
}

let lastHeartbeat = {
  focused: false,
  projectId: '' as string | null,
  threadId: '' as string | null,
  at: 0,
}

/** Report foreground focus state to the server so Web Push is suppressed while actively viewing. */
export function reportPresence(focused: boolean, force = false): void {
  if (state.stream === 'unauthorized') return
  const now = Date.now()
  const projectId = state.currentId
  const threadId = focusedThreadId()
  const unchanged =
    lastHeartbeat.focused === focused &&
    lastHeartbeat.projectId === projectId &&
    lastHeartbeat.threadId === threadId
  if (!force && unchanged && now - lastHeartbeat.at < HEARTBEAT_DEDUPE_MS) return
  lastHeartbeat = { focused, projectId, threadId, at: now }
  void api
    .reportNotificationPresence({
      clientId: CLIENT_ID,
      focused,
      projectId,
      threadId,
    })
    .catch(() => {})
}

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
export const returnedToProject = shallowRef<{ projectId: string; at: number }>()

export function currentProjectIsIdle() {
  return seen.projectId === state.currentId && Date.now() - seen.at >= AWAY_MS
}

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
    returnedToProject.value = { projectId, at: now }
    // Missing one recap is not worth an error on the user's first move back.
    void api.noteReturn(projectId, now - last).catch(() => {})
  }
}

const seenNow = () => touch(Date.now())

function onFocus() {
  seenNow()
  reportPresence(isForegroundFocused())
}

function onBlur() {
  reportPresence(false)
}

function onVisibility() {
  seenNow()
  reportPresence(isForegroundFocused())
}

function onPageHide() {
  reportPresence(false, true)
}

let heartbeatTimer: ReturnType<typeof setInterval> | null = null

/** Follow the user's presence from now on. */
export function watchPresence(): void {
  for (const type of ['pointerdown', 'pointermove', 'keydown', 'wheel'] as const) {
    window.addEventListener(type, seenNow, { passive: true, capture: true })
  }

  window.addEventListener('focus', onFocus)
  window.addEventListener('blur', onBlur)
  document.addEventListener('visibilitychange', onVisibility)
  window.addEventListener('pagehide', onPageHide)
  watch(
    () => state.currentId,
    () => {
      seenNow()
      if (isForegroundFocused()) reportPresence(true)
    },
    { immediate: true },
  )
  watch(
    () => state.stream,
    (stream) => {
      if (stream === 'open') reportPresence(isForegroundFocused(), true)
    },
    { immediate: true },
  )

  if (heartbeatTimer !== null) clearInterval(heartbeatTimer)
  heartbeatTimer = setInterval(() => {
    if (isForegroundFocused()) {
      reportPresence(true, true)
    }
  }, PRESENCE_HEARTBEAT_MS)
}
