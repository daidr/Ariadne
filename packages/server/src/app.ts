import {
  buildTrace,
  compactAt,
  connectReference,
  connectSource,
  conversationSkills,
  DEFAULT_CONTEXT_TOKENS,
  HOSTING_SECRET,
  LinkResolver,
  listWorktrees,
  MAX_LINKS,
  mentionFiles,
  mergeThread,
  referenceSecrets,
  undoMerge,
  refreshDashboard,
  SharedArtifacts,
  sourceSummary,
  syncThreadPullRequest,
  threadChanges,
  threadFile,
  TOOL_SWITCH_NAMES,
  traceRequest,
  type ArtifactFeedback,
  type McpHub,
  type PluginHost,
  type Scheduler,
  type WorkspaceManager,
} from '@ariadne/agent'
import {
  DomainError,
  type ArtifactCommentAction,
  type ConversationView,
  emptyContent,
  MAX_FILE_BYTES,
  parsePlanOps,
  parseProjectSettings,
  type Ariadne,
  type ProjectSource,
} from '@ariadne/core'
import { createTranslator, type LocalizedMessage } from '@ariadne/i18n'
import type { Server } from 'bun'
import { browseLocal, NothingToBrowse, type ListedModel } from '@ariadne/plugin-sdk'
import { eventStream } from './events-stream'
import type { DataStore } from './data'
import type { SettingsStore } from './settings'
import {
  BadRequest,
  errorResponse,
  json,
  readJson,
  requireString,
  sameToken,
  toErrorResponse,
} from './http'
import { checkModel, suggestModel, type ModelCheck } from './model-check'
import { ModelCatalog, type ModelInfo } from './prices'
import { ThreadSync } from './thread-sync'

/** A directory listing gives up after this long. */
const BROWSE_TIMEOUT_MS = 15_000
/** How long opening a project's settings waits for its source to say what it installs. */
const DETECT_WAIT_MS = 1500
/** How long the page waits to hear whether a reference source can be read. */
const REFERENCE_STATUS_MS = 5000

/** How long a listing may take: a remote machine may be slow to reach. */
const browseOptions = () => ({ signal: AbortSignal.timeout(BROWSE_TIMEOUT_MS) })

/**
 * A listing; `null` when there is nothing to list yet (no host typed, a path that is not
 * there), else why there is none, said the way a broken source is.
 */
async function browsing<T>(list: () => Promise<T>): Promise<T | null> {
  try {
    return await list()
  } catch (error) {
    if (error instanceof NothingToBrowse) return null
    if (error instanceof DomainError) throw error
    throw new DomainError('error.browse', {
      detail: error instanceof Error ? error.message : String(error),
    })
  }
}

export interface HandlerOptions {
  app: Ariadne
  token: string
  /** Serve the built web app from here for non-API paths. */
  webDist?: string | null
  /** Agents and settings; left out in tests that only exercise the domain API. */
  scheduler?: Scheduler
  settings?: SettingsStore
  /** Context providers, to check a project's directory before saving it. */
  host?: PluginHost
  /** The projects' workspaces and repositories, to refresh a pull request on request. */
  workspaces?: WorkspaceManager
  /** The connections to the user's external tool servers. */
  mcp?: McpHub
  /** The data directory: its size, and backups of the database. */
  data?: DataStore
  /** The user's own skill folders for every project: Ariadne's own first, then shared ones. */
  skillsDirs?: readonly string[]
  /** What is publicly known of models, to fill in a model's settings. */
  prices?: ModelCatalog
  /** Asks a model set up whether it answers; swapped in tests. */
  check?: ModelCheck
  /** Documents and whiteboards edited together; left out, they are saved whole. */
  shared?: SharedArtifacts
  /** The user's feedback in artifacts, sent to the agent in rounds. */
  feedback?: ArtifactFeedback
}

/** What a socket joined to an artifact's room carries. */
export interface SharedSocketData {
  artifactId: string
  link?: { message(data: Uint8Array): void; close(): void }
}

type Params = Record<string, string>

/** The body, refused as soon as it grows past `max` bytes, whether or not it gave a length. */
async function readLimited(request: Request, max: number): Promise<Uint8Array> {
  const tooLarge = () => new DomainError('error.fileTooLarge', { max: max / 1024 / 1024 })
  if (Number(request.headers.get('content-length') ?? 0) > max) throw tooLarge()
  const chunks: Uint8Array[] = []
  let size = 0
  for await (const chunk of request.body ?? []) {
    size += chunk.byteLength
    if (size > max) throw tooLarge()
    chunks.push(chunk)
  }
  const data = new Uint8Array(size)
  let offset = 0
  for (const chunk of chunks) {
    data.set(chunk, offset)
    offset += chunk.byteLength
  }
  return data
}
type Route = {
  method: string
  pattern: URLPattern
  handle: (request: Request, params: Params, url: URL) => Response | Promise<Response>
}

function isStringRecord(value: unknown): value is Record<string, string> {
  return (
    typeof value === 'object' &&
    value !== null &&
    !Array.isArray(value) &&
    Object.values(value).every((v) => typeof v === 'string')
  )
}

/** A comment action as the page sends it, checked field by field. */
function commentAction(body: Record<string, unknown>): ArtifactCommentAction {
  const text = (key: string) => {
    const value = body[key]
    if (typeof value !== 'string' || !value) throw new BadRequest(key)
    return value
  }
  const threadId = text('threadId')
  switch (body.type) {
    case 'create': {
      // On a whiteboard, the item or connector instead of a quote.
      if (body.anchor !== undefined) {
        const anchor = body.anchor as { node?: unknown; edge?: unknown } | null
        const node = typeof anchor?.node === 'string' && anchor.node ? anchor.node : null
        const edge = typeof anchor?.edge === 'string' && anchor.edge ? anchor.edge : null
        if (!node === !edge) throw new BadRequest('anchor')
        return {
          type: 'create',
          threadId,
          commentId: text('commentId'),
          quote: '',
          anchor: node ? { node } : { edge: edge! },
          body: text('body'),
        }
      }
      return {
        type: 'create',
        threadId,
        commentId: text('commentId'),
        quote: text('quote'),
        body: text('body'),
      }
    }
    case 'reply':
      return { type: 'reply', threadId, commentId: text('commentId'), body: text('body') }
    case 'edit':
      return { type: 'edit', threadId, commentId: text('commentId'), body: text('body') }
    case 'delete':
      return { type: 'delete', threadId, commentId: text('commentId') }
    case 'resolve':
    case 'reopen':
    case 'remove':
      return { type: body.type, threadId }
    default:
      throw new BadRequest('type')
  }
}

/** Where a page joins the room of a document or whiteboard edited together. */
const SHARED_PATH = /^\/api\/artifacts\/(art_[\w-]+)\/live$/

/**
 * A page asks to join the room of a document or whiteboard: the socket is opened, and the
 * answer is `undefined`, or the refusal. Anything else is not for this and gets `null`.
 */
export function joinShared(
  request: Request,
  server: Server<SharedSocketData>,
  { app, token }: { app: Ariadne; token: string },
): Response | undefined | null {
  const url = new URL(request.url)
  const live = SHARED_PATH.exec(url.pathname)
  if (!live || request.method !== 'GET') return null
  const given = bearer(request, url)
  if (!given || !sameToken(given, token)) return errorResponse({ key: 'error.unauthorized' }, 401)
  try {
    const artifact = app.getArtifact(live[1]!)
    if (!SharedArtifacts.shared(artifact.kind)) {
      return errorResponse({ key: 'error.notFound', params: { what: url.pathname } }, 404)
    }
    if (server.upgrade(request, { data: { artifactId: artifact.id } })) return undefined
    throw new BadRequest('upgrade')
  } catch (error) {
    return toErrorResponse(error)
  }
}

function bearer(request: Request, url: URL): string | null {
  const header = request.headers.get('authorization')
  if (header?.startsWith('Bearer ')) return header.slice(7)
  // EventSource and WebSocket cannot set headers, so these may pass the token in the query.
  if (url.pathname === '/api/events' || SHARED_PATH.test(url.pathname)) {
    return url.searchParams.get('token')
  }
  return null
}

/** What a provider listed, each name once, as entries. */
function entries(listed: (string | ListedModel)[]): ListedModel[] {
  const seen = new Set<string>()
  return listed.flatMap((item) => {
    const entry = typeof item === 'string' ? { id: item } : item
    if (!entry.id || seen.has(entry.id)) return []
    seen.add(entry.id)
    return [entry]
  })
}

/** A service's models, those the catalog knows to work with tools first, newest first. */
function inOrder(listed: string[], info: ReadonlyMap<string, ModelInfo>): string[] {
  const known = (name: string) => {
    const facts = info.get(name)
    return !!facts && facts.tools !== false
  }
  const released = (name: string) => info.get(name)?.released ?? ''
  return [
    ...listed.filter(known).toSorted((a, b) => released(b).localeCompare(released(a))),
    ...listed.filter((name) => !known(name)),
  ]
}

/** Build the request handler. Kept separate from `Bun.serve` so tests can call it directly. */
export function createHandler({
  app,
  token,
  webDist,
  scheduler,
  settings,
  host,
  workspaces,
  mcp,
  data: dataStore,
  skillsDirs,
  prices = new ModelCatalog(),
  check = checkModel,
  shared,
  feedback,
}: HandlerOptions) {
  const routes: Route[] = []
  const add = (method: string, pathname: string, handle: Route['handle']) =>
    routes.push({ method, pattern: new URLPattern({ pathname }), handle })

  add('GET', '/api/projects', () => json({ projects: app.listProjects(), lastSeq: app.lastSeq() }))

  // A new project. From the first screen it comes with where its files are and what the
  // user said first, and takes its name from one of them.
  add('POST', '/api/projects', async (request) => {
    const body = await readJson(request)
    const name = typeof body.name === 'string' ? body.name : null
    const message = typeof body.message === 'string' ? body.message : ''
    if (body.source === undefined || body.source === null) {
      return json({ project: app.createProject(name, { message }) }, 201)
    }
    // Connected before anything is created, so a mistyped path leaves no project behind.
    const { source, secrets } = await readSource(body.source, null)
    const project = app.createProject(name, { source, message })
    settings?.saveSourceSecrets(project.id, secrets)
    return json({ project }, 201)
  })

  // With `since` (a `sync.cursor` given before), only the threads changed after it.
  const threadSync = new ThreadSync()
  add('GET', '/api/projects/:projectId', (_request, { projectId = '' }, url) => {
    const detail = app.getProjectDetail(projectId)
    const { threads, ...sync } = threadSync.delta(
      projectId,
      detail.threads,
      url.searchParams.get('since'),
    )
    return json({ ...detail, threads, sync, hasTracker: !!workspaces?.hasTracker(projectId) })
  })

  add('PUT', '/api/projects/:projectId', async (request, { projectId = '' }) => {
    const body = await readJson(request)
    return json({ project: app.renameProject(projectId, requireString(body, 'name')) })
  })

  // Delete a project with everything Ariadne keeps for it. Its turns end first, then its
  // threads' worktrees go (branches stay), then its data. The source's own directory or
  // repository is never touched.
  const deleting = new Map<string, Promise<void>>()
  add('DELETE', '/api/projects/:projectId', async (_request, { projectId = '' }) => {
    app.getProject(projectId)
    let deletion = deleting.get(projectId)
    if (!deletion) {
      deletion = (async () => {
        const release = (await scheduler?.holdProject(projectId)) ?? (() => {})
        try {
          await workspaces?.discardProject(projectId)
          app.deleteProject(projectId)
          threadSync.forget(projectId)
          settings?.forgetProject(projectId)
        } finally {
          release()
        }
      })().finally(() => deleting.delete(projectId))
      deleting.set(projectId, deletion)
    }
    await deletion
    return new Response(null, { status: 204 })
  })

  // Find what was said and made in the project.
  add('GET', '/api/projects/:projectId/search', (_request, { projectId = '' }, url) =>
    json(app.search(projectId, (url.searchParams.get('q') ?? '').slice(0, 200))),
  )

  // Files people add: whiteboard images, or (with `?for=message`) a message's images and
  // text files. The body is the file itself.
  add('POST', '/api/projects/:projectId/files', async (request, { projectId = '' }, url) => {
    const data = await readLimited(request, MAX_FILE_BYTES)
    const mime = request.headers.get('content-type') ?? ''
    const text = url.searchParams.get('for') === 'message'
    return json({ file: app.saveFile(projectId, { mime, data }, { text }) }, 201)
  })

  // The project's memory: standing preferences every conversation sees.
  add('GET', '/api/projects/:projectId/memory', (_request, { projectId = '' }) =>
    json({ memories: app.listMemories(projectId) }),
  )
  add('POST', '/api/projects/:projectId/memory', async (request, { projectId = '' }) => {
    const body = await readJson(request)
    const memory = app.saveMemory(projectId, { text: requireString(body, 'text'), author: 'user' })
    return json({ memory }, 201)
  })
  add('PUT', '/api/projects/:projectId/memory/:memoryId', async (request, params) => {
    const body = await readJson(request)
    const memory = app.saveMemory(params.projectId ?? '', {
      memoryId: params.memoryId ?? '',
      text: requireString(body, 'text'),
      author: 'user',
    })
    return json({ memory })
  })
  // The user is back at the project: what happened while they were away is told, if anything.
  add('POST', '/api/projects/:projectId/return', async (request, params) => {
    const { away } = await readJson(request)
    if (typeof away !== 'number') throw new BadRequest('away')
    app.noteReturn(params.projectId ?? '', away)
    return new Response(null, { status: 204 })
  })
  // The user takes back an agent's change to the memory.
  add('POST', '/api/projects/:projectId/notices/:noticeId/undo', (_request, params) => {
    const { projectId = '', noticeId = '' } = params
    // One for every project lives in the settings: the line goes, or comes back.
    const everywhere = app.everywhereNotice(projectId, noticeId)
    if (!everywhere) app.undoMemoryNotice(projectId, noticeId)
    else if (settings && !everywhere.undone) {
      if (everywhere.action === 'saved') settings.removeInstruction(everywhere.text)
      else settings.addInstruction(everywhere.text)
      app.settleNotice(projectId, noticeId)
    }
    return new Response(null, { status: 204 })
  })
  add('DELETE', '/api/projects/:projectId/memory/:memoryId', (_request, params) => {
    app.removeMemory(params.projectId ?? '', params.memoryId ?? '', 'user')
    return new Response(null, { status: 204 })
  })

  // Plugins: the built-in ones and those loaded from a path, with why a path failed.
  add('GET', '/api/plugins', () => json({ plugins: host?.records() ?? [] }))

  add('POST', '/api/plugins', async (request) => {
    if (!settings) throw new BadRequest('settings')
    await settings.addPlugin((await readJson(request)).path)
    return json({ plugins: host?.records() ?? [] })
  })

  add('POST', '/api/plugins/remove', async (request) => {
    if (!settings) throw new BadRequest('settings')
    await settings.removePlugin((await readJson(request)).path)
    return json({ plugins: host?.records() ?? [] })
  })

  // The kinds of context source plugins offer, with the form each one needs.
  add('GET', '/api/source-kinds', () =>
    json({
      kinds: [...(host?.contexts.values() ?? [])].map((provider) => ({
        kind: provider.kind,
        title: provider.title,
        settings: provider.settings,
        provides: provider.provides,
        remote: !!provider.remote,
        hosting: !!provider.hosting,
        browse: !!provider.browse,
        summary: provider.summary ?? null,
      })),
    }),
  )

  // Directories at what the user typed into a source's directory setting, on the machine
  // the rest of the form points at. Secrets left empty are the project's stored ones.
  add('POST', '/api/source-kinds/:kind/browse', async (request, { kind = '' }) => {
    const provider = host?.contexts.get(kind)
    if (!provider) throw new DomainError('error.notFound', { what: kind })
    if (!provider.browse) throw new BadRequest('kind')
    const body = await readJson(request)
    const typed = typeof body.path === 'string' ? body.path : ''
    const given = (
      body.settings && typeof body.settings === 'object' && !Array.isArray(body.settings)
        ? body.settings
        : {}
    ) as Record<string, unknown>
    let filled = given
    const projectId = typeof body.projectId === 'string' ? body.projectId : null
    if (projectId && settings) {
      const previous = app.getProject(projectId).source?.kind ?? null
      const split = settings.splitSourceSettings(projectId, kind, given, previous)
      filled = { ...split.settings, ...split.secrets }
    }
    return json({
      listing: await browsing(() => provider.browse!(filled as never, typed, browseOptions())),
    })
  })

  // Directories on this machine, for paths the server itself uses (an MCP server's working
  // directory, a plugin's location).
  add('GET', '/api/directories', async (_request, _params, url) =>
    json({ listing: await browsing(() => browseLocal(url.searchParams.get('path') ?? '')) }),
  )

  add('GET', '/api/projects/:projectId/source', (_request, { projectId = '' }) =>
    json({
      source: app.getProject(projectId).source,
      secretsSet: settings?.sourceSecretsSet(projectId) ?? [],
    }),
  )

  // Where the project's threads work. The source is connected before it is saved, so a
  // typo or a missing permission shows up here rather than in a thread's first turn.
  // Secret settings (tokens) go to the user's config, never into the event log.
  // A live dashboard fetches its data again; a new version records what came back.
  add('POST', '/api/artifacts/:artifactId/refresh', async (request, { artifactId = '' }) => {
    // The fetch ends with the request: a closed page stops waiting on the source.
    const artifact = await refreshDashboard(app, host?.dashboards ?? new Map(), artifactId, {
      author: 'user',
      signal: request.signal,
      ...(shared ? { shared } : {}),
      reason: createTranslator(settings?.current.locale ?? 'zh-CN')('artifact.dashboard.refreshed'),
    })
    return json({ artifact })
  })

  // The project's own settings, with the names of its command environment variables (their
  // values never leave the server).
  add('GET', '/api/projects/:projectId/settings', async (_request, { projectId = '' }) => {
    app.getProject(projectId)
    // Before any workspace was opened, what a new one would install is read from the
    // source, so the user sees it here first; a source slow to answer is not waited for.
    await Promise.race([
      workspaces?.detectSetup(projectId).catch(() => {}),
      new Promise((done) => setTimeout(done, DETECT_WAIT_MS)),
    ])
    return json({
      settings: app.getProject(projectId).settings,
      detected: app.getProject(projectId).detected,
      env: settings?.projectEnvNames(projectId) ?? [],
      tools: TOOL_SWITCH_NAMES,
      hostedMerge: !!workspaces?.hasRepo(projectId),
    })
  })

  add('PUT', '/api/projects/:projectId/settings', async (request, { projectId = '' }) => {
    const body = await readJson(request)
    const current = app.getProject(projectId).settings
    // Both are checked before either is saved, so a refusal changes nothing; settings left
    // out stay as they are.
    const next = body.settings === undefined ? current : body.settings
    parseProjectSettings(next, TOOL_SWITCH_NAMES)
    const env =
      body.env !== undefined
        ? (settings?.updateProjectEnv(projectId, body.env) ?? [])
        : (settings?.projectEnvNames(projectId) ?? [])
    const project = app.setProjectSettings(projectId, next, TOOL_SWITCH_NAMES)
    return json({
      project,
      settings: project.settings,
      detected: project.detected,
      env,
      tools: TOOL_SWITCH_NAMES,
      hostedMerge: !!workspaces?.hasRepo(projectId),
    })
  })

  // Everything the project's log holds, as a file to keep.
  add('GET', '/api/projects/:projectId/export', (_request, { projectId = '' }) => {
    const exported = app.exportProject(projectId)
    const body = JSON.stringify({ ariadne: 1, exportedAt: Date.now(), ...exported })
    const name = `ariadne-${projectId}.json`
    return new Response(body, {
      headers: {
        'content-type': 'application/json; charset=utf-8',
        'content-disposition': `attachment; filename="${name}"`,
      },
    })
  })

  add('GET', '/api/settings/data', async () => {
    if (!dataStore) throw new BadRequest('data')
    return json(await dataStore.usage())
  })

  add('POST', '/api/settings/data/backups', () => {
    if (!dataStore) throw new BadRequest('data')
    return json({ backup: dataStore.backup() }, 201)
  })

  add('DELETE', '/api/settings/data/backups/:name', (_request, { name = '' }) => {
    if (!dataStore) throw new BadRequest('data')
    dataStore.removeBackup(name)
    return new Response(null, { status: 204 })
  })

  // How the work is done everywhere: instructions, command rules, limits.
  add('GET', '/api/settings/work', () => {
    if (!settings) throw new BadRequest('settings')
    return json(settings.work())
  })

  add('PUT', '/api/settings/work', async (request) => {
    if (!settings) throw new BadRequest('settings')
    return json(settings.updateWork(await readJson(request)))
  })

  // The header's switch: pause the project's autopilot, or let it go on.
  add('PUT', '/api/projects/:projectId/autopilot', async (request, { projectId = '' }) => {
    const { paused } = await readJson(request)
    if (typeof paused !== 'boolean') throw new BadRequest('paused')
    return json({ project: app.setAutopilotPaused(projectId, paused) })
  })
  add('PUT', '/api/projects/:projectId/slots', async (request, { projectId = '' }) => {
    const { slots } = await readJson(request)
    if (slots !== null && typeof slots !== 'number') throw new BadRequest('slots')
    return json({ project: app.setProjectSlots(projectId, slots) })
  })

  add('PUT', '/api/projects/:projectId/source', async (request, { projectId = '' }) => {
    const body = await readJson(request)
    const previous = app.getProject(projectId).source
    if (body.source === null) {
      settings?.saveSourceSecrets(projectId, {})
      return json({ project: app.setProjectSource(projectId, null) })
    }
    const { source, secrets } = await readSource(body.source, { id: projectId, previous })
    settings?.saveSourceSecrets(projectId, secrets)
    return json({ project: app.setProjectSource(projectId, source) })
  })

  /**
   * A source as the form sent it, checked and connected to once, so a typo or a missing
   * permission shows up before it is saved. Its secrets come apart from the rest; those
   * left empty are the ones stored for `project`, when it has the same kind of source.
   */
  async function readSource(
    raw: unknown,
    project: { id: string; previous: ProjectSource | null } | null,
  ): Promise<{ source: ProjectSource; secrets: Record<string, string> }> {
    const input = (raw && typeof raw === 'object' ? raw : {}) as Record<string, unknown>
    const part = readSourcePart(input, 'source')
    const hosting =
      input.hosting === undefined || input.hosting === null
        ? null
        : readSourcePart(
            (typeof input.hosting === 'object' ? input.hosting : {}) as Record<string, unknown>,
            'hosting',
          )
    if (host && hosting) {
      if (!host.contexts.get(hosting.kind)?.hosting) throw new BadRequest('hosting.kind')
      if (!host.contexts.get(part.kind)?.remote) throw new BadRequest('hosting')
    }
    const projectId = project?.id ?? ''
    const previous = project?.previous ?? null
    const split = settings
      ? settings.splitSourceSettings(projectId, part.kind, part.settings, previous?.kind ?? null)
      : { settings: part.settings, secrets: {} }
    const hostingSplit =
      hosting && settings
        ? settings.splitSourceSettings(
            projectId,
            hosting.kind,
            hosting.settings,
            previous?.hosting?.kind ?? null,
            HOSTING_SECRET,
          )
        : hosting
          ? { settings: hosting.settings, secrets: {} }
          : null
    const source: ProjectSource = {
      kind: part.kind,
      settings: split.settings,
      ...(hosting && hostingSplit
        ? { hosting: { kind: hosting.kind, settings: hostingSplit.settings } }
        : {}),
    }
    const secrets = { ...split.secrets, ...hostingSplit?.secrets }
    if (host) await connectSource(host.contexts, source, secrets)
    return { source, secrets }
  }

  // A source the project only reads. Connected and listed before it is saved, like the
  // project's own; its secrets go to the user's config under the reference's id.
  add('POST', '/api/projects/:projectId/references', async (request, { projectId = '' }) => {
    const body = await readJson(request)
    const part = readSourcePart((body.reference ?? {}) as Record<string, unknown>, 'source')
    const project = app.getProject(projectId)
    const split = settings
      ? settings.splitSourceSettings(projectId, part.kind, part.settings, null)
      : { settings: part.settings, secrets: {} }
    const reference = { kind: part.kind, settings: split.settings }
    let where = part.kind
    if (host) {
      const connected = await connectReference(host.contexts, reference, {
        ...referenceSecrets(
          host.contexts,
          project.source,
          reference,
          settings?.sourceSecrets(projectId) ?? {},
        ),
        ...split.secrets,
      })
      try {
        await (await connected()).list('.')
      } catch (error) {
        if (error instanceof DomainError) throw error
        throw new DomainError('error.workspace', {
          detail: error instanceof Error ? error.message : String(error),
        })
      }
      where = sourceSummary(host.contexts.get(part.kind)?.summary, reference.settings) ?? where
    }
    const added = app.addProjectReference(projectId, reference, where)
    if (Object.keys(split.secrets).length) {
      settings?.saveReferenceSecrets(projectId, added.id, split.secrets)
    }
    return json({ project: app.getProject(projectId) })
  })

  add('DELETE', '/api/projects/:projectId/references/:referenceId', (_request, params) => {
    const { projectId = '', referenceId = '' } = params
    const project = app.removeProjectReference(projectId, referenceId)
    settings?.saveReferenceSecrets(projectId, referenceId, {})
    return json({ project })
  })

  // Which of the project's reference sources cannot be read right now, and why; each is
  // given a few seconds, so a slow one does not hold the page.
  add('GET', '/api/projects/:projectId/references/status', async (_request, { projectId = '' }) => {
    const { references } = app.getProject(projectId)
    const failures = await Promise.all(
      references.map(async (reference) => {
        if (!workspaces) return null
        let timer: ReturnType<typeof setTimeout> | undefined
        const late = new Promise<null>((done) => {
          timer = setTimeout(() => done(null), REFERENCE_STATUS_MS)
        })
        const read = workspaces
          .referenceView(projectId, reference.name)
          .then((view) => view.list('.'))
          .then(
            () => null,
            (error: unknown): LocalizedMessage =>
              error instanceof DomainError
                ? { key: error.code, params: error.params }
                : {
                    key: 'error.workspace',
                    params: { detail: error instanceof Error ? error.message : String(error) },
                  },
          )
        try {
          return await Promise.race([read, late])
        } finally {
          clearTimeout(timer)
        }
      }),
    )
    return json({
      failed: references.flatMap((reference, at) =>
        failures[at] ? [{ id: reference.id, error: failures[at] }] : [],
      ),
    })
  })

  /** A source's kind and its settings as typed, with the required ones there. */
  function readSourcePart(
    input: Record<string, unknown>,
    what: 'source' | 'hosting',
  ): { kind: string; settings: Record<string, unknown> } {
    const kind = requireString(input, 'kind')
    const provider = host?.contexts.get(kind)
    if (host && !provider) throw new BadRequest(`${what}.kind`)
    const raw = (
      input.settings && typeof input.settings === 'object' ? input.settings : {}
    ) as Record<string, unknown>
    const trimmed = Object.fromEntries(
      Object.entries(raw).map(([key, value]) => [
        key,
        typeof value === 'string' ? value.trim() : value,
      ]),
    )
    for (const key of provider?.settings.required ?? []) {
      // A secret left empty keeps the stored one; without one, connecting says what is wrong.
      const field = provider?.settings.properties[key]
      if (field?.type === 'string' && field.secret) continue
      if (trimmed[key] === undefined || trimmed[key] === '') {
        throw new BadRequest(`${what === 'source' ? 'settings' : 'hosting.settings'}.${key}`)
      }
    }
    return { kind, settings: trimmed }
  }

  // The user lets a held thread go on.
  add('POST', '/api/threads/:threadId/stop', async (request, { threadId = '' }) => {
    const { reason } = await readJson(request)
    if (typeof reason !== 'string') throw new BadRequest('reason')
    return json({ thread: app.stopThread(threadId, { reason, by: 'user' }) })
  })

  add('POST', '/api/threads/:threadId/resume', (_request, { threadId = '' }) =>
    json({ thread: app.resumeThread(threadId, 'user') }),
  )

  // A source the user allowed one thread to read: taken back, or kept for the project.
  add('DELETE', '/api/threads/:threadId/references/:referenceId', (_request, params) => {
    const { threadId = '', referenceId = '' } = params
    return json({ thread: app.removeThreadReference(threadId, referenceId) })
  })

  add('POST', '/api/threads/:threadId/references/:referenceId/keep', (_request, params) => {
    const { threadId = '', referenceId = '' } = params
    app.keepThreadReference(threadId, referenceId)
    return json({ thread: app.getThread(threadId) })
  })

  add('POST', '/api/threads/:threadId/reviewed', (_request, { threadId = '' }) =>
    json({ thread: app.reviewThread(threadId) }),
  )

  // Read the thread's pull request and checks again now, instead of waiting for the poll.
  add(
    'POST',
    '/api/threads/:threadId/pull-request/refresh',
    async (_request, { threadId = '' }) => {
      const thread = app.getThread(threadId)
      if (workspaces?.hasRepo(thread.projectId)) {
        await syncThreadPullRequest(app, await workspaces.repo(thread.projectId), threadId)
      }
      return json({ thread: app.getThread(threadId) })
    },
  )

  // What the thread's branch changed against its base, file by file.
  add('GET', '/api/threads/:threadId/changes', async (_request, { threadId = '' }) => {
    if (!workspaces) return json({ changes: null, mergeable: false })
    return json(await threadChanges(app, workspaces, threadId))
  })

  // One changed file before and after; `from` and `head` come from the changes.
  add('GET', '/api/threads/:threadId/changes/file', async (_request, { threadId = '' }, url) => {
    if (!workspaces) throw new DomainError('error.noBranch')
    const commit = (name: string) => {
      const value = url.searchParams.get(name) ?? ''
      if (!/^[0-9a-f]{40,64}$/.test(value)) throw new BadRequest(name)
      return value
    }
    const path = url.searchParams.get('path')
    if (!path) throw new BadRequest('path')
    return json({
      file: await threadFile(app, workspaces, threadId, {
        from: commit('from'),
        head: commit('head'),
        path,
        previousPath: url.searchParams.get('previousPath') || null,
      }),
    })
  })

  // The user merges the thread's branch into its base.
  add('POST', '/api/threads/:threadId/merge', async (request, { threadId = '' }) => {
    if (!workspaces) throw new DomainError('error.merge.unsupported')
    const body = await readJson(request)
    const method = body.method ?? 'squash'
    if (method !== 'squash' && method !== 'merge') throw new BadRequest('method')
    const message = typeof body.message === 'string' ? body.message : ''
    return json(await mergeThread(app, workspaces, threadId, { method, message }))
  })

  // The user takes the thread's last merge back.
  add('POST', '/api/threads/:threadId/merge/undo', async (_request, { threadId = '' }) => {
    if (!workspaces) throw new DomainError('error.merge.unsupported')
    return json({ thread: await undoMerge(app, workspaces, threadId) })
  })

  // Threads' worktrees across projects, with the space they take.
  add('GET', '/api/worktrees', async () =>
    json({
      projects: workspaces ? await listWorktrees(app, workspaces) : [],
      // Where a Dev Drive is what makes worktrees share storage.
      windows: process.platform === 'win32',
    }),
  )

  // The user removes a thread's worktree, or one left from a thread Ariadne no longer
  // knows; the branch stays.
  add(
    'DELETE',
    '/api/projects/:projectId/worktrees/:threadId',
    async (_request, { projectId = '', threadId = '' }) => {
      app.getProject(projectId)
      if (!/^[\w-]+$/.test(threadId)) throw new BadRequest('threadId')
      const removed = workspaces
        ? await workspaces.removeWorktree(projectId, threadId, 'user')
        : 'dirty'
      // A command running in it right now would lose its directory.
      if (removed === 'busy') throw new DomainError('error.merge.working')
      if (removed === 'dirty') throw new DomainError('error.worktreeDirty')
      return new Response(null, { status: 204 })
    },
  )

  // The user links an issue to a plan node; its title is read from the tracker.
  add(
    'POST',
    '/api/projects/:projectId/plan/nodes/:nodeId/issues',
    async (request, { projectId = '', nodeId = '' }) => {
      const { number } = await readJson(request)
      if (!Number.isInteger(number) || (number as number) < 1) throw new BadRequest('number')
      // The node first: no request leaves for a link that cannot be made.
      if (!app.getPlan(projectId).nodes.some((node) => node.id === nodeId)) {
        throw new DomainError('error.planInvalid', { reason: `no node ${nodeId}` })
      }
      if (!workspaces?.hasTracker(projectId)) throw new DomainError('error.noTracker')
      const tracker = await workspaces.tracker(projectId)
      let issue: { title: string; url: string }
      try {
        issue = await tracker.issue(number as number)
      } catch (error) {
        throw new DomainError('error.issue', {
          detail: error instanceof Error ? error.message : String(error),
        })
      }
      return json({
        plan: app.linkIssue(
          projectId,
          nodeId,
          { tracker: tracker.name, number: number as number, title: issue.title, url: issue.url },
          'user',
        ),
      })
    },
  )

  add(
    'DELETE',
    '/api/projects/:projectId/plan/nodes/:nodeId/issues/:number',
    (_request, { projectId = '', nodeId = '', number = '' }) => {
      const plan = app.getPlan(projectId)
      const node = plan.nodes.find((n) => n.id === nodeId)
      const linked = node?.issues.find((i) => String(i.number) === number)
      if (!linked) return json({ plan })
      return json({
        plan: app.unlinkIssue(
          projectId,
          nodeId,
          { tracker: linked.tracker, number: linked.number },
          'user',
        ),
      })
    },
  )

  add('GET', '/api/projects/:projectId/plan', (_request, { projectId = '' }) =>
    json({ plan: app.getPlan(projectId) }),
  )

  add(
    'POST',
    '/api/projects/:projectId/plan/proposals/:proposalId/dismiss',
    (_request, { projectId = '', proposalId = '' }) => {
      app.dismissPlanProposal(projectId, proposalId)
      return json({ plan: app.getPlan(projectId) })
    },
  )

  // The user edits the plan on the graph. Their changes apply at once and can be undone.
  add('POST', '/api/projects/:projectId/plan/revise', async (request, { projectId = '' }) => {
    const body = await readJson(request)
    const reason = typeof body.reason === 'string' ? body.reason : ''
    const change = app.revisePlan(projectId, {
      author: 'user',
      reason,
      ops: parsePlanOps(body.ops),
    })
    return json({ change, plan: app.getPlan(projectId) })
  })

  add(
    'GET',
    '/api/projects/:projectId/plan/revisions/:revisionId/revert',
    (_request, { projectId = '', revisionId = '' }) =>
      json({ preview: app.previewPlanUndo(projectId, revisionId) }),
  )

  add(
    'POST',
    '/api/projects/:projectId/plan/revisions/:revisionId/revert',
    async (request, { projectId = '', revisionId = '' }) => {
      const body = await readJson(request)
      const reason = typeof body.reason === 'string' ? body.reason : ''
      app.revertPlanRevision(projectId, revisionId, 'user', reason)
      return json({ plan: app.getPlan(projectId) })
    },
  )

  add('GET', '/api/projects/:projectId/artifacts', (_request, { projectId = '' }) =>
    json({ artifacts: app.listArtifacts(projectId) }),
  )

  // The user starts an artifact of their own, empty, from the overview.
  add('POST', '/api/projects/:projectId/artifacts', async (request, { projectId = '' }) => {
    const body = await readJson(request)
    const kind = body.kind
    if (kind !== 'document' && kind !== 'whiteboard' && kind !== 'dashboard') {
      throw new BadRequest('kind')
    }
    if (typeof body.title !== 'string') throw new BadRequest('title')
    const artifact = app.createArtifact(projectId, {
      kind,
      title: body.title,
      author: 'user',
      content: emptyContent(kind),
    })
    return json({ artifact, versions: app.listArtifactVersions(artifact.id), comments: [] })
  })

  add('GET', '/api/artifacts/:artifactId', (_request, { artifactId = '' }, url) => {
    const given = url.searchParams.get('version')
    const version = given === null ? undefined : Number(given)
    if (version !== undefined && !Number.isInteger(version)) throw new BadRequest('version')
    return json({
      artifact: app.getArtifact(artifactId, version),
      versions: app.listArtifactVersions(artifactId),
      comments: app.listArtifactComments(artifactId),
    })
  })

  // The user comments on a document's text; see ArtifactCommentAction.
  add('POST', '/api/artifacts/:artifactId/comments', async (request, { artifactId = '' }) => {
    const body = await readJson(request)
    return json({ comments: app.commentOnArtifact(artifactId, 'user', commentAction(body)) })
  })

  // The page saw the user stop for a moment, or leave: what they did goes to the agent now,
  // with pictures of the whiteboard options they marked (files added before).
  add('POST', '/api/artifacts/:artifactId/feedback', async (request, { artifactId = '' }) => {
    app.getArtifact(artifactId)
    const body = await readJson(request)
    const images = body.images ?? []
    if (!Array.isArray(images) || !images.every((id) => typeof id === 'string')) {
      throw new BadRequest('images')
    }
    return json({ feedback: feedback?.send(artifactId, images as string[]) ?? null })
  })

  // The user saves an edit. `baseVersion` is the version they started from. An artifact
  // edited together takes it into its room (a restored version, a new title).
  add('PUT', '/api/artifacts/:artifactId', async (request, { artifactId = '' }) => {
    const body = await readJson(request)
    const current = app.getArtifact(artifactId)
    const together = shared && SharedArtifacts.shared(current.kind) ? shared : null
    // Edited together, `null` keeps what the room holds: a rename.
    const content = body.content
    if (typeof content !== 'string' && !(together && content === null)) {
      throw new BadRequest('content')
    }
    if (!Number.isInteger(body.baseVersion)) throw new BadRequest('baseVersion')
    const artifact = together
      ? together.write(artifactId, 'user', content as string | null, {
          reason: typeof body.reason === 'string' ? body.reason : '',
          ...(typeof body.title === 'string' ? { title: body.title } : {}),
        })
      : app.reviseArtifact(artifactId, {
          content: content as string,
          author: 'user',
          reason: typeof body.reason === 'string' ? body.reason : '',
          baseVersion: body.baseVersion as number,
          ...(typeof body.title === 'string' ? { title: body.title } : {}),
          ...(isStringRecord(body.quotes) ? { quotes: body.quotes } : {}),
        })
    return json({
      artifact,
      versions: app.listArtifactVersions(artifactId),
      comments: app.listArtifactComments(artifactId),
    })
  })

  /** The model a conversation uses now, as the page shows it; `null` when none is set up. */
  function modelOf(conversation: ConversationView) {
    const given = conversation.role === 'thread' ? app.getThread(conversation.id).model : null
    let chosen = settings?.modelFor(conversation.role, given) ?? null
    // A turn on the fallback is compacted where the fallback's context fills.
    if (chosen && conversation.turn?.fallback) {
      chosen = settings?.fallbackFor(conversation.role) ?? chosen
    }
    if (!chosen) return null
    const contextTokens = chosen.contextTokens ?? DEFAULT_CONTEXT_TOKENS
    return {
      id: chosen.id,
      contextTokens,
      // Where older history is made room for: the context ring is full there.
      compactAt: compactAt(contextTokens, settings?.current.limits.compactShare),
    }
  }

  // The newest messages, or the page before `before`, or everything from `from` on; with
  // `since` (an event seq, as `lastSeq` gave it), only what changed after that event.
  add('GET', '/api/conversations/:conversationId', (_request, { conversationId = '' }, url) => {
    const conversation = app.getConversation(conversationId)
    const before = url.searchParams.get('before')
    const from = url.searchParams.get('from')
    const limit = url.searchParams.get('limit')
    const since = url.searchParams.get('since')
    if (limit !== null && !/^[1-9]\d{0,2}$/.test(limit)) throw new BadRequest('limit')
    if (since !== null && !/^\d{1,15}$/.test(since)) throw new BadRequest('since')
    const page = app.pageMessages(conversationId, {
      ...(before === null ? {} : { before }),
      ...(from === null ? {} : { from }),
      ...(limit === null ? {} : { limit: Number(limit) }),
      ...(since === null ? {} : { since: Number(since) }),
    })
    return json({
      conversation,
      ...page,
      lastSeq: app.lastSeq(),
      todo: app.getTodo(conversationId),
      model: modelOf(conversation),
    })
  })

  // What the conversation's agent did, turn by turn, rebuilt from the log for debugging.
  add(
    'GET',
    '/api/conversations/:conversationId/trace',
    (_request, { conversationId = '' }, url) => {
      const given = url.searchParams.get('before')
      const before = given === null ? undefined : Number(given)
      if (before !== undefined && !Number.isInteger(before)) throw new BadRequest('before')
      return json(buildTrace(app, conversationId, before === undefined ? {} : { before }))
    },
  )

  // The whole of one model request: a step, or the summary of older history, with the
  // body its provider built (no credentials: those go in headers).
  add(
    'GET',
    '/api/conversations/:conversationId/trace/requests/:seq',
    (_request, { conversationId = '', seq = '' }) => {
      const at = Number(seq)
      if (!Number.isInteger(at) || at < 1) throw new BadRequest('seq')
      const { detail, request } = traceRequest(app, conversationId, at)
      const provider = detail.call ? host?.models.get(detail.call.provider) : undefined
      let body: unknown = null
      if (provider?.requestBody && request && detail.call) {
        try {
          body = provider.requestBody(detail.call.settings as never, request)
        } catch {
          body = null
        }
      }
      return json({ request: { ...detail, body } })
    },
  )

  // The user gives a thread another model, or back the default with `null`.
  add('PUT', '/api/threads/:threadId/model', async (request, { threadId = '' }) => {
    const { model } = await readJson(request)
    if (model !== null && typeof model !== 'string') throw new BadRequest('model')
    if (model !== null && !settings?.modelChoices().some((choice) => choice.id === model)) {
      throw new BadRequest('model')
    }
    return json({ thread: app.setThreadModel(threadId, model, 'user') })
  })

  // The user wakes a waiting conversation now, or calls its wait off.
  add('POST', '/api/conversations/:conversationId/wait/now', (_request, params) => {
    app.endWait(params.conversationId ?? '', 'now', 'user')
    return new Response(null, { status: 204 })
  })
  add('DELETE', '/api/conversations/:conversationId/wait', (_request, params) => {
    app.endWait(params.conversationId ?? '', 'cancelled', 'user')
    return new Response(null, { status: 204 })
  })

  // A thread's background commands: the end of what one printed, and stopping it.
  add('GET', '/api/threads/:threadId/commands/:commandId', (_request, params) => {
    const output = workspaces?.background.peek(params.threadId ?? '', params.commandId ?? '')
    if (!output) throw new DomainError('error.notFound', { what: params.commandId ?? '' })
    return json(output)
  })
  add('POST', '/api/threads/:threadId/commands/:commandId/stop', async (_request, params) => {
    const stopped = await workspaces?.background.stop(params.threadId ?? '', params.commandId ?? '')
    if (!stopped) throw new DomainError('error.notFound', { what: params.commandId ?? '' })
    return json({ command: stopped })
  })

  add('POST', '/api/conversations/:conversationId/stop', (_request, { conversationId = '' }) => {
    app.getConversation(conversationId)
    return json({ stopped: scheduler?.cancel(conversationId) ?? false })
  })

  add('POST', '/api/conversations/:conversationId/retry', (_request, { conversationId = '' }) => {
    app.requestTurn(conversationId)
    return new Response(null, { status: 204 })
  })

  add('GET', '/api/settings/models', () => {
    if (!settings)
      return errorResponse({ key: 'error.notFound', params: { what: 'settings' } }, 404)
    return json(settings.publicModels())
  })

  add('PUT', '/api/settings/models', async (request) => {
    if (!settings)
      return errorResponse({ key: 'error.notFound', params: { what: 'settings' } }, 404)
    const changed = settings.updateModels(await readJson(request))
    // What was saved stays saved; a model that does not answer is told with it, so a wrong
    // key or name shows up now and not in the first conversation.
    const asked = await Promise.all(
      changed.map(async (model) => {
        const resolved = settings.resolveSaved(model)
        const problem = resolved ? await check(resolved, settings.current.locale) : null
        return problem ? [{ model: model.id, provider: model.provider, problem }] : []
      }),
    )
    return json({ ...settings.publicModels(), problems: asked.flat() })
  })

  /** What the catalog knows of `models`; nothing when it cannot be read. */
  const describe = (models: string[], hint: string): Promise<Map<string, ModelInfo>> =>
    prices.describe(models, hint).catch(() => new Map<string, ModelInfo>())

  /**
   * What the catalog knows of listed models, an alias by the model it stands for, and with
   * what the service says of them: `known` from the catalog alone, to order them by.
   */
  const describeListed = async (listed: ListedModel[], hint: string) => {
    const looked = await describe([...new Set(listed.map((m) => m.resolves ?? m.id))], hint)
    const known = new Map<string, ModelInfo>()
    const info = new Map<string, ModelInfo>()
    for (const m of listed) {
      const facts = looked.get(m.resolves ?? m.id)
      if (facts) known.set(m.id, facts)
      // The service knows better than the catalog what its models accept.
      if (facts || m.efforts)
        info.set(m.id, { ...facts, ...(m.efforts ? { efforts: m.efforts } : {}) })
    }
    return { known, info }
  }

  // The models a provider in the form can use, each with what the catalog knows of it, so
  // picking one fills its settings in. `models: null` for a provider that cannot list them.
  add('POST', '/api/settings/model-list', async (request) => {
    if (!settings)
      return errorResponse({ key: 'error.notFound', params: { what: 'settings' } }, 404)
    const body = await readJson(request)
    const draft = settings.draftProvider(body.provider)
    if (!draft.provider.models) return json({ models: null })
    let listed: ListedModel[]
    try {
      listed = entries(
        await draft.provider.models(draft.settings as never, {
          headers: draft.headers,
          signal: AbortSignal.timeout(BROWSE_TIMEOUT_MS),
        }),
      )
    } catch (error) {
      return errorResponse(
        {
          key: 'error.modelCheck',
          params: { detail: error instanceof Error ? error.message : String(error) },
        },
        502,
      )
    }
    const { known, info } = await describeListed(listed, draft.hint)
    const titles = new Map(listed.flatMap((m) => (m.title ? [[m.id, m.title] as const] : [])))
    const ids = listed.map((m) => m.id)
    // An agent program lists its models in its own order, `default` first.
    return json({
      models: (draft.provider.program ? ids : inOrder(ids, known)).map((id) => ({
        id,
        ...(titles.has(id) ? { title: titles.get(id) } : {}),
        ...(info.has(id) ? { info: info.get(id) } : {}),
      })),
    })
  })

  // What the public catalog knows of one model, by the name typed: a suggestion for the form.
  add('GET', '/api/settings/model-info', async (_request, _params, url) => {
    const model = (url.searchParams.get('model') ?? '').trim().slice(0, 200)
    if (!model) throw new BadRequest('model')
    let info
    try {
      const hint = (url.searchParams.get('hint') ?? '').slice(0, 500)
      info = (await prices.describe([model], hint)).get(model)
    } catch (error) {
      return errorResponse(
        {
          key: 'error.priceCatalog',
          params: { detail: error instanceof Error ? error.message : String(error) },
        },
        502,
      )
    }
    if (!info) return errorResponse({ key: 'error.priceUnknown', params: { model } }, 404)
    return json({ info })
  })

  // What is already on this machine to talk to a model with; secrets stay here.
  add('GET', '/api/settings/found', () =>
    json({
      found: (settings?.foundSetups() ?? []).map(({ id, kind, title, from }) => ({
        id,
        kind,
        title,
        from,
      })),
    }),
  )

  // Use one of them: it is asked for its models, and the one to start with is set up.
  add('POST', '/api/settings/found/use', async (request) => {
    if (!settings)
      return errorResponse({ key: 'error.notFound', params: { what: 'settings' } }, 404)
    const id = requireString(await readJson(request), 'id')
    const setup = settings.foundSetups().find((found) => found.id === id)
    if (!setup) throw new DomainError('error.notFound', { what: id })
    const { provider, settings: talksWith, hint } = settings.foundProvider(setup)
    let listed: ListedModel[]
    try {
      listed = entries(
        (await provider.models?.(talksWith as never, {
          signal: AbortSignal.timeout(BROWSE_TIMEOUT_MS),
        })) ?? [],
      )
    } catch (error) {
      throw new DomainError('error.modelCheck', {
        detail: error instanceof Error ? error.message : String(error),
      })
    }
    const { known, info } = await describeListed(listed, hint)
    const ids = listed.map((m) => m.id)
    // An agent program starts on what it is itself set to.
    const model = provider.program && ids.includes('default') ? 'default' : suggestModel(ids, known)
    if (!model) throw new DomainError('error.modelNone')
    const saved = settings.useFound(setup, model, info.get(model))
    const resolved = settings.resolveSaved(saved)
    const problem = resolved ? await check(resolved, settings.current.locale) : null
    return json({
      ...settings.publicModels(),
      problems: problem ? [{ model: saved.id, provider: saved.provider, problem }] : [],
    })
  })

  // External tool servers: what is set up, secrets left out, and how each stands.
  const mcpSettings = () => ({
    servers: settings?.publicMcp() ?? [],
    status: mcp?.status() ?? [],
  })
  add('GET', '/api/settings/mcp', () => json(mcpSettings()))

  add('PUT', '/api/settings/mcp', async (request) => {
    if (!settings) throw new BadRequest('settings')
    settings.updateMcp(await readJson(request))
    await mcp?.reconfigure()
    return json(mcpSettings())
  })

  // Connect to a server again now and list its tools.
  add('POST', '/api/settings/mcp/:serverId/check', async (_request, { serverId = '' }) => {
    const status = await mcp?.check(serverId)
    if (!status) throw new DomainError('error.notFound', { what: serverId })
    return json({ status })
  })

  add('PUT', '/api/projects/:projectId/mcp', async (request, { projectId = '' }) => {
    const { servers } = await readJson(request)
    if (!Array.isArray(servers)) throw new BadRequest('servers')
    const known = new Set((settings?.current.mcp.servers ?? []).map((s) => s.id))
    if (servers.some((id) => typeof id !== 'string' || !known.has(id))) {
      throw new BadRequest('servers')
    }
    return json({ project: app.setProjectMcp(projectId, servers as string[]) })
  })

  add('GET', '/api/settings/concurrency', () => {
    if (!settings)
      return errorResponse({ key: 'error.notFound', params: { what: 'settings' } }, 404)
    return json(settings.current.concurrency)
  })

  add('PUT', '/api/settings/concurrency', async (request) => {
    if (!settings)
      return errorResponse({ key: 'error.notFound', params: { what: 'settings' } }, 404)
    const concurrency = settings.updateConcurrency(await readJson(request))
    scheduler?.setSlots(concurrency.slots, concurrency.reserved)
    return json(concurrency)
  })

  add('GET', '/api/settings/input', () => {
    if (!settings)
      return errorResponse({ key: 'error.notFound', params: { what: 'settings' } }, 404)
    return json(settings.current.input)
  })

  add('PUT', '/api/settings/input', async (request) => {
    if (!settings)
      return errorResponse({ key: 'error.notFound', params: { what: 'settings' } }, 404)
    return json(settings.updateInput(await readJson(request)))
  })

  add('GET', '/api/settings/search', () => {
    if (!settings)
      return errorResponse({ key: 'error.notFound', params: { what: 'settings' } }, 404)
    return json(settings.publicSearch())
  })

  add('PUT', '/api/settings/search', async (request) => {
    if (!settings)
      return errorResponse({ key: 'error.notFound', params: { what: 'settings' } }, 404)
    return json(settings.updateSearch(await readJson(request)))
  })

  // Links in messages that a plugin knows, such as pull requests: what each names, and the
  // card shown when the user points at one.
  const links = new LinkResolver(host?.links ?? new Map())
  add('POST', '/api/links', async (request) => {
    const { urls } = await readJson(request)
    if (
      !Array.isArray(urls) ||
      urls.length > MAX_LINKS ||
      urls.some((url) => typeof url !== 'string')
    ) {
      throw new BadRequest('urls')
    }
    return json({ links: links.summarize(urls as string[]) })
  })

  add('GET', '/api/links/preview', async (_request, _params, url) => {
    const address = url.searchParams.get('url')
    if (!address) throw new BadRequest('url')
    return json({ preview: await links.preview(address) })
  })

  // The user writes to the coordinator or straight into a thread.
  add('POST', '/api/projects/:projectId/messages', async (request, { projectId = '' }) => {
    const body = await readJson(request)
    const attachments = body.attachments ?? []
    if (!Array.isArray(attachments)) throw new BadRequest('attachments')
    const mentions = body.mentions ?? []
    if (!Array.isArray(mentions)) throw new BadRequest('mentions')
    const comments = body.comments ?? []
    if (!Array.isArray(comments)) throw new BadRequest('comments')
    const message = app.postMessage(projectId, {
      conversationId: requireString(body, 'conversationId'),
      author: 'user',
      content: typeof body.content === 'string' ? body.content : '',
      attachments,
      mentions,
      comments,
    })
    return json({ message }, 201)
  })

  // The review rounds of a thread's changes (a reviewing thread gives its author's).
  add('GET', '/api/threads/:threadId/reviews', (_request, { threadId = '' }) =>
    json({ rounds: app.listReviews(threadId) }),
  )

  // The line comments the user sent a thread about its changes.
  add('GET', '/api/threads/:threadId/line-comments', (_request, { threadId = '' }) =>
    json({ comments: app.listLineComments(threadId) }),
  )

  // Skills offered after `/` in a conversation's input: what that conversation can read.
  add('GET', '/api/conversations/:conversationId/skills', async (_request, params) => {
    const conversation = app.getConversation(params.conversationId ?? '')
    const owner =
      conversation.role === 'thread'
        ? (app.getThread(conversation.id).reviewOf ?? conversation.id)
        : conversation.id
    const skills = await conversationSkills(app, conversation, owner, workspaces, skillsDirs ?? [])
    return json({ skills: skills.list() })
  })

  // Files offered after `@` in a conversation's input, for what was typed after it.
  add(
    'GET',
    '/api/projects/:projectId/mention-files',
    async (_request, { projectId = '' }, url) => {
      app.getProject(projectId)
      if (!workspaces?.available(projectId)) return json({ files: [] })
      const conversationId = url.searchParams.get('conversationId')
      let threadId: string | null = null
      if (conversationId) {
        const conversation = app.getConversation(conversationId)
        if (conversation.projectId !== projectId) throw new BadRequest('conversationId')
        if (conversation.role === 'thread') threadId = conversation.id
      }
      const query = (url.searchParams.get('q') ?? '').slice(0, 200)
      try {
        return json({ files: await mentionFiles(app, workspaces, projectId, threadId, query) })
      } catch {
        // A source that cannot be read now offers no files; threads and artifacts still show.
        return json({ files: [] })
      }
    },
  )

  add(
    'POST',
    '/api/conversations/:conversationId/read',
    async (request, { conversationId = '' }) => {
      const body = await readJson(request)
      app.markRead(conversationId, requireString(body, 'upTo'))
      return new Response(null, { status: 204 })
    },
  )

  add('POST', '/api/attentions/:attentionId/answer', async (request, { attentionId = '' }) => {
    // Checked against the block it answers, so a form's values are what its fields allow.
    const body = await readJson(request)
    app.answerAttention(attentionId, body.answer)
    return new Response(null, { status: 204 })
  })

  add('GET', '/api/events', (request, _params, url) => {
    // Without a starting point the stream carries only new events: clients load the
    // current state over REST, so replaying the whole log would be wasted work.
    // EventSource reconnects with the same URL plus Last-Event-ID, so the header wins.
    const given = request.headers.get('last-event-id') ?? url.searchParams.get('afterSeq')
    const afterSeq = given === null ? app.lastSeq() : Number(given)
    if (!Number.isInteger(afterSeq) || afterSeq < 0) throw new BadRequest('afterSeq')
    return eventStream(app, { afterSeq, projectId: url.searchParams.get('projectId') })
  })

  async function serveStatic(url: URL): Promise<Response> {
    if (!webDist)
      return errorResponse({ key: 'error.notFound', params: { what: url.pathname } }, 404)
    const path = url.pathname === '/' ? '/index.html' : url.pathname
    const file = Bun.file(`${webDist}${path}`)
    // Unknown paths fall back to the app shell, so client-side routes work on reload.
    if (!path.includes('..') && (await file.exists())) {
      // Built assets carry a hash of their content in the name: the browser keeps them.
      const headers: Record<string, string> = path.startsWith('/assets/')
        ? { 'cache-control': 'public, max-age=31536000, immutable' }
        : { 'cache-control': 'no-cache' }
      return new Response(file, { headers })
    }
    // The shell names the assets of this build, so it is asked for again every time.
    return new Response(Bun.file(`${webDist}/index.html`), {
      headers: { 'cache-control': 'no-cache' },
    })
  }

  /**
   * A file by its address. An `<img>` cannot send the token, so the random id is what
   * guards it; other sites may not embed it, and it never changes.
   */
  function serveFile(id: string): Response {
    try {
      const file = app.getFile(id)
      return new Response(new Blob([file.data as Uint8Array<ArrayBuffer>]), {
        headers: {
          'content-type': file.mime === 'text/plain' ? 'text/plain; charset=utf-8' : file.mime,
          'cache-control': 'private, max-age=31536000, immutable',
          'cross-origin-resource-policy': 'same-origin',
          'x-content-type-options': 'nosniff',
          'content-security-policy': "default-src 'none'",
        },
      })
    } catch (error) {
      return toErrorResponse(error)
    }
  }

  return async function handle(request: Request): Promise<Response> {
    const url = new URL(request.url)
    if (url.pathname === '/api/health') return json({ ok: true })
    const file = /^\/api\/files\/(fil_[\w-]+)$/.exec(url.pathname)
    if (file && request.method === 'GET') return serveFile(file[1]!)
    if (!url.pathname.startsWith('/api/')) return serveStatic(url)

    const given = bearer(request, url)
    if (!given || !sameToken(given, token)) {
      return errorResponse({ key: 'error.unauthorized' }, 401)
    }

    for (const route of routes) {
      if (route.method !== request.method) continue
      const match = route.pattern.exec(url)
      if (!match) continue
      try {
        return await route.handle(request, match.pathname.groups as Params, url)
      } catch (error) {
        return toErrorResponse(error)
      }
    }
    return errorResponse({ key: 'error.notFound', params: { what: url.pathname } }, 404)
  }
}
