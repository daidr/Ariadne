import { Ariadne, type LiveUpdate } from '@ariadne/core'
import type { BranchCapability, ContextProvider, Workspace } from '@ariadne/plugin-sdk'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { mentionRank, walkFiles } from './mention-files'
import { PluginHost } from './plugins'
import type { ResolvedModel } from './runner'
import { Scheduler } from './scheduler'
import { call, fakeModel, say } from './test-model'
import { turnChanges } from './turn-changes'
import { commentsText, mentionsText } from './transcript'
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

/** A worktree kept in memory whose snapshots are its files as JSON, so diffs can be checked. */
async function trackedHost(files: Record<string, string>) {
  const snapshots: string[] = []
  const workspace: Workspace = {
    id: 'w',
    label: '/work/thread',
    branch: 'ariadne/thread',
    base: 'main',
    readFile: async (path) => files[path] ?? '',
    readBytes: async (path) => new TextEncoder().encode(files[path] ?? ''),
    writeFile: async (path, content) => {
      files[path] = content
    },
    list: async () => [],
    async exec(command, options) {
      options?.onOutput?.('building…\n')
      // A command changes files behind the tools' back.
      files['out.txt'] = 'built\n'
      options?.onOutput?.('done\n')
      return { exitCode: 0, stdout: `ran: ${command}`, stderr: '' }
    },
    dispose: async () => {},
  }
  const branches: BranchCapability = {
    changes: async () => null,
    file: async () => ({ before: null, after: null, skipped: null }),
    merged: async () => false,
    remove: async () => true,
    worktrees: async () => [],
    async snapshot() {
      const tree = JSON.stringify(files)
      snapshots.push(tree)
      return Buffer.from(tree).toString('hex').slice(0, 64).padEnd(40, '0') + snapshots.length
    },
    async diff() {
      const [before, after] = snapshots.slice(-2).map((tree) => JSON.parse(tree))
      return Object.keys(after)
        .filter((path) => before[path] !== after[path])
        .map((path) => ({
          path,
          previousPath: null,
          status: before[path] === undefined ? ('added' as const) : ('modified' as const),
          added: 1,
          removed: before[path] === undefined ? 0 : 1,
        }))
    },
  }
  const host = new PluginHost('/tmp/ariadne-test-plugins')
  const provider: ContextProvider<never> = {
    kind: 'tracked',
    title: { 'zh-CN': '仓库' },
    settings: { type: 'object', properties: {} },
    provides: ['workspace'],
    async connect() {
      return { workspace: { isolated: true, open: async () => workspace, branches } }
    },
  }
  await host.load({
    id: 'tracked',
    name: { 'zh-CN': '仓库' },
    setup: (ctx) => ctx.registerContextProvider(provider),
  })
  return { host, snapshots }
}

function startScheduler(model: ResolvedModel, workspaces: WorkspaceManager) {
  scheduler = new Scheduler({ app, resolveModel: () => model, locale: () => 'zh-CN', workspaces })
  scheduler.start()
  return scheduler
}

describe('a thread turn', () => {
  test('shows its running command and records what it changed, however it changed it', async () => {
    const files: Record<string, string> = { 'a.ts': 'const x = 1\n' }
    const { host, snapshots } = await trackedHost(files)
    const workspaces = new WorkspaceManager(app, host)
    const project = app.createProject('P')
    app.setProjectSource(project.id, { kind: 'tracked', settings: {} })
    const thread = app.createThread(project.id, { title: '改代码' })
    const updates: LiveUpdate[] = []
    app.live.subscribe((update) => updates.push(update))
    const model = fakeModel((request, role) => {
      if (role === 'coordinator') return say('')
      if (request.messages.some((m) => m.role === 'tool')) return say('改好了')
      return [
        call('t1', 'write_file', { path: 'a.ts', content: 'const x = 2\n' }),
        call('t2', 'run_command', { command: 'bun run build' }),
        { type: 'finish', reason: 'tool-calls' },
      ]
    })
    app.postMessage(project.id, { conversationId: thread.id, author: 'user', content: '改成 2' })
    await startScheduler(model, workspaces).idle()

    // Once before the first tool touched the workspace, once as the turn ended.
    expect(snapshots).toHaveLength(2)
    const notice = app.listNotices(thread.id).find((n) => n.kind === 'turn-changes')
    expect(notice?.kind === 'turn-changes' && notice.files.map((f) => f.path).toSorted()).toEqual([
      'a.ts',
      'out.txt',
    ])
    expect(notice?.kind === 'turn-changes' && notice.total).toEqual({
      files: 2,
      added: 2,
      removed: 1,
    })
    // The command was shown while it ran, and gone once it ended; none of it is in the log.
    expect(updates[0]?.command).toMatchObject({ command: 'bun run build', cwd: '.' })
    expect(updates.at(-1)).toEqual({
      projectId: project.id,
      conversationId: thread.id,
      command: null,
    })
    expect(app.live.list()).toEqual([])
    const types = app.events({ afterSeq: 0, limit: 1000 }).map((event) => event.type)
    expect(types.filter((type) => type === 'turn.changed')).toHaveLength(1)
    expect(types.indexOf('turn.changed')).toBeLessThan(types.lastIndexOf('turn.finished'))
  })

  test('that only talks takes no snapshot and records nothing', async () => {
    const { host, snapshots } = await trackedHost({})
    const workspaces = new WorkspaceManager(app, host)
    const project = app.createProject('P')
    app.setProjectSource(project.id, { kind: 'tracked', settings: {} })
    const thread = app.createThread(project.id, { title: '问答' })
    const model = fakeModel(() => say('好的'))
    app.postMessage(project.id, { conversationId: thread.id, author: 'user', content: '在吗' })
    await startScheduler(model, workspaces).idle()
    expect(snapshots).toHaveLength(0)
    expect(app.listNotices(thread.id).some((n) => n.kind === 'turn-changes')).toBe(false)
  })

  test('reads the line comments and mentions the user sent', async () => {
    const { host } = await trackedHost({})
    const workspaces = new WorkspaceManager(app, host)
    const project = app.createProject('P')
    app.setProjectSource(project.id, { kind: 'tracked', settings: {} })
    const thread = app.createThread(project.id, { title: '改代码' })
    const other = app.createThread(project.id, { title: '调研' })
    const model = fakeModel(() => say('好'))
    app.postMessage(project.id, {
      conversationId: thread.id,
      author: 'user',
      content: '参考 @调研',
      mentions: [
        { kind: 'thread', id: other.id },
        { kind: 'file', path: 'src/a.ts' },
      ],
      comments: [{ path: 'src/a.ts', line: 3, quote: 'const `a` = 1', body: '改成 2\n再加注释' }],
    })
    await startScheduler(model, workspaces).idle()
    const request = model.requests.find((r) => r.messages[0]?.content.includes('You are a thread'))
    const user = request!.messages.at(-1)!.content
    expect(user).toContain('参考 @调研')
    expect(user).toContain("- src/a.ts:3 `const 'a' = 1`\n  改成 2\n  再加注释")
    expect(user).toContain(`- thread "调研" (${other.id})`)
    expect(user).toContain('- file src/a.ts')
  })
})

describe('a stopped turn', () => {
  test('waits for its changes only so long, and records none that come late', async () => {
    let calls = 0
    const branches = {
      snapshot: async () => {
        calls++
        // The second snapshot, at the turn's end, is slow.
        if (calls > 1) await new Promise((resolve) => setTimeout(resolve, 200))
        return `tree${calls}`
      },
      diff: async () => [
        { path: 'a', previousPath: null, status: 'modified' as const, added: 1, removed: 0 },
      ],
    }
    const workspaces = { branches: async () => branches } as unknown as WorkspaceManager
    const recorded: unknown[] = []
    const fakeApp = { recordTurnChanges: (...args: unknown[]) => recorded.push(args) } as never
    const changes = turnChanges(workspaces, 'p', 't')
    await changes.begin()
    const started = Date.now()
    await changes.record(fakeApp, 'turn', 20)
    expect(Date.now() - started).toBeLessThan(150)
    await new Promise((resolve) => setTimeout(resolve, 300))
    expect(recorded).toEqual([])
    // Given the time, it records them.
    await changes.record(fakeApp, 'turn')
    expect(recorded).toHaveLength(1)
  })
})

describe('what the model reads', () => {
  test('nothing for messages without comments or mentions', () => {
    expect(commentsText([])).toBe('')
    expect(mentionsText([])).toBe('')
  })
})

describe('files offered after @', () => {
  test('rank a name that starts with the query first, then names, then paths', () => {
    const files = ['src/app/main.ts', 'docs/apple.md', 'src/App.vue', 'lib/zapper.ts', 'x.ts']
    const ranked = files
      .flatMap((path) => {
        const rank = mentionRank(path, 'app')
        return rank === null ? [] : [{ path, rank }]
      })
      .toSorted((a, b) => a.rank - b.rank || a.path.length - b.path.length)
      .map((item) => item.path)
    expect(ranked).toEqual(['src/App.vue', 'docs/apple.md', 'lib/zapper.ts', 'src/app/main.ts'])
  })

  test('a walk skips installed things and stops at its bound', async () => {
    const tree: Record<string, { name: string; kind: 'file' | 'directory' }[]> = {
      '.': [
        { name: 'node_modules', kind: 'directory' },
        { name: 'src', kind: 'directory' },
        { name: 'a.ts', kind: 'file' },
      ],
      src: [
        { name: 'b.ts', kind: 'file' },
        { name: 'c.ts', kind: 'file' },
      ],
      node_modules: [{ name: 'dep.js', kind: 'file' }],
    }
    const view = { list: async (path: string) => tree[path] ?? [] }
    expect(await walkFiles(view)).toEqual(['a.ts', 'src/b.ts', 'src/c.ts'])
    expect(await walkFiles(view, 2)).toEqual(['a.ts', 'src/b.ts'])
  })
})
