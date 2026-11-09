import { FileTooLargeError, WorkspacePathError, type SourceView } from '@ariadne/plugin-sdk'
import { branchFor, run } from '@ariadne/plugin-local'
import { existsSync } from 'node:fs'
import { mkdir, mkdtemp, readFile, rm, symlink, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import {
  browseRemote,
  clip,
  connectRemote,
  LocalShell,
  OutputText,
  relativePath,
  WORKTREE_DIR,
} from './index'

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

/** git in `cwd`. */
const gitIn = (cwd: string, ...args: string[]) => run(['git', ...args], { cwd })

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

describe('output as it arrives', () => {
  test('bounded, it keeps the start and the latest end, like clip of the whole', () => {
    const kept = new OutputText(true)
    let whole = ''
    for (let i = 0; i < 3000; i++) {
      const piece = `${i}:${'x'.repeat(1000)}\n`
      kept.add(piece)
      whole += piece
    }
    expect(kept.text()).toBe(clip(whole))
    const small = new OutputText(true)
    small.add('a')
    small.add('b')
    expect(small.text()).toBe('ab')
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

  test('output comes as it is printed, without what the login shell said first', async () => {
    const dir = join(base, 'plain')
    await mkdir(dir, { recursive: true })
    const { workspace } = await provider().connect({ host: 'box', path: dir })
    const ws = await workspace!.open({ threadId: 'thr_a' })
    const seen: string[] = []
    const result = await ws.exec('echo one; echo two >&2; sleep 0.2; echo three', {
      onOutput: (chunk, stream) => seen.push(`${stream}:${chunk}`),
    })
    expect(result.exitCode).toBe(0)
    expect(seen.join('')).toContain('stdout:one\n')
    expect(seen.join('')).toContain('stderr:two\n')
    expect(seen.join('')).not.toContain('ariadne: started')
    // A program and its arguments go as they are, quoting and all.
    const file = await ws.execFile!(['printf', '%s|%s', "it's", 'a b'])
    expect(file.stdout).toBe("it's|a b")
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
    // The project's shell and environment, with values that need quoting.
    const shaped = await ws.exec('printf "%s|%s" "$0" "$GREETING"', {
      shell: ['sh', '-c'],
      env: { GREETING: "it's a b=c" },
    })
    expect(shaped.stdout).toBe("sh|it's a b=c")
    expect((await ws.exec('true', { shell: ['pwsh', '-NoProfile', '-Command'] })).exitCode).toBe(
      127,
    )

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

  test("git push from a thread's worktree goes nowhere, and nothing on disk changes", async () => {
    const dir = await repo()
    const upstream = join(base, 'upstream.git')
    await gitIn(base, 'init', '-q', '--bare', upstream)
    const other = join(base, 'other.git')
    await gitIn(base, 'init', '-q', '--bare', other)
    await gitIn(dir, 'remote', 'add', 'origin', upstream)
    // A remote with its own push address, which push rewrites leave alone.
    await gitIn(dir, 'remote', 'add', 'mirror', join(base, 'unused.git'))
    await gitIn(dir, 'config', 'remote.mirror.pushurl', other)
    const config = await readFile(join(dir, '.git', 'config'), 'utf8')
    const { workspace } = await provider().connect({ host: 'box', path: dir })
    const ws = await workspace!.open({ threadId: 'thr_push' })

    for (const target of ['origin', 'mirror', upstream, `file://${other}`]) {
      const pushed = await ws.exec(`git push -q ${target} HEAD:refs/heads/pushed`)
      expect(pushed.exitCode).not.toBe(0)
      expect(pushed.stderr).toContain('git-push-is-blocked/the-user-pushes')
    }
    expect((await gitIn(upstream, 'for-each-ref')).stdout).toBe('')
    expect((await gitIn(other, 'for-each-ref')).stdout).toBe('')
    // Fetching still works, and the user's checkout keeps its config.
    expect((await ws.exec('git fetch -q origin')).exitCode).toBe(0)
    expect(await readFile(join(dir, '.git', 'config'), 'utf8')).toBe(config)
    // The user's own push from their checkout is not affected.
    expect((await gitIn(dir, 'push', '-q', 'origin', 'main')).exitCode).toBe(0)
  })

  test('fetching still works when a push address is also a fetch address, or a prefix of one', async () => {
    const dir = await repo()
    const upstream = join(base, 'up.git')
    await gitIn(base, 'init', '-q', '--bare', upstream)
    await gitIn(dir, 'push', '-q', upstream, 'main')
    // The same address for both, and a push address that a fetch address starts with.
    await gitIn(dir, 'remote', 'add', 'origin', upstream)
    await gitIn(dir, 'config', 'remote.origin.pushurl', upstream)
    await gitIn(dir, 'remote', 'add', 'other', `${upstream}/`)
    await gitIn(dir, 'remote', 'add', 'prefix', join(base, 'unused.git'))
    await gitIn(dir, 'config', 'remote.prefix.pushurl', upstream.slice(0, -4))
    const { workspace } = await provider().connect({ host: 'box', path: dir })
    const ws = await workspace!.open({ threadId: 'thr_fetch' })
    expect((await ws.exec('git fetch -q origin && git fetch -q other')).exitCode).toBe(0)
    const pushed = await ws.exec('git push -q prefix HEAD:refs/heads/x')
    expect(pushed.stderr).toContain('git-push-is-blocked')
  })

  test("the source's view is the checkout as it is on that machine, with no worktree", async () => {
    const dir = await repo()
    await writeFile(join(dir, 'draft.md'), 'not committed\n')
    const { workspace } = await provider().connect({ host: 'box', path: dir })
    const view = await workspace!.view!()
    expect(Object.keys(view).toSorted()).toEqual(['files', 'list', 'readBytes', 'search'])
    expect((await view.list('.')).map((e) => e.name)).toEqual(['draft.md', 'pkg', 'README.md'])
    expect(await textOf(view, 'pkg/index.ts')).toBe('export {}\n')
    expect((await view.files!('.')).files.toSorted()).toEqual([
      'README.md',
      'draft.md',
      'pkg/index.ts',
    ])
    expect((await view.search!('not commit', '.')).lines).toEqual(['draft.md:1:not committed'])
    await expect(view.files!('.git')).rejects.toThrow(WorkspacePathError)
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

    const changes = (await branches.changes(
      { threadId: 'thr_a', branch: branchFor('thr_a') },
      'main',
    ))!
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
      branch: branchFor('thr_a'),
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
      await branches.merge!({
        threadId: 'thr_a',
        branch: branchFor('thr_a'),
        base: 'main',
        method: 'squash',
        message: 'x',
      }),
    ).toEqual({ kind: 'conflict', files: ['README.md'] })

    expect((await branches.worktrees()).map((w) => w.threadId)).toEqual(['thr_a'])
    expect(await branches.remove('thr_a')).toBe(false)
    // Dropping what was not committed, as when the project is deleted.
    expect(await branches.remove('thr_a', { discard: true })).toBe(true)
    expect(await branches.worktrees()).toEqual([])
    expect(await git('branch', '--list', 'ariadne/thr_a')).toContain('ariadne/thr_a')
  })
})

describe('turn work on a remote machine', () => {
  test('snapshots of a worktree compare what a command changed', async () => {
    const dir = await repo()
    const { workspace } = await provider().connect({ host: 'box', path: dir })
    const ws = await workspace!.open({ threadId: 'thr_a' })
    const branches = workspace!.branches!
    const before = await branches.snapshot!('thr_a')
    await ws.exec('echo b > b.txt && echo more >> README.md')
    const after = await branches.snapshot!('thr_a')
    const files = await branches.diff!(before!, after!)
    expect(files.map((f) => [f.path, f.status]).toSorted()).toEqual([
      ['README.md', 'modified'],
      ['b.txt', 'added'],
    ])
    expect((await ws.exec('git diff --cached --name-only')).stdout).toBe('')
    expect(await branches.snapshot!('thr_none')).toBeNull()
  })

  test('a command hands its output over as it comes, without what came before it', async () => {
    const dir = join(base, 'plain')
    await mkdir(dir, { recursive: true })
    const { workspace } = await provider().connect({ host: 'box', path: dir })
    const ws = await workspace!.open({ threadId: 'thr_a' })
    const pieces: string[] = []
    const result = await ws.exec('echo one; echo two >&2; echo three', {
      onOutput: (text) => pieces.push(text),
    })
    expect(result.stdout).toBe('one\nthree\n')
    const seen = pieces.join('')
    expect(seen).toContain('one\n')
    expect(seen).toContain('two\n')
    expect(seen).toContain('three\n')
    expect(seen).not.toContain('ariadne: started')
  })
})

describe('browsing the remote machine', () => {
  test('lists directories there, starting at home', async () => {
    await mkdir(join(home, 'projects', 'one'), { recursive: true })
    await mkdir(join(home, 'projects', 'two words'))
    await mkdir(join(home, '.hidden'))
    await writeFile(join(home, 'projects', 'file'), '')
    const shell = new TestShell()
    const start = await browseRemote(shell, '')
    expect(start.path).toBe(home)
    expect(start.directories.map((entry) => entry.name)).toEqual(['projects'])
    const inside = await browseRemote(shell, join(home, 'projects', 't'))
    expect(inside.directories).toEqual([
      { name: 'two words', path: join(home, 'projects', 'two words') },
    ])
    const dotted = await browseRemote(shell, `${home}/.`)
    expect(dotted.directories.map((entry) => entry.name)).toEqual(['.hidden'])
  })

  test('keeps links in the path as typed', async () => {
    await mkdir(join(home, 'real', 'inner'), { recursive: true })
    await symlink(join(home, 'real'), join(home, 'link'))
    const listing = await browseRemote(new TestShell(), join(home, 'link'))
    expect(listing.path).toBe(join(home, 'link'))
    expect(listing.directories).toEqual([{ name: 'inner', path: join(home, 'link', 'inner') }])
  })

  test('says why when the machine cannot be reached', async () => {
    class Down extends LocalShell {
      override async run() {
        return { exitCode: 255, stdout: '', stderr: 'connection refused' }
      }
    }
    await expect(browseRemote(new Down(), '/srv')).rejects.toThrow('connection refused')
  })
})
