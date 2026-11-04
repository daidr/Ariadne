import {
  definePlugin,
  WorkspacePathError,
  type CommandSandbox,
  type ContextProvider,
  type ExecResult,
  type Workspace,
  type WorkspaceCapability,
} from '@ariadne/plugin-sdk'
import { createHash } from 'node:crypto'
import { constants, existsSync } from 'node:fs'
import { mkdir, open, readdir, realpath, stat, type FileHandle } from 'node:fs/promises'
import { dirname, isAbsolute, join, relative, resolve } from 'node:path'
import { platformCloner, seedWorktree, type Cloner } from './cow'

export interface LocalSettings {
  path: string
}

/** Commands may run this long unless the caller asks for less. */
const DEFAULT_TIMEOUT_MS = 120_000
/** Output beyond this is cut, keeping the end where errors usually are. */
const MAX_OUTPUT = 64_000

function clip(text: string): string {
  if (text.length <= MAX_OUTPUT) return text
  return `[… ${text.length - MAX_OUTPUT} characters cut …]\n${text.slice(-MAX_OUTPUT)}`
}

/** Credentials the server holds that a thread's commands must never see. */
const HIDDEN_ENV = new Set([
  'GH_TOKEN',
  'GITHUB_TOKEN',
  'GH_ENTERPRISE_TOKEN',
  'GITHUB_ENTERPRISE_TOKEN',
  'GITLAB_TOKEN',
  'GITLAB_ACCESS_TOKEN',
  'GL_TOKEN',
  'CI_JOB_TOKEN',
  'OAUTH_TOKEN',
])

/**
 * The environment for commands a thread runs: the server's own, without its credentials
 * (Ariadne's local token, GitHub and GitLab tokens, git config passed through the environment).
 * This keeps them out of tool results, and so out of the event log and the model's input.
 * It is not a sandbox: a command can still read files the user can read.
 */
export function threadEnv(
  base: Record<string, string | undefined> = process.env,
): Record<string, string> {
  const env: Record<string, string> = {}
  for (const [key, value] of Object.entries(base)) {
    if (value === undefined) continue
    if (HIDDEN_ENV.has(key) || key.startsWith('ARIADNE_') || key.startsWith('GIT_CONFIG_')) continue
    env[key] = value
  }
  return env
}

/**
 * Git run by the server, outside the sandbox, in a git directory threads write to. It
 * reads no global or system config (a thread can write the user's `~/.gitconfig`), runs
 * no hooks and no fsmonitor, and only speaks the protocols given, so whatever a thread
 * left in the repository cannot make the server's git run a command.
 */
export function serverGitEnv(protocols: string[] = ['https']): Record<string, string> {
  return {
    GIT_CONFIG_NOSYSTEM: '1',
    GIT_CONFIG_GLOBAL: '/dev/null',
    GIT_ALLOW_PROTOCOL: protocols.join(':'),
    GIT_CONFIG_COUNT: '2',
    GIT_CONFIG_KEY_0: 'core.hooksPath',
    GIT_CONFIG_VALUE_0: '/dev/null',
    GIT_CONFIG_KEY_1: 'core.fsmonitor',
    GIT_CONFIG_VALUE_1: 'false',
  }
}

/** The parts of a git directory that make git run something: kept read-only for threads. */
export const gitControlPaths = (gitDir: string) => [join(gitDir, 'config'), join(gitDir, 'hooks')]

const windows = process.platform === 'win32'

/** Run a program without a shell. */
export async function run(
  argv: string[],
  options: {
    cwd: string
    timeoutMs?: number
    signal?: AbortSignal
    env?: Record<string, string>
    /** Leave out the server's credentials, for commands a thread asked for. */
    forThread?: boolean
  },
): Promise<ExecResult> {
  // Stopped before it started (say, while the worktree was being made): an abort listener
  // added now would never fire, so the command must not start at all.
  if (options.signal?.aborted) return { exitCode: 130, stdout: '', stderr: 'cancelled' }
  const base = options.forThread ? threadEnv() : process.env
  const proc = Bun.spawn(argv, {
    cwd: options.cwd,
    stdout: 'pipe',
    stderr: 'pipe',
    stdin: 'ignore',
    env: { ...base, GIT_TERMINAL_PROMPT: '0', ...options.env },
    // A thread's command gets a process group of its own, so what it leaves running in the
    // background ends with it (on macOS the sandbox does not do this). Windows has no
    // process groups, and a detached command there would open a console window of its own.
    detached: !!options.forThread && !windows,
  })
  const killGroup = () => {
    if (windows) {
      // The command and what it started, while it still runs; once it has exited, the
      // processes it left behind are no longer found from it.
      Bun.spawn(['taskkill', '/T', '/F', '/PID', String(proc.pid)], {
        stdout: 'ignore',
        stderr: 'ignore',
        stdin: 'ignore',
      })
      return
    }
    try {
      process.kill(-proc.pid, 'SIGKILL')
    } catch {
      // Already gone.
    }
  }
  const kill = () => (options.forThread ? killGroup() : proc.kill())
  const timer = setTimeout(kill, options.timeoutMs ?? DEFAULT_TIMEOUT_MS)
  options.signal?.addEventListener('abort', kill, { once: true })
  try {
    const stdout = collect(proc.stdout)
    const stderr = collect(proc.stderr)
    const exitCode = await proc.exited
    // Background processes would keep the output open, and keep running, otherwise.
    if (options.forThread && !windows) killGroup()
    // One that left the group (setsid) may still hold the output: give it a moment, then
    // stop waiting, so the call returns.
    const [out, err] = await Promise.all([stdout.settle(), stderr.settle()])
    return { exitCode, stdout: clip(out), stderr: clip(err) }
  } finally {
    clearTimeout(timer)
    options.signal?.removeEventListener('abort', kill)
  }
}

const OUTPUT_GRACE_MS = 1000

/**
 * Read a process's output as it comes. `settle` gives what was read once the output ends,
 * or after a short grace, whichever is first, and stops reading.
 */
function collect(stream: ReadableStream<Uint8Array>) {
  const reader = stream.getReader()
  const decoder = new TextDecoder()
  let text = ''
  const ended = (async () => {
    try {
      for (;;) {
        const chunk = await reader.read()
        if (chunk.done) break
        text += decoder.decode(chunk.value, { stream: true })
      }
    } catch {
      // Cancelled after the grace.
    }
  })()
  return {
    async settle(): Promise<string> {
      let timer: ReturnType<typeof setTimeout> | undefined
      const late = new Promise<void>((done) => {
        timer = setTimeout(done, OUTPUT_GRACE_MS)
      })
      await Promise.race([ended, late])
      clearTimeout(timer)
      await reader.cancel().catch(() => {})
      return text
    },
  }
}

/** Commands run as they are, for workspaces made without a host. */
const unsandboxed: CommandSandbox = { kind: null, wrap: (argv) => argv }

/**
 * A directory on this machine, with every path kept inside it. Commands run in `sandbox`,
 * which keeps `keep` visible: the root, or the whole checkout and the git directory
 * behind it when the root is part of one.
 */
export class LocalWorkspace implements Workspace {
  private readonly sandbox: CommandSandbox
  private readonly keep: string[]
  private readonly readOnly: string[]

  constructor(
    readonly id: string,
    readonly root: string,
    readonly branch: string | null,
    options: { sandbox?: CommandSandbox; keep?: string[]; readOnly?: string[] } = {},
  ) {
    this.sandbox = options.sandbox ?? unsandboxed
    this.keep = options.keep ?? [root]
    this.readOnly = options.readOnly ?? []
  }

  get label(): string {
    return this.root
  }

  /**
   * Resolve a workspace path to an absolute one inside the root. The nearest existing
   * ancestor is resolved through symbolic links, so a link cannot lead outside.
   */
  async resolve(path: string): Promise<string> {
    const root = await realpath(this.root)
    const target = resolve(root, path || '.')
    const inside = (candidate: string) => {
      const rel = relative(root, candidate)
      return rel === '' || (!rel.startsWith('..') && !isAbsolute(rel))
    }
    if (!inside(target)) throw new WorkspacePathError(path)
    let existing = target
    while (!existsSync(existing) && existing !== root) existing = dirname(existing)
    const real = await realpath(existing)
    if (!inside(real)) throw new WorkspacePathError(path)
    return join(real, relative(existing, target))
  }

  /**
   * Open a workspace file, and make sure what was opened is the file inside the root. A
   * command left running could swap a directory on the way for a link between resolving
   * the path and opening it; the open file must then be the one the path resolves to now,
   * still inside the root, or it is refused. Nothing is truncated before that check. A
   * refused write may leave an empty file or directory where the swapped path led: removing
   * it by path again would follow the same swap and could delete a file outside.
   */
  private async open(path: string, flags: number): Promise<FileHandle> {
    const target = await this.resolve(path)
    const handle = await open(target, flags | constants.O_NOFOLLOW).catch(() => {
      throw new WorkspacePathError(path)
    })
    try {
      const [opened, now] = await Promise.all([handle.stat(), stat(await this.resolve(path))])
      if (opened.dev !== now.dev || opened.ino !== now.ino) throw new WorkspacePathError(path)
      return handle
    } catch (error) {
      await handle.close()
      throw error instanceof WorkspacePathError ? error : new WorkspacePathError(path)
    }
  }

  async readFile(path: string): Promise<string> {
    const handle = await this.open(path, constants.O_RDONLY)
    try {
      return await handle.readFile('utf8')
    } finally {
      await handle.close()
    }
  }

  async writeFile(path: string, content: string): Promise<void> {
    const target = await this.resolve(path)
    await mkdir(dirname(target), { recursive: true })
    const handle = await this.open(path, constants.O_WRONLY | constants.O_CREAT)
    try {
      await handle.truncate(0)
      await handle.writeFile(content)
    } finally {
      await handle.close()
    }
  }

  async list(path: string): Promise<{ name: string; kind: 'file' | 'directory' }[]> {
    const entries = await readdir(await this.resolve(path), { withFileTypes: true })
    return entries
      .filter((entry) => entry.name !== '.git')
      .map((entry) => ({
        name: entry.name,
        kind: entry.isDirectory() ? ('directory' as const) : ('file' as const),
      }))
      .toSorted((a, b) => a.name.localeCompare(b.name))
  }

  async exec(
    command: string,
    options: { cwd?: string; timeoutMs?: number; signal?: AbortSignal } = {},
  ): Promise<ExecResult> {
    const cwd = await this.resolve(options.cwd ?? '.')
    return run(
      this.sandbox.wrap(['bash', '-c', command], { keep: this.keep, readOnly: this.readOnly }),
      {
        cwd,
        forThread: true,
        ...(options.timeoutMs ? { timeoutMs: options.timeoutMs } : {}),
        ...(options.signal ? { signal: options.signal } : {}),
      },
    )
  }

  async dispose(): Promise<void> {
    // Worktrees stay: the branch holds the thread's work.
  }
}

async function gitRoot(path: string): Promise<string | null> {
  const result = await git(path, 'rev-parse', '--show-toplevel')
  return result.exitCode === 0 ? result.stdout.trim() : null
}

// The user's repository needs no network; `file` only for local submodules and the like.
async function git(cwd: string, ...args: string[]): Promise<ExecResult> {
  return run(['git', ...args], { cwd, env: serverGitEnv(['file']) })
}

/** Branch names stay short: the random tail of the thread id is unique enough. */
export const branchFor = (threadId: string) => `ariadne/${threadId.slice(-12)}`

/**
 * A git repository gives every thread its own worktree on branch `ariadne/<id tail>`,
 * created from the repository's current HEAD, so threads never see each other's changes.
 * When the directory is a subdirectory of the repository, the workspace is that
 * subdirectory of the worktree. Any other directory is shared by all threads as it is.
 *
 * A new worktree gets the checkout's ignored dependency directories (`node_modules` and
 * the like) as copy-on-write clones where the filesystem supports them, so it can build
 * at once without taking room; elsewhere it is a plain worktree. `clone: null` turns it off.
 *
 * Worktrees are not removed automatically yet; `git worktree remove <dir>` (and deleting
 * the `ariadne/*` branch) in the repository cleans one up.
 */
export function localWorkspaces(input: {
  path: string
  repoRoot: string | null
  dataDir: string
  sandbox?: CommandSandbox
  clone?: Cloner | null
}): WorkspaceCapability {
  const { sandbox } = input
  const clone = input.clone === undefined ? platformCloner : input.clone
  return {
    isolated: input.repoRoot !== null,
    async open({ threadId }) {
      const { repoRoot } = input
      if (!repoRoot) return new LocalWorkspace(threadId, input.path, null, { sandbox })
      const branch = branchFor(threadId)
      const key = createHash('sha256').update(repoRoot).digest('hex').slice(0, 12)
      const dir = join(input.dataDir, 'worktrees', key, threadId)
      if (!existsSync(join(dir, '.git'))) {
        await mkdir(dirname(dir), { recursive: true })
        // A worktree whose directory was deleted by hand is still registered; forget it
        // first or `worktree add` refuses the branch.
        await git(repoRoot, 'worktree', 'prune')
        const hasBranch =
          (await git(repoRoot, 'rev-parse', '--verify', '--quiet', `refs/heads/${branch}`))
            .exitCode === 0
        const result = hasBranch
          ? await git(repoRoot, 'worktree', 'add', dir, branch)
          : await git(repoRoot, 'worktree', 'add', '-b', branch, dir, 'HEAD')
        if (result.exitCode !== 0) {
          throw new Error(result.stderr.trim() || `git worktree add exited ${result.exitCode}`)
        }
        if (clone) await seedWorktree(repoRoot, dir, git, clone)
      }
      // A subdirectory of the repository stays the workspace root inside the worktree.
      const sub = relative(await realpath(repoRoot), await realpath(input.path))
      const root = sub ? join(dir, sub) : dir
      if (!existsSync(root)) {
        // The worktree holds only what HEAD tracks.
        throw new Error(
          `${sub} is not in the repository's current commit (untracked or ignored), so the thread's checkout has no such directory; commit it, or choose the repository root`,
        )
      }
      // git in the worktree writes to the repository's own git directory, but must not
      // change what makes git run something: the server runs git there, and so does the user.
      const common = (
        await git(repoRoot, 'rev-parse', '--path-format=absolute', '--git-common-dir')
      ).stdout.trim()
      // A hooks directory that is missing could be made by a thread; make it first.
      if (common) await mkdir(join(common, 'hooks'), { recursive: true })
      return new LocalWorkspace(threadId, root, branch, {
        sandbox,
        keep: [dir, ...(common ? [common] : [])],
        readOnly: common ? gitControlPaths(common) : [],
      })
    },
  }
}

/** The "local directory" context source; checkouts go under `dataDir`. */
export function localDirectoryProvider(
  dataDir: string,
  sandbox?: CommandSandbox,
  clone?: Cloner | null,
): ContextProvider<LocalSettings> {
  return {
    kind: 'local-directory',
    title: { 'zh-CN': '本地目录', en: 'Local directory' },
    settings: {
      type: 'object',
      properties: {
        path: {
          type: 'string',
          title: { 'zh-CN': '目录', en: 'Directory' },
          description: {
            'zh-CN': 'git 仓库会为每个 thread 建一个独立的 worktree',
            en: 'A git repository gets a separate worktree per thread',
          },
        },
      },
      required: ['path'],
    },
    provides: ['workspace'],
    async connect(settings) {
      const path = settings.path?.trim()
      if (!path || !isAbsolute(path)) throw new Error('the directory must be an absolute path')
      const info = await stat(path).catch(() => null)
      if (!info?.isDirectory()) throw new Error(`${path} is not a directory`)
      const repoRoot = await gitRoot(path)
      return {
        workspace: localWorkspaces({
          path,
          repoRoot,
          dataDir,
          ...(sandbox ? { sandbox } : {}),
          ...(clone !== undefined ? { clone } : {}),
        }),
      }
    },
  }
}

export default definePlugin({
  id: 'local',
  name: { 'zh-CN': '本地目录', en: 'Local directory' },
  setup(ctx) {
    return ctx.registerContextProvider(
      localDirectoryProvider(ctx.dataDir, ctx.sandbox) as ContextProvider<never>,
    )
  },
})
