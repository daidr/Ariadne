import type { WhiteboardContent, WhiteboardEdge, WhiteboardNode } from '@ariadne/core'
import { ToolInputError } from './tool-helpers'
import { fitText, layoutBoard, rankGap, type Direction } from './whiteboard-layout'

/**
 * The model writes a whiteboard as items and connectors, and leaves the pixels to us:
 * a new board is laid out in ranks along its connectors, with boxes sized to their text
 * and room between ranks for the labels; an item added later goes next to what it
 * connects from. Each connector joins the sides of its two items that face each other.
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
  /** Lay every item the model can change out again, instead of only placing new ones. */
  relayout?: boolean
  direction?: Direction
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

/** Kinds the model can see and move but not rewrite: drawn by the user in the editor. */
type FixedKind = 'drawing' | 'image' | 'node'
const isFixed = (kind: string): kind is FixedKind =>
  kind === 'drawing' || kind === 'image' || kind === 'node'

function kindOf(node: WhiteboardNode): ItemKind | FixedKind {
  if (node.type === 'shape') {
    const shape = node.data?.shape
    return SHAPES.includes(shape as never) ? (shape as ItemKind) : 'rect'
  }
  if (node.type === 'sticky' || node.type === 'text') return node.type
  if (node.type === 'ink') return 'drawing'
  return node.type === 'image' ? 'image' : 'node'
}

function boxOf(node: WhiteboardNode): Box {
  const kind = kindOf(node)
  const fallback = isFixed(kind) ? SIZE.rect : SIZE[kind]
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

/** Move a box across the flow (down, or right in a top-to-bottom board) until it is clear of every other. */
function clear(box: Box, others: Box[], direction: Direction): Box {
  const next = { ...box }
  for (let i = 0; i < 1000; i++) {
    const hit = others.find((other) => overlaps(next, other))
    if (!hit) break
    if (direction === 'down') next.x = hit.x + hit.width + GAP_Y
    else next.y = hit.y + hit.height + GAP_Y
  }
  return next
}

/** Apply one item to a node, new or existing; only the fields given change. */
function applyItem(item: Record<string, unknown>, existing?: WhiteboardNode): WhiteboardNode {
  const id = text(item, 'id')
  if (!id) throw new ToolInputError('every item needs an "id"')
  const given = oneOf(item, 'kind', KINDS) as ItemKind | undefined
  const before = existing ? kindOf(existing) : undefined
  if (existing && before && isFixed(before)) {
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
  const reshaped = !existing || (given !== undefined && given !== before)
  // A new or reshaped item is sized to its text; otherwise it keeps its width and only
  // grows taller when its text no longer fits. Text is never cut off.
  const width = num(item.width) ?? (reshaped ? undefined : existing?.width)
  const fit = fitText(kind, typeof data.text === 'string' ? data.text : '', SIZE[kind], width)
  node.width = fit.width
  if (kind === 'text') delete node.height
  else {
    const height = num(item.height) ?? (reshaped ? undefined : existing?.height)
    node.height = Math.max(height ?? 0, fit.height!)
  }
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
function place(nodes: WhiteboardNode[], edges: WhiteboardEdge[], direction: Direction): void {
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
    const gap = rankGap(
      edges
        .filter((e) => e.target.node === node.id && e.source.node === from?.id)
        .map((e) => e.label),
      direction,
    )
    const start = !from
      ? { x: left, y: bottom }
      : direction === 'down'
        ? { x: from.x, y: from.y + boxOf(from).height + gap }
        : { x: from.x + boxOf(from).width + gap, y: from.y }
    const box = clear({ ...size, ...start }, boxes, direction)
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
  // Curves bend around the items in their way; a straight line can't (see layoutBoard).
  if (!existing && data.route === undefined) data.route = 'curve'
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

/** The way most connectors run: left to right when they mostly leave items on the right. */
function directionOf(edges: WhiteboardEdge[]): Direction {
  const down = edges.filter((e) => e.source.port === 'bottom').length
  const right = edges.filter((e) => e.source.port === 'right').length
  return right > down ? 'right' : 'down'
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

  const direction = changes.direction ?? directionOf(board.edges)
  if (changes.relayout) {
    relayout(nodes, edges, direction)
    return { version: 1, nodes, edges }
  }
  place(nodes, edges, direction)
  for (const edge of edges) {
    if (!touched.has(edge) && !moved.has(edge.source.node) && !moved.has(edge.target.node)) continue
    const side = ports(boxOf(byId.get(edge.source.node)!), boxOf(byId.get(edge.target.node)!))
    edge.source = { node: edge.source.node, port: side.source }
    edge.target = { node: edge.target.node, port: side.target }
    // The way around the items between was worked out for the old places.
    delete edge.points
  }
  return { version: 1, nodes, edges }
}

/**
 * Lay out every item the model can change, in ranks along the connectors, where the
 * items stood before (or at the top left of a new board); below the user's drawings and
 * images if it would cover them. Connectors between neighbouring ranks join facing sides,
 * longer ones run through the gaps of the ranks between.
 */
function relayout(nodes: WhiteboardNode[], edges: WhiteboardEdge[], direction: Direction): void {
  const movable = nodes.filter((n) => !isFixed(kindOf(n)))
  const fixed = nodes.filter((n) => isFixed(kindOf(n))).map(boxOf)
  const ids = new Set(movable.map((n) => n.id))
  const inside = edges.filter((e) => ids.has(e.source.node) && ids.has(e.target.node))
  const layout = layoutBoard(
    movable.map((n) => ({ id: n.id, ...boxOf({ ...n, x: 0, y: 0 }) })),
    inside.map((e) => ({ from: e.source.node, to: e.target.node, label: e.label })),
    direction,
  )
  const before = movable.filter(isPlaced)
  const origin = {
    x: before.length ? Math.min(...before.map((n) => n.x)) : 0,
    y: before.length ? Math.min(...before.map((n) => n.y)) : 0,
  }
  const area = { ...origin, width: layout.width, height: layout.height }
  if (fixed.some((box) => overlaps(area, box))) {
    origin.y = Math.max(...fixed.map((b) => b.y + b.height)) + GAP_Y * 2
  }
  for (const node of movable) {
    const at = layout.nodes.get(node.id)!
    node.x = origin.x + at.x
    node.y = origin.y + at.y
  }
  const byId = new Map(nodes.map((n) => [n.id, n]))
  // Several connectors between the same two items run side by side.
  const seen = new Map<string, number>()
  for (const edge of edges) {
    const key = `${edge.source.node}>${edge.target.node}`
    const laid = ids.has(edge.source.node) && ids.has(edge.target.node) && !layout.dropped.has(key)
    delete edge.points
    if (!laid) {
      const side = ports(boxOf(byId.get(edge.source.node)!), boxOf(byId.get(edge.target.node)!))
      edge.source = { node: edge.source.node, port: side.source }
      edge.target = { node: edge.target.node, port: side.target }
      continue
    }
    edge.source = { node: edge.source.node, port: direction === 'down' ? 'bottom' : 'right' }
    edge.target = { node: edge.target.node, port: direction === 'down' ? 'top-in' : 'left-in' }
    const through = layout.points.get(key)
    if (!through) continue
    const n = seen.get(key) ?? 0
    seen.set(key, n + 1)
    const off = n * 24
    edge.points = through.map((p) =>
      direction === 'down'
        ? { x: origin.x + p.x + off, y: origin.y + p.y }
        : { x: origin.x + p.x, y: origin.y + p.y + off },
    )
  }
}

/** A new board, laid out whole; places the model gives are not used. */
export function boardFromItems(
  items: Record<string, unknown>[],
  connectors: Record<string, unknown>[],
  direction: Direction = 'down',
): WhiteboardContent {
  const seen = new Set<string>()
  const unplaced = items.map((item) => {
    const id = text(item, 'id')
    if (id && seen.has(id)) throw new ToolInputError(`item id "${id}" is used twice`)
    if (id) seen.add(id)
    const { x: _x, y: _y, ...rest } = item
    return rest
  })
  return mergeBoard(
    { version: 1, nodes: [], edges: [] },
    {
      items: unplaced,
      connectors,
      removeItems: [],
      removeConnectors: [],
      relayout: true,
      direction,
    },
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
        ...(kind === 'node' && typeof data.label === 'string' ? { text: data.label } : {}),
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
