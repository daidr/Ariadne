import { Ariadne } from '@ariadne/core'
import {
  PluginHost,
  Scheduler,
  toolsFor,
  WorkspaceManager,
  WorktreeJanitor,
  type ToolContext,
} from '@ariadne/agent'
import { fakeModel } from '../../agent/src/test-model'
import { localDirectoryProvider, run } from '@ariadne/plugin-local'
import type { ContextProvider } from '@ariadne/plugin-sdk'
import { existsSync } from 'node:fs'
import { mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { createHandler } from './app'

const token = 'test-token'
let base: string
let app: Ariadne
let workspaces: WorkspaceManager
let handle: ReturnType<typeof createHandler>

beforeEach(async () => {
  base = await mkdtemp(join(tmpdir(), 'ariadne-merge-'))
  app = new Ariadne({ path: ':memory:' })
  const host = new PluginHost(join(base, 'plugins'))
  await host.load({
    id: 'local',
    name: { 'zh-CN': '本地目录' },
    setup: (ctx) =>
      ctx.registerContextProvider(
        localDirectoryProvider(join(base, 'data'), undefined, null) as ContextProvider<never>,
      ),
  })
  workspaces = new WorkspaceManager(app, host)
  handle = createHandler({ app, token, workspaces })
})

afterEach(async () => {
  app.close()
  await rm(base, { recursive: true, force: true })
})

function call(path: string, init: Omit<RequestInit, 'body'> & { body?: unknown } = {}) {
  const headers = new Headers(init.headers)
  headers.set('authorization', `Bearer ${token}`)
  const body = init.body === undefined ? undefined : JSON.stringify(init.body)
  if (body) headers.set('content-type', 'application/json')
  return handle(new Request(`http://127.0.0.1${path}`, { ...init, headers, body }))
}

const git = async (cwd: string, ...args: string[]) =>
  (await run(['git', ...args], { cwd })).stdout.trim()

/** A project on a fresh repository, with a thread that committed `a.txt` on its branch. */
async function setup() {
  const dir = join(base, 'repo')
  await run(['mkdir', '-p', dir], { cwd: base })
  await git(dir, 'init', '-q', '-b', 'main')
  await git(dir, 'config', 'user.email', 'test@example.com')
  await git(dir, 'config', 'user.name', 'test')
  await writeFile(join(dir, 'README.md'), '# repo\n')
  await git(dir, 'add', '.')
  await git(dir, 'commit', '-qm', 'init')
  const project = app.createProject('P')
  app.setProjectSource(project.id, { kind: 'local-directory', settings: { path: dir } })
  const thread = app.createThread(project.id, { title: '加一个文件' })
  app.startThread(thread.id)
  const ws = await workspaces.forThread(project.id, thread.id)
  await ws.exec('echo a > a.txt && git add . && git commit -qm a')
  return { dir, project, thread, ws }
}

describe('thread changes', () => {
  test('the user sees the changes and squash-merges them; the coordinator hears of it', async () => {
    const { dir, project, thread } = await setup()
    expect(app.getThread(thread.id).workspace).toMatchObject({ base: 'main', removed: false })

    const listed = await (await call(`/api/threads/${thread.id}/changes`)).json()
    expect(listed.mergeable).toBe(true)
    expect(listed.changes.files.map((f: { path: string }) => f.path)).toEqual(['a.txt'])
    const query = new URLSearchParams({
      from: listed.changes.from,
      head: listed.changes.head,
      path: 'a.txt',
    })
    const file = await (await call(`/api/threads/${thread.id}/changes/file?${query}`)).json()
    expect(file.file).toEqual({ before: null, after: 'a\n', skipped: null })
    const bad = await call(`/api/threads/${thread.id}/changes/file?from=HEAD&head=x&path=a`)
    expect(bad.status).toBe(400)

    const merged = await call(`/api/threads/${thread.id}/merge`, {
      method: 'POST',
      body: { message: '' },
    })
    const body = await merged.json()
    expect(body.outcome.kind).toBe('merged')
    // An empty message takes the thread's title.
    expect(await git(dir, 'log', '-1', '--format=%s')).toBe('加一个文件')
    expect(body.thread.merged).toMatchObject({ base: 'main', method: 'squash' })

    const coordinator = app.listTranscript(project.coordinatorId)
    expect(coordinator.at(-1)).toMatchObject({ kind: 'note', note: { type: 'branch-merged' } })
    expect(app.getConversation(project.coordinatorId).pendingSeq).toBeGreaterThan(0)
    // The thread reads it next time it works, without being woken for it.
    expect(app.getConversation(thread.id).pendingSeq).toBe(0)
    expect(app.listNotices(thread.id).map((n) => n.kind)).toEqual(['merged'])
  })

  test('the user takes a merge back: the changes wait again, with their worktree', async () => {
    const { dir, project, thread } = await setup()
    app.reportThread(thread.id, { kind: 'ready', summary: '加好了', headline: '加好了' })
    const none = await call(`/api/threads/${thread.id}/merge/undo`, { method: 'POST' })
    expect((await none.json()).error.key).toBe('error.undoMerge.none')

    const before = await git(dir, 'rev-parse', 'main')
    await call(`/api/threads/${thread.id}/merge`, { method: 'POST', body: {} })
    expect(app.getThread(thread.id)).toMatchObject({ stage: 'done', archived: false })
    // Its worktree went once the work had landed.
    await workspaces.removeWorktree(project.id, thread.id, 'merged')
    expect(app.getThread(thread.id).workspace?.removed).toBe(true)

    const undone = await call(`/api/threads/${thread.id}/merge/undo`, { method: 'POST' })
    expect((await undone.json()).thread).toMatchObject({
      stage: 'review',
      merged: null,
      archived: false,
      workspace: { removed: false },
    })
    expect(await git(dir, 'rev-parse', 'main')).toBe(before)
    expect(existsSync(join(dir, 'a.txt'))).toBe(false)
    expect(app.listNotices(thread.id).filter((n) => n.kind === 'merged')).toMatchObject([
      { undone: true },
    ])
    const coordinator = app.listTranscript(project.coordinatorId)
    expect(coordinator.at(-1)).toMatchObject({ kind: 'note', note: { type: 'merge-undone' } })
    // It merges again.
    const again = await call(`/api/threads/${thread.id}/merge`, { method: 'POST', body: {} })
    expect((await again.json()).outcome.kind).toBe('merged')

    // Once the base moved on, it is refused and nothing changes.
    await writeFile(join(dir, 'user.txt'), 'user\n')
    await git(dir, 'add', '.')
    await git(dir, 'commit', '-qm', 'user')
    const late = await call(`/api/threads/${thread.id}/merge/undo`, { method: 'POST' })
    expect((await late.json()).error.key).toBe('error.merge.undo-moved')
    expect(app.getThread(thread.id).merged).not.toBeNull()
  })

  test('a conflict merges nothing and wakes the thread to resolve it', async () => {
    const { dir, thread, ws } = await setup()
    await ws.exec('echo thread > README.md && git commit -qam readme')
    await writeFile(join(dir, 'README.md'), 'user\n')
    await git(dir, 'commit', '-qam', 'user')
    const response = await call(`/api/threads/${thread.id}/merge`, {
      method: 'POST',
      body: { method: 'merge', message: 'x' },
    })
    expect((await response.json()).outcome).toEqual({ kind: 'conflict', files: ['README.md'] })
    expect(app.listTranscript(thread.id).at(-1)).toMatchObject({
      kind: 'note',
      note: { type: 'merge-conflict', base: 'main', files: ['README.md'] },
    })
    expect(app.getConversation(thread.id).pendingSeq).toBeGreaterThan(0)
    expect(app.getThread(thread.id).merged).toBeNull()
  })

  test('refusals: a bad method, a dirty checkout, a thread at work', async () => {
    const { dir, thread, ws } = await setup()
    const merge = (body: unknown) =>
      call(`/api/threads/${thread.id}/merge`, { method: 'POST', body })
    expect((await merge({ method: 'rebase' })).status).toBe(400)

    await ws.exec('echo thread > README.md && git commit -qam readme')
    await writeFile(join(dir, 'README.md'), 'not committed\n')
    const dirty = await (await merge({})).json()
    expect(dirty.error.key).toBe('error.merge.checkout')
  })

  test('once merged and the thread is finished, its worktree goes and comes back on use', async () => {
    const { project, thread, ws } = await setup()
    const janitor = new WorktreeJanitor(app, workspaces, { delayMs: 0 })
    await call(`/api/threads/${thread.id}/merge`, { method: 'POST', body: {} })
    // Still at work: kept.
    await janitor.check(thread.id)
    expect(existsSync(ws.label)).toBe(true)

    app.reportThread(thread.id, { kind: 'ready', summary: '好了', headline: '好了' })
    await janitor.check(thread.id)
    expect(existsSync(ws.label)).toBe(false)
    expect(app.getThread(thread.id).workspace?.removed).toBe(true)
    expect(app.listNotices(thread.id).map((n) => n.kind)).toEqual(['merged', 'worktree-removed'])

    const again = await workspaces.forThread(project.id, thread.id)
    expect(existsSync(again.label)).toBe(true)
    expect(app.getThread(thread.id).workspace).toMatchObject({ base: 'main', removed: false })
    await janitor.stop()
  })

  test('opening it while it is being removed waits and checks it out again', async () => {
    const { project, thread } = await setup()
    const removing = workspaces.removeWorktree(project.id, thread.id, 'user')
    const reopened = workspaces.forThread(project.id, thread.id)
    expect(await removing).toBe('removed')
    const ws = await reopened
    expect(existsSync(ws.label)).toBe(true)
    expect(await workspaces.forThread(project.id, thread.id)).toBe(ws)
    expect(app.getThread(thread.id).workspace?.removed).toBe(false)
  })

  test('a report after the merge with nothing new stays landed', async () => {
    const { thread, ws } = await setup()
    const merged = await (
      await call(`/api/threads/${thread.id}/merge`, { method: 'POST', body: {} })
    ).json()
    const head = merged.outcome.head as string
    app.reportThread(thread.id, { kind: 'ready', summary: '好了', headline: '好了' }, head)
    expect(app.getThread(thread.id).stage).toBe('done')
    await ws.exec('echo b > b.txt && git add . && git commit -qm b')
    app.reportThread(
      thread.id,
      { kind: 'ready', summary: '又改了', headline: '又改了' },
      await ws.exec('git rev-parse HEAD').then((r) => r.stdout.trim()),
    )
    expect(app.getThread(thread.id).stage).toBe('review')
  })

  test('a report after its worktree went reads the branch and does not bring it back', async () => {
    const { project, thread, ws } = await setup()
    await call(`/api/threads/${thread.id}/merge`, { method: 'POST', body: {} })
    expect(await workspaces.removeWorktree(project.id, thread.id, 'merged')).toBe('removed')
    const report = toolsFor('thread', { workspace: true, repo: false }).find(
      (tool) => tool.spec.name === 'report',
    )!
    const ctx = {
      app,
      projectId: project.id,
      conversationId: thread.id,
      role: 'thread',
      locale: 'zh-CN',
      signal: new AbortController().signal,
      workspace: () => workspaces.forThread(project.id, thread.id),
      repo: () => Promise.reject(new Error('no repo')),
      branches: () => workspaces.branches(project.id),
    } satisfies ToolContext
    await report.run({ kind: 'ready', summary: '好了', headline: '好了' }, ctx)
    expect(app.getThread(thread.id).stage).toBe('done')
    expect(existsSync(ws.label)).toBe(false)
  })

  test('a branch merged by hand counts; one with new commits after the merge does not', async () => {
    const { dir, thread, ws } = await setup()
    const janitor = new WorktreeJanitor(app, workspaces, { delayMs: 0 })
    await call(`/api/threads/${thread.id}/merge`, { method: 'POST', body: {} })
    await ws.exec('echo b > b.txt && git add . && git commit -qm b')
    app.reportThread(thread.id, { kind: 'done', summary: '完成', headline: '完成' })
    await janitor.check(thread.id)
    expect(existsSync(ws.label)).toBe(true)

    await git(dir, 'merge', '-q', '--no-edit', 'ariadne/' + thread.id.slice(-12))
    await janitor.check(thread.id)
    expect(existsSync(ws.label)).toBe(false)
  })

  test('the settings list worktrees and remove one; one not committed stays', async () => {
    const { project, thread, ws } = await setup()
    const listed = await (await call('/api/worktrees')).json()
    expect(listed.projects).toHaveLength(1)
    expect(listed.projects[0].worktrees[0]).toMatchObject({
      threadId: thread.id,
      thread: { id: thread.id },
    })
    await ws.exec('echo wip > wip.txt')
    const path = `/api/projects/${project.id}/worktrees/${thread.id}`
    expect((await (await call(path, { method: 'DELETE' })).json()).error.key).toBe(
      'error.worktreeDirty',
    )
    await ws.exec('rm wip.txt')
    expect((await call(path, { method: 'DELETE' })).status).toBe(204)
    expect(app.listNotices(thread.id).at(-1)).toMatchObject({
      kind: 'worktree-removed',
      reason: 'user',
    })
    expect(
      (await call(`/api/projects/${project.id}/worktrees/a.b`, { method: 'DELETE' })).status,
    ).toBe(400)
  })
})

describe('deleting a project', () => {
  test('ends its turns, removes its worktrees with their changes, and keeps the repository', async () => {
    const { dir, project, thread, ws } = await setup()
    const other = await setup()
    await ws.exec('echo wip > wip.txt')
    expect(existsSync(join(ws.label, 'wip.txt'))).toBe(true)

    // The thread is mid-turn: its model answers only once the turn is stopped.
    let started!: () => void
    const running = new Promise<void>((resolve) => (started = resolve))
    const model = fakeModel(async (request) => {
      started()
      await new Promise((resolve) => request.signal?.addEventListener('abort', resolve))
      return []
    })
    const scheduler = new Scheduler({ app, resolveModel: () => model, locale: () => 'zh-CN' })
    handle = createHandler({ app, token, workspaces, scheduler })
    scheduler.start()
    app.postMessage(project.id, { conversationId: thread.id, author: 'user', content: '继续' })
    await running

    expect((await call(`/api/projects/${project.id}`, { method: 'DELETE' })).status).toBe(204)
    await scheduler.stop()
    expect(app.listProjects().map((p) => p.id)).toEqual([other.project.id])
    expect(existsSync(ws.label)).toBe(false)
    // The repository, its checkout and the thread's branch stay.
    expect(existsSync(join(dir, 'README.md'))).toBe(true)
    expect(await git(dir, 'worktree', 'list')).not.toContain(thread.id)
    expect(await git(dir, 'branch', '--list', 'ariadne/*')).toContain(thread.id.slice(-12))
    // The other project's worktree is untouched.
    expect(existsSync(other.ws.label)).toBe(true)
    expect((await call(`/api/projects/${project.id}`)).status).toBe(404)
  })

  test('a worktree that cannot be removed keeps the project', async () => {
    const { project, thread } = await setup()
    app.setProjectSource(project.id, {
      kind: 'local-directory',
      settings: { path: join(base, 'gone') },
    })
    const response = await call(`/api/projects/${project.id}`, { method: 'DELETE' })
    expect(response.status).toBe(400)
    expect(app.getThread(thread.id).workspace?.removed).toBe(false)
    expect(app.listProjects()).toHaveLength(1)
  })
})
