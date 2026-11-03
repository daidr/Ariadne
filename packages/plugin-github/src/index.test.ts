import { run } from '@ariadne/plugin-local'
import { mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { connectRepository, githubProvider, parseRepository } from './index'

let base: string
let server: ReturnType<typeof Bun.serve>
let requests: { method: string; path: string; auth: string | null; body: unknown }[]
let pulls: Record<string, unknown>[]

const git = (cwd: string, ...args: string[]) => run(['git', ...args], { cwd })

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
      const body = request.method === 'POST' ? await request.json() : null
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
        })
      }
      if (url.pathname === '/repos/acme/missing') {
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
    await expect(repo.push(ws.branch!)).rejects.toThrow(/git push failed/)
  })
})

function check(result: { exitCode: number; stderr: string }) {
  if (result.exitCode !== 0) throw new Error(result.stderr)
}
