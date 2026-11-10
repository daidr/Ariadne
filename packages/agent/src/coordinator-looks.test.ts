import { Ariadne } from '@ariadne/core'
import type { ModelRequest } from '@ariadne/plugin-sdk'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { COORDINATOR_LOOK_HINT, COORDINATOR_LOOK_LIMIT, LOOK_HINT, LOOK_REFUSED } from './runner'
import { Scheduler } from './scheduler'
import { call, fakeModel, say } from './test-model'

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

/** A coordinator turn of one call a step, chosen by `pick`, until `steps`. */
async function runCoordinator(steps: number, pick: (step: number) => ReturnType<typeof call>) {
  const model = fakeModel((request, role) => {
    if (role !== 'coordinator') return say('')
    const step = toolResults(request).length
    if (step >= steps) return say('好')
    return [pick(step), { type: 'finish', reason: 'tool-calls' }]
  })
  const project = app.createProject('P')
  app.postMessage(project.id, {
    conversationId: project.coordinatorId,
    author: 'user',
    content: '看看怎么做',
  })
  scheduler = new Scheduler({
    app,
    resolveModel: () => model,
    locale: () => 'zh-CN',
    retryBaseMs: 1,
  })
  scheduler.start()
  await scheduler.idle()
  const last = model.requests
    .filter((r) => r.messages[0]?.content.includes('You are the coordinator'))
    .at(-1)!
  return { results: toolResults(last), project }
}

// Each look asks for something else, so the turn is not taken for going in circles; a local
// address is refused at once, without the network.
const look = (step: number) => call(`c${step}`, 'fetch_url', { url: `http://127.0.0.1/${step}` })

describe("the coordinator's looking", () => {
  test('is reminded once, then turned away', async () => {
    const { results } = await runCoordinator(COORDINATOR_LOOK_LIMIT + 3, look)
    const hinted = results.map((r) => r.includes(LOOK_HINT))
    expect(hinted.indexOf(true)).toBe(COORDINATOR_LOOK_HINT - 1)
    expect(hinted.filter(Boolean)).toHaveLength(1)
    const refused = results.map((r) => r.includes(LOOK_REFUSED))
    expect(refused.indexOf(true)).toBe(COORDINATOR_LOOK_LIMIT)
    expect(refused.filter(Boolean)).toHaveLength(3)
  })

  test('may look again after handing work to a thread', async () => {
    const { results, project } = await runCoordinator(COORDINATOR_LOOK_LIMIT + 4, (step) =>
      step === COORDINATOR_LOOK_LIMIT - 1
        ? call(`c${step}`, 'create_thread', { title: '调研', brief: '看看' })
        : look(step),
    )
    expect(results.some((r) => r.includes(LOOK_REFUSED))).toBe(false)
    expect(app.listThreads(project.id)).toHaveLength(1)
  })
})
