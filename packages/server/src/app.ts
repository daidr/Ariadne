import type { Ariadne } from '@ariadne/core'
import { eventStream } from './events-stream'
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
}

type Params = Record<string, string>
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
export function createHandler({ app, token, webDist }: HandlerOptions) {
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

  add('GET', '/api/conversations/:conversationId', (_request, { conversationId = '' }) =>
    json({ messages: app.listMessages(conversationId), todo: app.getTodo(conversationId) }),
  )

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

  return async function handle(request: Request): Promise<Response> {
    const url = new URL(request.url)
    if (url.pathname === '/api/health') return json({ ok: true })
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
