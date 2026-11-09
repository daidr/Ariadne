import { run } from '@ariadne/plugin-local'
import { mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import {
  instanceDirs,
  connectRepository,
  GitLabInstances,
  gitlabLinks,
  gitlabProvider,
  parseMergeRequestUrl,
  parseInstance,
  parseRepository,
  plainLog,
} from './index'

let base: string
let server: ReturnType<typeof Bun.serve>
let instance: string
let requests: { method: string; path: string; auth: string | null; body: unknown }[]
let mergeRequests: Record<string, unknown>[]
let visibility: string

const git = (cwd: string, ...args: string[]) => run(['git', ...args], { cwd })

/** The project's path as the API takes it: one URL-encoded segment. */
const P = '/api/v4/projects/acme%2Ftools%2Fapp'

/** An issue as GitLab's REST API returns it. */
const rawIssue = (iid: number, extra: Record<string, unknown> = {}) => ({
  iid,
  id: 1000 + iid,
  web_url: `https://gitlab.example.com/acme/tools/app/-/issues/${iid}`,
  title: `Issue ${iid}`,
  state: 'opened',
  author: { username: 'someone' },
  labels: ['bug', 'p1'],
  user_notes_count: 2,
  description: 'It breaks',
  created_at: '2026-11-01T00:00:00Z',
  updated_at: '2026-11-03T00:00:00Z',
  ...extra,
})

const rawMergeRequest = (iid: number, extra: Record<string, unknown> = {}) => ({
  iid,
  web_url: `https://gitlab.example.com/acme/tools/app/-/merge_requests/${iid}`,
  title: 'Add feature',
  state: 'opened',
  draft: false,
  sha: 'abc',
  source_branch: 'ariadne/aaaaaaaaaaaa',
  source_project_id: 42,
  target_branch: 'main',
  created_at: '2026-11-03T00:00:00Z',
  ...extra,
})

beforeEach(async () => {
  base = await mkdtemp(join(tmpdir(), 'ariadne-gitlab-'))
  requests = []
  mergeRequests = []
  visibility = 'private'
  // The remote: a bare repository at <base>/remote/acme/tools/app.git with one commit on main.
  const seed = join(base, 'seed')
  await run(['mkdir', '-p', seed, join(base, 'remote', 'acme', 'tools')], { cwd: base })
  await git(seed, 'init', '-q', '-b', 'main')
  await git(seed, 'config', 'user.email', 'test@example.com')
  await git(seed, 'config', 'user.name', 'test')
  await writeFile(join(seed, 'README.md'), '# app\n')
  await git(seed, 'add', '.')
  await git(seed, 'commit', '-q', '-m', 'init')
  await git(base, 'clone', '-q', '--bare', seed, join(base, 'remote', 'acme', 'tools', 'app.git'))

  server = Bun.serve({
    port: 0,
    async fetch(request) {
      const url = new URL(request.url)
      // The raw path: the project's id must arrive encoded as one segment.
      const raw = request.url.slice(url.origin.length).split('?')[0]!
      const body = ['POST', 'PUT'].includes(request.method) ? await request.json() : null
      requests.push({
        method: request.method,
        path: raw + url.search,
        auth: request.headers.get('authorization'),
        body,
      })
      const route = `${request.method} ${raw}`
      switch (route) {
        case `GET ${P}`:
          return Response.json({
            id: 42,
            default_branch: 'main',
            web_url: 'https://gitlab.example.com/acme/tools/app',
            path_with_namespace: 'acme/tools/app',
            visibility,
          })
        case 'GET /api/v4/projects/acme%2Fmissing':
          return Response.json({ message: '404 Project Not Found' }, { status: 404 })
        case 'GET /api/v4/projects/acme%2Fmoved':
          return new Response(null, {
            status: 301,
            headers: { location: 'https://evil.example/api/v4/projects/x' },
          })
        case `GET ${P}/merge_requests`:
          return Response.json(mergeRequests)
        case `POST ${P}/merge_requests`: {
          const input = body as { source_branch: string; target_branch: string; title: string }
          const mr = rawMergeRequest(7, {
            title: input.title,
            draft: input.title.startsWith('Draft:'),
            source_branch: input.source_branch,
            target_branch: input.target_branch,
          })
          mergeRequests.push(mr)
          return Response.json(mr, { status: 201 })
        }
        case `GET ${P}/merge_requests/8`:
          return Response.json(
            rawMergeRequest(8, {
              state: 'merged',
              author: { username: 'daidr' },
              changes_count: '12',
              merged_at: '2026-11-04T12:00:00Z',
              closed_at: null,
            }),
          )
        case `GET ${P}/merge_requests/9`:
          return Response.json(
            rawMergeRequest(9, {
              draft: true,
              author: null,
              changes_count: '1000+',
              merged_at: null,
              closed_at: null,
            }),
          )
        case `GET ${P}/merge_requests/10`:
          return Response.json({ message: '404 Not found' }, { status: 404 })
        case `GET ${P}/merge_requests/7`:
          return Response.json(rawMergeRequest(7, { state: 'merged' }))
        case `GET ${P}/repository/commits/abc/statuses`:
          return Response.json([
            { id: 10, name: 'test', status: 'failed', allow_failure: false, target_url: 'u10' },
            { id: 11, name: 'test', status: 'failed', allow_failure: false, target_url: 'u11' },
            { id: 12, name: 'lint', status: 'running', target_url: null },
            { id: 13, name: 'flaky', status: 'failed', allow_failure: true, target_url: null },
            { id: 14, name: 'deploy', status: 'manual', target_url: null },
            { id: 15, name: 'external', status: 'success', target_url: null },
          ])
        case `GET ${P}/jobs/11/trace`:
          return new Response(
            'section_start:1700000000:step_script\r\x1b[0K\x1b[32;1m$ bun test\x1b[0;m\nError: expected 1 to be 2\nsection_end:1700000001:step_script\r\x1b[0K',
          )
        case `GET ${P}/jobs/15/trace`:
          return Response.json({ message: '404 Not found' }, { status: 404 })
        case `GET ${P}/issues`:
          return Response.json([rawIssue(3), rawIssue(4, { state: 'closed' })])
        case `GET ${P}/issues/3`:
          return Response.json(rawIssue(3))
        case `GET ${P}/issues/3/notes`:
          // Newest first, system notes mixed in.
          return Response.json([
            { id: 3, author: { username: 'b' }, body: 'newest', created_at: 'z', system: false },
            { id: 2, author: null, body: 'added ~bug label', created_at: 'y', system: true },
            { id: 1, author: { username: 'a' }, body: 'older', created_at: 'x', system: false },
          ])
        case `POST ${P}/issues/3/notes`: {
          const { body: text } = body as { body: string }
          return Response.json(
            { id: 9, author: { username: 'me' }, body: text, created_at: 'w', system: false },
            { status: 201 },
          )
        }
        case `PUT ${P}/issues/3`: {
          const event = (body as { state_event: string }).state_event
          return Response.json(rawIssue(3, { state: event === 'close' ? 'closed' : 'opened' }))
        }
      }
      return new Response('nope', { status: 500 })
    },
  })
  instance = `http://localhost:${server.port}`
})

afterEach(async () => {
  server.stop(true)
  await rm(base, { recursive: true, force: true })
})

const options = (env: Record<string, string> = { GITLAB_HOST: instance, GITLAB_TOKEN: 'tok' }) => ({
  dataDir: join(base, 'data'),
  gitBase: `file://${join(base, 'remote')}`,
  env: { ...env },
})

const settings = () => ({ repository: 'acme/tools/app', instance })

function check(result: { exitCode: number; stderr: string }) {
  if (result.exitCode !== 0) throw new Error(result.stderr)
}

describe('gitlab settings', () => {
  test('a token belongs to its instance, however the project is written', () => {
    const goes = gitlabProvider({ dataDir: '/nowhere' }).secretsFor!
    const corp = goes({ repository: 'me/app', instance: 'https://gitlab.corp.example' })
    expect(goes({ repository: 'https://gitlab.corp.example/me/lib.git' })).toBe(corp)
    expect(goes({ repository: 'me/lib', instance: 'https://gitlab.corp.example/' })).toBe(corp)
    expect(goes({ repository: 'x/y', instance: 'https://evil.example' })).not.toBe(corp)
    expect(goes({ repository: 'x/y' })).toBe('https://gitlab.com')
    expect(() => goes({})).toThrow()
  })

  test('reads the project from a path or a URL, with subgroups and self-managed instances', () => {
    expect(parseRepository('acme/app')).toEqual({
      instance: 'https://gitlab.com',
      path: 'acme/app',
    })
    expect(parseRepository('https://gitlab.com/acme/tools/app.git')).toEqual({
      instance: 'https://gitlab.com',
      path: 'acme/tools/app',
    })
    expect(parseRepository('https://git.example.com/acme/app/-/issues/3')).toEqual({
      instance: 'https://git.example.com',
      path: 'acme/app',
    })
    expect(parseRepository('git@git.example.com:acme/tools/app.git')).toEqual({
      instance: 'https://git.example.com',
      path: 'acme/tools/app',
    })
    expect(parseRepository('ssh://git@git.example.com:2222/acme/app.git')).toEqual({
      instance: 'https://git.example.com',
      path: 'acme/app',
    })
    // An instance under a path: the project is what follows it.
    expect(
      parseRepository('https://example.com/gitlab/acme/app', 'https://example.com/gitlab/'),
    ).toEqual({ instance: 'https://example.com/gitlab', path: 'acme/app' })
    expect(parseRepository('acme/app', 'https://example.com/gitlab')).toEqual({
      instance: 'https://example.com/gitlab',
      path: 'acme/app',
    })
    expect(() => parseRepository('https://gitlab.com/acme/app', 'https://git.example.com')).toThrow(
      /not on the instance/,
    )
    expect(() => parseRepository('app')).toThrow()
    expect(() => parseRepository('acme/../app')).toThrow()
  })

  test('tokens only go to https instances, or to this machine', () => {
    expect(parseInstance('https://git.example.com/')).toBe('https://git.example.com')
    expect(parseInstance('http://localhost:8080')).toBe('http://localhost:8080')
    expect(() => parseInstance('http://git.example.com')).toThrow(/not https/)
    expect(() => parseInstance('https://user:pw@git.example.com')).toThrow()
    expect(() => parseInstance('git.example.com')).toThrow()
  })

  test('connecting checks access and reports GitLab’s message', async () => {
    const provider = gitlabProvider(options())
    await expect(provider.connect({ repository: 'acme/missing', instance })).rejects.toThrow(
      /404 Project Not Found/,
    )
    expect(requests[0]?.auth).toBe('Bearer tok')
    // A redirect is not followed with the token.
    await expect(provider.connect({ repository: 'acme/moved', instance })).rejects.toThrow(
      /301 moved/,
    )
    expect(requests).toHaveLength(2)
  })

  test('a token from the environment only goes to the host it is meant for', async () => {
    // GITLAB_TOKEN without GITLAB_HOST is for GitLab.com, not this instance.
    const { api } = connectRepository(settings(), options({ GITLAB_TOKEN: 'for-gitlab-com' }))
    await api.project()
    expect(requests.at(-1)?.auth).toBeNull()
    const configured = connectRepository({ ...settings(), token: 'mine' }, options({}))
    await configured.api.project()
    expect(requests.at(-1)?.auth).toBe('Bearer mine')
  })

  test('glab is asked for its own token for the host, never one from the environment', async () => {
    const calls: { argv: string[]; env?: Record<string, string> }[] = []
    const fakeRun: typeof run = async (argv, opts) => {
      calls.push({ argv, ...(opts.env ? { env: opts.env } : {}) })
      return { exitCode: 0, stdout: 'from-glab\n', stderr: '' }
    }
    const { api } = connectRepository(settings(), {
      ...options({ GITLAB_TOKEN: 'for-gitlab-com' }),
      run: fakeRun,
    })
    await api.project()
    expect(requests.at(-1)?.auth).toBe('Bearer from-glab')
    expect(calls[0]?.argv).toEqual([
      'glab',
      'config',
      'get',
      'token',
      '--host',
      `localhost:${server.port}`,
    ])
    // glab reads these before its own configuration; each is cleared for the call.
    for (const name of [
      'GITLAB_TOKEN',
      'GITLAB_ACCESS_TOKEN',
      'OAUTH_TOKEN',
      'GL_TOKEN',
      'CI_JOB_TOKEN',
    ]) {
      expect(calls[0]?.env?.[name]).toBe('')
    }
  })

  test('the same path on two instances gets two clones', () => {
    expect(instanceDirs('https://gitlab.com')).toEqual(['gitlab.com'])
    expect(instanceDirs('https://example.com:8443/gitlab')).toEqual(['example.com:8443', 'gitlab'])
  })
})

describe('gitlab repository', () => {
  test('threads get worktrees from the default branch, push their branch, and open a merge request', async () => {
    const { workspace, repo } = connectRepository(settings(), options())
    const [a, b] = await Promise.all([
      workspace.open({ threadId: 'thr_aaaaaaaaaaaa' }),
      workspace.open({ threadId: 'thr_bbbbbbbbbbbb' }),
    ])
    expect(a.branch).toBe('ariadne/aaaaaaaaaaaa')
    expect(a.label).toContain(
      join('worktrees', `localhost:${server.port}`, 'acme', 'tools', 'app', 'thr_aaaaaaaaaaaa'),
    )
    expect(await a.readFile('README.md')).toBe('# app\n')
    await a.writeFile('feature.txt', 'x\n')
    await expect(b.readFile('feature.txt')).rejects.toThrow()

    check(await a.exec('git add . && git -c user.email=t@e -c user.name=t commit -qm feature'))
    const { sha } = await repo.push(a.branch!)
    const remote = await git(
      join(base, 'remote', 'acme', 'tools', 'app.git'),
      'rev-parse',
      a.branch!,
    )
    expect(remote.stdout.trim()).toBe(sha)
    // A plain push from the workspace goes nowhere.
    expect((await a.exec(`git push origin ${a.branch}`)).exitCode).not.toBe(0)

    expect(await repo.pullRequestFor(a.branch!)).toBeNull()
    const mr = await repo.openPullRequest({
      branch: a.branch!,
      title: 'Add feature',
      body: 'b',
      draft: true,
    })
    expect(mr).toMatchObject({
      number: 7,
      reference: '!7',
      state: 'open',
      base: 'main',
      draft: true,
    })
    expect(requests.find((r) => r.method === 'POST')?.body).toEqual({
      source_branch: 'ariadne/aaaaaaaaaaaa',
      target_branch: 'main',
      title: 'Draft: Add feature',
      description: 'b',
    })
    expect((await repo.pullRequestFor(a.branch!))?.number).toBe(7)
    expect((await repo.pullRequest(7)).state).toBe('merged')
    expect(await repo.isPublic()).toBe(false)
  })

  test('a merge request from a fork with the same branch name is not the thread’s', async () => {
    const { repo } = connectRepository(settings(), options())
    mergeRequests.push(rawMergeRequest(5, { source_project_id: 99 }))
    expect(await repo.pullRequestFor('ariadne/aaaaaaaaaaaa')).toBeNull()
    mergeRequests.push(
      rawMergeRequest(6, { state: 'closed', created_at: '2026-11-04T00:00:00Z' }),
      rawMergeRequest(4),
    )
    // The open one wins over a newer closed one.
    expect((await repo.pullRequestFor('ariadne/aaaaaaaaaaaa'))?.number).toBe(4)
  })

  test('internal projects count as public: everyone on the instance reads them', async () => {
    const { repo } = connectRepository(settings(), options())
    visibility = 'internal'
    expect(await repo.isPublic()).toBe(true)
  })

  test('reads pipeline jobs as checks, and a failed job’s log without escapes', async () => {
    const { repo } = connectRepository(settings(), options())
    const checks = await repo.checks('abc')
    expect(checks.map((c) => [c.id, c.name, c.status, c.conclusion])).toEqual([
      ['11', 'test', 'completed', 'failure'],
      ['12', 'lint', 'in_progress', null],
      ['13', 'flaky', 'completed', 'neutral'],
      ['14', 'deploy', 'completed', 'skipped'],
      ['15', 'external', 'completed', 'success'],
    ])
    const log = await repo.checkLog('11')
    expect(log).toBe('$ bun test\nError: expected 1 to be 2\n')
    expect(await repo.checkLog('15')).toMatch(/no log here/)
    await expect(repo.checkLog('../x')).rejects.toThrow()
  })

  test('strips colours and section markers from logs', () => {
    expect(plainLog('a\r\x1b[0Kb\x1b[31mred\x1b[0m\r\n')).toBe('a\nbred\r\n')
  })
})

describe('gitlab issues', () => {
  test('lists issues by state, labels and words within the project', async () => {
    const { tracker } = connectRepository(settings(), options())
    const issues = await tracker.list({ labels: ['bug', 'p1'], search: 'login crash' })
    expect(issues.map((i) => [i.number, i.state])).toEqual([
      [3, 'open'],
      [4, 'closed'],
    ])
    expect(issues[0]).toMatchObject({ labels: ['bug', 'p1'], author: 'someone', comments: 2 })
    const params = new URL(`http://x${requests.at(-1)!.path}`).searchParams
    expect(params.get('state')).toBe('opened')
    expect(params.get('labels')).toBe('bug,p1')
    expect(params.get('search')).toBe('login crash')
    await tracker.list({ state: 'all' })
    expect(new URL(`http://x${requests.at(-1)!.path}`).searchParams.has('state')).toBe(false)
    expect(tracker.webUrl).toBe(`${instance}/acme/tools/app/-/issues`)
  })

  test('reads an issue with its newest comments, comments, and closes it', async () => {
    const { tracker } = connectRepository(settings(), options())
    const issue = await tracker.issue(3)
    expect(issue.body).toBe('It breaks')
    // Oldest first, without what GitLab notes by itself.
    expect(issue.recent.map((c) => c.body)).toEqual(['older', 'newest'])
    expect(issue.recent[1]?.url).toBe(`${instance}/acme/tools/app/-/issues/3#note_3`)
    const comment = await tracker.comment(3, 'Fixed in !7')
    expect(comment).toMatchObject({ id: '9', body: 'Fixed in !7' })
    const closed = await tracker.setState(3, 'closed', 'not_planned')
    expect(closed.state).toBe('closed')
    expect(requests.at(-1)?.body).toEqual({ state_event: 'close' })
    await tracker.setState(3, 'open')
    expect(requests.at(-1)?.body).toEqual({ state_event: 'reopen' })
  })
})

const at = (url: string, home = 'https://gitlab.com') => parseMergeRequestUrl(new URL(url), home)

describe('merge request links', () => {
  test('reads merge request pages of the instance, and nothing else', () => {
    expect(at('https://gitlab.com/acme/tools/app/-/merge_requests/12')).toEqual({
      path: 'acme/tools/app',
      iid: 12,
    })
    expect(at('https://gitlab.com/acme/app/-/merge_requests/12/diffs#note_1')).toMatchObject({
      iid: 12,
    })
    expect(at('https://gitlab.com/acme/app/-/merge_requests')).toBeNull()
    expect(at('https://gitlab.com/acme/app/-/issues/12')).toBeNull()
    expect(at('https://gitlab.com/app/-/merge_requests/12')).toBeNull()
    expect(at('http://gitlab.com/acme/app/-/merge_requests/12')).toBeNull()
    expect(at('https://gitlab.com.evil.example/acme/app/-/merge_requests/12')).toBeNull()
    const nested = 'https://example.com/gitlab'
    expect(at('https://example.com/gitlab/acme/app/-/merge_requests/3', nested)).toEqual({
      path: 'acme/app',
      iid: 3,
    })
    expect(at('https://example.com/acme/app/-/merge_requests/3', nested)).toBeNull()
  })

  test('knows GitLab.com, the GITLAB_HOST instance and connected ones', async () => {
    const instances = new GitLabInstances({ GITLAB_HOST: 'gitlab.corp.example' })
    const links = gitlabLinks(instances, { env: {} })
    const match = (url: string) => links.match(new URL(url))
    expect(match('https://gitlab.com/acme/app/-/merge_requests/1')).toEqual({
      kind: 'pull-request',
      label: 'acme/app!1',
    })
    expect(match('https://gitlab.corp.example/acme/app/-/merge_requests/2')).toMatchObject({
      label: 'acme/app!2',
    })
    expect(match(`${instance}/acme/tools/app/-/merge_requests/8`)).toBeNull()
    // Connecting a project on the instance makes its links known.
    await gitlabProvider({ ...options(), instances }).connect({ ...settings(), token: 'given' })
    expect(match(`${instance}/acme/tools/app/-/merge_requests/8`)).toMatchObject({
      label: 'acme/tools/app!8',
    })
  })

  test('shows the card with the token the instance was given', async () => {
    const instances = new GitLabInstances({})
    instances.add(instance, 'acme/other', 'other-project')
    instances.add(instance, 'acme/tools/app', 'given')
    const links = gitlabLinks(instances, { env: {} })
    expect(
      await links.preview(new URL(`${instance}/acme/tools/app/-/merge_requests/8`), {}),
    ).toEqual({
      kind: 'pull-request',
      repository: 'acme/tools/app',
      number: 8,
      reference: '!8',
      url: 'https://gitlab.example.com/acme/tools/app/-/merge_requests/8',
      title: 'Add feature',
      state: 'merged',
      draft: false,
      author: 'daidr',
      additions: null,
      deletions: null,
      changedFiles: 12,
      at: '2026-11-04T12:00:00Z',
    })
    // The linked project's own token first.
    expect(requests.at(-1)).toMatchObject({
      path: `${P}/merge_requests/8`,
      auth: 'Bearer given',
    })
    expect(
      await links.preview(new URL(`${instance}/acme/tools/app/-/merge_requests/9`), {}),
    ).toMatchObject({ draft: true, author: null, changedFiles: null, at: '2026-11-03T00:00:00Z' })
    // Not there for any token: each is tried once, the other project's too.
    const before = requests.length
    expect(
      await links.preview(new URL(`${instance}/acme/tools/app/-/merge_requests/10`), {}),
    ).toBeNull()
    expect(requests.slice(before).map((request) => request.auth)).toEqual([
      'Bearer given',
      'Bearer other-project',
      null,
    ])
  })
})
