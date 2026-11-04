import { run } from '@ariadne/plugin-local'
import { hostedClone } from '@ariadne/plugin-local/hosted'
import {
  definePlugin,
  type Capabilities,
  type CommandSandbox,
  type ContextProvider,
  type RepoCapability,
  type TrackerCapability,
  type WorkspaceCapability,
} from '@ariadne/plugin-sdk'
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
  /** What thread commands run in; they see their worktree and the clone behind it. */
  sandbox?: CommandSandbox
}

export { branchFor } from '@ariadne/plugin-local'

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

/**
 * One configured repository: a shared clone with a worktree per thread (see `hostedClone`),
 * pull requests and checks, and issues, all through the server's token.
 */
export function connectRepository(
  settings: GitHubSettings,
  options: GitHubProviderOptions,
): {
  workspace: WorkspaceCapability
  repo: RepoCapability
  tracker: TrackerCapability
  api: GitHubApi
} {
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
  let repository: ReturnType<GitHubApi['repository']> | null = null
  const getRepository = () => {
    repository ??= api.repository()
    repository.catch(() => {
      repository = null
    })
    return repository
  }
  const getDefaultBranch = async () => (await getRepository()).default_branch
  const clone = hostedClone({
    dataDir: options.dataDir,
    path: `${owner}/${name}`,
    remoteUrl,
    user: 'x-access-token',
    token: getToken,
    defaultBranch: getDefaultBranch,
    ...(options.sandbox ? { sandbox: options.sandbox } : {}),
  })
  const { workspace } = clone

  const repo: RepoCapability = {
    remote: `${owner}/${name}`,
    webUrl: `${options.gitBase?.startsWith('http') ? options.gitBase : 'https://github.com'}/${owner}/${name}`,
    defaultBranch: getDefaultBranch,
    isPublic: async () => !(await getRepository()).private,
    fetch: clone.fetch,
    push: clone.push,
    objects: clone.objects,
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

  // Issues go through the same API client: the token stays with the server, never in a
  // workspace a thread's commands can read.
  const tracker: TrackerCapability = {
    name: `${owner}/${name}`,
    webUrl: `${repo.webUrl}/issues`,
    list: (query) => api.issues(query),
    issue: (number) => api.issue(number),
    comment: (number, body) => api.comment(number, body),
    setState: (number, state, reason) => api.setIssueState(number, state, reason),
  }

  return { workspace, repo, tracker, api }
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
    provides: ['workspace', 'repo', 'tracker'],
    hosting: true,
    async connect(settings): Promise<Partial<Capabilities>> {
      const connected = connectRepository(settings, options)
      // Fails early on a typo or a token without access.
      await connected.api.repository()
      return { workspace: connected.workspace, repo: connected.repo, tracker: connected.tracker }
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
        sandbox: ctx.sandbox,
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
