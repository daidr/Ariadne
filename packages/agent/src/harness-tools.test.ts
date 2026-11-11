import { Ariadne } from '@ariadne/core'
import { LocalWorkspace, run } from '@ariadne/plugin-local'
import { defaultShell, shellArgv, type ContextProvider, type Workspace } from '@ariadne/plugin-sdk'
import { mkdtemp, readFile, rm, writeFile, mkdir } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { BackgroundCommands, servedUrl } from './background'
import { applyEdit } from './edit-match'
import { globToRegExp } from './glob'
import { commandParts, commandVerdict, DEFAULT_COMMAND_RULES, DEFAULT_LIMITS } from './limits'
import { PluginHost } from './plugins'
import { Scheduler } from './scheduler'
import { buildTrace, traceRequest } from './trace'
import { call, fakeModel, reminded, say, plain } from './test-model'
import type { Tool, ToolContext } from './tool-helpers'
import { TOOL_SWITCH_NAMES } from './tools'
import { forgetReads } from './file-state'
import { applyPatch, plainOutput, workspaceTools } from './workspace-tools'
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

  test('text a command only prints or looks for is not asked about, but still denied', () => {
    const mentioned = [
      'git commit -m "docs: explain the git push flow"',
      'git log --grep "git push"',
      'rg -n "git push" docs',
      'cat notes.md | grep "git push"',
      'echo git push',
      'printf "%s\\n" "git push"',
    ]
    for (const command of mentioned) {
      expect([command, commandVerdict(command, rules).action]).toEqual([command, 'allow'])
    }
    // Something on the line runs text, or the phrase is run after all.
    const ran = [
      'echo "git push" | sh',
      'echo "git push" | timeout 5 sh',
      'echo "git push" | nice -n 5 bash',
      'echo "git push" | busybox sh',
      'echo git push | xargs -I{} bash -c {}',
      'echo "$(git push)"',
      'git commit -m x && git push',
      'git rebase --exec "git push" main',
    ]
    for (const command of ran) {
      expect([command, commandVerdict(command, rules).action]).toEqual([command, 'ask'])
    }
    expect(commandVerdict('echo "rm -rf /x"', rules).action).toBe('deny')
  })

  test('the default rules ask before pushing, publishing, merging and releasing', () => {
    const asked = [
      'git push',
      'git -C . push origin +main',
      'cd pkg && bun publish --access public',
      'sudo npm publish',
      'npx npm publish',
      'pnpm publish --no-git-checks',
      'yarn publish',
      'cargo publish',
      'gh pr merge 12 --squash',
      'gh release create v1.0.0',
    ]
    for (const command of asked) {
      expect([command, commandVerdict(command, DEFAULT_COMMAND_RULES).action]).toEqual([
        command,
        'ask',
      ])
    }
    const free = [
      'git stash push -m wip',
      'git commit -m "ci: npm publish with provenance"',
      'gh pr view 12',
      'gh release list',
      'bun test',
      'rg "gh pr merge" .',
    ]
    for (const command of free) {
      expect([command, commandVerdict(command, DEFAULT_COMMAND_RULES).action]).toEqual([
        command,
        'allow',
      ])
    }
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
  test('apply_patch changes several files at once, or none', async () => {
    await writeFile(join(dir, 'a.ts'), 'const a = 1\nconst b = 2\n')
    await writeFile(join(dir, 'gone.ts'), 'x\n')
    const ws = new LocalWorkspace('w', dir, null)
    const ctx = context(ws)
    const failed = await applyPatch.run(
      {
        input:
          '*** Begin Patch\n*** Add File: b.ts\n+new\n*** Update File: a.ts\n-const c = 3\n+const c = 4\n*** End Patch',
      },
      ctx,
    )
    expect(failed.isError).toBe(true)
    expect(failed.content).toContain('Nothing was changed.')
    await expect(readFile(join(dir, 'b.ts'), 'utf8')).rejects.toThrow()
    const done = await applyPatch.run(
      {
        input:
          '*** Begin Patch\n*** Add File: sub/b.ts\n+new\n*** Update File: a.ts\n*** Move to: c.ts\n const a = 1\n-const b = 2\n+const b = 3\n*** Delete File: gone.ts\n*** End Patch',
      },
      ctx,
    )
    expect(done.isError).toBeFalsy()
    expect(done.content).toBe('Done:\nA sub/b.ts\nR a.ts -> c.ts\nD gone.ts')
    expect(await readFile(join(dir, 'sub/b.ts'), 'utf8')).toBe('new\n')
    expect(await readFile(join(dir, 'c.ts'), 'utf8')).toBe('const a = 1\nconst b = 3\n')
    await expect(readFile(join(dir, 'a.ts'), 'utf8')).rejects.toThrow()
    await expect(readFile(join(dir, 'gone.ts'), 'utf8')).rejects.toThrow()
    const outside = await applyPatch.run(
      { input: '*** Begin Patch\n*** Delete File: ../x\n*** End Patch' },
      ctx,
    )
    expect(outside.isError).toBe(true)
    // A move out of the workspace is refused before anything is written.
    await writeFile(join(dir, 'd.ts'), 'one\n')
    const moved = await applyPatch.run(
      {
        input:
          '*** Begin Patch\n*** Add File: e.ts\n+e\n*** Update File: d.ts\n*** Move to: ../d.ts\n-one\n+two\n*** End Patch',
      },
      ctx,
    )
    expect(moved.content).toContain('Nothing was changed.')
    await expect(readFile(join(dir, 'e.ts'), 'utf8')).rejects.toThrow()
    // A delete that fails part way says what was already done, as a result of the call.
    const busy = Object.assign(Object.create(ws) as Workspace, {
      exec: async () => ({ exitCode: 1, stdout: '', stderr: 'busy' }),
    })
    const stuck = await applyPatch.run(
      { input: '*** Begin Patch\n*** Add File: f.ts\n+f\n*** Delete File: d.ts\n*** End Patch' },
      { ...ctx, workspace: async () => busy },
    )
    expect(stuck.isError).toBe(true)
    expect(stuck.content).toContain('d.ts could not be deleted: busy')
    expect(stuck.content).toContain('Already done: wrote f.ts')
  })

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

  test('a check that fails as it did after the previous edit says so in a line', async () => {
    await writeFile(join(dir, 'a.txt'), 'one\n')
    await writeFile(join(dir, 'errors.log'), 'old error elsewhere\n')
    const ws = new LocalWorkspace('w', dir, null)
    const ctx = context(ws, {
      commands: {
        rules: [],
        shell: ['bash', '-c'],
        shellName: 'bash',
        env: {},
        check: 'cat errors.log; exit 1',
      },
      keepsResults: () => true,
    })
    const edit = tool('edit_file')
    const first = await edit.run({ path: 'a.txt', old: 'one', new: 'two' }, ctx)
    expect(first.content).toContain('old error elsewhere')
    const second = await edit.run({ path: 'a.txt', old: 'two', new: 'three' }, ctx)
    expect(second.content).toContain('with the same output as after your previous edit')
    expect(second.content).not.toContain('old error elsewhere')
    // Once its results are cleared, the model is shown the output again.
    forgetReads(ctx.conversationId)
    const third = await edit.run({ path: 'a.txt', old: 'three', new: 'four' }, ctx)
    expect(third.content).toContain('old error elsewhere')
    // An agent program cuts its own history: it always gets the output.
    const program = { ...ctx, conversationId: `${ctx.conversationId}-p`, keepsResults: () => false }
    await edit.run({ path: 'a.txt', old: 'four', new: 'five' }, program)
    const again = await edit.run({ path: 'a.txt', old: 'five', new: 'six' }, program)
    expect(again.content).toContain('old error elsewhere')
  })

  test('a loose match shows the lines it wrote', async () => {
    await writeFile(join(dir, 'a.ts'), 'function f() {\n\tif (x) {\n\t\treturn 1\n\t}\n}\n')
    const ws = new LocalWorkspace('w', dir, null)
    const done = await tool('edit_file').run(
      { path: 'a.ts', old: '  if (x) {\n    return 1', new: '\tif (y) {\n\t\treturn 2' },
      context(ws),
    )
    expect(done.content).toBe(
      'Replaced one occurrence. (matched with other whitespace; the lines now read:)\n    2\t\tif (y) {\n    3\t\t\treturn 2',
    )
  })

  test('read_file hands back a line for lines the model still has, unchanged', async () => {
    await writeFile(join(dir, 'a.ts'), 'const a = 1\n')
    const ws = new LocalWorkspace('w', dir, null)
    const ctx = context(ws, { keepsResults: () => true })
    const read = tool('read_file')
    expect((await read.run({ path: 'a.ts' }, ctx)).content).toBe('    1\tconst a = 1\n    2\t')
    expect((await read.run({ path: 'a.ts' }, ctx)).content).toContain(
      'is unchanged since you read it',
    )
    // Another range is read as asked.
    expect((await read.run({ path: 'a.ts', limit: 1 }, ctx)).content).toBe(
      '    1\tconst a = 1\n[lines 1-1 of 2; read on with offset 2]',
    )
    await writeFile(join(dir, 'a.ts'), 'const a = 2\n')
    expect((await read.run({ path: 'a.ts' }, ctx)).content).toBe('    1\tconst a = 2\n    2\t')
    // After a clearing the model no longer has it.
    forgetReads(ctx.conversationId)
    expect((await read.run({ path: 'a.ts' }, ctx)).content).toBe('    1\tconst a = 2\n    2\t')
    // An agent program keeps its own history: always the text.
    const program = context(ws, { keepsResults: () => false })
    await read.run({ path: 'a.ts' }, program)
    expect((await read.run({ path: 'a.ts' }, program)).content).toBe('    1\tconst a = 2\n    2\t')
  })

  test('a command still running after its wait goes on in the background', async () => {
    const ws = new LocalWorkspace('w', dir, null)
    const ctx = context(ws)
    const ran = await tool('run_command').run(
      { command: 'echo started; sleep 2; echo finished', timeoutSeconds: 1 },
      ctx,
    )
    expect(ran.isError).toBeFalsy()
    expect(ran.content).toContain('It still ran after 1 seconds, so it goes on in the background')
    expect(ran.content).toContain('cmd-')
    expect(ran.content).toContain('started')
    const id = /cmd-\d+/.exec(ran.content)![0]
    const read = workspaceTools.find((t) => t.spec.name === 'read_command_output')!
    let seen = ''
    for (let i = 0; i < 4 && !seen.includes('ended'); i++) {
      seen += (await read.run({ handle: id, waitSeconds: 5 }, ctx)).content
    }
    expect(seen).toContain('ended with exit code 0')
    expect(seen).toContain('finished')
    expect(seen).not.toContain('started')
  })

  test('command output loses colour codes and redrawn progress', async () => {
    expect(
      plainOutput(
        '\x1b[32m✓\x1b[0m ok\r\nload 10%\rload 100%\n\x1b]8;;https://x\x07link\x1b]8;;\x07\n',
      ),
    ).toBe('✓ ok\nload 100%\nlink\n')
    const ws = new LocalWorkspace('w', dir, null)
    const ran = await tool('run_command').run(
      { command: "printf '\\033[31mred\\033[0m\\n50%%\\r100%%\\n'" },
      context(ws),
    )
    expect(ran.content).toBe('exit code 0\n--- stdout ---\nred\n100%\n')
  })

  test('search finds hidden files, narrows by glob and case, and cuts long lines', async () => {
    const git = (...args: string[]) => run(['git', ...args], { cwd: dir })
    await git('init', '-q')
    await mkdir(join(dir, '.github'))
    await mkdir(join(dir, 'src'))
    await writeFile(join(dir, '.github', 'ci.yml'), 'run: bun test\n')
    await writeFile(join(dir, 'src', 'a.vue'), '    <Test />\n')
    await writeFile(join(dir, 'src', 'a.ts'), `const t = '${'x'.repeat(400)} test'\n`)
    const ctx = context(new LocalWorkspace('w', dir, null))
    const search = tool('search')
    const all = (await search.run({ pattern: 'test' }, ctx)).content
    expect(all).toContain('.github/ci.yml\n  1: run: bun test')
    expect(all).toContain('[… 117 characters cut]')
    expect(all).not.toContain('a.vue')
    expect(
      (await search.run({ pattern: 'test', glob: '*.vue', ignoreCase: true }, ctx)).content,
    ).toBe('src/a.vue\n  1: <Test />')
    expect((await search.run({ pattern: 'test', glob: 'src/**/*.ts' }, ctx)).content).toMatch(
      /^src\/a\.ts\n {2}1: /,
    )
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
    expect(hits.content).toBe('src/a.ts\n  1: export const needle = 1\nsrc/deep/b.ts\n  1: needle')
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
    await tool('run_command').run({ command, background: true, keepForUser: true }, ctx)
    await tool('run_command').run({ command: 'sleep 30', background: true }, ctx)
    // The same words somewhere else are another command.
    await mkdir(join(dir, 'docs'), { recursive: true })
    await tool('run_command').run({ command, background: true, cwd: 'docs' }, ctx)
    // Another spelling of the same directory is the same one.
    const again = await tool('run_command').run({ command, background: true, cwd: './' }, ctx)
    expect(again.content).toContain('cmd-4 runs in the background')
    expect(again.content).toContain('[cmd-1 ran the same command and was ended first]')
    expect(
      ctx
        .background!.list(ctx.conversationId)
        .filter((c) => !c.result)
        .map((c) => c.id),
    ).toEqual(['cmd-2', 'cmd-3', 'cmd-4'])
    // What the user asked to try is still theirs to try after the restart.
    await ctx.background!.stopThread(ctx.conversationId, { keepAsked: true })
    expect(ctx.background!.list(ctx.conversationId).map((c) => c.id)).toEqual(['cmd-4'])
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
  test('runs in the background; stopped, it is told of and not run again', async () => {
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
    // Handed out while the setup still runs.
    await workspaces.forThread(project.id, thread.id)
    expect(calls.setup).toBe(1)
    const [running] = workspaces.background.list(thread.id)
    expect(running).toMatchObject({ command: 'bun install', result: null })
    expect(workspaces.takeSetup(thread.id)).toContain(`in the background as ${running!.id}`)
    // A command waits for it, until its turn is stopped.
    const turn = new AbortController()
    const waiting = workspaces.setupDone(thread.id, turn.signal)
    turn.abort('cancelled')
    expect(await waiting).toBeNull()
    await workspaces.stopThread(thread.id)
    expect(workspaces.takeSetup(thread.id)).toContain('was stopped before it finished')
    await workspaces.forThread(project.id, thread.id)
    expect(calls.setup).toBe(1)
    expect(await workspaces.setupDone(thread.id, new AbortController().signal)).toBeNull()
  })
})

describe('editing tools by model', () => {
  test('GPT-family models edit with apply_patch, the others with edit_file and write_file', async () => {
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
    const toolsOf = async (name: string) => {
      const project = app.createProject(name)
      app.setProjectSource(project.id, { kind: 'dir', settings: {} })
      const thread = app.createThread(project.id, { title: '改文件' })
      const model = {
        ...fakeModel((_request, role) => (role === 'coordinator' ? say('') : say('好'))),
        model: name,
      }
      app.postMessage(project.id, {
        conversationId: thread.id,
        kind: 'relay',
        author: 'coordinator',
        content: '改一下',
      })
      const scheduler = new Scheduler({
        app,
        workspaces,
        resolveModel: () => model,
        locale: () => 'zh-CN',
        retryBaseMs: 1,
      })
      scheduler.start()
      await scheduler.idle()
      await scheduler.stop()
      const request = model.sent.find(
        (r) => !r.messages[0]?.content.includes('You are the coordinator'),
      )!
      return request.tools.map((t) => t.name)
    }
    const gpt = await toolsOf('gpt-5.5')
    expect(gpt).toContain('apply_patch')
    expect(gpt).not.toContain('edit_file')
    expect(gpt).not.toContain('write_file')
    const other = await toolsOf('claude-sonnet-5-5')
    expect(other).toContain('edit_file')
    expect(other).toContain('write_file')
    expect(other).not.toContain('apply_patch')
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
      if (request.messages.at(-1)?.role === 'tool') return plain('README 只有标题。')
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
        // After the message went, the turn ends.
        if (request.messages.at(-1)?.role === 'tool') {
          yield { type: 'finish', reason: 'stop' }
          return
        }
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
