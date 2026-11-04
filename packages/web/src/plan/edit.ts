import type { PlanNodeKind, PlanNodeView, PlanOp } from '@ariadne/core'
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

/**
 * Threads on a node that the user removing it stops: those not done, unless the user
 * stopped them already (a stop by the coordinator is taken over). The server's rule.
 */
export const liveThreads = (node: Pick<PlanNodeView, 'threads'>) =>
  node.threads.filter((thread) => thread.stage !== 'done' && thread.stoppedBy !== 'user').length

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

type Translate = (key: MessageKey, params?: MessageParams) => string

/** How many changes the reason names one by one before it only counts the rest. */
const NAMED = 4

/**
 * Why the plan changed, as the revision records it and the coordinator reads it: each
 * change named, so neither has to work out what "changed 3 things" meant.
 */
export function reasonOf(
  ops: readonly PlanOp[],
  titleOf: (id: string) => string,
  translate: Translate,
): string {
  const added = new Map(ops.flatMap((op) => (op.op === 'add' ? [[op.id, op.title]] : [])))
  const title = (id: string) => added.get(id) ?? titleOf(id)
  const line = (op: PlanOp) => {
    switch (op.op) {
      case 'add':
        return translate('plan.reason.add', { title: op.title })
      case 'remove':
        return translate('plan.reason.remove', { title: title(op.id) })
      case 'update':
        return translate('plan.reason.update', { title: title(op.id) })
      case 'link':
        return translate('plan.reason.link', { from: title(op.from), to: title(op.to) })
      case 'unlink':
        return translate('plan.reason.unlink', { from: title(op.from), to: title(op.to) })
    }
  }
  const named = ops.slice(0, NAMED).map(line).join(translate('plan.reason.join'))
  return ops.length > NAMED
    ? translate('plan.reason.more', { first: named, count: ops.length })
    : named
}

export type NodeFields = { title: string; description: string; acceptance: string }
export const NODE_FIELDS = ['title', 'description', 'acceptance'] as const

/**
 * Bring a node's newer values into an edit form. A field the user has not touched (still
 * as it was when they started) takes the newer value at once; a field they changed keeps
 * their text, and its newer value is left for them to weigh. Returns the new starting
 * point: the newer value for fields taken over, the old one for fields left to the user.
 */
export function mergeIntoForm(form: NodeFields, base: NodeFields, now: NodeFields): NodeFields {
  const next = { ...base }
  for (const field of NODE_FIELDS) {
    const mine = field === 'title' ? form.title.trim() : form[field]
    // Both sides wrote the same thing: nothing left to weigh.
    if (mine === now[field]) next[field] = now[field]
    if (mine !== base[field]) continue
    form[field] = now[field]
    next[field] = now[field]
  }
  return next
}
