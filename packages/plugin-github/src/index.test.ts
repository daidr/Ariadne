import { branchFor, run } from '@ariadne/plugin-local'
import { existsSync } from 'node:fs'
import { mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import {
  connectRepository,
  githubLinks,
  githubProvider,
  parsePullRequestUrl,
  parseRepository,
} from './index'

let base: string
let server: ReturnType<typeof Bun.serve>
let requests: { method: string; path: string; auth: string | null; body: unknown }[]
let pulls: Record<string, unknown>[]

const git = (cwd: string, ...args: string[]) => run(['git', ...args], { cwd })

/** An issue as GitHub's REST API returns it. */
const rawIssue = (number: number, extra: Record<string, unknown> = {}) => ({
  number,
  html_url: `https://github.com/acme/app/issues/${number}`,
  repository_url: 'https://api.github.com/repos/acme/app',
  title: `Issue ${number}`,
  state: 'open',
  user: { login: 'someone' },
  labels: [{ name: 'bug' }, 'p1'],
  comments: 25,
  body: 'It breaks',
  created_at: '2026-11-01T00:00:00Z',
  updated_at: '2026-11-03T00:00:00Z',
  ...extra,
})

beforeEach(async () => {
  base = await mkdtemp(join(tmpdir(), 'ariadne-github-'))
  requests = []
  pulls = []
  // The remote: a bare repository at <base>/remote/acme/app.git with one commit on main.
  const seed = join(base, 'seed')
  await run(['mkdir', '-p', seed, join(base, 'remote', 'acme')], { cwd: base })
  await git(seed, 'init', '-q', '-b', 'main')
  await git(seed, 'config', 'user.email', 'test@example.com')
  await git(seed, 'config', 'user.name', 'test')
  await writeFile(join(seed, 'README.md'), '# app\n')
  await git(seed, 'add', '.')
  await git(seed, 'commit', '-q', '-m', 'init')
  await git(base, 'clone', '-q', '--bare', seed, join(base, 'remote', 'acme', 'app.git'))

  server = Bun.serve({
    port: 0,
    async fetch(request) {
      const url = new URL(request.url)
      const body =
        request.method === 'POST' || request.method === 'PATCH' ? await request.json() : null
      requests.push({
        method: request.method,
        path: url.pathname + url.search,
        auth: request.headers.get('authorization'),
        body,
      })
      if (url.pathname === '/repos/acme/app') {
        return Response.json({
          default_branch: 'main',
          html_url: 'https://github.com/acme/app',
          clone_url: '',
          private: true,
        })
      }
      if (url.pathname === '/repos/acme/missing') {
        return Response.json({ message: 'Not Found' }, { status: 404 })
      }
      if (url.pathname === '/repos/acme/app/pulls/34') {
        return Response.json({
          number: 34,
          html_url: 'https://github.com/acme/app/pull/34',
          title: '项目用量移到 header',
          state: 'closed',
          merged_at: '2026-11-04T12:00:00Z',
          closed_at: '2026-11-04T12:00:00Z',
          draft: false,
          head: { sha: 'abc', ref: 'feature' },
          base: { ref: 'main' },
          created_at: '2026-11-04T10:00:00Z',
          user: { login: 'daidr' },
          additions: 359,
          deletions: 45,
          changed_files: 19,
        })
      }
      if (url.pathname === '/repos/acme/app/pulls/35') {
        return Response.json({ message: 'Not Found' }, { status: 404 })
      }
      if (url.pathname === '/repos/acme/app/pulls' && request.method === 'GET') {
        return Response.json(pulls)
      }
      if (url.pathname === '/repos/acme/app/pulls' && request.method === 'POST') {
        const input = body as { head: string; base: string; title: string; draft: boolean }
        const pull = {
          number: 7,
          html_url: 'https://github.com/acme/app/pull/7',
          title: input.title,
          state: 'open',
          merged_at: null,
          draft: input.draft,
          head: { sha: 'abc', ref: input.head },
          base: { ref: input.base },
          created_at: '2026-11-03T00:00:00Z',
        }
        pulls.push(pull)
        return Response.json(pull, { status: 201 })
      }
      if (url.pathname === '/repos/acme/app/commits/abc/check-runs') {
        return Response.json({
          check_runs: [
            { id: 11, name: 'test', status: 'completed', conclusion: 'failure', html_url: 'u' },
            { id: 12, name: 'lint', status: 'in_progress', conclusion: null, html_url: null },
          ],
        })
      }
      if (url.pathname === '/repos/acme/app/commits/abc/status') {
        return Response.json({
          statuses: [{ context: 'deploy', state: 'success', target_url: null }],
        })
      }
      if (url.pathname === '/repos/acme/app/issues' && request.method === 'GET') {
        // The issues endpoint lists pull requests too.
        return Response.json([rawIssue(3), rawIssue(7, { pull_request: {} })])
      }
      if (url.pathname === '/search/issues') {
        // A search can reach other repositories the token reads; those must not come back.
        return Response.json({
          items: [
            rawIssue(4),
            rawIssue(5, { repository_url: 'https://api.github.com/repos/acme/secret' }),
          ],
        })
      }
      if (url.pathname === '/repos/acme/app/issues/3' && request.method === 'GET') {
        return Response.json(rawIssue(3))
      }
      if (url.pathname === '/repos/acme/app/issues/3' && request.method === 'PATCH') {
        return Response.json(rawIssue(3, { state: (body as { state: string }).state }))
      }
      if (url.pathname === '/repos/acme/app/issues/3/comments' && request.method === 'GET') {
        return Response.json([
          {
            id: 1,
            user: null,
            body: url.searchParams.get('page') === '2' ? 'newest' : 'older',
            created_at: 'x',
            html_url: 'c1',
          },
        ])
      }
      if (url.pathname === '/repos/acme/app/issues/3/comments' && request.method === 'POST') {
        const { body: text } = body as { body: string }
        return Response.json(
          { id: 2, user: { login: 'me' }, body: text, created_at: 'y', html_url: 'c2' },
          { status: 201 },
        )
      }
      if (url.pathname === '/repos/acme/app/actions/jobs/11/logs') {
        return new Response('step 1\nError: expected 1 to be 2\n')
      }
      return new Response('nope', { status: 500 })
    },
  })
})

afterEach(async () => {
  server.stop(true)
  await rm(base, { recursive: true, force: true })
})

const options = () => ({
  dataDir: join(base, 'data'),
  apiBase: `http://localhost:${server.port}`,
  gitBase: `file://${join(base, 'remote')}`,
  env: { GH_TOKEN: 'tok' },
})

describe('github repository', () => {
  test('reads the repository from a slug or a URL', () => {
    expect(parseRepository('acme/app')).toEqual({ owner: 'acme', name: 'app' })
    expect(parseRepository('https://github.com/acme/app.git')).toEqual({
      owner: 'acme',
      name: 'app',
    })
    expect(parseRepository('git@github.com:acme/app.git')).toEqual({ owner: 'acme', name: 'app' })
    expect(() => parseRepository('not a repo')).toThrow()
  })

  test('connecting checks access and reports GitHub’s message', async () => {
    const provider = githubProvider(options())
    await expect(provider.connect({ repository: 'acme/missing' })).rejects.toThrow(/Not Found/)
    expect(requests[0]?.auth).toBe('Bearer tok')
  })

  test('threads get worktrees from the default branch, push their branch, and open a PR', async () => {
    const { workspace, repo } = connectRepository({ repository: 'acme/app' }, options())
    const [a, b] = await Promise.all([
      workspace.open({ threadId: 'thr_aaaaaaaaaaaa' }),
      workspace.open({ threadId: 'thr_bbbbbbbbbbbb' }),
    ])
    expect(a.branch).toBe('ariadne/aaaaaaaaaaaa')
    expect(await a.readFile('README.md')).toBe('# app\n')
    await a.writeFile('feature.txt', 'x\n')
    await expect(b.readFile('feature.txt')).rejects.toThrow()

    check(await a.exec('git add . && git -c user.email=t@e -c user.name=t commit -qm feature'))
    const { sha } = await repo.push(a.branch!)
    const remote = await git(join(base, 'remote', 'acme', 'app.git'), 'rev-parse', a.branch!)
    expect(remote.stdout.trim()).toBe(sha)

    expect(await repo.pullRequestFor(a.branch!)).toBeNull()
    const pr = await repo.openPullRequest({ branch: a.branch!, title: 'Add feature', body: 'b' })
    expect(pr).toMatchObject({ number: 7, state: 'open', base: 'main' })
    expect(requests.find((r) => r.method === 'POST')?.body).toMatchObject({
      head: 'ariadne/aaaaaaaaaaaa',
      base: 'main',
    })
    expect((await repo.pullRequestFor(a.branch!))?.number).toBe(7)
  })

  test('changes compare against the default branch; nothing merges here', async () => {
    const { workspace } = connectRepository({ repository: 'acme/app' }, options())
    const ws = await workspace.open({ threadId: 'thr_ffffffffffff' })
    expect(ws.base).toBe('main')
    await ws.exec(
      'echo x > x.txt && git add . && git -c user.email=t@e -c user.name=t commit -qm x',
    )
    const branches = workspace.branches!
    expect(branches.merge).toBeUndefined()
    const changes = await branches.changes(
      { threadId: 'thr_ffffffffffff', branch: branchFor('thr_ffffffffffff') },
      'main',
    )
    expect(changes?.files.map((f) => [f.path, f.status])).toEqual([['x.txt', 'added']])
    expect(
      await branches.merged(
        { threadId: 'thr_ffffffffffff', branch: branchFor('thr_ffffffffffff') },
        'main',
      ),
    ).toBe(false)
    expect((await branches.worktrees()).map((w) => w.threadId)).toEqual(['thr_ffffffffffff'])
    expect(await branches.remove('thr_ffffffffffff')).toBe(true)
    expect(await branches.worktrees()).toEqual([])
  })

  test('reads checks and the log of a failed job', async () => {
    const { repo } = connectRepository({ repository: 'acme/app' }, options())
    const checks = await repo.checks('abc')
    expect(checks.map((c) => [c.name, c.status, c.conclusion])).toEqual([
      ['test', 'completed', 'failure'],
      ['lint', 'in_progress', null],
      ['deploy', 'completed', 'success'],
    ])
    expect(await repo.checkLog('11')).toContain('expected 1 to be 2')
  })

  test('a push never overwrites a branch that moved on the remote', async () => {
    const { workspace, repo } = connectRepository({ repository: 'acme/app' }, options())
    const ws = await workspace.open({ threadId: 'thr_cccccccccccc' })
    await ws.exec('echo 1 > a && git add . && git -c user.email=t@e -c user.name=t commit -qm one')
    await repo.push(ws.branch!)
    // Someone else rewrites the branch on the remote.
    const other = join(base, 'other')
    await git(base, 'clone', '-q', join(base, 'remote', 'acme', 'app.git'), other)
    await git(other, 'checkout', '-q', ws.branch!)
    await run(
      ['git', '-c', 'user.email=t@e', '-c', 'user.name=t', 'commit', '--amend', '-qm', 'r'],
      {
        cwd: other,
      },
    )
    await git(other, 'push', '-q', '-f', 'origin', ws.branch!)
    await ws.exec('echo 2 > b && git add . && git -c user.email=t@e -c user.name=t commit -qm two')
    await expect(repo.push(ws.branch!)).rejects.toThrow(/rejected.*sync_branch/)

    // Fetching brings the remote's version in; after a merge the push goes through.
    await repo.fetch()
    check(
      await ws.exec(
        `git -c user.email=t@e -c user.name=t merge -q --no-edit -X ours origin/${ws.branch}`,
      ),
    )
    await expect(repo.push(ws.branch!)).resolves.toMatchObject({ sha: expect.any(String) })
  })

  test('the default branch moving on is visible after a fetch', async () => {
    const { workspace, repo } = connectRepository({ repository: 'acme/app' }, options())
    const ws = await workspace.open({ threadId: 'thr_dddddddddddd' })
    const other = join(base, 'other2')
    await git(base, 'clone', '-q', join(base, 'remote', 'acme', 'app.git'), other)
    await writeFile(join(other, 'new.txt'), 'n\n')
    await git(other, 'add', '.')
    await run(['git', '-c', 'user.email=t@e', '-c', 'user.name=t', 'commit', '-qm', 'n'], {
      cwd: other,
    })
    await git(other, 'push', '-q', 'origin', 'main')
    expect((await ws.exec('git rev-list --count HEAD..origin/main')).stdout.trim()).toBe('0')
    await repo.fetch()
    expect((await ws.exec('git rev-list --count HEAD..origin/main')).stdout.trim()).toBe('1')
  })

  test('workspaces cannot push themselves or redirect where the server pushes', async () => {
    const { workspace, repo } = connectRepository({ repository: 'acme/app' }, options())
    const ws = await workspace.open({ threadId: 'thr_eeeeeeeeeeee' })
    await ws.exec('echo 1 > a && git add . && git -c user.email=t@e -c user.name=t commit -qm one')
    const direct = await ws.exec(`git push origin ${ws.branch}`)
    expect(direct.exitCode).not.toBe(0)
    // Rewriting origin in the shared clone does not change the server's push target.
    await ws.exec(`git remote set-url origin file://${join(base, 'elsewhere.git')}`)
    await ws.exec(
      `printf '#!/bin/sh\nexit 1\n' > "$(git rev-parse --git-common-dir)/hooks/pre-push" && chmod +x "$(git rev-parse --git-common-dir)/hooks/pre-push"`,
    )
    const { sha } = await repo.push(ws.branch!)
    const remote = await git(join(base, 'remote', 'acme', 'app.git'), 'rev-parse', ws.branch!)
    expect(remote.stdout.trim()).toBe(sha)
    expect(await repo.isPublic()).toBe(false)
  })

  test("what a workspace writes into the shared clone's config never runs in the server's git", async () => {
    const { workspace, repo } = connectRepository({ repository: 'acme/app' }, options())
    const ws = await workspace.open({ threadId: 'thr_ffffffffffff' })
    const marker = join(base, 'ran')
    // The workspace can write the config; the server's git must still
    // ignore what would run a command.
    const remote = `file://${join(base, 'remote')}/`
    await ws.exec('echo 1 > a && git add . && git -c user.email=t@e -c user.name=t commit -qm one')
    const planted = await ws.exec(
      [
        'git config protocol.ext.allow always',
        `git config 'url.ext::sh -c touch% ${marker}% #.insteadOf' '${remote}'`,
        `git config core.fsmonitor 'touch ${marker}-fsmonitor; false'`,
      ].join(' && '),
    )
    expect(planted.exitCode).toBe(0)
    await expect(repo.fetch()).rejects.toThrow(/not allowed/)
    await repo.push(ws.branch!).catch(() => undefined)
    await workspace.open({ threadId: 'thr_gggggggggggg' }).catch(() => undefined)
    expect(existsSync(marker)).toBe(false)
    expect(existsSync(`${marker}-fsmonitor`)).toBe(false)
  })
})

function check(result: { exitCode: number; stderr: string }) {
  if (result.exitCode !== 0) throw new Error(result.stderr)
}

describe('github issues', () => {
  test('lists issues without pull requests, and searches within the repository', async () => {
    const { tracker } = connectRepository({ repository: 'acme/app' }, options())
    const issues = await tracker.list({ labels: ['bug'] })
    expect(issues.map((i) => i.number)).toEqual([3])
    expect(issues[0]).toMatchObject({ labels: ['bug', 'p1'], author: 'someone', comments: 25 })
    expect(requests.at(-1)?.path).toContain('labels=bug')
    const found = await tracker.list({
      search: 'login repo:acme/secret OR -org:acme is:pr NOT crash',
      state: 'all',
    })
    expect(found.map((i) => i.number)).toEqual([4])
    const query = new URL(`http://x${requests.at(-1)!.path}`).searchParams.get('q')
    expect(query).toBe('login crash repo:acme/app is:issue')
    expect(requests.at(-1)?.auth).toBe('Bearer tok')
  })

  test('reads an issue with its newest comments, comments, and closes it', async () => {
    const { tracker } = connectRepository({ repository: 'acme/app' }, options())
    const issue = await tracker.issue(3)
    expect(issue.body).toBe('It breaks')
    expect(issue.recent.map((c) => c.body)).toEqual(['older', 'newest'])
    // 25 comments, 20 a page: the newest are on page 2.
    expect(
      requests.filter((r) => r.path.includes('/comments?')).map((r) => r.path.slice(-6)),
    ).toEqual(['page=2', 'page=1'])
    const comment = await tracker.comment(3, 'Fixed in #7')
    expect(comment).toMatchObject({ id: '2', url: 'c2', body: 'Fixed in #7' })
    const closed = await tracker.setState(3, 'closed', 'not_planned')
    expect(closed.state).toBe('closed')
    expect(requests.at(-1)?.body).toEqual({ state: 'closed', state_reason: 'not_planned' })
  })
})

const at = (url: string, web = 'https://github.com') => parsePullRequestUrl(new URL(url), web)

describe('pull request links', () => {
  test('reads pull request pages of the host, and nothing else', () => {
    expect(at('https://github.com/acme/app/pull/34')).toEqual({
      owner: 'acme',
      name: 'app',
      number: 34,
    })
    expect(at('https://github.com/acme/app/pull/34/files#diff-1')).toMatchObject({ number: 34 })
    expect(at('https://github.com/acme/app/pulls')).toBeNull()
    expect(at('https://github.com/acme/app/issues/34')).toBeNull()
    expect(at('http://github.com/acme/app/pull/34')).toBeNull()
    expect(at('https://github.com.evil.example/acme/app/pull/34')).toBeNull()
    expect(at('https://ghe.example.com/acme/app/pull/1', 'https://ghe.example.com')).toMatchObject({
      number: 1,
    })
    expect(at('https://github.com/acme/app/pull/1', 'https://ghe.example.com')).toBeNull()
  })

  test('names a link and shows its card with the token', async () => {
    const links = githubLinks({
      apiBase: `http://localhost:${server.port}`,
      env: { GH_TOKEN: 'tok' },
    })
    expect(links.match(new URL('https://github.com/acme/app/pull/34'))).toEqual({
      kind: 'pull-request',
      label: 'acme/app#34',
    })
    expect(links.match(new URL('https://github.com/acme/app'))).toBeNull()
    expect(await links.preview(new URL('https://github.com/acme/app/pull/34'), {})).toEqual({
      kind: 'pull-request',
      repository: 'acme/app',
      number: 34,
      url: 'https://github.com/acme/app/pull/34',
      title: '项目用量移到 header',
      state: 'merged',
      draft: false,
      author: 'daidr',
      additions: 359,
      deletions: 45,
      changedFiles: 19,
      at: '2026-11-04T12:00:00Z',
    })
    expect(requests.at(-1)).toMatchObject({ path: '/repos/acme/app/pulls/34', auth: 'Bearer tok' })
    expect(await links.preview(new URL('https://github.com/acme/app/pull/35'), {})).toBeNull()
  })
})
