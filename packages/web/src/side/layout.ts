/**
 * The right side of the page: groups of tabs, split side by side or one above another as
 * the user likes, in the layout a `DouDock` shows. Each thing shown there lives in one tab
 * at a time. The functions here never change the layout they are given; they return the
 * new one.
 */
import {
  dockGroups,
  findDockGroup,
  moveDockTab,
  removeDockTab,
  selectDockTab,
  splitDockTab,
  type DockGroup,
  type DockNode,
} from 'dou-ui'

/** What a tab shows: one of the project's lists, or a thread's conversation. */
export type PaneKey = 'plan' | 'threads' | 'artifacts' | 'memory' | `thread:${string}`

export const LISTS = ['plan', 'threads', 'artifacts', 'memory'] as const satisfies PaneKey[]

export type SplitDirection = 'horizontal' | 'vertical'

export interface SideLayout {
  root: DockNode
  /** The group the user last used: what opens next goes there. */
  focused?: string
}

export const threadKey = (id: string): PaneKey => `thread:${id}`

/** The thread a tab shows, or `null` for a list. */
export const threadOf = (key: PaneKey): string | null =>
  key.startsWith('thread:') ? key.slice('thread:'.length) : null

/** The tabs of a group; the layout only ever holds pane keys. */
export const tabsOf = (group: DockGroup) => group.tabs as PaneKey[]

/** The tab in front of a group. */
export const activeOf = (group: DockGroup) => (group.active ?? group.tabs[0]) as PaneKey

/** What a project starts with: its lists in one group, the plan in front. */
export function defaultLayout(): SideLayout {
  return { root: { type: 'group', id: 'group-1', tabs: [...LISTS], active: 'plan' } }
}

/** Every group, left to right and top to bottom. */
export const groupsOf = (node: DockNode): DockGroup[] => dockGroups(node)

export function groupWith(layout: SideLayout, key: PaneKey): DockGroup | null {
  return findDockGroup(layout.root, key) ?? null
}

/** The group things open in: the one last used, else the first. */
export function focusedGroup(layout: SideLayout): DockGroup {
  const groups = groupsOf(layout.root)
  return groups.find((item) => item.id === layout.focused) ?? groups[0]!
}

/** The thread in front of the group last used, if a thread is. */
export function focusedThread(layout: SideLayout): string | null {
  return threadOf(activeOf(focusedGroup(layout)))
}

/** Every thread open in a tab, in front or not. */
export function openThreads(layout: SideLayout): string[] {
  return groupsOf(layout.root).flatMap((item) => tabsOf(item).flatMap((key) => threadOf(key) ?? []))
}

/** A layout with a new root, still pointing at a group that is there. */
function withRoot(layout: SideLayout, root: DockNode, focused = layout.focused): SideLayout {
  // The lists never close, so a side left without tabs only comes from a layout gone wrong.
  if (groupsOf(root).every((item) => !item.tabs.length)) return defaultLayout()
  const groups = groupsOf(root)
  return { root, focused: groups.some((item) => item.id === focused) ? focused : groups[0]!.id }
}

/** Bring `key` to the front of its group and use that group. */
export function activate(layout: SideLayout, key: PaneKey): SideLayout {
  const target = groupWith(layout, key)
  if (!target) return layout
  return withRoot(layout, selectDockTab(layout.root, key), target.id)
}

export function focus(layout: SideLayout, groupId: string | undefined): SideLayout {
  return layout.focused === groupId || !groupId ? layout : withRoot(layout, layout.root, groupId)
}

export interface OpenOptions {
  /** Open it in a new group beside the one last used, split this way. */
  beside?: SplitDirection
  /** Take the place of the thread in front of the group last used, if a thread is. */
  replace?: boolean
}

const sideOf = (direction: SplitDirection) => (direction === 'horizontal' ? 'right' : 'bottom')

/** Split `key` off to the right of or below the group `groupId`, into a group of its own. */
function splitOff(layout: SideLayout, key: PaneKey, groupId: string, direction: SplitDirection) {
  const root = splitDockTab(layout.root, key, groupId, sideOf(direction))
  if (root === layout.root) return layout
  return withRoot(layout, root, findDockGroup(root, key)!.id)
}

/**
 * Show `key`: where it already is, else in the group last used, after the tab in front.
 * With `beside`, it gets a group of its own next to that one, moved there if it was open.
 */
export function open(layout: SideLayout, key: PaneKey, options: OpenOptions = {}): SideLayout {
  const current = groupWith(layout, key)
  if (current && !options.beside) return activate(layout, key)
  const target = focusedGroup(layout)
  if (options.beside) {
    if (current?.tabs.length === 1) return activate(layout, key)
    return splitOff(layout, key, target.id, options.beside)
  }
  const front = activeOf(target)
  const at = target.tabs.indexOf(front)
  let root = moveDockTab(layout.root, key, target.id, at + 1)
  if (options.replace && threadOf(front)) root = removeDockTab(root, front)
  return withRoot(layout, root, target.id)
}

/** Close a tab. A group left empty goes, and the group beside it takes its room. */
export function close(layout: SideLayout, key: PaneKey): SideLayout {
  if (!groupWith(layout, key)) return layout
  return withRoot(layout, removeDockTab(layout.root, key))
}

/**
 * Close the threads of the group but `key`. The lists stay: they never close, so the
 * project's plan, threads, artifacts and memory are always somewhere on the side.
 */
export function closeOthers(layout: SideLayout, key: PaneKey): SideLayout {
  const target = groupWith(layout, key)
  if (!target) return layout
  let root = selectDockTab(layout.root, key)
  for (const other of tabsOf(target)) {
    if (other !== key && threadOf(other)) root = removeDockTab(root, other)
  }
  return withRoot(layout, root, target.id)
}

/** Move `key` out of its group into a new one beside it. Needs another tab to stay behind. */
export function split(layout: SideLayout, key: PaneKey, direction: SplitDirection): SideLayout {
  const target = groupWith(layout, key)
  if (!target || target.tabs.length < 2) return layout
  return splitOff(layout, key, target.id, direction)
}

/** Move `key` to the end of the group `groupId`, in front there. */
export function move(layout: SideLayout, key: PaneKey, groupId: string): SideLayout {
  if (groupWith(layout, key)?.id === groupId) return layout
  if (!groupsOf(layout.root).some((item) => item.id === groupId)) return layout
  return withRoot(layout, moveDockTab(layout.root, key, groupId), groupId)
}

/** Close the tabs `keep` turns down, e.g. threads that no longer exist. */
export function prune(layout: SideLayout, keep: (key: PaneKey) => boolean): SideLayout {
  let next = layout
  for (const item of groupsOf(layout.root)) {
    for (const key of tabsOf(item)) if (!keep(key)) next = close(next, key)
  }
  return next
}

const isKey = (value: unknown): value is PaneKey =>
  typeof value === 'string' &&
  ((LISTS as readonly string[]).includes(value) || /^thread:.+/.test(value))

function parseNode(value: unknown, seen: Set<string>, ids: Set<string>): DockNode | null {
  if (!value || typeof value !== 'object') return null
  const node = value as Record<string, unknown>
  if (node.type === 'group') {
    if (typeof node.id !== 'string' || ids.has(node.id) || !Array.isArray(node.tabs)) return null
    ids.add(node.id)
    const tabs: PaneKey[] = []
    for (const key of node.tabs) {
      if (!isKey(key) || seen.has(key)) continue
      seen.add(key)
      tabs.push(key)
    }
    if (!tabs.length) return null
    const active = isKey(node.active) && tabs.includes(node.active) ? node.active : tabs[0]!
    return { type: 'group', id: node.id, tabs, active }
  }
  if (node.type !== 'split') return null
  const start = parseNode(node.start, seen, ids)
  const end = parseNode(node.end, seen, ids)
  if (!start || !end) return start ?? end
  const direction = node.direction === 'vertical' ? 'vertical' : 'horizontal'
  const size = typeof node.size === 'number' && node.size > 0 && node.size < 100 ? node.size : 50
  return { type: 'split', direction, size, start, end }
}

/** A layout as stored, checked; `null` if it does not hold one. */
export function parseLayout(raw: unknown): SideLayout | null {
  if (!raw || typeof raw !== 'object') return null
  const root = parseNode((raw as { root?: unknown }).root, new Set(), new Set())
  if (!root) return null
  const focused = (raw as { focused?: unknown }).focused
  return withRoot({ root }, root, typeof focused === 'string' ? focused : undefined)
}
