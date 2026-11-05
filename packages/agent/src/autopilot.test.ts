import { Ariadne, parseProjectSettings, type MergeMethod } from '@ariadne/core'
import type {
  BranchCapability,
  MergeOutcome,
  PullRequestInfo,
  PullRequestMergeOutcome,
  RepoCapability,
} from '@ariadne/plugin-sdk'
import { MergeRefused } from '@ariadne/plugin-sdk'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { Autopilot, type AutopilotWorkspaces } from './autopilot'
import { describeNote } from './transcript'

let app: Ariadne
let projectId: string

beforeEach(() => {
  app = new Ariadne({ path: ':memory:' })
  projectId = app.createProject('托管').id
})

afterEach(() => app.close())

function autopilotOn(extra: Record<string, unknown> = {}) {
  app.setProjectSettings(projectId, { autopilot: { enabled: true, ...extra } }, [])
}

/** A branch whose newest commit is `head`, and merges as `outcome` says. */
function localBranches(state: { head: string; outcome: () => MergeOutcome }) {
  const merges: { method: MergeMethod; message: string }[] = []
  const branches = {
    changes: async () => ({
      branch: 'ariadne/a',
      base: 'main',
      from: 'f0',
      head: state.head,
      commits: [],
      files: [],
      uncommitted: [],
    }),
    merge: async (input: { method: MergeMethod; message: string }) => {
      merges.push({ method: input.method, message: input.message })
      return state.outcome()
    },
  } as unknown as BranchCapability
  const workspaces: AutopilotWorkspaces = {
    branches: async () => branches,
    hasRepo: () => false,
    repo: async () => {
      throw new Error('no repository')
    },
  }
  return { workspaces, merges }
}

/** A thread whose changes at `head` were reported ready and went to review round 1. */
function deliver(title: string, head: string, nodeId: string | null = null) {
  const thread = app.createThread(projectId, { title, nodeId })
  app.recordWorkspace(thread.id, { label: '/w', branch: 'ariadne/a', base: 'main' })
  app.reportThread(thread.id, { kind: 'ready', summary: '改好了', headline: '看一下' }, undefined, {
    reviewable: true,
    head,
    reviewerTitle: `审阅：${title}`,
  })
  return app.getThread(thread.id)
}

function pass(threadId: string) {
  const reviewerId = app.getThread(threadId).review!.reviewerId
  app.submitReview(reviewerId, { verdict: 'pass', summary: '没问题', comments: [], headline: '' })
}

function addNodes(
  nodes: { id: string; kind?: 'task' | 'milestone' | 'decision' }[],
  links: [string, string][],
) {
  app.revisePlan(projectId, {
    author: 'user',
    reason: '计划',
    ops: [
      ...nodes.map((node) => ({
        op: 'add' as const,
        id: node.id,
        kind: node.kind ?? 'task',
        title: node.id.toUpperCase(),
        description: '',
        acceptance: '',
      })),
      ...links.map(([from, to]) => ({ op: 'link' as const, from, to })),
    ],
  })
}

/** A thread on the node that reported done. */
function finish(nodeId: string) {
  const thread = app.createThread(projectId, { title: nodeId, nodeId })
  app.reportThread(thread.id, { kind: 'done', summary: '完成', headline: '完成' })
  return thread.id
}

/** A thread's turn that read its brief and reported done. */
function complete(threadId: string) {
  const turnId = app.startTurn(threadId, 'm')
  app.reportThread(threadId, { kind: 'done', summary: '完成', headline: '完成' })
  app.finishTurn(threadId, turnId, 'cancelled')
}

/** The coordinator's turn for whatever woke it, seen and over. */
function settle() {
  const coordinator = app.getProject(projectId).coordinatorId
  if (!app.getConversation(coordinator).pendingSeq) return
  app.finishTurn(coordinator, app.startTurn(coordinator, 'm'), 'cancelled')
}

const none: AutopilotWorkspaces = {
  branches: async () => null,
  hasRepo: () => false,
  repo: async () => {
    throw new Error('no repository')
  },
}
const pilot = (workspaces: AutopilotWorkspaces = none, noChecksWaitMs = 0) =>
  new Autopilot(app, workspaces, {
    locale: () => 'zh-CN',
    noChecksWaitMs,
    onError: (error) => {
      throw error
    },
  })

describe('settings', () => {
  test('off by default; on only with the review, squash and three threads unless set', () => {
    expect(app.getProject(projectId).settings.autopilot).toEqual({
      enabled: false,
      method: 'squash',
      threads: 3,
    })
    expect(() =>
      parseProjectSettings({ review: { enabled: false }, autopilot: { enabled: true } }, []),
    ).toThrow()
    expect(() => parseProjectSettings({ autopilot: { threads: 0 } }, [])).toThrow()
    expect(
      parseProjectSettings({ autopilot: { enabled: true, method: 'merge' } }, []).autopilot,
    ).toEqual({ enabled: true, method: 'merge', threads: 3 })
  })

  test('pausing needs it on; switching it on again lets it run', () => {
    expect(() => app.setAutopilotPaused(projectId, true)).toThrow()
    autopilotOn()
    expect(app.setAutopilotPaused(projectId, true).autopilotPaused).toBe(true)
    app.setProjectSettings(projectId, { autopilot: { enabled: false } }, [])
    autopilotOn()
    expect(app.getProject(projectId).autopilotPaused).toBe(false)
  })
})

describe('merging', () => {
  test('changes that passed the review are merged with the project method and title', async () => {
    autopilotOn({ method: 'merge' })
    const { workspaces, merges } = localBranches({
      head: 'h1',
      outcome: () => ({ kind: 'merged', sha: 'm1', head: 'h1' }),
    })
    const thread = deliver('修导出', 'h1')
    await pilot(workspaces).check(projectId)
    // Still under review: nothing to merge.
    expect(merges).toEqual([])
    pass(thread.id)
    await pilot(workspaces).check(projectId)
    expect(merges).toEqual([{ method: 'merge', message: '修导出' }])
    const after = app.getThread(thread.id)
    expect(after.stage).toBe('done')
    expect(after.merged).toMatchObject({ sha: 'm1', auto: true })
    expect(app.listNotices(thread.id).map((n) => n.kind)).toContain('merged')
    expect(app.listNeedsMe(projectId)).toEqual([])
  })

  test('paused, off, or comments left after the last round: the user merges', async () => {
    const { workspaces, merges } = localBranches({
      head: 'h1',
      outcome: () => ({ kind: 'merged', sha: 'm1', head: 'h1' }),
    })
    const thread = deliver('修导出', 'h1')
    pass(thread.id)
    await pilot(workspaces).check(projectId)
    autopilotOn()
    app.setAutopilotPaused(projectId, true)
    await pilot(workspaces).check(projectId)
    expect(merges).toEqual([])
    expect(app.getThread(thread.id).stage).toBe('review')

    // Two rounds that both asked for changes reach the user as they are.
    app.setAutopilotPaused(projectId, false)
    const other = deliver('改样式', 'h1')
    const reviewerId = app.getThread(other.id).review!.reviewerId
    const comment = { path: 'a.ts', line: 1, quote: 'x', body: '错了', kind: 'bug' as const }
    for (const round of [1, 2]) {
      app.submitReview(reviewerId, {
        verdict: 'changes',
        summary: `第 ${round} 轮`,
        comments: [comment],
        headline: '',
      })
      app.reportThread(other.id, { kind: 'ready', summary: '改了', headline: '看' }, undefined, {
        reviewable: true,
        head: 'h1',
        replies: [{ comment: 1, outcome: 'fixed', note: '' }],
      })
    }
    expect(app.passedReview(other.id)).toBeNull()
  })

  test('new commits after the review are left for the user, once', async () => {
    autopilotOn()
    const state = { head: 'h2', outcome: () => ({ kind: 'merged', sha: 'm', head: 'h2' }) as const }
    const { workspaces, merges } = localBranches(state)
    const thread = deliver('修导出', 'h1')
    pass(thread.id)
    await pilot(workspaces).check(projectId)
    await pilot(workspaces).check(projectId)
    expect(merges).toEqual([])
    const held = app.listNotices(thread.id).filter((n) => n.kind === 'autopilot-held')
    expect(held).toHaveLength(1)
    expect(held[0]).toMatchObject({ reason: 'moved' })
    expect(app.listNeedsMe(projectId).map((item) => item.kind)).toEqual(['thread'])
  })

  test('a conflict goes back to the thread; a refusal stays with the user', async () => {
    autopilotOn()
    let refuse = false
    const { workspaces } = localBranches({
      head: 'h1',
      outcome: (): MergeOutcome => {
        if (refuse) throw new MergeRefused('checkout', 'a.ts')
        return { kind: 'conflict', files: ['a.ts'] }
      },
    })
    const thread = deliver('修导出', 'h1')
    pass(thread.id)
    await pilot(workspaces).check(projectId)
    expect(app.listNotices(thread.id).map((n) => n.kind)).toContain('merge-conflict')
    expect(app.getConversation(thread.id).pendingSeq).toBeGreaterThan(0)

    const other = deliver('改样式', 'h1')
    pass(other.id)
    refuse = true
    await pilot(workspaces).check(projectId)
    expect(app.listNotices(other.id).find((n) => n.kind === 'autopilot-held')).toMatchObject({
      reason: 'refused',
    })
    expect(app.getThread(other.id).stage).toBe('review')
  })

  test('a workspace that cannot merge leaves the thread to the user with a word why', async () => {
    autopilotOn()
    const thread = deliver('修导出', 'h1')
    pass(thread.id)
    await pilot(none).check(projectId)
    await pilot(none).check(projectId)
    const held = app.listNotices(thread.id).filter((n) => n.kind === 'autopilot-held')
    expect(held).toHaveLength(1)
    expect(held[0]).toMatchObject({
      reason: 'refused',
      detail: '这个工作区不能合并分支，请手动合并',
    })
  })
})

/** A hosted repository with pull request 7 at h1, whose checks are as given. */
function hosted(checks: { status: 'completed' | 'in_progress'; conclusion: string | null }[]) {
  const pr: PullRequestInfo = {
    number: 7,
    url: 'https://example.com/pr/7',
    title: '修导出',
    state: 'open',
    draft: false,
    headSha: 'h1',
    base: 'main',
  }
  const calls: { number: number; method: MergeMethod; sha: string }[] = []
  let outcome: PullRequestMergeOutcome = { kind: 'merged', sha: 'm7' }
  const repo = {
    remote: 'o/r',
    webUrl: 'https://example.com/o/r',
    defaultBranch: async () => 'main',
    pullRequest: async () => pr,
    pullRequestFor: async () => pr,
    checks: async () =>
      checks.map((check, i) => ({ id: String(i), name: `ci${i}`, url: null, ...check })),
    async mergePullRequest(number: number, input: { method: MergeMethod; sha: string }) {
      calls.push({ number, ...input })
      if (outcome.kind === 'merged') pr.state = 'merged'
      return outcome
    },
  } as unknown as RepoCapability
  const workspaces: AutopilotWorkspaces = {
    branches: async () => null,
    hasRepo: () => true,
    repo: async () => repo,
  }
  return {
    workspaces,
    calls,
    checks,
    setOutcome: (next: PullRequestMergeOutcome) => (outcome = next),
  }
}

describe('pull requests', () => {
  test('one with no checks is given a while for them to show up', async () => {
    autopilotOn()
    const host = hosted([])
    const thread = deliver('修导出', 'h1')
    pass(thread.id)
    const waiting = pilot(host.workspaces, 30)
    await waiting.check(projectId)
    expect(host.calls).toEqual([])
    // Checks that showed up meanwhile are waited for as usual.
    host.checks.push({ status: 'in_progress', conclusion: null })
    await new Promise((resolve) => setTimeout(resolve, 40))
    await waiting.check(projectId)
    expect(host.calls).toEqual([])
    // None came after all: then nothing stands in the way.
    host.checks.pop()
    await waiting.check(projectId)
    expect(host.calls).toEqual([{ number: 7, method: 'squash', sha: 'h1' }])
    await waiting.stop()
  })

  test('merged once its checks are green, and the coordinator hears the autopilot did it', async () => {
    autopilotOn()
    const host = hosted([{ status: 'in_progress', conclusion: null }])
    const thread = deliver('修导出', 'h1')
    pass(thread.id)
    await pilot(host.workspaces).check(projectId)
    expect(host.calls).toEqual([])
    host.checks[0] = { status: 'completed', conclusion: 'success' }
    await pilot(host.workspaces).check(projectId)
    expect(host.calls).toEqual([{ number: 7, method: 'squash', sha: 'h1' }])
    expect(app.getThread(thread.id).stage).toBe('done')
    expect(app.listNotices(thread.id).find((n) => n.kind === 'merged')).toMatchObject({
      auto: true,
      pullRequest: 7,
      base: 'main',
    })
    const coordinator = app.getProject(projectId).coordinatorId
    const note = app
      .listTranscript(coordinator)
      .findLast((entry) => entry.kind === 'note' && entry.note.type === 'pull-request-closed')
    expect(note?.kind === 'note' && describeNote(note.note, () => '')).toMatch(/autopilot/)
  })

  test('a conflicting pull request goes back to the thread to bring the base in', async () => {
    autopilotOn()
    const host = hosted([])
    host.setOutcome({ kind: 'conflict' })
    const thread = deliver('修导出', 'h1')
    pass(thread.id)
    await pilot(host.workspaces).check(projectId)
    const note = app
      .listTranscript(thread.id)
      .findLast((entry) => entry.kind === 'note' && entry.note.type === 'merge-conflict')
    expect(note?.kind === 'note' && describeNote(note.note, () => '')).toMatch(
      /sync_branch.*origin\/main.*push_branch/s,
    )
    expect(app.listNotices(thread.id).find((n) => n.kind === 'merge-conflict')).toMatchObject({
      pullRequest: 7,
    })
  })
})

describe('starting nodes', () => {
  test('nodes whose dependencies are done get threads, up to the cap', async () => {
    autopilotOn({ threads: 2 })
    addNodes([{ id: 'a' }, { id: 'b' }, { id: 'c' }, { id: 'd' }], [['a', 'd']])
    await pilot().check(projectId)
    const opened = app.listThreads(projectId)
    // a, b and c have nothing to wait for; the cap stops at two.
    expect(opened.map((thread) => thread.nodeId)).toEqual(['a', 'b'])
    expect(opened.every((thread) => thread.auto)).toBe(true)
    expect(app.listMessages(opened[0]!.id)[0]?.content).toContain('A')
    const coordinator = app.getProject(projectId).coordinatorId
    expect(
      app
        .listTranscript(coordinator)
        .some((entry) => entry.kind === 'note' && entry.note.type === 'thread-auto-started'),
    ).toBe(true)

    // Once a is done, c and then d may go.
    complete(opened[0]!.id)
    await pilot().check(projectId)
    // The coordinator, woken by the report, goes first.
    expect(app.listThreads(projectId)).toHaveLength(2)
    settle()
    await pilot().check(projectId)
    expect(app.listThreads(projectId).map((thread) => thread.nodeId)).toEqual(['a', 'b', 'c'])
    complete(opened[1]!.id)
    settle()
    await pilot().check(projectId)
    expect(app.listThreads(projectId).map((thread) => thread.nodeId)).toEqual(['a', 'b', 'c', 'd'])
  })

  test('nothing starts while the coordinator is about to work, or when paused', async () => {
    addNodes([{ id: 'a' }], [])
    await pilot().check(projectId)
    expect(app.listThreads(projectId)).toEqual([])
    autopilotOn()
    const coordinator = app.getProject(projectId).coordinatorId
    app.postMessage(projectId, { conversationId: coordinator, author: 'user', content: '开始吧' })
    await pilot().check(projectId)
    expect(app.listThreads(projectId)).toEqual([])
    settle()
    app.setAutopilotPaused(projectId, true)
    await pilot().check(projectId)
    expect(app.listThreads(projectId)).toEqual([])
    app.setAutopilotPaused(projectId, false)
    await pilot().check(projectId)
    expect(app.listThreads(projectId).map((thread) => thread.nodeId)).toEqual(['a'])
  })
})

describe('milestones', () => {
  test('the work stops at a milestone until the user agrees to go on', async () => {
    autopilotOn()
    addNodes(
      [{ id: 'a' }, { id: 'm', kind: 'milestone' }, { id: 'b' }],
      [
        ['a', 'm'],
        ['m', 'b'],
      ],
    )
    finish('a')
    settle()
    await pilot().check(projectId)
    const asked = app.listNeedsMe(projectId)
    expect(asked).toHaveLength(1)
    const card = asked[0]!
    expect(card.kind === 'attention' && card.attention.block).toMatchObject({
      type: 'confirm',
      title: expect.stringContaining('M'),
    })
    expect(app.getPlan(projectId).nodes.find((n) => n.id === 'm')).toMatchObject({
      gate: 'open',
      status: 'waiting',
    })
    expect(app.listThreads(projectId).map((thread) => thread.nodeId)).toEqual(['a'])

    // Asked once, however often it looks.
    await pilot().check(projectId)
    expect(app.listNeedsMe(projectId)).toHaveLength(1)

    if (card.kind !== 'attention') throw new Error('not a card')
    app.answerAttention(card.attention.id, { type: 'confirm', approved: true })
    // The coordinator hears the answer and runs first.
    const coordinator = app.getProject(projectId).coordinatorId
    expect(app.getConversation(coordinator).pendingSeq).toBeGreaterThan(0)
    settle()
    await pilot().check(projectId)
    expect(app.getPlan(projectId).nodes.find((n) => n.id === 'm')).toMatchObject({
      gate: 'passed',
      status: 'done',
    })
    expect(app.listThreads(projectId).map((thread) => thread.nodeId)).toEqual(['a', 'b'])
  })

  test('switched on midway, milestones the work went past are not asked about', async () => {
    addNodes(
      [{ id: 'a' }, { id: 'm', kind: 'milestone' }, { id: 'b' }, { id: 'c' }],
      [
        ['a', 'm'],
        ['m', 'b'],
        ['m', 'c'],
      ],
    )
    finish('a')
    finish('b')
    settle()
    autopilotOn()
    await pilot().check(projectId)
    expect(app.listNeedsMe(projectId)).toEqual([])
    // What else came after it goes on.
    expect(app.listThreads(projectId).map((thread) => thread.nodeId)).toEqual(['a', 'b', 'c'])
  })

  test('a no holds until the autopilot goes on again', async () => {
    autopilotOn()
    addNodes([{ id: 'm', kind: 'milestone' }, { id: 'b' }], [['m', 'b']])
    await pilot().check(projectId)
    const card = app.listNeedsMe(projectId)[0]!
    if (card.kind !== 'attention') throw new Error('not a card')
    app.answerAttention(card.attention.id, { type: 'confirm', approved: false })
    settle()
    await pilot().check(projectId)
    expect(app.listNeedsMe(projectId)).toEqual([])
    expect(app.listThreads(projectId)).toEqual([])
    app.setAutopilotPaused(projectId, true)
    app.setAutopilotPaused(projectId, false)
    await new Promise((resolve) => setTimeout(resolve, 2))
    await pilot().check(projectId)
    expect(app.listNeedsMe(projectId)).toHaveLength(1)
  })

  test('a milestone removed from the plan takes its question back', async () => {
    autopilotOn()
    addNodes([{ id: 'm', kind: 'milestone' }], [])
    await pilot().check(projectId)
    expect(app.listNeedsMe(projectId)).toHaveLength(1)
    app.revisePlan(projectId, {
      author: 'user',
      reason: '不要了',
      ops: [{ op: 'remove', id: 'm' }],
    })
    await pilot().check(projectId)
    expect(app.listNeedsMe(projectId)).toEqual([])
  })
})
