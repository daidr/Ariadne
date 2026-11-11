import type { WhiteboardContent } from '@ariadne/core'
import { ToolInputError } from './tool-helpers'
import { boardFromItems, boardProblems, boardSummary, mergeBoard, userDrawn } from './whiteboard'

/**
 * Whiteboards inside a document. dou-ui writes each as a code fence in the document's
 * Markdown, `dou-whiteboard`, holding `{ height, board }` with every node's pixels. The
 * model never sees that: it reads and writes a board in a document as a `whiteboard` fence
 * holding the items and connectors create_whiteboard takes, and the board is laid out and
 * merged here, the same way as a whiteboard of its own.
 *
 * A fence the model gives back as it read it keeps the board it stood for untouched,
 * with whatever the user drew and moved. A changed fence is taken as the whole board:
 * matched to the one it stood for by its place among the boards, its items and
 * connectors are merged by id, and what it leaves out goes. A new fence is a new board.
 */

const STORED = 'dou-whiteboard'
const WRITTEN = 'whiteboard'

/** The kinds the model can put on a board in a document: no placeholders or options there. */
const KINDS = new Set(['rect', 'ellipse', 'diamond', 'sticky', 'text'])

/** How tall a board stands in the document, as dou-ui allows it. */
const HEIGHT = { min: 160, max: 1200, fit: 480 }
/** About how wide a board shows in a document: its reading column. */
const COLUMN = 680
/** Room the preview keeps around what it fits. */
const PAD = 48

type Part =
  | { type: 'text'; text: string }
  | { type: 'fence'; lang: string; body: string; raw: string; closed: boolean }

/** The text and the code fences of a Markdown document, in order. */
function partsOf(markdown: string): Part[] {
  const lines = markdown.split('\n')
  const parts: Part[] = []
  let text: string[] = []
  const flush = () => {
    if (text.length) parts.push({ type: 'text', text: text.join('\n') })
    text = []
  }
  for (let i = 0; i < lines.length; i++) {
    const open = /^ {0,3}(`{3,}|~{3,})[ \t]*([^\s`]*)[^`]*$/.exec(lines[i]!)
    if (!open) {
      text.push(lines[i]!)
      continue
    }
    const mark = open[1]!
    let end = i + 1
    while (end < lines.length) {
      const close = /^ {0,3}(`{3,}|~{3,})[ \t]*$/.exec(lines[end]!)
      if (close && close[1]![0] === mark[0] && close[1]!.length >= mark.length) break
      end++
    }
    flush()
    const closed = end < lines.length
    parts.push({
      type: 'fence',
      lang: open[2]!,
      body: lines.slice(i + 1, end).join('\n'),
      raw: lines.slice(i, closed ? end + 1 : end).join('\n'),
      closed,
    })
    i = end
  }
  flush()
  return parts
}

const join = (parts: string[]) => parts.join('\n')

interface Stored {
  height?: number
  board: WhiteboardContent
}

function readStored(body: string): Stored | null {
  try {
    const value = JSON.parse(body) as { height?: unknown; board?: unknown }
    const board = value?.board as WhiteboardContent | undefined
    if (!board || !Array.isArray(board.nodes) || !Array.isArray(board.edges)) return null
    return { board, ...(typeof value.height === 'number' ? { height: value.height } : {}) }
  } catch {
    return null
  }
}

/** A list one entry a line, so an edit can take out or change one. */
const lines = (list: object[]) =>
  list.length ? `[\n${list.map((entry) => JSON.stringify(entry)).join(',\n')}\n]` : '[]'

/** A board as the model reads it in a document: one item or connector a line. */
function written(board: WhiteboardContent): string {
  const { items, connectors } = boardSummary(board)
  const shown = items.map(({ x: _x, y: _y, width: _w, height: _h, ...item }) => item)
  return `{"items": ${lines(shown)}, "connectors": ${lines(connectors)}}`
}

const fence = (lang: string, body: string) => `\`\`\`${lang}\n${body}\n\`\`\``

export interface DocumentView {
  /** The document as the model reads it. */
  content: string
  /** What looks wrong on its boards, numbered from the top. */
  problems: string[]
}

/** A stored document as the model reads it: each board as its items and connectors. */
export function documentView(markdown: string): DocumentView {
  if (!markdown.includes(STORED)) return { content: markdown, problems: [] }
  const problems: string[] = []
  let n = 0
  const content = join(
    partsOf(markdown).map((part) => {
      if (part.type === 'text') return part.text
      const stored = part.lang === STORED && part.closed ? readStored(part.body) : null
      if (!stored) return part.raw
      n++
      for (const problem of boardProblems(stored.board))
        problems.push(`whiteboard ${n}: ${problem}`)
      return fence(WRITTEN, written(stored.board))
    }),
  )
  return { content, problems }
}

/** What the model wrote in a `whiteboard` fence. */
interface Spec {
  items: Record<string, unknown>[]
  connectors: Record<string, unknown>[]
  direction?: 'down' | 'right'
  relayout?: boolean
  height?: number
}

/** JSON, or JSON with a comma left before a bracket where a line was taken out. */
function parseLoose(body: string): Record<string, unknown> {
  try {
    return JSON.parse(body) as Record<string, unknown>
  } catch {
    return JSON.parse(body.replace(/,(\s*[\]}])/g, '$1')) as Record<string, unknown>
  }
}

function specOf(body: string, n: number): Spec {
  let value: Record<string, unknown>
  try {
    value = parseLoose(body)
  } catch {
    throw new ToolInputError(
      `whiteboard ${n}: the fence must hold JSON: {"items": [...], "connectors": [...]}`,
    )
  }
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw new ToolInputError(`whiteboard ${n}: give {"items": [...], "connectors": [...]}`)
  }
  const list = (key: string) => {
    const at = value[key]
    if (at === undefined || at === null) return []
    if (!Array.isArray(at) || at.some((e) => typeof e !== 'object' || e === null)) {
      throw new ToolInputError(`whiteboard ${n}: "${key}" must be an array of objects`)
    }
    return at as Record<string, unknown>[]
  }
  const direction = value.direction
  if (direction !== undefined && direction !== 'down' && direction !== 'right') {
    throw new ToolInputError(`whiteboard ${n}: "direction" must be "down" or "right"`)
  }
  return {
    items: list('items'),
    connectors: list('connectors'),
    ...(direction ? { direction } : {}),
    ...(value.relayout === true ? { relayout: true } : {}),
    ...(typeof value.height === 'number' && Number.isFinite(value.height)
      ? { height: value.height }
      : {}),
  }
}

/** A board's items given by the model, checked for the kinds a document's board can hold. */
function checkKinds(spec: Spec, n: number, before?: WhiteboardContent) {
  const drawn = new Set(before?.nodes.filter(userDrawn).map((node) => node.id))
  for (const item of spec.items) {
    if (typeof item.id === 'string' && drawn.has(item.id)) continue
    if (item.kind !== undefined && !KINDS.has(item.kind as string)) {
      throw new ToolInputError(
        `whiteboard ${n}: item kind must be one of ${[...KINDS].join(', ')} in a document`,
      )
    }
  }
}

const clampHeight = (h: number) => Math.round(Math.min(HEIGHT.max, Math.max(HEIGHT.min, h)))

/** How tall a new board stands: all of it shown, at most as large as it is, within reason. */
function fitHeight(board: WhiteboardContent): number {
  if (!board.nodes.length) return 360
  const left = Math.min(...board.nodes.map((n) => n.x))
  const right = Math.max(...board.nodes.map((n) => n.x + (n.width ?? 160)))
  const top = Math.min(...board.nodes.map((n) => n.y))
  const bottom = Math.max(...board.nodes.map((n) => n.y + (n.height ?? 90)))
  const scale = Math.min(1, (COLUMN - PAD) / Math.max(1, right - left))
  return Math.min(HEIGHT.fit, clampHeight((bottom - top) * scale + PAD))
}

/** The model's items for a board it read: the user's drawings only move. */
function itemsFor(spec: Spec, before: WhiteboardContent) {
  const drawn = new Map(before.nodes.filter(userDrawn).map((node) => [node.id, node]))
  return spec.items.map((item) => {
    if (typeof item.id !== 'string' || !drawn.has(item.id)) return item
    const { id, x, y } = item
    return { id, ...(x !== undefined ? { x } : {}), ...(y !== undefined ? { y } : {}) }
  })
}

/** The board a changed fence makes of the one it stood for. */
function mergeSpec(before: WhiteboardContent, spec: Spec): WhiteboardContent {
  const items = itemsFor(spec, before)
  const kept = new Set(items.map((item) => item.id))
  const keptEdges = new Set<string>()
  for (const connector of spec.connectors) {
    if (typeof connector.id === 'string') keptEdges.add(connector.id)
    else {
      const same = before.edges.find(
        (e) => e.source.node === connector.from && e.target.node === connector.to,
      )
      if (same) keptEdges.add(same.id)
    }
  }
  return mergeBoard(structuredClone(before), {
    items,
    connectors: spec.connectors,
    removeItems: before.nodes.filter((node) => !kept.has(node.id)).map((node) => node.id),
    removeConnectors: before.edges.filter((e) => !keptEdges.has(e.id)).map((e) => e.id),
    relayout: spec.relayout === true,
    ...(spec.direction ? { direction: spec.direction } : {}),
  })
}

const storedFence = (stored: Stored) => fence(STORED, JSON.stringify(stored))

/** The placeholder a board shows as while the model is still writing its fence. */
const WRITING = '<pending>画板</pending>'

/**
 * Text the model is still writing, with a board it has begun but not finished shown as a
 * placeholder. Left open, its fence would run to the end of the document and take in
 * everything after it.
 */
export function boardSoFar(text: string): string {
  if (!text.includes(WRITTEN)) return text
  const parts = partsOf(text)
  const last = parts.at(-1)
  if (last?.type !== 'fence' || last.closed || last.lang !== WRITTEN) return text
  return join([...parts.slice(0, -1).map((p) => (p.type === 'text' ? p.text : p.raw)), WRITING])
}

export interface Written {
  /** The document to store. */
  content: string
  /** What looks wrong on the boards written, numbered from the top. */
  problems: string[]
}

/**
 * The document to store from what the model wrote, `base` being the stored document it
 * read. `live`: the model is still writing, so a board not finished yet shows as a
 * placeholder and a fence that does not hold together yet is not an error.
 */
export function documentFromView(view: string, base: string, live = false): Written {
  if (!view.includes(WRITTEN)) return { content: view, problems: [] }
  const before: { raw: string; seen: string; stored: Stored }[] = []
  for (const part of partsOf(base)) {
    if (part.type !== 'fence' || part.lang !== STORED || !part.closed) continue
    const stored = readStored(part.body)
    if (stored) before.push({ raw: part.raw, seen: written(stored.board), stored })
  }
  const parts = partsOf(view)
  const fences = parts.flatMap((part, at) =>
    part.type === 'fence' && part.lang === WRITTEN ? [{ part, at }] : [],
  )
  // Fences as read keep their boards; between two of those, the changed ones stand for
  // the boards there, in order.
  const match = new Map<number, number>()
  const exact = new Set<number>()
  let from = 0
  for (const [i, { part }] of fences.entries()) {
    const found = before.findIndex((b, k) => k >= from && b.seen === part.body.trim())
    if (found < 0 || !part.closed) continue
    match.set(i, found)
    exact.add(i)
    from = found + 1
  }
  let lastBoard = -1
  for (let i = 0; i < fences.length; i++) {
    if (exact.has(i)) {
      lastBoard = match.get(i)!
      continue
    }
    let next = before.length
    for (let j = i + 1; j < fences.length; j++) {
      if (exact.has(j)) {
        next = match.get(j)!
        break
      }
    }
    if (lastBoard + 1 < next) match.set(i, ++lastBoard)
  }

  const problems: string[] = []
  const out = parts.map((part) => (part.type === 'text' ? part.text : part.raw))
  for (const [i, { part, at }] of fences.entries()) {
    const n = i + 1
    const old = match.has(i) ? before[match.get(i)!]! : undefined
    if (exact.has(i)) {
      out[at] = old!.raw
      continue
    }
    if (!part.closed) {
      if (!live) throw new ToolInputError(`whiteboard ${n}: the fence is not closed with \`\`\``)
      out[at] = WRITING
      continue
    }
    let spec: Spec
    try {
      spec = specOf(part.body, n)
      checkKinds(spec, n, old?.stored.board)
    } catch (error) {
      if (!live) throw error
      out[at] = old ? old.raw : WRITING
      continue
    }
    const board = old
      ? mergeSpec(old.stored.board, spec)
      : boardFromItems(spec.items, spec.connectors, spec.direction)
    const height =
      spec.height !== undefined
        ? clampHeight(spec.height)
        : (old?.stored.height ?? fitHeight(board))
    out[at] = storedFence({ height, board })
    for (const problem of boardProblems(board)) problems.push(`whiteboard ${n}: ${problem}`)
  }
  return { content: join(out), problems }
}
