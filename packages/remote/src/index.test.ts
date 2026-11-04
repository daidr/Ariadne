import { FileTooLargeError, WorkspacePathError, type SourceView } from '@ariadne/plugin-sdk'
import { run } from '@ariadne/plugin-local'
import { existsSync } from 'node:fs'
import { mkdir, mkdtemp, readFile, rm, symlink, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { connectRemote, LocalShell, relativePath, WORKTREE_DIR } from './index'

let base: string

/** A view's file as text. */
const textOf = async (view: SourceView, path: string) =>
  new TextDecoder().decode(await view.readBytes(path, 1 << 20))
let home: string

beforeEach(async () => {
  base = await mkdtemp(join(tmpdir(), 'ariadne-ssh-'))
  home = join(base, 'home')
  await mkdir(home)
})

afterEach(async () => {
  await rm(base, { recursive: true, force: true })
})

/** The scripts run on this machine with `home` as the remote user's home. */
class TestShell extends LocalShell {
  override run(script: string, args: string[], options = {}) {
    return super.run(`HOME=${JSON.stringify(home)}\n${script}`, args, options)
  }
}

const provider = () => ({
  connect: async ({ path }: { host: string; path: string }) => ({
    workspace: await connectRemote(new TestShell(), path),
  }),
})

async function repo(): Promise<string> {
  const dir = join(base, 'repo')
  await mkdir(join(dir, 'pkg'), { recursive: true })
  const git = (...args: string[]) => run(['git', ...args], { cwd: dir })
  await git('init', '-q', '-b', 'main')
  await git('config', 'user.email', 'test@example.com')
  await git('config', 'user.name', 'test')
  await writeFile(join(dir, 'README.md'), '# repo\n')
  await writeFile(join(dir, 'pkg', 'index.ts'), 'export {}\n')
  await git('add', '.')
  await git('commit', '-q', '-m', 'init')
  return dir
}

describe('workspace paths', () => {
  test('stay relative and inside', () => {
    expect(relativePath('.')).toBe('')
    expect(relativePath('')).toBe('')
    expect(relativePath('a/./b/')).toBe('a/b')
    expect(relativePath('a/../b')).toBe('b')
    expect(() => relativePath('../x')).toThrow(WorkspacePathError)
    expect(() => relativePath('a/../../x')).toThrow(WorkspacePathError)
    expect(() => relativePath('/etc/passwd')).toThrow(WorkspacePathError)
  })
})

describe('remote shell', () => {
  // Login shells quote differently; the command line they read has nothing to quote, so
  // every one of them hands the script the same arguments and the same input.
  const shells = ['sh', 'bash', 'zsh', 'fish', 'tcsh'].filter((shell) => Bun.which(shell))
  test.each(shells)('arguments and input arrive unchanged through %s', async (shell) => {
    const args = ["it's", '\\.ts', "x\\'", '$HOME `id` $(id)', 'two\nlines', 'ends\n', '', '中文']
    const script = 'for a in "$@"; do printf "[%s]" "$a"; done; printf "|"; cat'
    const result = await new LocalShell(shell).run(script, args, { input: "after\\'\n" })
    expect(result.stderr).toBe('')
    expect(result.stdout).toBe(`${args.map((a) => `[${a}]`).join('')}|after\\'\n`)
  })
})

describe('remote directory', () => {
  test('a plain directory is shared, with files kept inside it', async () => {
    const dir = join(base, 'plain')
    await mkdir(join(dir, 'sub'), { recursive: true })
    await writeFile(join(base, 'secret'), 'secret\n')
    await symlink(join(base, 'secret'), join(dir, 'leak'))
    await symlink(base, join(dir, 'up'))
    await symlink(join(base, 'nowhere'), join(dir, 'dangling'))
    const { workspace } = await provider().connect({ host: 'box', path: dir })
    expect(workspace?.isolated).toBe(false)
    const ws = await workspace!.open({ threadId: 'thr_a' })
    expect(ws.branch).toBeNull()
    expect(ws.label).toMatch(/^localhost:\//)

    await ws.writeFile('sub/deep/new.txt', "it's here\n")
    expect(await ws.readFile('sub/deep/new.txt')).toBe("it's here\n")
    expect(await readFile(join(dir, 'sub/deep/new.txt'), 'utf8')).toBe("it's here\n")
    expect(await ws.list('.')).toEqual([
      { name: 'dangling', kind: 'file' },
      { name: 'leak', kind: 'file' },
      { name: 'sub', kind: 'directory' },
      { name: 'up', kind: 'file' },
    ])

    await expect(ws.readFile('../secret')).rejects.toThrow(WorkspacePathError)
    await expect(ws.readFile('leak')).rejects.toThrow(WorkspacePathError)
    await expect(ws.readFile('up/secret')).rejects.toThrow(WorkspacePathError)
    await expect(ws.writeFile('up/new', 'x')).rejects.toThrow(WorkspacePathError)
    await expect(ws.writeFile('dangling', 'x')).rejects.toThrow(WorkspacePathError)
    expect(existsSync(join(base, 'new'))).toBe(false)
    expect(existsSync(join(base, 'nowhere'))).toBe(false)
    await expect(ws.readFile('missing.txt')).rejects.toThrow('no such file')
    await expect(ws.readFile('sub')).rejects.toThrow('directory')

    await writeFile(join(dir, 'a.bin'), new Uint8Array([0, 255, 2, 3]))
    expect(await ws.readBytes('a.bin', 4)).toEqual(new Uint8Array([0, 255, 2, 3]))
    await expect(ws.readBytes('a.bin', 3)).rejects.toThrow(FileTooLargeError)
    await expect(ws.readBytes('leak', 10)).rejects.toThrow(WorkspacePathError)
  })

  test('commands run in the directory and end with their background processes', async () => {
    const dir = join(base, 'plain')
    await mkdir(join(dir, 'sub'), { recursive: true })
    const { workspace } = await provider().connect({ host: 'box', path: dir })
    const ws = await workspace!.open({ threadId: 'thr_a' })

    const here = await ws.exec('pwd; echo err >&2; exit 7', { cwd: 'sub' })
    expect(here.exitCode).toBe(7)
    expect(here.stdout.trim()).toMatch(/\/plain\/sub$/)
    expect(here.stderr.trim()).toBe('err')
    // Exit code 3 of the command itself is just its exit code, and 255 is not a failed
    // connection when the command ran (it may run ssh itself).
    expect((await ws.exec('exit 3')).exitCode).toBe(3)
    expect(await ws.exec('echo no route >&2; exit 255')).toEqual({
      exitCode: 255,
      stdout: '',
      stderr: 'no route\n',
    })
    await expect(ws.exec('pwd', { cwd: '../' })).rejects.toThrow(WorkspacePathError)

    // A background process ends with the command, so the call returns at once.
    const started = Date.now()
    // A duration no other process uses, so only this test's sleeps are looked for.
    const nap = `sleep 30.${process.pid}`
    const bg = await ws.exec(`(${nap}; touch late) & echo started`)
    expect(bg.stdout.trim()).toBe('started')
    expect(Date.now() - started).toBeLessThan(5000)

    // A timeout ends the whole group.
    const slow = await ws.exec(`${nap} & ${nap}; touch done`, { timeoutMs: 500 })
    expect(slow.exitCode).not.toBe(0)
    const controller = new AbortController()
    const stopped = ws.exec(`${nap}; touch stopped`, { signal: controller.signal })
    setTimeout(() => controller.abort(), 300)
    expect((await stopped).exitCode).not.toBe(0)
    // Killing takes a moment on a busy machine: wait up to 5 seconds for them to go.
    let left = ''
    for (let i = 0; i < 50; i++) {
      left = (await run(['pgrep', '-f', nap], { cwd: base })).stdout.trim()
      if (!left) break
      await new Promise((done) => setTimeout(done, 100))
    }
    expect(left).toBe('')
    expect(existsSync(join(dir, 'done'))).toBe(false)
    expect(existsSync(join(dir, 'stopped'))).toBe(false)
  }, 20_000)

  test('a git checkout gives each thread its own worktree and branch', async () => {
    const dir = await repo()
    await mkdir(join(dir, 'node_modules', 'dep'), { recursive: true })
    await writeFile(join(dir, '.gitignore'), 'node_modules\n')
    const { workspace } = await provider().connect({ host: 'box', path: dir })
    expect(workspace?.isolated).toBe(true)
    const a = await workspace!.open({ threadId: 'thr_a' })
    const b = await workspace!.open({ threadId: 'thr_b' })
    expect(a.branch).toBe('ariadne/thr_a')
    expect(a.label).toContain(`${WORKTREE_DIR}/`)
    expect(a.label).not.toBe(b.label)

    await a.writeFile('src/new.ts', 'export {}\n')
    await expect(b.readFile('src/new.ts')).rejects.toThrow()
    expect(await a.readFile('README.md')).toBe('# repo\n')
    const status = await a.exec('git status --short && git branch --show-current')
    expect(status.stdout).toContain('?? src/')
    expect(status.stdout).toContain('ariadne/thr_a')

    // Opening again reuses the worktree and keeps the work.
    const again = await workspace!.open({ threadId: 'thr_a' })
    expect(await again.readFile('src/new.ts')).toBe('export {}\n')
    // The user's own checkout is untouched.
    expect(existsSync(join(dir, 'src'))).toBe(false)
  })

  test("the source's view is the checkout as it is on that machine, with no worktree", async () => {
    const dir = await repo()
    await writeFile(join(dir, 'draft.md'), 'not committed\n')
    const { workspace } = await provider().connect({ host: 'box', path: dir })
    const view = await workspace!.view!()
    expect(Object.keys(view).toSorted()).toEqual(['list', 'readBytes'])
    expect((await view.list('.')).map((e) => e.name)).toEqual(['draft.md', 'pkg', 'README.md'])
    expect(await textOf(view, 'pkg/index.ts')).toBe('export {}\n')
    await expect(view.readBytes('../x', 10)).rejects.toThrow(WorkspacePathError)
    await expect(view.readBytes('.git/config', 1000)).rejects.toThrow(WorkspacePathError)
    await expect(view.list('.git')).rejects.toThrow(WorkspacePathError)
    expect(existsSync(join(home, WORKTREE_DIR))).toBe(false)
  })

  test('a worktree deleted by hand is recreated on its branch', async () => {
    const dir = await repo()
    const { workspace } = await provider().connect({ host: 'box', path: dir })
    const a = await workspace!.open({ threadId: 'thr_a' })
    await a.writeFile('kept.txt', 'kept\n')
    await a.exec('git add . && git -c user.email=t@e -c user.name=t commit -qm kept')
    await rm(a.label.slice('localhost:'.length), { recursive: true, force: true })
    const again = await workspace!.open({ threadId: 'thr_a' })
    expect(await again.readFile('kept.txt')).toBe('kept\n')
  })

  test('a subdirectory of a checkout stays the root inside the worktree', async () => {
    const dir = await repo()
    const { workspace } = await provider().connect({ host: 'box', path: join(dir, 'pkg') })
    const ws = await workspace!.open({ threadId: 'thr_a' })
    expect(ws.label).toMatch(/\/thr_a\/pkg$/)
    expect(await ws.readFile('index.ts')).toBe('export {}\n')
    await expect(ws.readFile('../README.md')).rejects.toThrow(WorkspacePathError)
  })

  test('a missing directory fails to connect with the reason', async () => {
    await expect(provider().connect({ host: 'box', path: join(base, 'none') })).rejects.toThrow(
      'not a directory',
    )
  })
})

describe('ssh branches', () => {
  test('changes, squash merge into the checkout, conflicts and removing a worktree', async () => {
    const dir = await repo()
    const git = async (...args: string[]) =>
      (await run(['git', ...args], { cwd: dir })).stdout.trim()
    const { workspace } = await provider().connect({ host: 'box', path: dir })
    const ws = await workspace!.open({ threadId: 'thr_a' })
    expect(ws.base).toBe('main')
    await ws.exec('echo a > a.txt && git add . && git commit -qm a && echo wip > wip.txt')
    const branches = workspace!.branches!

    const changes = (await branches.changes('thr_a', 'main'))!
    expect(changes.files.map((f) => f.path)).toEqual(['a.txt'])
    expect(changes.uncommitted).toEqual(['wip.txt'])
    expect(
      await branches.file({
        from: changes.from,
        head: changes.head,
        path: 'a.txt',
        previousPath: null,
      }),
    ).toEqual({ before: null, after: 'a\n', skipped: null })

    const outcome = await branches.merge!({
      threadId: 'thr_a',
      base: 'main',
      method: 'squash',
      message: 'Add a',
    })
    expect(outcome.kind).toBe('merged')
    expect(await git('log', '-1', '--format=%s')).toBe('Add a')
    expect(await readFile(join(dir, 'a.txt'), 'utf8')).toBe('a\n')

    await ws.exec('echo thread > README.md && git commit -qam readme')
    await writeFile(join(dir, 'README.md'), 'user\n')
    await git('commit', '-qam', 'user')
    expect(
      await branches.merge!({ threadId: 'thr_a', base: 'main', method: 'squash', message: 'x' }),
    ).toEqual({ kind: 'conflict', files: ['README.md'] })

    expect((await branches.worktrees()).map((w) => w.threadId)).toEqual(['thr_a'])
    expect(await branches.remove('thr_a')).toBe(false)
    // Dropping what was not committed, as when the project is deleted.
    expect(await branches.remove('thr_a', { discard: true })).toBe(true)
    expect(await branches.worktrees()).toEqual([])
    expect(await git('branch', '--list', 'ariadne/thr_a')).toContain('ariadne/thr_a')
  })
})
