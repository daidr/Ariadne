/**
 * The right side of the page: groups of tabs, split side by side or one above another as
 * the user likes, in the layout a `DouDock` shows. Lists have a fixed tab and can also
 * have a movable copy. Layout updates never mutate the tree they are given.
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

/**
 * What a tab shows: one of the project's lists, a thread's conversation, an artifact, a
 * thread's branch changes, or what one turn of a thread changed.
 */
export type ListKey = 'plan' | 'threads' | 'artifacts'
export type PaneKey =
  | ListKey
  | `list:${ListKey}:${number}`
  | `thread:${string}`
  | `artifact:${string}`
  | `changes:${string}`
  | `turn:${string}`

export const LISTS = ['plan', 'threads', 'artifacts'] as const satisfies ListKey[]
export const isPinned = (key: string | number): key is ListKey =>
  (LISTS as readonly (string | number)[]).includes(key)
export function listOf(key: string | number): ListKey | null {
  if (isPinned(key)) return key
  const list =
    typeof key === 'string' ? /^list:(plan|threads|artifacts):\d+$/.exec(key)?.[1] : undefined
  if (list !== undefined && isPinned(list)) return list
  return null
}

export type SplitDirection = 'horizontal' | 'vertical'

export interface SideLayout {
  root: DockNode
  /** The group the user last used: what opens next goes there. */
  focused?: string
  /** The tabs the user has activated, most recent first. */
  recent?: PaneKey[]
}

export const threadKey = (id: string): PaneKey => `thread:${id}`

/** The thread a tab shows, or `null` for a list. */
export const threadOf = (key: PaneKey): string | null =>
  key.startsWith('thread:') ? key.slice('thread:'.length) : null

export const artifactKey = (id: string): PaneKey => `artifact:${id}`
export const changesKey = (threadId: string): PaneKey => `changes:${threadId}`
export const turnKey = (threadId: string, turnId: string): PaneKey => `turn:${threadId}:${turnId}`

/** What a tab other than a list or a thread shows. */
export type PaneView =
  | { kind: 'artifact'; id: string }
  | { kind: 'changes'; threadId: string }
  | { kind: 'turn'; threadId: string; turnId: string }

export function viewOf(key: PaneKey): PaneView | null {
  if (key.startsWith('artifact:')) return { kind: 'artifact', id: key.slice('artifact:'.length) }
  if (key.startsWith('changes:')) return { kind: 'changes', threadId: key.slice('changes:'.length) }
  if (key.startsWith('turn:')) {
    const [threadId = '', turnId = ''] = key.slice('turn:'.length).split(':')
    return { kind: 'turn', threadId, turnId }
  }
  return null
}

/** The tabs of a group; the layout only ever holds pane keys. */
export const tabsOf = (group: DockGroup) => group.tabs as PaneKey[]

export function pinnedTabsOf(group: DockGroup): ListKey[] {
  const pinned = group.tabs.filter(isPinned)
  return pinned.length === LISTS.length ? pinned : [...LISTS]
}

/** The tab in front of a group. */
export const activeOf = (group: DockGroup) => (group.active ?? group.tabs[0]) as PaneKey

/** What a project starts with: its lists in one group, the plan in front unless told otherwise. */
export function defaultLayout(front: 'plan' | 'threads' = 'plan'): SideLayout {
  return {
    root: { type: 'group', id: 'group-1', tabs: [...LISTS], active: front },
    recent: [front],
  }
}

/** Every group, left to right and top to bottom. */
export const groupsOf = (node: DockNode): DockGroup[] => dockGroups(node)

function listCopy(root: DockNode, key: ListKey): PaneKey {
  let index = 1
  while (findDockGroup(root, `list:${key}:${index}`)) index++
  return `list:${key}:${index}`
}

function mapGroups(node: DockNode, map: (group: DockGroup) => DockGroup): DockNode {
  if (node.type === 'group') return map(node)
  const start = mapGroups(node.start, map)
  const end = mapGroups(node.end, map)
  return start === node.start && end === node.end ? node : { ...node, start, end }
}

export function groupWith(layout: SideLayout, key: PaneKey): DockGroup | null {
  return findDockGroup(layout.root, key) ?? null
}

/** The group things open in: the one last used, else the first. */
export function focusedGroup(layout: SideLayout): DockGroup {
  const groups = groupsOf(layout.root)
  return groups.find((item) => item.id === layout.focused) ?? groups[0]!
}

/** Present the dock as one group on narrow screens without changing the saved splits. */
export function compactGroup(layout: SideLayout): DockGroup {
  const groups = groupsOf(layout.root)
  const active = activeOf(focusedGroup(layout))
  return {
    type: 'group',
    id: groups[0]!.id,
    tabs: [...pinnedTabsOf(groups[0]!), ...groups.flatMap(tabsOf).filter((key) => !listOf(key))],
    active: listOf(active) ?? active,
  }
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
function withRoot(layout: SideLayout, root: DockNode, focused?: string | null): SideLayout {
  const previous = groupsOf(layout.root)[0]!
  let first = groupsOf(root)[0]!
  if (previous.id !== first.id) {
    const original = groupsOf(root).find((group) => group.id === previous.id)
    if (original) {
      // Splitting on the leading edge still keeps the fixed tabs in the first panel.
      const leading = first
      root = mapGroups(root, (group) =>
        group.id === leading.id ? original : group.id === original.id ? leading : group,
      )
      first = original
    }
  }
  const nextPinned = first.tabs.filter(isPinned)
  const pinned = nextPinned.length === LISTS.length ? nextPinned : pinnedTabsOf(previous)
  const tabs = [...pinned, ...first.tabs.filter((key) => !listOf(key))]
  const returnedList =
    first.active != null && !tabs.includes(first.active) ? listOf(first.active) : null
  const active =
    first.active != null && tabs.includes(first.active)
      ? first.active
      : returnedList && tabs.includes(returnedList)
        ? returnedList
        : previous.active != null && tabs.includes(previous.active)
          ? previous.active
          : tabs[0]
  const targetFocused = focused ?? (returnedList ? first.id : layout.focused)
  if (
    first.active !== active ||
    tabs.some((key, i) => first.tabs[i] !== key) ||
    tabs.length !== first.tabs.length
  ) {
    root = mapGroups(root, (group) => (group.id === first.id ? { ...group, tabs, active } : group))
  }
  const groups = groupsOf(root)
  const groupByTab = new Map(
    groups.flatMap((group) => tabsOf(group).map((key) => [key, group] as const)),
  )
  const recent = [...new Set(layout.recent ?? [])].filter((key) => groupByTab.has(key))
  const current =
    groups.find((group) => group.id === targetFocused) ?? groupByTab.get(recent[0]!) ?? groups[0]!
  return {
    root,
    focused: current.id,
    recent: [...new Set([activeOf(current), ...recent])],
  }
}

/** A native drag of a fixed tab opens its copy. Returning a copy removes it. */
export function updateDock(layout: SideLayout, root: DockNode): SideLayout {
  const first = groupsOf(layout.root)[0]!
  root = mapGroups(root, (group) => {
    if (group.id === first.id) return group
    const lists = new Map<ListKey, string | number>()
    let active = group.active
    for (const key of group.tabs) {
      const list = listOf(key)
      if (list && !isPinned(key)) lists.set(list, key)
    }
    const tabs = [
      ...new Set(
        group.tabs.map((key) => {
          const list = listOf(key)
          if (!list) return key
          const copy = lists.get(list) ?? listCopy(root, list)
          lists.set(list, copy)
          if (key === active) active = copy
          return copy
        }),
      ),
    ]
    return active !== group.active ||
      tabs.some((key, i) => key !== group.tabs[i]) ||
      tabs.length !== group.tabs.length
      ? { ...group, tabs, active }
      : group
  })
  return withRoot(layout, root)
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
  if (isPinned(key)) key = listCopy(layout.root, key)
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
export function close(layout: SideLayout, key: PaneKey, acrossGroups = false): SideLayout {
  if (isPinned(key)) return layout
  const group = groupWith(layout, key)
  if (!group) return layout
  const inFront = activeOf(group) === key
  const compact = acrossGroups && inFront && group.id === focusedGroup(layout).id
  const previous =
    inFront && layout.recent?.find((tab) => tab !== key && (compact || group.tabs.includes(tab)))
  const root = removeDockTab(previous ? selectDockTab(layout.root, previous) : layout.root, key)
  const focused = compact && previous ? groupWith(layout, previous)?.id : layout.focused
  return withRoot(layout, root, focused)
}

/**
 * Close the other tabs of the group but its lists: they never close, so the project's
 * plan, threads and artifacts are always somewhere on the side.
 */
export function closeOthers(layout: SideLayout, key: PaneKey): SideLayout {
  const target = groupWith(layout, key)
  if (!target) return layout
  let root = selectDockTab(layout.root, key)
  for (const other of tabsOf(target)) {
    if (other !== key && !isPinned(other)) root = removeDockTab(root, other)
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
  const list = listOf(key)
  if (list && groupId === groupsOf(layout.root)[0]!.id) {
    return activate(close(layout, key), list)
  }
  const target = groupsOf(layout.root).find((item) => item.id === groupId)
  if (!target) return layout
  if (groupWith(layout, key)?.id === groupId) return layout
  if (list) {
    const existing = tabsOf(target).find((tab) => listOf(tab) === list)
    if (existing) return activate(close(layout, key), existing)
    if (isPinned(key)) key = listCopy(layout.root, key)
  }
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

export const isPaneKey = (value: unknown): value is PaneKey =>
  typeof value === 'string' &&
  (listOf(value) !== null || /^(?:(?:thread|artifact|changes):[^:]+|turn:[^:]+:[^:]+)$/.test(value))

function parseNode(
  value: unknown,
  seen: Set<string>,
  ids: Set<string>,
  keepTurnChanges: boolean,
): DockNode | null {
  if (!value || typeof value !== 'object') return null
  const node = value as Record<string, unknown>
  if (node.type === 'group') {
    if (typeof node.id !== 'string' || ids.has(node.id) || !Array.isArray(node.tabs)) return null
    ids.add(node.id)
    const tabs: PaneKey[] = []
    for (const key of node.tabs) {
      if (!isPaneKey(key) || seen.has(key) || (!keepTurnChanges && key.startsWith('turn:')))
        continue
      seen.add(key)
      tabs.push(key)
    }
    if (!tabs.length) return null
    const active = isPaneKey(node.active) && tabs.includes(node.active) ? node.active : tabs[0]!
    return { type: 'group', id: node.id, tabs, active }
  }
  if (node.type !== 'split') return null
  const start = parseNode(node.start, seen, ids, keepTurnChanges)
  const end = parseNode(node.end, seen, ids, keepTurnChanges)
  if (!start || !end) return start ?? end
  const direction = node.direction === 'vertical' ? 'vertical' : 'horizontal'
  const size = typeof node.size === 'number' && node.size > 0 && node.size < 100 ? node.size : 50
  return { type: 'split', direction, size, start, end }
}

/** A layout as stored, checked; `null` if it does not hold one. */
export function parseLayout(raw: unknown, keepTurnChanges = false): SideLayout | null {
  if (!raw || typeof raw !== 'object') return null
  const root = parseNode((raw as { root?: unknown }).root, new Set(), new Set(), keepTurnChanges)
  if (!root) return null
  const { focused, recent } = raw as { focused?: unknown; recent?: unknown }
  return updateDock(
    {
      root,
      focused: typeof focused === 'string' ? focused : undefined,
      recent: Array.isArray(recent) ? recent.filter(isPaneKey) : undefined,
    },
    root,
  )
}
