import type { WhiteboardContent, WhiteboardEdge, WhiteboardNode } from '@ariadne/core'
import { ToolInputError } from './tool-helpers'

/**
 * The model writes a whiteboard as items and connectors, and leaves the pixels to us:
 * items without x and y are placed next to what they connect from, and each connector
 * picks the sides of its two items that face each other.
 */

const SHAPES = ['rect', 'ellipse', 'diamond'] as const
const KINDS = [...SHAPES, 'sticky', 'text'] as const
type ItemKind = (typeof KINDS)[number]
const TONES = ['neutral', 'primary', 'success', 'warning', 'danger']
const ROUTES = ['straight', 'elbow', 'curve']
const DASHES = ['solid', 'dashed', 'dotted', 'dash-dot']
const ARROWS = ['none', 'end', 'both']

const SIZE: Record<ItemKind, { width: number; height: number }> = {
  rect: { width: 160, height: 90 },
  ellipse: { width: 160, height: 90 },
  diamond: { width: 160, height: 110 },
  sticky: { width: 160, height: 160 },
  text: { width: 200, height: 40 },
}
const GAP_X = 80
const GAP_Y = 40

interface Box {
  x: number
  y: number
  width: number
  height: number
}

export interface BoardChanges {
  items: Record<string, unknown>[]
  connectors: Record<string, unknown>[]
  removeItems: string[]
  removeConnectors: string[]
}

const num = (value: unknown): number | undefined =>
  typeof value === 'number' && Number.isFinite(value) ? value : undefined

function text(item: Record<string, unknown>, key: string): string | undefined {
  const value = item[key]
  if (value === undefined || value === null) return undefined
  if (typeof value !== 'string') throw new ToolInputError(`"${key}" must be a string`)
  return value
}

function oneOf(item: Record<string, unknown>, key: string, allowed: readonly string[]) {
  const value = text(item, key)
  if (value !== undefined && !allowed.includes(value)) {
    throw new ToolInputError(`"${key}" must be one of ${allowed.join(', ')}`)
  }
  return value
}

function kindOf(node: WhiteboardNode): ItemKind | 'drawing' | 'image' {
  if (node.type === 'shape') {
    const shape = node.data?.shape
    return SHAPES.includes(shape as never) ? (shape as ItemKind) : 'rect'
  }
  if (node.type === 'sticky' || node.type === 'text') return node.type
  return node.type === 'ink' ? 'drawing' : 'image'
}

function boxOf(node: WhiteboardNode): Box {
  const kind = kindOf(node)
  const fallback = kind === 'drawing' || kind === 'image' ? SIZE.rect : SIZE[kind]
  return {
    x: node.x,
    y: node.y,
    width: node.width ?? fallback.width,
    height: node.height ?? fallback.height,
  }
}

const overlaps = (a: Box, b: Box) =>
  a.x < b.x + b.width + GAP_Y / 2 &&
  b.x < a.x + a.width + GAP_Y / 2 &&
  a.y < b.y + b.height + GAP_Y / 2 &&
  b.y < a.y + a.height + GAP_Y / 2

/** Move a box down until it is clear of every other. */
function clear(box: Box, others: Box[]): Box {
  const next = { ...box }
  for (let i = 0; i < 1000; i++) {
    const hit = others.find((other) => overlaps(next, other))
    if (!hit) break
    next.y = hit.y + hit.height + GAP_Y
  }
  return next
}

/** Apply one item to a node, new or existing; only the fields given change. */
function applyItem(item: Record<string, unknown>, existing?: WhiteboardNode): WhiteboardNode {
  const id = text(item, 'id')
  if (!id) throw new ToolInputError('every item needs an "id"')
  const given = oneOf(item, 'kind', KINDS) as ItemKind | undefined
  const before = existing ? kindOf(existing) : undefined
  if (existing && (before === 'drawing' || before === 'image')) {
    if (given || item.text !== undefined || item.tone !== undefined) {
      throw new ToolInputError(
        `item "${id}" is a ${before}; it can be moved or removed, not changed`,
      )
    }
    return {
      ...existing,
      x: num(item.x) ?? existing.x,
      y: num(item.y) ?? existing.y,
      ...(num(item.width) !== undefined ? { width: num(item.width) } : {}),
      ...(num(item.height) !== undefined ? { height: num(item.height) } : {}),
    }
  }
  const kind: ItemKind = given ?? (before as ItemKind | undefined) ?? 'rect'
  const data: Record<string, unknown> = { ...existing?.data }
  if (SHAPES.includes(kind as never)) data.shape = kind
  else delete data.shape
  const label = text(item, 'text')
  if (label !== undefined) data.text = label
  const tone = oneOf(item, 'tone', TONES)
  if (tone !== undefined) data.tone = tone
  if (kind === 'text') delete data.tone
  const type = SHAPES.includes(kind as never) ? 'shape' : kind
  const node: WhiteboardNode = {
    ...existing,
    id,
    type,
    x: num(item.x) ?? existing?.x ?? Number.NaN,
    y: num(item.y) ?? existing?.y ?? Number.NaN,
    data,
  }
  const width = num(item.width) ?? (given && given !== before ? SIZE[kind].width : existing?.width)
  const height =
    num(item.height) ?? (given && given !== before ? SIZE[kind].height : existing?.height)
  node.width = width ?? SIZE[kind].width
  if (kind === 'text') delete node.height
  else node.height = height ?? SIZE[kind].height
  return node
}

/** The sides of two boxes that face each other: a source port and a target port. */
function ports(from: Box, to: Box): { source: string; target: string } {
  const dx = to.x + to.width / 2 - (from.x + from.width / 2)
  const dy = to.y + to.height / 2 - (from.y + from.height / 2)
  if (Math.abs(dx) >= Math.abs(dy)) {
    return dx >= 0 ? { source: 'right', target: 'left-in' } : { source: 'left', target: 'right-in' }
  }
  return dy >= 0 ? { source: 'bottom', target: 'top-in' } : { source: 'top', target: 'bottom-in' }
}

const isPlaced = (n: WhiteboardNode) => Number.isFinite(n.x) && Number.isFinite(n.y)

/**
 * Give every node without a place one: right of a placed node it is connected from, or
 * below everything else; then down until it overlaps nothing.
 */
function place(nodes: WhiteboardNode[], edges: WhiteboardEdge[]): void {
  const placed = nodes.filter(isPlaced)
  const boxes = placed.map(boxOf)
  const bottom = boxes.length ? Math.max(...boxes.map((b) => b.y + b.height)) + GAP_Y * 2 : 0
  const left = boxes.length ? Math.min(...boxes.map((b) => b.x)) : 0
  const pending = nodes.filter((n) => !placed.includes(n))
  const byId = new Map(nodes.map((n) => [n.id, n]))
  while (pending.length) {
    // Prefer a node whose predecessor already has a place, so chains run left to right.
    let index = pending.findIndex((n) =>
      edges.some((e) => e.target.node === n.id && isPlaced(byId.get(e.source.node)!)),
    )
    // Otherwise a root: nothing pending points at it.
    if (index < 0) {
      index = pending.findIndex(
        (n) =>
          !edges.some((e) => e.target.node === n.id && pending.some((p) => p.id === e.source.node)),
      )
    }
    if (index < 0) index = 0
    const node = pending.splice(index, 1)[0]!
    const from = edges
      .filter((e) => e.target.node === node.id)
      .map((e) => byId.get(e.source.node)!)
      .find(isPlaced)
    const size = boxOf({ ...node, x: 0, y: 0 })
    const start = from
      ? { x: from.x + boxOf(from).width + GAP_X, y: from.y }
      : { x: left, y: bottom }
    const box = clear({ ...size, ...start }, boxes)
    node.x = Math.round(box.x)
    node.y = Math.round(box.y)
    boxes.push(boxOf(node))
  }
}

function applyConnector(
  connector: Record<string, unknown>,
  ids: Set<string>,
  existing?: WhiteboardEdge,
): WhiteboardEdge {
  const from = text(connector, 'from') ?? existing?.source.node
  const to = text(connector, 'to') ?? existing?.target.node
  if (!from || !to) throw new ToolInputError('every connector needs "from" and "to"')
  for (const end of [from, to]) {
    if (!ids.has(end)) throw new ToolInputError(`connector names a missing item "${end}"`)
  }
  const data: Record<string, unknown> = { ...existing?.data }
  for (const [key, allowed] of [
    ['route', ROUTES],
    ['dash', DASHES],
    ['arrow', ARROWS],
  ] as const) {
    const value = oneOf(connector, key, allowed)
    if (value !== undefined) data[key] = value
  }
  const edge: WhiteboardEdge = {
    ...existing,
    id: existing?.id ?? text(connector, 'id') ?? `${from}-${to}`,
    source: { node: from, port: existing?.source.port ?? 'right' },
    target: { node: to, port: existing?.target.port ?? 'left-in' },
  }
  if (Object.keys(data).length) edge.data = data
  const label = text(connector, 'label')
  if (label !== undefined) {
    if (label) edge.label = label
    else delete edge.label
  }
  return edge
}

function uniqueId(id: string, taken: Set<string>): string {
  let next = id
  for (let i = 2; taken.has(next); i++) next = `${id}-${i}`
  taken.add(next)
  return next
}

/** Apply the model's changes to a board; with an empty board, this draws a new one. */
export function mergeBoard(board: WhiteboardContent, changes: BoardChanges): WhiteboardContent {
  const removed = new Set(changes.removeItems)
  const nodes = board.nodes.filter((n) => !removed.has(n.id))
  const byId = new Map(nodes.map((n) => [n.id, n]))
  const moved = new Set<string>()
  for (const item of changes.items) {
    const id = text(item, 'id')
    const existing = id ? byId.get(id) : undefined
    const node = applyItem(item, existing)
    if (existing) {
      if (num(item.x) !== undefined || num(item.y) !== undefined || node.width !== existing.width) {
        moved.add(node.id)
      }
      nodes[nodes.indexOf(existing)] = node
    } else {
      nodes.push(node)
      moved.add(node.id)
    }
    byId.set(node.id, node)
  }

  const dropped = new Set(changes.removeConnectors)
  const edges = board.edges.filter(
    (e) => !dropped.has(e.id) && byId.has(e.source.node) && byId.has(e.target.node),
  )
  const edgeIds = new Set(edges.map((e) => e.id))
  const ids = new Set(byId.keys())
  const touched = new Set<WhiteboardEdge>()
  for (const connector of changes.connectors) {
    const id = text(connector, 'id')
    const existing =
      edges.find((e) => e.id === id) ??
      (id
        ? undefined
        : edges.find((e) => e.source.node === connector.from && e.target.node === connector.to))
    const edge = applyConnector(connector, ids, existing)
    if (existing) edges[edges.indexOf(existing)] = edge
    else {
      edge.id = uniqueId(edge.id, edgeIds)
      edges.push(edge)
    }
    touched.add(edge)
  }

  place(nodes, edges)
  for (const edge of edges) {
    if (!touched.has(edge) && !moved.has(edge.source.node) && !moved.has(edge.target.node)) continue
    const side = ports(boxOf(byId.get(edge.source.node)!), boxOf(byId.get(edge.target.node)!))
    edge.source = { node: edge.source.node, port: side.source }
    edge.target = { node: edge.target.node, port: side.target }
  }
  return { version: 1, nodes, edges }
}

export function boardFromItems(
  items: Record<string, unknown>[],
  connectors: Record<string, unknown>[],
): WhiteboardContent {
  const seen = new Set<string>()
  for (const item of items) {
    const id = text(item, 'id')
    if (id && seen.has(id)) throw new ToolInputError(`item id "${id}" is used twice`)
    if (id) seen.add(id)
  }
  return mergeBoard(
    { version: 1, nodes: [], edges: [] },
    { items, connectors, removeItems: [], removeConnectors: [] },
  )
}

/** The board as the model reads it: items and connectors, without ink points or image data. */
export function boardSummary(board: WhiteboardContent) {
  return {
    items: board.nodes.map((node) => {
      const kind = kindOf(node)
      const { x, y } = node
      const box = boxOf(node)
      const data = node.data ?? {}
      return {
        id: node.id,
        kind,
        ...(typeof data.text === 'string' && data.text ? { text: data.text } : {}),
        ...(typeof data.tone === 'string' ? { tone: data.tone } : {}),
        ...(kind === 'image' && typeof data.alt === 'string' ? { alt: data.alt } : {}),
        x,
        y,
        width: box.width,
        height: box.height,
      }
    }),
    connectors: board.edges.map((edge) => ({
      id: edge.id,
      from: edge.source.node,
      to: edge.target.node,
      ...(edge.label ? { label: edge.label } : {}),
      ...edge.data,
    })),
  }
}
