import { readOnlyView } from '@ariadne/plugin-sdk'
import type { ExecResult, GitObjects, WorkspaceCapability } from '@ariadne/plugin-sdk'
import { existsSync } from 'node:fs'
import { mkdir, mkdtemp, readdir, readFile, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { branchChanges, branchMerged, fileVersions, removeWorktree, type GitRun } from './branches'
import { canClone, checkoutCloned, platformCloner, type Cloner } from './cow'
import { branchFor, cloneGit, LocalWorkspace, run, serverGitEnv, worktreesIn } from './index'
import { GitTreeView } from './tree'

/** The coordinator's view fetches again once what it shows is older than this. */
const VIEW_FRESH_MS = 60_000

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

/** Where the shared clone keeps the remote's branch `base`. */
const baseRef = (base: string) => `refs/remotes/origin/${base}`

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
  /** How files are cloned between worktrees; `null` turns it off. */
  clone?: Cloner | null
}): {
  workspace: WorkspaceCapability
  fetch(): Promise<void>
  push(branch: string): Promise<{ sha: string }>
  objects: GitObjects
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

  const worktrees = join(input.dataDir, 'worktrees', ...segments, last)
  const dirOf = (threadId: string) => join(worktrees, threadId)
  const clone = input.clone === undefined ? platformCloner : input.clone
  let cloning: Promise<boolean> | null = null
  /** Whether worktrees share storage: files cloned from one to the next take no room. */
  const clones = (): Promise<boolean> => {
    // Asked again until the shared clone is there to try it with.
    if (!clone || !existsSync(join(bare, 'HEAD'))) return Promise.resolve(false)
    return (cloning ??= canClone(clone, bare, worktrees).catch(() => false))
  }
  /** Another thread's worktree, to clone a new one's files from. */
  const sibling = async (threadId: string) =>
    (await readdir(worktrees).catch(() => []))
      .filter((name) => name !== threadId && existsSync(join(worktrees, name, '.git')))
      .map((name) => join(worktrees, name))[0] ?? null
  // Reading the shared clone needs no network and no token.
  const local: GitRun = (args, options = {}) =>
    run(['git', ...args], {
      cwd: options.cwd ?? bare,
      env: serverGitEnv(['file']),
      ...(options.whole ? { whole: true } : {}),
      ...(options.signal ? { signal: options.signal } : {}),
      ...(options.timeoutMs ? { timeoutMs: options.timeoutMs } : {}),
    })

  // What the coordinator reads: the default branch as last fetched, at most a minute old.
  let fetchedAt = 0
  const view = async () => {
    const base = await input.defaultBranch()
    if (Date.now() - fetchedAt > VIEW_FRESH_MS) {
      await serial(ensureClone)
      fetchedAt = Date.now()
    }
    return readOnlyView(
      new GitTreeView(local, (args) => gitBytes(bare, args), baseRef(base), `origin/${base}`),
      `origin/${base}`,
    )
  }

  const workspace: WorkspaceCapability = {
    isolated: true,
    view,
    // Branches reach the default branch through pull requests: nothing is merged here.
    branches: {
      changes: ({ threadId, branch }, base) =>
        branchChanges(local, {
          branch,
          base,
          baseRef: baseRef(base),
          worktree: existsSync(dirOf(threadId)) ? dirOf(threadId) : null,
        }),
      file: (request) => fileVersions(local, request),
      merged: ({ branch }, base) => branchMerged(local, branch, baseRef(base)),
      remove: (threadId, options) =>
        serial(() =>
          removeWorktree(local, dirOf(threadId), async (dir) => existsSync(dir), options),
        ),
      worktrees: () => worktreesIn(worktrees),
      sharesStorage: clones,
    },
    open: ({ threadId, base: recorded, branch: named }) =>
      serial(async () => {
        const branch = named ?? branchFor(threadId)
        const dir = dirOf(threadId)
        const base = recorded ?? (await input.defaultBranch())
        if (!existsSync(join(dir, '.git'))) {
          await ensureClone()
          await mkdir(join(dir, '..'), { recursive: true })
          await git(bare, 'worktree', 'prune')
          const hasBranch =
            (await git(bare, 'rev-parse', '--verify', '--quiet', `refs/heads/${branch}`))
              .exitCode === 0
          // Where clones work, the files come from another thread's worktree instead of git.
          const from = clone && (await clones()) ? await sibling(threadId) : null
          const plain = from ? ['--no-checkout'] : []
          check(
            hasBranch
              ? await git(bare, 'worktree', 'add', ...plain, dir, branch)
              : await git(
                  bare,
                  'worktree',
                  'add',
                  ...plain,
                  '--no-track',
                  '-b',
                  branch,
                  dir,
                  baseRef(base),
                ),
            'git worktree add',
          )
          if (clone && from) await checkoutCloned(from, dir, cloneGit(['file']), clone)
        }
        return new LocalWorkspace(threadId, dir, branch, { base })
      }),
  }

  // The shared clone's refs and objects, for commits made in a checkout on another machine.
  const ready = () => (existsSync(join(bare, 'HEAD')) ? Promise.resolve() : serial(ensureClone))
  const objects: GitObjects = {
    async refs(prefixes) {
      await ready()
      const result = await local([
        'for-each-ref',
        '--format=%(objectname) %(refname)',
        '--',
        ...prefixes,
      ])
      return parseRefs(check(result, 'git for-each-ref').stdout)
    },
    async has(shas) {
      if (!shas.length) return []
      await ready()
      const out = await gitInput(
        bare,
        ['cat-file', '--batch-check=%(objectname) %(objecttype)'],
        `${shas.join('\n')}\n`,
      )
      return out
        .split('\n')
        .map((line) => line.split(' '))
        .filter(([, type]) => type === 'commit')
        .map(([sha]) => sha!)
    },
    async bundle(tips, exclude) {
      await ready()
      return withTemp(async (file) => {
        const revs = [...tips, ...exclude.map((sha) => `^${sha}`)].join('\n')
        await gitInput(bare, ['bundle', 'create', file, '--stdin'], `${revs}\n`)
        return new Uint8Array(await readFile(file))
      })
    },
    receive: (bundle, refs) =>
      serial(async () => {
        await ready()
        if (bundle) {
          await withTemp(async (file) => {
            await writeFile(file, bundle)
            check(await local(['bundle', 'unbundle', file]), 'git bundle unbundle')
          })
        }
        const updates = Object.entries(refs).map(([ref, sha]) =>
          sha ? `update ${ref} ${sha}\n` : `delete ${ref}\n`,
        )
        if (updates.length) await gitInput(bare, ['update-ref', '--stdin'], updates.join(''))
      }),
  }

  return {
    workspace,
    objects,
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

/** `<sha> <ref>` lines, as `for-each-ref` prints them. */
function parseRefs(text: string): Record<string, string> {
  const refs: Record<string, string> = {}
  for (const line of text.split('\n')) {
    const [sha, ref] = line.trim().split(' ')
    if (sha && ref) refs[ref] = sha
  }
  return refs
}

/** The server's git in `cwd`, fed `input`; its output, or an error with the reason. */
async function gitInput(cwd: string, args: string[], input: string): Promise<string> {
  const proc = Bun.spawn(['git', ...args], {
    cwd,
    stdin: new TextEncoder().encode(input),
    stdout: 'pipe',
    stderr: 'pipe',
    windowsHide: true,
    env: { ...process.env, GIT_TERMINAL_PROMPT: '0', ...serverGitEnv(['file']) },
  })
  const [stdout, stderr, code] = await Promise.all([
    new Response(proc.stdout).text(),
    new Response(proc.stderr).text(),
    proc.exited,
  ])
  if (code !== 0) throw new Error(`git ${args[0]} failed: ${stderr.trim() || `exit ${code}`}`)
  return stdout
}

/** The server's git in the shared clone, its output as bytes. */
async function gitBytes(cwd: string, args: string[]): Promise<Uint8Array> {
  const proc = Bun.spawn(['git', ...args], {
    cwd,
    stdin: 'ignore',
    stdout: 'pipe',
    stderr: 'pipe',
    windowsHide: true,
    env: { ...process.env, GIT_TERMINAL_PROMPT: '0', ...serverGitEnv(['file']) },
  })
  const [stdout, stderr, code] = await Promise.all([
    new Response(proc.stdout).bytes(),
    new Response(proc.stderr).text(),
    proc.exited,
  ])
  if (code !== 0) throw new Error(`git ${args[0]} failed: ${stderr.trim() || `exit ${code}`}`)
  return stdout
}

async function withTemp<T>(work: (file: string) => Promise<T>): Promise<T> {
  const dir = await mkdtemp(join(tmpdir(), 'ariadne-bundle-'))
  try {
    return await work(join(dir, 'objects.bundle'))
  } finally {
    await rm(dir, { recursive: true, force: true })
  }
}
