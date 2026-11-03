import { LocalWorkspace, run } from '@ariadne/plugin-local'
import {
  definePlugin,
  type Capabilities,
  type ContextProvider,
  type ExecResult,
  type RepoCapability,
  type WorkspaceCapability,
} from '@ariadne/plugin-sdk'
import { existsSync } from 'node:fs'
import { mkdir } from 'node:fs/promises'
import { join } from 'node:path'
import { GitHubApi } from './api'

export { GitHubApi, GitHubError } from './api'

export interface GitHubSettings {
  /** `owner/name`, or the repository's URL. */
  repository: string
  /** Left empty, the token comes from the environment or the `gh` command line tool. */
  token?: string
}

export interface GitHubProviderOptions {
  /** Checkouts go under here. */
  dataDir: string
  /** For tests: where the REST API and the git remote live. */
  apiBase?: string
  gitBase?: string
  fetch?: typeof fetch
  env?: Record<string, string | undefined>
}

/** Branch names stay short: the random tail of the thread id is unique enough. */
export const branchFor = (threadId: string) => `ariadne/${threadId.slice(-12)}`

/** `owner/name` from what the user typed: a slug, or an https or ssh URL. */
export function parseRepository(input: string): { owner: string; name: string } {
  const text = input
    .trim()
    .replace(/^(?:https?:\/\/github\.com\/|git@github\.com:)/, '')
    .replace(/\.git$/, '')
    .replace(/\/+$/, '')
  const match = /^([A-Za-z0-9-]+)\/([A-Za-z0-9._-]+)$/.exec(text)
  if (!match?.[1] || !match[2])
    throw new Error(`"${input}" is not a GitHub repository (owner/name)`)
  return { owner: match[1], name: match[2] }
}

/** The token to use: the configured one, else GH_TOKEN, GITHUB_TOKEN, or `gh auth token`. */
async function findToken(
  configured: string | undefined,
  env: Record<string, string | undefined>,
): Promise<string | null> {
  if (configured?.trim()) return configured.trim()
  const fromEnv = env.GH_TOKEN?.trim() || env.GITHUB_TOKEN?.trim()
  if (fromEnv) return fromEnv
  try {
    const result = await run(['gh', 'auth', 'token'], { cwd: '/', timeoutMs: 10_000 })
    return result.exitCode === 0 && result.stdout.trim() ? result.stdout.trim() : null
  } catch {
    // No gh installed.
    return null
  }
}

/** Where `git push` from a workspace goes: nowhere, so every push goes through the tool. */
const NO_PUSH_URL = 'no-push://use-the-push_branch-tool'

/**
 * Git config for the server's own git commands, passed through the environment so the
 * token never lands on disk or argv. The header is bound to the repository's URL, and
 * hooks are off: the shared clone's config and hooks can be changed from a workspace.
 */
function serverGitEnv(remoteUrl: string, token: string | null): Record<string, string> {
  const config: [string, string][] = [['core.hooksPath', '/dev/null']]
  if (token) {
    const basic = Buffer.from(`x-access-token:${token}`).toString('base64')
    config.push([`http.${remoteUrl}.extraHeader`, `Authorization: Basic ${basic}`])
  }
  const env: Record<string, string> = { GIT_CONFIG_COUNT: String(config.length) }
  config.forEach(([key, value], index) => {
    env[`GIT_CONFIG_KEY_${index}`] = key
    env[`GIT_CONFIG_VALUE_${index}`] = value
  })
  return env
}

function check(result: ExecResult, what: string): ExecResult {
  if (result.exitCode !== 0) {
    throw new Error(`${what} failed: ${result.stderr.trim() || `exit ${result.exitCode}`}`)
  }
  return result
}

/**
 * One configured repository. A bare clone under the data directory is shared by every
 * thread; each thread gets a worktree on its own branch, started from the remote's default
 * branch, so threads never see each other's changes.
 */
export function connectRepository(
  settings: GitHubSettings,
  options: GitHubProviderOptions,
): { workspace: WorkspaceCapability; repo: RepoCapability; api: GitHubApi } {
  const { owner, name } = parseRepository(settings.repository)
  const env = options.env ?? process.env
  // A token found is kept; none found is looked for again, e.g. after `gh auth login`.
  let token: Promise<string | null> | null = null
  const getToken = async () => {
    token ??= findToken(settings.token, env)
    const found = await token
    if (!found) token = null
    return found
  }
  const api = new GitHubApi({
    base: options.apiBase ?? 'https://api.github.com',
    owner,
    name,
    token: getToken,
    fetch: options.fetch ?? fetch,
  })
  const remoteUrl = `${options.gitBase ?? 'https://github.com'}/${owner}/${name}.git`
  const bare = join(options.dataDir, 'repos', owner, `${name}.git`)
  let repository: ReturnType<GitHubApi['repository']> | null = null
  const getRepository = () => {
    repository ??= api.repository()
    repository.catch(() => {
      repository = null
    })
    return repository
  }
  const getDefaultBranch = async () => (await getRepository()).default_branch

  const git = async (cwd: string, ...args: string[]) =>
    run(['git', ...args], {
      cwd,
      timeoutMs: 600_000,
      env: serverGitEnv(remoteUrl, await getToken()),
    })

  /** Fetch by the URL the server knows, never by a remote a workspace could rewrite. */
  const fetchRemote = async () =>
    check(
      await git(bare, 'fetch', '--prune', '-q', remoteUrl, '+refs/heads/*:refs/remotes/origin/*'),
      'git fetch',
    )

  // Setup and fetches touch the shared clone; one at a time.
  let queue: Promise<unknown> = Promise.resolve()
  const serial = <T>(work: () => Promise<T>): Promise<T> => {
    const next = queue.then(work, work)
    queue = next.catch(() => undefined)
    return next
  }

  async function ensureClone() {
    if (!existsSync(join(bare, 'HEAD'))) {
      await mkdir(bare, { recursive: true })
      check(await git(bare, 'init', '--bare', '-q'), 'git init')
      check(await git(bare, 'remote', 'add', 'origin', remoteUrl), 'git remote add')
      // Remote branches go under refs/remotes, so fetching never moves a thread's branch.
      check(
        await git(bare, 'config', 'remote.origin.fetch', '+refs/heads/*:refs/remotes/origin/*'),
        'git config',
      )
    }
    // A plain `git push` from a workspace fails at once; pushes go through `push`.
    check(await git(bare, 'config', 'remote.origin.pushurl', NO_PUSH_URL), 'git config')
    await fetchRemote()
  }

  const workspace: WorkspaceCapability = {
    isolated: true,
    open: ({ threadId }) =>
      serial(async () => {
        const branch = branchFor(threadId)
        const dir = join(options.dataDir, 'worktrees', owner, name, threadId)
        if (!existsSync(join(dir, '.git'))) {
          await ensureClone()
          await mkdir(join(dir, '..'), { recursive: true })
          await git(bare, 'worktree', 'prune')
          const hasBranch =
            (await git(bare, 'rev-parse', '--verify', '--quiet', `refs/heads/${branch}`))
              .exitCode === 0
          check(
            hasBranch
              ? await git(bare, 'worktree', 'add', dir, branch)
              : await git(
                  bare,
                  'worktree',
                  'add',
                  '--no-track',
                  '-b',
                  branch,
                  dir,
                  `refs/remotes/origin/${await getDefaultBranch()}`,
                ),
            'git worktree add',
          )
        }
        return new LocalWorkspace(threadId, dir, branch)
      }),
  }

  const repo: RepoCapability = {
    remote: `${owner}/${name}`,
    webUrl: `${options.gitBase?.startsWith('http') ? options.gitBase : 'https://github.com'}/${owner}/${name}`,
    defaultBranch: getDefaultBranch,
    isPublic: async () => !(await getRepository()).private,
    fetch: () => serial(ensureClone),
    push: (branch) =>
      serial(async () => {
        const ref = `refs/heads/${branch}`
        const sha = check(await git(bare, 'rev-parse', '--verify', ref), 'git rev-parse')
        // No "+": a branch that moved on the remote is refused rather than overwritten.
        const pushed = await git(bare, 'push', '-q', '--no-verify', remoteUrl, `${ref}:${ref}`)
        if (pushed.exitCode !== 0) {
          const rejected = /rejected|non-fast-forward|fetch first/.test(pushed.stderr)
          throw new Error(
            rejected
              ? `git push was rejected: origin/${branch} has commits your branch does not have. Fetch them (sync_branch), merge origin/${branch} into your branch, then push again. Never force-push.`
              : `git push failed: ${pushed.stderr.trim() || `exit ${pushed.exitCode}`}`,
          )
        }
        // Keep origin/<branch> in step, so ahead/behind counts are right after a push.
        await git(bare, 'update-ref', `refs/remotes/origin/${branch}`, sha.stdout.trim())
        return { sha: sha.stdout.trim() }
      }),
    pullRequestFor: (branch) => api.pullRequestFor(branch),
    pullRequest: (number) => api.pullRequest(number),
    openPullRequest: async (input) =>
      api.openPullRequest({
        branch: input.branch,
        base: input.base ?? (await getDefaultBranch()),
        title: input.title,
        body: input.body,
        draft: input.draft ?? false,
      }),
    checks: (sha) => api.checks(sha),
    checkLog: (id) => api.checkLog(id),
  }

  return { workspace, repo, api }
}

export function githubProvider(options: GitHubProviderOptions): ContextProvider<GitHubSettings> {
  return {
    kind: 'github',
    title: { 'zh-CN': 'GitHub 仓库', en: 'GitHub repository' },
    settings: {
      type: 'object',
      properties: {
        repository: {
          type: 'string',
          title: { 'zh-CN': '仓库', en: 'Repository' },
          description: {
            'zh-CN': 'owner/name 或仓库地址；每个 thread 在自己的分支上工作',
            en: 'owner/name or the URL; each thread works on its own branch',
          },
        },
        token: {
          type: 'string',
          secret: true,
          title: { 'zh-CN': '访问令牌', en: 'Access token' },
          description: {
            'zh-CN': '留空时使用 GH_TOKEN、GITHUB_TOKEN 环境变量或 gh auth token',
            en: 'Left empty: GH_TOKEN, GITHUB_TOKEN, or gh auth token',
          },
        },
      },
      required: ['repository'],
    },
    provides: ['workspace', 'repo'],
    async connect(settings): Promise<Partial<Capabilities>> {
      const connected = connectRepository(settings, options)
      // Fails early on a typo or a token without access.
      await connected.api.repository()
      return { workspace: connected.workspace, repo: connected.repo }
    },
  }
}

export default definePlugin({
  id: 'github',
  name: { 'zh-CN': 'GitHub', en: 'GitHub' },
  setup(ctx) {
    return ctx.registerContextProvider(
      githubProvider({
        dataDir: ctx.dataDir,
        // Another GitHub host (e.g. GitHub Enterprise), or a stand-in for testing.
        ...(process.env.ARIADNE_GITHUB_API_URL
          ? { apiBase: process.env.ARIADNE_GITHUB_API_URL }
          : {}),
        ...(process.env.ARIADNE_GITHUB_GIT_URL
          ? { gitBase: process.env.ARIADNE_GITHUB_GIT_URL }
          : {}),
      }) as ContextProvider<never>,
    )
  },
})
