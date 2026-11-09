import {
  FileTooLargeError,
  WorkspacePathError,
  type RepoCapability,
  type SourceView,
} from '@ariadne/plugin-sdk'
import { branchFor, run } from '@ariadne/plugin-local'
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

/** A view's file as text. */
const textOf = async (view: SourceView, path: string) =>
  new TextDecoder().decode(await view.readBytes(path, 1 << 20))

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

describe("a hosted repository's worktrees", () => {
  test("a new one's files are cloned from another thread's, where clones work", async () => {
    const { upstream } = await setup()
    const cloned: string[] = []
    const clone = hostedClone({
      dataDir: join(base, 'data-clones'),
      path: 'team/app',
      remoteUrl: `file://${upstream}`,
      user: 'x',
      token: async () => null,
      defaultBranch: async () => 'main',
      clone: async (from, to) => {
        cloned.push(from)
        return (await run(['cp', '-a', from, to], { cwd: base })).exitCode === 0
      },
    })
    const first = await clone.workspace.open({ threadId: 'thr_a' })
    expect(await clone.workspace.branches!.sharesStorage!()).toBe(true)
    // Nothing to clone from yet: git wrote the first one.
    expect(cloned.filter((path) => path.endsWith('README.md'))).toEqual([])
    await first.writeFile('README.md', '# changed by the first thread\n')
    await first.writeFile('draft.md', 'not committed\n')
    const second = await clone.workspace.open({ threadId: 'thr_b' })
    expect(cloned.filter((path) => path.endsWith('NEW.md'))).toHaveLength(1)
    // What the first thread changed stays its own.
    expect(await second.readFile('README.md')).toBe('# app\n')
    expect(await second.readFile('NEW.md')).toBe('new\n')
    await expect(second.readFile('draft.md')).rejects.toThrow()
    expect((await second.exec('git status --short')).stdout).toBe('')
  })
})

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
    expect(view.ref).toBe('origin/main')
    expect(Object.keys(view).toSorted()).toEqual(['files', 'list', 'readBytes', 'ref', 'search'])
    expect(await view.list('.')).toEqual([
      { name: 'docs', kind: 'directory' },
      { name: 'logo.png', kind: 'file' },
      { name: 'NEW.md', kind: 'file' },
      { name: 'README.md', kind: 'file' },
    ])
    expect(await view.list('docs/')).toEqual([{ name: 'a.md', kind: 'file' }])
    // Finding and searching go through git, on the same commit and without a checkout.
    expect((await view.files!('.')).files).toEqual(['NEW.md', 'README.md', 'docs/a.md', 'logo.png'])
    expect((await view.files!('docs')).files).toEqual(['docs/a.md'])
    expect(await view.search!('^A$', '.')).toEqual({ lines: ['docs/a.md:1:A'], error: null })
    expect((await view.search!('^A$', 'docs')).lines).toEqual(['docs/a.md:1:A'])
    expect((await view.search!('nowhere to be found', '.')).lines).toEqual([])
    expect((await view.search!('(', '.')).error).toBeTruthy()
    expect(await textOf(view, './docs/a.md')).toBe('A\n')
    expect(await textOf(view, 'docs\\a.md')).toBe('A\n')
    expect(await view.readBytes('logo.png', 4)).toEqual(new Uint8Array([0x89, 0x50, 0, 255]))
    await expect(view.readBytes('logo.png', 3)).rejects.toThrow(FileTooLargeError)
    await expect(view.readBytes('docs', 10)).rejects.toThrow('not a file')
    await expect(view.list('README.md')).rejects.toThrow('not a directory')
    await expect(view.readBytes('missing.md', 10)).rejects.toThrow('does not exist')
    for (const path of ['../x', 'docs/../../x', '/etc/passwd', 'C:\\x']) {
      await expect(view.readBytes(path, 10)).rejects.toThrow(WorkspacePathError)
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

    const changes = await hosted.workspace!.branches!.changes(
      { threadId: 'thr_hosting1', branch: branchFor('thr_hosting1') },
      'main',
    )
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

  test("a branch deleted on the hosted repository leaves the checkout's origin too", async () => {
    const { seed, checkout, repo, workspace } = await setup()
    await git(seed, 'push', '-q', 'origin', 'main:refs/heads/topic')
    const hosted = await withHosting({ workspace }, { repo })
    await hosted.repo!.fetch()
    expect(await git(checkout, 'for-each-ref', '--format=%(refname)', 'refs/remotes/')).toBe(
      'refs/remotes/origin/HEAD\nrefs/remotes/origin/main\nrefs/remotes/origin/topic',
    )
    await git(seed, 'push', '-q', 'origin', ':refs/heads/topic')
    await hosted.repo!.fetch()
    // origin/HEAD is a pointer to origin/main, and stays.
    expect(await git(checkout, 'for-each-ref', '--format=%(refname)', 'refs/remotes/')).toBe(
      'refs/remotes/origin/HEAD\nrefs/remotes/origin/main',
    )
    expect(await git(checkout, 'rev-parse', 'refs/remotes/origin/HEAD')).toBe(
      await git(seed, 'rev-parse', 'HEAD'),
    )
  })

  test("a thread's git push goes through Ariadne only", async () => {
    const { upstream, checkout, repo, workspace } = await setup()
    const hosted = await withHosting({ workspace }, { repo })
    // The checkout's own credentials would reach the hosted repository.
    await git(checkout, 'remote', 'set-url', '--push', 'origin', upstream)
    const ws = await hosted.workspace!.open({ threadId: 'thr_hosting2' })
    const pushed = await ws.exec(`git push -q origin HEAD:refs/heads/${ws.branch}`)
    expect(pushed.exitCode).not.toBe(0)
    expect(pushed.stderr).toContain('use-the-push_branch-tool')
    expect(await git(upstream, 'for-each-ref', '--format=%(refname)')).toBe('refs/heads/main')
  })

  test('a checkout of another repository is refused', async () => {
    const { repo, workspace } = await setup('https://example.com/team/other.git')
    await expect(withHosting({ workspace }, { repo })).rejects.toThrow('not https://example.com')
  })
})
