import {
  syncThreadPullRequest,
  type PluginHost,
  type Scheduler,
  type WorkspaceManager,
} from '@ariadne/agent'
import {
  DomainError,
  emptyContent,
  MAX_FILE_BYTES,
  parsePlanOps,
  type Ariadne,
} from '@ariadne/core'
import { eventStream } from './events-stream'
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

function bearer(request: Request, url: URL): string | null {
  const header = request.headers.get('authorization')
  if (header?.startsWith('Bearer ')) return header.slice(7)
  // EventSource cannot set headers, so the event stream may pass the token in the query.
  if (url.pathname === '/api/events') return url.searchParams.get('token')
  return null
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
}: HandlerOptions) {
  const routes: Route[] = []
  const add = (method: string, pathname: string, handle: Route['handle']) =>
    routes.push({ method, pattern: new URLPattern({ pathname }), handle })

  add('GET', '/api/projects', () => json({ projects: app.listProjects(), lastSeq: app.lastSeq() }))

  add('POST', '/api/projects', async (request) => {
    const body = await readJson(request)
    return json({ project: app.createProject(requireString(body, 'name')) }, 201)
  })

  add('GET', '/api/projects/:projectId', (_request, { projectId = '' }) =>
    json({
      project: app.getProject(projectId),
      threads: app.listThreads(projectId),
      needsMe: app.listNeedsMe(projectId),
    }),
  )

  // Files people add, such as whiteboard images. The body is the file itself.
  add('POST', '/api/projects/:projectId/files', async (request, { projectId = '' }) => {
    const data = await readLimited(request, MAX_FILE_BYTES)
    const mime = request.headers.get('content-type') ?? ''
    return json({ file: app.saveFile(projectId, { mime, data }) }, 201)
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
  // The user takes back an agent's change to the memory.
  add('POST', '/api/projects/:projectId/notices/:noticeId/undo', (_request, params) => {
    app.undoMemoryNotice(params.projectId ?? '', params.noticeId ?? '')
    return new Response(null, { status: 204 })
  })
  add('DELETE', '/api/projects/:projectId/memory/:memoryId', (_request, params) => {
    app.removeMemory(params.projectId ?? '', params.memoryId ?? '', 'user')
    return new Response(null, { status: 204 })
  })

  // How thread commands are isolated, so the page can say when they are not.
  add('GET', '/api/sandbox', () =>
    json({ sandbox: host?.sandbox.status ?? { kind: null, mode: 'auto', hidden: [] } }),
  )

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
      })),
    }),
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
  add('PUT', '/api/projects/:projectId/source', async (request, { projectId = '' }) => {
    const body = await readJson(request)
    const previous = app.getProject(projectId).source
    if (body.source === null) {
      settings?.saveSourceSecrets(projectId, {})
      return json({ project: app.setProjectSource(projectId, null) })
    }
    const input = (body.source ?? {}) as { kind?: unknown; settings?: unknown }
    const kind = requireString(input as Record<string, unknown>, 'kind')
    const provider = host?.contexts.get(kind)
    if (host && !provider) throw new BadRequest('source.kind')
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
      if (trimmed[key] === undefined || trimmed[key] === '') throw new BadRequest(`settings.${key}`)
    }
    const split = settings
      ? settings.splitSourceSettings(projectId, kind, trimmed, previous?.kind ?? null)
      : { settings: trimmed, secrets: {} }
    if (provider) {
      try {
        await provider.connect({ ...split.settings, ...split.secrets } as never)
      } catch (error) {
        throw new DomainError('error.workspace', {
          detail: error instanceof Error ? error.message : String(error),
        })
      }
    }
    settings?.saveSourceSecrets(projectId, split.secrets)
    return json({ project: app.setProjectSource(projectId, { kind, settings: split.settings }) })
  })

  // The user lets a held thread go on.
  add('POST', '/api/threads/:threadId/stop', async (request, { threadId = '' }) => {
    const { reason } = await readJson(request)
    if (typeof reason !== 'string') throw new BadRequest('reason')
    return json({ thread: app.stopThread(threadId, { reason, by: 'user' }) })
  })

  add('POST', '/api/threads/:threadId/resume', (_request, { threadId = '' }) =>
    json({ thread: app.resumeThread(threadId, 'user') }),
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
    return json({ artifact, versions: app.listArtifactVersions(artifact.id) })
  })

  add('GET', '/api/artifacts/:artifactId', (_request, { artifactId = '' }, url) => {
    const given = url.searchParams.get('version')
    const version = given === null ? undefined : Number(given)
    if (version !== undefined && !Number.isInteger(version)) throw new BadRequest('version')
    return json({
      artifact: app.getArtifact(artifactId, version),
      versions: app.listArtifactVersions(artifactId),
    })
  })

  // The user saves an edit. `baseVersion` is the version they started from.
  add('PUT', '/api/artifacts/:artifactId', async (request, { artifactId = '' }) => {
    const body = await readJson(request)
    const content = body.content
    if (typeof content !== 'string') throw new BadRequest('content')
    if (!Number.isInteger(body.baseVersion)) throw new BadRequest('baseVersion')
    const artifact = app.reviseArtifact(artifactId, {
      content,
      author: 'user',
      reason: typeof body.reason === 'string' ? body.reason : '',
      baseVersion: body.baseVersion as number,
      ...(typeof body.title === 'string' ? { title: body.title } : {}),
    })
    return json({ artifact, versions: app.listArtifactVersions(artifactId) })
  })

  add('GET', '/api/conversations/:conversationId', (_request, { conversationId = '' }) =>
    json({
      conversation: app.getConversation(conversationId),
      messages: app.listMessages(conversationId),
      notices: app.listNotices(conversationId),
      todo: app.getTodo(conversationId),
    }),
  )

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
    settings.updateModels(await readJson(request))
    return json(settings.publicModels())
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

  // The user writes to the coordinator or straight into a thread.
  add('POST', '/api/projects/:projectId/messages', async (request, { projectId = '' }) => {
    const body = await readJson(request)
    const message = app.postMessage(projectId, {
      conversationId: requireString(body, 'conversationId'),
      author: 'user',
      content: requireString(body, 'content'),
    })
    return json({ message }, 201)
  })

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
    const body = await readJson(request)
    const choice = body.choice ?? null
    const text = body.text ?? null
    if (choice !== null && !Number.isInteger(choice)) throw new BadRequest('choice')
    if (text !== null && typeof text !== 'string') throw new BadRequest('text')
    if (choice === null && text === null) throw new BadRequest('answer')
    app.answerAttention(attentionId, {
      choice: choice as number | null,
      text: text as string | null,
    })
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
    if (!path.includes('..') && (await file.exists())) return new Response(file)
    return new Response(Bun.file(`${webDist}/index.html`))
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
          'content-type': file.mime,
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
