import { Ariadne } from '@ariadne/core'
import type {
  CheckInfo,
  ContextProvider,
  PullRequestInfo,
  RepoCapability,
  Workspace,
} from '@ariadne/plugin-sdk'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { PluginHost } from './plugins'
import { summarizeChecks, syncThreadPullRequest } from './repo-tools'
import { RepoWatcher } from './repo-watcher'
import { Scheduler } from './scheduler'
import { call, fakeModel, reminded, say } from './test-model'
import { WorkspaceManager } from './workspaces'

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

/** A repository in memory: pushes, one pull request per branch, and settable checks. */
function memoryRepo() {
  const state = {
    pushed: [] as string[],
    pulls: new Map<string, PullRequestInfo>(),
    checks: new Map<string, CheckInfo[]>(),
    head: 'sha1',
    public: false,
    fetches: 0,
  }
  const repo: RepoCapability = {
    remote: 'acme/app',
    webUrl: 'https://github.com/acme/app',
    defaultBranch: async () => 'main',
    isPublic: async () => state.public,
    async fetch() {
      state.fetches++
    },
    async push(branch) {
      state.pushed.push(branch)
      return { sha: state.head }
    },
    pullRequestFor: async (branch) => state.pulls.get(branch) ?? null,
    async pullRequest(number) {
      const pr = [...state.pulls.values()].find((p) => p.number === number)
      if (!pr) throw new Error('no such pull request')
      return { ...pr, headSha: state.head }
    },
    async openPullRequest(input) {
      const pr: PullRequestInfo = {
        number: state.pulls.size + 1,
        url: `https://github.com/acme/app/pull/${state.pulls.size + 1}`,
        title: input.title,
        state: 'open',
        draft: !!input.draft,
        headSha: state.head,
        base: 'main',
      }
      state.pulls.set(input.branch, pr)
      return pr
    },
    checks: async (sha) => state.checks.get(sha) ?? [],
    checkLog: async (id) => `log of ${id}: expected 1 to be 2`,
  }
  return { state, repo }
}

const workspace: Workspace = {
  id: 'w',
  label: '/work/thread',
  branch: 'ariadne/thread',
  base: 'main',
  readFile: async () => '',
  readBytes: async () => new Uint8Array(),
  writeFile: async () => {},
  list: async () => [],
  exec: async () => ({ exitCode: 0, stdout: '', stderr: '' }),
  dispose: async () => {},
}

async function repoHost(repo: RepoCapability, secrets: unknown[] = []) {
  const host = new PluginHost('/tmp/ariadne-test-plugins')
  const provider: ContextProvider<never> = {
    kind: 'memory-repo',
    title: { 'zh-CN': '内存仓库' },
    settings: { type: 'object', properties: {} },
    provides: ['workspace', 'repo'],
    async connect(settings) {
      secrets.push(settings)
      return { workspace: { isolated: true, open: async () => workspace }, repo }
    },
  }
  await host.load({
    id: 'memory-repo',
    name: { 'zh-CN': '内存仓库' },
    setup: (ctx) => ctx.registerContextProvider(provider),
  })
  return host
}

const check = (name: string, conclusion: string | null): CheckInfo => ({
  id: name,
  name,
  status: conclusion === null ? 'in_progress' : 'completed',
  conclusion,
  url: null,
})

describe('checks', () => {
  test('sum up as one state', () => {
    expect(summarizeChecks([])).toEqual({ state: 'none', failed: [] })
    expect(summarizeChecks([check('a', 'success'), check('b', null)]).state).toBe('pending')
    expect(summarizeChecks([check('a', 'failure'), check('b', null)])).toEqual({
      state: 'failure',
      failed: ['a'],
    })
    expect(summarizeChecks([check('a', 'success'), check('b', 'skipped')]).state).toBe('success')
    // A job allowed to fail (GitLab) does not fail the rest.
    expect(summarizeChecks([check('a', 'success'), check('b', 'neutral')]).state).toBe('success')
  })

  test('keep how the host refers to a pull request, e.g. a GitLab merge request', async () => {
    const { repo } = memoryRepo()
    const project = app.createProject('P')
    const thread = app.createThread(project.id, { title: '实现' })
    await syncThreadPullRequest(app, repo, thread.id, {
      number: 4,
      reference: '!4',
      url: 'https://gitlab.com/acme/app/-/merge_requests/4',
      title: 't',
      state: 'open',
      draft: false,
      headSha: 'sha1',
      base: 'main',
    })
    expect(app.getThread(thread.id).pullRequest).toMatchObject({ number: 4, reference: '!4' })
  })
})

describe('repository', () => {
  test('a thread opens a pull request; a failed check wakes it, a merge tells the coordinator', async () => {
    const { state, repo } = memoryRepo()
    const seen: unknown[] = []
    const workspaces = new WorkspaceManager(app, await repoHost(repo, seen), () => ({
      token: 'tok',
    }))
    const project = app.createProject('P')
    app.setProjectSource(project.id, { kind: 'memory-repo', settings: { repository: 'acme/app' } })
    const thread = app.createThread(project.id, { title: '实现' })

    let fixed = false
    const model = fakeModel((request, role) => {
      if (role === 'coordinator') return say('')
      const last = request.messages.at(-1)
      const toolResults = request.messages.filter((m) => m.role === 'tool').length
      if (last?.role === 'user' && last.content.includes('<checks_failed')) {
        fixed = true
        return [
          call('f1', 'read_check_log', { checkId: 'test' }),
          call('f2', 'push_branch', {}),
          { type: 'finish', reason: 'tool-calls' },
        ]
      }
      if (toolResults === 0) {
        return [
          call('t1', 'open_pull_request', { title: '加功能', body: '说明' }),
          { type: 'finish', reason: 'tool-calls' },
        ]
      }
      return say('')
    })
    app.postMessage(project.id, {
      conversationId: thread.id,
      kind: 'relay',
      author: 'coordinator',
      content: '做完开 PR',
    })
    scheduler = new Scheduler({
      app,
      resolveModel: () => model,
      locale: () => 'zh-CN',
      workspaces,
    })
    scheduler.start()
    await scheduler.idle()

    expect(seen[0]).toEqual({ repository: 'acme/app', token: 'tok' })
    expect(state.pushed).toEqual(['ariadne/thread'])
    expect(app.getThread(thread.id).pullRequest).toMatchObject({
      number: 1,
      state: 'open',
      checks: { state: 'none' },
    })
    const threadRequest = model.requests.find((r) =>
      String(r.messages[0]?.content).includes('You are a thread'),
    )
    expect(threadRequest?.tools.map((t) => t.name)).toEqual(
      expect.arrayContaining(['open_pull_request', 'push_branch', 'repo_status', 'read_check_log']),
    )

    // CI runs and fails: the watcher records it and wakes the thread once.
    const watcher = new RepoWatcher(app, workspaces, 60_000, (error) => {
      throw error
    })
    state.checks.set('sha1', [check('test', 'failure'), check('lint', 'success')])
    await watcher.poll()
    await watcher.poll()
    expect(app.getThread(thread.id).pullRequest?.checks).toEqual({
      state: 'failure',
      failed: ['test'],
    })
    expect(
      app.events({ projectId: project.id }).filter((e) => e.type === 'checks.failed'),
    ).toHaveLength(1)
    await scheduler.idle()
    expect(fixed).toBe(true)
    expect(state.pushed).toEqual(['ariadne/thread', 'ariadne/thread'])

    // The pull request is merged: the coordinator hears of it, polling stops.
    state.pulls.set('ariadne/thread', { ...state.pulls.get('ariadne/thread')!, state: 'merged' })
    await watcher.poll()
    expect(app.listTranscript(project.coordinatorId).at(-1)).toMatchObject({
      kind: 'note',
      note: { type: 'pull-request-closed', number: 1, merged: true },
    })
    expect(app.getThread(thread.id).pullRequest?.state).toBe('merged')
  })

  test('a public repository asks the user before the first push; a closed PR is not reopened', async () => {
    const { state, repo } = memoryRepo()
    state.public = true
    const workspaces = new WorkspaceManager(app, await repoHost(repo))
    const project = app.createProject('P')
    app.setProjectSource(project.id, { kind: 'memory-repo', settings: {} })
    const thread = app.createThread(project.id, { title: '实现' })

    const results: string[] = []
    const model = fakeModel((request, role) => {
      if (role === 'coordinator' || reminded(request)) return say('')
      const last = request.messages.at(-1)
      if (last?.role === 'tool') {
        results.push(last.content)
        return say('')
      }
      return [
        call(`c${results.length}`, 'open_pull_request', { title: '加功能', body: '说明' }),
        { type: 'finish', reason: 'tool-calls' },
      ]
    })
    app.postMessage(project.id, {
      conversationId: thread.id,
      kind: 'relay',
      author: 'coordinator',
      content: '开 PR',
    })
    scheduler = new Scheduler({ app, resolveModel: () => model, locale: () => 'zh-CN', workspaces })
    scheduler.start()
    await scheduler.idle()

    expect(state.pushed).toEqual([])
    expect(results[0]).toContain('public')
    const [item] = app.listNeedsMe(project.id)
    if (item?.kind !== 'attention') throw new Error('expected a question')
    expect(item.attention.block).toMatchObject({ type: 'confirm', tone: 'warning' })
    expect(item.attention.block.type === 'confirm' && item.attention.block.title).toContain(
      'acme/app',
    )

    // The user agrees: the thread is woken and its next call pushes.
    app.answerAttention(item.attention.id, { type: 'confirm', approved: true })
    await scheduler.idle()
    expect(state.pushed).toEqual(['ariadne/thread'])
    expect(app.getThread(thread.id).pullRequest?.number).toBe(1)

    // The user closes the pull request: asking again does not open a new one.
    state.pulls.set('ariadne/thread', { ...state.pulls.get('ariadne/thread')!, state: 'closed' })
    app.postMessage(project.id, {
      conversationId: thread.id,
      kind: 'relay',
      author: 'coordinator',
      content: '再开一次',
    })
    await scheduler.idle()
    expect(results.at(-1)).toContain('closed without merging')
    expect(state.pulls.size).toBe(1)
  })

  test('threads asking to push to a public repository at once share one card', async () => {
    const { state, repo } = memoryRepo()
    state.public = true
    const workspaces = new WorkspaceManager(app, await repoHost(repo))
    const project = app.createProject('P')
    app.setProjectSource(project.id, { kind: 'memory-repo', settings: {} })
    const threads = [
      app.createThread(project.id, { title: '甲' }),
      app.createThread(project.id, { title: '乙' }),
    ]
    const results: string[] = []
    const model = fakeModel((request, role) => {
      if (role === 'coordinator' || reminded(request)) return say('')
      const last = request.messages.at(-1)
      if (last?.role === 'tool') {
        results.push(last.content)
        return say('')
      }
      return [
        call(`c${results.length}`, 'push_branch', {}),
        { type: 'finish', reason: 'tool-calls' },
      ]
    })
    for (const thread of threads) {
      app.postMessage(project.id, {
        conversationId: thread.id,
        kind: 'relay',
        author: 'coordinator',
        content: '推送',
      })
    }
    scheduler = new Scheduler({ app, resolveModel: () => model, locale: () => 'zh-CN', workspaces })
    scheduler.start()
    await scheduler.idle()

    expect(state.pushed).toEqual([])
    const cards = app.listOpenAttentions(project.id)
    expect(cards).toHaveLength(1)
    expect(results.some((r) => r.includes('already being asked'))).toBe(true)

    // One yes wakes both threads, and both push.
    app.answerAttention(cards[0]!.id, { type: 'confirm', approved: true })
    await scheduler.idle()
    expect(state.pushed).toHaveLength(2)
    expect(app.listOpenAttentions(project.id)).toEqual([])
  })

  test('without a repository, threads get no repository tools', async () => {
    const model = fakeModel(() => say(''))
    const project = app.createProject('P')
    const thread = app.createThread(project.id, { title: 'x' })
    app.postMessage(project.id, {
      conversationId: thread.id,
      kind: 'relay',
      author: 'coordinator',
      content: 'hi',
    })
    scheduler = new Scheduler({ app, resolveModel: () => model, locale: () => 'zh-CN' })
    scheduler.start()
    await scheduler.idle()
    const names = model.requests.flatMap((r) => r.tools.map((t) => t.name))
    expect(names).not.toContain('open_pull_request')
  })
})
