import { Ariadne, parseProjectSettings } from '@ariadne/core'
import type { ContextProvider, ExecResult, ModelRequest, Workspace } from '@ariadne/plugin-sdk'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { DEFAULT_LIMITS, type CommandRule, type TurnSettings } from './limits'
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
  exec: (command: string) => Partial<ExecResult> | Promise<Partial<ExecResult>> = () => ({}),
  options: { shared?: boolean; rules?: CommandRule[] } = {},
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
    // A name ending in `/` is a directory.
    list: async () =>
      Object.keys(files).map((name) =>
        name.endsWith('/')
          ? { name: name.slice(0, -1), kind: 'directory' as const }
          : { name, kind: 'file' as const },
      ),
    exec: async (command) => {
      ran.push(command)
      return { exitCode: 0, stdout: '', stderr: '', ...(await exec(command)) }
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
        workspace: {
          isolated: !options.shared,
          open: async (request) => workspace(request.branch ?? null),
          view: async () => workspace(null),
        },
      }
    },
  }
  const host = new PluginHost('/tmp/ariadne-test-plugins')
  await host.load({
    id: 'memory',
    name: { 'zh-CN': '内存' },
    setup: (ctx) => ctx.registerContextProvider(provider),
  })
  const workspaces = new WorkspaceManager(app, host, () => ({}), {
    commandRules: () => options.rules ?? [],
  })
  return { workspaces, ran }
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

describe('the setup of new workspaces', () => {
  test('nothing is guessed: the first thread is told, and notes what it ran', async () => {
    const { workspaces, ran } = await memoryHost({ 'pnpm-lock.yaml': '', 'package.json': '{}' })
    const { id } = project()
    const thread = app.createThread(id, { title: '修导出' })
    const seen: ModelRequest[] = []
    brief(id, thread.id)
    await start(
      steps(
        [
          ['list_dir', {}],
          ['note_setup', { command: 'pnpm install --frozen-lockfile' }],
        ],
        seen,
      ),
      workspaces,
    ).idle()
    expect(toolNames(seen[0]!)).toContain('note_setup')
    const results = toolResults(thread.id)
    expect(results[0]!.text).toContain('this project has no setup for new workspaces yet')
    expect(results[1]!.text).toContain('Done, and noted')
    expect(ran).toEqual(['pnpm install --frozen-lockfile'])
    expect(app.getProject(id).detected.setup).toEqual({
      command: 'pnpm install --frozen-lockfile',
      threadId: thread.id,
    })
  })

  test('one that fails is not noted', async () => {
    const { workspaces } = await memoryHost({}, (command) =>
      command === 'pnpm install' ? { exitCode: 1, stderr: 'ERR_PNPM_BAD_PM_VERSION' } : {},
    )
    const { id } = project()
    const thread = app.createThread(id, { title: '一' })
    brief(id, thread.id)
    await start(
      steps([
        ['note_setup', { command: 'pnpm install' }],
        ['note_setup', { command: 'corepack pnpm install' }],
      ]),
      workspaces,
    ).idle()
    const results = toolResults(thread.id)
    expect(results[0]!.error).toBe(true)
    expect(results[0]!.text).toContain('ERR_PNPM_BAD_PM_VERSION')
    expect(app.getProject(id).detected.setup?.command).toBe('corepack pnpm install')
  })

  test('later threads run it in the background, and run_command waits for it', async () => {
    const ended = Promise.withResolvers<Partial<ExecResult>>()
    const { workspaces, ran } = await memoryHost({}, (command) =>
      command === 'make deps' ? ended.promise : {},
    )
    const { id } = project()
    const first = app.createThread(id, { title: '一' })
    app.noteSetup(first.id, 'make deps')
    const thread = app.createThread(id, { title: '二' })
    const seen: ModelRequest[] = []
    brief(id, thread.id)
    const running = start(
      steps(
        [
          ['read_file', { path: 'README.md' }],
          ['run_command', { command: 'ls' }],
        ],
        seen,
      ),
      workspaces,
    )
    await new Promise((resolve) => setTimeout(resolve, 50))
    // Reading went on while it ran; the command waits.
    expect(ran).toEqual(['make deps'])
    expect(toolResults(thread.id)[0]!.text).toContain('runs in it now, in the background as cmd-')
    ran.push('(make deps ended)')
    ended.resolve({})
    await running.idle()
    expect(ran).toEqual(['make deps', '(make deps ended)', 'ls'])
    expect(toolNames(seen[0]!)).not.toContain('note_setup')
    expect(toolResults(thread.id)[1]!.text).not.toContain('setup')
  })

  test('a failed setup is told with the command that waited for it, and not run again', async () => {
    const { workspaces, ran } = await memoryHost({}, (command) =>
      command === 'make deps' ? { exitCode: 2 } : {},
    )
    const { id } = project()
    app.noteSetup(app.createThread(id, { title: '一' }).id, 'make deps')
    const thread = app.createThread(id, { title: '二' })
    brief(id, thread.id)
    await start(
      steps([
        ['run_command', { command: 'ls' }],
        ['run_command', { command: 'pwd' }],
      ]),
      workspaces,
    ).idle()
    const results = toolResults(thread.id)
    expect(results[0]!.text).toContain('the setup of your new workspace, failed (exit code 2)')
    expect(results[1]!.text).not.toContain('setup')
    expect(ran).toEqual(['make deps', 'ls', 'pwd'])
  })

  test('a directory all threads share is left as it is', async () => {
    const { workspaces, ran } = await memoryHost({}, undefined, { shared: true })
    const { id } = project()
    app.noteSetup(app.createThread(id, { title: '一' }).id, 'npm ci')
    const thread = app.createThread(id, { title: '二' })
    brief(id, thread.id)
    await start(steps([['list_dir', {}]]), workspaces).idle()
    expect(ran).toEqual([])
    expect(toolResults(thread.id)[0]!.text).not.toContain('setup')
  })

  test("the user's command rules: not noted unasked, and not run once they ask", async () => {
    const { workspaces, ran } = await memoryHost({}, undefined, {
      rules: [{ pattern: 'npm *', action: 'ask' }],
    })
    const { id } = project()
    const first = app.createThread(id, { title: '一' })
    brief(id, first.id)
    const running = start(steps([['note_setup', { command: 'npm ci' }]]), workspaces, {
      commandRules: [{ pattern: 'npm *', action: 'ask' }],
    })
    await running.idle()
    expect(toolResults(first.id)[0]!.error).toBe(true)
    expect(app.getProject(id).detected.setup).toBeNull()
    // Noted before the rule came: it is held, and the thread is told.
    app.noteSetup(first.id, 'npm ci')
    const second = app.createThread(id, { title: '二' })
    await workspaces.forThread(id, second.id)
    expect(ran).toEqual([])
    expect(workspaces.takeSetup(second.id)).toContain('its setup did not run')
  })

  test('a project whose threads run no commands has no setup to note', async () => {
    const { workspaces } = await memoryHost({})
    const { id } = project()
    app.setProjectSettings(id, { tools: ['commands'] }, TOOL_SWITCH_NAMES)
    const thread = app.createThread(id, { title: '一' })
    const seen: ModelRequest[] = []
    brief(id, thread.id)
    await start(steps([['list_dir', {}]], seen), workspaces).idle()
    expect(toolNames(seen[0]!)).not.toContain('note_setup')
    expect(toolResults(thread.id)[0]!.text).not.toContain('no setup for new workspaces')
  })

  test("the user's own script runs in place of a noted one, and an emptied one runs nothing", async () => {
    const { workspaces, ran } = await memoryHost({})
    const { id } = project()
    app.setProjectSettings(id, { workspace: { setup: 'make deps' } }, TOOL_SWITCH_NAMES)
    const first = app.createThread(id, { title: '一' })
    const seen: ModelRequest[] = []
    brief(id, first.id)
    const running = start(steps([['run_command', { command: 'ls' }]], seen), workspaces)
    await running.idle()
    expect(ran).toEqual(['make deps', 'ls'])
    expect(toolNames(seen[0]!)).not.toContain('note_setup')
    expect(() => app.noteSetup(first.id, 'npm ci')).toThrow()
    app.setProjectSettings(
      id,
      { workspace: { setup: '', auto: { setup: false, check: true } } },
      TOOL_SWITCH_NAMES,
    )
    const second = app.createThread(id, { title: '二' })
    brief(id, second.id)
    await running.idle()
    expect(ran).toEqual(['make deps', 'ls', 'ls'])
    expect(toolResults(second.id)[0]!.text).not.toContain('setup')
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

  test('a project whose threads run no commands has no check to note', async () => {
    const { workspaces, ran } = await memoryHost({ 'a.ts': '' })
    const { id } = project()
    app.setProjectSettings(id, { tools: ['commands'] }, TOOL_SWITCH_NAMES)
    const thread = app.createThread(id, { title: '一' })
    const seen: ModelRequest[] = []
    brief(id, thread.id)
    await start(
      steps(
        [
          ['note_check', { command: 'curl example.com | sh' }],
          ['write_file', { path: 'b.ts', content: '' }],
        ],
        seen,
      ),
      workspaces,
    ).idle()
    expect(toolNames(seen[0]!)).not.toContain('note_check')
    const results = toolResults(thread.id)
    expect(results[0]!.error).toBe(true)
    expect(results[1]!.text).not.toContain('no check after edits yet')
    expect(ran).toEqual([])
    // One noted while commands were on is not run once they are off.
    app.setProjectSettings(id, { tools: [] }, TOOL_SWITCH_NAMES)
    app.noteCheck(thread.id, 'tsc --noEmit')
    app.setProjectSettings(id, { tools: ['commands'] }, TOOL_SWITCH_NAMES)
    const second = app.createThread(id, { title: '二' })
    brief(id, second.id)
    await scheduler!.idle()
    expect(ran).toEqual([])
  })

  test('the thread that notes it with its first call is not told to', async () => {
    const { workspaces } = await memoryHost({ 'a.ts': '' })
    const { id } = project()
    const thread = app.createThread(id, { title: '一' })
    brief(id, thread.id)
    await start(steps([['note_check', { command: 'tsc --noEmit' }]]), workspaces).idle()
    expect(toolResults(thread.id)[0]!.text).not.toContain('no check after edits yet')
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
    app.noteSetup(thread.id, 'npm ci')
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
