import type { ThreadView } from '@ariadne/core'
import { ref } from 'vue'
import { useHotkey } from 'dou-ui'
import { locale } from './i18n'
import { groupOf, sortThreads, threadGroups } from './stage'
import { openThread, state } from './store'
import { threadSort } from './components/thread-list'

/** The two conversations on screen: the coordinator's on the left, a thread's on the right. */
export type PanelKind = 'coordinator' | 'thread'

/** What the keyboard can ask of a conversation on screen. */
export interface PanelHandle {
  focus: () => void
  /** Stop its turn; `false` when nothing was running. */
  stop: () => boolean
}

const panels = new Map<PanelKind, PanelHandle>()

/** The conversation the user last clicked or typed in; keys with no focus go there. */
export const lastPanel = ref<PanelKind>('coordinator')

/** The shortcut sheet is open. */
export const sheetOpen = ref(false)

/** Make a conversation reachable from the keyboard while it is on screen. */
export function registerPanel(kind: PanelKind, handle: PanelHandle): () => void {
  panels.set(kind, handle)
  return () => {
    if (panels.get(kind) === handle) panels.delete(kind)
  }
}

/**
 * Open a thread with the caret in its input, for the user to say what to change. Its panel
 * may only mount once the thread is open, so this waits a few frames for it.
 */
export async function writeToThread(threadId: string) {
  await openThread(threadId)
  for (let frame = 0; frame < 30; frame++) {
    await new Promise(requestAnimationFrame)
    const panel = panels.get('thread')
    if (panel && state.threadId === threadId) {
      lastPanel.value = 'thread'
      panel.focus()
      return
    }
  }
}

/** The conversation focus is in, else the one last used, else whichever is on screen. */
function activePanel(): PanelHandle | null {
  const focused = document.activeElement?.closest<HTMLElement>('[data-panel]')?.dataset.panel
  const kind = (focused as PanelKind | undefined) ?? lastPanel.value
  return panels.get(kind) ?? panels.get('thread') ?? panels.get('coordinator') ?? null
}

/** The project's threads in the order the thread list shows them, folded groups too. */
export function threadOrder(): string[] {
  const threads = state.detail?.threads ?? []
  const ids = new Set(threads.map((thread) => thread.id))
  // A reviewing thread comes right after its author, as the list shows it.
  const nested = (thread: ThreadView) => !!thread.reviewOf && ids.has(thread.reviewOf)
  return threadGroups.flatMap((group) =>
    sortThreads(
      threads.filter((thread) => groupOf[thread.stage] === group && !nested(thread)),
      threadSort.value,
      locale.value,
    ).flatMap((thread) => {
      const reviewer = thread.review?.reviewerId
      return reviewer && ids.has(reviewer) ? [thread.id, reviewer] : [thread.id]
    }),
  )
}

/**
 * The thread `step` away from `current` in `order`, stopping at either end. With none open,
 * down starts at the top and up at the bottom.
 */
export function steppedThread(order: string[], current: string | null, step: 1 | -1) {
  if (!order.length) return null
  const at = current ? order.indexOf(current) : -1
  if (at < 0) return step > 0 ? order[0]! : order.at(-1)!
  return order[Math.min(order.length - 1, Math.max(0, at + step))]!
}

/** Whether a key press came from a field other than a message input. */
function typingElsewhere(event: KeyboardEvent) {
  const target = event.target as HTMLElement | null
  if (!target || !('tagName' in target)) return false
  const field = target.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(target.tagName)
  return field && !target.closest('[data-message-input]')
}

/**
 * Whether a dialog, drawer or the search is open, or focus is in a menu or popover: keys
 * there belong to it, and switching or focusing what is behind it would pull the user out.
 */
function layerOpen() {
  const modal = [...document.querySelectorAll('[aria-modal="true"]')].some(
    (el) => el.getClientRects().length > 0,
  )
  return modal || !!document.activeElement?.closest('[role="dialog"], [role="menu"]')
}

const keys = () => state.input.keys

/**
 * Whether a key types into a field when pressed there: no modifier but Shift, and a key
 * that writes or edits text. Such a key bound to an action is only heard outside fields.
 */
export function typesText(hotkey: string): boolean {
  const parts = hotkey.split('+')
  const key = parts.at(-1) ?? ''
  if (parts.slice(0, -1).some((mod) => mod !== 'shift')) return false
  return key.length === 1 || ['space', 'enter', 'tab', 'backspace', 'delete'].includes(key)
}

function inField(event: KeyboardEvent) {
  const target = event.target as HTMLElement | null
  if (!target || !('tagName' in target)) return false
  return target.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(target.tagName)
}

/** The app's keys, as the user bound them: bound once, for as long as the calling component lives. */
export function useShortcuts() {
  // The sheet is listed first: `?` is Shift+/ on most keyboards and would also match `/`.
  useHotkey(
    () => [keys().sheet, keys().focus],
    (event, key) => {
      if (layerOpen()) return
      event.preventDefault()
      if (key === keys().sheet) sheetOpen.value = true
      else activePanel()?.focus()
    },
    { preventDefault: false },
  )

  useHotkey(
    () => [keys().previousThread, keys().nextThread],
    (event, key) => {
      // In any other field the keys do what they do there, as usual.
      if (typingElsewhere(event) || layerOpen()) return
      if (inField(event) && typesText(key)) return
      event.preventDefault()
      const step = key === keys().previousThread ? -1 : 1
      const next = steppedThread(threadOrder(), state.threadId, step)
      if (!next) return
      lastPanel.value = 'thread'
      void openThread(next)
    },
    { allowInInput: true, preventDefault: false },
  )

  // Heard on the window, after the document: an open menu, popover or dialog takes Escape
  // there and stops it, so the key only stops a turn when nothing else wanted it.
  useHotkey(
    () => keys().stop,
    (event, key) => {
      if (typingElsewhere(event) || (inField(event) && typesText(key))) return
      if (activePanel()?.stop()) event.preventDefault()
    },
    { target: () => window, allowInInput: true, preventDefault: false },
  )
}

const NAMED_KEYS: Record<string, string> = {
  ' ': 'space',
  arrowup: 'up',
  arrowdown: 'down',
  arrowleft: 'left',
  arrowright: 'right',
  escape: 'escape',
  enter: 'enter',
  tab: 'tab',
  backspace: 'backspace',
  delete: 'delete',
  home: 'home',
  end: 'end',
  pageup: 'pageup',
  pagedown: 'pagedown',
}

/** The keys the settings take, as the server checks them. */
const BINDABLE =
  /^((mod|meta|ctrl|alt|shift)\+){0,4}([a-z0-9]|f([1-9]|1[0-2])|escape|enter|tab|space|backspace|delete|up|down|left|right|home|end|pageup|pagedown|[`~!@#$%^&*()\-_=[\]{}\\|;:'",.<>/?])$/

/**
 * A key press as a hotkey the app can bind, e.g. `mod+shift+k`; `null` for a modifier on its
 * own or a key the settings do not take. Letters and digits are read from the physical key,
 * so Alt or a layout does not change them; a symbol typed with Shift (`?`) is the symbol alone,
 * as `useHotkey` matches it.
 */
export function hotkeyOf(event: KeyboardEvent, apple: boolean): string | null {
  const raw = event.key
  if (!raw || ['Meta', 'Control', 'Alt', 'Shift', 'CapsLock', 'Dead'].includes(raw)) return null
  const lower = raw.toLowerCase()
  let key: string
  let symbol = false
  if (/^Key[A-Z]$/.test(event.code)) key = event.code.slice(3).toLowerCase()
  else if (/^Digit\d$/.test(event.code)) key = event.code.slice(5)
  else if (NAMED_KEYS[lower]) key = NAMED_KEYS[lower]
  else if (/^f([1-9]|1[0-2])$/.test(lower)) key = lower
  else if (raw.length === 1 && raw !== '+') {
    key = raw
    symbol = true
  } else return null
  const mods = [
    apple ? event.metaKey && 'mod' : event.ctrlKey && 'mod',
    apple ? false : event.metaKey && 'meta',
    apple ? event.ctrlKey && 'ctrl' : false,
    event.altKey && 'alt',
    event.shiftKey && !symbol && 'shift',
  ].filter((mod): mod is string => !!mod)
  const hotkey = [...mods, key].join('+')
  return BINDABLE.test(hotkey) ? hotkey : null
}
