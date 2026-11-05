import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { Ariadne } from './ariadne'
import { DomainError } from './errors'
import type { ReviewComment, ReviewReply } from './events'
import type { TranscriptNote } from './views'

const STEP_INPUT = { system: 'system', tools: '[]', call: '{}', images: true }
const TIMING = { startedAt: 0, firstOutputAt: null }

const comment = (line: number, body = '空数组会崩'): ReviewComment => ({
  path: 'src/a.ts',
  line,
  quote: `line ${line}`,
  body,
  kind: 'bug',
})

describe('pre-delivery review', () => {
  let app: Ariadne
  let projectId: string
  let coordinatorId: string
  let authorId: string

  beforeEach(() => {
    app = new Ariadne({ path: ':memory:' })
    const project = app.createProject('P')
    projectId = project.id
    coordinatorId = project.coordinatorId
    authorId = app.createThread(projectId, { title: '修导出', model: 'm1' }).id
    app.startThread(authorId)
    app.recordWorkspace(authorId, { label: 'local', branch: 'ariadne/x', base: 'main' })
  })
  afterEach(() => app.close())

  const ready = (replies?: ReviewReply[]) =>
    app.reportThread(
      authorId,
      { kind: 'ready', summary: '改好了导出', headline: '导出修好了' },
      undefined,
      {
        reviewable: true,
        head: 'h1',
        reviewerTitle: '审阅：修导出',
        ...(replies ? { replies } : {}),
      },
    )
  const notes = (conversationId: string): TranscriptNote[] =>
    app
      .listTranscript(conversationId)
      .flatMap((entry) => (entry.kind === 'note' ? [entry.note] : []))
  const reviewerOf = () => app.getThread(authorId).review!.reviewerId
  const verdict = (kind: 'pass' | 'changes', comments: ReviewComment[] = []) =>
    app.submitReview(reviewerOf(), {
      verdict: kind,
      summary: kind === 'pass' ? '没问题' : '有问题',
      comments,
      headline: kind === 'pass' ? '审过了' : `${comments.length} 个问题`,
    })
  /** The reviewer's turn ends without a verdict. */
  const idleReviewer = () => handled(reviewerOf())
  /** A turn of the conversation reads what waits for it, does `act`, and ends. */
  const inTurn = (conversationId: string, act: () => void) => {
    const turnId = app.startTurn(conversationId, 'm1')
    act()
    app.recordStep(conversationId, turnId, {
      reasoning: [],
      text: '',
      toolCalls: [],
      sawSeq: app.lastSeq(),
      usage: null,
      input: STEP_INPUT,
      timing: TIMING,
    })
    app.finishTurn(conversationId, turnId, 'completed')
  }
  /** A turn of the conversation reads what waits for it, and ends. */
  const handled = (conversationId: string) => {
    const turnId = app.startTurn(conversationId, 'm1')
    app.recordStep(conversationId, turnId, {
      reasoning: [],
      text: '',
      toolCalls: [],
      sawSeq: app.lastSeq(),
      usage: null,
      input: STEP_INPUT,
      timing: TIMING,
    })
    app.finishTurn(conversationId, turnId, 'completed')
  }

  test('a ready goes to a new reviewing thread first, not to the coordinator', () => {
    const author = ready()
    expect(author.stage).toBe('working')
    expect(author.review).toMatchObject({ rounds: 1, reviewing: true })
    const reviewer = app.getThread(author.review!.reviewerId)
    expect(reviewer).toMatchObject({ reviewOf: authorId, title: '审阅：修导出', model: 'm1' })
    expect(reviewer.nodeId).toBeNull()
    expect(notes(coordinatorId).filter((n) => n.type === 'report')).toEqual([])
    expect(notes(reviewer.id)).toEqual([
      {
        type: 'review-requested',
        threadId: authorId,
        round: 1,
        summary: '改好了导出',
        head: 'h1',
        previous: null,
      },
    ])
    expect(app.getConversation(reviewer.id).pendingSeq).toBeGreaterThan(0)
    expect(app.listNeedsMe(projectId)).toEqual([])
  })

  test('a pass hands the changes to the user and tells the coordinator', () => {
    ready()
    inTurn(reviewerOf(), () => verdict('pass'))
    const author = app.getThread(authorId)
    expect(author.stage).toBe('review')
    expect(author.review).toMatchObject({ rounds: 1, reviewing: false })
    expect(notes(coordinatorId).at(-1)).toMatchObject({
      type: 'report',
      threadId: authorId,
      report: 'ready',
      headline: '导出修好了',
      reviewRounds: 1,
    })
    const reviewer = app.getThread(reviewerOf())
    expect(reviewer.stage).toBe('done')
    expect(reviewer.lastHeadline).toBe('审过了')
    expect(
      app.listNeedsMe(projectId).map((item) => item.kind === 'thread' && item.thread.id),
    ).toEqual([authorId])
    expect(app.listNotices(authorId).map((n) => n.kind === 'review' && n.state)).toEqual([
      'started',
      'pass',
    ])
  })

  test('comments go back to the author, who answers each before the next round', () => {
    ready()
    verdict('changes', [comment(3), comment(9, '没处理权限')])
    expect(notes(authorId).at(-1)).toMatchObject({
      type: 'review-comments',
      round: 1,
      last: false,
      comments: [{ line: 3 }, { line: 9 }],
    })
    expect(app.openReviewRound(authorId)?.round).toBe(1)
    // A ready that leaves comments unanswered is refused.
    expect(() => ready()).toThrow(DomainError)
    expect(() => ready([{ comment: 1, outcome: 'fixed', note: '' }])).toThrow(DomainError)
    expect(() =>
      ready([
        { comment: 1, outcome: 'fixed', note: '' },
        { comment: 2, outcome: 'declined', note: '' },
      ]),
    ).toThrow(DomainError)
    ready([
      { comment: 2, outcome: 'declined', note: '权限在上一层查过了' },
      { comment: 1, outcome: 'fixed', note: '' },
    ])
    const rounds = app.listReviews(authorId)
    expect(rounds.map((r) => [r.round, r.verdict])).toEqual([
      [1, 'changes'],
      [2, null],
    ])
    expect(rounds[0]!.replies).toEqual([
      { comment: 1, outcome: 'fixed', note: '' },
      { comment: 2, outcome: 'declined', note: '权限在上一层查过了' },
    ])
    expect(notes(reviewerOf()).at(-1)).toMatchObject({
      type: 'review-requested',
      round: 2,
      previous: { comments: [{ line: 3 }, { line: 9 }], replies: [{ comment: 1 }, { comment: 2 }] },
    })
    // The same reviewing thread takes the next round.
    expect(app.listThreads(projectId).filter((t) => t.reviewOf)).toHaveLength(1)
  })

  test('after the last round the next ready goes to the user with its rounds', () => {
    ready()
    verdict('changes', [comment(3)])
    ready([{ comment: 1, outcome: 'fixed', note: '' }])
    verdict('changes', [comment(5)])
    expect(notes(authorId).at(-1)).toMatchObject({ type: 'review-comments', round: 2, last: true })
    handled(authorId)
    const author = ready([{ comment: 1, outcome: 'fixed', note: '加了判断' }])
    expect(author.stage).toBe('review')
    expect(author.review).toMatchObject({ rounds: 2, reviewing: false })
    expect(app.listReviews(authorId)).toHaveLength(2)
    expect(app.listReviews(authorId)[1]!.replies).toEqual([
      { comment: 1, outcome: 'fixed', note: '加了判断' },
    ])
    expect(notes(coordinatorId).at(-1)).toMatchObject({ type: 'report', reviewRounds: 2 })
    // The user sends it back; the next delivery is reviewed from the first round again.
    app.postMessage(projectId, { conversationId: authorId, author: 'user', content: '还要改' })
    ready()
    expect(app.getThread(authorId).review).toMatchObject({ rounds: 1, reviewing: true })
  })

  test('a reviewer that ends its turn without a verdict lets the round go, as a pass', () => {
    ready()
    idleReviewer()
    const author = app.getThread(authorId)
    expect(author.stage).toBe('review')
    expect(author.review?.reviewing).toBe(false)
    expect(app.listReviews(authorId).at(-1)).toMatchObject({ verdict: null, abandoned: true })
    // The coordinator hears of the changes, as after a pass.
    expect(notes(coordinatorId).filter((n) => n.type === 'report')).toEqual([
      expect.objectContaining({
        threadId: authorId,
        report: 'ready',
        reviewRounds: 1,
        reviewAbandoned: true,
      }),
    ])
    expect(app.listNotices(authorId).map((n) => n.kind === 'review' && n.state)).toEqual([
      'started',
      'abandoned',
    ])
    // The reviewer waits for the next round; a late verdict is refused.
    expect(app.getThread(reviewerOf()).stage).toBe('idle')
    expect(() => verdict('pass')).toThrow(DomainError)
    // The user is not asked about the reviewer itself.
    expect(
      app.listNeedsMe(projectId).map((item) => (item.kind === 'thread' ? item.thread.id : null)),
    ).toEqual([authorId])
    // The next delivery is reviewed again, from the first round.
    app.postMessage(projectId, { conversationId: authorId, author: 'user', content: '还要改' })
    expect(ready().review).toMatchObject({ rounds: 1, reviewing: true })
  })

  test('a reviewer whose turn fails or cannot start lets the round go', () => {
    ready()
    const turnId = app.startTurn(reviewerOf(), 'm1')
    app.finishTurn(reviewerOf(), turnId, 'failed', { key: 'error.noModel' })
    expect(app.getThread(authorId).stage).toBe('review')
    app.postMessage(projectId, { conversationId: authorId, author: 'user', content: '再改改' })
    handled(authorId)
    ready()
    app.failBeforeTurn(reviewerOf(), null)
    expect(app.getThread(authorId).stage).toBe('review')
    expect(notes(coordinatorId).filter((n) => n.type === 'report')).toHaveLength(2)
  })

  test('an interrupted reviewer keeps the round; a stopped one lets it go and reviews no more', () => {
    ready()
    const turnId = app.startTurn(reviewerOf(), 'm1')
    app.finishTurn(reviewerOf(), turnId, 'interrupted')
    expect(app.getThread(authorId).review?.reviewing).toBe(true)
    app.stopThread(reviewerOf(), { reason: '不用审了', by: 'user' })
    expect(app.getThread(authorId).stage).toBe('review')
    expect(notes(coordinatorId).filter((n) => n.type === 'report')).toHaveLength(1)
    // While it stays stopped, deliveries go straight to the user.
    app.postMessage(projectId, { conversationId: authorId, author: 'user', content: '还要改' })
    handled(authorId)
    const author = ready()
    expect(author.stage).toBe('review')
    expect(author.review?.reviewing).toBe(false)
    expect(app.listReviews(authorId)).toHaveLength(1)
  })

  test('work that ends without changes leaves its comments unanswered', () => {
    ready()
    verdict('changes', [comment(3)])
    app.reportThread(authorId, { kind: 'done', summary: '不改了', headline: '不改了' })
    expect(app.openReviewRound(authorId)).toBeNull()
    // Later changes start a delivery of their own, without the old comments.
    app.postMessage(projectId, { conversationId: authorId, author: 'user', content: '换个做法' })
    expect(ready().review).toMatchObject({ rounds: 1, reviewing: true })
  })

  test('without review, or without changes to review, a ready goes straight to the user', () => {
    app.setProjectSettings(projectId, { review: { enabled: false } }, [])
    expect(ready().stage).toBe('review')
    expect(app.getThread(authorId).review).toBeNull()
    expect(notes(coordinatorId).at(-1)).toMatchObject({ type: 'report', report: 'ready' })
    app.setProjectSettings(projectId, { review: { enabled: true } }, [])
    const plain = app.reportThread(authorId, { kind: 'ready', summary: 's', headline: 'h' })
    expect(plain.review).toBeNull()
  })

  test('only the reviewer of the round under way gives a verdict, once', () => {
    ready()
    expect(() =>
      app.submitReview(authorId, { verdict: 'pass', summary: 's', comments: [], headline: 'h' }),
    ).toThrow(DomainError)
    expect(() => verdict('changes')).toThrow(DomainError)
    expect(() => verdict('pass', [comment(1)])).toThrow(DomainError)
    verdict('pass')
    expect(() => verdict('pass')).toThrow(DomainError)
  })

  test('the settings default to review on', () => {
    expect(app.getProject(projectId).settings.review).toEqual({ enabled: true })
    expect(() => app.setProjectSettings(projectId, { review: { enabled: 'yes' } }, [])).toThrow(
      DomainError,
    )
  })
})
