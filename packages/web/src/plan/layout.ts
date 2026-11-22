import type {
  PlanNodeKind,
  PlanNodeStatus,
  PlanNodeView,
  PlanOp,
  PlanPreview,
  PlanRevisionView,
  PlanView,
} from '@ariadne/core'
import type { FlowDocument, FlowNode } from '@dou-ui/flow'
import { layoutGraph } from 'dou-ui'
import { showsHeadline } from '../stage'

/** Room between dependency levels, between nodes on one level, and between unrelated parts. */
const RANK_GAP = 56
const LANE_GAP = 32
const PART_GAP = 72
const COMPACT_PART_GAP = 48
const INNER_PART_GAP = 40
const COMPACT_INNER_PART_GAP = 24
const GROUP_PAD = { side: 24, top: 52, bottom: 24 } as const
const COMPACT_GROUP_PAD = { side: 16, top: 44, bottom: 16 } as const

/** Prefix for visual group container node IDs in the flow document. */
export const GROUP_PREFIX = 'grp:'
export const isGroupId = (id: string) => id.startsWith(GROUP_PREFIX)
export const groupIdOf = (name: string) => `${GROUP_PREFIX}${name}`
export const groupNameOf = (node: Pick<PlanNodeView, 'group'>) => (node.group ?? '').trim()

/**
 * Plan nodes are drawn this wide (PlanNode.vue: 248px of text plus 12px padding each side);
 * their height is estimated from what they show.
 */
export const NODE_WIDTH = 272
/** In a narrow pane nodes show their status and title only, this wide (PlanNode.vue). */
export const COMPACT_WIDTH = 176

/** How a node or edge stands in a change shown on the graph. */
export type PlanMark = 'added' | 'removed' | 'changed' | null

export interface PlanNodeData {
  title: string
  kind: PlanNodeKind
  description: string
  acceptance: string
  group?: string
  status: PlanNodeStatus
  threads: PlanNodeView['threads']
  /** In a previewed change: what it does to this node. */
  mark: PlanMark
  /** Changed by the coordinator since the user last looked. */
  fresh: boolean
  /** A change waiting on the user's yes touches this node. */
  proposed: boolean
  /** Drawn small, with its status and title only. */
  compact: boolean
  /** Part of a fully completed node/group shown only when "Show completed" is enabled. */
  archived?: boolean
  /** Its dependencies that are not done, by title, while work on it started anyway. */
  ahead: string[]
  [key: string]: unknown
}

export interface PlanGroupData {
  label: string
  collapsed?: boolean
  doneCount: number
  totalCount: number
  allDone: boolean
  archived: boolean
  compact: boolean
  [key: string]: unknown
}

/** The coordinator's changes that still stand, newest first. */
function standing(plan: PlanView | null): PlanRevisionView[] {
  return (plan?.revisions ?? []).filter(
    (r) => r.author === 'coordinator' && !r.revertedBy && !r.revertOf,
  )
}

/** The newest change the coordinator made that still stands. */
export function latestCoordinatorChange(plan: PlanView | null) {
  return standing(plan)[0]
}

/** The nodes a change touches, including both ends of the dependencies it adds or drops. */
export function touchedBy(ops: readonly PlanOp[]): Set<string> {
  const ids = new Set<string>()
  for (const op of ops) {
    if (op.op === 'link' || op.op === 'unlink') {
      ids.add(op.from)
      ids.add(op.to)
    } else ids.add(op.id)
  }
  return ids
}

/**
 * The coordinator's changes the user has not seen yet: those newer than `seen`, the
 * newest revision when the user last caught up. `nodes` maps each node they touch to the
 * newest such revision, so a node the user looked at since stops standing out. The plan
 * lists only its latest revisions; when `seen` is not among them, `truncated` says that
 * older ones are missing.
 */
export function freshChanges(plan: PlanView | null, seen: string | null) {
  const revisions: PlanRevisionView[] = []
  let found = false
  for (const revision of plan?.revisions ?? []) {
    if (revision.id === seen) {
      found = true
      break
    }
    if (revision.author === 'coordinator' && !revision.revertedBy && !revision.revertOf) {
      revisions.push(revision)
    }
  }
  // Only nodes still on the plan: one removed since cannot be looked at.
  const present = new Set(plan?.nodes.map((node) => node.id))
  const nodes = new Map<string, string>()
  for (const revision of revisions) {
    for (const id of touchedBy(revision.ops)) {
      if (present.has(id) && !nodes.has(id)) nodes.set(id, revision.id)
    }
  }
  return { revisions, nodes, truncated: !!seen && !found && revisions.length > 0 }
}

export interface PlanCompletionSummary {
  /** Nodes belonging to an active (not-all-done) group or connected subgraph. */
  active: PlanNodeView[]
  /** IDs of nodes in fully completed lone nodes, groups, or connected subgraphs. */
  completedIds: Set<string>
}

/**
 * Partition plan nodes into active work and auto-completed items.
 * Two nodes belong to the same completion unit if they share a non-empty `group` OR are
 * linked by a dependency edge. A unit is completed only when every node in it is `done`,
 * so partially completed groups and dependency chains keep their full context visible.
 */
export function partitionPlanNodes(
  nodes: PlanNodeView[],
  edges: { from: string; to: string }[] = nodes.flatMap((node) =>
    node.dependsOn.map((from) => ({ from, to: node.id })),
  ),
): PlanCompletionSummary {
  const root = new Map(nodes.map((node) => [node.id, node.id]))
  const find = (id: string): string => {
    let at = id
    while (root.get(at) !== at) at = root.get(at)!
    root.set(id, at)
    return at
  }
  const unite = (a: string, b: string) => {
    const ra = find(a)
    const rb = find(b)
    if (ra !== rb) root.set(ra, rb)
  }
  for (const { from, to } of edges) {
    if (root.has(from) && root.has(to)) unite(from, to)
  }
  const groupLeader = new Map<string, string>()
  for (const node of nodes) {
    const group = groupNameOf(node)
    if (!group) continue
    const leader = groupLeader.get(group)
    if (leader) unite(leader, node.id)
    else groupLeader.set(group, node.id)
  }
  const units = new Map<string, PlanNodeView[]>()
  for (const node of nodes) {
    const key = find(node.id)
    const list = units.get(key)
    if (list) list.push(node)
    else units.set(key, [node])
  }
  const completedIds = new Set<string>()
  for (const unit of units.values()) {
    if (unit.length && unit.every((node) => node.status === 'done')) {
      for (const node of unit) completedIds.add(node.id)
    }
  }
  const active = nodes.filter((node) => !completedIds.has(node.id))
  return { active, completedIds }
}

/** How wide a title runs at the node's text size: CJK and other wide characters count double. */
function textWidth(text: string): number {
  let width = 0
  for (const char of text) width += (char.codePointAt(0) ?? 0) > 0x2e80 ? 14 : 7.5
  return width
}

/**
 * About how tall a node is drawn: the kind line, the title wrapped to the node's width,
 * and up to two threads, each with the line it waits on the user for, plus a "more" line.
 * Only used to keep nodes apart, so a little over is fine.
 */
export function nodeHeight(node: Pick<PlanNodeView, 'title' | 'threads'>, compact = false): number {
  if (compact) return 20 + Math.max(1, Math.ceil(textWidth(node.title) / (COMPACT_WIDTH - 44))) * 20
  const lines = Math.max(1, Math.ceil(textWidth(node.title) / (NODE_WIDTH - 24)))
  const threads = Math.min(node.threads.length, 2) + (node.threads.length > 2 ? 1 : 0)
  const headlines = node.threads
    .slice(0, 2)
    .filter((thread) => thread.lastHeadline && showsHeadline(thread.stage)).length
  return 44 + lines * 22 + (threads ? 10 + threads * 22 : 0) + headlines * 18
}

/** Connected components of items by undirected edges, in input order. */
function connectedParts(ids: string[], edges: { from: string; to: string }[]): string[][] {
  const root = new Map(ids.map((id) => [id, id]))
  const find = (id: string): string => {
    let at = id
    while (root.get(at) !== at) at = root.get(at)!
    root.set(id, at)
    return at
  }
  for (const { from, to } of edges) {
    if (!root.has(from) || !root.has(to)) continue
    root.set(find(from), find(to))
  }
  const groups = new Map<string, string[]>()
  for (const id of ids) {
    const key = find(id)
    const list = groups.get(key)
    if (list) list.push(id)
    else groups.set(key, [id])
  }
  return [...groups.values()]
}

interface Block {
  width: number
  height: number
  /** Item positions inside the block, from its top left corner. */
  nodes: Map<string, { x: number; y: number }>
  /** Where long edges pass between the items in their way, keyed `from>to`. */
  edges: Map<string, { x: number; y: number }[]>
}

export interface GroupLayout {
  id: string
  label: string
  x: number
  y: number
  width: number
  height: number
  nodeIds: string[]
}

export interface PlanLayout {
  nodes: Map<string, { x: number; y: number }>
  groups: GroupLayout[]
  /** The points an edge spanning several levels runs through, keyed `from>to`. */
  edges: Map<string, { x: number; y: number }[]>
}

/** Lay out a flat subset of plan nodes (such as the members of one group) into a compact block. */
function layoutSubset(
  nodes: PlanNodeView[],
  edges: { from: string; to: string }[],
  nodeWidth: number,
  heightOfNode: (node: PlanNodeView) => number,
  compact: boolean,
): Block {
  const byId = new Map(nodes.map((node) => [node.id, node]))
  const blocks: Block[] = []
  const lone: string[] = []
  for (const ids of connectedParts(
    nodes.map((node) => node.id),
    edges,
  )) {
    if (ids.length === 1) {
      lone.push(ids[0]!)
      continue
    }
    const inside = new Set(ids)
    const layout = layoutGraph(
      ids.map((id) => ({
        id,
        width: nodeWidth,
        height: heightOfNode(byId.get(id)!),
        anchor: nodeWidth / 2,
      })),
      edges.filter((edge) => inside.has(edge.from) && inside.has(edge.to)),
      {
        direction: 'vertical',
        rankGap: RANK_GAP,
        laneGap: LANE_GAP,
        equalWidth: false,
        straightChains: true,
        grid: 8,
      },
    )
    const placed = new Map([...layout.nodes].map(([id, node]) => [id, { x: node.x, y: node.y }]))
    const routed = new Map(
      layout.edges
        .filter((edge) => edge.points.length)
        .map((edge) => [`${edge.from}>${edge.to}`, edge.points]),
    )
    blocks.push({ width: layout.width, height: layout.height, nodes: placed, edges: routed })
  }

  if (lone.length) {
    const maxCols = compact ? 2 : 3
    const columns =
      lone.length <= 3 ? Math.min(lone.length, maxCols) : lone.length === 4 ? 2 : maxCols
    const placed = new Map<string, { x: number; y: number }>()
    let y = 0
    for (let i = 0; i < lone.length; i += columns) {
      const row = lone.slice(i, i + columns)
      row.forEach((id, k) => placed.set(id, { x: k * (nodeWidth + LANE_GAP), y }))
      y += Math.max(...row.map((id) => heightOfNode(byId.get(id)!))) + LANE_GAP
    }
    const width = Math.min(lone.length, columns) * (nodeWidth + LANE_GAP) - LANE_GAP
    blocks.push({ width, height: Math.max(0, y - LANE_GAP), nodes: placed, edges: new Map() })
  }

  if (blocks.length === 1) return blocks[0]!

  const gap = compact ? COMPACT_INNER_PART_GAP : INNER_PART_GAP
  const area = blocks.reduce((sum, block) => sum + block.width * block.height, 0)
  const rowWidth = Math.max(nodeWidth, ...blocks.map((block) => block.width), Math.sqrt(area))
  const positions = new Map<string, { x: number; y: number }>()
  const routes = new Map<string, { x: number; y: number }[]>()
  let x = 0
  let y = 0
  let rowHeight = 0
  let totalWidth = 0
  for (const block of blocks) {
    if (x > 0 && x + block.width > rowWidth) {
      totalWidth = Math.max(totalWidth, x - gap)
      x = 0
      y += rowHeight + gap
      rowHeight = 0
    }
    for (const [id, at] of block.nodes) {
      positions.set(id, { x: Math.round(x + at.x), y: Math.round(y + at.y) })
    }
    for (const [key, points] of block.edges) {
      routes.set(
        key,
        points.map((point) => ({ x: Math.round(x + point.x), y: Math.round(y + point.y) })),
      )
    }
    x += block.width + gap
    rowHeight = Math.max(rowHeight, block.height)
  }
  totalWidth = Math.max(totalWidth, x - gap)
  return {
    width: Math.max(0, totalWidth),
    height: Math.max(0, y + rowHeight),
    nodes: positions,
    edges: routes,
  }
}

/** Check whether `start` reaches `target` in a directed adjacency map. */
function reaches(adj: Map<string, Set<string>>, start: string, target: string): boolean {
  const seen = new Set<string>()
  const stack = [start]
  while (stack.length) {
    const current = stack.pop()!
    if (current === target) return true
    if (seen.has(current)) continue
    seen.add(current)
    for (const next of adj.get(current) ?? []) stack.push(next)
  }
  return false
}

/**
 * Lay the graph out top to bottom in dependency levels (a node sits below everything it
 * depends on), ordered to cross as few edges as possible. Nodes sharing a `group` are laid
 * out together inside a titled container box, and groups participate in the outer dependency
 * layout as compound blocks; unrelated parts are placed side by side in balanced rows, and
 * ungrouped lone nodes come last in a grid.
 */
export function layoutPlan(
  nodes: PlanNodeView[],
  edges: { from: string; to: string }[] = nodes.flatMap((node) =>
    node.dependsOn.map((from) => ({ from, to: node.id })),
  ),
  compact = false,
): PlanLayout {
  const nodeWidth = compact ? COMPACT_WIDTH : NODE_WIDTH
  const heightOfNode = (node: PlanNodeView) => nodeHeight(node, compact)
  const pad = compact ? COMPACT_GROUP_PAD : GROUP_PAD
  const partGap = compact ? COMPACT_PART_GAP : PART_GAP

  // 1. Build inner layouts for every named group, preserving first-seen order.
  const groupedMembers = new Map<string, PlanNodeView[]>()
  for (const node of nodes) {
    const group = groupNameOf(node)
    if (!group) continue
    const list = groupedMembers.get(group)
    if (list) list.push(node)
    else groupedMembers.set(group, [node])
  }

  interface MacroGroup {
    id: string
    label: string
    width: number
    height: number
    extraX: number
    nodeIds: string[]
    inner: Block
  }
  const macroGroups = new Map<string, MacroGroup>()
  const macroIdOf = new Map<string, string>()

  for (const [label, members] of groupedMembers) {
    const id = groupIdOf(label)
    const inside = new Set(members.map((node) => node.id))
    const innerEdges = edges.filter((edge) => inside.has(edge.from) && inside.has(edge.to))
    const inner = layoutSubset(members, innerEdges, nodeWidth, heightOfNode, compact)
    const titleWidth = Math.min(compact ? 260 : 360, Math.ceil(textWidth(label) + 96))
    const minWidth = Math.max(compact ? 200 : 280, titleWidth)
    const width = Math.max(minWidth, inner.width + pad.side * 2)
    const height = inner.height + pad.top + pad.bottom
    const extraX = Math.round((width - pad.side * 2 - inner.width) / 2)
    macroGroups.set(id, {
      id,
      label,
      width,
      height,
      extraX,
      nodeIds: members.map((node) => node.id),
      inner,
    })
    for (const member of members) macroIdOf.set(member.id, id)
  }

  // 2. Build outer macro-items (one per group, one per ungrouped node) in plan order.
  interface MacroItem {
    id: string
    width: number
    height: number
    isGroup: boolean
  }
  const macroItems: MacroItem[] = []
  const macroById = new Map<string, MacroItem>()
  const addedGroup = new Set<string>()
  for (const node of nodes) {
    const gid = macroIdOf.get(node.id)
    if (gid) {
      if (addedGroup.has(gid)) continue
      addedGroup.add(gid)
      const g = macroGroups.get(gid)!
      const item: MacroItem = { id: gid, width: g.width, height: g.height, isGroup: true }
      macroItems.push(item)
      macroById.set(gid, item)
    } else {
      macroIdOf.set(node.id, node.id)
      const item: MacroItem = {
        id: node.id,
        width: nodeWidth,
        height: heightOfNode(node),
        isGroup: false,
      }
      macroItems.push(item)
      macroById.set(node.id, item)
    }
  }

  // Deduplicated acyclic macro-edges between distinct macro-items.
  const macroEdges: { from: string; to: string }[] = []
  const macroAdj = new Map<string, Set<string>>()
  const seenMacroEdge = new Set<string>()
  for (const edge of edges) {
    const from = macroIdOf.get(edge.from)
    const to = macroIdOf.get(edge.to)
    if (!from || !to || from === to) continue
    const key = `${from}>${to}`
    if (seenMacroEdge.has(key)) continue
    // Skip any reverse macro-edge that would introduce a cycle at the group level.
    if (reaches(macroAdj, to, from)) continue
    seenMacroEdge.add(key)
    macroEdges.push({ from, to })
    const set = macroAdj.get(from)
    if (set) set.add(to)
    else macroAdj.set(from, new Set([to]))
  }

  // 3. Lay out each macro-component; standalone groups stay as their own blocks, lone ungrouped nodes form a grid.
  const blocks: Block[] = []
  const lone: MacroItem[] = []
  for (const ids of connectedParts(
    macroItems.map((item) => item.id),
    macroEdges,
  )) {
    if (ids.length === 1) {
      const item = macroById.get(ids[0]!)!
      if (item.isGroup) {
        blocks.push({
          width: item.width,
          height: item.height,
          nodes: new Map([[item.id, { x: 0, y: 0 }]]),
          edges: new Map(),
        })
      } else {
        lone.push(item)
      }
      continue
    }
    const inside = new Set(ids)
    const layout = layoutGraph(
      ids.map((id) => {
        const item = macroById.get(id)!
        return {
          id,
          width: item.width,
          height: item.height,
          anchor: item.width / 2,
        }
      }),
      macroEdges.filter((edge) => inside.has(edge.from) && inside.has(edge.to)),
      {
        direction: 'vertical',
        rankGap: RANK_GAP,
        laneGap: LANE_GAP,
        equalWidth: false,
        straightChains: true,
        grid: 8,
      },
    )
    const placed = new Map([...layout.nodes].map(([id, node]) => [id, { x: node.x, y: node.y }]))
    const routed = new Map(
      layout.edges
        .filter(
          (edge) => edge.points.length && !macroGroups.has(edge.from) && !macroGroups.has(edge.to),
        )
        .map((edge) => [`${edge.from}>${edge.to}`, edge.points]),
    )
    blocks.push({ width: layout.width, height: layout.height, nodes: placed, edges: routed })
  }

  const area =
    blocks.reduce((sum, block) => sum + block.width * block.height, 0) +
    lone.reduce((sum, item) => sum + (nodeWidth + LANE_GAP) * (item.height + RANK_GAP), 0)
  const rowWidth = Math.max(
    nodeWidth,
    ...blocks.map((block) => block.width),
    Math.sqrt(area * (compact ? 1 : 1.6)),
  )

  if (lone.length) {
    const columns = Math.max(1, Math.floor((rowWidth + LANE_GAP) / (nodeWidth + LANE_GAP)))
    const placed = new Map<string, { x: number; y: number }>()
    let y = 0
    for (let i = 0; i < lone.length; i += columns) {
      const row = lone.slice(i, i + columns)
      row.forEach((item, k) => placed.set(item.id, { x: k * (nodeWidth + LANE_GAP), y }))
      y += Math.max(...row.map((item) => item.height)) + LANE_GAP
    }
    const width = Math.min(lone.length, columns) * (nodeWidth + LANE_GAP) - LANE_GAP
    blocks.push({ width, height: y - LANE_GAP, nodes: placed, edges: new Map() })
  }

  // 4. Pack blocks into rows and expand each group macro-item into container + world child coordinates.
  const positions = new Map<string, { x: number; y: number }>()
  const groups: GroupLayout[] = []
  const routes = new Map<string, { x: number; y: number }[]>()
  let x = 0
  let y = 0
  let rowHeight = 0
  for (const block of blocks) {
    if (x > 0 && x + block.width > rowWidth) {
      x = 0
      y += rowHeight + partGap
      rowHeight = 0
    }
    for (const [id, at] of block.nodes) {
      const gx = Math.round(x + at.x)
      const gy = Math.round(y + at.y)
      const group = macroGroups.get(id)
      if (group) {
        groups.push({
          id: group.id,
          label: group.label,
          x: gx,
          y: gy,
          width: group.width,
          height: group.height,
          nodeIds: group.nodeIds,
        })
        const ox = gx + pad.side + group.extraX
        const oy = gy + pad.top
        for (const [childId, childAt] of group.inner.nodes) {
          positions.set(childId, { x: Math.round(ox + childAt.x), y: Math.round(oy + childAt.y) })
        }
        for (const [key, points] of group.inner.edges) {
          routes.set(
            key,
            points.map((point) => ({ x: Math.round(ox + point.x), y: Math.round(oy + point.y) })),
          )
        }
      } else {
        positions.set(id, { x: gx, y: gy })
      }
    }
    for (const [key, points] of block.edges) {
      routes.set(
        key,
        points.map((point) => ({ x: Math.round(x + point.x), y: Math.round(y + point.y) })),
      )
    }
    x += block.width + partGap
    rowHeight = Math.max(rowHeight, block.height)
  }
  return { nodes: positions, groups, edges: routes }
}

export interface GraphOptions {
  /** A change to show on the graph instead of the plan as it is, as the server drew it. */
  preview?: PlanPreview | null
  fresh?: Set<string>
  proposed?: Set<string>
  /** Drawn for a narrow pane: small nodes, tighter group containers. */
  compact?: boolean
  /** Whether to include fully completed lone nodes, groups, and subgraphs. Defaults to false. */
  showCompleted?: boolean
  /** Visual group container IDs folded locally by the user. */
  collapsedGroups?: ReadonlySet<string>
}

/**
 * For each node work started on, or that is done, its dependencies not done yet, by the
 * dependency they hold it up through: the plan meant it to wait for them. A milestone the
 * user let pass counts as done; one nobody worked on, like an open decision, is looked
 * through to what comes before it, as work after it says it was passed.
 */
function aheadOf(nodes: PlanNodeView[]): Map<string, Map<string, string[]>> {
  const byId = new Map(nodes.map((node) => [node.id, node]))
  const open = (id: string, seen: Set<string>): string[] => {
    const dep = byId.get(id)
    if (!dep || dep.status === 'done' || seen.has(id)) return []
    seen.add(id)
    if (dep.kind === 'task' || dep.status !== 'none') return [dep.title]
    return dep.dependsOn.flatMap((from) => open(from, seen))
  }
  const ahead = new Map<string, Map<string, string[]>>()
  for (const node of nodes) {
    if (node.status === 'none') continue
    const through = new Map<string, string[]>()
    for (const id of node.dependsOn) {
      const titles = open(id, new Set())
      if (titles.length) through.set(id, titles)
    }
    if (through.size) ahead.set(node.id, through)
  }
  return ahead
}

/** For each node work started on too early, the titles of the dependencies not done. */
export function startedAhead(nodes: PlanNodeView[]): Map<string, string[]> {
  return new Map(
    [...aheadOf(nodes)].map(([id, through]) => [id, [...new Set([...through.values()].flat())]]),
  )
}

export function toFlowDocument(plan: PlanView | null, options: GraphOptions = {}): FlowDocument {
  const { preview } = options
  const current = new Map((plan?.nodes ?? []).map((node) => [node.id, node]))
  // A previewed node keeps the threads and status of the node it is now, if any.
  const allNodes: (PlanNodeView & { mark: PlanMark })[] = preview
    ? preview.nodes.map((node) => ({
        ...node,
        status: current.get(node.id)?.status ?? 'none',
        threads: current.get(node.id)?.threads ?? [],
        issues: current.get(node.id)?.issues ?? [],
        gate: current.get(node.id)?.gate ?? null,
        changedIn: null,
      }))
    : (plan?.nodes ?? []).map((node) => ({ ...node, mark: null }))
  const allEdges: { from: string; to: string; mark: PlanMark }[] =
    preview?.edges ??
    allNodes.flatMap((node) => node.dependsOn.map((from) => ({ from, to: node.id, mark: null })))

  const { completedIds } = preview
    ? { completedIds: new Set<string>() }
    : partitionPlanNodes(allNodes, allEdges)
  const nodes =
    preview || options.showCompleted
      ? allNodes
      : allNodes.filter((node) => !completedIds.has(node.id))
  const visibleIds = new Set(nodes.map((node) => node.id))
  const edges = allEdges.filter((edge) => visibleIds.has(edge.from) && visibleIds.has(edge.to))

  const compact = !!options.compact
  const auto = layoutPlan(nodes, edges, compact)
  const byId = new Map(nodes.map((node) => [node.id, node]))
  const through = preview ? new Map<string, Map<string, string[]>>() : aheadOf(nodes)
  // An edge from a dependency not done into work already under way.
  const skipped = (from: string, to: string) => !!through.get(to)?.has(from)

  const parentOf = new Map<string, string>()
  const groupNodes: FlowNode[] = auto.groups.map((group) => {
    for (const nodeId of group.nodeIds) parentOf.set(nodeId, group.id)
    const doneCount = group.nodeIds.filter((id) => byId.get(id)?.status === 'done').length
    const totalCount = group.nodeIds.length
    const allDone = totalCount > 0 && doneCount === totalCount
    const archived = !preview && group.nodeIds.every((id) => completedIds.has(id))
    const data: PlanGroupData = {
      label: group.label,
      collapsed: options.collapsedGroups?.has(group.id) || undefined,
      doneCount,
      totalCount,
      allDone,
      archived,
      compact,
    }
    return {
      id: group.id,
      type: 'group',
      x: group.x,
      y: group.y,
      width: group.width,
      height: group.height,
      data,
    }
  })

  const planNodes: FlowNode[] = nodes.map((node) => {
    const at = auto.nodes.get(node.id) ?? { x: 0, y: 0 }
    const parent = parentOf.get(node.id)
    const data: PlanNodeData = {
      title: node.title,
      kind: node.kind,
      description: node.description,
      acceptance: node.acceptance,
      group: groupNameOf(node),
      status: node.status,
      threads: node.threads,
      mark: node.mark,
      fresh: !preview && !!options.fresh?.has(node.id),
      proposed: !preview && !!options.proposed?.has(node.id),
      compact,
      archived: !preview && completedIds.has(node.id),
      ahead: [...new Set([...(through.get(node.id)?.values() ?? [])].flat())],
    }
    return {
      id: node.id,
      type: 'plan',
      x: at.x,
      y: at.y,
      ...(parent ? { parent } : {}),
      data,
    }
  })

  return {
    version: 1,
    nodes: [...groupNodes, ...planNodes],
    edges: edges.map(({ from, to, mark }) => {
      const points = auto.edges.get(`${from}>${to}`)
      return {
        id: `${from}>${to}`,
        source: { node: from, port: 'out' },
        target: { node: to, port: 'in' },
        data: { mark, skipped: !mark && skipped(from, to) },
        ...(points ? { points } : {}),
        ...(mark === 'added' ? { animated: true } : {}),
      }
    }),
  }
}
