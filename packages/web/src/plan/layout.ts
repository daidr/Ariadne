import type {
  PlanNodeKind,
  PlanNodeStatus,
  PlanNodeView,
  PlanOp,
  PlanRevisionView,
  PlanView,
} from '@ariadne/core'
import { applyOps, graphOf } from '@ariadne/core/plan'
import type { FlowDocument } from '@dou-ui/flow'

/** Space between dependency levels (top to bottom) and between nodes on one level. */
const LEVEL = 170
const SPREAD = 284

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
 * revision that was newest when they last said "got it".
 */
export function freshChanges(plan: PlanView | null, seen: string | null) {
  const revisions: PlanRevisionView[] = []
  for (const revision of plan?.revisions ?? []) {
    if (revision.id === seen) break
    if (revision.author === 'coordinator' && !revision.revertedBy && !revision.revertOf) {
      revisions.push(revision)
    }
  }
  const nodes = new Set<string>()
  for (const revision of revisions) for (const id of touchedBy(revision.ops)) nodes.add(id)
  return { revisions, nodes }
}

export interface PlanPreview {
  nodes: PlanNodeView[]
  marks: Map<string, PlanMark>
  edges: { from: string; to: string; mark: PlanMark }[]
  /** Why the change cannot apply to the plan as it is now, if it cannot. */
  problem: string | null
}

/** The plan with a change applied, every node and edge marked by what the change does. */
export function previewOps(plan: PlanView, ops: readonly PlanOp[]): PlanPreview {
  const before = graphOf(plan.nodes)
  const current = (from: string, to: string) => before.edges.has(`${from}>${to}`)
  let after
  try {
    after = applyOps(before, [...ops]).graph
  } catch (error) {
    return {
      nodes: plan.nodes,
      marks: new Map(),
      edges: [],
      // `error.planInvalid` names the op that no longer fits in its `reason`.
      problem: String(
        (error as { params?: { reason?: unknown } }).params?.reason ??
          (error instanceof Error ? error.message : error),
      ),
    }
  }
  const marks = new Map<string, PlanMark>()
  const byId = new Map(plan.nodes.map((node) => [node.id, node]))
  const nodes: PlanNodeView[] = []
  for (const id of new Set([...before.order, ...after.order])) {
    const was = before.nodes.get(id)
    const now = after.nodes.get(id)
    const dependsOn = [...after.edges, ...before.edges]
      .map((key) => key.split('>'))
      .filter(([, to]) => to === id)
      .map(([from]) => from as string)
    const base = byId.get(id)
    nodes.push({
      id,
      ...(now ?? was)!,
      dependsOn: [...new Set(dependsOn)],
      status: base?.status ?? 'none',
      threads: base?.threads ?? [],
      changedIn: base?.changedIn ?? null,
    })
    if (!was) marks.set(id, 'added')
    else if (!now) marks.set(id, 'removed')
    else if (
      was.title !== now.title ||
      was.description !== now.description ||
      was.acceptance !== now.acceptance
    ) {
      marks.set(id, 'changed')
    }
  }
  const edges = [...new Set([...before.edges, ...after.edges])].map((key) => {
    const [from = '', to = ''] = key.split('>')
    const mark: PlanMark = !current(from, to) ? 'added' : after.edges.has(key) ? null : 'removed'
    return { from, to, mark }
  })
  return { nodes, marks, edges, problem: null }
}

/**
 * Lay the graph out top to bottom by dependency depth (a node sits below everything it
 * depends on), keeping the planned order inside each level. Top to bottom suits the tall,
 * narrow panels the plan is shown in.
 */
export function layoutPlan(nodes: PlanNodeView[]): Map<string, { x: number; y: number }> {
  const byId = new Map(nodes.map((node) => [node.id, node]))
  const depth = new Map<string, number>()
  const depthOf = (id: string, seen: Set<string>): number => {
    const known = depth.get(id)
    if (known !== undefined) return known
    if (seen.has(id)) return 0
    seen.add(id)
    const deps = byId.get(id)?.dependsOn ?? []
    const value = deps.length ? Math.max(...deps.map((dep) => depthOf(dep, seen) + 1)) : 0
    depth.set(id, value)
    return value
  }
  const used = new Map<number, number>()
  const positions = new Map<string, { x: number; y: number }>()
  for (const node of nodes) {
    const level = depthOf(node.id, new Set())
    const index = used.get(level) ?? 0
    used.set(level, index + 1)
    positions.set(node.id, { x: index * SPREAD, y: level * LEVEL })
  }
  return positions
}

export interface GraphOptions {
  /** Where the user moved nodes; others are laid out by dependency. */
  positions?: Positions
  /** A change to show on the graph instead of the plan as it is. */
  preview?: PlanPreview | null
  fresh?: Set<string>
  proposed?: Set<string>
}

export function toFlowDocument(plan: PlanView | null, options: GraphOptions = {}): FlowDocument {
  const preview = options.preview && !options.preview.problem ? options.preview : null
  const nodes = preview?.nodes ?? plan?.nodes ?? []
  const auto = layoutPlan(nodes)
  const edges =
    preview?.edges ??
    nodes.flatMap((node) => node.dependsOn.map((from) => ({ from, to: node.id, mark: null })))
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
        mark: preview?.marks.get(node.id) ?? null,
        fresh: !preview && !!options.fresh?.has(node.id),
        proposed: !preview && !!options.proposed?.has(node.id),
      }
      return {
        id: node.id,
        type: 'plan',
        ...(options.positions?.[node.id] ?? auto.get(node.id) ?? { x: 0, y: 0 }),
        data,
      }
    }),
    edges: edges.map(({ from, to, mark }) => ({
      id: `${from}>${to}`,
      source: { node: from, port: 'out' },
      target: { node: to, port: 'in' },
      data: { mark },
      ...(mark === 'added' ? { animated: true } : {}),
    })),
  }
}
