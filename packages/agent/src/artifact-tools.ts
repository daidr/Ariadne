import type { ArtifactKind, ArtifactView, DashboardContent, WhiteboardContent } from '@ariadne/core'
import { SharedArtifacts, type LiveWrite, type Writer } from './shared-artifacts'
import { applyDocumentEdits, editsSoFar, rewriteSoFar } from './document-writing'
import { isOpen } from './partial-json'
import { anchorWhere } from './artifact-feedback'
import { OPTION_SCHEMA, placeOptions } from './whiteboard-options'
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
  type LiveCall,
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

/** Whether the artifact is written through its room, where people may be editing it now. */
function sharedIn(
  ctx: ToolContext,
  artifact: ArtifactView,
): ctx is ToolContext & { shared: SharedArtifacts } {
  return !!ctx.shared && SharedArtifacts.shared(artifact.kind)
}

/**
 * The artifact as an agent reads it to change it: its newest version, with what the user
 * is changing in its room saved first so the version holds it.
 */
function toChange(ctx: ToolContext, artifactId: string, kind?: ArtifactKind): ArtifactView {
  const artifact = ownArtifact(ctx, artifactId, kind)
  if (!sharedIn(ctx, artifact)) return artifact
  // Shown while the call was written: the change is worked out from where that began.
  const writing = ctx.shared.writing(artifactId, ctx.conversationId)
  if (writing) return { ...artifact, content: writing.base, version: writing.version }
  ctx.shared.save(artifactId)
  return ctx.app.getArtifact(artifactId)
}

/** The agent's name at its cursor, as the artifact's versions name it. */
function writerOf(ctx: ToolContext): Writer {
  if (ctx.role === 'coordinator') {
    return { name: createTranslator(ctx.locale)('artifact.by.coordinator') }
  }
  try {
    return { name: ctx.app.getThread(ctx.conversationId).title }
  } catch {
    return { name: createTranslator(ctx.locale)('artifact.by.coordinator') }
  }
}

/**
 * Run a call that changes an artifact; when it fails, what was shown of it while it was
 * written is taken back at once.
 */
async function settled<T>(ctx: ToolContext, artifactId: string, run: () => T | Promise<T>) {
  try {
    return await run()
  } catch (error) {
    ctx.shared?.writing(artifactId, ctx.conversationId)?.cancel()
    throw error
  }
}

/**
 * Shows a call that changes an artifact edited together while it is written: once the
 * artifact is named and the change has begun, `next` gives the content as far as the
 * arguments go (`null` or a throw: nothing to show for this piece).
 */
function showWriting(
  ctx: ToolContext,
  kind: ArtifactKind,
  begun: (args: Record<string, unknown>) => boolean,
  next: (
    args: Record<string, unknown>,
    open: Parameters<LiveCall['input']>[1],
    base: string,
  ) => string | null,
): LiveCall {
  // `null`: it cannot be shown (nobody watching, another writing going on).
  let writing: LiveWrite | null | undefined
  return {
    input(args, open) {
      if (writing === null) return
      if (!writing) {
        const id = args.artifactId
        if (typeof id !== 'string' || isOpen(open, 'artifactId') || !begun(args)) return
        const artifact = ownArtifact(ctx, id, kind)
        writing =
          (sharedIn(ctx, artifact) && ctx.shared.live(id, ctx.conversationId, writerOf(ctx))) ||
          null
        if (!writing) return
      }
      let content: string | null = null
      try {
        content = next(args, open, writing.base)
      } catch {
        // A piece that does not hold together yet: the next one may.
      }
      if (content !== null) writing.write(content)
    },
    cancel: () => writing?.cancel(),
  }
}

/**
 * Save the agent's new content. Edited together, it goes into the room as the changes
 * from the version the agent read (`baseVersion`) to its content, so what others changed
 * since, there or elsewhere, stays. Otherwise the version must be the one read.
 */
function save(
  ctx: ToolContext,
  artifact: ArtifactView,
  content: string,
  input: { reason: string; title?: string | undefined; baseVersion?: number },
): ArtifactView {
  const title = input.title ? { title: input.title } : {}
  if (!sharedIn(ctx, artifact)) {
    return ctx.app.reviseArtifact(artifact.id, {
      content,
      reason: input.reason,
      author: ctx.conversationId,
      ...title,
      ...(input.baseVersion !== undefined ? { baseVersion: input.baseVersion } : {}),
    })
  }
  // Shown while written from the newest version: a change made from an older one is not
  // what was shown, so that is taken back and the change merged in as usual.
  const writing = ctx.shared.writing(artifact.id, ctx.conversationId)
  if (writing && input.baseVersion !== undefined && input.baseVersion !== writing.version) {
    writing.cancel()
  }
  const base =
    input.baseVersion === undefined
      ? undefined
      : ctx.app.getArtifact(artifact.id, input.baseVersion).content
  return ctx.shared.write(artifact.id, ctx.conversationId, content, {
    reason: input.reason,
    writer: writerOf(ctx),
    ...title,
    ...(base === undefined ? {} : { base }),
  })
}

const NODE_ID = {
  type: 'string',
  description: 'The plan node the artifact belongs to, if any.',
} as const

// ---- documents ----

const PENDING_HINT =
  'A part you have not written yet can stand as a placeholder, on a line of its own: <pending>what will be here, in a few words</pending>. The user sees it in grey, as an outline of what is coming.'

const createDocument: Tool = {
  spec: {
    name: 'create_document',
    description: `Create a document in the project, written in Markdown: a plan, spec, report or notes the user will read and may edit. Long results belong in a document rather than a message; say in one line that it exists. For a long document, create it as its outline first (the title, the headings you are sure of, and a placeholder for each part), then write the parts one at a time with update_document edits: the user watches it fill in instead of waiting for all of it. ${PENDING_HINT}`,
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
    description: `Change a document. With edits, replace parts of it: each "old" is text of the document as read_artifact gives it (a placeholder line, a paragraph, a few lines; it must occur once) and "new" is what goes there; use this to write a part, fill a placeholder or fix a passage. With content, save its full Markdown again, for a change all over; baseVersion is then the version you read, and only what you changed from it is written. Either way the user may be editing it at the same time and their edits stay, and they see your change land as you write it. Comments are tied to the text they quote: change a commented passage only when that is what its comment asks, then reply and resolve_artifact_comment, since the comment no longer finds its text. ${PENDING_HINT}`,
    parameters: {
      type: 'object',
      properties: {
        artifactId: { type: 'string' },
        baseVersion: { type: 'integer', description: 'With content: the version you read.' },
        edits: {
          type: 'array',
          description: 'Replacements, applied in order; all or none.',
          items: {
            type: 'object',
            properties: { old: { type: 'string' }, new: { type: 'string' } },
            required: ['old', 'new'],
          },
        },
        content: { type: 'string', description: 'The whole document, in Markdown.' },
        reason: { type: 'string', description: 'What changed, in one line.' },
        title: { type: 'string' },
      },
      required: ['artifactId', 'reason'],
    },
  },
  run(args, ctx) {
    const artifactId = str(args, 'artifactId')
    return settled(ctx, artifactId, () => {
      const edits = args.edits !== undefined && args.edits !== null
      if (edits === (typeof args.content === 'string')) {
        throw new ToolInputError('Give "edits" or "content", one of them.')
      }
      const now = edits
        ? toChange(ctx, artifactId, 'document')
        : ownArtifact(ctx, artifactId, 'document')
      const content = edits ? applyDocumentEdits(now.content, args.edits) : str(args, 'content')
      const artifact = save(ctx, now, content, {
        reason: str(args, 'reason'),
        title: optionalStr(args, 'title'),
        baseVersion: edits ? now.version : baseVersionOf(args),
      })
      return json({ artifactId, version: artifact.version })
    })
  },
  live: (ctx) =>
    showWriting(
      ctx,
      'document',
      (args) => args.edits !== undefined || args.content !== undefined,
      (args, open, base) => {
        if (typeof args.content !== 'string') return editsSoFar(base, args.edits, open)
        return isOpen(open, 'content') ? rewriteSoFar(base, args.content) : args.content
      },
    ),
}

// ---- whiteboards ----

const NEW_ITEM_SCHEMA = {
  type: 'object',
  properties: {
    id: { type: 'string', description: 'Your own short id, used by connectors and later edits.' },
    kind: {
      type: 'string',
      enum: ['rect', 'ellipse', 'diamond', 'sticky', 'text', 'pending'],
      description:
        'pending is a placeholder, drawn in grey: its text says what will be there. Give the item again with its real kind and text once you get to it.',
    },
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

const OPTIONS =
  "Options for the user to compare and pick from, side by side: each a frame with its title, its screenshots one under another and a few lines. The user marks on them, comments and picks one; that comes back to you as feedback by itself. A new version of an option (revises) goes under the one it revises, so the user's marks on it stay. An option given again by id is redrawn in place." as const

const PICK = {
  type: 'string',
  enum: ['one', 'many'],
  description: 'Whether the user picks one option (the default) or any number.',
} as const

function pickOf(args: Record<string, unknown>): 'one' | 'many' | undefined {
  const value = args.pick
  if (value === undefined || value === null) return undefined
  if (value !== 'one' && value !== 'many')
    throw new ToolInputError('"pick" must be "one" or "many"')
  return value
}

const createWhiteboard: Tool = {
  spec: {
    name: 'create_whiteboard',
    description:
      'Create a whiteboard in the project: shapes, sticky notes and text joined by arrows, for a sketch of an architecture, a flow or a mind map; or options (with screenshots) for the user to mark, comment on and pick from. The user can move things, draw and add their own. Items are laid out for you in ranks along the connectors, sized to their text, with room for connector labels; items not connected to anything are set in rows. Give x and y only to sketch a picture the layout cannot (a comparison side by side, a ring); the picture is then stretched until nothing overlaps. Keep a board to about 15 items and give connectors only where the arrow says something. The result lists problems the board still has (overlaps, labels on items, connectors through items, text cut off); fix them with update_whiteboard.',
    parameters: {
      type: 'object',
      properties: {
        title: { type: 'string' },
        items: { type: 'array', items: NEW_ITEM_SCHEMA },
        connectors: { type: 'array', items: CONNECTOR_SCHEMA },
        direction: DIRECTION,
        options: { type: 'array', items: OPTION_SCHEMA, description: OPTIONS },
        pick: PICK,
        nodeId: NODE_ID,
      },
      required: ['title'],
    },
  },
  async run(args, ctx) {
    const options = arrayOf(args, 'options')
    const items = arrayOf(args, 'items')
    if (!options.length && !items.length) throw new ToolInputError('Give "items" or "options".')
    let board = boardFromItems(items, arrayOf(args, 'connectors'), directionOf(args))
    if (options.length) board = await placeOptions(ctx, board, options, pickOf(args))
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
      "Change a whiteboard; the user sees each item appear as you write it. Items and connectors are matched by id: a known id changes only the fields you give (an item keeps its place unless you give x and y), a new id is added and placed next to what it connects from. Everything else on the board stays, including the user's drawings, images and what they put in options; the user may be changing the board at the same time, and their changes and yours are merged. With relayout, every item but the user's drawings and images is laid out again, as on a new board; use it after adding several items, not to undo where the user moved things. The result lists problems the board still has; read_artifact lists them too, also after the user changed the board.",
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
        options: { type: 'array', items: OPTION_SCHEMA, description: OPTIONS },
        pick: PICK,
      },
      required: ['artifactId', 'reason'],
    },
  },
  run(args, ctx) {
    const artifactId = str(args, 'artifactId')
    return settled(ctx, artifactId, async () => {
      const now = toChange(ctx, artifactId, 'whiteboard')
      const shared = sharedIn(ctx, now)
      if (!shared) baseVersionOf(args)
      let board = mergeBoard(JSON.parse(now.content) as WhiteboardContent, {
        items: arrayOf(args, 'items'),
        connectors: arrayOf(args, 'connectors'),
        removeItems: stringsOf(args, 'removeItems'),
        removeConnectors: stringsOf(args, 'removeConnectors'),
        relayout: args.relayout === true,
        direction: directionOf(args),
      })
      const options = arrayOf(args, 'options')
      if (options.length) board = await placeOptions(ctx, board, options, pickOf(args))
      const artifact = save(ctx, now, JSON.stringify(board), {
        reason: str(args, 'reason'),
        title: optionalStr(args, 'title'),
        // Edited together, the changes from the version read go in; others' since stay.
        baseVersion: shared ? now.version : baseVersionOf(args),
      })
      return json({ artifactId, version: artifact.version, ...problemsOf(board) })
    })
  },
  // The items show one by one as each is written; what is taken away goes at the end.
  live: (ctx) =>
    showWriting(
      ctx,
      'whiteboard',
      (args) => Array.isArray(args.items) || Array.isArray(args.connectors),
      (args, open, base) => {
        const whole = (key: string) =>
          (Array.isArray(args[key]) ? (args[key] as unknown[]) : []).filter(
            (item, index): item is Record<string, unknown> =>
              typeof item === 'object' && item !== null && !isOpen(open, key, index),
          )
        const board = mergeBoard(JSON.parse(base) as WhiteboardContent, {
          items: whole('items'),
          connectors: whole('connectors'),
          removeItems: [],
          removeConnectors: [],
        })
        return JSON.stringify(board)
      },
    ),
}

// ---- dashboards ----

const WIDGET_SCHEMA = {
  type: 'object',
  properties: {
    id: { type: 'string' },
    type: { type: 'string', enum: ['stat', 'text', 'table', 'chart', 'pivot', 'pending'] },
    title: { type: 'string' },
    props: {
      type: 'object',
      description:
        'By type; field names may be dotted paths like total.amount, read in the data set named by source. stat: { value: number, prefix?, suffix?, precision? } for a number you give, or { source, field, trend?, delta?, prefix?, suffix?, precision? } to read it: field, trend and delta are all field names, never numbers (delta names a field holding the change as a ratio, 0.125 shows +12.5%; trend names a list of numbers). text: { text, size?: sm|md|lg }. table: { source, path?, columns?: [{ key, title? }] }. chart: { source, path?, kind: line|area|bar|pie, x: field, y: [fields], labels?: { field: name }, stacked?, horizontal? }. pivot: { source, path?, config: { rows: [fields], columns: [fields], values: [field | { field, aggregate: sum|count|avg|min|max|distinct|median, label? }] } }. pending: { text }, a placeholder drawn in grey that says what will be there; give the widget again with its real type and props once you have its data, and it keeps its place. No other props.',
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
        ...(ctx.shared ? { shared: ctx.shared } : {}),
      },
    )
    // the fetched data may have changed shape, so the widgets are checked against it again
    const content = JSON.parse(artifact.content) as DashboardContent
    return json({ artifactId, version: artifact.version, ...dashboardProblemsOf(content) })
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
  run(args, ctx) {
    const artifactId = str(args, 'artifactId')
    return settled(ctx, artifactId, async () => {
      const data = args.data
      if (
        data !== undefined &&
        (typeof data !== 'object' || Array.isArray(data) || data === null)
      ) {
        throw new ToolInputError('"data" must be an object of named data sources')
      }
      const baseVersion = baseVersionOf(args)
      const first = ownArtifact(ctx, artifactId, 'dashboard')
      const live =
        args.source === undefined || args.source === null
          ? null
          : await fetchSource(args.source, ctx, first.projectId)
      // Read once the data is in: what the user changed meanwhile is part of it.
      const now = toChange(ctx, artifactId, 'dashboard')
      const shared = sharedIn(ctx, now)
      const fetched = live || data ? { ...(data as Record<string, unknown>), ...live?.data } : null
      const content = mergeDashboard(
        JSON.parse(now.content) as DashboardContent,
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
      const artifact = save(ctx, now, JSON.stringify(content), {
        reason: str(args, 'reason'),
        title: optionalStr(args, 'title'),
        // Edited together, the changes from the version read go in; others' since stay.
        baseVersion: shared ? now.version : baseVersion,
      })
      return json({ artifactId, version: artifact.version, ...dashboardProblemsOf(content) })
    })
  },
  // The widgets show one by one as each is written, with the data sets written whole so
  // far; what is taken away and a source's data come at the end.
  live: (ctx) =>
    showWriting(
      ctx,
      'dashboard',
      (args) => Array.isArray(args.widgets),
      (args, open, base) => {
        const widgets = (args.widgets as unknown[]).filter(
          (item, index): item is Record<string, unknown> =>
            typeof item === 'object' && item !== null && !isOpen(open, 'widgets', index),
        )
        if (!widgets.length) return null
        const given =
          typeof args.data === 'object' && args.data !== null && !Array.isArray(args.data)
            ? (args.data as Record<string, unknown>)
            : {}
        const data = Object.fromEntries(
          Object.entries(given).filter(([name]) => !isOpen(open, 'data', name)),
        )
        const content = JSON.parse(base) as DashboardContent
        const changes = { widgets, removeWidgets: [], removeData: [] }
        return JSON.stringify(
          Object.keys(data).length
            ? mergeDashboard(content, { ...changes, data }, Date.now())
            : mergeDashboard(content, changes, content.takenAt ?? 0),
        )
      },
    ),
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
    const artifactId = str(args, 'artifactId')
    // What the user is changing right now is saved first, so the newest version has it.
    if (version === null && ctx.shared) {
      const newest = ctx.app.getArtifact(artifactId)
      if (newest.projectId === ctx.projectId && SharedArtifacts.shared(newest.kind)) {
        ctx.shared.save(artifactId)
      }
    }
    const artifact = ctx.app.getArtifact(artifactId, version ?? undefined)
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
    const newest = version === null || version === ctx.app.getArtifact(artifact.id).version
    if (artifact.kind === 'whiteboard') {
      const content = JSON.parse(artifact.content) as WhiteboardContent
      return json({
        ...head,
        ...boardSummary(content),
        ...problemsOf(content),
        ...(newest
          ? { ...commentsOf(ctx, artifact.id, content), ...roundsOf(ctx, artifact.id) }
          : {}),
      })
    }
    if (artifact.kind === 'dashboard') {
      const content = JSON.parse(artifact.content) as DashboardContent
      return json({ ...head, content, ...dashboardProblemsOf(content) })
    }
    // Comments are on the newest text; an older version is read without them.
    if (!newest) return json({ ...head, content: artifact.content })
    return json({
      ...head,
      content: artifact.content,
      ...commentsOf(ctx, artifact.id),
      ...roundsOf(ctx, artifact.id),
    })
  },
}

const clamp = (n: number) => Math.min(1, Math.max(0, n))

/** A point of an item as fractions of its size, kept within it; `null` when not given. */
function pointOf(value: unknown): { x: number; y: number } | null {
  if (value === undefined || value === null) return null
  const { x, y } = value as { x?: unknown; y?: unknown }
  if (typeof x !== 'number' || typeof y !== 'number' || !Number.isFinite(x + y)) {
    throw new ToolInputError('"at" must be { x, y }, fractions of the item\'s width and height')
  }
  return { x: clamp(x), y: clamp(y) }
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

/** The feedback rounds on an artifact not finished yet, for finish_feedback_round. */
function roundsOf(ctx: ToolContext, artifactId: string) {
  const open = ctx.app
    .listArtifactFeedback(artifactId)
    .filter((round) => round.doneAt === null)
    .map((round) => round.round)
  return open.length ? { openFeedbackRounds: open.toReversed() } : {}
}

/**
 * The open comment threads on a document or whiteboard, and how many were resolved: on a
 * document by the text they quote, on a whiteboard by the item and where it is.
 */
function commentsOf(ctx: ToolContext, artifactId: string, board?: WhiteboardContent) {
  const threads = ctx.app.listArtifactComments(artifactId)
  const open = threads.filter((thread) => !thread.resolved)
  if (!threads.length) return {}
  const on = (thread: (typeof threads)[number]) => {
    const { anchor } = thread
    if (!board || !anchor) return {}
    if ('edge' in anchor) return { on: anchor.edge }
    return { on: anchor.node, where: anchorWhere(board, anchor, thread.id) }
  }
  return {
    comments: open.map((thread) => ({
      commentThread: thread.id,
      ...(board ? on(thread) : { quote: thread.quote }),
      comments: thread.comments.map((c) => ({ by: commenter(ctx, c.author), body: c.body })),
    })),
    resolvedComments: threads.length - open.length,
  }
}

const commentArtifact: Tool = {
  spec: {
    name: 'comment_artifact',
    description:
      "Comment on a document or whiteboard, as the user does there: reply in a comment thread (commentThread), or start one on a short passage of a document's text (quote, as the reader sees it, without Markdown marks) or on a whiteboard's item or option (on, its id; at pins it to a point of it, such as a spot on a screenshot). Keep comments short. Answer the user's comments here, not in a chat message.",
    parameters: {
      type: 'object',
      properties: {
        artifactId: { type: 'string' },
        commentThread: { type: 'string', description: 'The comment thread to reply in.' },
        quote: { type: 'string', description: 'The text a new comment thread is about.' },
        on: { type: 'string', description: 'The whiteboard item a new comment thread is about.' },
        at: {
          type: 'object',
          description:
            'The point of that item it is pinned to, as fractions of its width and height: { x: 0.8, y: 0.1 } is near the top right.',
          properties: { x: { type: 'number' }, y: { type: 'number' } },
          required: ['x', 'y'],
        },
        body: { type: 'string', description: 'Plain text.' },
      },
      required: ['artifactId', 'body'],
    },
  },
  run(args, ctx) {
    const artifactId = str(args, 'artifactId')
    const artifact = toChange(ctx, artifactId)
    if (artifact.kind === 'dashboard') throw new ToolInputError('Dashboards have no comments.')
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
    if (artifact.kind === 'whiteboard') {
      const on = optionalStr(args, 'on')?.trim()
      if (!on) throw new ToolInputError('Give commentThread to reply, or on to start one.')
      const board = JSON.parse(artifact.content) as WhiteboardContent
      const node = board.nodes.find((n) => n.id === on)
      const edge = node ? undefined : board.edges.find((e) => e.id === on)
      if (!node && !edge) {
        throw new ToolInputError(`There is no item or connector "${on}" on the board.`)
      }
      const at = pointOf(args.at)
      if (at && !node) throw new ToolInputError('"at" pins a comment to an item, not a connector.')
      const created = `cth_${crypto.randomUUID()}`
      ctx.app.commentOnArtifact(artifactId, ctx.conversationId, {
        type: 'create',
        threadId: created,
        commentId,
        quote: '',
        anchor: node ? { node: on } : { edge: on },
        body,
      })
      // The board shows the thread where its item names it, pinned to the point if given.
      const item = (node ?? edge)! as Record<string, unknown>
      item.comments = [...(Array.isArray(item.comments) ? item.comments : []), created]
      if (at) item.commentPins = { ...(item.commentPins as object | undefined), [created]: at }
      save(ctx, artifact, JSON.stringify(board), {
        reason: '',
        baseVersion: artifact.version,
      })
      return json({ commentThread: created })
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
      "Mark a document's or whiteboard's comment thread resolved once it is settled, for example after you made the change it asked for. The user can reopen it.",
    parameters: {
      type: 'object',
      properties: { artifactId: { type: 'string' }, commentThread: { type: 'string' } },
      required: ['artifactId', 'commentThread'],
    },
  },
  run(args, ctx) {
    const artifactId = str(args, 'artifactId')
    ownArtifact(ctx, artifactId)
    const threadId = str(args, 'commentThread')
    ctx.app.commentOnArtifact(artifactId, ctx.conversationId, { type: 'resolve', threadId })
    return json({ commentThread: threadId, resolved: true })
  },
}

const finishFeedbackRound: Tool = {
  spec: {
    name: 'finish_feedback_round',
    description:
      "Say you dealt with a round of the user's feedback on a document or whiteboard (what they changed, marked, picked and commented, sent to you by itself): once you made the changes it asks for, answered its comments and resolved the settled ones. The user sees your summary where the round was.",
    parameters: {
      type: 'object',
      properties: {
        artifactId: { type: 'string' },
        round: { type: 'integer' },
        summary: {
          type: 'string',
          description: 'What you did, in one short line, in their language.',
        },
      },
      required: ['artifactId', 'round', 'summary'],
    },
  },
  run(args, ctx) {
    const artifactId = str(args, 'artifactId')
    ownArtifact(ctx, artifactId)
    const round = optionalInt(args, 'round')
    if (round === null) throw new ToolInputError('"round" is required')
    const done = ctx.app.finishArtifactFeedback(
      artifactId,
      round,
      ctx.conversationId,
      str(args, 'summary'),
    )
    return json({ artifactId, round: done.round, done: true })
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
  finishFeedbackRound,
]
