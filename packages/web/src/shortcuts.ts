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

/** The conversation focus is in, else the one last used, else whichever is on screen. */
function activePanel(): PanelHandle | null {
  const focused = document.activeElement?.closest<HTMLElement>('[data-panel]')?.dataset.panel
  const kind = (focused as PanelKind | undefined) ?? lastPanel.value
  return panels.get(kind) ?? panels.get('thread') ?? panels.get('coordinator') ?? null
}

/** The project's threads in the order the thread list shows them, folded groups too. */
export function threadOrder(): string[] {
  const threads = state.detail?.threads ?? []
  return threadGroups.flatMap((group) =>
    sortThreads(
      threads.filter((thread) => groupOf[thread.stage] === group),
      threadSort.value,
      locale.value,
    ).map((thread) => thread.id),
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

/** The app's keys: bound once, for as long as the calling component lives. */
export function useShortcuts() {
  // `?` is listed first: Shift+/ is the same physical key and would also match `/`.
  useHotkey(
    ['?', '/'],
    (event, key) => {
      if (layerOpen()) return
      event.preventDefault()
      if (key === '?') sheetOpen.value = true
      else activePanel()?.focus()
    },
    { preventDefault: false },
  )

  useHotkey(
    ['mod+up', 'mod+down'],
    (event, key) => {
      // In any other field the keys move the caret, as usual.
      if (typingElsewhere(event) || layerOpen()) return
      event.preventDefault()
      const next = steppedThread(threadOrder(), state.threadId, key === 'mod+up' ? -1 : 1)
      if (!next) return
      lastPanel.value = 'thread'
      void openThread(next)
    },
    { allowInInput: true, preventDefault: false },
  )

  // Heard on the window, after the document: an open menu, popover or dialog takes Escape
  // there and stops it, so Escape only stops a turn when nothing else wanted it.
  useHotkey(
    'escape',
    (event) => {
      if (typingElsewhere(event)) return
      if (activePanel()?.stop()) event.preventDefault()
    },
    { target: () => window, allowInInput: true, preventDefault: false },
  )
}
