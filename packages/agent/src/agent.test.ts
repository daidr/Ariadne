import { Ariadne, type ConversationView } from '@ariadne/core'
import type { ChatMessage, ModelProvider, ModelRequest, ModelStreamPart } from '@ariadne/plugin-sdk'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import type { ResolvedModel } from './runner'
import { Scheduler } from './scheduler'
import { toChatMessages } from './transcript'

type Script = (
  request: ModelRequest,
  role: string,
) => ModelStreamPart[] | Promise<ModelStreamPart[]>

function fakeModel(script: Script): ResolvedModel & { requests: ModelRequest[] } {
  const requests: ModelRequest[] = []
  const provider: ModelProvider<never> = {
    kind: 'fake',
    title: { 'zh-CN': '假模型' },
    settings: { type: 'object', properties: {} },
    async *stream(_settings, request) {
      requests.push(request)
      const system = request.messages[0]?.content ?? ''
      const role = system.includes('You are the coordinator') ? 'coordinator' : 'thread'
      for (const part of await script(request, role)) yield part
    },
  }
  return { provider, settings: {}, model: 'fake-1', label: 'fake / fake-1', requests }
}

const call = (id: string, name: string, args: unknown): ModelStreamPart => ({
  type: 'tool-call',
  id,
  name,
  arguments: JSON.stringify(args),
})
const say = (text: string): ModelStreamPart[] => [
  { type: 'text', text },
  { type: 'usage', input: 10, output: 5 },
  { type: 'finish', reason: 'stop' },
]
const lastUser = (request: ModelRequest) =>
  request.messages.filter((m: ChatMessage) => m.role === 'user').at(-1)?.content ?? ''
const answered = (request: ModelRequest) => request.messages.at(-1)?.role === 'tool'

let app: Ariadne
let scheduler: Scheduler | null = null

beforeEach(() => {
  app = new Ariadne({ path: ':memory:' })
})

afterEach(async () => {
  await scheduler?.stop()
  scheduler = null
  app.close()
})

function startScheduler(model: ResolvedModel | null, options: { slots?: number } = {}) {
  scheduler = new Scheduler({
    app,
    resolveModel: () => model,
    locale: () => 'zh-CN',
    ...options,
  })
  scheduler.start()
  return scheduler
}

describe('a coordinator turn', () => {
  test('runs tools, opens a thread, and posts only its final text', async () => {
    const model = fakeModel((request, role) => {
      if (role === 'thread') return say('')
      if (answered(request)) return say('我开了一个 thread 去调研。')
      return [
        call('c1', 'update_todo', { items: [{ label: '拆解需求', status: 'doing' }] }),
        call('c2', 'create_thread', { title: '调研事件存储', brief: '比较几种做法' }),
        { type: 'finish', reason: 'tool-calls' },
      ]
    })
    const project = app.createProject('P')
    app.postMessage(project.id, {
      conversationId: project.coordinatorId,
      author: 'user',
      content: '帮我规划一下',
    })
    await startScheduler(model).idle()

    const messages = app.listMessages(project.coordinatorId)
    expect(messages.map((m) => [m.author, m.content])).toEqual([
      ['user', '帮我规划一下'],
      ['coordinator', '我开了一个 thread 去调研。'],
    ])
    expect(app.getTodo(project.coordinatorId).items).toEqual([
      { label: '拆解需求', status: 'doing' },
    ])
    const [thread] = app.listThreads(project.id)
    expect(thread?.title).toBe('调研事件存储')
    expect(app.listMessages(thread!.id)[0]).toMatchObject({
      kind: 'relay',
      content: '比较几种做法',
    })

    const coordinator = app.getConversation(project.coordinatorId)
    expect(coordinator).toMatchObject({ pendingSeq: 0, turn: null, lastOutcome: 'completed' })
    expect(coordinator.usage).toEqual({ input: 10, output: 5 })

    // The second request replays the first step and both results, in call order.
    const second = model.requests.find(
      (r) => answered(r) && r.messages[0]?.content.includes('coordinator'),
    )
    expect(second?.messages.slice(1).map((m) => m.role)).toEqual([
      'user',
      'assistant',
      'tool',
      'tool',
    ])
  })

  test('a thread report wakes the coordinator; a progress report does not', async () => {
    const model = fakeModel((request, role) => {
      if (role === 'thread') {
        if (answered(request)) return say('')
        return [
          call('t1', 'report', { kind: 'done', summary: '比较完成' }),
          { type: 'finish', reason: 'tool-calls' },
        ]
      }
      if (lastUser(request).includes('<thread_report')) return say('调研做完了。')
      if (answered(request)) return say('')
      return [
        call('c1', 'create_thread', { title: '调研', brief: '去调研' }),
        { type: 'finish', reason: 'tool-calls' },
      ]
    })
    const project = app.createProject('P')
    app.postMessage(project.id, {
      conversationId: project.coordinatorId,
      author: 'user',
      content: '开始',
    })
    await startScheduler(model).idle()

    const [thread] = app.listThreads(project.id)
    expect(thread?.stage).toBe('done')
    expect(app.listMessages(project.coordinatorId).at(-1)?.content).toBe('调研做完了。')
    const report = model.requests.find((r) => lastUser(r).includes('<thread_report'))
    expect(lastUser(report!)).toContain('title="调研" kind="done"')
  })
})

describe('failures', () => {
  test('a model error fails the turn without retrying; retry runs it again', async () => {
    let fail = true
    const model = fakeModel(() =>
      fail ? [{ type: 'finish', reason: 'error', error: 'HTTP 401: bad key' }] : say('好了'),
    )
    const project = app.createProject('P')
    app.postMessage(project.id, {
      conversationId: project.coordinatorId,
      author: 'user',
      content: 'hi',
    })
    const s = startScheduler(model)
    await s.idle()
    expect(app.getConversation(project.coordinatorId)).toMatchObject({
      pendingSeq: 0,
      lastOutcome: 'failed',
      lastError: { key: 'error.modelRequest', params: { detail: 'HTTP 401: bad key' } },
    })
    expect(model.requests).toHaveLength(1)

    fail = false
    app.requestTurn(project.coordinatorId)
    await s.idle()
    expect(app.getConversation(project.coordinatorId).lastOutcome).toBe('completed')
    expect(app.listMessages(project.coordinatorId).at(-1)?.content).toBe('好了')
  })

  test('without a model the turn fails with a hint, and a failed thread waits on the user', async () => {
    const project = app.createProject('P')
    const thread = app.createThread(project.id, { title: 't' })
    app.postMessage(project.id, { conversationId: thread.id, author: 'user', content: '做吧' })
    await startScheduler(null).idle()
    expect(app.getConversation(thread.id).lastError).toEqual({ key: 'error.noModel' })
    expect(app.getThread(thread.id).stage).toBe('blocked')
    expect(app.listNeedsMe(project.id).map((item) => item.kind)).toEqual(['thread'])
  })

  test('a bad tool call comes back to the model as an error result', async () => {
    const model = fakeModel((request) =>
      answered(request)
        ? say('')
        : [
            call('c1', 'update_todo', { items: 'nope' }),
            call('c2', 'no_such_tool', {}),
            { type: 'finish', reason: 'tool-calls' },
          ],
    )
    const project = app.createProject('P')
    app.postMessage(project.id, {
      conversationId: project.coordinatorId,
      author: 'user',
      content: 'x',
    })
    await startScheduler(model).idle()
    const tools = app.listTranscript(project.coordinatorId).filter((e) => e.kind === 'tool')
    expect(tools.map((e) => e.kind === 'tool' && [e.isError, e.content])).toEqual([
      [true, '"items" must be an array'],
      [true, 'Unknown tool "no_such_tool".'],
    ])
  })
})

describe('control', () => {
  test('the user can stop a turn; it does not restart by itself', async () => {
    const model = fakeModel(
      (request) =>
        new Promise((resolve) => {
          request.signal?.addEventListener('abort', () =>
            resolve([{ type: 'finish', reason: 'stop' }]),
          )
        }),
    )
    const project = app.createProject('P')
    app.postMessage(project.id, {
      conversationId: project.coordinatorId,
      author: 'user',
      content: 'x',
    })
    const s = startScheduler(model)
    await new Promise((resolve) => setTimeout(resolve, 5))
    expect(app.getConversation(project.coordinatorId).turn).not.toBeNull()
    expect(s.cancel(project.coordinatorId)).toBe(true)
    await s.idle()
    expect(app.getConversation(project.coordinatorId)).toMatchObject({
      turn: null,
      lastOutcome: 'cancelled',
      pendingSeq: 0,
    })
  })

  test('slots limit concurrent turns, but a user message can take the reserved slot', async () => {
    let release!: () => void
    const gate = new Promise<void>((resolve) => (release = resolve))
    let peak = 0
    let active = 0
    const model = fakeModel(async () => {
      active++
      peak = Math.max(peak, active)
      await gate
      active--
      return say('')
    })
    const project = app.createProject('P')
    const a = app.createThread(project.id, { title: 'a' })
    const b = app.createThread(project.id, { title: 'b' })
    for (const thread of [a, b]) {
      app.postMessage(project.id, {
        conversationId: thread.id,
        author: 'coordinator',
        kind: 'relay',
        content: 'go',
      })
    }
    const s = startScheduler(model, { slots: 1 })
    await new Promise((resolve) => setTimeout(resolve, 5))
    expect(app.listRunningConversations()).toHaveLength(1)

    // A relay does not get the reserved slot; the user's own message does.
    app.postMessage(project.id, {
      conversationId: project.coordinatorId,
      author: 'user',
      content: 'hi',
    })
    await new Promise((resolve) => setTimeout(resolve, 5))
    expect(
      app
        .listRunningConversations()
        .map((c: ConversationView) => c.role)
        .toSorted(),
    ).toEqual(['coordinator', 'thread'])
    release()
    await s.idle()
    expect(peak).toBe(2)
    expect(app.listPendingConversations()).toEqual([])
  })

  test('a turn left running by a stopped process is closed and resumed', async () => {
    const project = app.createProject('P')
    app.postMessage(project.id, {
      conversationId: project.coordinatorId,
      author: 'user',
      content: 'x',
    })
    const turnId = app.startTurn(project.coordinatorId, 'fake')
    app.recordStep(project.coordinatorId, turnId, {
      text: '',
      toolCalls: [{ id: 'c1', name: 'list_threads', arguments: '{}' }],
      sawSeq: app.lastSeq(),
      usage: null,
    })
    const model = fakeModel(() => say('继续'))
    await startScheduler(model).idle()

    const tool = app.listTranscript(project.coordinatorId).find((e) => e.kind === 'tool')
    expect(tool).toMatchObject({ toolCallId: 'c1', isError: true })
    expect(app.listMessages(project.coordinatorId).at(-1)?.content).toBe('继续')
  })
})

describe('transcript to model messages', () => {
  test('holds input that arrived while tools ran until the results are in', () => {
    const messages = toChatMessages(
      [
        {
          seq: 1,
          kind: 'step',
          text: '',
          sawSeq: 0,
          toolCalls: [
            { id: 'a', name: 'x', arguments: '{}' },
            { id: 'b', name: 'y', arguments: '{}' },
          ],
        },
        {
          seq: 2,
          kind: 'note',
          note: { type: 'user-in-thread', threadId: 't', content: '插一句' },
        },
        { seq: 3, kind: 'tool', toolCallId: 'a', name: 'x', content: 'ok', isError: false },
      ],
      () => '标题',
    )
    expect(messages.map((m) => m.role)).toEqual(['assistant', 'tool', 'tool', 'user'])
    expect(messages[2]).toMatchObject({ toolCallId: 'b', isError: true })
    expect(messages[3]?.content).toContain('title="标题"')
  })

  test('puts input that arrived while the model was answering after that answer', () => {
    const report = { type: 'report', threadId: 't', report: 'done', summary: '好了' } as const
    const messages = toChatMessages(
      [
        { seq: 1, kind: 'message', message: { content: '开始', kind: 'chat' } as never },
        { seq: 2, kind: 'note', note: report },
        { seq: 3, kind: 'step', text: '我去安排', toolCalls: [], sawSeq: 1 },
      ],
      () => 't',
    )
    expect(messages.map((m) => [m.role, m.content.slice(0, 4)])).toEqual([
      ['user', '开始'],
      ['assistant', '我去安排'],
      ['user', '<thr'],
    ])
  })
})
