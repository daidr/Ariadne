import { Ariadne, nextRun, type Routine } from '@ariadne/core'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { BackgroundCommands } from './background'
import { buildTrace } from './trace'
import { answered, call, fakeModel, lastUser, say } from './test-model'
import { Scheduler } from './scheduler'
import type { ResolvedModel } from './runner'
import { Timers } from './timers'
import { waitUntil } from './wait-tool'
import { keptNote, type WorkspaceManager } from './workspaces'

let app: Ariadne
let scheduler: Scheduler | null = null
let timers: Timers | null = null

beforeEach(() => {
  app = new Ariadne({ path: ':memory:' })
})

afterEach(async () => {
  timers?.stop()
  timers = null
  await scheduler?.stop()
  scheduler = null
  app.close()
})

function startScheduler(model: ResolvedModel) {
  scheduler = new Scheduler({
    app,
    resolveModel: () => model,
    locale: () => 'zh-CN',
    retryBaseMs: 1,
  })
  scheduler.start()
  return scheduler
}

function userSays(projectId: string, conversationId: string, content: string) {
  app.postMessage(projectId, { conversationId, author: 'user', content })
}

const at = (y: number, mo: number, d: number, h: number, mi: number) =>
  new Date(y, mo - 1, d, h, mi).getTime()

describe('waitUntil', () => {
  // A Thursday.
  const now = at(2026, 11, 5, 14, 10)

  test('minutes count from now; a clock time is the next one to come', () => {
    expect(waitUntil({ minutes: 20 }, now)).toBe(now + 20 * 60_000)
    expect(waitUntil({ at: '14:30' }, now)).toBe(at(2026, 11, 5, 14, 30))
    expect(waitUntil({ at: '9:05' }, now)).toBe(at(2026, 11, 6, 9, 5))
    expect(waitUntil({ at: '2026-11-07 08:00' }, now)).toBe(at(2026, 11, 7, 8, 0))
  })

  test('refuses the past, more than a week, both or neither', () => {
    expect(() => waitUntil({ at: '2026-11-05 14:00' }, now)).toThrow('already passed')
    expect(() => waitUntil({ minutes: 8 * 24 * 60 }, now)).toThrow()
    expect(() => waitUntil({ at: '2026-11-20 08:00' }, now)).toThrow('a week')
    expect(() => waitUntil({ minutes: 5, at: '15:00' }, now)).toThrow('either')
    expect(() => waitUntil({}, now)).toThrow('either')
    expect(() => waitUntil({ at: 'soon' }, now)).toThrow()
  })
})

describe('the wait tool', () => {
  test('a wait ends the turn, comes due, and wakes the agent with its note', async () => {
    let phase = 'set'
    const model = fakeModel((request) => {
      if (phase === 'set') {
        if (answered(request)) return say('')
        return [
          call('w1', 'wait', { minutes: 30, note: '看部署是否完成' }),
          { type: 'finish', reason: 'tool-calls' },
        ]
      }
      return say(lastUser(request).includes('<wait_over') ? '部署完成了' : '?')
    })
    const project = app.createProject('P')
    userSays(project.id, project.coordinatorId, '部署后看一下')
    const s = startScheduler(model)
    await s.idle()
    const wait = app.getConversation(project.coordinatorId).wait
    expect(wait).toMatchObject({ note: '看部署是否完成' })
    const tool = app.listTranscript(project.coordinatorId).find((e) => e.kind === 'tool')
    expect(tool?.kind === 'tool' && tool.content).toContain('You will be woken at')

    // Not due yet: nothing happens.
    let now = wait!.until - 1000
    timers = new Timers(app, () => now)
    timers.start()
    timers.check()
    expect(app.getConversation(project.coordinatorId).wait).not.toBeNull()

    phase = 'due'
    now = wait!.until
    timers.check()
    await s.idle()
    expect(app.getConversation(project.coordinatorId).wait).toBeNull()
    expect(app.listMessages(project.coordinatorId).at(-1)?.content).toBe('部署完成了')
    const woken = app.listNotices(project.coordinatorId).find((n) => n.kind === 'woken')
    expect(woken).toMatchObject({ kind: 'woken', how: 'due', note: '看部署是否完成' })
  })

  test('a new wait replaces the old one, and the agent can drop its own', () => {
    const project = app.createProject('P')
    const id = project.coordinatorId
    const first = app.setWait(id, { until: Date.now() + 60_000, note: 'a' })
    const second = app.setWait(id, { until: Date.now() + 120_000, note: 'b' })
    expect(app.getConversation(id).wait).toMatchObject({ id: second, note: 'b' })
    expect(app.endWait(id, 'due', 'time', first)).toBe(false)
    expect(app.listWaits().map((w) => w.waitId)).toEqual([second])
    // Its own replacing and dropping tell it nothing and wake nothing.
    expect(app.endWait(id, 'cancelled', id)).toBe(true)
    expect(app.listTranscript(id)).toEqual([])
    expect(app.getConversation(id).pendingSeq).toBe(0)
  })

  test('woken early by the user it is told so; called off it learns it without waking', () => {
    const project = app.createProject('P')
    const id = project.coordinatorId
    app.setWait(id, { until: Date.now() + 60_000, note: '看 CI' })
    app.endWait(id, 'now', 'user')
    expect(app.getConversation(id).pendingSeq).toBeGreaterThan(0)
    const notes = app.listTranscript(id).filter((e) => e.kind === 'note')
    expect(notes.map((e) => e.kind === 'note' && e.note)).toEqual([
      expect.objectContaining({ type: 'wait-over', how: 'now', note: '看 CI' }),
    ])

    const thread = app.createThread(project.id, { title: 't' })
    app.setWait(thread.id, { until: Date.now() + 60_000, note: '看部署' })
    app.endWait(thread.id, 'cancelled', 'user')
    expect(app.getConversation(thread.id).pendingSeq).toBe(0)
    expect(app.listTranscript(thread.id).map((e) => e.kind === 'note' && e.note.type)).toEqual([
      'wait-over',
    ])
    expect(app.listNotices(thread.id).some((n) => n.kind === 'woken')).toBe(false)
  })

  test('stopping a thread drops its wait', () => {
    const project = app.createProject('P')
    const thread = app.createThread(project.id, { title: 't' })
    app.setWait(thread.id, { until: Date.now() + 60_000, note: '看部署' })
    app.stopThread(thread.id, { reason: '先停', by: 'user' })
    expect(app.getConversation(thread.id).wait).toBeNull()
    expect(app.listWaits()).toEqual([])
  })

  test('a wait that came due while the server was down ends on start', () => {
    const project = app.createProject('P')
    app.setWait(project.coordinatorId, { until: 1000, note: 'x' })
    timers = new Timers(app, () => 5000)
    timers.start()
    timers.check()
    expect(app.getConversation(project.coordinatorId).wait).toBeNull()
    expect(app.getConversation(project.coordinatorId).pendingSeq).toBeGreaterThan(0)
  })
})

function routine(over: Partial<Routine> = {}): Routine {
  return {
    id: 'r1',
    prompt: '整理新 issue',
    days: [1, 2, 3, 4, 5],
    time: '09:00',
    enabled: true,
    ...over,
  }
}

describe('routines', () => {
  test('nextRun finds the next picked day at that time', () => {
    // Thursday 2026-11-05.
    expect(nextRun(routine(), at(2026, 11, 5, 8, 0))).toBe(at(2026, 11, 5, 9, 0))
    expect(nextRun(routine(), at(2026, 11, 5, 9, 0))).toBe(at(2026, 11, 6, 9, 0))
    // Friday after nine: Monday.
    expect(nextRun(routine(), at(2026, 11, 6, 10, 0))).toBe(at(2026, 11, 9, 9, 0))
    expect(nextRun(routine({ days: [0] }), at(2026, 11, 5, 8, 0))).toBe(at(2026, 11, 8, 9, 0))
    expect(nextRun(routine({ enabled: false }), 0)).toBeNull()
    expect(nextRun(routine({ days: [] }), 0)).toBeNull()
  })

  test('a due routine wakes the coordinator with its words, once', () => {
    const project = app.createProject('P')
    let now = at(2026, 11, 5, 8, 59)
    timers = new Timers(app, () => now)
    timers.start()
    app.setProjectSettings(project.id, { routines: [routine()] }, [])
    timers.check()
    expect(app.listNotices(project.coordinatorId)).toEqual([])

    now = at(2026, 11, 5, 9, 0)
    timers.check()
    timers.check()
    const notices = app.listNotices(project.coordinatorId).filter((n) => n.kind === 'routine')
    expect(notices).toEqual([
      expect.objectContaining({ kind: 'routine', prompt: '整理新 issue', dueAt: now }),
    ])
    expect(app.getConversation(project.coordinatorId).pendingSeq).toBeGreaterThan(0)
    const note = app.listTranscript(project.coordinatorId).find((e) => e.kind === 'note')
    expect(note?.kind === 'note' && note.note).toMatchObject({ type: 'routine' })
  })

  test('a time moved to earlier today is not run late, and missed runs are not made up', () => {
    const project = app.createProject('P')
    let now = at(2026, 11, 5, 10, 0)
    timers = new Timers(app, () => now)
    timers.start()
    app.setProjectSettings(project.id, { routines: [routine({ time: '11:00' })] }, [])
    timers.check()
    now = at(2026, 11, 5, 10, 30)
    app.setProjectSettings(project.id, { routines: [routine({ time: '09:30' })] }, [])
    timers.check()
    expect(app.listNotices(project.coordinatorId)).toEqual([])

    // A restart long after a run was due: the next one comes on time.
    timers.stop()
    now = at(2026, 11, 6, 12, 0)
    timers = new Timers(app, () => now)
    timers.start()
    timers.check()
    expect(app.listNotices(project.coordinatorId)).toEqual([])
  })

  test('a run found late after the machine slept is skipped', () => {
    const project = app.createProject('P')
    let now = at(2026, 11, 5, 8, 0)
    timers = new Timers(app, () => now)
    timers.start()
    app.setProjectSettings(project.id, { routines: [routine()] }, [])
    timers.check()
    // Asleep across nine; the timer fires at noon.
    now = at(2026, 11, 5, 12, 0)
    timers.check()
    expect(app.listNotices(project.coordinatorId)).toEqual([])
    now = at(2026, 11, 6, 9, 2)
    timers.check()
    expect(app.listNotices(project.coordinatorId).map((n) => n.kind)).toEqual(['routine'])
  })

  test('settings refuse a bad routine', () => {
    const project = app.createProject('P')
    expect(() =>
      app.setProjectSettings(project.id, { routines: [routine({ time: '25:00' })] }, []),
    ).toThrow()
    expect(() =>
      app.setProjectSettings(project.id, { routines: [routine({ prompt: ' ' })] }, []),
    ).toThrow()
    expect(() =>
      app.setProjectSettings(project.id, { routines: [routine(), routine()] }, []),
    ).toThrow()
  })
})

describe('the fallback model', () => {
  const busy = {
    type: 'finish',
    reason: 'error',
    error: 'HTTP 529: overloaded',
    retryable: true,
    busy: true,
  } as const

  test('when the main model stays busy past its retries, the turn goes on with the fallback', async () => {
    const backup = {
      ...fakeModel(() => say('备用模型来答')),
      model: 'backup-1',
      label: 'other / backup',
    }
    const main = { ...fakeModel(() => [busy]), fallback: backup }
    const project = app.createProject('P')
    userSays(project.id, project.coordinatorId, 'hi')
    await startScheduler(main).idle()
    expect(app.listMessages(project.coordinatorId).at(-1)?.content).toBe('备用模型来答')
    expect(app.getConversation(project.coordinatorId).lastOutcome).toBe('completed')
    const notice = app.listNotices(project.coordinatorId).find((n) => n.kind === 'model-fallback')
    expect(notice).toMatchObject({ from: 'fake / fake-1', to: 'other / backup' })
    const items = buildTrace(app, project.coordinatorId).turns.flatMap((turn) => turn.items)
    expect(items.find((item) => item.kind === 'fallback')).toMatchObject({
      from: 'fake / fake-1',
      to: 'other / backup',
      detail: 'HTTP 529: overloaded',
    })
    // The step it took is recorded as the fallback's call.
    const step = items.find((item) => item.kind === 'request')
    expect(step?.kind === 'request' && step.model).toBe('backup-1')
  })

  test('an error another model would make too does not switch', async () => {
    const backup = fakeModel(() => say('不该来'))
    const main = {
      ...fakeModel(() => [{ type: 'finish', reason: 'error', error: 'HTTP 401: bad key' }]),
      fallback: backup,
    }
    const project = app.createProject('P')
    userSays(project.id, project.coordinatorId, 'hi')
    await startScheduler(main as ResolvedModel).idle()
    expect(backup.requests).toHaveLength(0)
    expect(app.getConversation(project.coordinatorId).lastOutcome).toBe('failed')
  })

  test('a busy fallback fails the turn', async () => {
    const backup = fakeModel(() => [busy])
    const main = { ...fakeModel(() => [busy]), fallback: backup }
    const project = app.createProject('P')
    userSays(project.id, project.coordinatorId, 'hi')
    await startScheduler(main).idle()
    expect(app.getConversation(project.coordinatorId).lastOutcome).toBe('failed')
    expect(backup.requests.length).toBeGreaterThan(0)
  })
})

const settled = () => new Promise((resolve) => setTimeout(resolve, 20))

describe('background commands for the status line', () => {
  test('the list changes as commands start and end, and peeking leaves the model its reads', async () => {
    const changed: string[] = []
    const background = new BackgroundCommands((threadId) => changed.push(threadId))
    let finish: ((code: number) => void) | undefined
    const started = background.start('thr_1', 'bun dev', async ({ onOutput }) => {
      onOutput?.('\x1b[32mready\x1b[0m\n', 'stdout')
      return new Promise((done) => {
        finish = (exitCode) => done({ exitCode, stdout: '', stderr: '' })
      })
    })
    expect(changed).toEqual(['thr_1'])
    expect(background.peek('thr_1', started.id)?.tail).toBe('ready\n')
    expect(background.peek('thr_2', started.id)).toBeNull()
    expect((await background.read('thr_1', started.id))?.text).toContain('ready')
    finish?.(0)
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(changed).toEqual(['thr_1', 'thr_1'])
    await background.stopThread('thr_1')
    expect(changed).toHaveLength(3)
  })

  test('a served address stays open while the work waits to be looked at', async () => {
    const project = app.createProject('P')
    const thread = app.createThread(project.id, { title: '改页面' })
    const background = new BackgroundCommands()
    const run = (output: string) =>
      background.start(
        thread.id,
        'bun dev',
        ({ signal, onOutput }) => {
          onOutput?.(output, 'stdout')
          return new Promise((done) => {
            signal?.addEventListener('abort', () => done({ exitCode: 143, stdout: '', stderr: '' }))
          })
        },
        { local: true },
      )
    scheduler = new Scheduler({
      app,
      resolveModel: () => null,
      locale: () => 'zh-CN',
      workspaces: { background, stopThread: async () => {} } as unknown as WorkspaceManager,
    })
    scheduler.start()
    app.startThread(thread.id)
    const server = run('Local: http://localhost:5173/\n')
    run('building\n')
    app.reportThread(thread.id, { kind: 'ready', summary: '改好了', headline: '看一下' })
    await settled()
    expect(background.list(thread.id)).toMatchObject([
      { id: server.id, url: 'http://localhost:5173/', result: null },
    ])
    // Back at work, the thread is told once what it left serving.
    expect(background.takeKept(thread.id)).toMatchObject([{ id: server.id }])
    expect(background.takeKept(thread.id)).toEqual([])
    expect(keptNote(background.list(thread.id))).toContain(
      `${server.id} at http://localhost:5173/: bun dev`,
    )
    // Reported again after more work: still there for the thread and the user.
    app.reportThread(thread.id, { kind: 'ready', summary: '又改了', headline: '再看一下' })
    await settled()
    expect(background.running(thread.id)).toBe(1)
    app.recordMerge(thread.id, {
      branch: 'ariadne/x',
      base: 'main',
      method: 'squash',
      sha: 'a',
      head: 'b',
    })
    await settled()
    expect(background.list(thread.id)).toEqual([])

    // The other ways out of waiting end it too.
    const other = app.createThread(project.id, { title: '写文档' })
    for (const leave of [
      () => app.reviewThread(other.id),
      () => app.reportThread(other.id, { kind: 'blocked', summary: '卡住了', headline: '卡住了' }),
    ]) {
      app.startThread(other.id)
      background.start(
        other.id,
        'bun preview',
        ({ signal, onOutput }) => {
          onOutput?.('http://127.0.0.1:4173/\n', 'stdout')
          return new Promise((done) => {
            signal?.addEventListener('abort', () => done({ exitCode: 143, stdout: '', stderr: '' }))
          })
        },
        { local: true },
      )
      app.reportThread(other.id, { kind: 'ready', summary: '写好了', headline: '看一下' })
      await settled()
      expect(background.running(other.id)).toBe(1)
      leave()
      await settled()
      expect(background.list(other.id)).toEqual([])
    }
  })
})
