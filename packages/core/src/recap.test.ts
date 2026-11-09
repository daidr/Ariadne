import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { Ariadne, AWAY_MS } from './ariadne'
import { confirmBlock } from './blocks'

let app: Ariadne
let clock = 1_000

beforeEach(() => {
  clock = 1_000
  app = new Ariadne({ path: ':memory:', now: () => ++clock })
})

afterEach(() => app.close())

const recaps = (conversationId: string) =>
  app.listNotices(conversationId).flatMap((notice) => (notice.kind === 'recap' ? [notice] : []))

const merge = {
  branch: 'ariadne/a',
  base: 'main',
  method: 'squash',
  sha: 'abc',
  head: 'def',
} as const

describe('coming back to a project', () => {
  test('tells what finished, was merged and came to wait on the user meanwhile', () => {
    const project = app.createProject('P')
    const old = app.createThread(project.id, { title: '早就做完的' })
    app.startThread(old.id)
    app.reportThread(old.id, { kind: 'done', summary: '完成', headline: '完成' })
    const stale = app.createThread(project.id, { title: '走之前就在等的' })
    app.startThread(stale.id)
    app.reportThread(stale.id, { kind: 'blocked', summary: '缺权限', headline: '缺权限' })
    const left = clock

    clock += AWAY_MS
    const done = app.createThread(project.id, { title: '做完的' })
    const merged = app.createThread(project.id, { title: '合并的' })
    const ready = app.createThread(project.id, { title: '待看的' })
    const asking = app.createThread(project.id, { title: '提问的' })
    const busy = app.createThread(project.id, { title: '还在做的' })
    for (const thread of [done, merged, ready, asking, busy]) app.startThread(thread.id)
    app.reportThread(done.id, { kind: 'done', summary: '完成', headline: '完成' })
    app.reportThread(merged.id, { kind: 'ready', summary: '可以看了', headline: '可以看了' })
    app.recordMerge(merged.id, merge)
    app.reportThread(ready.id, { kind: 'ready', summary: '可以看了', headline: '可以看了' })
    app.openAttention(project.id, {
      threadId: asking.id,
      block: confirmBlock({ title: '删掉旧表吗？' }),
    })
    const gate = app.openAttention(project.id, { block: confirmBlock({ title: '进入下一阶段？' }) })

    const transcript = app.listTranscript(project.coordinatorId).length
    const pending = app.getConversation(project.coordinatorId).pendingSeq
    app.noteReturn(project.id, clock - left)
    const [recap] = recaps(project.coordinatorId)
    expect(recap).toMatchObject({
      done: [done.id],
      merged: [merged.id],
      waiting: [asking.id, ready.id],
      asks: [gate.id],
    })
    expect(recap!.since).toBeGreaterThanOrEqual(left)
    // The model hears nothing of it.
    expect(app.listTranscript(project.coordinatorId)).toHaveLength(transcript)
    expect(app.getConversation(project.coordinatorId).pendingSeq).toBe(pending)
  })

  test('says nothing after a short absence, or when nothing came of the work', () => {
    const project = app.createProject('P')
    const thread = app.createThread(project.id, { title: 't' })
    app.startThread(thread.id)
    app.reportThread(thread.id, { kind: 'done', summary: '完成', headline: '完成' })
    app.noteReturn(project.id, AWAY_MS - 1)
    expect(recaps(project.coordinatorId)).toHaveLength(0)

    const quiet = app.createProject('Q')
    const working = app.createThread(quiet.id, { title: 't' })
    app.startThread(working.id)
    app.reportThread(working.id, { kind: 'progress', summary: '在做', headline: '在做' })
    clock += AWAY_MS
    app.noteReturn(quiet.id, AWAY_MS)
    expect(recaps(quiet.coordinatorId)).toHaveLength(0)
    expect(app.events({ projectId: quiet.id }).some((e) => e.type === 'user.returned')).toBe(false)
  })

  test('keeps the latest recap only, and does not tell the same twice', () => {
    const project = app.createProject('P')
    const first = app.createThread(project.id, { title: '第一个' })
    app.startThread(first.id)
    clock += AWAY_MS
    app.reportThread(first.id, { kind: 'done', summary: '完成', headline: '完成' })
    app.noteReturn(project.id, AWAY_MS)
    expect(recaps(project.coordinatorId)[0]).toMatchObject({ done: [first.id] })

    // Another tab reports the same absence a moment later.
    app.noteReturn(project.id, AWAY_MS)
    expect(
      app.events({ projectId: project.id }).filter((e) => e.type === 'user.returned'),
    ).toHaveLength(1)

    const second = app.createThread(project.id, { title: '第二个' })
    app.startThread(second.id)
    clock += AWAY_MS
    app.reportThread(second.id, { kind: 'done', summary: '完成', headline: '完成' })
    // However long the browser says, what the last recap told stays told.
    app.noteReturn(project.id, 10 * AWAY_MS)
    const shown = recaps(project.coordinatorId)
    expect(shown).toHaveLength(1)
    expect(shown[0]).toMatchObject({ done: [second.id], merged: [], waiting: [], asks: [] })
  })

  test('leaves out a reviewing thread and reads the same after a rebuild', () => {
    const project = app.createProject('P')
    const author = app.createThread(project.id, { title: '作者' })
    app.startThread(author.id)
    app.recordWorkspace(author.id, { label: 'local', branch: 'ariadne/x', base: 'main' })
    clock += AWAY_MS
    const { review } = app.reportThread(
      author.id,
      { kind: 'ready', summary: '改好了', headline: '改好了' },
      undefined,
      { reviewable: true, head: 'h1', reviewerTitle: '审阅：作者' },
    )
    app.submitReview(review!.reviewerId, {
      verdict: 'pass',
      summary: '没问题',
      comments: [],
      headline: '审过了',
    })
    app.noteReturn(project.id, AWAY_MS)
    const before = recaps(project.coordinatorId)
    expect(before[0]).toMatchObject({ done: [], merged: [], waiting: [author.id], asks: [] })
    app.rebuildProjections()
    expect(recaps(project.coordinatorId)).toEqual(before)
  })
})
