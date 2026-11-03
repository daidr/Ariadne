import type { PlanNodeView, PlanView } from '@ariadne/core'
import type { FlowDocument } from '@dou-ui/flow'

/** Space between dependency levels (top to bottom) and between nodes on one level. */
const LEVEL = 120
const SPREAD = 232

/** The newest change the coordinator made that still stands; its nodes are highlighted. */
export function latestCoordinatorChange(plan: PlanView | null) {
  return plan?.revisions.find((r) => r.author === 'coordinator' && !r.revertedBy && !r.revertOf)
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

export function toFlowDocument(plan: PlanView | null): FlowDocument {
  const nodes = plan?.nodes ?? []
  const positions = layoutPlan(nodes)
  const highlight = latestCoordinatorChange(plan)?.id
  return {
    version: 1,
    nodes: nodes.map((node) => ({
      id: node.id,
      type: 'plan',
      ...(positions.get(node.id) ?? { x: 0, y: 0 }),
      data: {
        title: node.title,
        kind: node.kind,
        status: node.status,
        threads: node.threads.length,
        changed: !!highlight && node.changedIn === highlight,
      },
    })),
    edges: nodes.flatMap((node) =>
      node.dependsOn.map((from) => ({
        id: `${from}>${node.id}`,
        source: { node: from, port: 'out' },
        target: { node: node.id, port: 'in' },
      })),
    ),
  }
}
