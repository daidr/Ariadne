import type {
  PlanNodeKind,
  PlanNodeStatus,
  PlanNodeView,
  PlanOp,
  PlanPreview,
  PlanRevisionView,
  PlanView,
} from '@ariadne/core'
import type { FlowDocument } from '@dou-ui/flow'
import { layoutGraph } from 'dou-ui'
import { showsHeadline } from '../stage'

/** Room between dependency levels, between nodes on one level, and between unrelated parts. */
const RANK_GAP = 56
const LANE_GAP = 32
const PART_GAP = 96
/**
 * Plan nodes are drawn this wide (PlanNode.vue: 248px of text plus 12px padding each side);
 * their height is estimated from what they show.
 */
export const NODE_WIDTH = 272
const WIDE = NODE_WIDTH
/** In a narrow pane nodes show their status and title only, this wide (PlanNode.vue). */
export const COMPACT_WIDTH = 176

/** How a node or edge stands in a change shown on the graph. */
export type PlanMark = 'added' | 'removed' | 'changed' | null

export interface PlanNodeData {
  title: string
  kind: PlanNodeKind
  description: string
  acceptance: string
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
  /** Its dependencies that are not done, by title, while work on it started anyway. */
  ahead: string[]
  [key: string]: unknown
}

export type Positions = Record<string, { x: number; y: number }>

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

const heightOf = nodeHeight

/** Groups of nodes linked to each other through dependencies, in planned order. */
function parts(nodes: PlanNodeView[], edges: { from: string; to: string }[]): string[][] {
  const root = new Map(nodes.map((node) => [node.id, node.id]))
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
  for (const node of nodes) {
    const key = find(node.id)
    groups.set(key, [...(groups.get(key) ?? []), node.id])
  }
  return [...groups.values()]
}

interface Block {
  width: number
  height: number
  /** Node positions inside the block, from its top left corner. */
  nodes: Map<string, { x: number; y: number }>
  /** Where long edges pass between the nodes in their way, keyed `from>to`. */
  edges: Map<string, { x: number; y: number }[]>
}

export interface PlanLayout {
  nodes: Map<string, { x: number; y: number }>
  /** The points an edge spanning several levels runs through, keyed `from>to`. */
  edges: Map<string, { x: number; y: number }[]>
}

/**
 * Lay the graph out top to bottom in dependency levels (a node sits below everything it
 * depends on), ordered to cross as few edges as possible. Parts of the plan that don't
 * depend on each other are laid out apart and placed side by side, wrapping into rows so
 * the whole stays about as wide as it is tall; lone nodes come last, in a grid.
 */
export function layoutPlan(
  nodes: PlanNodeView[],
  edges: { from: string; to: string }[] = nodes.flatMap((node) =>
    node.dependsOn.map((from) => ({ from, to: node.id })),
  ),
  compact = false,
): PlanLayout {
  const nodeWidth = compact ? COMPACT_WIDTH : WIDE
  const heightOfNode = (node: PlanNodeView) => heightOf(node, compact)
  const byId = new Map(nodes.map((node) => [node.id, node]))
  const blocks: Block[] = []
  const lone: string[] = []
  for (const ids of parts(nodes, edges)) {
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
        // Ports sit in the middle of the top and bottom edges.
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

  const area =
    blocks.reduce((sum, block) => sum + block.width * block.height, 0) +
    lone.reduce(
      (sum, id) => sum + (nodeWidth + LANE_GAP) * (heightOfNode(byId.get(id)!) + RANK_GAP),
      0,
    )
  const rowWidth = Math.max(nodeWidth, ...blocks.map((block) => block.width), Math.sqrt(area))

  if (lone.length) {
    const columns = Math.max(1, Math.floor((rowWidth + LANE_GAP) / (nodeWidth + LANE_GAP)))
    const placed = new Map<string, { x: number; y: number }>()
    let y = 0
    for (let i = 0; i < lone.length; i += columns) {
      const row = lone.slice(i, i + columns)
      row.forEach((id, k) => placed.set(id, { x: k * (nodeWidth + LANE_GAP), y }))
      y += Math.max(...row.map((id) => heightOfNode(byId.get(id)!))) + LANE_GAP
    }
    const width = Math.min(lone.length, columns) * (nodeWidth + LANE_GAP) - LANE_GAP
    blocks.push({ width, height: y - LANE_GAP, nodes: placed, edges: new Map() })
  }

  // Rows of blocks, each as wide as the widest block or the square root of the whole.
  const positions = new Map<string, { x: number; y: number }>()
  const routes = new Map<string, { x: number; y: number }[]>()
  let x = 0
  let y = 0
  let rowHeight = 0
  for (const block of blocks) {
    if (x > 0 && x + block.width > rowWidth) {
      x = 0
      y += rowHeight + PART_GAP
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
    x += block.width + PART_GAP
    rowHeight = Math.max(rowHeight, block.height)
  }
  return { nodes: positions, edges: routes }
}

function place(auto: { x: number; y: number } | undefined, offset?: { x: number; y: number }) {
  const at = auto ?? { x: 0, y: 0 }
  return offset ? { x: at.x + offset.x, y: at.y + offset.y } : at
}

/**
 * Where nodes were moved to, as offsets from where the layout of `plan` puts them. A node
 * the layout doesn't have yet keeps its place as its offset, from the origin.
 */
export function offsetsOf(plan: PlanView, moved: Positions): Positions {
  const auto = layoutPlan(plan.nodes).nodes
  const offsets: Positions = {}
  for (const [id, at] of Object.entries(moved)) {
    const from = auto.get(id) ?? { x: 0, y: 0 }
    offsets[id] = { x: at.x - from.x, y: at.y - from.y }
  }
  return offsets
}

export interface GraphOptions {
  /** How far the user moved nodes from where the layout puts them. */
  offsets?: Positions
  /** A change to show on the graph instead of the plan as it is, as the server drew it. */
  preview?: PlanPreview | null
  fresh?: Set<string>
  proposed?: Set<string>
  /** Drawn for a narrow pane: small nodes, laid out as they come, without moved offsets. */
  compact?: boolean
}

/**
 * For each node work started on, or that is done, the dependencies not done yet: the plan
 * meant it to wait for them. A milestone the user let pass counts as done.
 */
export function startedAhead(nodes: PlanNodeView[]): Map<string, string[]> {
  const byId = new Map(nodes.map((node) => [node.id, node]))
  const ahead = new Map<string, string[]>()
  for (const node of nodes) {
    if (node.status === 'none') continue
    const open = node.dependsOn
      .map((id) => byId.get(id))
      .filter((dep): dep is PlanNodeView => !!dep && dep.status !== 'done')
    if (open.length)
      ahead.set(
        node.id,
        open.map((dep) => dep.title),
      )
  }
  return ahead
}

export function toFlowDocument(plan: PlanView | null, options: GraphOptions = {}): FlowDocument {
  const { preview } = options
  const current = new Map((plan?.nodes ?? []).map((node) => [node.id, node]))
  // A previewed node keeps the threads and status of the node it is now, if any.
  const nodes: (PlanNodeView & { mark: PlanMark })[] = preview
    ? preview.nodes.map((node) => ({
        ...node,
        status: current.get(node.id)?.status ?? 'none',
        threads: current.get(node.id)?.threads ?? [],
        issues: current.get(node.id)?.issues ?? [],
        gate: current.get(node.id)?.gate ?? null,
        changedIn: null,
      }))
    : (plan?.nodes ?? []).map((node) => ({ ...node, mark: null }))
  const edges: { from: string; to: string; mark: PlanMark }[] =
    preview?.edges ??
    nodes.flatMap((node) => node.dependsOn.map((from) => ({ from, to: node.id, mark: null })))
  const compact = !!options.compact
  const auto = layoutPlan(nodes, edges, compact)
  const offsets = compact ? {} : (options.offsets ?? {})
  const offset = (id: string) => offsets[id] ?? { x: 0, y: 0 }
  const ahead = preview ? new Map<string, string[]>() : startedAhead(nodes)
  const byId = new Map(nodes.map((node) => [node.id, node]))
  // An edge from a dependency not done into work already under way.
  const skipped = (from: string, to: string) => !!ahead.get(to) && byId.get(from)?.status !== 'done'
  /**
   * A long edge passes between the nodes in its way, as laid out. Once the user moved one
   * of its ends on its own the route no longer fits, and the edge goes straight there.
   */
  const route = (from: string, to: string) => {
    const points = auto.edges.get(`${from}>${to}`)
    const [a, b] = [offset(from), offset(to)]
    if (!points || a.x !== b.x || a.y !== b.y) return {}
    return { points: points.map((point) => ({ x: point.x + a.x, y: point.y + a.y })) }
  }
  return {
    version: 1,
    nodes: nodes.map((node) => {
      const data: PlanNodeData = {
        title: node.title,
        kind: node.kind,
        description: node.description,
        acceptance: node.acceptance,
        status: node.status,
        threads: node.threads,
        mark: node.mark,
        fresh: !preview && !!options.fresh?.has(node.id),
        proposed: !preview && !!options.proposed?.has(node.id),
        compact,
        ahead: ahead.get(node.id) ?? [],
      }
      return {
        id: node.id,
        type: 'plan',
        ...place(auto.nodes.get(node.id), offsets[node.id]),
        data,
      }
    }),
    edges: edges.map(({ from, to, mark }) => ({
      id: `${from}>${to}`,
      source: { node: from, port: 'out' },
      target: { node: to, port: 'in' },
      data: { mark, skipped: !mark && skipped(from, to) },
      ...route(from, to),
      ...(mark === 'added' ? { animated: true } : {}),
    })),
  }
}
