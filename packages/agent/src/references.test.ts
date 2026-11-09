import { Ariadne, MAX_REFERENCES, referenceName } from '@ariadne/core'
import type { ContextProvider, ModelRequest, Workspace } from '@ariadne/plugin-sdk'
import { FileTooLargeError, WorkspacePathError } from '@ariadne/plugin-sdk'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { PluginHost } from './plugins'
import type { ResolvedModel } from './runner'
import { Scheduler } from './scheduler'
import { call, fakeModel, say } from './test-model'
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
        path: { type: 'string', title: { 'zh-CN': '路径' } },
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

const LIB = {
  'README.md': '# lib\n组件库\n',
  'src/button.ts': 'export const Button = 1\n',
  'src/input.ts': 'export const Input = 2\n',
}

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
    app.removeProjectReference(project.id, first.id)
    expect(app.getProject(project.id).references.map((ref) => ref.name)).toEqual(['lib-2'])
    for (let n = 1; n < MAX_REFERENCES; n++) {
      app.addProjectReference(project.id, { kind: 'disk', settings: {} }, `/more/${n}`)
    }
    expect(() =>
      app.addProjectReference(project.id, { kind: 'disk', settings: {} }, '/one/too/many'),
    ).toThrow('error.tooManyReferences')
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
    expect(referenceSecrets(source, { id: 'ref_1', kind: 'forge' }, secrets)).toEqual({
      token: 'own',
    })
    expect(referenceSecrets(source, { id: 'ref_9', kind: 'forge' }, secrets)).toEqual({
      token: 'main',
    })
    expect(referenceSecrets(source, { id: 'ref_9', kind: 'host' }, secrets)).toEqual({
      token: 'hosting',
    })
    // Another kind gets none of them.
    expect(referenceSecrets(source, { id: 'ref_9', kind: 'disk' }, secrets)).toEqual({})
    expect(referenceSecrets(null, { kind: 'forge' }, secrets)).toEqual({})
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

  test('a project without references has no source parameter and no section', async () => {
    const workspaces = new WorkspaceManager(app, await diskHost({ '/main': { 'a.ts': '' } }))
    const project = app.createProject('P')
    app.setProjectSource(project.id, { kind: 'disk', settings: { path: '/main' } })
    const thread = app.createThread(project.id, { title: 'T' })
    const model = fakeModel(() => say(''))
    app.postMessage(project.id, { conversationId: thread.id, author: 'user', content: '开始' })
    await startScheduler(model, workspaces).idle()
    const request = model.requests.find((r) => system(r).includes('You are a thread'))!
    expect(system(request)).not.toContain('Reference sources:')
    expect(JSON.stringify(request.tools.find((tool) => tool.name === 'read_file'))).not.toContain(
      '"source"',
    )
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
    expect(toolResults(project.coordinatorId).at(-1)?.[2]).toContain('Already a reference source')
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
