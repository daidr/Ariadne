import { Ariadne } from '@ariadne/core'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { Scheduler } from './scheduler'
import { fakeModel } from './test-model'

let app: Ariadne
let scheduler: Scheduler
beforeEach(() => {
  app = new Ariadne({ path: ':memory:' })
})
afterEach(async () => {
  await scheduler?.stop()
  app.close()
})

function start(slots: number, reservedSlots: number) {
  const model = fakeModel(
    (request) =>
      new Promise((resolve) => {
        const stopped = () => resolve([{ type: 'finish', reason: 'stop' }])
        if (request.signal?.aborted) stopped()
        else request.signal?.addEventListener('abort', stopped, { once: true })
      }),
  )
  scheduler = new Scheduler({
    app,
    resolveModel: () => model,
    locale: () => 'zh-CN',
    slots,
    reservedSlots,
  })
  scheduler.start()
  return model
}
function worker(projectId: string, author: 'user' | 'coordinator') {
  const thread = app.createThread(projectId, { title: '工作' })
  app.postMessage(projectId, {
    conversationId: thread.id,
    author,
    content: '执行',
    ...(author === 'coordinator' ? { kind: 'relay' as const } : {}),
  })
  return thread
}

test('coordinators receive user messages and thread reports with all ordinary and reserved slots occupied', async () => {
  const a = app.createProject('A')
  const b = app.createProject('B')
  app.setProjectSlots(a.id, 1)
  const general = worker(a.id, 'coordinator')
  const reserved = worker(a.id, 'user')
  const queued = worker(a.id, 'coordinator')
  start(1, 1)
  await vi.waitFor(() => expect(scheduler.isRunning(reserved.id)).toBe(true))
  expect(scheduler.isRunning(general.id)).toBe(true)
  app.postMessage(a.id, { conversationId: a.coordinatorId, author: 'user', content: '紧急更正' })
  const report = app.createThread(b.id, { title: '汇报' })
  app.reportThread(report.id, { kind: 'waiting', headline: '关键信息', summary: '需要立即协调' })
  await vi.waitFor(() => {
    expect(scheduler.isRunning(a.coordinatorId)).toBe(true)
    expect(scheduler.isRunning(b.coordinatorId)).toBe(true)
  })
  expect(scheduler.isRunning(queued.id)).toBe(false)
  expect(app.listRunningConversations()).toHaveLength(4)
  const turn = app.getConversation(a.coordinatorId).turn!.id
  app.postMessage(a.id, { conversationId: a.coordinatorId, author: 'user', content: '还有一条' })
  await Promise.resolve()
  expect(app.getConversation(a.coordinatorId).turn!.id).toBe(turn)
})

test('coordinators neither consume worker slots nor weaken project and global worker limits', async () => {
  const p = app.createProject('P')
  app.setProjectSlots(p.id, 1)
  app.postMessage(p.id, { conversationId: p.coordinatorId, author: 'user', content: '协调' })
  const a = worker(p.id, 'coordinator')
  const b = worker(p.id, 'coordinator')
  start(2, 0)
  await vi.waitFor(() => expect(scheduler.isRunning(a.id)).toBe(true))
  expect(scheduler.isRunning(p.coordinatorId)).toBe(true)
  expect(scheduler.isRunning(b.id)).toBe(false)
  app.setProjectSlots(p.id, null)
  await vi.waitFor(() => expect(scheduler.isRunning(b.id)).toBe(true))
  const c = worker(p.id, 'coordinator')
  await Promise.resolve()
  expect(scheduler.isRunning(c.id)).toBe(false)
  expect(app.listRunningConversations()).toHaveLength(3)
})

test('a held project still prevents its coordinator from starting until released', async () => {
  const p = app.createProject('P')
  start(1, 0)
  const release = await scheduler.holdProject(p.id)
  app.postMessage(p.id, { conversationId: p.coordinatorId, author: 'user', content: '协调' })
  await Promise.resolve()
  expect(scheduler.isRunning(p.coordinatorId)).toBe(false)
  release()
  await vi.waitFor(() => expect(scheduler.isRunning(p.coordinatorId)).toBe(true))
})
