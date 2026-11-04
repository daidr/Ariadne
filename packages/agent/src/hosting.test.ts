import type { RepoCapability } from '@ariadne/plugin-sdk'
import { run } from '@ariadne/plugin-local'
import { hostedClone } from '@ariadne/plugin-local/hosted'
import { connectRemote, LocalShell } from '@ariadne/remote'
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
