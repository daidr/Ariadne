import { Ariadne, type AnyStoredEvent } from '@ariadne/core'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { createHandler } from './app'

const token = 'test-token'
let app: Ariadne
let handle: ReturnType<typeof createHandler>

beforeEach(() => {
  app = new Ariadne({ path: ':memory:' })
  handle = createHandler({ app, token })
})

afterEach(() => app.close())

function call(
  path: string,
  init: Omit<RequestInit, 'body'> & { body?: unknown } = {},
  auth = true,
) {
  const headers = new Headers(init.headers)
  if (auth) headers.set('authorization', `Bearer ${token}`)
  const body = init.body === undefined ? undefined : JSON.stringify(init.body)
  if (body) headers.set('content-type', 'application/json')
  return handle(new Request(`http://127.0.0.1${path}`, { ...init, headers, body }))
}

describe('auth', () => {
  test('health needs no token, everything else does', async () => {
    expect((await call('/api/health', {}, false)).status).toBe(200)
    const denied = await call('/api/projects', {}, false)
    expect(denied.status).toBe(401)
    expect(await denied.json()).toEqual({ error: { key: 'error.unauthorized' } })
    expect(
      (await call('/api/projects', { headers: { authorization: 'Bearer nope' } }, false)).status,
    ).toBe(401)
  })
})

describe('projects and messages', () => {
  test('creates a project and talks to its coordinator', async () => {
    const created = await call('/api/projects', { method: 'POST', body: { name: 'Ariadne' } })
    expect(created.status).toBe(201)
    const { project } = (await created.json()) as { project: { id: string; coordinatorId: string } }

    const posted = await call(`/api/projects/${project.id}/messages`, {
      method: 'POST',
      body: { conversationId: project.coordinatorId, content: '先调研一下' },
    })
    expect(posted.status).toBe(201)

    const conversation = await call(`/api/conversations/${project.coordinatorId}`)
    const body = (await conversation.json()) as { messages: { author: string; content: string }[] }
    expect(body.messages).toMatchObject([{ author: 'user', content: '先调研一下' }])
  })

  test('returns localized errors', async () => {
    const missing = await call('/api/projects/prj_missing')
    expect(missing.status).toBe(404)
    expect(await missing.json()).toEqual({
      error: { key: 'error.notFound', params: { what: 'prj_missing' } },
    })
    const invalid = await call('/api/projects', { method: 'POST', body: { name: '' } })
    expect(invalid.status).toBe(400)
    expect(((await invalid.json()) as { error: { key: string } }).error.key).toBe(
      'error.invalidBody',
    )
  })

  test('answers an attention', async () => {
    const project = app.createProject('P')
    const attention = app.openAttention(project.id, {
      question: '开始吗？',
      options: [{ label: '开始', consequence: '协调器开 thread' }],
    })
    const answered = await call(`/api/attentions/${attention.id}/answer`, {
      method: 'POST',
      body: { choice: 0 },
    })
    expect(answered.status).toBe(204)
    expect(app.listOpenAttentions(project.id)).toEqual([])
  })
})

async function readFrames(response: Response, count: number): Promise<AnyStoredEvent[]> {
  const reader = response.body!.pipeThrough(new TextDecoderStream()).getReader()
  const events: AnyStoredEvent[] = []
  let buffer = ''
  while (events.length < count) {
    const { value, done } = await reader.read()
    if (done) break
    buffer += value
    let end: number
    while ((end = buffer.indexOf('\n\n')) >= 0) {
      const chunk = buffer.slice(0, end)
      buffer = buffer.slice(end + 2)
      const data = chunk.split('\n').find((line) => line.startsWith('data: '))
      if (data) events.push(JSON.parse(data.slice(6)))
    }
  }
  await reader.cancel()
  return events
}

describe('event stream', () => {
  test('replays the backlog, then delivers live events for one project', async () => {
    const a = app.createProject('A')
    const b = app.createProject('B')
    const response = await call(`/api/events?projectId=${a.id}&token=${token}`, {}, false)
    expect(response.headers.get('content-type')).toBe('text/event-stream')

    app.renameProject(b.id, 'B2')
    app.renameProject(a.id, 'A2')

    const events = await readFrames(response, 2)
    expect(events.map((e) => [e.type, e.projectId])).toEqual([
      ['project.created', a.id],
      ['project.renamed', a.id],
    ])
  })

  test('resumes after the last seen sequence number', async () => {
    const a = app.createProject('A')
    app.renameProject(a.id, 'A2')
    const response = await call(`/api/events?afterSeq=1`)
    app.renameProject(a.id, 'A3')
    const events = await readFrames(response, 2)
    expect(events.map((e) => e.seq)).toEqual([2, 3])
  })
})
