import { Ariadne } from '@ariadne/core'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import type { ResolvedModel } from './runner'
import { Scheduler } from './scheduler'
import { answered, call, fakeModel, lastUser, say } from './test-model'
import type { ModelChoice } from './tools'
import { buildTrace, traceInput } from './trace'

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
    expect(step).toMatchObject({ kind: 'step', usage: { input: 100, output: 20 } })
    if (step?.kind !== 'step') throw new Error('expected a step')
    expect(step.toolCalls[0]).toMatchObject({
      name: 'update_todo',
      result: { content: 'Todo list updated.', isError: false },
    })
    expect(answer).toMatchObject({ kind: 'step', text: '好了', toolCalls: [] })
    // The second call adds the first one's step and result to what that one read.
    if (answer?.kind !== 'step') throw new Error('expected a step')
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
