import { Ariadne, AWAY_MS } from '@ariadne/core'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { createHandler } from './app'

const token = 'test-token'
let app: Ariadne
let handle: ReturnType<typeof createHandler>
let clock = 1_000

beforeEach(() => {
  clock = 1_000
  app = new Ariadne({ path: ':memory:', now: () => ++clock })
  handle = createHandler({ app, token })
})

afterEach(() => app.close())

const back = (projectId: string, body: unknown) =>
  handle(
    new Request(`http://127.0.0.1/api/projects/${projectId}/return`, {
      method: 'POST',
      headers: { authorization: `Bearer ${token}`, 'content-type': 'application/json' },
      body: JSON.stringify(body),
    }),
  )

describe('coming back', () => {
  test('puts the recap among what changed in the coordinator conversation', async () => {
    const project = app.createProject('P')
    const thread = app.createThread(project.id, { title: '修导出' })
    app.startThread(thread.id)
    const since = app.lastSeq()
    clock += AWAY_MS
    app.reportThread(thread.id, { kind: 'done', summary: '完成', headline: '完成' })

    expect((await back(project.id, { away: AWAY_MS })).status).toBe(204)
    const page = app.pageMessages(project.coordinatorId, { since })
    expect(page.notices).toMatchObject([{ kind: 'recap', done: [thread.id] }])
  })

  test('refuses a body without a duration and a project that is not there', async () => {
    const project = app.createProject('P')
    expect((await back(project.id, { away: 'long' })).status).toBe(400)
    expect((await back('prj_nope', { away: AWAY_MS })).status).toBe(404)
  })
})
