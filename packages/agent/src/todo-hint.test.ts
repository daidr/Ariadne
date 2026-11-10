import { Ariadne } from '@ariadne/core'
import type { ModelRequest } from '@ariadne/plugin-sdk'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { TODO_MISSING_AFTER, TODO_MISSING_HINT, TODO_STALE_AFTER, TODO_STALE_HINT } from './runner'
import { Scheduler } from './scheduler'
import { call, fakeModel, lastUser, say } from './test-model'

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

const toolResults = (request: ModelRequest) =>
  request.messages.filter((m) => m.role === 'tool').map((m) => m.content)

/** A thread that takes `steps` steps of one call each, chosen by `pick`, then answers. */
async function runThread(steps: number, pick: (step: number) => ReturnType<typeof call>) {
  const project = app.createProject('P')
  const thread = app.createThread(project.id, { title: '调研' })
  const model = fakeModel((request, role) => {
    if (role !== 'thread') return say('')
    const step = toolResults(request).length
    if (step >= steps) return say('')
    return [pick(step), { type: 'finish', reason: 'tool-calls' }]
  })
  app.postMessage(project.id, { conversationId: thread.id, author: 'user', content: '开始吧' })
  scheduler = new Scheduler({
    app,
    resolveModel: () => model,
    locale: () => 'zh-CN',
    retryBaseMs: 1,
  })
  scheduler.start()
  await scheduler.idle()
  const last = model.requests
    .filter((r) => !r.messages[0]?.content.includes('You are the coordinator'))
    .at(-1)!
  return toolResults(last)
}

// Each look asks for something else, so the turn is not taken for going in circles.
const look = (step: number) => call(`c${step}`, 'list_messages', { limit: step + 1 })
const todo = (step: number, status: 'doing' | 'done' = 'doing') =>
  call(`c${step}`, 'update_todo', { items: [{ label: '看看', status }] })

describe('the todo list reminder', () => {
  test('a thread working without a todo list is reminded once', async () => {
    const results = await runThread(TODO_MISSING_AFTER + 3, look)
    const hinted = results.map((r) => r.includes(TODO_MISSING_HINT))
    expect(hinted.indexOf(true)).toBe(TODO_MISSING_AFTER - 1)
    expect(hinted.filter(Boolean)).toHaveLength(1)
  })

  test('a thread that writes its todo list first is not reminded', async () => {
    const results = await runThread(TODO_MISSING_AFTER + 3, (step) =>
      step === 0 ? todo(step) : look(step),
    )
    expect(results.some((r) => r.includes('[Ariadne: you are working without'))).toBe(false)
  })

  test('a todo list left standing for many steps is reminded once', async () => {
    const results = await runThread(TODO_STALE_AFTER + 5, (step) =>
      step === 0 ? todo(step) : look(step),
    )
    const hinted = results.map((r) => r.includes(TODO_STALE_HINT))
    expect(hinted.indexOf(true)).toBe(TODO_STALE_AFTER)
    expect(hinted.filter(Boolean)).toHaveLength(1)
  })

  test('a finished todo list is not stale', async () => {
    const results = await runThread(TODO_STALE_AFTER + 5, (step) =>
      step === 0 ? todo(step, 'done') : look(step),
    )
    expect(results.some((r) => r.includes(TODO_STALE_HINT))).toBe(false)
  })
})

describe('the project source set later', () => {
  test('wakes the coordinator, which hears of it', async () => {
    const seen: string[] = []
    const model = fakeModel((request, role) => {
      if (role === 'coordinator') seen.push(lastUser(request))
      return say('')
    })
    const project = app.createProject('P')
    app.postMessage(project.id, {
      conversationId: project.coordinatorId,
      author: 'user',
      content: '帮我看看这个仓库',
    })
    scheduler = new Scheduler({
      app,
      resolveModel: () => model,
      locale: () => 'zh-CN',
      retryBaseMs: 1,
    })
    scheduler.start()
    await scheduler.idle()
    expect(seen).toHaveLength(1)

    app.setProjectSource(project.id, { kind: 'memory', settings: {} })
    await scheduler.idle()
    expect(seen).toHaveLength(2)
    expect(seen[1]).toContain('<project_source_changed>')
    expect(seen[1]).toContain('go on with that now')
  })

  test('set with the project, before anything was said, is not a note', async () => {
    const project = app.createProject('P')
    app.setProjectSource(project.id, { kind: 'memory', settings: {} })
    const coordinator = app.getConversation(project.coordinatorId)
    expect(coordinator.pendingSeq).toBe(0)
    expect(app.listTranscript(project.coordinatorId)).toEqual([])
  })
})
