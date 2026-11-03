import type { LocalizedMessage } from '@ariadne/i18n'
import { DomainError } from './errors'
import type { PlanNodeFields, PlanOp } from './events'

/** The plan graph of one project: nodes by id, and `from>to` dependency edges. */
export interface PlanGraph {
  nodes: Map<string, PlanNodeFields>
  /** Insertion order of nodes, so views list them as they were planned. */
  order: string[]
  edges: Set<string>
}

const edgeKey = (from: string, to: string) => `${from}>${to}`

export function emptyGraph(): PlanGraph {
  return { nodes: new Map(), order: [], edges: new Set() }
}

export function edgesOf(graph: PlanGraph): { from: string; to: string }[] {
  return [...graph.edges].map((key) => {
    const [from = '', to = ''] = key.split('>')
    return { from, to }
  })
}

function cloneGraph(graph: PlanGraph): PlanGraph {
  return {
    nodes: new Map([...graph.nodes].map(([id, node]) => [id, { ...node }])),
    order: [...graph.order],
    edges: new Set(graph.edges),
  }
}

function invalid(reason: string): never {
  throw new DomainError('error.planInvalid', { reason })
}

/** Whether `to` can already reach `from`, so a `from -> to` edge would close a cycle. */
function reaches(graph: PlanGraph, start: string, target: string): boolean {
  const seen = new Set<string>()
  const stack = [start]
  while (stack.length) {
    const current = stack.pop() as string
    if (current === target) return true
    if (seen.has(current)) continue
    seen.add(current)
    for (const { from, to } of edgesOf(graph)) if (from === current) stack.push(to)
  }
  return false
}

/**
 * Apply ops to a copy of the graph. Returns the new graph and the ops that undo the change,
 * in the order they must run. Throws `error.planInvalid` naming the first bad op.
 */
export function applyOps(graph: PlanGraph, ops: PlanOp[]): { graph: PlanGraph; inverse: PlanOp[] } {
  const next = cloneGraph(graph)
  const inverse: PlanOp[] = []
  ops.forEach((op, index) => {
    const where = `ops[${index}]`
    switch (op.op) {
      case 'add': {
        if (next.nodes.has(op.id)) invalid(`${where}: node ${op.id} exists`)
        if (!op.title.trim()) invalid(`${where}: title is empty`)
        const { kind, title, description, acceptance } = op
        next.nodes.set(op.id, { kind, title: title.trim(), description, acceptance })
        next.order.push(op.id)
        inverse.unshift({ op: 'remove', id: op.id })
        break
      }
      case 'update': {
        const node = next.nodes.get(op.id)
        if (!node) invalid(`${where}: no node ${op.id}`)
        const fields = ['title', 'description', 'acceptance'] as const
        for (const field of fields) {
          const expected = op.expect?.[field]
          if (expected !== undefined && node[field] !== expected) {
            // Someone else changed the field since; the user sees which node, not an op.
            throw new DomainError('error.planConflict', {
              title: node.title,
              reason: `${where}: ${field} of ${op.id} changed since`,
            })
          }
        }
        const restore: Partial<Omit<PlanNodeFields, 'kind'>> = {}
        const after: Partial<Omit<PlanNodeFields, 'kind'>> = {}
        for (const field of fields) {
          const value = op[field]
          if (value === undefined) continue
          if (field === 'title' && !value.trim()) invalid(`${where}: title is empty`)
          restore[field] = node[field]
          node[field] = field === 'title' ? value.trim() : value
          after[field] = node[field]
        }
        inverse.unshift({ op: 'update', id: op.id, ...restore, expect: after })
        break
      }
      case 'remove': {
        const node = next.nodes.get(op.id)
        if (!node) invalid(`${where}: no node ${op.id}`)
        const undo: PlanOp[] = [{ op: 'add', id: op.id, ...node }]
        for (const { from, to } of edgesOf(next)) {
          if (from !== op.id && to !== op.id) continue
          next.edges.delete(edgeKey(from, to))
          undo.push({ op: 'link', from, to })
        }
        next.nodes.delete(op.id)
        next.order = next.order.filter((id) => id !== op.id)
        inverse.unshift(...undo)
        break
      }
      case 'link': {
        if (!next.nodes.has(op.from)) invalid(`${where}: no node ${op.from}`)
        if (!next.nodes.has(op.to)) invalid(`${where}: no node ${op.to}`)
        if (op.from === op.to) invalid(`${where}: a node cannot depend on itself`)
        if (next.edges.has(edgeKey(op.from, op.to))) invalid(`${where}: already linked`)
        if (reaches(next, op.to, op.from)) invalid(`${where}: would create a cycle`)
        next.edges.add(edgeKey(op.from, op.to))
        inverse.unshift({ op: 'unlink', from: op.from, to: op.to })
        break
      }
      case 'unlink': {
        if (!next.edges.delete(edgeKey(op.from, op.to))) invalid(`${where}: not linked`)
        inverse.unshift({ op: 'link', from: op.from, to: op.to })
        break
      }
    }
  })
  return { graph: next, inverse }
}

/**
 * Changes the user must agree to before they take effect: removing a node, changing how a
 * node is accepted, or touching a milestone. Everything else applies at once and can be
 * reverted.
 */
export function needsConfirmation(graph: PlanGraph, ops: PlanOp[]): boolean {
  return ops.some((op) => {
    switch (op.op) {
      case 'remove':
        return true
      case 'add':
        return op.kind === 'milestone'
      case 'update': {
        const node = graph.nodes.get(op.id)
        if (!node) return false
        if (node.kind === 'milestone') return true
        return op.acceptance !== undefined && op.acceptance !== node.acceptance
      }
      case 'link':
      case 'unlink':
        return false
    }
  })
}

/**
 * What a change does, one line per op, for the user to judge before agreeing. Titles come
 * from the graph as it is now, or from the op for nodes the change adds.
 */
export function describePlanOps(
  graph: PlanGraph,
  ops: PlanOp[],
  threadsOn: (nodeId: string) => number,
): LocalizedMessage[] {
  const added = new Map<string, string>()
  for (const op of ops) if (op.op === 'add') added.set(op.id, op.title)
  const title = (id: string) => graph.nodes.get(id)?.title ?? added.get(id) ?? id
  return ops.flatMap((op): LocalizedMessage[] => {
    switch (op.op) {
      case 'add':
        return [
          {
            key: op.kind === 'milestone' ? 'plan.op.addMilestone' : 'plan.op.add',
            params: { title: op.title },
          },
        ]
      case 'remove': {
        const count = threadsOn(op.id)
        return [
          count
            ? { key: 'plan.op.removeWithThreads', params: { title: title(op.id), count } }
            : { key: 'plan.op.remove', params: { title: title(op.id) } },
        ]
      }
      case 'update': {
        // One line per field that changes, so a rename next to new acceptance is not lost.
        const node = graph.nodes.get(op.id)
        const lines: LocalizedMessage[] = []
        if (op.title !== undefined && op.title.trim() !== node?.title) {
          lines.push({
            key: 'plan.op.rename',
            params: { title: title(op.id), value: op.title.trim() },
          })
        }
        if (op.acceptance !== undefined && op.acceptance !== node?.acceptance) {
          lines.push({
            key: 'plan.op.acceptance',
            params: { title: title(op.id), value: op.acceptance || '—' },
          })
        }
        if (op.description !== undefined && op.description !== node?.description) {
          lines.push({ key: 'plan.op.update', params: { title: title(op.id) } })
        }
        return lines.length ? lines : [{ key: 'plan.op.update', params: { title: title(op.id) } }]
      }
      case 'link':
        return [{ key: 'plan.op.link', params: { from: title(op.from), to: title(op.to) } }]
      case 'unlink':
        return [{ key: 'plan.op.unlink', params: { from: title(op.from), to: title(op.to) } }]
    }
  })
}

const KINDS = ['task', 'milestone', 'decision'] as const
const FIELDS = ['title', 'description', 'acceptance'] as const

/**
 * Read ops sent from outside (the web's graph editor) strictly: every op names its node
 * ids and only known fields, so nothing unexpected reaches the event log.
 */
export function parsePlanOps(raw: unknown): PlanOp[] {
  if (!Array.isArray(raw) || !raw.length) invalid('ops must be a non-empty array')
  const id = (value: unknown, where: string): string => {
    if (typeof value !== 'string' || !value || value.length > 80) invalid(`${where}: bad id`)
    return value
  }
  const text = (value: unknown, where: string): string | undefined => {
    if (value === undefined) return undefined
    if (typeof value !== 'string' || value.length > 20_000) invalid(`${where}: not text`)
    return value
  }
  return raw.map((item: unknown, index): PlanOp => {
    const where = `ops[${index}]`
    if (typeof item !== 'object' || item === null) invalid(`${where}: not an object`)
    const op = item as Record<string, unknown>
    switch (op.op) {
      case 'add': {
        const kind = op.kind ?? 'task'
        if (!KINDS.includes(kind as never)) invalid(`${where}: bad kind`)
        return {
          op: 'add',
          id: id(op.id, where),
          kind: kind as PlanNodeFields['kind'],
          title: text(op.title, where) ?? '',
          description: text(op.description, where) ?? '',
          acceptance: text(op.acceptance, where) ?? '',
        }
      }
      case 'update': {
        const update: Extract<PlanOp, { op: 'update' }> = { op: 'update', id: id(op.id, where) }
        for (const field of FIELDS) {
          const value = text(op[field], where)
          if (value !== undefined) update[field] = value
        }
        if (op.expect !== undefined) {
          if (typeof op.expect !== 'object' || op.expect === null) invalid(`${where}: bad expect`)
          const expect: Partial<Omit<PlanNodeFields, 'kind'>> = {}
          for (const field of FIELDS) {
            const value = text((op.expect as Record<string, unknown>)[field], where)
            if (value !== undefined) expect[field] = value
          }
          update.expect = expect
        }
        return update
      }
      case 'remove':
        return { op: 'remove', id: id(op.id, where) }
      case 'link':
      case 'unlink':
        return { op: op.op, from: id(op.from, where), to: id(op.to, where) }
      default:
        return invalid(`${where}: unknown op`)
    }
  })
}

/** What a change does to one node or dependency, for drawing it on the graph. */
export type PlanMark = 'added' | 'removed' | 'changed'

export interface PlanPreview {
  /** Every node before or after the change, in plan order, removed ones included. */
  nodes: ({ id: string; mark: PlanMark | null; dependsOn: string[] } & PlanNodeFields)[]
  edges: { from: string; to: string; mark: Exclude<PlanMark, 'changed'> | null }[]
}

/**
 * The plan with a change applied, every node and edge marked by what the change does, so
 * the web can draw it before the user agrees. `null` when it no longer applies.
 */
export function previewPlanOps(graph: PlanGraph, ops: PlanOp[]): PlanPreview | null {
  let after: PlanGraph
  try {
    after = applyOps(graph, ops).graph
  } catch (error) {
    if (error instanceof DomainError) return null
    throw error
  }
  const keys = [...new Set([...graph.edges, ...after.edges])]
  const edges = keys.map((key) => {
    const [from = '', to = ''] = key.split('>')
    const mark = !graph.edges.has(key) ? 'added' : after.edges.has(key) ? null : 'removed'
    return { from, to, mark } as PlanPreview['edges'][number]
  })
  const nodes = [...new Set([...graph.order, ...after.order])].map((id) => {
    const was = graph.nodes.get(id)
    const now = after.nodes.get(id)
    const fields = (now ?? was) as PlanNodeFields
    let mark: PlanMark | null = null
    if (!was) mark = 'added'
    else if (!now) mark = 'removed'
    else if (
      was.title !== now.title ||
      was.description !== now.description ||
      was.acceptance !== now.acceptance
    ) {
      mark = 'changed'
    }
    const dependsOn = edges.filter((edge) => edge.to === id).map((edge) => edge.from)
    return { id, ...fields, mark, dependsOn }
  })
  return { nodes, edges }
}
