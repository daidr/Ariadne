import type { PlanNodeKind, PlanOp } from '@ariadne/core'
import type { MessageKey, MessageParams } from '@ariadne/i18n'
import type { FlowDocument, FlowEdge, FlowStep } from '@dou-ui/flow'
import type { Positions } from './layout'

const KINDS: readonly PlanNodeKind[] = ['task', 'milestone', 'decision']

export interface GraphEdit {
  /** What the edit changes in the plan, in the order it must run. */
  ops: PlanOp[]
  /** Nodes the user moved, to remember where they put them. */
  moved: Positions
}

const text = (value: unknown) => (typeof value === 'string' ? value : '')

/**
 * Turn what the user did on the graph into plan changes. Edges run from the port at the
 * bottom of a node to the top of the node that depends on it, so an edge is a `link` from
 * its source to its target. Removing a node drops its edges with it, so the edges the editor
 * removes alongside are not sent again.
 */
export function editOf(
  steps: readonly FlowStep[],
  before: FlowDocument,
  newTitle: string,
): GraphEdit {
  const removed = new Set(steps.flatMap((step) => (step.type === 'removeNode' ? [step.id] : [])))
  const edges = new Map<string, FlowEdge>(before.edges.map((edge) => [edge.id, edge]))
  const ops: PlanOp[] = []
  const moved: Positions = {}
  for (const step of steps) {
    switch (step.type) {
      case 'addNode': {
        const { node } = step
        const data = node.data ?? {}
        const kind = KINDS.find((k) => k === data.kind) ?? 'task'
        ops.push({
          op: 'add',
          id: node.id,
          kind,
          title: text(data.title).trim() || newTitle,
          description: text(data.description),
          acceptance: text(data.acceptance),
        })
        moved[node.id] = { x: node.x, y: node.y }
        break
      }
      case 'removeNode':
        ops.push({ op: 'remove', id: step.id })
        delete moved[step.id]
        break
      case 'updateNode': {
        const { x, y } = step.patch
        const was = before.nodes.find((node) => node.id === step.id)
        if (typeof x === 'number' || typeof y === 'number') {
          moved[step.id] = {
            x: typeof x === 'number' ? x : (moved[step.id]?.x ?? was?.x ?? 0),
            y: typeof y === 'number' ? y : (moved[step.id]?.y ?? was?.y ?? 0),
          }
        }
        break
      }
      case 'addEdge':
        edges.set(step.edge.id, step.edge)
        ops.push({ op: 'link', from: step.edge.source.node, to: step.edge.target.node })
        break
      case 'removeEdge': {
        const edge = edges.get(step.id)
        edges.delete(step.id)
        if (!edge || removed.has(edge.source.node) || removed.has(edge.target.node)) break
        ops.push({ op: 'unlink', from: edge.source.node, to: edge.target.node })
        break
      }
      case 'updateEdge': {
        // An edge dragged to another port: the old dependency goes, the new one comes.
        const edge = edges.get(step.id)
        if (!edge || (!step.patch.source && !step.patch.target)) break
        const next = { ...edge, ...step.patch } as FlowEdge
        edges.set(step.id, next)
        if (next.source.node === edge.source.node && next.target.node === edge.target.node) break
        ops.push({ op: 'unlink', from: edge.source.node, to: edge.target.node })
        ops.push({ op: 'link', from: next.source.node, to: next.target.node })
        break
      }
    }
  }
  return { ops, moved }
}

/**
 * Why the plan changed, as the revision records it and the coordinator reads it. One op
 * is named; several are counted.
 */
export function reasonOf(
  ops: readonly PlanOp[],
  titleOf: (id: string) => string,
): { key: MessageKey; params: MessageParams } {
  if (ops.length !== 1) return { key: 'plan.reason.many', params: { count: ops.length } }
  const [op] = ops as [PlanOp]
  switch (op.op) {
    case 'add':
      return { key: 'plan.reason.add', params: { title: op.title } }
    case 'remove':
      return { key: 'plan.reason.remove', params: { title: titleOf(op.id) } }
    case 'update':
      return { key: 'plan.reason.update', params: { title: titleOf(op.id) } }
    case 'link':
      return { key: 'plan.reason.link', params: { from: titleOf(op.from), to: titleOf(op.to) } }
    case 'unlink':
      return { key: 'plan.reason.unlink', params: { from: titleOf(op.from), to: titleOf(op.to) } }
  }
}
