import type { FlowDocument, FlowNode } from '@dou-ui/flow'
import { toRaw } from 'vue'

/** Structural snapshot of a whiteboard's nodes indexed by ID, without full-board JSON round-trips. */
export type BoardNodeSnapshot = ReadonlyMap<string, FlowNode>

function clonePlain<T>(value: T): T {
  const raw = value && typeof value === 'object' ? toRaw(value) : value
  if (raw === null || typeof raw !== 'object') return raw
  if (Array.isArray(raw)) return raw.map((item) => clonePlain(item)) as T
  const out: Record<string, unknown> = {}
  for (const [key, entry] of Object.entries(raw as Record<string, unknown>)) {
    if (entry !== undefined) out[key] = clonePlain(entry)
  }
  return out as T
}

/** Deep structural equality for plain JSON-like node data, short-circuiting on the first mismatch. */
export function nodesEqual(a: unknown, b: unknown): boolean {
  const left = a && typeof a === 'object' ? toRaw(a) : a
  const right = b && typeof b === 'object' ? toRaw(b) : b
  if (Object.is(left, right)) return true
  if (left === null || right === null || typeof left !== 'object' || typeof right !== 'object') {
    return false
  }
  if (Array.isArray(left) || Array.isArray(right)) {
    if (!Array.isArray(left) || !Array.isArray(right) || left.length !== right.length) {
      return false
    }
    for (let i = 0; i < left.length; i++) {
      if (!nodesEqual(left[i], right[i])) return false
    }
    return true
  }
  const aObj = left as Record<string, unknown>
  const bObj = right as Record<string, unknown>
  let aCount = 0
  for (const key in aObj) {
    if (!Object.hasOwn(aObj, key) || aObj[key] === undefined) continue
    aCount++
    if (!Object.hasOwn(bObj, key) || !nodesEqual(aObj[key], bObj[key])) return false
  }
  let bCount = 0
  for (const key in bObj) {
    if (Object.hasOwn(bObj, key) && bObj[key] !== undefined) bCount++
  }
  return aCount === bCount
}

/** Capture an immutable node map of `doc` for fast later change comparison. */
export function snapshotBoardNodes(doc: FlowDocument): BoardNodeSnapshot {
  const map = new Map<string, FlowNode>()
  for (const node of doc.nodes) {
    map.set(node.id, clonePlain(node))
  }
  return map
}

const inside = (x: number, y: number, n: FlowNode) =>
  x >= n.x && x <= n.x + (n.width ?? 0) && y >= n.y && y <= n.y + (n.height ?? 0)

function optionOfNode(
  node: FlowNode,
  byId: ReadonlyMap<string, FlowNode>,
  options: readonly FlowNode[],
): FlowNode | null {
  for (let at: FlowNode | undefined = node; at; at = at.parent ? byId.get(at.parent) : undefined) {
    if (at.type === 'option') return at
  }
  const cx = node.x + (node.width ?? 0) / 2
  const cy = node.y + (node.height ?? 0) / 2
  return options.find((n) => inside(cx, cy, n)) ?? null
}

/**
 * Extract one FlowDocument per option node that changed (or contains a changed/added mark)
 * since `before`, in linear time without JSON.stringify/JSON.parse.
 */
export function touchedOptionDocuments(
  doc: FlowDocument,
  before: BoardNodeSnapshot | null,
): FlowDocument[] {
  const options: FlowNode[] = []
  const byId = new Map<string, FlowNode>()
  for (const node of doc.nodes) {
    byId.set(node.id, node)
    if (node.type === 'option') options.push(node)
  }
  if (!options.length) return []

  const touched = new Set<FlowNode>()
  for (const node of doc.nodes) {
    const prev = before?.get(node.id)
    if (prev !== undefined && nodesEqual(prev, node)) continue
    const option = optionOfNode(node, byId, options)
    if (option) touched.add(option)
  }
  if (!touched.size) return []

  const optionNodeIds = new Map<FlowNode, Set<string>>()
  const optionNodes = new Map<FlowNode, FlowNode[]>()
  for (const option of touched) {
    optionNodeIds.set(option, new Set())
    optionNodes.set(option, [])
  }
  for (const node of doc.nodes) {
    const owner = optionOfNode(node, byId, options)
    if (owner && touched.has(owner)) {
      optionNodeIds.get(owner)!.add(node.id)
      optionNodes.get(owner)!.push(node)
    }
  }

  const docs: FlowDocument[] = []
  for (const option of touched) {
    const ids = optionNodeIds.get(option)!
    docs.push({
      version: 1,
      nodes: optionNodes.get(option)!,
      edges: doc.edges.filter((e) => ids.has(e.source.node) && ids.has(e.target.node)),
    })
  }
  return docs
}
