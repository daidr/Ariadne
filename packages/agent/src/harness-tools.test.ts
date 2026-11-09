import { Ariadne, DomainError } from '@ariadne/core'
import { LocalWorkspace, run } from '@ariadne/plugin-local'
import { defaultShell, shellArgv, type ContextProvider, type Workspace } from '@ariadne/plugin-sdk'
import { mkdtemp, readFile, rm, writeFile, mkdir } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { BackgroundCommands, servedUrl } from './background'
import { applyEdit } from './edit-match'
import { globToRegExp } from './glob'
import { commandParts, commandVerdict, DEFAULT_LIMITS } from './limits'
import { PluginHost } from './plugins'
import { Scheduler } from './scheduler'
import { buildTrace, traceRequest } from './trace'
import { call, fakeModel, reminded, say } from './test-model'
import type { Tool, ToolContext } from './tool-helpers'
import { TOOL_SWITCH_NAMES } from './tools'
import { workspaceTools } from './workspace-tools'
import { WorkspaceManager } from './workspaces'

describe('edit matching', () => {
  test('replaces text as written, and refuses more than one place unless all', () => {
    expect(applyEdit('a b a', 'b', 'c', false)).toMatchObject({ ok: true, text: 'a c a' })
    expect(applyEdit('a b a', 'a', 'c', false)).toMatchObject({ ok: false })
    expect(applyEdit('a b a', 'a', 'c', true)).toMatchObject({ ok: true, text: 'c b c', count: 2 })
  })

  test('finds lines that differ only in whitespace, when there is one such place', () => {
    const file = 'function f() {\n\tif (x) {\r\n\t\treturn 1\n\t}\n}\n'
    const edit = applyEdit(
      file,
      '  if (x) {\n    return 1\n  }\n',
      '\tif (x) {\n\t\treturn 2\n\t}\n',
      false,
    )
    expect(edit).toEqual({
      ok: true,
      text: 'function f() {\n\tif (x) {\n\t\treturn 2\n\t}\n}\n',
      count: 1,
      loose: [{ from: 2, to: 4 }],
    })
    // Whitespace inside a line may be in a string: it has to match as written.
    expect(applyEdit('say("a  b")\n', 'say("a b")', 'x', false)).toMatchObject({ ok: false })
    const twice = 'x = 1\n  x = 1\n'
    expect(applyEdit(twice, '    x = 1', 'y', false)).toMatchObject({
      ok: false,
      reason: expect.stringContaining('2 times once whitespace is set aside'),
    })
  })

  test('shows the nearest lines and where they differ when nothing matches', () => {
    const file = [
      'import a',
      'const total = items.reduce((sum, i) => sum + i.price, 0)',
      'export {}',
    ].join('\n')
    const edit = applyEdit(
      file,
      'const total = items.reduce((sum, i) => sum + i.cost, 0)',
      'x',
      false,
    )
    expect(edit.ok).toBe(false)
    if (edit.ok) return
    expect(edit.reason).toContain('nearest text is at lines 2-2')
    expect(edit.reason).toContain('sum + i.price')
    expect(applyEdit(file, 'nothing like it at all, really', 'x', false)).toMatchObject({
      reason: expect.stringContaining('no lines are much like it'),
    })
  })
})

describe('globs', () => {
  test('match within and across directories, and names at any depth', () => {
    const ts = globToRegExp('*.ts')
    expect(ts.test('a.ts')).toBe(true)
    expect(ts.test('src/deep/a.ts')).toBe(true)
    expect(ts.test('a.tsx')).toBe(false)
    const src = globToRegExp('src/**/index.{ts,vue}')
    expect(src.test('src/index.ts')).toBe(true)
    expect(src.test('src/a/b/index.vue')).toBe(true)
    expect(src.test('lib/index.ts')).toBe(false)
    expect(globToRegExp('src/*.ts').test('src/a/b.ts')).toBe(false)
    expect(globToRegExp('file?.[ch]').test('x/file1.c')).toBe(true)
  })
})

describe('command rules', () => {
  const rules = [
    { pattern: 'git push*', action: 'ask' as const },
    { pattern: 'git push origin ariadne/*', action: 'allow' as const },
    { pattern: 'rm -rf /*', action: 'deny' as const },
    { pattern: 'ls*', action: 'allow' as const },
  ]

  test('a redirection is not a separator', () => {
    expect(commandParts('bun test 2>&1 | tail -5 &> log')).toEqual([
      'bun test 2>&1',
      'tail -5 &> log',
    ])
    expect(commandParts('(cd x && make) > out.txt')).toEqual(['cd x', 'make', '> out.txt'])
  })

  test('a part is matched as what it runs', () => {
    expect(commandVerdict('FOO=1 git -C . push --force', rules)).toEqual({
      action: 'ask',
      pattern: 'git push*',
    })
    expect(commandVerdict('(cd x && git push)', rules).action).toBe('ask')
    expect(commandVerdict('env -i A=1 /usr/bin/git --no-pager -c a=b push x', rules).action).toBe(
      'ask',
    )
    expect(commandVerdict('git -C repo push origin ariadne/x', rules).action).toBe('allow')
    expect(commandVerdict('sudo rm -rf /x', rules).action).toBe('deny')
    expect(commandVerdict('echo "$(git push)"', rules).action).toBe('ask')
  })

  test("a shell's command line is split into its own parts", () => {
    expect(commandVerdict("bash -c 'ls; git push'", rules).action).toBe('ask')
    expect(commandVerdict('sh -ec "rm -rf /"', rules).action).toBe('deny')
    expect(commandVerdict('pwsh -NoProfile -Command "git push"', rules).action).toBe('ask')
    expect(commandVerdict('cmd /c git push', rules).action).toBe('ask')
  })

  test('a command run by another command is still matched', () => {
    const asked = [
      'if true; then git push; fi',
      'for i in 1; do git push; done',
      '{ git push; }',
      '! git push',
      'eval git push',
      'eval "git push"',
      'echo x | xargs git push',
      'find . -exec git push \\;',
      'timeout 10 git push',
      'watch git push',
      'stdbuf -o0 git push',
      'source <(echo git push)',
      'sudo -u root git push',
      'env -u HOME git push',
      'nice -n 5 git push',
      'time -p git push',
      'command -p git push',
      'exec -a x git push',
      'bash -ce "git push"',
      'bash -c -- "git push"',
      'pwsh -Com "git push"',
      'g`i`t push',
      'iex "g`it push"',
      `pwsh -enc ${Buffer.from('git push', 'utf16le').toString('base64')}`,
      'timeout 10 git -C repo push',
    ]
    for (const command of asked) {
      expect([command, commandVerdict(command, rules).action]).toEqual([command, 'ask'])
    }
    for (const command of ['then rm -rf /', 'xargs rm -rf /x', 'eval "rm -rf /x"']) {
      expect([command, commandVerdict(command, rules).action]).toEqual([command, 'deny'])
    }
  })

  test('an allow lifts an ask only as far as it spells the command out', () => {
    const wide = [
      { pattern: 'git push*', action: 'ask' as const },
      { pattern: 'timeout *', action: 'allow' as const },
      { pattern: 'timeout 10 git push', action: 'allow' as const },
    ]
    expect(commandVerdict('timeout 5 git push', wide).action).toBe('ask')
    expect(commandVerdict('timeout 5 make', wide).action).toBe('allow')
    expect(commandVerdict('timeout 10 git push', wide).action).toBe('allow')
    // A deny is never lifted.
    expect(
      commandVerdict('timeout 5 rm -rf /', [
        { pattern: 'rm -rf /*', action: 'deny' },
        { pattern: 'timeout*', action: 'allow' },
      ]).action,
    ).toBe('deny')
  })

  test('an allow on one part does not lift an ask on another', () => {
    expect(commandVerdict('ls && git push --force', rules)).toEqual({
      action: 'ask',
      pattern: 'git push*',
    })
    expect(commandVerdict('ls -la', rules)).toEqual({ action: 'allow', pattern: 'ls*' })
  })
})

describe('shells', () => {
  test('Windows runs PowerShell 7 when it is there, else 5.1; elsewhere bash', () => {
    expect(defaultShell('linux', () => null).name).toBe('bash')
    expect(defaultShell('win32', (p) => (p === 'pwsh' ? 'C:/pwsh.exe' : null)).name).toBe('pwsh')
    expect(defaultShell('win32', () => null)).toEqual({
      name: 'powershell',
      argv: ['powershell', '-NoProfile', '-NonInteractive', '-Command'],
    })
  })

  test('PowerShell gets its command encoded, other shells as it is', () => {
    expect(shellArgv(['bash', '-c'], 'echo "hi"')).toEqual(['bash', '-c', 'echo "hi"'])
    const argv = shellArgv(['pwsh', '-NoProfile', '-NonInteractive', '-Command'], 'echo "a b"')
    expect(argv.slice(0, 4)).toEqual(['pwsh', '-NoProfile', '-NonInteractive', '-EncodedCommand'])
    expect(Buffer.from(argv[4]!, 'base64').toString('utf16le')).toMatch(
      /UTF8Encoding.*\necho "a b"$/s,
    )
  })
})

let dir: string
let app: Ariadne

beforeEach(async () => {
  dir = await mkdtemp(join(tmpdir(), 'ariadne-tools-'))
  app = new Ariadne({ path: ':memory:' })
})

afterEach(async () => {
  app.close()
  await rm(dir, { recursive: true, force: true })
})

const tool = (name: string) => workspaceTools.find((t) => t.spec.name === name) as Tool

function context(workspace: Workspace, extra: Partial<ToolContext> = {}): ToolContext {
  return {
    app,
    projectId: 'p',
    conversationId: `thr_${Math.random().toString(36).slice(2)}`,
    role: 'thread',
    locale: 'zh-CN',
    signal: new AbortController().signal,
    workspace: async () => workspace,
    repo: () => Promise.reject(new Error('no repo')),
    limits: { ...DEFAULT_LIMITS },
    commands: { rules: [], shell: ['bash', '-c'], shellName: 'bash', env: {} },
    background: new BackgroundCommands(),
    ...extra,
  }
}

describe('file tools', () => {
  test('edit_file makes several edits at once, or none', async () => {
    await writeFile(join(dir, 'a.ts'), 'const a = 1\nconst b = 2\n')
    const ws = new LocalWorkspace('w', dir, null)
    const ctx = context(ws)
    const edit = tool('edit_file')
    const both = await edit.run(
      {
        path: 'a.ts',
        edits: [
          { old: 'const a = 1', new: 'const a = 10' },
          { old: '  const b = 2  ', new: 'const b = 20' },
        ],
      },
      ctx,
    )
    expect(both.content).toContain('Replaced 2 occurrences')
    expect(both.content).toContain('Edit 2 of 2: matched with other whitespace at lines 2-2')
    expect(await readFile(join(dir, 'a.ts'), 'utf8')).toBe('const a = 10\nconst b = 20\n')
    const failed = await edit.run(
      {
        path: 'a.ts',
        edits: [
          { old: 'const a = 10', new: 'x' },
          { old: 'missing', new: 'y' },
        ],
      },
      ctx,
    )
    expect(failed).toMatchObject({ isError: true })
    expect(failed.content).toContain('Edit 2 of 2')
    expect(failed.content).toContain('None of the edits was made')
    expect(await readFile(join(dir, 'a.ts'), 'utf8')).toBe('const a = 10\nconst b = 20\n')
  })

  test('write_file refuses to overwrite a file that changed since it was read', async () => {
    await writeFile(join(dir, 'notes.md'), 'first\n')
    const ws = new LocalWorkspace('w', dir, null)
    const ctx = context(ws)
    await tool('read_file').run({ path: 'notes.md' }, ctx)
    // The user changes it in their editor meanwhile.
    await writeFile(join(dir, 'notes.md'), 'the user wrote this\n')
    const refused = await tool('write_file').run({ path: 'notes.md', content: 'mine\n' }, ctx)
    expect(refused).toMatchObject({ isError: true })
    expect(await readFile(join(dir, 'notes.md'), 'utf8')).toBe('the user wrote this\n')
    await tool('read_file').run({ path: 'notes.md' }, ctx)
    expect(
      (await tool('write_file').run({ path: 'notes.md', content: 'mine\n' }, ctx)).content,
    ).toBe('Written.')
    // A new file, and one never read, are written as before.
    expect(
      (await tool('write_file').run({ path: 'new.md', content: 'x' }, ctx)).isError,
    ).toBeFalsy()
  })

  test("the project's check runs after an edit, with the changed file", async () => {
    await writeFile(join(dir, 'a.txt'), 'ok\n')
    const ws = new LocalWorkspace('w', dir, null)
    const ctx = context(ws, {
      commands: {
        rules: [],
        shell: ['bash', '-c'],
        shellName: 'bash',
        env: {},
        check: 'grep -q ok {file} || { echo "no ok in" {file}; exit 2; }',
      },
    })
    const passed = await tool('edit_file').run({ path: 'a.txt', old: 'ok', new: 'ok!' }, ctx)
    expect(passed.content).toContain("check after edits passed: grep -q ok 'a.txt'")
    const failed = await tool('edit_file').run({ path: 'a.txt', old: 'ok!', new: 'bad' }, ctx)
    expect(failed.isError).toBeFalsy()
    expect(failed.content).toContain('check after edits failed (exit code 2)')
    expect(failed.content).toContain('no ok in a.txt')
  })

  test('find_files and search leave out what git ignores', async () => {
    const git = (...args: string[]) => run(['git', ...args], { cwd: dir })
    await git('init', '-q')
    await mkdir(join(dir, 'src', 'deep'), { recursive: true })
    await mkdir(join(dir, 'dist'))
    await writeFile(join(dir, '.gitignore'), 'dist/\n')
    await writeFile(join(dir, 'src', 'a.ts'), 'export const needle = 1\n')
    await writeFile(join(dir, 'src', 'deep', 'b.ts'), 'needle\n')
    await writeFile(join(dir, 'dist', 'a.ts'), 'needle\n')
    await git('add', 'src/a.ts')
    const ws = new LocalWorkspace('w', dir, null)
    const ctx = context(ws)
    const found = await tool('find_files').run({ pattern: '*.ts' }, ctx)
    expect(found.content).toBe('src/a.ts\nsrc/deep/b.ts')
    expect((await tool('find_files').run({ pattern: '*.rs' }, ctx)).content).toBe('No files found.')
    const hits = await tool('search').run({ pattern: 'need+le' }, ctx)
    expect(hits.content.split('\n').toSorted()).toEqual([
      'src/a.ts:1:export const needle = 1',
      'src/deep/b.ts:1:needle',
    ])
    expect((await tool('search').run({ pattern: 'nothing-here' }, ctx)).content).toBe('No matches.')
  })

  test('find_files walks a directory that is no checkout', async () => {
    await mkdir(join(dir, 'node_modules', 'x'), { recursive: true })
    await mkdir(join(dir, 'a'))
    await writeFile(join(dir, 'a', 'one.vue'), '')
    await writeFile(join(dir, 'node_modules', 'x', 'two.vue'), '')
    const ws = new LocalWorkspace('w', dir, null)
    // As on a machine with neither git nor ripgrep.
    const bare: Workspace = {
      id: 'w',
      label: dir,
      branch: null,
      base: null,
      writeFile: (path, content) => ws.writeFile(path, content),
      dispose: async () => {},
      list: (path) => ws.list(path),
      readFile: (path) => ws.readFile(path),
      readBytes: (path, max) => ws.readBytes(path, max),
      execFile: async () => ({ exitCode: 127, stdout: '', stderr: 'not found' }),
      exec: async () => ({ exitCode: 127, stdout: '', stderr: 'not found' }),
    }
    const ctx = context(bare)
    expect((await tool('find_files').run({ pattern: '**/*.vue' }, ctx)).content).toBe('a/one.vue')
  })
})

describe('background commands', () => {
  test('the address a server prints is found once it is whole', () => {
    expect(servedUrl('  \x1b[32m➜\x1b[39m  Local:   http://localhost:\x1b[1m5173\x1b[22m/\n')).toBe(
      'http://localhost:5173/',
    )
    expect(servedUrl('Listening on http://0.0.0.0:3000 (press Ctrl+C)\n')).toBe(
      'http://localhost:3000',
    )
    expect(servedUrl('open http://127.0.0.1:8080/docs/.\n')).toBe('http://127.0.0.1:8080/docs/')
    // The line that says it serves wins over an address merely named before it.
    expect(
      servedUrl('proxy /api -> http://localhost:8787\n  Local:   http://localhost:5173/\n'),
    ).toBe('http://localhost:5173/')
    // Still arriving: the port may not be all there yet.
    expect(servedUrl('Local: http://localhost:51')).toBeNull()
    expect(servedUrl('fetching https://example.com:443/x \n')).toBeNull()
  })

  test('a server started on this machine shows its address', async () => {
    const ws = new LocalWorkspace('w', dir, null)
    const ctx = context(ws)
    await tool('run_command').run(
      { command: 'echo "Local: http://localhost:4321/"; sleep 30', background: true },
      ctx,
    )
    expect(ctx.background!.list(ctx.conversationId)).toMatchObject([
      { url: 'http://localhost:4321/' },
    ])
    await ctx.background!.stopThread(ctx.conversationId)
  })

  test('run on, show their output, and stop', async () => {
    const ws = new LocalWorkspace('w', dir, null)
    const ctx = context(ws)
    const started = await tool('run_command').run(
      { command: 'echo ready; while true; do sleep 0.1; done', background: true },
      ctx,
    )
    expect(started.content).toContain('cmd-1 runs in the background')
    expect(started.content).toContain('ready')
    const nothing = await tool('read_command_output').run({ handle: 'cmd-1' }, ctx)
    expect(nothing.content).toContain('cmd-1 is still running')
    expect(nothing.content).toContain('(no new output)')
    const stopped = await tool('stop_command').run({ handle: 'cmd-1' }, ctx)
    expect(stopped.content).toContain('cmd-1 was stopped')
    expect((await tool('read_command_output').run({ handle: 'cmd-9' }, ctx)).content).toContain(
      'cmd-1 (ended)',
    )
  })

  test('started again, the same command replaces the one still running', async () => {
    const ws = new LocalWorkspace('w', dir, null)
    const ctx = context(ws)
    const command = 'echo "Local: http://localhost:4321/"; sleep 30'
    await tool('run_command').run({ command, background: true }, ctx)
    await tool('run_command').run({ command: 'sleep 30', background: true }, ctx)
    const again = await tool('run_command').run({ command, background: true }, ctx)
    expect(again.content).toContain('cmd-3 runs in the background')
    expect(again.content).toContain('[cmd-1 ran the same command and was ended first]')
    expect(
      ctx
        .background!.list(ctx.conversationId)
        .filter((c) => !c.result)
        .map((c) => c.id),
    ).toEqual(['cmd-2', 'cmd-3'])
    await ctx.background!.stopThread(ctx.conversationId)
  })

  test('one that ends says how; another thread cannot read it', async () => {
    const ws = new LocalWorkspace('w', dir, null)
    const ctx = context(ws)
    await tool('run_command').run(
      { command: 'sleep 0.3; echo done; exit 3', background: true },
      ctx,
    )
    const read = await tool('read_command_output').run({ handle: 'cmd-1', waitSeconds: 5 }, ctx)
    // Output may come before the end; wait once more for the end itself.
    const end = read.content.includes('exit code 3')
      ? read
      : await tool('read_command_output').run({ handle: 'cmd-1', waitSeconds: 5 }, ctx)
    expect(end.content).toContain('cmd-1 ended with exit code 3')
    const other = { ...ctx, conversationId: 'thr_other' }
    expect((await tool('read_command_output').run({ handle: 'cmd-1' }, other)).isError).toBe(true)
  })
})

/** A source whose workspace's setup script waits until it is stopped. */
async function slowSetupHost(calls: { setup: number }) {
  const workspace: Workspace = {
    id: 'w',
    label: '/work',
    branch: null,
    base: null,
    readFile: async () => '',
    readBytes: async () => new Uint8Array(),
    writeFile: async () => {},
    list: async () => [],
    exec: (command, options = {}) => {
      calls.setup++
      return new Promise((resolve) => {
        options.signal?.addEventListener('abort', () =>
          resolve({ exitCode: 130, stdout: '', stderr: 'stopped' }),
        )
      })
    },
    dispose: async () => {},
  }
  const provider: ContextProvider<never> = {
    kind: 'memory',
    title: { 'zh-CN': '内存' },
    settings: { type: 'object', properties: {} },
    provides: ['workspace'],
    connect: async () => ({ workspace: { isolated: true, open: async () => workspace } }),
  }
  const host = new PluginHost('/tmp/ariadne-test-plugins')
  await host.load({
    id: 'memory',
    name: { 'zh-CN': '内存' },
    setup: (ctx) => ctx.registerContextProvider(provider),
  })
  return host
}

describe('setup script', () => {
  test("stops with the thread's turn, and runs again next time", async () => {
    const calls = { setup: 0 }
    const workspaces = new WorkspaceManager(app, await slowSetupHost(calls))
    const project = app.createProject('P')
    app.setProjectSource(project.id, { kind: 'memory', settings: {} })
    app.setProjectSettings(
      project.id,
      { workspace: { setup: 'bun install', shell: 'auto', check: '', branchPrefix: 'ariadne/' } },
      TOOL_SWITCH_NAMES,
    )
    const thread = app.createThread(project.id, { title: 'x' })
    const turn = new AbortController()
    const opening = workspaces.forThread(project.id, thread.id, turn.signal)
    await new Promise((resolve) => setTimeout(resolve, 20))
    turn.abort('cancelled')
    await expect(opening).rejects.toThrow(DomainError)
    expect(app.getThread(thread.id).workspace).toBeNull()
    // Another thread's setup is not stopped by it.
    const next = workspaces.forThread(project.id, thread.id)
    await new Promise((resolve) => setTimeout(resolve, 20))
    expect(calls.setup).toBe(2)
    await workspaces.stopThread(thread.id)
    await expect(next).rejects.toThrow(DomainError)
  })
})

describe('research', () => {
  test('runs in a context of its own, and the trace shows it under the call', async () => {
    await writeFile(join(dir, 'README.md'), '# hi\n')
    const provider: ContextProvider<never> = {
      kind: 'dir',
      title: { 'zh-CN': '目录' },
      settings: { type: 'object', properties: {} },
      provides: ['workspace'],
      connect: async () => ({
        workspace: { isolated: false, open: async () => new LocalWorkspace('w', dir, null) },
      }),
    }
    const host = new PluginHost('/tmp/ariadne-test-plugins')
    await host.load({
      id: 'dir',
      name: { 'zh-CN': '目录' },
      setup: (ctx) => ctx.registerContextProvider(provider),
    })
    const workspaces = new WorkspaceManager(app, host)
    const project = app.createProject('P')
    app.setProjectSource(project.id, { kind: 'dir', settings: {} })
    const thread = app.createThread(project.id, { title: '调研' })
    const researcher = fakeModel((request) => {
      if (request.messages.at(-1)?.role === 'tool') return say('README 只有标题。')
      return [
        call('r1', 'read_file', { path: 'README.md' }),
        { type: 'finish', reason: 'tool-calls' },
      ]
    })
    const model = fakeModel((request, role) => {
      if (role === 'coordinator' || reminded(request)) return say('')
      if (request.messages.at(-1)?.role === 'tool') return say('好')
      return [
        call('t1', 'research', { question: 'README 写了什么？' }),
        { type: 'finish', reason: 'tool-calls' },
      ]
    })
    app.postMessage(project.id, {
      conversationId: thread.id,
      kind: 'relay',
      author: 'coordinator',
      content: '看看',
    })
    const scheduler = new Scheduler({
      app,
      workspaces,
      resolveModel: () => model,
      researchModel: () => researcher,
      locale: () => 'zh-CN',
      retryBaseMs: 1,
    })
    scheduler.start()
    await scheduler.idle()
    await scheduler.stop()

    // The research's own requests: the question, then its look, with only looking tools.
    expect(researcher.requests).toHaveLength(2)
    const tools = researcher.requests[0]!.tools.map((t) => t.name)
    expect(tools).toContain('read_file')
    expect(tools).not.toContain('write_file')
    expect(tools).not.toContain('research')
    expect(researcher.requests[1]!.messages.at(-1)?.content).toContain('# hi')
    // The thread sees the answer alone.
    const result = app
      .listTranscript(thread.id)
      .find((e) => e.kind === 'tool' && e.name === 'research')
    expect(result?.kind === 'tool' && result.content).toContain('README 只有标题。')
    expect(JSON.stringify(model.requests.at(-1)!.messages)).not.toContain('# hi')
    // Its tokens count toward the thread's: one answer of each, and the turn it was told
    // to report in, as it ended without a report.
    expect(app.getConversation(thread.id).usage.input).toBe(30)

    const { turns } = buildTrace(app, thread.id)
    const step = turns[0]!.items.find((item) => item.kind === 'request')
    const research = step?.kind === 'request' ? step.toolCalls[0]?.research : undefined
    expect(research?.map((r) => [r.purpose, r.number, r.toolCalls.map((c) => c.name)])).toEqual([
      ['research', 1, ['read_file']],
      ['research', 2, []],
    ])
    expect(research?.[0]?.toolCalls[0]?.result?.content).toContain('# hi')
    const detail = traceRequest(app, thread.id, research![1]!.seq).detail
    expect(detail.messages.map((m) => m.role)).toEqual(['system', 'user', 'assistant', 'tool'])
    expect(detail.newFrom).toBe(2)
  })
})

describe('a silent request', () => {
  test('is not given up while the service keeps sending anything', async () => {
    const project = app.createProject('P')
    const model = fakeModel(() => [])
    let attempts = 0
    model.request = { timeoutSeconds: 1 }
    model.provider = {
      ...model.provider,
      async *stream(_settings, request) {
        attempts++
        // Thinking for 1.6 seconds, with a sign of life every 0.2: longer than the timeout.
        for (let i = 0; i < 8; i++) {
          await new Promise((resolve) => setTimeout(resolve, 200))
          if (request.signal?.aborted) return
          yield { type: 'alive' }
        }
        yield* say('想好了')
      },
    }
    const scheduler = new Scheduler({
      app,
      resolveModel: () => model,
      locale: () => 'zh-CN',
      retryBaseMs: 1,
    })
    app.postMessage(project.id, {
      conversationId: project.coordinatorId,
      author: 'user',
      content: '在吗',
    })
    scheduler.start()
    await scheduler.idle()
    await scheduler.stop()
    expect(attempts).toBe(1)
    expect(app.listMessages(project.coordinatorId).at(-1)?.content).toBe('想好了')
  })
})
