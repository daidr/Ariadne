import {
  FileTooLargeError,
  WorkspacePathError,
  type CommandSandbox,
  type SourceView,
} from '@ariadne/plugin-sdk'
import { existsSync } from 'node:fs'
import {
  mkdir,
  mkdtemp,
  readFile,
  realpath,
  rename,
  rm,
  symlink,
  writeFile,
} from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { branchFor, LocalWorkspace, localDirectoryProvider, run } from './index'
import { hostedGitEnv } from './hosted'

let base: string

/** A view's file as text. */
const textOf = async (view: SourceView, path: string) =>
  new TextDecoder().decode(await view.readBytes(path, 1 << 20))

beforeEach(async () => {
  base = await mkdtemp(join(tmpdir(), 'ariadne-local-'))
})

afterEach(async () => {
  await rm(base, { recursive: true, force: true })
})

async function repo(): Promise<string> {
  const dir = join(base, 'repo')
  await run(['mkdir', '-p', dir], { cwd: base })
  const git = (...args: string[]) => run(['git', ...args], { cwd: dir })
  await git('init', '-q', '-b', 'main')
  await git('config', 'user.email', 'test@example.com')
  await git('config', 'user.name', 'test')
  await writeFile(join(dir, 'README.md'), '# repo\n')
  await git('add', '.')
  await git('commit', '-q', '-m', 'init')
  return dir
}

describe('local directory', () => {
  test('a git repository gives each thread its own worktree and branch', async () => {
    const dir = await repo()
    const provider = localDirectoryProvider(join(base, 'data'))
    const { workspace } = await provider.connect({ path: dir })
    expect(workspace?.isolated).toBe(true)
    const a = await workspace!.open({ threadId: 'thr_a' })
    const b = await workspace!.open({ threadId: 'thr_b' })
    expect(a.branch).toBe('ariadne/thr_a')
    expect(a.label).not.toBe(b.label)

    await a.writeFile('src/new.ts', 'export {}\n')
    await expect(b.readFile('src/new.ts')).rejects.toThrow()
    expect(await a.readFile('README.md')).toBe('# repo\n')
    expect((await a.list('.')).map((e) => e.name)).toEqual(['README.md', 'src'])

    const status = await a.exec('git status --short && git branch --show-current')
    expect(status.stdout).toContain('?? src/')
    expect(status.stdout).toContain('ariadne/thr_a')

    // Opening again reuses the worktree and keeps the work.
    const again = await workspace!.open({ threadId: 'thr_a' })
    expect(await again.readFile('src/new.ts')).toBe('export {}\n')
  })

  test("the source's view is the user's directory as it is, read-only", async () => {
    const dir = await repo()
    await writeFile(join(dir, 'draft.md'), 'not committed\n')
    const provider = localDirectoryProvider(join(base, 'data'))
    const { workspace } = await provider.connect({ path: dir })
    const view = await workspace!.view!()
    // Only reading: nothing behind it to write or run with.
    expect(Object.keys(view).toSorted()).toEqual(['files', 'list', 'readBytes', 'search'])
    expect((await view.list('.')).map((e) => e.name)).toEqual(['draft.md', 'README.md'])
    expect(await textOf(view, 'draft.md')).toBe('not committed\n')
    // Found and searched as it is now, the file not committed included.
    expect((await view.files!('.')).files.toSorted()).toEqual(['README.md', 'draft.md'])
    expect((await view.search!('not commit', '.')).lines).toEqual(['draft.md:1:not committed'])
    await expect(view.readBytes('../outside', 10)).rejects.toBeInstanceOf(WorkspacePathError)
    // git's own directory, which may hold credentials in a remote's URL, is not shown.
    for (const path of ['.git/config', './.git/config', 'sub/../.git/HEAD', '.GIT/config']) {
      await expect(view.readBytes(path, 1000)).rejects.toBeInstanceOf(WorkspacePathError)
    }
    // Finding and searching run programs on the path: it stays inside the same way.
    await writeFile(join(base, 'outside.txt'), 'OUTSIDE\n')
    await symlink(join(base, 'outside.txt'), join(dir, 'link.txt'))
    for (const path of [
      '..',
      '../outside.txt',
      join(base, 'outside.txt'),
      '/etc/hostname',
      '.git/config',
      '.GIT',
      'sub/../.git',
      'link.txt',
    ]) {
      await expect(view.search!('.', path)).rejects.toBeInstanceOf(WorkspacePathError)
      await expect(view.files!(path)).rejects.toBeInstanceOf(WorkspacePathError)
    }
    // A link met on the way is not followed out either.
    expect((await view.search!('OUTSIDE', '.')).lines).toEqual([])
    expect((await view.files!('.')).files).not.toContain('link.txt')

    // Looking makes no worktree and no branch.
    expect(existsSync(join(base, 'data', 'worktrees'))).toBe(false)
    const branches = await run(['git', 'branch', '--list'], { cwd: dir })
    expect(branches.stdout.trim()).toBe('* main')

    const plain = join(base, 'plain')
    await run(['mkdir', '-p', plain], { cwd: base })
    await writeFile(join(plain, 'notes.txt'), 'hi\n')
    const shared = (await provider.connect({ path: plain })).workspace!
    expect(await textOf(await shared.view!(), 'notes.txt')).toBe('hi\n')
  })

  test('what the sandbox hides is not read, listed or written, with or without a sandbox', async () => {
    // A source that holds Ariadne's home, as when the source is the user's home directory.
    const home = join(base, 'home')
    const secrets = join(home, '.ariadne')
    await run(['mkdir', '-p', join(secrets, 'worktrees', 'thr_a'), join(home, 'src')], {
      cwd: base,
    })
    await writeFile(join(secrets, 'config.json'), '{"apiKey":"sk-secret"}')
    await writeFile(join(home, 'src', 'a.ts'), 'export {}\n')
    await symlink(join(secrets, 'config.json'), join(home, 'src', 'link.json'))
    const sandbox: CommandSandbox = {
      kind: null,
      wrap: (argv) => argv,
      hidden: [await realpath(secrets)],
    }
    const provider = localDirectoryProvider(join(base, 'data'), sandbox)
    const { workspace } = await provider.connect({ path: home })
    for (const files of [await workspace!.view!(), await workspace!.open({ threadId: 'thr_x' })]) {
      expect((await files.list('.')).map((e) => e.name)).toEqual(['src'])
      for (const path of ['.ariadne/config.json', 'src/link.json', '.ariadne']) {
        await expect(files.readBytes(path, 1000)).rejects.toBeInstanceOf(WorkspacePathError)
      }
      await expect(files.list('.ariadne')).rejects.toBeInstanceOf(WorkspacePathError)
    }
    const thread = await workspace!.open({ threadId: 'thr_x' })
    await expect(thread.writeFile('.ariadne/config.json', '{}')).rejects.toBeInstanceOf(
      WorkspacePathError,
    )
    expect(await readFile(join(secrets, 'config.json'), 'utf8')).toBe('{"apiKey":"sk-secret"}')

    // A workspace kept inside a hidden directory (a worktree under Ariadne's home) still works.
    const inside = new LocalWorkspace('thr_a', join(secrets, 'worktrees', 'thr_a'), null, {
      sandbox,
    })
    await inside.writeFile('ok.txt', 'ok')
    expect(await inside.readFile('ok.txt')).toBe('ok')
  })

  test('bytes are read up to a limit', async () => {
    const dir = join(base, 'plain')
    await run(['mkdir', '-p', dir], { cwd: base })
    await writeFile(join(dir, 'a.bin'), new Uint8Array([0, 1, 2, 3]))
    const { workspace } = await localDirectoryProvider(join(base, 'data')).connect({ path: dir })
    const ws = await workspace!.open({ threadId: 'thr_a' })
    expect(await ws.readBytes('a.bin', 4)).toEqual(new Uint8Array([0, 1, 2, 3]))
    await expect(ws.readBytes('a.bin', 3)).rejects.toThrow(FileTooLargeError)
    await expect(ws.readBytes('../secret', 4)).rejects.toThrow(WorkspacePathError)
  })

  test('a worktree deleted by hand is recreated on its branch', async () => {
    const dir = await repo()
    const provider = localDirectoryProvider(join(base, 'data'))
    const { workspace } = await provider.connect({ path: dir })
    const first = await workspace!.open({ threadId: 'thr_a' })
    await first.exec('echo hi > kept.txt && git add . && git commit -qm kept')
    await rm(first.label, { recursive: true, force: true })
    const again = await workspace!.open({ threadId: 'thr_a' })
    expect(again.branch).toBe(first.branch)
    expect(await again.readFile('kept.txt')).toBe('hi\n')
  })

  test('a subdirectory of a repository stays the root inside the worktree', async () => {
    const dir = await repo()
    await run(['mkdir', '-p', join(dir, 'pkg')], { cwd: base })
    await writeFile(join(dir, 'pkg', 'a.txt'), 'a')
    await run(['git', 'add', '.'], { cwd: dir })
    await run(['git', 'commit', '-qm', 'pkg'], { cwd: dir })
    const { workspace } = await localDirectoryProvider(join(base, 'data')).connect({
      path: join(dir, 'pkg'),
    })
    const ws = await workspace!.open({ threadId: 'thr_a' })
    expect((await ws.list('.')).map((e) => e.name)).toEqual(['a.txt'])
    await expect(ws.readFile('../README.md')).rejects.toThrow(WorkspacePathError)
  })

  test('a subdirectory the commit does not track is refused with a clear reason', async () => {
    const dir = await repo()
    await run(['mkdir', '-p', join(dir, 'untracked')], { cwd: base })
    const { workspace } = await localDirectoryProvider(join(base, 'data')).connect({
      path: join(dir, 'untracked'),
    })
    await expect(workspace!.open({ threadId: 'thr_a' })).rejects.toThrow(/current commit/)
  })

  test('paths cannot leave the workspace, not even through a link', async () => {
    const dir = join(base, 'plain')
    await run(['mkdir', '-p', dir], { cwd: base })
    await writeFile(join(base, 'secret'), 'no')
    await symlink(join(base, 'secret'), join(dir, 'link'))
    const { workspace } = await localDirectoryProvider(join(base, 'data')).connect({ path: dir })
    expect(workspace?.isolated).toBe(false)
    const ws = await workspace!.open({ threadId: 'thr_a' })
    await expect(ws.readFile('../secret')).rejects.toThrow(WorkspacePathError)
    await expect(ws.readFile('link')).rejects.toThrow(WorkspacePathError)
    await expect(ws.writeFile('/etc/passwd', 'x')).rejects.toThrow(WorkspacePathError)
    await ws.writeFile('deep/er/file.txt', 'ok')
    expect(await ws.readFile('deep/er/file.txt')).toBe('ok')
  })

  test('a single file is read by its name alone, and nothing beside it', async () => {
    const dir = join(base, 'logs')
    await mkdir(dir)
    await writeFile(join(dir, 'app.log'), 'boot\nfailed: 502\n')
    await writeFile(join(dir, 'secret.env'), 'TOKEN=1\n')
    await symlink(join(dir, 'secret.env'), join(dir, 'link.log'))
    const provider = localDirectoryProvider(join(base, 'data'))
    const { workspace } = await provider.connect({ path: join(dir, 'app.log') })
    // Nothing to work in: a project cannot take it as its source.
    expect(workspace!.viewOnly).toBe(true)
    await expect(workspace!.open({ threadId: 't' })).rejects.toThrow('is a file')
    const view = await workspace!.view!()
    expect(await view.list('.')).toEqual([{ name: 'app.log', kind: 'file' }])
    expect(new TextDecoder().decode(await view.readBytes('app.log', 1000))).toContain('502')
    expect(new TextDecoder().decode(await view.readBytes('./app.log', 1000))).toContain('502')
    for (const path of ['secret.env', '../logs/secret.env', 'app.log/../secret.env', 'sub']) {
      await expect(view.readBytes(path, 1000)).rejects.toThrow()
    }
    await expect(view.list('sub')).rejects.toThrow()
    await expect(view.list('..')).rejects.toThrow()
    // Searching goes file by file through the same two doors.
    expect(view.files).toBeUndefined()
    expect(view.search).toBeUndefined()
  })

  test('refuses relative or missing directories', async () => {
    const provider = localDirectoryProvider(join(base, 'data'))
    await expect(provider.connect({ path: 'relative' })).rejects.toThrow()
    await expect(provider.connect({ path: join(base, 'missing') })).rejects.toThrow()
  })

  test('branch names use the random tail of the thread id', () => {
    expect(branchFor('thr_01a1a344-64ac-71b6-9cac-f9b91ac96c2d')).toBe('ariadne/f9b91ac96c2d')
  })

  test("a thread's commands do not see the server's credentials", async () => {
    const saved = { ...process.env }
    process.env.GH_TOKEN = 'ghp_probe_secret'
    process.env.ARIADNE_TOKEN = 'local-secret'
    process.env.GIT_CONFIG_VALUE_0 = 'Authorization: Basic x'
    try {
      const dir = join(base, 'plain')
      await run(['mkdir', '-p', dir], { cwd: base })
      const { workspace } = await localDirectoryProvider(join(base, 'data')).connect({ path: dir })
      const ws = await workspace!.open({ threadId: 'thr_a' })
      const result = await ws.exec('env')
      expect(result.stdout).not.toContain('ghp_probe_secret')
      expect(result.stdout).not.toContain('local-secret')
      expect(result.stdout).not.toContain('Authorization')
      expect(result.stdout).toContain('PATH=')
    } finally {
      process.env = saved
    }
  })

  test('commands run in the sandbox, which keeps the worktree and its git directory', async () => {
    const dir = await repo()
    const calls: { argv: string[]; keep: string[]; readOnly?: string[] }[] = []
    const sandbox: CommandSandbox = {
      kind: 'test',
      wrap: (argv, { keep, readOnly }) => {
        calls.push({ argv, keep, readOnly })
        return argv
      },
    }
    const { workspace } = await localDirectoryProvider(join(base, 'data'), sandbox).connect({
      path: dir,
    })
    const ws = await workspace!.open({ threadId: 'thr_a' })
    expect((await ws.exec('echo hi')).stdout).toBe('hi\n')
    expect(calls).toHaveLength(1)
    expect(calls[0]!.argv).toEqual(['bash', '-c', 'echo hi'])
    // The project's shell goes inside the sandbox too, and its environment reaches it.
    const shaped = await ws.exec('printf %s "$GREETING"', {
      shell: ['sh', '-c'],
      env: { GREETING: 'hello' },
    })
    expect(shaped.stdout).toBe('hello')
    expect(calls[1]!.argv).toEqual(['sh', '-c', 'printf %s "$GREETING"'])
    const real = await realpath(dir)
    const [worktree] = calls[0]!.keep
    expect(worktree?.startsWith(join(base, 'data', 'worktrees'))).toBe(true)
    expect(worktree?.endsWith('/thr_a')).toBe(true)
    expect(calls[0]!.keep).toContain(join(real, '.git'))
    expect(calls[0]!.readOnly).toEqual([join(real, '.git', 'config'), join(real, '.git', 'hooks')])
  })

  test("hooks left in the repository do not run in the server's git", async () => {
    const dir = await repo()
    const marker = join(base, 'ran')
    const hook = join(dir, '.git', 'hooks', 'post-checkout')
    await writeFile(hook, `#!/bin/sh\ntouch ${marker}\n`, { mode: 0o755 })
    await run(['git', 'config', 'core.fsmonitor', `touch ${marker}`], { cwd: dir })
    const { workspace } = await localDirectoryProvider(join(base, 'data')).connect({ path: dir })
    await workspace!.open({ threadId: 'thr_a' })
    expect(existsSync(marker)).toBe(false)
  })

  test('what a command leaves running in the background ends with it', async () => {
    const dir = join(base, 'plain')
    await run(['mkdir', '-p', dir], { cwd: base })
    const { workspace } = await localDirectoryProvider(join(base, 'data')).connect({ path: dir })
    const ws = await workspace!.open({ threadId: 'thr_a' })
    const started = Date.now()
    const result = await ws.exec('(sleep 3; touch late) & echo started')
    expect(result.stdout).toBe('started\n')
    expect(Date.now() - started).toBeLessThan(2000)
    await Bun.sleep(3500)
    expect(existsSync(join(dir, 'late'))).toBe(false)
  })

  test('a file swapped for a link after the path was checked is refused', async () => {
    const dir = join(base, 'plain')
    await run(['mkdir', '-p', join(dir, 'sub')], { cwd: base })
    await writeFile(join(dir, 'sub', 'a.txt'), 'inside\n')
    await writeFile(join(base, 'secret'), 'outside\n')
    const { workspace } = await localDirectoryProvider(join(base, 'data')).connect({ path: dir })
    const ws = (await workspace!.open({ threadId: 'thr_a' })) as LocalWorkspace
    // Swap the directory for a link to outside right after the path is resolved.
    const resolveFirst = ws.resolve.bind(ws)
    let calls = 0
    ws.resolve = async (path: string) => {
      const resolved = await resolveFirst(path)
      if (++calls === 1) {
        await rm(join(dir, 'sub'), { recursive: true })
        await symlink(base, join(dir, 'sub'))
        await rename(join(base, 'secret'), join(base, 'a.txt'))
      }
      return resolved.replace(/sub\/a\.txt$/, 'sub/a.txt')
    }
    await expect(ws.readFile('sub/a.txt')).rejects.toThrow(WorkspacePathError)
    await expect(ws.writeFile('sub/a.txt', 'x')).rejects.toThrow(WorkspacePathError)
    expect(await readFile(join(base, 'a.txt'), 'utf8')).toBe('outside\n')
  })

  test('a refused write neither fills nor deletes what lies outside the workspace', async () => {
    const dir = join(base, 'plain2')
    const outside = join(base, 'outside')
    await run(['mkdir', '-p', join(dir, 'sub'), join(outside, 'new')], { cwd: base })
    // Stands for a file outside, such as Ariadne's own settings.
    await writeFile(join(outside, 'new', 'config.json'), 'keep\n')
    const { workspace } = await localDirectoryProvider(join(base, 'data')).connect({ path: dir })
    const ws = (await workspace!.open({ threadId: 'thr_a' })) as LocalWorkspace
    const resolveFirst = ws.resolve.bind(ws)
    let calls = 0
    // The swap lands after the file was opened inside, just before the check: removing the
    // new file by its path then would delete the one outside.
    ws.resolve = async (path: string) => {
      if (++calls === 3) {
        await rm(join(dir, 'sub'), { recursive: true })
        await symlink(outside, join(dir, 'sub'))
      }
      return resolveFirst(path)
    }
    await expect(ws.writeFile('sub/new/config.json', 'x')).rejects.toThrow(WorkspacePathError)
    expect(await readFile(join(outside, 'new', 'config.json'), 'utf8')).toBe('keep\n')
  })

  test('output held by a process that left the group does not hang the call', async () => {
    const dir = join(base, 'plain')
    await run(['mkdir', '-p', dir], { cwd: base })
    const { workspace } = await localDirectoryProvider(join(base, 'data')).connect({ path: dir })
    const ws = await workspace!.open({ threadId: 'thr_a' })
    const started = Date.now()
    const result = await ws.exec('setsid sleep 4 & echo started')
    expect(result.stdout).toBe('started\n')
    expect(Date.now() - started).toBeLessThan(3000)
  })

  test('a command stopped before it starts never runs', async () => {
    const controller = new AbortController()
    controller.abort()
    const marker = join(base, 'ran')
    const result = await run(['bash', '-c', `touch ${marker}`], {
      cwd: base,
      forThread: true,
      signal: controller.signal,
    })
    expect(result.exitCode).not.toBe(0)
    await Bun.sleep(100)
    expect(existsSync(marker)).toBe(false)
  })

  test("a new worktree gets the checkout's dependencies as clones, where clones work", async () => {
    const dir = await repo()
    await writeFile(join(dir, '.gitignore'), 'node_modules/\ndist/\n')
    await run(['git', 'add', '.'], { cwd: dir })
    await run(['git', 'commit', '-qm', 'ignore'], { cwd: dir })
    await run(['mkdir', '-p', join(dir, 'node_modules', 'dep'), join(dir, 'dist')], { cwd: base })
    await writeFile(join(dir, 'node_modules', 'dep', 'index.js'), 'ok')
    await writeFile(join(dir, 'dist', 'out.js'), 'built')
    // A linked dependency folder and a Python environment are left out.
    await run(['mkdir', '-p', join(base, 'elsewhere'), join(dir, 'pkg'), join(dir, '.venv')], {
      cwd: base,
    })
    await symlink(join(base, 'elsewhere'), join(dir, 'pkg', 'node_modules'))
    await writeFile(join(dir, '.gitignore'), 'node_modules/\nnode_modules\ndist/\n.venv/\n')
    await run(['git', 'add', '.gitignore'], { cwd: dir })
    await run(['git', 'commit', '-qm', 'ignore more'], { cwd: dir })
    const cloned: string[] = []
    // Stands for a filesystem that clones: copies, and records what it was asked to clone.
    const clone = async (from: string, to: string) => {
      cloned.push(from)
      return (await run(['cp', '-a', from, to], { cwd: base })).exitCode === 0
    }
    const provider = localDirectoryProvider(join(base, 'data'), undefined, clone)
    const { workspace } = await provider.connect({ path: dir })
    const ws = await workspace!.open({ threadId: 'thr_a' })
    expect(await ws.readFile('node_modules/dep/index.js')).toBe('ok')
    // Only dependencies, not build output.
    await expect(ws.readFile('dist/out.js')).rejects.toThrow()
    expect(cloned.some((path) => path.endsWith('node_modules'))).toBe(true)
    expect(cloned.some((path) => path.includes('pkg') || path.endsWith('.venv'))).toBe(false)
    // It stays ignored, so the thread's commits do not pick it up.
    expect((await ws.exec('git status --short')).stdout).toBe('')

    // Where the filesystem cannot clone, the worktree is a plain one.
    const plain = localDirectoryProvider(join(base, 'data2'), undefined, async () => false)
    const other = await (await plain.connect({ path: dir })).workspace!.open({ threadId: 'thr_b' })
    await expect(other.readFile('node_modules/dep/index.js')).rejects.toThrow()
  })

  test('commands time out', async () => {
    const result = await run(['sleep', '5'], { cwd: base, timeoutMs: 50 })
    expect(result.exitCode).not.toBe(0)
  })
})

describe('hosted repository git', () => {
  test('credentials are bound to the remote, and git follows no redirect with them', () => {
    const env = hostedGitEnv('https://git.example.com/a/b.git', { user: 'oauth2', token: 't' })
    const config = Object.fromEntries(
      Array.from({ length: Number(env.GIT_CONFIG_COUNT) }, (_, i) => [
        env[`GIT_CONFIG_KEY_${i}`],
        env[`GIT_CONFIG_VALUE_${i}`],
      ]),
    )
    expect(config['http.followRedirects']).toBe('false')
    expect(config['http.https://git.example.com/a/b.git.extraHeader']).toBe(
      `Authorization: Basic ${Buffer.from('oauth2:t').toString('base64')}`,
    )
    expect(config['core.hooksPath']).toBe('/dev/null')
    expect(env.GIT_ALLOW_PROTOCOL).toBe('https')
  })
})
