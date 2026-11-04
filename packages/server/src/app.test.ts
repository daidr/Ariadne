import { Ariadne, type AnyStoredEvent } from '@ariadne/core'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { McpHub, noSandbox, PluginHost, WorkspaceManager } from '@ariadne/agent'
import type { ContextProvider, TrackerCapability } from '@ariadne/plugin-sdk'
import { mkdtempSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { createHandler } from './app'
import { SettingsStore } from './settings'

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

  test('answers an attention with values its form allows', async () => {
    const project = app.createProject('P')
    const attention = app.openAttention(project.id, {
      block: {
        type: 'form',
        title: '发布设置',
        fields: [
          { key: 'version', label: '版本号', kind: 'text', required: true },
          { key: 'notify', label: '通知订阅者', kind: 'switch' },
        ],
      } as never,
    })
    const answer = (values: Record<string, unknown>) =>
      call(`/api/attentions/${attention.id}/answer`, {
        method: 'POST',
        body: { answer: { type: 'form', values } },
      })
    const missing = await answer({ notify: true })
    expect(missing.status).toBe(400)
    expect(((await missing.json()) as { error: { key: string } }).error.key).toBe(
      'error.blockInvalid',
    )
    expect((await answer({ version: '1.2.0', notify: true })).status).toBe(204)
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
    const response = await call(
      `/api/events?afterSeq=0&projectId=${a.id}&token=${token}`,
      {},
      false,
    )
    expect(response.headers.get('content-type')).toBe('text/event-stream')

    app.renameProject(b.id, 'B2')
    app.renameProject(a.id, 'A2')

    const events = await readFrames(response, 2)
    expect(events.map((e) => [e.type, e.projectId])).toEqual([
      ['project.created', a.id],
      ['project.renamed', a.id],
    ])
  })

  test('starts at the newest event when no starting point is given', async () => {
    const a = app.createProject('A')
    const response = await call('/api/events')
    app.renameProject(a.id, 'A2')
    const events = await readFrames(response, 1)
    expect(events.map((e) => e.type)).toEqual(['project.renamed'])
  })

  test('resumes after the last seen sequence number', async () => {
    const a = app.createProject('A')
    app.renameProject(a.id, 'A2')
    const response = await call(`/api/events?afterSeq=1`)
    app.renameProject(a.id, 'A3')
    const events = await readFrames(response, 2)
    expect(events.map((e) => e.seq)).toEqual([2, 3])
  })

  test('prefers Last-Event-ID over the URL on an automatic reconnect', async () => {
    const a = app.createProject('A')
    app.renameProject(a.id, 'A2')
    const response = await call('/api/events?afterSeq=0', { headers: { 'last-event-id': '1' } })
    app.renameProject(a.id, 'A3')
    const events = await readFrames(response, 2)
    expect(events.map((e) => e.seq)).toEqual([2, 3])
  })
})

const add = (id: string, title: string) => ({ op: 'add', id, title })
const errorKey = async (response: Response) =>
  ((await response.json()) as { error: { key: string } }).error.key

describe('plan edits from the graph', () => {
  test('apply at once as the user, removals included, and tell the coordinator', async () => {
    const projectId = app.createProject('P').id
    const first = await call(`/api/projects/${projectId}/plan/revise`, {
      method: 'POST',
      body: {
        reason: '在计划图上改了 3 处',
        ops: [add('a', 'A'), add('b', 'B'), { op: 'link', from: 'a', to: 'b' }],
      },
    })
    expect(first.status).toBe(200)
    const body = (await first.json()) as {
      change: { status: string }
      plan: { nodes: { id: string; dependsOn: string[] }[]; revisions: { author: string }[] }
    }
    expect(body.change.status).toBe('applied')
    expect(body.plan.nodes.map((n) => [n.id, n.dependsOn])).toEqual([
      ['a', []],
      ['b', ['a']],
    ])
    expect(body.plan.revisions[0]?.author).toBe('user')

    // The coordinator would need a yes to remove a node; the user's own edit does not.
    const removed = await call(`/api/projects/${projectId}/plan/revise`, {
      method: 'POST',
      body: { reason: '删除 B', ops: [{ op: 'remove', id: 'b' }] },
    })
    expect(((await removed.json()) as { change: { status: string } }).change.status).toBe('applied')
    expect(app.getPlan(projectId).nodes.map((n) => n.id)).toEqual(['a'])
    expect(app.getPlan(projectId).proposals).toEqual([])
  })

  test('refuses malformed ops, changes that do not apply, and an empty reason', async () => {
    const projectId = app.createProject('P').id
    const post = (body: unknown) =>
      call(`/api/projects/${projectId}/plan/revise`, { method: 'POST', body })

    const malformed = await post({ reason: 'x', ops: [{ op: 'drop', id: 'a' }] })
    expect(malformed.status).toBe(400)
    expect(await errorKey(malformed)).toBe('error.planInvalid')
    expect(await errorKey(await post({ reason: 'x', ops: [{ op: 'remove', id: 'nope' }] }))).toBe(
      'error.planInvalid',
    )
    expect(await errorKey(await post({ reason: ' ', ops: [add('a', 'A')] }))).toBe(
      'error.invalidBody',
    )
    expect(app.getPlan(projectId).revisions).toEqual([])
  })
})

describe('files', () => {
  const png = new Uint8Array([0x89, 0x50, 0x4e, 0x47, 9, 9])

  const upload = (projectId: string, type: string) =>
    handle(
      new Request(`http://127.0.0.1/api/projects/${projectId}/files`, {
        method: 'POST',
        headers: { authorization: `Bearer ${token}`, 'content-type': type },
        body: png,
      }),
    )

  test('an uploaded image loads by its address without the token, and only same-site', async () => {
    const project = app.createProject('P')
    const response = await upload(project.id, 'image/png')
    expect(response.status).toBe(201)
    const { file } = (await response.json()) as { file: { url: string } }
    const served = await call(file.url, {}, false)
    expect(served.status).toBe(200)
    expect(new Uint8Array(await served.arrayBuffer())).toEqual(png)
    expect(served.headers.get('content-type')).toBe('image/png')
    expect(served.headers.get('cross-origin-resource-policy')).toBe('same-origin')
    expect(served.headers.get('x-content-type-options')).toBe('nosniff')
    expect((await call('/api/files/fil_nothere', {}, false)).status).toBe(404)
  })

  test('a body sent without a length is refused once it grows too large', async () => {
    const project = app.createProject('P')
    const chunk = new Uint8Array(1024 * 1024)
    let sent = 0
    const body = new ReadableStream<Uint8Array>({
      pull(controller) {
        sent += 1
        if (sent > 64) controller.close()
        else controller.enqueue(chunk)
      },
    })
    const response = await handle(
      new Request(`http://127.0.0.1/api/projects/${project.id}/files`, {
        method: 'POST',
        headers: { authorization: `Bearer ${token}`, 'content-type': 'image/png' },
        body,
        duplex: 'half',
      } as RequestInit),
    )
    expect(response.status).toBe(400)
    expect(((await response.json()) as { error: { key: string } }).error.key).toBe(
      'error.fileTooLarge',
    )
    // It stopped reading soon after the limit.
    expect(sent).toBeLessThan(16)
  })

  test('refuses files that are not images, and uploads without the token', async () => {
    const project = app.createProject('P')
    expect((await upload(project.id, 'text/html')).status).toBe(400)
    const anonymous = await handle(
      new Request(`http://127.0.0.1/api/projects/${project.id}/files`, {
        method: 'POST',
        headers: { 'content-type': 'image/png' },
        body: png,
      }),
    )
    expect(anonymous.status).toBe(401)
  })
})

describe('attachments', () => {
  test('a message carries text files and images uploaded for it', async () => {
    const project = app.createProject('P')
    const text = new TextEncoder().encode('const x = 1\n')
    const sent = (type: string, body: Uint8Array<ArrayBuffer>) =>
      handle(
        new Request(`http://127.0.0.1/api/projects/${project.id}/files?for=message`, {
          method: 'POST',
          headers: { authorization: `Bearer ${token}`, 'content-type': type },
          body,
        }),
      )
    // A browser may call a .ts file a video, or nothing at all: it is text all the same.
    const response = await sent('video/mp2t', text)
    expect(response.status).toBe(201)
    const { file } = (await response.json()) as { file: { id: string; url: string; mime: string } }
    expect(file.mime).toBe('text/plain')
    const served = await call(file.url, {}, false)
    expect(served.headers.get('content-type')).toBe('text/plain; charset=utf-8')
    expect((await sent('application/octet-stream', new Uint8Array([0, 1, 2]))).status).toBe(400)

    const posted = await call(`/api/projects/${project.id}/messages`, {
      method: 'POST',
      body: {
        conversationId: project.coordinatorId,
        content: '',
        attachments: [{ fileId: file.id, name: 'a.ts' }],
      },
    })
    expect(posted.status).toBe(201)
    const { message } = (await posted.json()) as { message: { attachments: unknown[] } }
    expect(message.attachments).toEqual([
      { fileId: file.id, name: 'a.ts', mime: 'text/plain', size: text.byteLength },
    ])
  })
})

describe('sandbox', () => {
  test('says when thread commands are not isolated', async () => {
    const response = await call('/api/sandbox')
    expect(await response.json()).toEqual({
      sandbox: { kind: null, mode: 'auto', hidden: [], platform: process.platform },
    })
  })
})

/** A project whose source has a tracker, with one plan node `a`. */
async function withTracker(fail = false) {
  const host = new PluginHost('/tmp/ariadne-test-plugins')
  const tracker: TrackerCapability = {
    name: 'acme/app',
    webUrl: 'https://github.com/acme/app/issues',
    list: async () => [],
    async issue(number) {
      if (fail || number !== 3) throw new Error('Not Found')
      return {
        number,
        url: 'https://github.com/acme/app/issues/3',
        title: 'Safari 登录',
        state: 'open',
        author: null,
        labels: [],
        comments: 0,
        updatedAt: '',
        body: '',
        createdAt: '',
        recent: [],
      }
    },
    comment: () => Promise.reject(new Error('not here')),
    setState: () => Promise.reject(new Error('not here')),
  }
  const provider: ContextProvider<never> = {
    kind: 'tracked',
    title: { 'zh-CN': '有 issue' },
    settings: { type: 'object', properties: {} },
    provides: ['tracker'],
    connect: async () => ({ tracker }),
  }
  await host.load({
    id: 'tracked',
    name: { 'zh-CN': '有 issue' },
    setup: (ctx) => ctx.registerContextProvider(provider),
  })
  handle = createHandler({ app, token, workspaces: new WorkspaceManager(app, host) })
  const project = app.createProject('P')
  app.setProjectSource(project.id, { kind: 'tracked', settings: {} })
  app.revisePlan(project.id, {
    author: 'user',
    reason: '起步',
    ops: [{ op: 'add', id: 'a', kind: 'task', title: 'A', description: '', acceptance: '' }],
  })
  return project.id
}

const errorOf = async (response: Response) =>
  ((await response.json()) as { error: { key: string } }).error.key

describe('issues on plan nodes', () => {
  test('the user links an issue by number and unlinks it', async () => {
    const projectId = await withTracker()
    const path = `/api/projects/${projectId}/plan/nodes/a/issues`
    expect((await call(path, { method: 'POST', body: { number: 3 } }, false)).status).toBe(401)
    const linked = await call(path, { method: 'POST', body: { number: 3 } })
    expect(linked.status).toBe(200)
    expect(app.getPlan(projectId).nodes[0]?.issues).toEqual([
      {
        tracker: 'acme/app',
        number: 3,
        title: 'Safari 登录',
        url: 'https://github.com/acme/app/issues/3',
        linkedBy: 'user',
      },
    ])
    // Unlinking a number that is not linked changes nothing.
    expect((await call(`${path}/9`, { method: 'DELETE' })).status).toBe(200)
    expect(app.getPlan(projectId).nodes[0]?.issues).toHaveLength(1)
    expect((await call(`${path}/3`, { method: 'DELETE' })).status).toBe(200)
    expect(app.getPlan(projectId).nodes[0]?.issues).toEqual([])
  })

  test('refuses a bad number, a missing node, no tracker, and a tracker that fails', async () => {
    const projectId = await withTracker()
    const path = (node: string) => `/api/projects/${projectId}/plan/nodes/${node}/issues`
    const bad = await call(path('a'), { method: 'POST', body: { number: 0 } })
    expect(bad.status).toBe(400)
    expect(await errorOf(await call(path('nope'), { method: 'POST', body: { number: 3 } }))).toBe(
      'error.planInvalid',
    )
    expect(await errorOf(await call(path('a'), { method: 'POST', body: { number: 9 } }))).toBe(
      'error.issue',
    )
    const plain = app.createProject('Q')
    app.revisePlan(plain.id, {
      author: 'user',
      reason: '起步',
      ops: [{ op: 'add', id: 'b', kind: 'task', title: 'B', description: '', acceptance: '' }],
    })
    const none = await call(`/api/projects/${plain.id}/plan/nodes/b/issues`, {
      method: 'POST',
      body: { number: 3 },
    })
    expect(await errorOf(none)).toBe('error.noTracker')
    expect(app.getPlan(projectId).nodes[0]?.issues).toEqual([])
  })
})

describe('MCP servers', () => {
  test('are set up in the settings and picked per project, secrets kept on the server', async () => {
    const settings = new SettingsStore(
      join(mkdtempSync(join(tmpdir(), 'ariadne-mcp-')), 'config.json'),
      new PluginHost('/tmp/ariadne-test-plugins'),
    )
    const mcp = new McpHub({
      servers: () => settings.current.mcp.servers,
      sandbox: noSandbox,
      env: () => ({}),
    })
    handle = createHandler({ app, token, settings, mcp })
    const saved = await call('/api/settings/mcp', {
      method: 'PUT',
      body: {
        servers: [
          {
            id: 'gone',
            name: '不在',
            transport: 'stdio',
            command: '/definitely/not/here',
            env: [{ name: 'TOKEN', value: 'secret-1' }],
            tools: {},
          },
        ],
      },
    })
    expect(saved.status).toBe(200)
    const text = await saved.text()
    expect(text).not.toContain('secret-1')
    expect(JSON.parse(text).status).toMatchObject([{ id: 'gone', state: 'idle' }])
    const checked = await call('/api/settings/mcp/gone/check', { method: 'POST' })
    expect(((await checked.json()) as { status: { state: string } }).status.state).toBe('failed')

    const project = app.createProject('P')
    const refused = await call(`/api/projects/${project.id}/mcp`, {
      method: 'PUT',
      body: { servers: ['nope'] },
    })
    expect(refused.status).toBe(400)
    const picked = await call(`/api/projects/${project.id}/mcp`, {
      method: 'PUT',
      body: { servers: ['gone'] },
    })
    expect(((await picked.json()) as { project: { mcp: string[] } }).project.mcp).toEqual(['gone'])
    await mcp.dispose()
  })
})
