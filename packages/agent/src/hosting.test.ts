import { FileTooLargeError, WorkspacePathError, type RepoCapability } from '@ariadne/plugin-sdk'
import { run } from '@ariadne/plugin-local'
import { hostedClone } from '@ariadne/plugin-local/hosted'
import { connectRemote, LocalShell } from '@ariadne/remote'
import { existsSync } from 'node:fs'
import { mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { repositoryOf, withHosting } from './hosting'

let base: string
let home: string

beforeEach(async () => {
  base = await mkdtemp(join(tmpdir(), 'ariadne-hosting-'))
  home = join(base, 'home')
  await mkdir(home)
})

afterEach(async () => {
  await rm(base, { recursive: true, force: true })
})

/** The remote machine: this one, with `home` as the user's home. */
class TestShell extends LocalShell {
  override run(script: string, args: string[], options = {}) {
    return super.run(`HOME=${JSON.stringify(home)}\n${script}`, args, options)
  }
}

const git = async (cwd: string, ...args: string[]) => {
  const result = await run(['git', ...args], { cwd })
  if (result.exitCode !== 0) throw new Error(result.stderr)
  return result.stdout.trim()
}

const WEB = 'https://example.com/team/app'

const unused = async () => {
  throw new Error('not in this test')
}

/** The hosted repository, a checkout of it "on the remote machine", and Ariadne's clone. */
async function setup(origin = `git@example.com:team/app.git`) {
  const upstream = join(base, 'upstream.git')
  await git(base, 'init', '-q', '--bare', '-b', 'main', upstream)
  const seed = join(base, 'seed')
  await git(base, 'clone', '-q', upstream, seed)
  await git(seed, 'config', 'user.email', 't@example.com')
  await git(seed, 'config', 'user.name', 't')
  await writeFile(join(seed, 'README.md'), '# app\n')
  await git(seed, 'add', '.')
  await git(seed, 'commit', '-q', '-m', 'init')
  await git(seed, 'push', '-q', 'origin', 'main')

  const checkout = join(base, 'checkout')
  await git(base, 'clone', '-q', upstream, checkout)
  await git(checkout, 'remote', 'set-url', 'origin', origin)
  await git(checkout, 'config', 'user.email', 'remote@example.com')
  await git(checkout, 'config', 'user.name', 'remote')

  // The hosted repository moves on after the checkout was cloned.
  await writeFile(join(seed, 'NEW.md'), 'new\n')
  await git(seed, 'add', '.')
  await git(seed, 'commit', '-q', '-m', 'newer')
  await git(seed, 'push', '-q', 'origin', 'main')

  const clone = hostedClone({
    dataDir: join(base, 'data'),
    path: 'team/app',
    remoteUrl: `file://${upstream}`,
    user: 'x',
    token: async () => null,
    defaultBranch: async () => 'main',
  })
  const repo: RepoCapability = {
    remote: 'team/app',
    webUrl: WEB,
    defaultBranch: async () => 'main',
    isPublic: async () => false,
    fetch: clone.fetch,
    push: clone.push,
    objects: clone.objects,
    pullRequestFor: unused,
    pullRequest: unused,
    openPullRequest: unused,
    checks: unused,
    checkLog: unused,
  }
  const workspace = await connectRemote(new TestShell(), checkout)
  return { upstream, seed, checkout, repo, workspace }
}

describe("a hosted repository's view", () => {
  test('the coordinator reads the default branch as fetched, without a checkout', async () => {
    const { seed, upstream } = await setup()
    await mkdir(join(seed, 'docs'), { recursive: true })
    await writeFile(join(seed, 'docs', 'a.md'), 'A\n')
    await writeFile(join(seed, 'logo.png'), new Uint8Array([0x89, 0x50, 0, 255]))
    await git(seed, 'add', '.')
    await git(seed, 'commit', '-q', '-m', 'docs')
    await git(seed, 'push', '-q', 'origin', 'main')
    const clone = hostedClone({
      dataDir: join(base, 'data2'),
      path: 'team/app',
      remoteUrl: `file://${upstream}`,
      user: 'x',
      token: async () => null,
      defaultBranch: async () => 'main',
    })
    const view = await clone.workspace.view!()
    expect([view.label, view.ref]).toEqual(['team/app', 'origin/main'])
    expect(await view.list('.')).toEqual([
      { name: 'docs', kind: 'directory' },
      { name: 'logo.png', kind: 'file' },
      { name: 'NEW.md', kind: 'file' },
      { name: 'README.md', kind: 'file' },
    ])
    expect(await view.list('docs/')).toEqual([{ name: 'a.md', kind: 'file' }])
    expect(await view.readFile('./docs/a.md')).toBe('A\n')
    expect(await view.readFile('docs\\a.md')).toBe('A\n')
    expect(await view.readBytes('logo.png', 4)).toEqual(new Uint8Array([0x89, 0x50, 0, 255]))
    await expect(view.readBytes('logo.png', 3)).rejects.toThrow(FileTooLargeError)
    await expect(view.readFile('docs')).rejects.toThrow('not a file')
    await expect(view.list('README.md')).rejects.toThrow('not a directory')
    await expect(view.readFile('missing.md')).rejects.toThrow('does not exist')
    for (const path of ['../x', 'docs/../../x', '/etc/passwd', 'C:\\x']) {
      await expect(view.readFile(path)).rejects.toThrow(WorkspacePathError)
    }
    // No worktree was made for looking.
    expect(existsSync(join(base, 'data2', 'worktrees'))).toBe(false)
  })
})

describe('repository urls', () => {
  test('the same repository however it is written', () => {
    const expected = 'example.com/team/app'
    expect(repositoryOf('https://example.com/team/app')).toBe(expected)
    expect(repositoryOf('https://user@Example.com/team/app.git/')).toBe(expected)
    expect(repositoryOf('git@example.com:team/app.git')).toBe(expected)
    expect(repositoryOf('ssh://git@example.com:22/team/app')).toBe(expected)
    expect(repositoryOf('https://example.com/team/other')).not.toBe(expected)
    expect(repositoryOf('not a url')).toBeNull()
  })
})

describe('a remote checkout with a hosted repository', () => {
  test('threads start from the newest default branch and push through this machine', async () => {
    const { upstream, checkout, repo, workspace } = await setup()
    const hosted = await withHosting({ workspace }, { repo })
    // The checkout's own branch is left alone: its main is still behind.
    const checkoutMain = await git(checkout, 'rev-parse', 'main')

    const ws = await hosted.workspace!.open({ threadId: 'thr_hosting1' })
    expect(ws.base).toBe('main')
    expect((await ws.exec('ls')).stdout).toContain('NEW.md')
    await ws.writeFile('feature.txt', 'feature\n')
    await ws.exec('git add -A && git commit -qm feature')
    expect(await git(checkout, 'rev-parse', 'main')).toBe(checkoutMain)

    const changes = await hosted.workspace!.branches!.changes('thr_hosting1', 'main')
    expect(changes?.files.map((file) => file.path)).toEqual(['feature.txt'])
    // Branches reach the base through pull requests.
    expect(hosted.workspace!.branches!.merge).toBeUndefined()
    expect(hosted.workspace!.checkout).toBeUndefined()

    const branch = ws.branch!
    const { sha } = await hosted.repo!.push(branch)
    expect(await git(upstream, 'rev-parse', `refs/heads/${branch}`)).toBe(sha)
    expect(await git(checkout, 'rev-parse', `refs/remotes/origin/${branch}`)).toBe(sha)
    // The hosted repository never got the checkout's other refs.
    expect(await git(upstream, 'for-each-ref', '--format=%(refname)')).toBe(
      `refs/heads/${branch}\nrefs/heads/main`,
    )

    // A second commit goes as only what is new.
    await ws.writeFile('feature.txt', 'feature 2\n')
    await ws.exec('git commit -qam more')
    const second = await hosted.repo!.push(branch)
    expect(second.sha).not.toBe(sha)
    expect(await git(upstream, 'rev-parse', `refs/heads/${branch}`)).toBe(second.sha)
  })

  test('fetching brings new commits of the hosted repository to the checkout', async () => {
    const { seed, checkout, repo, workspace } = await setup()
    const hosted = await withHosting({ workspace }, { repo })
    await writeFile(join(seed, 'LATER.md'), 'later\n')
    await git(seed, 'add', '.')
    await git(seed, 'commit', '-q', '-m', 'later')
    await git(seed, 'push', '-q', 'origin', 'main')
    await hosted.repo!.fetch()
    expect(await git(checkout, 'rev-parse', 'refs/remotes/origin/main')).toBe(
      await git(seed, 'rev-parse', 'HEAD'),
    )
  })

  test('a checkout of another repository is refused', async () => {
    const { repo, workspace } = await setup('https://example.com/team/other.git')
    await expect(withHosting({ workspace }, { repo })).rejects.toThrow('not https://example.com')
  })
})
