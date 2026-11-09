import { Ariadne, MAX_REFERENCES, referenceName } from '@ariadne/core'
import type { ContextProvider, ModelRequest, Workspace } from '@ariadne/plugin-sdk'
import { FileTooLargeError, WorkspacePathError } from '@ariadne/plugin-sdk'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { PluginHost } from './plugins'
import type { ResolvedModel } from './runner'
import { Scheduler } from './scheduler'
import { call, fakeModel, say } from './test-model'
import { localRefusal, remoteRefusal } from './reference-guard'
import { describeNote } from './transcript'
import { referenceSecrets, WorkspaceManager } from './workspaces'

let app: Ariadne
let scheduler: Scheduler | null = null
/** What each connect was handed, to see which secrets a reference connects with. */
let connected: Record<string, unknown>[] = []

beforeEach(() => {
  app = new Ariadne({ path: ':memory:' })
  connected = []
})

afterEach(async () => {
  await scheduler?.stop()
  scheduler = null
  app.close()
})

type Files = Record<string, string>

const at = (path: string) => {
  if (path.startsWith('..') || path.startsWith('/')) throw new WorkspacePathError(path)
  return path.replace(/^\.\//, '')
}

function memoryFiles(files: Files, written: string[]): Workspace {
  return {
    id: 'w',
    label: '/work/thread',
    branch: null,
    base: null,
    async readFile(path) {
      const content = files[at(path)]
      if (content === undefined) throw new Error(`ENOENT: ${path}`)
      return content
    },
    async readBytes(path, maxBytes) {
      const content = files[at(path)]
      if (content === undefined) throw new Error(`ENOENT: ${path}`)
      const bytes = new TextEncoder().encode(content)
      if (bytes.byteLength > maxBytes) throw new FileTooLargeError(path, bytes.byteLength, maxBytes)
      return bytes
    },
    async writeFile(path, content) {
      written.push(at(path))
      files[at(path)] = content
    },
    async list(path) {
      const dir = at(path === '.' ? '' : path)
      const names = new Map<string, 'file' | 'directory'>()
      for (const file of Object.keys(files)) {
        if (dir && !file.startsWith(`${dir}/`)) continue
        const [first, ...rest] = file.slice(dir ? dir.length + 1 : 0).split('/')
        names.set(first!, rest.length ? 'directory' : 'file')
      }
      if (dir && !names.size) throw new Error(`ENOENT: ${path}`)
      return [...names].map(([name, kind]) => ({ name, kind }))
    },
    async exec() {
      return { exitCode: 127, stdout: '', stderr: 'no programs here' }
    },
    async dispose() {},
  }
}

/**
 * Directories kept in memory, by their path: a source of kind `disk` reads the one its
 * `path` names, and fails to connect when there is none. `token` is its secret.
 */
async function diskHost(disks: Record<string, Files>, written: string[] = []) {
  const host = new PluginHost('/tmp/ariadne-test-plugins')
  const provider: ContextProvider<never> = {
    kind: 'disk',
    title: { 'zh-CN': '本地目录', en: 'Local directory' },
    settings: {
      type: 'object',
      properties: {
        path: { type: 'string', title: { 'zh-CN': '路径' }, directory: true },
        token: { type: 'string', title: { 'zh-CN': '令牌' }, secret: true },
      },
      required: ['path'],
    },
    provides: ['workspace'],
    async connect(settings: Record<string, unknown>) {
      connected.push(settings)
      const files = disks[String(settings.path)]
      if (!files) throw new Error(`no such directory: ${String(settings.path)}`)
      return {
        workspace: {
          isolated: true,
          open: async () => memoryFiles(files, written),
          view: async () => {
            const { readBytes, list } = memoryFiles(files, written)
            return { readBytes, list }
          },
        },
      }
    },
  }
  await host.load({
    id: 'disk',
    name: { 'zh-CN': '本地目录' },
    setup: (ctx) => ctx.registerContextProvider(provider),
  })
  return host
}

function startScheduler(model: ResolvedModel, workspaces: WorkspaceManager) {
  scheduler = new Scheduler({ app, resolveModel: () => model, locale: () => 'zh-CN', workspaces })
  scheduler.start()
  return scheduler
}

const system = (request: ModelRequest) => request.messages[0]?.content ?? ''
const toolResults = (conversationId: string) =>
  app
    .listTranscript(conversationId)
    .flatMap((e) => (e.kind === 'tool' ? [[e.name, !!e.isError, e.content] as const] : []))

const notes = (conversationId: string) =>
  app.listTranscript(conversationId).flatMap((e) => (e.kind === 'note' ? [e.note] : []))

const LIB = {
  'README.md': '# lib\n组件库\n',
  'src/button.ts': 'export const Button = 1\n',
  'src/input.ts': 'export const Input = 2\n',
}

const more = (n: number | string) => ({ kind: 'disk', settings: { path: `/more/${n}` } })

describe('reference names', () => {
  test('come from where the source is, and stay apart', () => {
    expect(referenceName('daidr/dou-ui', [])).toBe('dou-ui')
    expect(referenceName('D:\\work\\story verse\\', [])).toBe('story-verse')
    expect(referenceName('box:/srv/app.git', [])).toBe('app')
    expect(referenceName('dou-ui', ['dou-ui', 'dou-ui-2'])).toBe('dou-ui-3')
    expect(referenceName('///', [])).toBe('reference')
  })

  test('a project keeps its references in order, up to the most it takes', () => {
    const project = app.createProject('P')
    const first = app.addProjectReference(
      project.id,
      { kind: 'disk', settings: { path: '/lib' } },
      '/lib',
    )
    app.addProjectReference(project.id, { kind: 'disk', settings: { path: '/x/lib' } }, '/x/lib')
    expect(app.getProject(project.id).references.map((ref) => ref.name)).toEqual(['lib', 'lib-2'])
    // The same source is not added twice.
    expect(() =>
      app.addProjectReference(project.id, { kind: 'disk', settings: { path: '/lib' } }, '/lib'),
    ).toThrow('error.referenceExists')
    app.removeProjectReference(project.id, first.id)
    expect(app.getProject(project.id).references.map((ref) => ref.name)).toEqual(['lib-2'])
    for (let n = 1; n < MAX_REFERENCES; n++) {
      app.addProjectReference(project.id, more(n), `/more/${n}`)
    }
    expect(() => app.addProjectReference(project.id, more('x'), '/one/too/many')).toThrow(
      'error.tooManyReferences',
    )
    // The project's own source is untouched by any of it.
    expect(app.getProject(project.id).source).toBeNull()
  })
})

describe('reference secrets', () => {
  const source = { kind: 'forge', settings: {}, hosting: { kind: 'host', settings: {} } }
  const secrets = {
    token: 'main',
    'hosting.token': 'hosting',
    'reference.ref_1.token': 'own',
    'reference.ref_2.other': 'x',
  }

  test('its own win, else the main source of the same kind lends its', () => {
    const none = new Map<string, ContextProvider<never>>()
    const of = (id: string | undefined, kind: string) =>
      referenceSecrets(none, source, { ...(id ? { id } : {}), kind, settings: {} }, secrets)
    expect(of('ref_1', 'forge')).toEqual({ token: 'own' })
    expect(of('ref_9', 'forge')).toEqual({ token: 'main' })
    expect(of('ref_9', 'host')).toEqual({ token: 'hosting' })
    // Another kind gets none of them.
    expect(of('ref_9', 'disk')).toEqual({})
    expect(referenceSecrets(none, null, { kind: 'forge', settings: {} }, secrets)).toEqual({})
  })

  test('a token is lent only to the place it was given for', () => {
    // A kind whose settings choose the host the token is sent to.
    const contexts = new Map<string, ContextProvider<never>>([
      [
        'forge',
        {
          secretsFor: (settings: { instance?: string; repository?: string }) => {
            if (!settings.repository) throw new Error('no repository')
            return settings.instance ?? 'https://forge.example'
          },
        } as unknown as ContextProvider<never>,
      ],
    ])
    const main = {
      kind: 'forge',
      settings: { repository: 'me/app', instance: 'https://corp.example' },
    }
    const lent = (settings: Record<string, unknown>, id?: string) =>
      referenceSecrets(contexts, main, { ...(id ? { id } : {}), kind: 'forge', settings }, secrets)
    expect(lent({ repository: 'me/lib', instance: 'https://corp.example' })).toEqual({
      token: 'main',
    })
    expect(lent({ repository: 'x/y', instance: 'https://evil.example' })).toEqual({})
    expect(lent({ repository: 'x/y' })).toEqual({})
    // Settings that cannot be read borrow nothing.
    expect(lent({ instance: 'https://corp.example' })).toEqual({})
    // Its own token still goes with it, wherever it points.
    expect(lent({ repository: 'x/y', instance: 'https://other.example' }, 'ref_1')).toEqual({
      token: 'own',
    })
  })

  test("a reference of the main source's kind connects with the main source's token", async () => {
    const workspaces = new WorkspaceManager(app, await diskHost({ '/lib': LIB }), () => ({
      token: 'main-token',
    }))
    const project = app.createProject('P')
    app.setProjectSource(project.id, { kind: 'disk', settings: { path: '/main' } })
    app.addProjectReference(project.id, { kind: 'disk', settings: { path: '/lib' } }, '/lib')
    await (await workspaces.referenceView(project.id, 'lib')).list('.')
    expect(connected).toEqual([{ path: '/lib', token: 'main-token' }])
    // Connected once for as long as nothing about it changes.
    await workspaces.referenceView(project.id, 'lib')
    expect(connected).toHaveLength(1)
  })
})

describe('reading a reference source', () => {
  test('a thread reads it with the looking tools and cannot write to it', async () => {
    const written: string[] = []
    const main = { 'app.ts': 'import { Button } from "lib"\n' }
    const workspaces = new WorkspaceManager(
      app,
      await diskHost({ '/main': main, '/lib': LIB }, written),
    )
    const project = app.createProject('P')
    app.setProjectSource(project.id, { kind: 'disk', settings: { path: '/main' } })
    app.addProjectReference(project.id, { kind: 'disk', settings: { path: '/lib' } }, '/lib')
    const thread = app.createThread(project.id, { title: '用上 Button' })
    const model = fakeModel((request, role) => {
      if (role !== 'thread') return say('')
      if (request.messages.some((m) => m.role === 'tool')) return say('好了')
      return [
        call('c1', 'list_dir', { source: 'lib' }),
        call('c2', 'read_file', { source: 'lib', path: 'src/button.ts' }),
        call('c3', 'search', { source: 'lib', pattern: 'export const' }),
        call('c4', 'find_files', { source: 'lib', pattern: '*.ts' }),
        call('c5', 'read_file', { source: 'nowhere', path: 'README.md' }),
        call('c6', 'read_file', { source: 'lib', path: '../main/app.ts' }),
        call('c7', 'write_file', { source: 'lib', path: 'src/button.ts', content: 'changed' }),
        { type: 'finish', reason: 'tool-calls' },
      ]
    })
    app.postMessage(project.id, { conversationId: thread.id, author: 'user', content: '开始' })
    await startScheduler(model, workspaces).idle()

    const request = model.requests.find((r) => system(r).includes('You are a thread'))!
    expect(system(request)).toContain('Reference sources:')
    expect(system(request)).toContain('- lib: 本地目录 /lib')
    // Only the tools that look take a source.
    const takesSource = (name: string) =>
      'source' in
      (request.tools.find((tool) => tool.name === name)!.parameters as { properties: object })
        .properties
    expect(['find_files', 'list_dir', 'read_file', 'search'].map(takesSource)).toEqual([
      true,
      true,
      true,
      true,
    ])
    expect(['write_file', 'edit_file', 'run_command'].map(takesSource)).toEqual([
      false,
      false,
      false,
    ])

    const results = toolResults(thread.id)
    expect(results[0]).toEqual(['list_dir', false, 'README.md\nsrc/'])
    expect(results[1]).toEqual(['read_file', false, '    1\texport const Button = 1\n    2\t'])
    expect(results[2]).toEqual([
      'search',
      false,
      'src/button.ts:1:export const Button = 1\nsrc/input.ts:1:export const Input = 2',
    ])
    expect(results[3]).toEqual(['find_files', false, 'src/button.ts\nsrc/input.ts'])
    expect(results[4]?.slice(0, 2)).toEqual(['read_file', true])
    expect(results[4]?.[2]).toContain('error.noReference')
    // A path cannot climb out of the reference.
    expect(results[5]?.slice(0, 2)).toEqual(['read_file', true])
    // write_file knows no source: the file lands in the thread's own workspace.
    expect(written).toEqual(['src/button.ts'])
    expect(LIB['src/button.ts']).toBe('export const Button = 1\n')
    expect(main).toHaveProperty(['src/button.ts'], 'changed')
  })

  test('a project without references has no section; a thread may still ask for one', async () => {
    const workspaces = new WorkspaceManager(app, await diskHost({ '/main': { 'a.ts': '' } }))
    const project = app.createProject('P')
    app.setProjectSource(project.id, { kind: 'disk', settings: { path: '/main' } })
    const thread = app.createThread(project.id, { title: 'T' })
    const model = fakeModel(() => say(''))
    app.postMessage(project.id, { conversationId: thread.id, author: 'user', content: '开始' })
    await startScheduler(model, workspaces).idle()
    const request = model.requests.find((r) => system(r).includes('You are a thread'))!
    expect(system(request)).not.toContain('Reference sources:')
    // What it asks for and is allowed, it reads in the same turn: the parameter is there.
    expect(JSON.stringify(request.tools.find((tool) => tool.name === 'read_file'))).toContain(
      '"source"',
    )
    expect(request.tools.map((tool) => tool.name)).toContain('add_reference')
  })

  test('one that cannot be reached fails the read that asked, and names itself', async () => {
    const workspaces = new WorkspaceManager(app, await diskHost({ '/main': {} }))
    const project = app.createProject('P')
    app.setProjectSource(project.id, { kind: 'disk', settings: { path: '/main' } })
    app.addProjectReference(project.id, { kind: 'disk', settings: { path: '/gone' } }, '/gone')
    await expect(workspaces.referenceView(project.id, 'gone')).rejects.toMatchObject({
      code: 'error.referenceUnreadable',
      params: { name: 'gone', detail: 'no such directory: /gone' },
    })
    // The project's own source reads as before.
    expect(await (await workspaces.view(project.id)).list('.')).toEqual([])
  })
})

describe('the coordinator adds a reference source', () => {
  test('it asks on a card, adds on a yes and reads it in the same turn', async () => {
    const workspaces = new WorkspaceManager(app, await diskHost({ '/lib': LIB }))
    const project = app.createProject('P')
    const add = call('c1', 'add_reference', {
      kind: 'disk',
      // A secret the model makes up is dropped, never stored.
      settings: { path: ' /lib ', token: 'made-up' },
    })
    const model = fakeModel((request, role) => {
      if (role !== 'coordinator') return say('')
      const last = request.messages.at(-1)
      if (last?.role !== 'tool') return [add, { type: 'finish', reason: 'tool-calls' }]
      if (String(last.content).startsWith('Added')) {
        return [
          call('c2', 'read_file', { source: 'lib', path: 'README.md' }),
          { type: 'finish', reason: 'tool-calls' },
        ]
      }
      return say('')
    })
    app.postMessage(project.id, {
      conversationId: project.coordinatorId,
      author: 'user',
      content: '也参考一下 /lib',
    })
    await startScheduler(model, workspaces).idle()

    // Connected to see that it reads, then asked; nothing is added yet.
    expect(connected).toEqual([{ path: '/lib' }])
    expect(app.getProject(project.id).references).toEqual([])
    const [card] = app.listOpenAttentions(project.id)
    expect(card?.block).toMatchObject({
      type: 'confirm',
      title: '添加参考来源？',
      detail: '本地目录 /lib',
    })
    expect(toolResults(project.coordinatorId).at(-1)?.[2]).toContain('The user was asked')

    // The card is in the coordinator's chat too, not only in the list of what needs the user.
    expect(
      app.listMessages(project.coordinatorId).map((m) => m.ask?.attentionId ?? null),
    ).toContain(card!.id)
    app.answerAttention(card!.id, { type: 'confirm', approved: true })
    await scheduler!.idle()
    const [reference] = app.getProject(project.id).references
    expect(reference).toMatchObject({ name: 'lib', kind: 'disk', settings: { path: '/lib' } })
    const results = toolResults(project.coordinatorId)
    expect(results.at(-2)?.[2]).toContain('Added. Read it with source "lib"')
    expect(results.at(-1)).toEqual(['read_file', false, '    1\t# lib\n    2\t组件库\n    3\t'])

    // Asked for again, it is already there: no second card.
    const again = fakeModel((request, role) => {
      if (role !== 'coordinator') return say('')
      return request.messages.at(-1)?.role === 'tool'
        ? say('')
        : [add, { type: 'finish', reason: 'tool-calls' }]
    })
    await scheduler!.stop()
    app.postMessage(project.id, {
      conversationId: project.coordinatorId,
      author: 'user',
      content: '再加一次',
    })
    await startScheduler(again, workspaces).idle()
    expect(toolResults(project.coordinatorId).at(-1)?.[2]).toContain('You can already read it')
    expect(app.listOpenAttentions(project.id)).toEqual([])
    expect(app.getProject(project.id).references).toHaveLength(1)
  })

  test('one that cannot be read asks nothing, and a no adds nothing', async () => {
    const workspaces = new WorkspaceManager(app, await diskHost({ '/lib': LIB }))
    const project = app.createProject('P')
    let path = '/typo'
    const model = fakeModel((request, role) => {
      if (role !== 'coordinator') return say('')
      return request.messages.at(-1)?.role === 'tool'
        ? say('')
        : [
            call('c1', 'add_reference', { kind: 'disk', settings: { path } }),
            { type: 'finish', reason: 'tool-calls' },
          ]
    })
    const ask = async (content: string) => {
      app.postMessage(project.id, {
        conversationId: project.coordinatorId,
        author: 'user',
        content,
      })
      await scheduler!.idle()
    }
    startScheduler(model, workspaces)
    await ask('参考 /typo')
    expect(toolResults(project.coordinatorId).at(-1)?.slice(0, 2)).toEqual(['add_reference', true])
    expect(app.listOpenAttentions(project.id)).toEqual([])

    path = '/lib'
    await ask('参考 /lib')
    const [card] = app.listOpenAttentions(project.id)
    app.answerAttention(card!.id, { type: 'confirm', approved: false })
    await scheduler!.idle()
    expect(toolResults(project.coordinatorId).at(-1)?.[2]).toContain('declined')
    expect(app.getProject(project.id).references).toEqual([])
  })
})

describe('a thread asks to read a source', () => {
  const LOGS = { 'access.log': 'GET / 502\n' }

  /** A thread that asks for `/logs` whenever it is its turn, and reads it once allowed. */
  function asking(keep: 'thread' | 'project') {
    return fakeModel((request, role) => {
      if (role !== 'thread') return say('')
      const last = request.messages.at(-1)
      if (last?.role !== 'tool') {
        return [
          call('c1', 'add_reference', {
            kind: 'disk',
            settings: { path: '/logs' },
            reason: '排查 502 要看访问日志',
            keep,
          }),
          { type: 'finish', reason: 'tool-calls' },
        ]
      }
      if (/^(Allowed|Added)/.test(String(last.content))) {
        return [
          call('c2', 'read_file', { source: 'logs', path: Object.keys(LOGS)[0] }),
          { type: 'finish', reason: 'tool-calls' },
        ]
      }
      return say('')
    })
  }

  async function start(keep: 'thread' | 'project', full = false) {
    const workspaces = new WorkspaceManager(
      app,
      await diskHost({ '/main': { 'a.ts': '' }, '/logs': LOGS }),
    )
    const project = app.createProject('P')
    app.setProjectSource(project.id, { kind: 'disk', settings: { path: '/main' } })
    for (let n = 0; n < (full ? MAX_REFERENCES : 0); n++) {
      app.addProjectReference(project.id, { kind: 'disk', settings: { path: `/r${n}` } }, `/r${n}`)
    }
    const thread = app.createThread(project.id, { title: 'T' })
    const model = asking(keep)
    app.postMessage(project.id, { conversationId: thread.id, author: 'user', content: '查 502' })
    await startScheduler(model, workspaces).idle()
    const [card] = app.listOpenAttentions(project.id)
    return { workspaces, project, thread, model, card: card! }
  }

  test('for itself alone: the card offers both, and only this thread reads it', async () => {
    const { workspaces, project, thread, model, card } = await start('thread')
    expect(card.block).toEqual({
      type: 'choice',
      question: '读取本地目录 /logs？\n排查 502 要看访问日志',
      options: [
        { label: '加入项目', consequence: '协调器和所有讨论串都能读' },
        { label: '仅这个讨论串', consequence: '只有这个讨论串能读，结束后失效' },
        { label: '不用', consequence: '不读它，继续做别的' },
      ],
      recommended: 1,
    })
    // In the thread's chat too, and nothing is readable before the answer.
    expect(app.listMessages(thread.id).map((m) => m.ask?.attentionId ?? null)).toContain(card.id)
    expect(app.getThread(thread.id).references).toEqual([])

    app.answerAttention(card.id, { type: 'choice', choice: 1 })
    await scheduler!.idle()
    const [own] = app.getThread(thread.id).references
    expect(own).toMatchObject({
      name: 'logs',
      kind: 'disk',
      settings: { path: '/logs' },
      attentionId: card.id,
    })
    expect(app.getProject(project.id).references).toEqual([])
    // (A thread that ends a turn without reporting is told to, so it asks once more and
    // hears that it can already read it.)
    const results = toolResults(thread.id)
    expect(results.map((result) => result[2]).join('\n')).toContain('Allowed for this thread only')
    expect(results).toContainEqual(['read_file', false, '    1\tGET / 502\n    2\t'])
    // Its next turn is told about it; the coordinator and other threads are not, and
    // cannot read it by its name.
    const told = model.requests.filter((r) => system(r).includes('You are a thread')).at(-1)!
    expect(system(told)).toContain('logs: 本地目录 /logs (allowed for this thread only)')
    const other = app.createThread(project.id, { title: 'Other' })
    await expect(workspaces.referenceView(project.id, 'logs')).rejects.toMatchObject({
      code: 'error.noReference',
    })
    await expect(workspaces.referenceView(project.id, 'logs', other.id)).rejects.toMatchObject({
      code: 'error.noReference',
    })
    expect(workspaces.references(project.id, 'zh-CN')).toEqual([])
    expect(workspaces.references(project.id, 'zh-CN', other.id)).toEqual([])

    // Taken back, it is gone for the thread too; the thread is told, and the card says so.
    app.removeThreadReference(thread.id, own!.id)
    await expect(workspaces.referenceView(project.id, 'logs', thread.id)).rejects.toMatchObject({
      code: 'error.noReference',
    })
    expect(app.getThread(thread.id).endedReferences).toEqual([
      { attentionId: card.id, kept: false },
    ])
    const note = notes(thread.id).at(-1)!
    expect(note).toEqual({ type: 'reference-ended', name: 'logs', now: null })
    expect(describeNote(note, () => '')).toContain('you can no longer read it')
  })

  test('a full project: the thread is recommended, and picking the project asks again', async () => {
    const { project, thread, card } = await start('project', true)
    expect(card.block).toMatchObject({ type: 'choice', recommended: 1 })
    app.answerAttention(card.id, { type: 'choice', choice: 0 })
    await scheduler!.idle()
    expect(
      toolResults(thread.id)
        .map((result) => result[2])
        .join('\n'),
    ).toContain('nothing was added')
    expect(app.getThread(thread.id).references).toEqual([])
    expect(app.getProject(project.id).references).toHaveLength(MAX_REFERENCES)
    // That answer is spent: asking again (here after the nudge to report) is a new card.
    const [again] = app.listOpenAttentions(project.id)
    expect(again).toBeDefined()
    expect(again!.id).not.toBe(card.id)
    app.answerAttention(again!.id, { type: 'choice', choice: 1 })
    await scheduler!.idle()
    expect(app.getThread(thread.id).references).toMatchObject([
      { settings: { path: '/logs' }, attentionId: again!.id },
    ])
  })

  test('the user may pick the project instead, and everyone reads it', async () => {
    const { workspaces, project, thread, card } = await start('thread')
    app.answerAttention(card.id, { type: 'choice', choice: 0 })
    await scheduler!.idle()
    expect(app.getThread(thread.id).references).toEqual([])
    expect(app.getProject(project.id).references).toMatchObject([{ name: 'logs', kind: 'disk' }])
    const results = toolResults(thread.id)
    expect(results.map((result) => result[2]).join('\n')).toContain(
      'Added. Read it with source "logs"',
    )
    expect(results).toContainEqual(['read_file', false, '    1\tGET / 502\n    2\t'])
    expect(await (await workspaces.referenceView(project.id, 'logs')).list('.')).toEqual([
      { name: 'access.log', kind: 'file' },
    ])
  })

  test('a no allows nothing, and the thread does not ask again by itself', async () => {
    const { project, thread, card } = await start('project')
    expect(card.block).toMatchObject({ type: 'choice', recommended: 0 })
    app.answerAttention(card.id, { type: 'choice', choice: 2 })
    await scheduler!.idle()
    expect(toolResults(thread.id).at(-1)?.[2]).toContain('declined')
    expect(app.listOpenAttentions(project.id)).toEqual([])
    expect(app.getThread(thread.id).references).toEqual([])
    expect(app.getProject(project.id).references).toEqual([])
  })

  test('one allowed for a thread can be kept for the project under its name', async () => {
    const { workspaces, project, thread, card } = await start('thread')
    app.answerAttention(card.id, { type: 'choice', choice: 1 })
    await scheduler!.idle()
    const [own] = app.getThread(thread.id).references
    // The project took the name since: the thread's keeps its own, and is renamed when kept.
    app.addProjectReference(project.id, { kind: 'disk', settings: { path: '/main' } }, '/x/logs')
    expect(await (await workspaces.referenceView(project.id, 'logs', thread.id)).list('.')).toEqual(
      [{ name: 'access.log', kind: 'file' }],
    )
    const kept = app.keepThreadReference(thread.id, own!.id)
    expect(kept).toMatchObject({ id: own!.id, name: 'logs-2', settings: { path: '/logs' } })
    expect(app.getThread(thread.id).references).toEqual([])
    expect(app.getProject(project.id).references.map((ref) => ref.name)).toEqual(['logs', 'logs-2'])
    // The thread hears the new name, and its card says where the source went.
    expect(app.getThread(thread.id).endedReferences).toEqual([{ attentionId: card.id, kept: true }])
    const note = notes(thread.id).at(-1)!
    expect(note).toEqual({ type: 'reference-ended', name: 'logs', now: 'logs-2' })
    expect(describeNote(note, () => '')).toContain('read it with source "logs-2" from now on')
  })

  test('a place kept out of reach is refused before any card', async () => {
    const workspaces = new WorkspaceManager(app, await diskHost({ '/main': {} }))
    const project = app.createProject('P')
    app.setProjectSource(project.id, { kind: 'disk', settings: { path: '/main' } })
    const thread = app.createThread(project.id, { title: 'T' })
    const model = fakeModel((request, role) => {
      if (role !== 'thread' || request.messages.at(-1)?.role === 'tool') return say('')
      return [
        call('c1', 'add_reference', {
          kind: 'disk',
          settings: { path: '/' },
          reason: '看看',
          keep: 'thread',
        }),
        { type: 'finish', reason: 'tool-calls' },
      ]
    })
    app.postMessage(project.id, { conversationId: thread.id, author: 'user', content: '开始' })
    await startScheduler(model, workspaces).idle()
    const result = toolResults(thread.id).find(([name]) => name === 'add_reference')!
    expect(result.slice(0, 2)).toEqual(['add_reference', true])
    expect(result[2]).toContain('too wide')
    expect(app.listOpenAttentions(project.id)).toEqual([])
    // It never got as far as connecting.
    expect(connected.filter((settings) => settings.path === '/')).toEqual([])
  })
})

describe('what a conversation may not ask for', () => {
  const home = '/home/someone'
  const hidden = ['/home/someone/.ariadne']

  test('on this machine: protected places, and anything wide enough to hold one', () => {
    expect(localRefusal('/home/someone/code/dou-ui', hidden, home)).toBeNull()
    expect(localRefusal('/var/log/nginx', hidden, home)).toBeNull()
    expect(localRefusal('/home/someone/.ssh', hidden, home)).toContain('out of reach')
    expect(localRefusal('/home/someone/.ssh/config', hidden, home)).toContain('out of reach')
    expect(localRefusal('/home/someone/.config/gh', hidden, home)).toContain('out of reach')
    expect(localRefusal('/home/someone/.ariadne/worktrees/x', hidden, home)).toContain(
      'out of reach',
    )
    expect(localRefusal('/home/someone', hidden, home)).toContain('too wide')
    expect(localRefusal('/home/someone/.config', hidden, home)).toContain('too wide')
    expect(localRefusal('/home', hidden, home)).toContain('too wide')
    expect(localRefusal('/', hidden, home)).toContain('too wide')
    // Spelled around it: still the same place.
    expect(localRefusal('/var/../home/someone/.ssh', hidden, home)).toContain('out of reach')
  })

  test('on another machine, by its spelling', () => {
    expect(remoteRefusal('/srv/app/logs')).toBeNull()
    expect(remoteRefusal('~/code/app')).toBeNull()
    expect(remoteRefusal('/')).toContain('too wide')
    expect(remoteRefusal('~')).toContain('too wide')
    expect(remoteRefusal('/home/deploy/')).toContain('too wide')
    expect(remoteRefusal('/root')).toContain('too wide')
    expect(remoteRefusal('~/.ssh')).toContain('out of reach')
    expect(remoteRefusal('/home/deploy/.aws/cli')).toContain('out of reach')
    expect(remoteRefusal('C:\\Users\\deploy')).toContain('too wide')
    // A directory that holds one of them.
    expect(remoteRefusal('/home/deploy/.config')).toContain('too wide')
    expect(remoteRefusal('~/.local/share/')).toContain('too wide')
    expect(remoteRefusal('~/.config/app')).toBeNull()
    // Spelled around it: judged by where it leads, or refused when that cannot be told.
    expect(remoteRefusal('/home/deploy/proj/..')).toContain('too wide')
    expect(remoteRefusal('/root/x/..')).toContain('too wide')
    expect(remoteRefusal('/srv/../root')).toContain('too wide')
    expect(remoteRefusal('/srv/app/../../home/deploy/.ssh')).toContain('out of reach')
    expect(remoteRefusal('/srv/app/./logs/../logs')).toBeNull()
    expect(remoteRefusal('~/..')).toContain('"..')
    expect(remoteRefusal('~/../other/code')).toContain('"..')
    expect(remoteRefusal('../code')).toContain('"..')
  })
})
