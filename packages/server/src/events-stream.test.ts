import { Ariadne } from '@ariadne/core'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { eventStream, MAX_SSE_PENDING_BYTES, MAX_SSE_PENDING_UPDATES } from './events-stream'

let app: Ariadne
let ending: AbortController[]
beforeEach(() => {
  app = new Ariadne({ path: ':memory:' })
  ending = []
})
afterEach(() => {
  ending.forEach((controller) => controller.abort())
  app.close()
  vi.useRealTimers()
  vi.restoreAllMocks()
})
function open(afterSeq = 0, projectId: string | null = null, heartbeatMs?: number) {
  const controller = new AbortController()
  ending.push(controller)
  const response = eventStream(app, { afterSeq, projectId, signal: controller.signal, heartbeatMs })
  return { response, controller, reader: () => response.body!.getReader() }
}
const text = (value: Uint8Array | undefined) => new TextDecoder().decode(value)
const live = (projectId: string, conversationId: string) =>
  app.live.start({ projectId, conversationId, command: '检查', cwd: '/work' })

test('a large backlog is read on demand, in order, including events appended during replay', async () => {
  const p = app.createProject('P')
  for (let i = 0; i < 128; i++)
    app.postMessage(p.id, {
      conversationId: p.coordinatorId,
      author: 'user',
      content: String(i) + 'x'.repeat(128_000),
    })
  const read = vi.spyOn(app, 'events')
  const stream = open()
  await Promise.resolve()
  expect(read).not.toHaveBeenCalled()
  const reader = stream.reader()
  const first = text((await reader.read()).value)
  expect(first).toContain('id: 1\n')
  expect(read).toHaveBeenCalledTimes(1)
  app.renameProject(p.id, 'P2')
  for (let seq = 2; seq <= app.lastSeq(); seq++)
    expect(text((await reader.read()).value)).toContain('id: ' + seq + '\n')
  expect(read.mock.calls.every(([options]) => options?.limit === 1)).toBe(true)
  await reader.cancel()
})

test('coalesces transient snapshots while preserving every durable event', async () => {
  const p = app.createProject('P')
  const stream = open(app.lastSeq())
  for (let i = 0; i < 1000; i++)
    app.background.set({
      projectId: p.id,
      conversationId: p.coordinatorId,
      commands: [{ id: 'cmd', command: 'iteration ' + i, startedAt: 0, url: null, result: null }],
    })
  app.renameProject(p.id, 'new')
  const reader = stream.reader()
  expect(text((await reader.read()).value)).toContain('project.renamed')
  const snapshot = text((await reader.read()).value)
  expect(snapshot).toContain('event: background')
  expect(snapshot).toContain('999')
  await reader.cancel()
})

test.each(['count', 'bytes'] as const)(
  'disconnects and cleans subscriptions on transient %s overflow; cursor replay recovers the log',
  async (kind) => {
    const p = app.createProject('P')
    const stream = open()
    const reader = stream.reader()
    expect(text((await reader.read()).value)).toContain('id: 1\n')
    app.renameProject(p.id, 'missed')
    const failed = reader.read()
    if (kind === 'count') {
      for (let i = 0; i <= MAX_SSE_PENDING_UPDATES; i++) live(p.id, String(i))
    } else {
      // Many individually valid live snapshots exceed the total byte budget.
      for (let i = 0; i < 12; i++)
        app.background.set({
          projectId: p.id,
          conversationId: String(i),
          commands: [
            {
              id: 'cmd',
              command: 'x'.repeat(Math.ceil(MAX_SSE_PENDING_BYTES / 8)),
              startedAt: 0,
              url: null,
              result: null,
            },
          ],
        })
    }
    await expect(failed).rejects.toThrow('too slow')
    const retry = open(1)
    const next = retry.reader()
    expect(text((await next.read()).value)).toContain('project.renamed')
    await next.cancel()
  },
)

test('cancellation and request abort release all four subscriptions and the heartbeat timer', async () => {
  vi.useFakeTimers()
  const stops = [vi.fn(), vi.fn(), vi.fn(), vi.fn()]
  vi.spyOn(app, 'subscribe').mockReturnValue(stops[0]!)
  vi.spyOn(app.live, 'subscribe').mockReturnValue(stops[1]!)
  vi.spyOn(app.background, 'subscribe').mockReturnValue(stops[2]!)
  vi.spyOn(app.drafts, 'subscribe').mockReturnValue(stops[3]!)
  const a = open()
  await a.response.body!.cancel()
  const b = open()
  const read = b.reader().read()
  b.controller.abort()
  expect((await read).done).toBe(true)
  for (const stop of stops) expect(stop).toHaveBeenCalledTimes(2)
  expect(vi.getTimerCount()).toBe(0)
})
