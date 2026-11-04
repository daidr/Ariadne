import type { CommandSandbox, ExecResult, WorkspaceCapability } from '@ariadne/plugin-sdk'
import { existsSync } from 'node:fs'
import { mkdir } from 'node:fs/promises'
import { join } from 'node:path'
import { branchFor, gitControlPaths, LocalWorkspace, run, serverGitEnv } from './index'

/** Where `git push` from a workspace goes: nowhere, so every push goes through the tool. */
const NO_PUSH_URL = 'no-push://use-the-push_branch-tool'

/**
 * The environment for the server's own git commands on a hosted repository: hardened like
 * any server git (no global config, hooks or fsmonitor; only the remote's protocol), with
 * the credentials passed as config so they never land on disk or argv, and bound to the
 * repository's URL so no other host receives them.
 */
export function hostedGitEnv(
  remoteUrl: string,
  credentials: { user: string; token: string } | null,
): Record<string, string> {
  const scheme = /^([a-z]+):/.exec(remoteUrl)?.[1] ?? 'https'
  const env = serverGitEnv([scheme])
  // The header goes with every request: a redirect to another host would take it along.
  addConfig(env, 'http.followRedirects', 'false')
  if (credentials) {
    const basic = Buffer.from(`${credentials.user}:${credentials.token}`).toString('base64')
    addConfig(env, `http.${remoteUrl}.extraHeader`, `Authorization: Basic ${basic}`)
  }
  return env
}

function addConfig(env: Record<string, string>, key: string, value: string) {
  const index = Number(env.GIT_CONFIG_COUNT)
  env.GIT_CONFIG_COUNT = String(index + 1)
  env[`GIT_CONFIG_KEY_${index}`] = key
  env[`GIT_CONFIG_VALUE_${index}`] = value
}

function check(result: ExecResult, what: string): ExecResult {
  if (result.exitCode !== 0) {
    throw new Error(`${what} failed: ${result.stderr.trim() || `exit ${result.exitCode}`}`)
  }
  return result
}

/**
 * A hosted repository checked out for threads. A bare clone under `dataDir/repos/<path>.git`
 * (under `namespace` when given) is shared by every thread; each thread gets a worktree under
 * `dataDir/worktrees/<path>/`
 * on its own branch, started from the remote's default branch, so threads never see each
 * other's changes. The server fetches and pushes by the URL it was given, never by a remote
 * a workspace could rewrite, and the clone's own push URL goes nowhere.
 */
export function hostedClone(input: {
  /** Where the clone and the worktrees go. */
  dataDir: string
  /** The repository's path on its host, e.g. `owner/name` or `group/sub/project`. */
  path: string
  /**
   * Directories above the path, so that the same path on two hosts gets two clones, e.g.
   * `['gitlab.example.com']`.
   */
  namespace?: string[]
  remoteUrl: string
  /** The basic-auth user the host takes with a token, e.g. `x-access-token` or `oauth2`. */
  user: string
  token: () => Promise<string | null>
  defaultBranch: () => Promise<string>
  sandbox?: CommandSandbox
}): {
  workspace: WorkspaceCapability
  fetch(): Promise<void>
  push(branch: string): Promise<{ sha: string }>
} {
  const { remoteUrl } = input
  const segments = [...(input.namespace ?? []), ...input.path.split('/')]
  const last = segments.pop()!
  const bare = join(input.dataDir, 'repos', ...segments, `${last}.git`)

  const git = async (cwd: string, ...args: string[]) => {
    const token = await input.token()
    return run(['git', ...args], {
      cwd,
      timeoutMs: 600_000,
      env: hostedGitEnv(remoteUrl, token ? { user: input.user, token } : null),
    })
  }

  const fetchRemote = async () => {
    check(
      await git(bare, 'fetch', '--prune', '-q', remoteUrl, '+refs/heads/*:refs/remotes/origin/*'),
      'git fetch',
    )
  }

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
        const dir = join(input.dataDir, 'worktrees', ...segments, last, threadId)
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
                  `refs/remotes/origin/${await input.defaultBranch()}`,
                ),
            'git worktree add',
          )
        }
        // A hooks directory that is missing could be made by a thread; make it first.
        await mkdir(join(bare, 'hooks'), { recursive: true })
        return new LocalWorkspace(threadId, dir, branch, {
          ...(input.sandbox ? { sandbox: input.sandbox } : {}),
          keep: [dir, bare],
          // The server fetches and pushes in the shared clone; its config and hooks stay as
          // the server set them.
          readOnly: gitControlPaths(bare),
        })
      }),
  }

  return {
    workspace,
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
  }
}
