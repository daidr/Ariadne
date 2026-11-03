import { WorkspacePathError, type CommandSandbox } from '@ariadne/plugin-sdk'
import { existsSync } from 'node:fs'
import { mkdtemp, realpath, rm, symlink, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { branchFor, localDirectoryProvider, run } from './index'

let base: string

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

  test('commands time out', async () => {
    const result = await run(['sleep', '5'], { cwd: base, timeoutMs: 50 })
    expect(result.exitCode).not.toBe(0)
  })
})
