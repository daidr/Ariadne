/**
 * The right side of the page: groups of tabs, split side by side or one above another as
 * the user likes. Each thing shown there lives in one tab at a time. The functions here
 * never change the layout they are given; they return the new one.
 */

/** What a tab shows: one of the project's lists, or a thread's conversation. */
export type PaneKey = 'plan' | 'threads' | 'artifacts' | 'memory' | `thread:${string}`

export const LISTS = ['plan', 'threads', 'artifacts', 'memory'] as const satisfies PaneKey[]

export interface PaneGroup {
  type: 'group'
  id: string
  tabs: PaneKey[]
  active: PaneKey
}

export type SplitDirection = 'horizontal' | 'vertical'

export interface PaneSplit {
  type: 'split'
  id: string
  /** `horizontal` puts `first` on the left; `vertical` puts it on top. */
  direction: SplitDirection
  /** How much of the room `first` takes, in percent. */
  size: number
  first: PaneNode
  second: PaneNode
}

export type PaneNode = PaneGroup | PaneSplit

export interface SideLayout {
  root: PaneNode
  /** The group the user last used: what opens next goes there. */
  focused: string
}

export const threadKey = (id: string): PaneKey => `thread:${id}`

/** The thread a tab shows, or `null` for a list. */
export const threadOf = (key: PaneKey): string | null =>
  key.startsWith('thread:') ? key.slice('thread:'.length) : null

let counter = 0
const newId = (prefix: string) => `${prefix}${Date.now().toString(36)}${(counter++).toString(36)}`

const group = (tabs: PaneKey[], active: PaneKey = tabs[0]!): PaneGroup => ({
  type: 'group',
  id: newId('g'),
  tabs,
  active,
})

/** What a project starts with: its lists in one group, the plan in front. */
export function defaultLayout(): SideLayout {
  const root = group([...LISTS])
  return { root, focused: root.id }
}

/** Every group, left to right and top to bottom. */
export function groupsOf(node: PaneNode): PaneGroup[] {
  return node.type === 'group' ? [node] : [...groupsOf(node.first), ...groupsOf(node.second)]
}

export function groupWith(layout: SideLayout, key: PaneKey): PaneGroup | null {
  return groupsOf(layout.root).find((item) => item.tabs.includes(key)) ?? null
}

/** The group things open in: the one last used, else the first. */
export function focusedGroup(layout: SideLayout): PaneGroup {
  const groups = groupsOf(layout.root)
  return groups.find((item) => item.id === layout.focused) ?? groups[0]!
}

/** The thread in front of the group last used, if a thread is. */
export function focusedThread(layout: SideLayout): string | null {
  return threadOf(focusedGroup(layout).active)
}

/** Every thread open in a tab, in front or not. */
export function openThreads(layout: SideLayout): string[] {
  return groupsOf(layout.root).flatMap((item) => item.tabs.flatMap((key) => threadOf(key) ?? []))
}

/** Replace the node with `id` by what `change` makes of it; `null` takes it out. */
function mapNode(
  node: PaneNode,
  id: string,
  change: (node: PaneNode) => PaneNode | null,
): PaneNode | null {
  if (node.id === id) return change(node)
  if (node.type === 'group') return node
  const first = mapNode(node.first, id, change)
  const second = mapNode(node.second, id, change)
  // A split left with one side is that side alone.
  if (!first) return second
  if (!second) return first
  return first === node.first && second === node.second ? node : { ...node, first, second }
}

function updateGroup(
  layout: SideLayout,
  id: string,
  change: (group: PaneGroup) => PaneNode | null,
): SideLayout {
  const root = mapNode(layout.root, id, (node) => (node.type === 'group' ? change(node) : node))
  // The last group never goes: it keeps the lists instead.
  if (!root) return defaultLayout()
  const focused = groupsOf(root).some((item) => item.id === layout.focused)
    ? layout.focused
    : groupsOf(root)[0]!.id
  return { root, focused }
}

/** A group without `key`, or `null` once nothing is left in it. */
function without(target: PaneGroup, key: PaneKey): PaneGroup | null {
  const at = target.tabs.indexOf(key)
  if (at === -1) return target
  const tabs = target.tabs.filter((item) => item !== key)
  if (!tabs.length) return null
  // The tab beside the closed one comes to the front, as tabs usually do.
  const active = target.active === key ? tabs[Math.min(at, tabs.length - 1)]! : target.active
  return { ...target, tabs, active }
}

/** Bring `key` to the front of its group and use that group. */
export function activate(layout: SideLayout, key: PaneKey): SideLayout {
  const target = groupWith(layout, key)
  if (!target) return layout
  return { ...updateGroup(layout, target.id, (g) => ({ ...g, active: key })), focused: target.id }
}

export function focus(layout: SideLayout, groupId: string): SideLayout {
  return layout.focused === groupId ? layout : { ...layout, focused: groupId }
}

export interface OpenOptions {
  /** Open it in a new group beside the one last used, split this way. */
  beside?: SplitDirection
  /** Take the place of the thread in front of the group last used, if a thread is. */
  replace?: boolean
}

/**
 * Show `key`: where it already is, else in the group last used, after the tab in front.
 * With `beside`, it gets a group of its own next to that one, moved there if it was open.
 */
export function open(layout: SideLayout, key: PaneKey, options: OpenOptions = {}): SideLayout {
  const current = groupWith(layout, key)
  if (current && !options.beside) return activate(layout, key)
  if (options.beside) {
    if (current?.tabs.length === 1) return activate(layout, key)
    // Taken from a group that keeps other tabs, so the group last used is still there.
    const from = focusedGroup(layout).id
    return splitWith(current ? close(layout, key) : layout, from, key, options.beside)
  }
  const target = focusedGroup(layout)
  const replacing = options.replace && threadOf(target.active) !== null
  return updateGroup(layout, target.id, (g) => {
    const at = g.tabs.indexOf(g.active)
    const tabs = [...g.tabs]
    tabs.splice(replacing ? at : at + 1, replacing ? 1 : 0, key)
    return { ...g, tabs, active: key }
  })
}

/** Put `key` in a new group after the group `groupId`, split in `direction`. */
function splitWith(
  layout: SideLayout,
  groupId: string,
  key: PaneKey,
  direction: SplitDirection,
): SideLayout {
  const added = group([key])
  const root = mapNode(layout.root, groupId, (node) => ({
    type: 'split',
    id: newId('s'),
    direction,
    size: 50,
    first: node,
    second: added,
  }))!
  return { root, focused: added.id }
}

/** Close a tab. A group left empty goes, and the group beside it takes its room. */
export function close(layout: SideLayout, key: PaneKey): SideLayout {
  const target = groupWith(layout, key)
  if (!target) return layout
  return updateGroup(layout, target.id, (g) => without(g, key))
}

/**
 * Close the threads of the group but `key`. The lists stay: they never close, so the
 * project's plan, threads, artifacts and memory are always somewhere on the side.
 */
export function closeOthers(layout: SideLayout, key: PaneKey): SideLayout {
  const target = groupWith(layout, key)
  if (!target) return layout
  return updateGroup(layout, target.id, (g) => {
    const tabs = g.tabs.filter((item) => item === key || !threadOf(item))
    return { ...g, tabs, active: key }
  })
}

/** Move `key` out of its group into a new one beside it. Needs another tab to stay behind. */
export function split(layout: SideLayout, key: PaneKey, direction: SplitDirection): SideLayout {
  const target = groupWith(layout, key)
  if (!target || target.tabs.length < 2) return layout
  const left = updateGroup(layout, target.id, (g) => without(g, key))
  return splitWith(left, target.id, key, direction)
}

/** Move `key` to the group `groupId`, in front there. */
export function move(layout: SideLayout, key: PaneKey, groupId: string): SideLayout {
  const from = groupWith(layout, key)
  if (!from || from.id === groupId) return layout
  const left = close(layout, key)
  if (!groupsOf(left.root).some((item) => item.id === groupId)) return layout
  return {
    ...updateGroup(left, groupId, (g) => ({ ...g, tabs: [...g.tabs, key], active: key })),
    focused: groupId,
  }
}

export function resize(layout: SideLayout, splitId: string, size: number): SideLayout {
  const root = mapNode(layout.root, splitId, (node) =>
    node.type === 'split' ? { ...node, size } : node,
  )!
  return { ...layout, root }
}

/** Close the tabs `keep` turns down, e.g. threads that no longer exist. */
export function prune(layout: SideLayout, keep: (key: PaneKey) => boolean): SideLayout {
  let next = layout
  for (const item of groupsOf(layout.root)) {
    for (const key of item.tabs) if (!keep(key)) next = close(next, key)
  }
  return next
}

const isKey = (value: unknown): value is PaneKey =>
  typeof value === 'string' &&
  ((LISTS as readonly string[]).includes(value) || /^thread:.+/.test(value))

function parseNode(value: unknown, seen: Set<string>, ids: Set<string>): PaneNode | null {
  if (!value || typeof value !== 'object') return null
  const node = value as Record<string, unknown>
  if (typeof node.id !== 'string' || ids.has(node.id)) return null
  ids.add(node.id)
  if (node.type === 'group') {
    if (!Array.isArray(node.tabs)) return null
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
  const first = parseNode(node.first, seen, ids)
  const second = parseNode(node.second, seen, ids)
  if (!first || !second) return first ?? second
  const direction = node.direction === 'vertical' ? 'vertical' : 'horizontal'
  const size = typeof node.size === 'number' && node.size > 0 && node.size < 100 ? node.size : 50
  return { type: 'split', id: node.id, direction, size, first, second }
}

/** A layout as stored, checked; `null` if it does not hold one. */
export function parseLayout(raw: unknown): SideLayout | null {
  if (!raw || typeof raw !== 'object') return null
  const root = parseNode((raw as { root?: unknown }).root, new Set(), new Set())
  if (!root) return null
  const focused = (raw as { focused?: unknown }).focused
  const groups = groupsOf(root)
  return {
    root,
    focused: groups.some((item) => item.id === focused) ? (focused as string) : groups[0]!.id,
  }
}
