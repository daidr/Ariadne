import { DomainError } from './errors'
import type { ArtifactKind } from './events'
import type { ArtifactPreview, PreviewBox, PreviewTile } from './views'

/**
 * What an artifact's content holds, by kind. A document is Markdown; the other kinds are
 * JSON in the formats of the dou-ui components that show them, so the web passes the
 * stored content to the component as it is.
 */

/** A node of a whiteboard: `@dou-ui/flow` node data with the whiteboard node types. */
export interface WhiteboardNode {
  id: string
  type: string
  x: number
  y: number
  width?: number
  height?: number
  z?: number
  data?: Record<string, unknown>
  [key: string]: unknown
}

export interface WhiteboardEdge {
  id: string
  source: { node: string; port: string }
  target: { node: string; port: string }
  type?: string
  label?: string
  data?: Record<string, unknown>
  [key: string]: unknown
}

/** A whiteboard: a `@dou-ui/flow` `FlowDocument` drawn with `whiteboardKit()`. */
export interface WhiteboardContent {
  version: 1
  nodes: WhiteboardNode[]
  edges: WhiteboardEdge[]
}

/** A widget of a dashboard, as `@dou-ui/dashboard` stores it. */
export interface DashboardWidgetContent {
  id: string
  type: string
  title?: string
  props: Record<string, unknown>
  place?: Record<string, { x: number; y: number; w: number; h: number }>
}

/**
 * A dashboard snapshot: a `@dou-ui/dashboard` `DashboardDocument` plus the data its
 * widgets read, copied in when the snapshot was taken. Each key of `data` is a data source
 * that widgets name in `props.source`; nothing is fetched again when it is shown.
 */
export interface DashboardContent {
  version: 1
  dashboard: { version: 1; widgets: DashboardWidgetContent[] }
  data: Record<string, unknown>
  /** When the data was taken (ms), shown with the dashboard. */
  takenAt?: number
  /**
   * Where a live dashboard's data comes from: a plugin's dashboard source and its
   * settings. Refreshing fetches again into a new version. Without it the data stays
   * what it was given.
   */
  source?: { kind: string; settings: Record<string, unknown> }
}

/**
 * The node types the web's whiteboard registers: `whiteboardKit()`'s, and the plain
 * `default` node from `flowKit()`. Grouping is turned off there, so no `group` nodes.
 */
export const WHITEBOARD_NODE_TYPES = ['shape', 'sticky', 'text', 'ink', 'image', 'default'] as const
/** The dashboard widget types the web registers. */
export const DASHBOARD_WIDGET_TYPES = ['stat', 'text', 'table', 'chart', 'pivot'] as const

/** Big enough for a whiteboard with a few pasted images. */
export const MAX_ARTIFACT_JSON = 8 * 1024 * 1024

export function emptyContent(kind: ArtifactKind): string {
  if (kind === 'whiteboard') return JSON.stringify({ version: 1, nodes: [], edges: [] })
  if (kind === 'dashboard') {
    return JSON.stringify({ version: 1, dashboard: { version: 1, widgets: [] }, data: {} })
  }
  return ''
}

function invalid(reason: string): never {
  throw new DomainError('error.artifactInvalid', { reason })
}

const isObject = (value: unknown): value is Record<string, unknown> =>
  typeof value === 'object' && value !== null && !Array.isArray(value)

const isNumber = (value: unknown): value is number =>
  typeof value === 'number' && Number.isFinite(value)

function parse(content: string): unknown {
  if (content.length > MAX_ARTIFACT_JSON) invalid(`larger than ${MAX_ARTIFACT_JSON} bytes`)
  try {
    return JSON.parse(content)
  } catch {
    return invalid('not JSON')
  }
}

function checkWhiteboard(value: unknown): void {
  if (!isObject(value) || value.version !== 1)
    invalid('a whiteboard is { version: 1, nodes, edges }')
  if (!Array.isArray(value.nodes) || !Array.isArray(value.edges)) {
    invalid('a whiteboard needs nodes and edges arrays')
  }
  const ids = new Set<string>()
  value.nodes.forEach((node: unknown, i) => {
    if (!isObject(node) || typeof node.id !== 'string' || !node.id) invalid(`nodes[${i}] has no id`)
    if (ids.has(node.id)) invalid(`node id "${node.id}" is used twice`)
    ids.add(node.id)
    if (!(WHITEBOARD_NODE_TYPES as readonly unknown[]).includes(node.type)) {
      invalid(
        `node "${node.id}" has type ${String(node.type)}; use ${WHITEBOARD_NODE_TYPES.join(', ')}`,
      )
    }
    if (!isNumber(node.x) || !isNumber(node.y)) invalid(`node "${node.id}" needs numeric x and y`)
    if (node.data !== undefined && !isObject(node.data))
      invalid(`node "${node.id}" data is not an object`)
  })
  const edgeIds = new Set<string>()
  value.edges.forEach((edge: unknown, i) => {
    if (!isObject(edge) || typeof edge.id !== 'string' || !edge.id) invalid(`edges[${i}] has no id`)
    if (edgeIds.has(edge.id)) invalid(`edge id "${edge.id}" is used twice`)
    edgeIds.add(edge.id)
    for (const end of ['source', 'target'] as const) {
      const at = edge[end]
      if (!isObject(at) || typeof at.node !== 'string' || typeof at.port !== 'string') {
        invalid(`edge "${edge.id}" ${end} needs { node, port }`)
      }
      if (!ids.has(at.node)) invalid(`edge "${edge.id}" ${end} names a missing node "${at.node}"`)
    }
  })
}

function checkDashboard(value: unknown): void {
  if (!isObject(value) || value.version !== 1) {
    invalid('a dashboard is { version: 1, dashboard, data }')
  }
  const { dashboard, data } = value
  if (!isObject(dashboard) || dashboard.version !== 1 || !Array.isArray(dashboard.widgets)) {
    invalid('dashboard must be { version: 1, widgets: [] }')
  }
  if (!isObject(data)) invalid('data must be an object of named data sources')
  if (value.takenAt !== undefined && !isNumber(value.takenAt)) invalid('takenAt is not a number')
  if (value.source !== undefined) {
    const { source } = value
    if (!isObject(source) || typeof source.kind !== 'string' || !source.kind) {
      invalid('source must be { kind, settings }')
    }
    if (source.settings !== undefined && !isObject(source.settings)) {
      invalid('source.settings is not an object')
    }
  }
  const ids = new Set<string>()
  dashboard.widgets.forEach((widget: unknown, i) => {
    if (!isObject(widget) || typeof widget.id !== 'string' || !widget.id) {
      invalid(`widgets[${i}] has no id`)
    }
    if (ids.has(widget.id)) invalid(`widget id "${widget.id}" is used twice`)
    ids.add(widget.id)
    if (!(DASHBOARD_WIDGET_TYPES as readonly unknown[]).includes(widget.type)) {
      invalid(
        `widget "${widget.id}" has type ${String(widget.type)}; use ${DASHBOARD_WIDGET_TYPES.join(', ')}`,
      )
    }
    if (!isObject(widget.props)) invalid(`widget "${widget.id}" props is not an object`)
    const source = widget.props.source
    if (source !== undefined && (typeof source !== 'string' || !(source in data))) {
      invalid(`widget "${widget.id}" reads source "${String(source)}", which is not in data`)
    }
    if (widget.place !== undefined) {
      if (!isObject(widget.place)) invalid(`widget "${widget.id}" place is not an object`)
      for (const [breakpoint, place] of Object.entries(widget.place)) {
        if (
          !isObject(place) ||
          ![place.x, place.y, place.w, place.h].every(
            (n) => Number.isInteger(n) && (n as number) >= 0,
          )
        ) {
          invalid(`widget "${widget.id}" place.${breakpoint} needs whole x, y, w, h`)
        }
      }
    }
  })
}

const EXCERPT = 160
/** About what a card's preview shows of a document. */
const LEAD = 600
/** How many nodes and edges, or widgets, a preview keeps. */
const PREVIEW_ITEMS = 60
const PREVIEW_TEXT = 40

/** Sizes `whiteboardKit()` gives a node that has none of its own. */
const NODE_SIZE: Record<string, [number, number]> = {
  shape: [160, 100],
  sticky: [160, 160],
  text: [200, 40],
  image: [240, 180],
}

const short = (text: string) =>
  text.length > PREVIEW_TEXT ? `${text.slice(0, PREVIEW_TEXT)}…` : text

/** The opening of a document, cut at a line end so no mark is left half open. */
function leadOf(content: string): string {
  if (content.length <= LEAD) return content
  const cut = content.lastIndexOf('\n', LEAD)
  const lead = content.slice(0, cut > LEAD / 2 ? cut : LEAD)
  // An unclosed code fence would swallow the rest of the preview as code.
  return (lead.match(/^\s*```/gm)?.length ?? 0) % 2 ? `${lead}\n\`\`\`` : lead
}

function boxesOf(
  board: WhiteboardContent,
): Pick<Extract<ArtifactPreview, { kind: 'whiteboard' }>, 'boxes' | 'lines'> {
  const boxes: PreviewBox[] = []
  const centres = new Map<string, [number, number]>()
  for (const node of board.nodes.slice(0, PREVIEW_ITEMS)) {
    const [w, h] = NODE_SIZE[node.type] ?? [160, 60]
    const text = typeof node.data?.text === 'string' && node.data.text.trim()
    const box: PreviewBox = {
      type: node.type,
      x: node.x,
      y: node.y,
      w: isNumber(node.width) && node.width > 0 ? node.width : w,
      h: isNumber(node.height) && node.height > 0 ? node.height : h,
      text: text ? short(text) : null,
      shape: typeof node.data?.shape === 'string' ? node.data.shape : null,
    }
    boxes.push(box)
    centres.set(node.id, [box.x + box.w / 2, box.y + box.h / 2])
  }
  const lines: [number, number, number, number][] = []
  for (const edge of board.edges) {
    const from = centres.get(edge.source.node)
    const to = centres.get(edge.target.node)
    if (from && to) lines.push([...from, ...to])
    if (lines.length >= PREVIEW_ITEMS) break
  }
  return { boxes, lines }
}

/** The widgets on the 12-column grid; ones without a place stack below, two to a row. */
function tilesOf(value: DashboardContent): PreviewTile[] {
  const widgets = value.dashboard.widgets.slice(0, PREVIEW_ITEMS)
  const tiles: PreviewTile[] = []
  let bottom = 0
  for (const widget of widgets) {
    const place = widget.place?.lg
    if (!place) continue
    tiles.push({ type: widget.type, title: widget.title ?? null, ...place })
    bottom = Math.max(bottom, place.y + place.h)
  }
  let free = 0
  for (const widget of widgets) {
    if (widget.place?.lg) continue
    tiles.push({
      type: widget.type,
      title: widget.title ?? null,
      x: (free % 2) * 6,
      y: bottom + Math.floor(free / 2) * 3,
      w: 6,
      h: 3,
    })
    free++
  }
  return tiles
}

/** A short look at stored content, for lists; content is assumed checked. */
export function previewOf(kind: ArtifactKind, content: string): ArtifactPreview {
  try {
    if (kind === 'whiteboard') {
      const board = JSON.parse(content) as WhiteboardContent
      return {
        kind,
        items: board.nodes.length,
        connectors: board.edges.length,
        texts: board.nodes
          .map((n) => n.data?.text)
          .filter((t): t is string => typeof t === 'string' && !!t.trim())
          .slice(0, 6),
        ...boxesOf(board),
      }
    }
    if (kind === 'dashboard') {
      const value = JSON.parse(content) as DashboardContent
      return {
        kind,
        widgets: value.dashboard.widgets.length,
        takenAt: value.takenAt ?? null,
        titles: value.dashboard.widgets
          .map((w) => w.title)
          .filter((t): t is string => !!t)
          .slice(0, 6),
        tiles: tilesOf(value),
      }
    }
  } catch {
    // Older or damaged content: show it as empty rather than fail the list.
    return kind === 'whiteboard'
      ? { kind, items: 0, connectors: 0, texts: [], boxes: [], lines: [] }
      : { kind: 'dashboard', widgets: 0, takenAt: null, titles: [], tiles: [] }
  }
  // Markdown without its marks, on one line.
  const text = content
    .replace(/```[\s\S]*?```/g, ' ')
    .replace(/!?\[([^\]]*)\]\([^)]*\)/g, '$1')
    .replace(/^\s*(?:[-+]|\d+\.)\s+/gm, ' ')
    .replace(/^\s*-{3,}\s*$/gm, ' ')
    .replace(/[#>*_`~|]+/g, ' ')
    .replace(/\s+/g, ' ')
    .trim()
  return {
    kind: 'document',
    excerpt: text.length > EXCERPT ? `${text.slice(0, EXCERPT)}…` : text,
    lead: leadOf(content),
  }
}

/** Refuse content the viewer for its kind could not show. */
export function checkArtifactContent(kind: ArtifactKind, content: string): void {
  if (kind === 'whiteboard') checkWhiteboard(parse(content))
  else if (kind === 'dashboard') checkDashboard(parse(content))
}
