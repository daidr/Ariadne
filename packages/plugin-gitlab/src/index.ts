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
import { GitLabApi, type RawProject } from './api'

export { GitLabApi, GitLabError, plainLog, toCheck } from './api'

export interface GitLabSettings {
  /** `group/project` (subgroups too), or the project's URL. */
  repository: string
  /** The instance's address, e.g. `https://gitlab.example.com`; GitLab.com when left empty. */
  instance?: string
  /** Left empty, the token comes from the environment or the `glab` command line tool. */
  token?: string
}

export interface GitLabProviderOptions {
  /** Checkouts go under here. */
  dataDir: string
  /** For tests: where the git remote lives, instead of the instance. */
  gitBase?: string
  fetch?: typeof fetch
  env?: Record<string, string | undefined>
  /** What thread commands run in; they see their worktree and the clone behind it. */
  sandbox?: CommandSandbox
  /** For tests: how `glab` is run. */
  run?: typeof run
}

/**
 * Variables glab reads a token from before its own configuration. Cleared when asking it,
 * so it answers with the token it keeps for the host and never one from the environment,
 * which may have been issued by another instance.
 */
const GLAB_ENV_TOKENS = [
  'GITLAB_TOKEN',
  'GITLAB_ACCESS_TOKEN',
  'OAUTH_TOKEN',
  'GL_TOKEN',
  'CI_JOB_TOKEN',
]

export const GITLAB_COM = 'https://gitlab.com'

const LOOPBACK = /^(?:localhost|127(?:\.\d{1,3}){3}|\[::1\])$/

/**
 * The instance's address without a trailing slash; it may sit under a path, e.g.
 * `https://example.com/gitlab`. Tokens go there, so only https is taken, except on this
 * machine.
 */
export function parseInstance(input: string): string {
  let url: URL
  try {
    url = new URL(input.trim())
  } catch {
    throw new Error(`"${input}" is not an address (https://gitlab.example.com)`)
  }
  if (url.protocol !== 'https:' && !(url.protocol === 'http:' && LOOPBACK.test(url.hostname))) {
    throw new Error(
      `"${input}" is not https: the token would cross the network unencrypted (http is taken only for localhost)`,
    )
  }
  if (url.username || url.password || url.search || url.hash) {
    throw new Error(`"${input}" holds more than an address; give only https://host[/path]`)
  }
  return `${url.origin}${url.pathname.replace(/\/+$/, '')}`
}

/** One segment of a project path: what GitLab allows in group and project names. */
const SEGMENT = /^[A-Za-z0-9_][A-Za-z0-9_.-]*$/

function checkPath(path: string, input: string): string {
  const segments = path.split('/')
  if (
    segments.length < 2 ||
    segments.some((segment) => !SEGMENT.test(segment) || segment.endsWith('.git'))
  ) {
    throw new Error(`"${input}" is not a GitLab project (group/project)`)
  }
  return path
}

/**
 * The instance and the project's full path from what the user typed: a path such as
 * `group/sub/project`, or a project URL (https or ssh). A URL names its instance; when an
 * instance is also given, the URL must be on it.
 */
export function parseRepository(
  input: string,
  instance?: string,
): { instance: string; path: string } {
  const given = instance?.trim() ? parseInstance(instance) : null
  const text = input
    .trim()
    .replace(/\/+$/, '')
    .replace(/\.git$/, '')

  if (/^https?:\/\//.test(text)) {
    const url = new URL(text)
    // Pages inside a project (`/-/issues`, `/-/tree/main`) name the same project.
    const pathname = url.pathname.replace(/\/-(?:\/.*)?$/, '').replace(/\/+$/, '')
    const full = `${url.origin}${pathname}`
    if (given) {
      if (!full.startsWith(`${given}/`)) {
        throw new Error(`"${input}" is not on the instance ${given}`)
      }
      return { instance: given, path: checkPath(full.slice(given.length + 1), input) }
    }
    return { instance: parseInstance(url.origin), path: checkPath(pathname.slice(1), input) }
  }

  const ssh =
    /^(?:ssh:\/\/)?[\w.-]+@([\w.-]+)(?::\d+)?[:/](.+)$/.exec(text) ??
    /^ssh:\/\/([\w.-]+)(?::\d+)?\/(.+)$/.exec(text)
  if (ssh?.[1] && ssh[2]) {
    const host = ssh[1]
    if (given && new URL(given).hostname !== host) {
      throw new Error(`"${input}" is not on the instance ${given}`)
    }
    // ssh addresses have no web path; an instance under a path must be given.
    return { instance: given ?? `https://${host}`, path: checkPath(ssh[2], input) }
  }

  return { instance: given ?? GITLAB_COM, path: checkPath(text, input) }
}

/** The directories an instance's clones go under: its host (with port) and path. */
export function instanceDirs(instance: string): string[] {
  const url = new URL(instance)
  return [url.host, ...url.pathname.split('/').filter(Boolean)]
}

/**
 * The token to use: the configured one; else `GITLAB_TOKEN`, `GITLAB_ACCESS_TOKEN` or
 * `GL_TOKEN`, but only for the host they are meant for (`GITLAB_HOST`, GitLab.com when
 * unset), so a token is never sent to an instance it was not issued by; else what `glab`
 * keeps for this host.
 */
async function findToken(
  configured: string | undefined,
  instance: string,
  env: Record<string, string | undefined>,
  runCommand: typeof run,
): Promise<string | null> {
  if (configured?.trim()) return configured.trim()
  const host = new URL(instance).host
  const envHost = (() => {
    const value = env.GITLAB_HOST?.trim()
    if (!value) return new URL(GITLAB_COM).host
    try {
      return new URL(value.includes('://') ? value : `https://${value}`).host
    } catch {
      return null
    }
  })()
  if (envHost === host) {
    const fromEnv =
      env.GITLAB_TOKEN?.trim() || env.GITLAB_ACCESS_TOKEN?.trim() || env.GL_TOKEN?.trim()
    if (fromEnv) return fromEnv
  }
  try {
    const result = await runCommand(['glab', 'config', 'get', 'token', '--host', host], {
      cwd: '/',
      timeoutMs: 10_000,
      // glab takes an empty variable as unset.
      env: Object.fromEntries(GLAB_ENV_TOKENS.map((name) => [name, ''])),
    })
    return result.exitCode === 0 && result.stdout.trim() ? result.stdout.trim() : null
  } catch {
    // No glab installed.
    return null
  }
}

/**
 * One configured project: a shared clone with a worktree per thread (see `hostedClone`),
 * merge requests and their pipelines, and issues, all through the server's token.
 */
export function connectRepository(
  settings: GitLabSettings,
  options: GitLabProviderOptions,
): {
  workspace: WorkspaceCapability
  repo: RepoCapability
  tracker: TrackerCapability
  api: GitLabApi
  instance: string
  path: string
} {
  const { instance, path } = parseRepository(settings.repository, settings.instance)
  const env = options.env ?? process.env
  // A token found is kept; none found is looked for again, e.g. after `glab auth login`.
  let token: Promise<string | null> | null = null
  const getToken = async () => {
    token ??= findToken(settings.token, instance, env, options.run ?? run)
    const found = await token
    if (!found) token = null
    return found
  }
  const webUrl = `${instance}/${path}`
  const api = new GitLabApi({
    base: `${instance}/api/v4`,
    path,
    webUrl,
    token: getToken,
    fetch: options.fetch ?? fetch,
  })
  let project: Promise<RawProject> | null = null
  const getProject = () => {
    project ??= api.project()
    project.catch(() => {
      project = null
    })
    return project
  }
  const getDefaultBranch = async () => {
    const branch = (await getProject()).default_branch
    if (!branch) throw new Error(`${path} has no default branch yet: push a first commit to it`)
    return branch
  }
  const clone = hostedClone({
    dataDir: options.dataDir,
    path,
    // Projects with the same path on two instances are different repositories: each
    // instance gets its own directories, e.g. `gitlab.com/` or `example.com:8443/gitlab/`.
    namespace: instanceDirs(instance),
    remoteUrl: `${options.gitBase ?? instance}/${path}.git`,
    // GitLab takes any token as the password of `oauth2`.
    user: 'oauth2',
    token: getToken,
    defaultBranch: getDefaultBranch,
    ...(options.sandbox ? { sandbox: options.sandbox } : {}),
  })

  const repo: RepoCapability = {
    remote: path,
    webUrl,
    defaultBranch: getDefaultBranch,
    // `internal` is readable by everyone signed in to the instance: what is pushed there
    // reaches people outside the project at once, so it counts as public.
    isPublic: async () => (await getProject()).visibility !== 'private',
    fetch: clone.fetch,
    push: clone.push,
    objects: clone.objects,
    pullRequestFor: async (branch) => api.mergeRequestFor(branch, (await getProject()).id),
    pullRequest: (iid) => api.mergeRequest(iid),
    openPullRequest: async (input) =>
      api.openMergeRequest({
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
    name: path,
    webUrl: `${webUrl}/-/issues`,
    list: (query) => api.issues(query),
    issue: (iid) => api.issue(iid),
    comment: (iid, body) => api.comment(iid, body),
    setState: (iid, state) => api.setIssueState(iid, state),
  }

  return { workspace: clone.workspace, repo, tracker, api, instance, path }
}

export function gitlabProvider(options: GitLabProviderOptions): ContextProvider<GitLabSettings> {
  return {
    kind: 'gitlab',
    title: { 'zh-CN': 'GitLab 仓库', en: 'GitLab repository' },
    settings: {
      type: 'object',
      properties: {
        repository: {
          type: 'string',
          title: { 'zh-CN': '仓库', en: 'Repository' },
          description: {
            'zh-CN': 'group/project（可含子组）或项目地址；每个讨论串在自己的分支上工作',
            en: 'group/project (subgroups too) or its URL; each thread works on its own branch',
          },
        },
        instance: {
          type: 'string',
          title: { 'zh-CN': '实例地址', en: 'Instance' },
          description: {
            'zh-CN':
              '自建实例填 https://gitlab.example.com；留空时用 GitLab.com，或仓库地址里的实例',
            en: 'For a self-managed instance, https://gitlab.example.com; left empty, GitLab.com or the instance in the repository URL',
          },
        },
        token: {
          type: 'string',
          secret: true,
          title: { 'zh-CN': '访问令牌', en: 'Access token' },
          description: {
            'zh-CN':
              '需要 api 权限。留空时使用 GITLAB_TOKEN 环境变量（只用于 GITLAB_HOST 指的实例，未设置时为 GitLab.com）或 glab 为这个实例保存的令牌',
            en: 'Needs the api scope. Left empty: GITLAB_TOKEN (only for the GITLAB_HOST instance, GitLab.com when unset) or the token glab keeps for this instance',
          },
        },
      },
      required: ['repository'],
    },
    provides: ['workspace', 'repo', 'tracker'],
    hosting: true,
    async connect(settings): Promise<Partial<Capabilities>> {
      const connected = connectRepository(settings, options)
      // Fails early on a typo, a wrong instance or a token without access.
      await connected.api.project()
      return { workspace: connected.workspace, repo: connected.repo, tracker: connected.tracker }
    },
  }
}

export default definePlugin({
  id: 'gitlab',
  name: { 'zh-CN': 'GitLab', en: 'GitLab' },
  setup(ctx) {
    return ctx.registerContextProvider(
      gitlabProvider({ dataDir: ctx.dataDir, sandbox: ctx.sandbox }) as ContextProvider<never>,
    )
  },
})
