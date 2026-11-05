import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { Ariadne } from './ariadne'
import { EventStore } from './store'

const CHILD = join(import.meta.dirname, 'testing/crash-child.ts')
const STEP_INPUT = { system: 'system', tools: '[]', call: '{}', images: true }
const TIMING = { startedAt: 0, firstOutputAt: null }

/** Run the crash child against `path`; it dies once an event of type `crashAt` is written. */
function crash(path: string, crashAt: string, scenario: string, ...args: string[]): number {
  const child = Bun.spawnSync([process.execPath, CHILD, path, crashAt, scenario, ...args], {
    stdout: 'pipe',
    stderr: 'pipe',
    windowsHide: true,
  })
  if (child.exitCode !== 137) console.error(child.stderr.toString())
  return child.exitCode
}

describe('a crash in the middle of a write', () => {
  let dir: string
  let path: string

  beforeEach(() => {
    dir = mkdtempSync(join(tmpdir(), 'ariadne-crash-'))
    path = join(dir, 'ariadne.db')
  })
  afterEach(() => rmSync(dir, { recursive: true, force: true }))

  test('leaves a transaction out entirely, and listeners hear it only once it commits', () => {
    const store = new EventStore({ path: ':memory:', projections: [] })
    const heard: string[][] = []
    store.subscribe((events) => heard.push(events.map((event) => event.type)))
    store.transaction(() => {
      store.append([{ type: 'project.created', projectId: 'p', data: {} } as never])
      expect(heard).toEqual([])
      store.append([{ type: 'project.renamed', projectId: 'p', data: {} } as never])
    })
    expect(heard).toEqual([['project.created', 'project.renamed']])
    expect(() =>
      store.transaction(() => {
        store.append([{ type: 'project.renamed', projectId: 'p', data: {} } as never])
        throw new Error('stop')
      }),
    ).toThrow('stop')
    expect(store.read().map((event) => event.type)).toEqual(['project.created', 'project.renamed'])
    expect(heard).toHaveLength(1)
    store.close()
  })

  test('a reviewer’s finished turn and the round it leaves are written together', () => {
    let app = new Ariadne({ path })
    const project = app.createProject('P')
    const authorId = app.createThread(project.id, { title: '修导出', model: 'm1' }).id
    app.startThread(authorId)
    app.recordWorkspace(authorId, { label: 'local', branch: 'ariadne/x', base: 'main' })
    app.reportThread(authorId, { kind: 'ready', summary: '改好了', headline: '好了' }, undefined, {
      reviewable: true,
      head: 'h1',
      reviewerTitle: '审阅：修导出',
    })
    const reviewerId = app.getThread(authorId).review!.reviewerId
    const turnId = app.startTurn(reviewerId, 'm1')
    app.recordStep(reviewerId, turnId, {
      reasoning: [],
      text: '',
      toolCalls: [],
      sawSeq: app.lastSeq(),
      usage: null,
      input: STEP_INPUT,
      timing: TIMING,
    })
    app.close()

    // The process dies right as the round is marked left, after the turn's end was written.
    expect(crash(path, 'review.abandoned', 'finish-turn', reviewerId, turnId)).toBe(137)

    app = new Ariadne({ path })
    // Neither happened: the turn still runs, so the next start closes it as interrupted and
    // the reviewer goes on, instead of a finished reviewer holding the round forever.
    expect(app.listRunningConversations().map((c) => c.id)).toEqual([reviewerId])
    expect(app.listReviews(authorId).at(-1)).toMatchObject({ verdict: null, abandoned: false })

    // Without the crash both are in.
    app.finishTurn(reviewerId, turnId, 'completed')
    expect(app.listRunningConversations()).toEqual([])
    expect(app.listReviews(authorId).at(-1)).toMatchObject({ abandoned: true })
    app.close()
  })

  test('a thread is never there without its brief', () => {
    let app = new Ariadne({ path })
    const project = app.createProject('P')
    app.close()
    expect(crash(path, 'message.posted', 'create-thread', project.id)).toBe(137)
    app = new Ariadne({ path })
    expect(app.listThreads(project.id)).toEqual([])
    app.close()
  })
})
