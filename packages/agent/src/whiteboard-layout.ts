import { layoutGraph } from 'dou-ui'

/**
 * Sizes and places for what the model draws on a whiteboard. Text is measured roughly
 * (a CJK character is one em, anything else about 0.6), so boxes grow to hold their text
 * and the gaps between ranks to hold the longest connector label.
 */

export type Direction = 'right' | 'down'

export interface Point {
  x: number
  y: number
}

/** The board's text sizes and paddings, as `@dou-ui/flow`'s whiteboard draws them. */
const SHAPE_FONT = 14
const SHAPE_LINE = 21
const SHAPE_PAD = { x: 16, y: 12 }
const STICKY_PAD = 16
const TEXT_FONT = 16
const TEXT_PAD = { x: 8, y: 4 }
/** A connector label: small text, 6px padding and a border, cut off past 200px. */
const LABEL_FONT = 13
const LABEL_HEIGHT = 20
const LABEL_MAX = 200

/** The widest a box grows to before its text wraps. */
const MAX_WIDTH = 280
/** Room left on both sides of a label between two ranks. */
const LABEL_ROOM = 24
const RANK_GAP = 80
const LANE_GAP = 40
const PART_GAP = 80

const WIDE = /[ᄀ-ᅟ⺀-꓏가-힣豈-﫿︰-﹏＀-｠￠-￦]/u

/** How wide a line is, in px, at a font size. */
export function textWidth(line: string, font: number): number {
  let ems = 0
  for (const char of line) ems += WIDE.test(char) ? 1 : 0.6
  return ems * font
}

/** How many lines text takes in a given width; wrapping loses a little to word breaks. */
function lineCount(text: string, width: number, font: number): number {
  return text
    .split('\n')
    .reduce((sum, line) => sum + Math.max(1, Math.ceil((textWidth(line, font) * 1.1) / width)), 0)
}

const longest = (text: string, font: number) =>
  Math.max(0, ...text.split('\n').map((line) => textWidth(line, font)))

/**
 * A size that shows all of `text`: a box at least `min`, widened up to MAX_WIDTH for a
 * long line, then as tall as its lines. An ellipse or diamond holds its text in a smaller
 * middle, so it gets more room. `width` keeps the width and only fits the height.
 */
export function fitText(
  kind: string,
  text: string,
  min: { width: number; height: number },
  width?: number,
): { width: number; height?: number } {
  if (kind === 'text') {
    const w =
      width ?? Math.min(Math.max(min.width, longest(text, TEXT_FONT) + TEXT_PAD.x * 2 + 4), 480)
    return { width: Math.round(w) }
  }
  const pad = kind === 'sticky' ? { x: STICKY_PAD, y: STICKY_PAD } : SHAPE_PAD
  const roomy = kind === 'ellipse' ? 1.3 : kind === 'diamond' ? 1.6 : 1
  const w =
    width ??
    Math.min(Math.max(min.width, (longest(text, SHAPE_FONT) + pad.x * 2) * roomy + 2), MAX_WIDTH)
  const inner = w / roomy - pad.x * 2
  const lines = text ? lineCount(text, Math.max(inner, SHAPE_FONT), SHAPE_FONT) : 1
  const h = Math.max(min.height, (lines * SHAPE_LINE + pad.y * 2) * roomy + 2)
  return { width: Math.round(w), height: Math.round(h) }
}

/** The width a connector label takes on the board. */
export function labelWidth(label: string | undefined): number {
  if (!label) return 0
  return Math.min(LABEL_MAX, textWidth(label, LABEL_FONT) + 14)
}

/** The gap between two items joined along the flow, with room for the label between them. */
export function rankGap(labels: (string | undefined)[], direction: Direction): number {
  if (direction === 'down') {
    return labels.some(Boolean) ? Math.max(RANK_GAP, LABEL_HEIGHT + LABEL_ROOM * 2) : RANK_GAP
  }
  return Math.max(RANK_GAP, Math.max(0, ...labels.map(labelWidth)) + LABEL_ROOM * 2)
}

export interface LayoutBox {
  id: string
  width: number
  height: number
}

export interface BoardLayout {
  nodes: Map<string, Point>
  /** Where a connector spanning several ranks runs between the items in its way, keyed `from>to`. */
  points: Map<string, Point[]>
  /** Connectors the layout left out, because they close a cycle; their ends face each other. */
  dropped: Set<string>
  width: number
  height: number
}

interface Block {
  width: number
  height: number
  nodes: Map<string, Point>
  points: Map<string, Point[]>
}

/** The parts of the graph not joined to each other, in the order their first item came. */
function parts(ids: string[], edges: { from: string; to: string }[]): string[][] {
  const near = new Map(ids.map((id) => [id, [] as string[]]))
  for (const { from, to } of edges) {
    near.get(from)?.push(to)
    near.get(to)?.push(from)
  }
  const seen = new Set<string>()
  const out: string[][] = []
  for (const id of ids) {
    if (seen.has(id)) continue
    const part: string[] = []
    const stack = [id]
    seen.add(id)
    while (stack.length) {
      const at = stack.pop()!
      part.push(at)
      for (const next of near.get(at) ?? []) {
        if (!seen.has(next)) {
          seen.add(next)
          stack.push(next)
        }
      }
    }
    out.push(part.toSorted((a, b) => ids.indexOf(a) - ids.indexOf(b)))
  }
  return out
}

/**
 * Lay a board out in ranks along its connectors, top to bottom or left to right. The gap
 * between ranks holds the longest label, and in a top-to-bottom board items side by side
 * stand far enough apart for the labels of a fan between them. Parts not joined to each
 * other are laid out apart and packed in rows about as wide as a screen; lone items come
 * last, in a grid.
 */
export function layoutBoard(
  boxes: LayoutBox[],
  edges: { from: string; to: string; label?: string }[],
  direction: Direction,
  /** How much wider than needed items side by side stand, so more labels clear each other. */
  loose = 1,
): BoardLayout {
  const byId = new Map(boxes.map((box) => [box.id, box]))
  const known = edges.filter((e) => byId.has(e.from) && byId.has(e.to) && e.from !== e.to)
  const vertical = direction === 'down'
  const widest = Math.max(0, ...known.map((e) => labelWidth(e.label)))
  const narrowest = Math.min(...boxes.map((box) => box.width))
  const laneGap = Math.round(
    (vertical ? Math.max(LANE_GAP, 2 * (widest + 8) - narrowest) : LANE_GAP) * loose,
  )
  const options = {
    direction: vertical ? ('vertical' as const) : ('horizontal' as const),
    rankGap: rankGap(
      known.map((e) => e.label),
      direction,
    ),
    laneGap,
    // Long connectors passing the same gap side by side keep their labels apart: labels
    // stand side by side in a top-to-bottom board, one above the other otherwise.
    edgeGap: !known.some((e) => e.label) ? 16 : vertical ? widest + 8 : LABEL_HEIGHT + 8,
    equalWidth: false,
    straightChains: true,
    grid: 8,
  }

  const blocks: Block[] = []
  const lone: string[] = []
  const dropped = new Set<string>()
  for (const ids of parts(
    boxes.map((box) => box.id),
    known,
  )) {
    if (ids.length === 1) {
      lone.push(ids[0]!)
      continue
    }
    const inside = new Set(ids)
    const layout = layoutGraph(
      ids.map((id) => byId.get(id)!),
      known.filter((e) => inside.has(e.from)),
      options,
    )
    for (const e of layout.dropped) dropped.add(`${e.from}>${e.to}`)
    blocks.push({
      width: layout.width,
      height: layout.height,
      nodes: new Map([...layout.nodes].map(([id, n]) => [id, { x: n.x, y: n.y }])),
      points: new Map(
        layout.edges.filter((e) => e.points.length).map((e) => [`${e.from}>${e.to}`, e.points]),
      ),
    })
  }

  const area =
    blocks.reduce((sum, b) => sum + b.width * b.height, 0) +
    lone.reduce(
      (sum, id) => sum + (byId.get(id)!.width + LANE_GAP) * (byId.get(id)!.height + LANE_GAP),
      0,
    )
  // Rows about 16:9, never narrower than the widest part.
  const rowWidth = Math.max(0, ...blocks.map((b) => b.width), Math.sqrt((area * 16) / 9))

  if (lone.length) {
    const nodes = new Map<string, Point>()
    let x = 0
    let y = 0
    let row = 0
    let width = 0
    for (const id of lone) {
      const box = byId.get(id)!
      if (x > 0 && x + box.width > rowWidth) {
        x = 0
        y += row + LANE_GAP
        row = 0
      }
      nodes.set(id, { x, y })
      x += box.width + LANE_GAP
      width = Math.max(width, x - LANE_GAP)
      row = Math.max(row, box.height)
    }
    blocks.push({ width, height: y + row, nodes, points: new Map() })
  }

  const nodes = new Map<string, Point>()
  const points = new Map<string, Point[]>()
  let x = 0
  let y = 0
  let row = 0
  let width = 0
  for (const block of blocks) {
    if (x > 0 && x + block.width > rowWidth) {
      x = 0
      y += row + PART_GAP
      row = 0
    }
    for (const [id, at] of block.nodes)
      nodes.set(id, { x: Math.round(x + at.x), y: Math.round(y + at.y) })
    for (const [key, through] of block.points) {
      points.set(
        key,
        through.map((p) => ({ x: Math.round(x + p.x), y: Math.round(y + p.y) })),
      )
    }
    x += block.width + PART_GAP
    width = Math.max(width, x - PART_GAP)
    row = Math.max(row, block.height)
  }
  return { nodes, points, dropped, width, height: y + row }
}
