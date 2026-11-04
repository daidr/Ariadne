import { Ariadne } from '@ariadne/core'
import type {
  ContextProvider,
  ExecOptions,
  ModelRequest,
  Workspace,
  WorkspaceRequest,
} from '@ariadne/plugin-sdk'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { commandParts, commandVerdict, DEFAULT_LIMITS, type TurnSettings } from './limits'
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

const system = (request: ModelRequest) => request.messages[0]?.content ?? ''
const toolNames = (request: ModelRequest) => request.tools.map((tool) => tool.name)
const toolResults = (conversationId: string) =>
  app
    .listTranscript(conversationId)
    .flatMap((e) =>
      e.kind === 'tool' ? [{ name: e.name, error: e.isError, text: e.content }] : [],
    )

interface Ran {
  command: string
  options: ExecOptions
}

/** A source whose workspaces live in memory and remember how commands were run. */
async function memoryHost(files: Record<string, string>) {
  const ran: Ran[] = []
  const opened: WorkspaceRequest[] = []
  const workspace = (branch: string | null): Workspace => ({
    id: 'w',
    label: '/work',
    branch,
    base: 'main',
    readFile: async (path) => {
      if (!(path in files)) throw new Error(`ENOENT: ${path}`)
      return files[path]!
    },
    readBytes: async (path) => {
      if (!(path in files)) throw new Error(`ENOENT: ${path}`)
      return new TextEncoder().encode(files[path]!)
    },
    writeFile: async (path, content) => {
      files[path] = content
    },
    list: async () => Object.keys(files).map((name) => ({ name, kind: 'file' as const })),
    exec: async (command, options = {}) => {
      ran.push({ command, options })
      return { exitCode: 0, stdout: `ran ${command}`, stderr: '' }
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
          isolated: true,
          open: async (request) => {
            opened.push(request)
            return workspace(request.branch ?? null)
          },
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
  return { host, ran, opened }
}

function start(
  model: ResolvedModel,
  options: { workspaces?: WorkspaceManager; settings?: Partial<TurnSettings> } = {},
) {
  const settings: TurnSettings = {
    limits: { ...DEFAULT_LIMITS },
    instructions: '',
    commandRules: [],
    env: {},
    ...options.settings,
  }
  scheduler = new Scheduler({
    app,
    resolveModel: () => model,
    locale: () => 'zh-CN',
    retryBaseMs: 1,
    settings: () => settings,
    ...(options.workspaces ? { workspaces: options.workspaces } : {}),
  })
  scheduler.start()
  return scheduler
}

function brief(projectId: string, threadId: string, content = '开始') {
  app.postMessage(projectId, {
    conversationId: threadId,
    kind: 'relay',
    author: 'coordinator',
    content,
  })
}

describe('command rules', () => {
  test('a command is split at its separators, quotes kept whole', () => {
    expect(commandParts('bun test && git push --force; echo "a; b" | tee x\nls')).toEqual([
      'bun test',
      'git push --force',
      'echo "a; b"',
      'tee x',
      'ls',
    ])
  })

  test('deny wins, a narrower allow lifts an ask, and no match allows', () => {
    const rules = [
      { pattern: 'git push*', action: 'ask' as const },
      { pattern: 'git push origin ariadne/*', action: 'allow' as const },
      { pattern: 'rm -rf /*', action: 'deny' as const },
    ]
    expect(commandVerdict('make && git push --force', rules)).toEqual({
      action: 'ask',
      pattern: 'git push*',
    })
    expect(commandVerdict('git push origin ariadne/x', rules).action).toBe('allow')
    expect(commandVerdict('ls; rm -rf /tmp/x', rules).action).toBe('deny')
    expect(commandVerdict('bun test', rules)).toEqual({ action: 'allow', pattern: null })
    // Only `*` is special: the rest of a pattern is taken as written.
    expect(commandVerdict('git pushy', [{ pattern: 'git push', action: 'deny' }]).action).toBe(
      'allow',
    )
  })

  test('a denied command never runs; an asked one runs once after the yes', async () => {
    const { host, ran } = await memoryHost({})
    const workspaces = new WorkspaceManager(app, host)
    const project = app.createProject('P')
    app.setProjectSource(project.id, { kind: 'memory', settings: {} })
    const thread = app.createThread(project.id, { title: '发布' })
    let turn = 0
    const model = fakeModel((request, role) => {
      if (role === 'coordinator') return say('')
      if (request.messages.at(-1)?.role !== 'tool') {
        turn++
        return [
          call(`d${turn}`, 'run_command', { command: 'rm -rf /etc' }),
          call(`a${turn}`, 'run_command', { command: 'git push origin main' }),
          { type: 'finish', reason: 'tool-calls' },
        ]
      }
      return say('')
    })
    const rules = [
      { pattern: 'rm -rf *', action: 'deny' as const },
      { pattern: 'git push *', action: 'ask' as const },
    ]
    brief(project.id, thread.id)
    await start(model, { workspaces, settings: { commandRules: rules } }).idle()

    expect(ran).toEqual([])
    const first = toolResults(thread.id)
    expect(first[0]).toMatchObject({ error: true })
    expect(first[0]!.text).toContain('do not allow')
    expect(first[1]!.text).toContain('they were asked')
    const [ask] = app.listOpenAttentions(project.id)
    expect(ask?.block).toMatchObject({ type: 'confirm', detail: 'git push origin main' })

    // The yes wakes the thread; the same command then runs, once.
    app.answerAttention(ask!.id, { type: 'confirm', approved: true })
    await scheduler!.idle()
    expect(ran.map((r) => r.command)).toEqual(['git push origin main'])
    // The yes is spent: the same command later asks again.
    brief(project.id, thread.id, '再推一次')
    await scheduler!.idle()
    expect(ran).toHaveLength(1)
    expect(app.listOpenAttentions(project.id)).toHaveLength(1)
  })

  test("commands run in the project's shell, with its environment and the user's limits", async () => {
    const { host, ran } = await memoryHost({})
    const workspaces = new WorkspaceManager(app, host)
    const project = app.createProject('P')
    app.setProjectSource(project.id, { kind: 'memory', settings: {} })
    app.setProjectSettings(
      project.id,
      { workspace: { setup: '', shell: 'zsh', branchPrefix: 'ariadne/' } },
      TOOL_SWITCH_NAMES,
    )
    const thread = app.createThread(project.id, { title: '跑测试' })
    const model = fakeModel((request, role) => {
      if (role === 'coordinator' || request.messages.at(-1)?.role === 'tool') return say('')
      return [
        call('t1', 'run_command', { command: 'bun test' }),
        call('t2', 'run_command', { command: 'bun run build', timeoutSeconds: 9999 }),
        { type: 'finish', reason: 'tool-calls' },
      ]
    })
    brief(project.id, thread.id)
    await start(model, {
      workspaces,
      settings: {
        env: { CI: '1' },
        limits: { ...DEFAULT_LIMITS, commandTimeout: 30, commandTimeoutMax: 90 },
      },
    }).idle()
    expect(ran.map((r) => [r.options.shell, r.options.env, r.options.timeoutMs])).toEqual([
      [['zsh', '-c'], { CI: '1' }, 30_000],
      [['zsh', '-c'], { CI: '1' }, 90_000],
    ])
    const request = model.requests.find((r) => system(r).includes('You are a thread'))!
    const spec = request.tools.find((tool) => tool.name === 'run_command')!
    expect(spec.description).toContain('(zsh)')
    expect(JSON.stringify(spec.parameters)).toContain('Default 30, at most 90.')
  })
})

describe('project settings', () => {
  test('switched-off tools are not offered', async () => {
    const { host } = await memoryHost({})
    const workspaces = new WorkspaceManager(app, host)
    const project = app.createProject('P')
    app.setProjectSource(project.id, { kind: 'memory', settings: {} })
    expect(() =>
      app.setProjectSettings(project.id, { tools: ['nope'] }, TOOL_SWITCH_NAMES),
    ).toThrow()
    app.setProjectSettings(project.id, { tools: ['fetch', 'commands'] }, TOOL_SWITCH_NAMES)
    const thread = app.createThread(project.id, { title: '调研' })
    const model = fakeModel(() => say(''))
    brief(project.id, thread.id)
    await start(model, { workspaces }).idle()
    const names = toolNames(model.requests.find((r) => system(r).includes('You are a thread'))!)
    expect(names).toContain('read_file')
    expect(names).not.toContain('fetch_url')
    expect(names).not.toContain('run_command')
  })

  test("the user's instructions reach both prompts; the coordinator reads the source's AGENTS.md", async () => {
    const { host } = await memoryHost({ 'AGENTS.md': '提交前跑 bun run check' })
    const workspaces = new WorkspaceManager(app, host)
    const project = app.createProject('P')
    app.setProjectSource(project.id, { kind: 'memory', settings: {} })
    app.setProjectSettings(project.id, { instructions: '只改 web 包' }, TOOL_SWITCH_NAMES)
    const thread = app.createThread(project.id, { title: '调研' })
    const model = fakeModel(() => say(''))
    brief(project.id, thread.id)
    app.postMessage(project.id, {
      conversationId: project.coordinatorId,
      author: 'user',
      content: '你好',
    })
    await start(model, { workspaces, settings: { instructions: '回答简短' } }).idle()
    const coordinator = model.requests.find((r) => system(r).includes('You are the coordinator'))!
    const threadRequest = model.requests.find((r) => system(r).includes('You are a thread'))!
    for (const request of [coordinator, threadRequest]) {
      expect(system(request)).toContain('回答简短')
      expect(system(request)).toContain('只改 web 包')
    }
    expect(system(coordinator)).toContain('提交前跑 bun run check')
    expect(app.getInstructions(project.coordinatorId)?.content).toBe('提交前跑 bun run check')
  })

  test('a new workspace gets a readable branch and runs the setup script once', async () => {
    const { host, ran, opened } = await memoryHost({})
    const workspaces = new WorkspaceManager(app, host, () => ({}), { env: () => ({ CI: '1' }) })
    const project = app.createProject('P')
    app.setProjectSource(project.id, { kind: 'memory', settings: {} })
    app.setProjectSettings(
      project.id,
      { workspace: { setup: 'bun install', shell: 'bash', branchPrefix: 'work/' } },
      TOOL_SWITCH_NAMES,
    )
    const thread = app.createThread(project.id, { title: '修导出', branch: 'Fix Safari export!' })
    const model = fakeModel((request, role) => {
      if (role === 'coordinator') return say('')
      const results = request.messages.filter((m) => m.role === 'tool').length
      if (results === 0) return [call('t1', 'read_file', { path: 'a.ts' })]
      if (results === 1) return [call('t2', 'run_command', { command: 'ls' })]
      return say('')
    })
    brief(project.id, thread.id)
    await start(model, { workspaces }).idle()
    expect(opened[0]?.branch).toBe(`work/fix-safari-export-${thread.id.slice(-6)}`)
    expect(ran.map((r) => [r.command, r.options.env])).toEqual([
      ['bun install', { CI: '1' }],
      ['ls', undefined],
    ])
    const results = toolResults(thread.id)
    expect(results[0]!.text).toContain("the project's setup script ran in it first")
    expect(results[1]!.text).not.toContain('setup script')
  })
})

describe('budgets and limits', () => {
  test('a thread over its budget stops and waits for the user', async () => {
    const project = app.createProject('P')
    app.setProjectSettings(
      project.id,
      { budget: { thread: 1000, project: null } },
      TOOL_SWITCH_NAMES,
    )
    const thread = app.createThread(project.id, { title: '调研' })
    let requests = 0
    const model = fakeModel((request, role) => {
      if (role === 'coordinator') return say('')
      requests++
      return [
        call(`t${requests}`, 'update_todo', { items: [{ label: '查', status: 'doing' }] }),
        { type: 'usage', input: 900, output: 200 },
        { type: 'finish', reason: 'tool-calls' },
      ]
    })
    brief(project.id, thread.id)
    await start(model).idle()
    // One step crosses the line; the turn stops before the next one.
    expect(requests).toBe(1)
    expect(app.getConversation(thread.id).lastError).toEqual({
      key: 'error.budgetThread',
      params: { used: '1', limit: '1' },
    })
    expect(app.getThread(thread.id).stage).toBe('blocked')
    // New input does not start it again while the budget stays.
    brief(project.id, thread.id, '继续')
    await scheduler!.idle()
    expect(requests).toBe(1)
  })

  test('the loop limit and the retry limit are the user’s', async () => {
    const project = app.createProject('P')
    const thread = app.createThread(project.id, { title: '转圈' })
    let failures = 0
    const model = fakeModel((_request, role) => {
      if (role === 'coordinator') return say('')
      if (failures < 1) {
        failures++
        return [{ type: 'finish', reason: 'error', retryable: true, error: 'overloaded' }]
      }
      return [call('t', 'list_threads', {}), { type: 'finish', reason: 'tool-calls' }]
    })
    brief(project.id, thread.id)
    await start(model, {
      settings: { limits: { ...DEFAULT_LIMITS, loopRepeats: 2, modelRetries: 0 } },
    }).idle()
    // No retries allowed: the first passing error fails the turn.
    expect(app.getConversation(thread.id).lastError?.key).toBe('error.modelRequest')
    app.requestTurn(thread.id)
    await scheduler!.idle()
    expect(app.getConversation(thread.id).lastError).toEqual({
      key: 'error.repeating',
      params: { count: 2 },
    })
  })
})
