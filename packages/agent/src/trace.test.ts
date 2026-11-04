import { Ariadne } from '@ariadne/core'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import type { ResolvedModel } from './runner'
import { Scheduler } from './scheduler'
import { answered, call, fakeModel, lastUser, say } from './test-model'
import type { ModelChoice } from './tools'
import { buildTrace, traceRequest } from './trace'

const traceInput = (app: Ariadne, conversationId: string, seq: number) =>
  traceRequest(app, conversationId, seq).detail

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

function start(model: ResolvedModel, options: { compactAt?: number; models?: ModelChoice[] } = {}) {
  scheduler = new Scheduler({
    app,
    resolveModel: () => model,
    locale: () => 'zh-CN',
    retryBaseMs: 1,
    ...(options.compactAt ? { compactAt: options.compactAt } : {}),
    ...(options.models ? { models: () => options.models! } : {}),
  })
  scheduler.start()
  return scheduler
}

/** Every model call of a conversation the trace knows of, oldest first. */
function calls(conversationId: string) {
  return app
    .events({ limit: 10_000 })
    .filter(
      (e) =>
        (e.type === 'turn.step' || e.type === 'conversation.compacted') &&
        e.data.conversationId === conversationId,
    )
}

describe('the trace', () => {
  test('rebuilds every request exactly as the model was sent it', async () => {
    const model = fakeModel((request) => {
      if (request.messages[0]?.content.startsWith('You summarise')) return say('- 三份需求')
      if (!answered(request) && lastUser(request).includes('第 2 份')) {
        return [call('c1', 'update_todo', { items: [{ label: '整理', status: 'doing' }] })]
      }
      return say('收到')
    })
    const project = app.createProject('P')
    const running = start(model, { compactAt: 600 })
    const first = app.postMessage(project.id, {
      conversationId: project.coordinatorId,
      author: 'user',
      content: `第 1 份需求：${'很长的描述。'.repeat(80)}`,
    })
    await running.idle()
    // Edited after the model read it: the trace shows what it read then.
    app.editMessage(
      first.id,
      { author: 'user', conversationId: project.coordinatorId },
      '改过的第 1 份',
      '',
    )
    for (const n of [2, 3, 4]) {
      app.postMessage(project.id, {
        conversationId: project.coordinatorId,
        author: 'user',
        content: `第 ${n} 份需求：${'很长的描述。'.repeat(80)}`,
      })
      await running.idle()
    }

    const made = calls(project.coordinatorId)
    expect(made.some((e) => e.type === 'conversation.compacted')).toBe(true)
    expect(made).toHaveLength(model.requests.length)
    for (const [index, event] of made.entries()) {
      const input = traceInput(app, project.coordinatorId, event.seq)
      const sent = model.requests[index]!
      expect(input.messages).toEqual(sent.messages)
      expect(input.tools).toEqual(sent.tools)
    }
    const firstInput = traceInput(app, project.coordinatorId, made[0]!.seq)
    expect(JSON.stringify(firstInput.messages)).toContain('第 1 份需求')
    expect(JSON.stringify(firstInput.messages)).not.toContain('改过的')
  })

  test('lists turns with their steps, calls, results, timing and tokens', async () => {
    const model = fakeModel((request) =>
      answered(request)
        ? say('好了')
        : [
            call('c1', 'update_todo', { items: [{ label: '看看', status: 'doing' }] }),
            { type: 'usage', input: 100, output: 20 },
            { type: 'finish', reason: 'tool-calls' },
          ],
    )
    const project = app.createProject('P')
    const running = start(model)
    for (const content of ['一', '二']) {
      app.postMessage(project.id, {
        conversationId: project.coordinatorId,
        author: 'user',
        content,
      })
      await running.idle()
    }

    const { turns, more } = buildTrace(app, project.coordinatorId)
    expect(more).toBe(false)
    expect(turns).toHaveLength(2)
    const [turn] = turns
    expect(turn).toMatchObject({
      model: 'fake / fake-1',
      outcome: 'completed',
      usage: { input: 110, output: 25 },
    })
    expect(turn!.finishedAt).toBeGreaterThanOrEqual(turn!.startedAt)
    const [step, answer] = turn!.items
    expect(step).toMatchObject({
      kind: 'request',
      number: 1,
      purpose: 'step',
      provider: 'fake',
      model: 'fake-1',
      finish: 'tool-calls',
      usage: { input: 100, output: 20 },
    })
    if (step?.kind !== 'request') throw new Error('expected a request')
    expect(step.firstOutputAt).toBeGreaterThanOrEqual(step.startedAt)
    expect(step.finishedAt).toBeGreaterThanOrEqual(step.startedAt)
    expect(step.toolCalls[0]).toMatchObject({
      name: 'update_todo',
      result: { content: 'Todo list updated.', isError: false },
    })
    expect(answer).toMatchObject({
      kind: 'request',
      number: 2,
      finish: 'stop',
      text: '好了',
      toolCalls: [],
    })
    // Numbers go on over turns.
    expect(turns[1]!.items.map((item) => item.kind === 'request' && item.number)).toEqual([3, 4])
    // The second call adds the first one's step and result to what that one read.
    if (answer?.kind !== 'request') throw new Error('expected a request')
    const input = traceInput(app, project.coordinatorId, answer.seq)
    expect(input.newFrom).toBe(input.messages.length - 2)
    expect(input.systemChanged).toBe(false)
    expect(input.messages.slice(input.newFrom).map((m) => m.role)).toEqual(['assistant', 'tool'])

    // Paged back by the oldest turn shown.
    const newest = buildTrace(app, project.coordinatorId, { turns: 1 })
    expect(newest.more).toBe(true)
    expect(newest.turns.map((t) => t.seq)).toEqual([turns[1]!.seq])
    const older = buildTrace(app, project.coordinatorId, { before: turns[1]!.seq, turns: 1 })
    expect(older).toMatchObject({ more: false, turns: [{ seq: turns[0]!.seq }] })
    expect(newest.turns[0]!.items[0]).toMatchObject({ number: 3 })
  })

  test('right after a summary, the system prompt is compared with the step before', async () => {
    const model = fakeModel((request) =>
      request.messages[0]?.content.startsWith('You summarise') ? say('- 前情') : say('收到'),
    )
    const project = app.createProject('P')
    const running = start(model, { compactAt: 600 })
    for (const n of [1, 2]) {
      app.postMessage(project.id, {
        conversationId: project.coordinatorId,
        author: 'user',
        content: `第 ${n} 份：${'很长的描述。'.repeat(80)}`,
      })
      await running.idle()
    }
    const made = calls(project.coordinatorId)
    const compacted = made.findIndex((e) => e.type === 'conversation.compacted')
    expect(compacted).toBeGreaterThan(0)
    const afterSummary = made[compacted + 1]!
    expect(afterSummary.type).toBe('turn.step')
    expect(traceInput(app, project.coordinatorId, afterSummary.seq).systemChanged).toBe(false)
  })

  test('a turn that made no step does not make everything after it new', async () => {
    const model = fakeModel((request) => {
      if (lastUser(request) === '坏') throw new Error('boom')
      return say('收到')
    })
    const project = app.createProject('P')
    const running = start(model)
    for (const content of ['一', '坏', '二']) {
      app.postMessage(project.id, {
        conversationId: project.coordinatorId,
        author: 'user',
        content,
      })
      await running.idle()
    }
    const { turns } = buildTrace(app, project.coordinatorId)
    expect(
      turns.map((turn) => [turn.outcome, turn.items.some((i) => i.kind === 'request')]),
    ).toEqual([
      ['completed', true],
      ['failed', false],
      ['completed', true],
    ])
    const last = calls(project.coordinatorId).at(-1)!
    const input = traceInput(app, project.coordinatorId, last.seq)
    // Since the first step read: its answer, the failed turn's message and this one's.
    expect(input.messages.slice(input.newFrom).map((m) => m.content)).toEqual([
      '收到',
      expect.stringContaining('坏'),
      expect.stringContaining('二'),
    ])
    expect(input.systemChanged).toBe(false)
  })

  test('thinking is kept with its step, sent back, and the request rebuilt with its effort', async () => {
    const model = fakeModel((request) =>
      answered(request)
        ? [{ type: 'reasoning', text: '收尾', signature: 's2' }, ...say('好了')]
        : [
            { type: 'reasoning', text: '先列待办', signature: 's1' },
            call('c1', 'update_todo', { items: [{ label: '看看', status: 'doing' }] }),
            { type: 'usage', input: 100, output: 20, cached: 80, reasoning: 12 },
            { type: 'finish', reason: 'tool-calls' },
          ],
    )
    const project = app.createProject('P')
    app.postMessage(project.id, {
      conversationId: project.coordinatorId,
      author: 'user',
      content: '开始',
    })
    await start({ ...model, effort: 'low' }).idle()

    const second = model.requests[1]!
    expect(second.effort).toBe('low')
    expect(second.messages.at(-2)).toMatchObject({
      role: 'assistant',
      reasoning: [{ text: '先列待办', signature: 's1' }],
    })
    const [turn] = buildTrace(app, project.coordinatorId).turns
    expect(turn!.usage).toEqual({ input: 110, output: 25, cached: 80, reasoning: 12 })
    const [first, answer] = turn!.items
    expect(first).toMatchObject({
      effort: 'low',
      reasoning: [{ text: '先列待办', redacted: false }],
      usage: { cached: 80, reasoning: 12 },
    })
    if (answer?.kind !== 'request') throw new Error('expected a request')
    const { request, detail } = traceRequest(app, project.coordinatorId, answer.seq)
    const { signal: _, ...sent } = second
    expect(request).toEqual(sent)
    expect(detail.call).toEqual({ provider: 'fake', model: 'fake-1', settings: {} })
  })

  test('a secret setting is left out of what is recorded and of the trace', async () => {
    const model = fakeModel(() => say('好'))
    model.provider = {
      ...model.provider,
      settings: {
        type: 'object',
        properties: {
          baseUrl: { type: 'string', title: { 'zh-CN': '地址' } },
          apiKey: { type: 'string', title: { 'zh-CN': '密钥' }, secret: true },
        },
      },
    }
    model.settings = { baseUrl: 'http://models.test', apiKey: 'sk-very-secret' }
    const project = app.createProject('P')
    app.postMessage(project.id, {
      conversationId: project.coordinatorId,
      author: 'user',
      content: '开始',
    })
    await start(model).idle()

    const [step] = calls(project.coordinatorId)
    if (step?.type !== 'turn.step') throw new Error('expected a step')
    const recorded = [step.data.input.system, step.data.input.tools, step.data.input.call]
      .map((hash) => app.getPrompt(project.id, hash))
      .join('\n')
    expect(recorded).toContain('http://models.test')
    const trace = JSON.stringify(buildTrace(app, project.coordinatorId))
    const detail = JSON.stringify(traceRequest(app, project.coordinatorId, step.seq))
    for (const text of [JSON.stringify(app.events({ limit: 10_000 })), recorded, trace, detail]) {
      expect(text).not.toContain('sk-very-secret')
    }
    expect(traceInput(app, project.coordinatorId, step.seq).call?.settings).toEqual({
      baseUrl: 'http://models.test',
    })
  })

  test('reasoning goes back only to the model that wrote it', async () => {
    const thinker = fakeModel(() => [{ type: 'reasoning', text: '我先想想' }, ...say('好')])
    const other = fakeModel(() => say('嗯'))
    other.model = 'fake-2'
    let current: ResolvedModel = thinker
    scheduler = new Scheduler({
      app,
      resolveModel: () => current,
      locale: () => 'zh-CN',
      retryBaseMs: 1,
    })
    scheduler.start()
    const project = app.createProject('P')
    const say1 = (content: string) =>
      app.postMessage(project.id, {
        conversationId: project.coordinatorId,
        author: 'user',
        content,
      })
    say1('一')
    await scheduler.idle()
    say1('二')
    await scheduler.idle()
    // The same model reads its reasoning back.
    expect(thinker.requests[1]!.messages.find((m) => m.role === 'assistant')).toMatchObject({
      reasoning: [{ text: '我先想想' }],
    })
    current = other
    say1('三')
    await scheduler.idle()
    const sent = other.requests[0]!
    const assistants = sent.messages.filter((m) => m.role === 'assistant')
    expect(assistants).toHaveLength(2)
    for (const message of assistants) expect(message).not.toHaveProperty('reasoning')
    // The trace rebuilds the request the other model was sent, without the reasoning too.
    const step = calls(project.coordinatorId).at(-1)!
    const { signal: _, ...request } = sent
    expect(traceRequest(app, project.coordinatorId, step.seq).request).toEqual(request)
  })

  test("refuses a seq that is not one of the conversation's calls", async () => {
    const project = app.createProject('P')
    const other = app.createProject('Q')
    const model = fakeModel(() => say('好'))
    app.postMessage(project.id, {
      conversationId: project.coordinatorId,
      author: 'user',
      content: 'x',
    })
    await start(model).idle()
    const step = calls(project.coordinatorId)[0]!
    expect(() => traceInput(app, other.coordinatorId, step.seq)).toThrow()
    expect(() => traceInput(app, project.coordinatorId, 1)).toThrow()
  })
})

describe('models per role', () => {
  const models: ModelChoice[] = [
    { id: 'big', label: 'p / big', note: '难的活', threadDefault: true },
    { id: 'small', label: 'p / small', note: '便宜，适合调研', threadDefault: false },
  ]

  test('the coordinator sees the models and gives one to a thread it opens', async () => {
    const model = fakeModel((request, role) => {
      if (role === 'thread') return say('')
      if (answered(request)) return say('开好了')
      return [
        call('c1', 'create_thread', { title: '调研', brief: '去调研', model: 'small' }),
        call('c2', 'create_thread', { title: '另一个', brief: 'x', model: 'nope' }),
      ]
    })
    const project = app.createProject('P')
    app.postMessage(project.id, {
      conversationId: project.coordinatorId,
      author: 'user',
      content: '开工',
    })
    await start(model, { models }).idle()

    const first = model.requests[0]!
    expect(first.messages[0]?.content).toContain('[small] p / small: 便宜，适合调研')
    expect(first.messages[0]?.content).toContain('[big] p / big (default for threads)')
    const spec = first.tools.find((tool) => tool.name === 'create_thread')
    expect(spec?.parameters).toMatchObject({
      properties: { model: { enum: ['big', 'small'] } },
    })
    const threads = app.listThreads(project.id)
    expect(threads.map((t) => [t.title, t.model])).toEqual([['调研', 'small']])
    const results = app
      .listTranscript(project.coordinatorId)
      .filter((e) => e.kind === 'tool')
      .map((e) => e.kind === 'tool' && e.isError)
    expect(results).toEqual([false, true])
  })

  test('with one model there is nothing to choose', async () => {
    const model = fakeModel(() => say('好'))
    const project = app.createProject('P')
    app.postMessage(project.id, {
      conversationId: project.coordinatorId,
      author: 'user',
      content: 'x',
    })
    await start(model, { models: models.slice(0, 1) }).idle()
    const request = model.requests[0]!
    expect(request.messages[0]?.content).not.toContain('Models:')
    const spec = request.tools.find((tool) => tool.name === 'create_thread')
    expect(JSON.stringify(spec?.parameters)).not.toContain('"model"')
  })
})
