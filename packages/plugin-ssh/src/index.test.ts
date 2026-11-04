import { WorkspacePathError } from '@ariadne/plugin-sdk'
import { run } from '@ariadne/plugin-local'
import { existsSync } from 'node:fs'
import { mkdir, mkdtemp, readFile, rm, symlink, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { LocalShell, relativePath, sshProvider, SshShell, WORKTREE_DIR } from './index'
import { remoteCommand, sshOptions } from './transport'

let base: string
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

const provider = () => sshProvider(join(base, 'data'), () => new TestShell())

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

describe('ssh connection', () => {
  test('forwards nothing and never asks', () => {
    const options = sshOptions('/tmp/cm/%C').join(' ')
    expect(options).toContain('BatchMode=yes')
    expect(options).toContain('ForwardAgent=no')
    expect(options).toContain('ClearAllForwardings=yes')
    expect(options).toContain('PermitLocalCommand=no')
    expect(options).toContain('ControlPath=/tmp/cm/%C')
  })

  test('quotes the script and every argument for the login shell', async () => {
    const line = remoteCommand('printf "%s|" "$@"', ["it's", '$HOME', 'a b'])
    const result = await run(['sh', '-c', line], { cwd: base })
    expect(result.stdout).toBe("it's|$HOME|a b|")
  })

  test('refuses a host that ssh would read as an option', async () => {
    await expect(
      sshProvider(join(base, 'data')).connect({ host: '-oProxyCommand=x', path: '/srv' }),
    ).rejects.toThrow('host')
    await expect(
      sshProvider(join(base, 'data')).connect({ host: 'box', path: 'relative' }),
    ).rejects.toThrow('absolute')
  })

  test('a long data directory gets no shared connection', () => {
    const shell = new SshShell({ host: 'box' }, join(base, 'x'.repeat(120)))
    expect(shell.host).toBe('box')
    expect(existsSync(join(base, 'x'.repeat(120)))).toBe(false)
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
    // Exit code 3 of the command itself is just its exit code.
    expect((await ws.exec('exit 3')).exitCode).toBe(3)
    await expect(ws.exec('pwd', { cwd: '../' })).rejects.toThrow(WorkspacePathError)

    // A background process ends with the command, so the call returns at once.
    const started = Date.now()
    const bg = await ws.exec('(sleep 30; touch late) & echo started')
    expect(bg.stdout.trim()).toBe('started')
    expect(Date.now() - started).toBeLessThan(5000)

    // A timeout ends the whole group.
    const slow = await ws.exec('sleep 30 & sleep 30; touch done', { timeoutMs: 500 })
    expect(slow.exitCode).not.toBe(0)
    const controller = new AbortController()
    const stopped = ws.exec('sleep 30; touch stopped', { signal: controller.signal })
    setTimeout(() => controller.abort(), 300)
    expect((await stopped).exitCode).not.toBe(0)
    await new Promise((done) => setTimeout(done, 300))
    const left = await run(['pgrep', '-f', 'sleep 30'], { cwd: base })
    expect(left.stdout.trim()).toBe('')
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
