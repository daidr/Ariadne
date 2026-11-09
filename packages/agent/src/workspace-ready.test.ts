import { Ariadne, installCommands, parseProjectSettings } from '@ariadne/core'
import type { ContextProvider, ExecResult, ModelRequest, Workspace } from '@ariadne/plugin-sdk'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { DEFAULT_LIMITS, type TurnSettings } from './limits'
import { PluginHost } from './plugins'
import type { ResolvedModel } from './runner'
import { Scheduler } from './scheduler'
import { call, fakeModel, say } from './test-model'
import { TOOL_SWITCH_NAMES } from './tools'
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

const toolNames = (request: ModelRequest) => request.tools.map((tool) => tool.name)
const toolResults = (conversationId: string) =>
  app
    .listTranscript(conversationId)
    .flatMap((e) =>
      e.kind === 'tool' ? [{ name: e.name, error: e.isError, text: e.content }] : [],
    )

/** A source whose workspaces live in memory; `exec` answers each command run in them. */
async function memoryHost(
  files: Record<string, string>,
  exec: (command: string) => Partial<ExecResult> = () => ({}),
) {
  const ran: string[] = []
  const workspace = (branch: string | null): Workspace => ({
    id: 'w',
    label: '/work',
    branch,
    base: 'main',
    readFile: async (path) => {
      if (!(path in files)) throw new Error(`ENOENT: ${path}`)
      return files[path]!
    },
    readBytes: async (path) => new TextEncoder().encode(files[path] ?? ''),
    writeFile: async (path, content) => {
      files[path] = content
    },
    list: async () => Object.keys(files).map((name) => ({ name, kind: 'file' as const })),
    exec: async (command) => {
      ran.push(command)
      return { exitCode: 0, stdout: '', stderr: '', ...exec(command) }
    },
    dispose: async () => {},
  })
  const provider: ContextProvider<never> = {
    kind: 'memory',
    title: { 'zh-CN': '内存' },
    settings: { type: 'object', properties: {} },
    provides: ['workspace'],
    async connect() {
      return {
        workspace: { isolated: true, open: async (request) => workspace(request.branch ?? null) },
      }
    },
  }
  const host = new PluginHost('/tmp/ariadne-test-plugins')
  await host.load({
    id: 'memory',
    name: { 'zh-CN': '内存' },
    setup: (ctx) => ctx.registerContextProvider(provider),
  })
  return { workspaces: new WorkspaceManager(app, host), ran }
}

function start(
  model: ResolvedModel,
  workspaces: WorkspaceManager,
  settings: Partial<TurnSettings> = {},
) {
  scheduler = new Scheduler({
    app,
    resolveModel: () => model,
    locale: () => 'zh-CN',
    retryBaseMs: 1,
    settings: () => ({
      limits: { ...DEFAULT_LIMITS },
      instructions: '',
      commandRules: [],
      env: {},
      ...settings,
    }),
    workspaces,
  })
  scheduler.start()
  return scheduler
}

function project() {
  const created = app.createProject('P')
  app.setProjectSource(created.id, { kind: 'memory', settings: {} })
  return created
}

function brief(projectId: string, threadId: string) {
  app.postMessage(projectId, {
    conversationId: threadId,
    kind: 'relay',
    author: 'coordinator',
    content: '开始',
  })
}

/** A thread's model that makes `calls` one after the other, then answers. */
const steps = (calls: [string, Record<string, unknown>][], seen: ModelRequest[] = []) =>
  fakeModel((request, role) => {
    if (role === 'coordinator') return say('')
    seen.push(request)
    const next = calls[request.messages.filter((m) => m.role === 'tool').length]
    return next ? [call(`t${seen.length}`, next[0], next[1])] : say('')
  })

describe('install commands by lockfile', () => {
  test('one for each dependency manager whose lockfile is there', () => {
    expect(installCommands(['package.json', 'bun.lock'])).toEqual(['bun install --frozen-lockfile'])
    expect(installCommands(['pnpm-lock.yaml'])).toEqual(['pnpm install --frozen-lockfile'])
    expect(installCommands(['package-lock.json'])).toEqual(['npm ci'])
    expect(installCommands(['yarn.lock'])).toEqual(['yarn install --frozen-lockfile'])
    expect(installCommands(['yarn.lock', '.yarnrc.yml'])).toEqual(['yarn install --immutable'])
    expect(installCommands(['bun.lock', 'Cargo.lock', 'uv.lock'])).toEqual([
      'bun install --frozen-lockfile',
      'uv sync --locked',
      'cargo fetch --locked',
    ])
  })

  test('none without a lockfile it knows', () => {
    expect(installCommands(['package.json', 'Makefile', 'requirements.txt'])).toEqual([])
  })

  test('lockfiles of several package managers: package.json says which, or none runs', () => {
    const both = ['package.json', 'yarn.lock', 'package-lock.json', 'go.sum']
    expect(installCommands(both)).toEqual(['go mod download'])
    expect(installCommands(both, 'yarn@1.22.22')).toEqual([
      'yarn install --frozen-lockfile',
      'go mod download',
    ])
  })
})

describe('a new workspace', () => {
  test('gets its dependencies installed by its lockfile, once, and the thread is told', async () => {
    const { workspaces, ran } = await memoryHost({ 'bun.lock': '', 'package.json': '{}' })
    const { id } = project()
    const thread = app.createThread(id, { title: '修导出' })
    brief(id, thread.id)
    await start(
      steps([
        ['list_dir', {}],
        ['run_command', { command: 'ls' }],
      ]),
      workspaces,
    ).idle()
    expect(ran).toEqual(['bun install --frozen-lockfile', 'ls'])
    const results = toolResults(thread.id)
    expect(results[0]!.text).toContain(
      'its dependencies were installed first, by its lockfiles: `bun install --frozen-lockfile`',
    )
    expect(results[1]!.text).not.toContain('dependencies')
    expect(app.getProject(id).detected.setup).toBe('bun install --frozen-lockfile')
  })

  test('with no lockfile it knows, nothing is installed', async () => {
    const { workspaces, ran } = await memoryHost({ 'README.md': '' })
    const { id } = project()
    const thread = app.createThread(id, { title: '调研' })
    brief(id, thread.id)
    await start(steps([['list_dir', {}]]), workspaces).idle()
    expect(ran).toEqual([])
    expect(app.getProject(id).detected.setup).toBe('')
    expect(toolResults(thread.id)[0]!.text).not.toContain('dependencies')
  })

  test('a failed install is told with its output, and the work goes on', async () => {
    const { workspaces } = await memoryHost({ 'package-lock.json': '' }, (command) =>
      command === 'npm ci' ? { exitCode: 1, stderr: 'npm: lock out of sync' } : {},
    )
    const { id } = project()
    const thread = app.createThread(id, { title: '修导出' })
    brief(id, thread.id)
    await start(steps([['list_dir', {}]]), workspaces).idle()
    const [first] = toolResults(thread.id)
    expect(first!.error).toBe(false)
    expect(first!.text).toContain('`npm ci` failed (exit code 1)')
    expect(first!.text).toContain('npm: lock out of sync')
  })

  test("the user's own script runs in place of the install, and an emptied one runs nothing", async () => {
    const { workspaces, ran } = await memoryHost({ 'bun.lock': '' })
    const { id } = project()
    app.setProjectSettings(id, { workspace: { setup: 'make deps' } }, TOOL_SWITCH_NAMES)
    const first = app.createThread(id, { title: '一' })
    brief(id, first.id)
    const running = start(steps([['list_dir', {}]]), workspaces)
    await running.idle()
    expect(ran).toEqual(['make deps'])
    app.setProjectSettings(
      id,
      { workspace: { setup: '', auto: { setup: false, check: true } } },
      TOOL_SWITCH_NAMES,
    )
    const second = app.createThread(id, { title: '二' })
    brief(id, second.id)
    await running.idle()
    expect(ran).toEqual(['make deps'])
  })
})

describe('the check after edits', () => {
  test('the first thread notes it, and edits of every thread are checked with it', async () => {
    const { workspaces, ran } = await memoryHost({ 'a.ts': 'let a' })
    const { id } = project()
    const first = app.createThread(id, { title: '一' })
    const seen: ModelRequest[] = []
    brief(id, first.id)
    const running = start(
      steps(
        [
          ['list_dir', {}],
          ['note_check', { command: 'tsc --noEmit {file}', files: ['a.ts'] }],
          ['write_file', { path: 'b.ts', content: 'let b' }],
        ],
        seen,
      ),
      workspaces,
    )
    await running.idle()
    expect(toolNames(seen[0]!)).toContain('note_check')
    const results = toolResults(first.id)
    expect(results[0]!.text).toContain('no check after edits yet')
    expect(results[1]!.text).toContain("Noted. It passed just now: tsc --noEmit 'a.ts'")
    expect(results[2]!.text).toContain("check after edits passed: tsc --noEmit 'b.ts'")
    expect(ran).toEqual(["tsc --noEmit 'a.ts'", "tsc --noEmit 'b.ts'"])
    expect(app.getProject(id).detected.check).toEqual({
      command: 'tsc --noEmit {file}',
      threadId: first.id,
    })

    // Another thread no longer gets to note one, and is not told to.
    const second = app.createThread(id, { title: '二' })
    seen.length = 0
    brief(id, second.id)
    await running.idle()
    expect(toolNames(seen[0]!)).not.toContain('note_check')
    expect(toolResults(second.id)[0]!.text).not.toContain('no check after edits yet')
    expect(app.canNoteCheck(second.id)).toBe(false)
    expect(app.canNoteCheck(first.id)).toBe(true)
  })

  test('a command that fails is noted with its output, to be corrected', async () => {
    const { workspaces } = await memoryHost({ 'a.ts': '' }, (command) =>
      command.startsWith('tsk') ? { exitCode: 127, stderr: 'tsk: command not found' } : {},
    )
    const { id } = project()
    const thread = app.createThread(id, { title: '一' })
    brief(id, thread.id)
    await start(
      steps([
        ['note_check', { command: 'tsk --noEmit' }],
        ['note_check', { command: 'tsc --noEmit' }],
      ]),
      workspaces,
    ).idle()
    const results = toolResults(thread.id)
    expect(results[0]!.text).toContain('It failed just now (exit code 127)')
    expect(results[0]!.text).toContain('tsk: command not found')
    expect(app.getProject(id).detected.check?.command).toBe('tsc --noEmit')
  })

  test("a command the user's rules would ask about cannot be the check", async () => {
    const { workspaces, ran } = await memoryHost({ 'a.ts': '' })
    const { id } = project()
    const thread = app.createThread(id, { title: '一' })
    brief(id, thread.id)
    await start(
      steps([
        ['note_check', { command: 'git push' }],
        ['note_check', { command: 'eslint {file}' }],
      ]),
      workspaces,
      { commandRules: [{ pattern: 'git push*', action: 'ask' }] },
    ).idle()
    const results = toolResults(thread.id)
    expect(results[0]!.error).toBe(true)
    // `{file}` needs a file to try it on.
    expect(results[1]!.error).toBe(true)
    expect(ran).toEqual([])
    expect(app.getProject(id).detected.check).toBeNull()
  })

  test("the user's own check, or none, leaves threads nothing to note", async () => {
    const { workspaces, ran } = await memoryHost({ 'a.ts': '' })
    const { id } = project()
    app.setProjectSettings(id, { workspace: { check: 'lint {file}' } }, TOOL_SWITCH_NAMES)
    const thread = app.createThread(id, { title: '一' })
    const seen: ModelRequest[] = []
    brief(id, thread.id)
    await start(steps([['write_file', { path: 'b.ts', content: '' }]], seen), workspaces).idle()
    expect(toolNames(seen[0]!)).not.toContain('note_check')
    expect(ran).toEqual(["lint 'b.ts'"])
    expect(() => app.noteCheck(thread.id, 'tsc')).toThrow()
  })

  test('another source starts over what was found out', async () => {
    const { id } = project()
    const thread = app.createThread(id, { title: '一' })
    app.recordDetectedSetup(id, 'npm ci')
    app.noteCheck(thread.id, 'tsc --noEmit')
    app.setProjectSource(id, { kind: 'memory', settings: { path: '/other' } })
    expect(app.getProject(id).detected).toEqual({ setup: null, check: null })
  })
})

const parse = (workspace: unknown) =>
  parseProjectSettings({ workspace }, TOOL_SWITCH_NAMES).workspace

describe('workspace settings', () => {
  test('a field with words is the user’s own; an empty one is left to Ariadne', () => {
    expect(parse({}).auto).toEqual({ setup: true, check: true })
    expect(parse({ setup: 'make', check: '' }).auto).toEqual({ setup: false, check: true })
    // Left to Ariadne, what was typed is not kept.
    expect(parse({ setup: 'make', auto: { setup: true, check: true } }).setup).toBe('')
    expect(parse({ check: '', auto: { check: false } })).toMatchObject({
      auto: { setup: true, check: false },
      check: '',
    })
    expect(() => parse({ auto: { setup: 'yes' } })).toThrow()
  })
})
