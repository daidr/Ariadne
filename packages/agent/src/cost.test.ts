import { Ariadne } from '@ariadne/core'
import { expect, test } from 'vitest'
import { withCost } from './model'
import { Scheduler } from './scheduler'
import { fakeModel } from './test-model'

test('a request costs its fresh input, cache reads and writes, and output at the model’s price', () => {
  const model = {
    ...fakeModel(() => []),
    price: { currency: 'USD' as const, input: 3, output: 15, cacheRead: 0.3 },
  }
  const usage = withCost(
    { input: 1_000_000, output: 100_000, cached: 600_000, cacheWrite: 100_000 },
    model,
  )
  // 300k fresh × 3 + 600k × 0.3 + 100k written at the input price × 3 + 100k × 15.
  expect(usage?.cost?.currency).toBe('USD')
  expect(usage?.cost?.amount).toBeCloseTo(0.9 + 0.18 + 0.3 + 1.5)
  // What the service reported stands; unpriced usage stays tokens only.
  const reported = { input: 10, output: 1, cost: { amount: 0.5, currency: 'USD' as const } }
  expect(withCost(reported, model)).toBe(reported)
  expect(
    withCost(
      { input: 10, output: 1 },
      fakeModel(() => []),
    )?.cost,
  ).toBeUndefined()
})

test('a conversation sums its requests’ cost by currency; a reported cost wins', async () => {
  const app = new Ariadne({ path: ':memory:' })
  let step = 0
  const model = {
    ...fakeModel(() => {
      step++
      return [
        { type: 'text', text: `回复 ${step}` },
        step === 1
          ? { type: 'usage', input: 1_000_000, output: 0 }
          : { type: 'usage', input: 10, output: 1, costUsd: 0.25 },
        { type: 'finish', reason: 'stop' },
      ]
    }),
    price: { currency: 'CNY' as const, input: 2, output: 8 },
  }
  const scheduler = new Scheduler({ app, resolveModel: () => model, locale: () => 'zh-CN' })
  scheduler.start()
  const project = app.createProject('P')
  for (const content of ['一', '二']) {
    app.postMessage(project.id, { conversationId: project.coordinatorId, author: 'user', content })
    await scheduler.idle()
  }
  await scheduler.stop()
  expect(app.getConversation(project.coordinatorId).usage.cost).toEqual({ USD: 0.25, CNY: 2 })
  expect(app.getProject(project.id).usage.cost).toEqual({ USD: 0.25, CNY: 2 })
  app.close()
})
