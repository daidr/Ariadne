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
 * The node types the web's whiteboard registers: `whiteboardKit()`'s, `flowKit()`'s plain
 * `default` node and `group`, and Ariadne's `option`: a frame holding one option the
 * user can pick, with what shows it inside (`data: { title, note?, chosen? }`), and
 * `pending`: a placeholder for what an agent will put there (`data: { text }`).
 */
export const WHITEBOARD_NODE_TYPES = [
  'shape',
  'sticky',
  'text',
  'ink',
  'image',
  'default',
  'group',
  'option',
  'pending',
] as const

/**
 * The dashboard widget types the web registers; `pending` is a placeholder for a widget
 * an agent will put there (`props: { text }`).
 */
export const DASHBOARD_WIDGET_TYPES = [
  'stat',
  'text',
  'table',
  'chart',
  'pivot',
  'pending',
] as const
