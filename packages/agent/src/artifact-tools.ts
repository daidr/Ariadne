import type { ArtifactKind, ArtifactView, DashboardContent, WhiteboardContent } from '@ariadne/core'
import { plainText } from './markdown-code'
import { boardFromItems, boardProblems, boardSummary, mergeBoard } from './whiteboard'
import { dashboardFromWidgets, mergeDashboard } from './dashboard'
import { dashboardProblems } from './dashboard-check'
import {
  checkSourceSettings,
  describeSources,
  fetchDashboardData,
  refreshDashboard,
} from './dashboard-sources'
import { createTranslator } from '@ariadne/i18n'
import {
  json,
  optionalInt,
  optionalStr,
  str,
  ToolInputError,
  type Tool,
  type ToolContext,
} from './tool-helpers'

/** The artifact, if it belongs to this project and is of the kind the tool works on. */
function ownArtifact(ctx: ToolContext, artifactId: string, kind?: ArtifactKind): ArtifactView {
  const artifact = ctx.app.getArtifact(artifactId)
  if (artifact.projectId !== ctx.projectId) {
    throw new ToolInputError(`No artifact ${artifactId} in this project.`)
  }
  if (kind && artifact.kind !== kind) {
    throw new ToolInputError(`${artifactId} is a ${artifact.kind}, not a ${kind}.`)
  }
  return artifact
}

function baseVersionOf(args: Record<string, unknown>): number {
  const baseVersion = optionalInt(args, 'baseVersion')
  if (baseVersion === null) {
    throw new ToolInputError('"baseVersion" is required: read the artifact first')
  }
  return baseVersion
}

function nodeIdOf(args: Record<string, unknown>): string | null {
  return optionalStr(args, 'nodeId') || null
}

function arrayOf(args: Record<string, unknown>, key: string): Record<string, unknown>[] {
  const value = args[key]
  if (value === undefined || value === null) return []
  if (!Array.isArray(value) || !value.every((v) => typeof v === 'object' && v !== null)) {
    throw new ToolInputError(`"${key}" must be an array of objects`)
  }
  return value as Record<string, unknown>[]
}

function stringsOf(args: Record<string, unknown>, key: string): string[] {
  const value = args[key]
  if (value === undefined || value === null) return []
  if (!Array.isArray(value) || !value.every((v) => typeof v === 'string')) {
    throw new ToolInputError(`"${key}" must be an array of strings`)
  }
  return value as string[]
}

const NODE_ID = {
  type: 'string',
  description: 'The plan node the artifact belongs to, if any.',
} as const

// ---- documents ----

const createDocument: Tool = {
  spec: {
    name: 'create_document',
    description:
      'Create a document in the project, written in Markdown: a plan, spec, report or notes the user will read and may edit. Long results belong in a document rather than a message; say in one line that it exists.',
    parameters: {
      type: 'object',
      properties: {
        title: { type: 'string' },
        content: { type: 'string', description: 'Markdown.' },
        nodeId: NODE_ID,
      },
      required: ['title', 'content'],
    },
  },
  run(args, ctx) {
    const artifact = ctx.app.createArtifact(ctx.projectId, {
      kind: 'document',
      title: str(args, 'title'),
      content: str(args, 'content'),
      nodeId: nodeIdOf(args),
      author: ctx.conversationId,
    })
    return json({ artifactId: artifact.id, version: artifact.version })
  },
}

const updateDocument: Tool = {
  spec: {
    name: 'update_document',
    description:
      "Save a new version of a document with its full Markdown content. baseVersion is the version you read: if anyone saved since, the call fails and you read it again, so the user's edits are never lost. Comments are tied to the text they quote: change a commented passage only when that is what its comment asks, then reply and resolve_artifact_comment, since the comment no longer finds its text.",
    parameters: {
      type: 'object',
      properties: {
        artifactId: { type: 'string' },
        content: { type: 'string', description: 'The whole document, in Markdown.' },
        reason: { type: 'string', description: 'What changed, in one line.' },
        title: { type: 'string' },
        baseVersion: { type: 'integer' },
      },
      required: ['artifactId', 'content', 'reason', 'baseVersion'],
    },
  },
  run(args, ctx) {
    const artifactId = str(args, 'artifactId')
    ownArtifact(ctx, artifactId, 'document')
    const baseVersion = baseVersionOf(args)
    const title = optionalStr(args, 'title')
    const artifact = ctx.app.reviseArtifact(artifactId, {
      content: str(args, 'content'),
      reason: str(args, 'reason'),
      author: ctx.conversationId,
      ...(title ? { title } : {}),
      baseVersion,
    })
    return json({ artifactId, version: artifact.version })
  },
}

// ---- whiteboards ----

const NEW_ITEM_SCHEMA = {
  type: 'object',
  properties: {
    id: { type: 'string', description: 'Your own short id, used by connectors and later edits.' },
    kind: { type: 'string', enum: ['rect', 'ellipse', 'diamond', 'sticky', 'text'] },
    text: {
      type: 'string',
      description:
        'A short title, a line break, then a few words at most. The box grows to fit it.',
    },
    tone: { type: 'string', enum: ['neutral', 'primary', 'success', 'warning', 'danger'] },
    x: { type: 'number', description: 'Only to sketch a picture; leave out for the layout.' },
    y: { type: 'number' },
  },
  required: ['id'],
} as const

const ITEM_SCHEMA = {
  type: 'object',
  properties: {
    ...NEW_ITEM_SCHEMA.properties,
    x: { type: 'number', description: 'Only to move an item that is on the board already.' },
    y: { type: 'number' },
    width: { type: 'number' },
    height: { type: 'number' },
  },
  required: ['id'],
} as const

const DIRECTION = {
  type: 'string',
  enum: ['down', 'right'],
  description:
    'Which way connectors run: top to bottom or left to right. A new board runs top to bottom unless you say (a long left-to-right board gets too wide to read); a board keeps its way when you leave this out.',
} as const

const CONNECTOR_SCHEMA = {
  type: 'object',
  properties: {
    id: { type: 'string' },
    from: { type: 'string', description: 'Item id.' },
    to: { type: 'string', description: 'Item id.' },
    label: { type: 'string', description: 'A few words; the gap between items grows to hold it.' },
    route: { type: 'string', enum: ['straight', 'elbow', 'curve'] },
    dash: { type: 'string', enum: ['solid', 'dashed', 'dotted', 'dash-dot'] },
    arrow: { type: 'string', enum: ['none', 'end', 'both'] },
  },
  required: ['from', 'to'],
} as const

/** What looks wrong on a board, for the model to fix; nothing when it looks right. */
function problemsOf(board: WhiteboardContent) {
  const problems = boardProblems(board)
  return problems.length ? { problems } : {}
}

function directionOf(args: Record<string, unknown>): 'right' | 'down' | undefined {
  const value = args.direction
  if (value === undefined || value === null) return undefined
  if (value !== 'right' && value !== 'down') {
    throw new ToolInputError('"direction" must be "right" or "down"')
  }
  return value
}

const createWhiteboard: Tool = {
  spec: {
    name: 'create_whiteboard',
    description:
      'Create a whiteboard in the project: shapes, sticky notes and text joined by arrows, for a sketch of an architecture, a flow, a mind map or options side by side. The user can move things, draw and add their own. Items are laid out for you in ranks along the connectors, sized to their text, with room for connector labels; items not connected to anything are set in rows. Give x and y only to sketch a picture the layout cannot (a comparison side by side, a ring); the picture is then stretched until nothing overlaps. Keep a board to about 15 items and give connectors only where the arrow says something. The result lists problems the board still has (overlaps, labels on items, connectors through items, text cut off); fix them with update_whiteboard.',
    parameters: {
      type: 'object',
      properties: {
        title: { type: 'string' },
        items: { type: 'array', items: NEW_ITEM_SCHEMA },
        connectors: { type: 'array', items: CONNECTOR_SCHEMA },
        direction: DIRECTION,
        nodeId: NODE_ID,
      },
      required: ['title', 'items'],
    },
  },
  run(args, ctx) {
    const board = boardFromItems(
      arrayOf(args, 'items'),
      arrayOf(args, 'connectors'),
      directionOf(args),
    )
    const artifact = ctx.app.createArtifact(ctx.projectId, {
      kind: 'whiteboard',
      title: str(args, 'title'),
      content: JSON.stringify(board),
      nodeId: nodeIdOf(args),
      author: ctx.conversationId,
    })
    return json({ artifactId: artifact.id, version: artifact.version, ...problemsOf(board) })
  },
}

const updateWhiteboard: Tool = {
  spec: {
    name: 'update_whiteboard',
    description:
      "Change a whiteboard. Items and connectors are matched by id: a known id changes only the fields you give (an item keeps its place unless you give x and y), a new id is added and placed next to what it connects from. Everything else on the board stays, including the user's drawings and images. With relayout, every item but the user's drawings and images is laid out again, as on a new board; use it after adding several items, not to undo where the user moved things. The result lists problems the board still has; read_artifact lists them too, also after the user changed the board. baseVersion is the version you read; if anyone saved since, read it again.",
    parameters: {
      type: 'object',
      properties: {
        artifactId: { type: 'string' },
        baseVersion: { type: 'integer' },
        reason: { type: 'string', description: 'What changed, in one line.' },
        title: { type: 'string' },
        items: { type: 'array', items: ITEM_SCHEMA },
        connectors: { type: 'array', items: CONNECTOR_SCHEMA },
        removeItems: { type: 'array', items: { type: 'string' } },
        removeConnectors: { type: 'array', items: { type: 'string' } },
        relayout: { type: 'boolean' },
        direction: DIRECTION,
      },
      required: ['artifactId', 'baseVersion', 'reason'],
    },
  },
  run(args, ctx) {
    const artifactId = str(args, 'artifactId')
    const current = ownArtifact(ctx, artifactId, 'whiteboard')
    const baseVersion = baseVersionOf(args)
    const board = mergeBoard(JSON.parse(current.content) as WhiteboardContent, {
      items: arrayOf(args, 'items'),
      connectors: arrayOf(args, 'connectors'),
      removeItems: stringsOf(args, 'removeItems'),
      removeConnectors: stringsOf(args, 'removeConnectors'),
      relayout: args.relayout === true,
      direction: directionOf(args),
    })
    const title = optionalStr(args, 'title')
    const artifact = ctx.app.reviseArtifact(artifactId, {
      content: JSON.stringify(board),
      reason: str(args, 'reason'),
      author: ctx.conversationId,
      ...(title ? { title } : {}),
      baseVersion,
    })
    return json({ artifactId, version: artifact.version, ...problemsOf(board) })
  },
}

// ---- dashboards ----

const WIDGET_SCHEMA = {
  type: 'object',
  properties: {
    id: { type: 'string' },
    type: { type: 'string', enum: ['stat', 'text', 'table', 'chart', 'pivot'] },
    title: { type: 'string' },
    props: {
      type: 'object',
      description:
        'By type; field names may be dotted paths like total.amount, read in the data set named by source. stat: { value: number, prefix?, suffix?, precision? } for a number you give, or { source, field, trend?, delta?, prefix?, suffix?, precision? } to read it: field, trend and delta are all field names, never numbers (delta names a field holding the change as a ratio, 0.125 shows +12.5%; trend names a list of numbers). text: { text, size?: sm|md|lg }. table: { source, path?, columns?: [{ key, title? }] }. chart: { source, path?, kind: line|area|bar|pie, x: field, y: [fields], labels?: { field: name }, stacked?, horizontal? }. pivot: { source, path?, config: { rows: [fields], columns: [fields], values: [field | { field, aggregate: sum|count|avg|min|max|distinct|median, label? }] } }. No other props.',
    },
    w: { type: 'integer', description: 'Width in columns, 1 to 12.' },
    h: { type: 'integer', description: 'Height in rows of 80px.' },
  },
  required: ['id'],
} as const

const DATA_SCHEMA = {
  type: 'object',
  description:
    'Named data sources the widgets read with props.source: usually an array of row objects, or one object for stat widgets.',
} as const

const SOURCE_SCHEMA = {
  type: 'object',
  description:
    'Make the dashboard live: a plugin source (list_dashboard_sources) fetches its data now and on every refresh, and the data sets it returns replace those of the same name. Leave it out for a snapshot of the data you give.',
  properties: { kind: { type: 'string' }, settings: { type: 'object' } },
  required: ['kind'],
} as const

/** What a dashboard will show wrong, for the model to fix; nothing when it looks right. */
function dashboardProblemsOf(content: DashboardContent) {
  const problems = dashboardProblems(content)
  return problems.length ? { problems } : {}
}

/** The source the model named, checked, and the data it gives now. */
async function fetchSource(
  value: unknown,
  ctx: ToolContext,
  projectId: string,
): Promise<{
  source: { kind: string; settings: Record<string, unknown> }
  data: Record<string, unknown>
}> {
  const input = (value ?? {}) as { kind?: unknown; settings?: unknown }
  const sources = ctx.dashboardSources ?? new Map()
  const source = typeof input.kind === 'string' ? sources.get(input.kind) : undefined
  if (!source) {
    const kinds = [...sources.keys()]
    throw new ToolInputError(
      kinds.length
        ? `"source.kind" must be one of: ${kinds.join(', ')}`
        : 'No plugin provides live dashboard data; give the data itself instead.',
    )
  }
  const checked = checkSourceSettings(source, input.settings)
  if (!checked.ok) throw new ToolInputError(checked.problem)
  let data: Record<string, unknown>
  try {
    data = await fetchDashboardData(source, checked.settings, { projectId, signal: ctx.signal })
  } catch (error) {
    throw new ToolInputError(`The source could not fetch: ${(error as Error).message}`)
  }
  return { source: { kind: source.kind, settings: checked.settings }, data }
}

const listDashboardSources: Tool = {
  readOnly: true,
  spec: {
    name: 'list_dashboard_sources',
    description:
      'List the live data sources plugins offer for dashboards: what each gives, its settings, and the data sets it returns for widgets to read.',
    parameters: { type: 'object', properties: {} },
  },
  run(_args, ctx) {
    return json(describeSources(ctx.dashboardSources ?? new Map()))
  },
}

const refreshDashboardTool: Tool = {
  spec: {
    name: 'refresh_dashboard',
    description:
      "Fetch a live dashboard's data again from its source, stored as a new version. A snapshot dashboard has nothing to fetch: update_dashboard with new data instead.",
    parameters: {
      type: 'object',
      properties: { artifactId: { type: 'string' } },
      required: ['artifactId'],
    },
  },
  async run(args, ctx) {
    const artifactId = str(args, 'artifactId')
    ownArtifact(ctx, artifactId, 'dashboard')
    const artifact = await refreshDashboard(
      ctx.app,
      ctx.dashboardSources ?? new Map(),
      artifactId,
      {
        author: ctx.conversationId,
        reason: createTranslator(ctx.locale)('artifact.dashboard.refreshed'),
        signal: ctx.signal,
      },
    )
    return json({ artifactId, version: artifact.version })
  },
}

const createDashboard: Tool = {
  spec: {
    name: 'create_dashboard',
    description:
      'Create a dashboard in the project: numbers, tables, charts and pivot tables over data you gathered. By default it is a snapshot: the data you give is stored with it and never fetched again, and it shows when it was taken; to bring it up to date, update_dashboard with new data. With source, a plugin fetches the data instead and the dashboard can be refreshed (refresh_dashboard, or the refresh button the user sees). Widgets are placed in order on a 12-column grid unless you give w and h. Props that do not fit their widget type are refused, nothing saved; the result lists problems the dashboard still has (data sets, fields or rows its widgets read that are not there); fix them with update_dashboard.',
    parameters: {
      type: 'object',
      properties: {
        title: { type: 'string' },
        widgets: { type: 'array', items: WIDGET_SCHEMA },
        data: DATA_SCHEMA,
        source: SOURCE_SCHEMA,
        nodeId: NODE_ID,
      },
      required: ['title', 'widgets'],
    },
  },
  async run(args, ctx) {
    const data = args.data ?? {}
    if (typeof data !== 'object' || Array.isArray(data) || data === null) {
      throw new ToolInputError('"data" must be an object of named data sources')
    }
    const live =
      args.source === undefined ? null : await fetchSource(args.source, ctx, ctx.projectId)
    const content = dashboardFromWidgets(
      arrayOf(args, 'widgets'),
      { ...(data as Record<string, unknown>), ...live?.data },
      Date.now(),
    )
    if (live) content.source = live.source
    const artifact = ctx.app.createArtifact(ctx.projectId, {
      kind: 'dashboard',
      title: str(args, 'title'),
      content: JSON.stringify(content),
      nodeId: nodeIdOf(args),
      author: ctx.conversationId,
    })
    return json({
      artifactId: artifact.id,
      version: artifact.version,
      ...dashboardProblemsOf(content),
    })
  },
}

const updateDashboard: Tool = {
  spec: {
    name: 'update_dashboard',
    description:
      "Change a dashboard. Widgets are matched by id: a known id changes only the fields you give (props are replaced as a whole; the widget keeps its place), a new id is added at the end. data sets the named sources you give and keeps the others; giving data marks the snapshot as taken now. The user's own layout and widgets stay. Props that do not fit their widget type are refused; the result lists problems the dashboard still has, and read_artifact lists them too. baseVersion is the version you read; if anyone saved since, read it again.",
    parameters: {
      type: 'object',
      properties: {
        artifactId: { type: 'string' },
        baseVersion: { type: 'integer' },
        reason: { type: 'string', description: 'What changed, in one line.' },
        title: { type: 'string' },
        widgets: { type: 'array', items: WIDGET_SCHEMA },
        removeWidgets: { type: 'array', items: { type: 'string' } },
        data: DATA_SCHEMA,
        removeData: { type: 'array', items: { type: 'string' } },
        source: {
          ...SOURCE_SCHEMA,
          description: `${SOURCE_SCHEMA.description} null turns a live dashboard back into a snapshot of its current data.`,
        },
      },
      required: ['artifactId', 'baseVersion', 'reason'],
    },
  },
  async run(args, ctx) {
    const artifactId = str(args, 'artifactId')
    const current = ownArtifact(ctx, artifactId, 'dashboard')
    const baseVersion = baseVersionOf(args)
    const data = args.data
    if (data !== undefined && (typeof data !== 'object' || Array.isArray(data) || data === null)) {
      throw new ToolInputError('"data" must be an object of named data sources')
    }
    const live =
      args.source === undefined || args.source === null
        ? null
        : await fetchSource(args.source, ctx, current.projectId)
    const fetched = live || data ? { ...(data as Record<string, unknown>), ...live?.data } : null
    const content = mergeDashboard(
      JSON.parse(current.content) as DashboardContent,
      {
        widgets: arrayOf(args, 'widgets'),
        removeWidgets: stringsOf(args, 'removeWidgets'),
        ...(fetched ? { data: fetched } : {}),
        removeData: stringsOf(args, 'removeData'),
      },
      Date.now(),
    )
    if (live) content.source = live.source
    else if (args.source === null) delete content.source
    const title = optionalStr(args, 'title')
    const artifact = ctx.app.reviseArtifact(artifactId, {
      content: JSON.stringify(content),
      reason: str(args, 'reason'),
      author: ctx.conversationId,
      ...(title ? { title } : {}),
      baseVersion,
    })
    return json({ artifactId, version: artifact.version, ...dashboardProblemsOf(content) })
  },
}

// ---- any kind ----

const readArtifact: Tool = {
  readOnly: true,
  spec: {
    name: 'read_artifact',
    description:
      "Read an artifact of the project at its newest version or an older one: a document's Markdown, a whiteboard's items and connectors (drawings and images are listed without their pixels), or a dashboard's widgets and data.",
    parameters: {
      type: 'object',
      properties: { artifactId: { type: 'string' }, version: { type: 'integer' } },
      required: ['artifactId'],
    },
  },
  run(args, ctx) {
    const version = optionalInt(args, 'version')
    const artifact = ctx.app.getArtifact(str(args, 'artifactId'), version ?? undefined)
    if (artifact.projectId !== ctx.projectId) {
      return { content: `No artifact ${artifact.id} in this project.`, isError: true }
    }
    const head = {
      artifactId: artifact.id,
      kind: artifact.kind,
      title: artifact.title,
      version: artifact.version,
      updatedBy: artifact.updatedBy,
    }
    if (artifact.kind === 'whiteboard') {
      const content = JSON.parse(artifact.content) as WhiteboardContent
      return json({ ...head, ...boardSummary(content), ...problemsOf(content) })
    }
    if (artifact.kind === 'dashboard') {
      const content = JSON.parse(artifact.content) as DashboardContent
      return json({ ...head, content, ...dashboardProblemsOf(content) })
    }
    // Comments are on the newest text; an older version is read without them.
    if (version !== null && version !== ctx.app.getArtifact(artifact.id).version) {
      return json({ ...head, content: artifact.content })
    }
    return json({ ...head, content: artifact.content, ...commentsOf(ctx, artifact.id) })
  },
}

/** Who wrote a comment, as the model reads it: the user, you, the coordinator or a thread. */
function commenter(ctx: ToolContext, author: string): string {
  if (author === 'user') return 'user'
  if (author === ctx.conversationId) return 'you'
  if (author === ctx.app.getProject(ctx.projectId).coordinatorId) return 'coordinator'
  try {
    return `thread "${ctx.app.getThread(author).title}"`
  } catch {
    return 'thread'
  }
}

/** The open comment threads on a document, and how many were resolved. */
function commentsOf(ctx: ToolContext, artifactId: string) {
  const threads = ctx.app.listArtifactComments(artifactId)
  const open = threads.filter((thread) => !thread.resolved)
  if (!threads.length) return {}
  return {
    comments: open.map((thread) => ({
      commentThread: thread.id,
      quote: thread.quote,
      comments: thread.comments.map((c) => ({ by: commenter(ctx, c.author), body: c.body })),
    })),
    resolvedComments: threads.length - open.length,
  }
}

const commentArtifact: Tool = {
  spec: {
    name: 'comment_artifact',
    description:
      "Comment on a document's text, as the user does in the document: reply in a comment thread (commentThread), or start one on a short passage of its text (quote, as the reader sees it, without Markdown marks). Keep comments short. Answer the user's comments here, not in a chat message.",
    parameters: {
      type: 'object',
      properties: {
        artifactId: { type: 'string' },
        commentThread: { type: 'string', description: 'The comment thread to reply in.' },
        quote: { type: 'string', description: 'The text a new comment thread is about.' },
        body: { type: 'string', description: 'Plain text.' },
      },
      required: ['artifactId', 'body'],
    },
  },
  run(args, ctx) {
    const artifactId = str(args, 'artifactId')
    const artifact = ownArtifact(ctx, artifactId, 'document')
    const threadId = optionalStr(args, 'commentThread')
    const body = str(args, 'body')
    const commentId = `cmt_${crypto.randomUUID()}`
    if (threadId) {
      ctx.app.commentOnArtifact(artifactId, ctx.conversationId, {
        type: 'reply',
        threadId,
        commentId,
        body,
      })
      return json({ commentThread: threadId })
    }
    const quote = optionalStr(args, 'quote')?.trim()
    if (!quote) throw new ToolInputError('Give commentThread to reply, or quote to start one.')
    // Matched as the page marks it: the text the reader sees, without Markdown marks.
    if (!plainText(artifact.content).includes(plainText(quote))) {
      throw new ToolInputError(
        'The quote is not in the document. Copy the text as the reader sees it, without Markdown marks; if it still fails, reply in an existing comment thread or write in chat instead.',
      )
    }
    const created = `cth_${crypto.randomUUID()}`
    ctx.app.commentOnArtifact(artifactId, ctx.conversationId, {
      type: 'create',
      threadId: created,
      commentId,
      quote: plainText(quote),
      body,
    })
    return json({ commentThread: created })
  },
}

const resolveArtifactComment: Tool = {
  spec: {
    name: 'resolve_artifact_comment',
    description:
      "Mark a document's comment thread resolved once it is settled, for example after you made the change it asked for. The user can reopen it.",
    parameters: {
      type: 'object',
      properties: { artifactId: { type: 'string' }, commentThread: { type: 'string' } },
      required: ['artifactId', 'commentThread'],
    },
  },
  run(args, ctx) {
    const artifactId = str(args, 'artifactId')
    ownArtifact(ctx, artifactId, 'document')
    const threadId = str(args, 'commentThread')
    ctx.app.commentOnArtifact(artifactId, ctx.conversationId, { type: 'resolve', threadId })
    return json({ commentThread: threadId, resolved: true })
  },
}

const listArtifacts: Tool = {
  readOnly: true,
  spec: {
    name: 'list_artifacts',
    description: "List the project's documents, whiteboards and dashboards, newest first.",
    parameters: { type: 'object', properties: {} },
  },
  run(_args, ctx) {
    return json(
      ctx.app.listArtifacts(ctx.projectId).map((a) => ({
        artifactId: a.id,
        kind: a.kind,
        title: a.title,
        version: a.version,
        nodeId: a.nodeId,
        updatedBy: a.updatedBy,
        ...(a.openComments ? { openComments: a.openComments } : {}),
      })),
    )
  },
}

export const artifactTools: Tool[] = [
  createDocument,
  updateDocument,
  createWhiteboard,
  updateWhiteboard,
  createDashboard,
  updateDashboard,
  refreshDashboardTool,
  listDashboardSources,
  readArtifact,
  listArtifacts,
  commentArtifact,
  resolveArtifactComment,
]
