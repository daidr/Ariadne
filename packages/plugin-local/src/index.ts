import {
  browseLocal,
  defaultShell,
  definePlugin,
  processes,
  shellArgv,
  FileTooLargeError,
  searchableView,
  WorkspacePathError,
  type CommandSandbox,
  type ContextProvider,
  type ExecOptions,
  type ExecResult,
  type Workspace,
  type WorkspaceCapability,
  type WorktreeInfo,
} from '@ariadne/plugin-sdk'
import { createHash } from 'node:crypto'
import { constants, existsSync, realpathSync } from 'node:fs'
import { mkdir, open, readdir, realpath, stat, type FileHandle } from 'node:fs/promises'
import { dirname, isAbsolute, join, relative, resolve } from 'node:path'
import {
  branchChanges,
  branchMerged,
  fileVersions,
  gitIdentity,
  mergeBranch,
  removeWorktree,
  treeDiff,
  dropSnapshots,
  worktreeSnapshot,
  type GitRun,
  type Identity,
} from './branches'
import { platformCloner, seedWorktree, type Cloner } from './cow'

export * from './branches'

export interface LocalSettings {
  path: string
}

/** Commands may run this long unless the caller asks for less. */
const DEFAULT_TIMEOUT_MS = 120_000
/**
 * Output beyond this is cut in the middle, keeping how it began and, larger, the end where
 * errors usually are. Callers cut further for the model; this only bounds memory.
 */
const MAX_OUTPUT = 1_000_000

function clip(text: string): string {
  if (text.length <= MAX_OUTPUT) return text
  const head = MAX_OUTPUT / 4
  const tail = text.length - (MAX_OUTPUT - head)
  return `${text.slice(0, head)}\n[… ${tail - head} characters cut …]\n${text.slice(tail)}`
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
    /** Keep the whole output, e.g. a file's contents; otherwise long output keeps its end. */
    whole?: boolean
    /** Called with output as it comes. */
    onOutput?: ExecOptions['onOutput']
  },
): Promise<ExecResult> {
  // Stopped before it started (say, while the worktree was being made): an abort listener
  // added now would never fire, so the command must not start at all.
  if (options.signal?.aborted) return { exitCode: 130, stdout: '', stderr: 'cancelled' }
  const base = options.forThread ? threadEnv() : process.env
  let proc: ReturnType<typeof spawnPiped>
  try {
    proc = spawnPiped(argv, {
      cwd: options.cwd,
      env: { ...base, GIT_TERMINAL_PROMPT: '0', ...options.env },
      detached: !!options.forThread && !windows,
    })
  } catch (error) {
    // The program is not there (or cannot run): as a shell says it.
    const reason = error instanceof Error ? error.message : String(error)
    return { exitCode: 127, stdout: '', stderr: `${argv[0]}: ${reason}` }
  }
  const killGroup = () => {
    if (windows) {
      // The command and what it started, while it still runs; once it has exited, the
      // processes it left behind are no longer found from it.
      Bun.spawn(['taskkill', '/T', '/F', '/PID', String(proc.pid)], {
        stdout: 'ignore',
        stderr: 'ignore',
        stdin: 'ignore',
        windowsHide: true,
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
  // A thread's command may run for hours (a dev server): if the server dies meanwhile, the
  // next start ends it.
  const release = options.forThread
    ? processes.track(proc.pid, { group: !windows, label: 'command' })
    : () => {}
  const timer = setTimeout(kill, options.timeoutMs ?? DEFAULT_TIMEOUT_MS)
  options.signal?.addEventListener('abort', kill, { once: true })
  try {
    const stdout = collect(
      proc.stdout,
      (chunk) => options.onOutput?.(chunk, 'stdout'),
      !!options.whole,
    )
    const stderr = collect(proc.stderr, (chunk) => options.onOutput?.(chunk, 'stderr'))
    const exitCode = await proc.exited
    // Background processes would keep the output open, and keep running, otherwise.
    if (options.forThread && !windows) killGroup()
    // One that left the group (setsid) may still hold the output: give it a moment, then
    // stop waiting, so the call returns.
    const [out, err] = await Promise.all([stdout.settle(), stderr.settle()])
    return { exitCode, stdout: options.whole ? out : clip(out), stderr: clip(err) }
  } finally {
    clearTimeout(timer)
    options.signal?.removeEventListener('abort', kill)
    release()
  }
}

/**
 * Start `argv` with its output piped and no input. On Windows a console program gets a
 * console window of its own unless told otherwise; every spawn in the code passes
 * `windowsHide` (checked by spawn.test.ts in the server). A thread's command is `detached`:
 * it gets a process group of its own, so what it leaves running in the background ends with
 * it (on macOS the sandbox does not do this). Windows has no process groups, and a detached
 * command there would open a console window of its own.
 */
function spawnPiped(
  argv: string[],
  options: { cwd: string; env: Record<string, string | undefined>; detached: boolean },
) {
  return Bun.spawn(argv, {
    cwd: options.cwd,
    stdout: 'pipe',
    stderr: 'pipe',
    stdin: 'ignore',
    windowsHide: true,
    env: options.env,
    detached: options.detached,
  })
}

const OUTPUT_GRACE_MS = 1000

/**
 * Read a process's output as it comes, handing each piece to `onChunk`. `settle` gives what
 * was read once the output ends, or after a short grace, whichever is first, and stops
 * reading. Unless the output is wanted `whole`, what is kept is cut in the middle as it
 * grows, so a command left running for hours does not fill memory.
 */
function collect(
  stream: ReadableStream<Uint8Array>,
  onChunk?: (chunk: string) => void,
  whole = false,
) {
  const reader = stream.getReader()
  const decoder = new TextDecoder()
  let text = ''
  const ended = (async () => {
    try {
      for (;;) {
        const chunk = await reader.read()
        if (chunk.done) break
        const piece = decoder.decode(chunk.value, { stream: true })
        text += piece
        if (!whole && text.length > MAX_OUTPUT * 2) text = clip(text)
        if (piece) onChunk?.(piece)
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

/** Whether `path` is `dir` or inside it, on this platform's paths. */
function within(path: string, dir: string): boolean {
  const rel = relative(dir, path)
  return rel === '' || (!rel.startsWith('..') && !isAbsolute(rel))
}

function real(path: string): string {
  try {
    return realpathSync(path)
  } catch {
    return resolve(path)
  }
}

/**
 * Whether the real path `path` is hidden: inside a hidden path, and not inside a kept path
 * that is itself in there (a worktree under Ariadne's home). A kept path that holds a
 * hidden one does not show it, as with the sandbox's mounts.
 */
export function hiddenPath(path: string, hidden: readonly string[], keep: string[]): boolean {
  return hidden.some(
    (dir) => within(path, dir) && !keep.some((kept) => within(kept, dir) && within(path, kept)),
  )
}

/** Commands run as they are, for workspaces made without a host. */
const unsandboxed: CommandSandbox = { kind: null, wrap: (argv) => argv }

/**
 * A directory on this machine, with every path kept inside it. Commands run in `sandbox`,
 * which keeps `keep` visible: the root, or the whole checkout and the git directory
 * behind it when the root is part of one.
 */
export class LocalWorkspace implements Workspace {
  readonly base: string | null
  private readonly sandbox: CommandSandbox
  private readonly keep: string[]
  /** `keep` through symbolic links, to compare with real paths. */
  private readonly kept: string[]
  private readonly readOnly: string[]
  private readonly hidden: readonly string[]
  private readonly git: boolean

  constructor(
    readonly id: string,
    readonly root: string,
    readonly branch: string | null,
    options: {
      sandbox?: CommandSandbox
      keep?: string[]
      readOnly?: string[]
      base?: string | null
      /** Real paths not to read, list or write; the sandbox's own by default. */
      hidden?: readonly string[]
      /** `false` refuses paths through a `.git` directory, for a view of the source. */
      git?: boolean
    } = {},
  ) {
    this.base = options.base ?? null
    this.sandbox = options.sandbox ?? unsandboxed
    this.keep = options.keep ?? [root]
    this.kept = this.keep.map(real)
    this.readOnly = options.readOnly ?? []
    this.hidden = options.hidden ?? this.sandbox.hidden ?? []
    this.git = options.git ?? true
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
    if (!within(target, root)) throw new WorkspacePathError(path)
    let existing = target
    while (!existsSync(existing) && existing !== root) existing = dirname(existing)
    const found = await realpath(existing)
    if (!within(found, root)) throw new WorkspacePathError(path)
    const full = join(found, relative(existing, target))
    if (this.refused(full, root)) throw new WorkspacePathError(path, 'is kept out of reach')
    return full
  }

  /** A hidden path, or one through git's own directory when that is not shown. */
  private refused(full: string, root: string): boolean {
    if (hiddenPath(full, this.hidden, this.kept)) return true
    if (this.git) return false
    return relative(root, full)
      .split(/[\\/]/)
      .some((part) => part.toLowerCase() === '.git')
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

  async readBytes(path: string, maxBytes: number): Promise<Uint8Array> {
    const handle = await this.open(path, constants.O_RDONLY)
    try {
      const { size } = await handle.stat()
      if (size > maxBytes) throw new FileTooLargeError(path, size, maxBytes)
      // At most one byte past the limit: a file that grew since `stat` still fails.
      const buffer = new Uint8Array(maxBytes + 1)
      let length = 0
      while (length < buffer.length) {
        const { bytesRead } = await handle.read(buffer, length, buffer.length - length, length)
        if (!bytesRead) break
        length += bytesRead
      }
      if (length > maxBytes) throw new FileTooLargeError(path, length, maxBytes)
      return buffer.slice(0, length)
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
    const dir = await this.resolve(path)
    const entries = await readdir(dir, { withFileTypes: true })
    return entries
      .filter(
        (entry) =>
          entry.name !== '.git' && !hiddenPath(join(dir, entry.name), this.hidden, this.kept),
      )
      .map((entry) => ({
        name: entry.name,
        kind: entry.isDirectory() ? ('directory' as const) : ('file' as const),
      }))
      .toSorted((a, b) => a.name.localeCompare(b.name))
  }

  async exec(command: string, options: ExecOptions = {}): Promise<ExecResult> {
    const shell = options.shell ?? defaultShell().argv
    return this.execFile(shellArgv(shell, command), options)
  }

  async execFile(argv: string[], options: Omit<ExecOptions, 'shell'> = {}): Promise<ExecResult> {
    const cwd = await this.resolve(options.cwd ?? '.')
    return run(this.sandbox.wrap(argv, { keep: this.keep, readOnly: this.readOnly }), {
      cwd,
      forThread: true,
      ...(options.timeoutMs ? { timeoutMs: options.timeoutMs } : {}),
      ...(options.signal ? { signal: options.signal } : {}),
      ...(options.env ? { env: options.env } : {}),
      ...(options.onOutput ? { onOutput: options.onOutput } : {}),
    })
  }

  async dispose(): Promise<void> {
    // Worktrees stay: the branch holds the thread's work.
  }
}

/** Space a directory takes, in bytes, as `du` counts it; `null` where it cannot. */
export async function diskUsage(dir: string): Promise<number | null> {
  try {
    const result = await run(['du', '-sk', dir], { cwd: dirname(dir) })
    const kib = Number.parseInt(result.stdout, 10)
    return result.exitCode === 0 && Number.isFinite(kib) ? kib * 1024 : null
  } catch {
    return null
  }
}

/** The worktrees under `dir`, one directory per thread, with the space each takes. */
export async function worktreesIn(dir: string): Promise<WorktreeInfo[]> {
  const entries = await readdir(dir, { withFileTypes: true }).catch(() => [])
  return Promise.all(
    entries
      .filter((entry) => entry.isDirectory())
      .map(async (entry) => {
        const path = join(dir, entry.name)
        return { threadId: entry.name, path, bytes: await diskUsage(path) }
      }),
  )
}

/** The author for commits the server writes: the user's own git identity. */
export function localIdentity(cwd: string): Identity {
  // Read with the user's own configuration, not the server's hardened one.
  return gitIdentity((key) => run(['git', 'config', '--get', key], { cwd }))
}

async function gitRoot(path: string): Promise<string | null> {
  const result = await git(path, 'rev-parse', '--show-toplevel')
  return result.exitCode === 0 ? result.stdout.trim() : null
}

// The user's repository needs no network; `file` only for local submodules and the like.
async function git(cwd: string, ...args: string[]): Promise<ExecResult> {
  return run(['git', ...args], { cwd, env: serverGitEnv(['file']) })
}

/** The checkout's branch, or `null` when it is on no branch. */
async function currentBranch(repoRoot: string): Promise<string | null> {
  const result = await git(repoRoot, 'symbolic-ref', '--quiet', '--short', 'HEAD')
  return result.exitCode === 0 ? result.stdout.trim() || null : null
}

/**
 * A thread's branch when it was given no name: the random tail of the thread id is unique
 * enough.
 */
export const branchFor = (threadId: string) => `ariadne/${threadId.slice(-12)}`

/**
 * A git repository gives every thread its own worktree on a branch of its own (the name
 * it was given, else `ariadne/<id tail>`),
 * created from the repository's current HEAD, so threads never see each other's changes.
 * When the directory is a subdirectory of the repository, the workspace is that
 * subdirectory of the worktree. Any other directory is shared by all threads as it is.
 *
 * A new worktree gets the checkout's ignored dependency directories (`node_modules` and
 * the like) as copy-on-write clones where the filesystem supports them, so it can build
 * at once without taking room; elsewhere it is a plain worktree. `clone: null` turns it off.
 *
 * The base is the branch the checkout was on when the thread's worktree was made; the
 * thread's changes are compared against it, and merged into it.
 */
export function localWorkspaces(input: {
  path: string
  repoRoot: string | null
  dataDir: string
  sandbox?: CommandSandbox
  clone?: Cloner | null
}): WorkspaceCapability {
  const { sandbox, repoRoot } = input
  const clone = input.clone === undefined ? platformCloner : input.clone
  // The coordinator reads the directory as the user has it, changes not committed and all,
  // without git's own directory and what the sandbox hides.
  const source = new LocalWorkspace('source', input.path, null, { sandbox, git: false })
  const inside = (path: string) => source.resolve(path)
  const view = async () => searchableView(source, { check: inside, checkFound: inside })
  if (!repoRoot) {
    return {
      isolated: false,
      open: async ({ threadId }) => new LocalWorkspace(threadId, input.path, null, { sandbox }),
      view,
    }
  }
  const key = createHash('sha256').update(repoRoot).digest('hex').slice(0, 12)
  const worktrees = join(input.dataDir, 'worktrees', key)
  const dirOf = (threadId: string) => join(worktrees, threadId)
  const gitRun: GitRun = (args, options = {}) =>
    run(['git', ...args], {
      cwd: options.cwd ?? repoRoot,
      env: { ...serverGitEnv(['file']), ...options.env },
      ...(options.whole ? { whole: true } : {}),
    })
  return {
    isolated: true,
    view,
    branches: {
      changes: ({ threadId, branch }, base) =>
        branchChanges(gitRun, {
          branch,
          base,
          baseRef: `refs/heads/${base}`,
          worktree: existsSync(dirOf(threadId)) ? dirOf(threadId) : null,
        }),
      file: (request) => fileVersions(gitRun, request),
      merged: ({ branch }, base) => branchMerged(gitRun, branch, `refs/heads/${base}`),
      merge: (request) => mergeBranch(gitRun, localIdentity(repoRoot), request),
      async remove(threadId, options) {
        const removed = await removeWorktree(
          gitRun,
          dirOf(threadId),
          async (dir) => existsSync(dir),
          options,
        )
        if (removed && options?.discard) await dropSnapshots(gitRun, threadId)
        return removed
      },
      worktrees: () => worktreesIn(worktrees),
      snapshot: async (threadId) =>
        existsSync(dirOf(threadId)) ? worktreeSnapshot(gitRun, dirOf(threadId), threadId) : null,
      diff: (from, to) => treeDiff(gitRun, from, to),
    },
    async open({ threadId, base: recorded, branch: named }) {
      const branch = named ?? branchFor(threadId)
      const dir = dirOf(threadId)
      const base = recorded === undefined ? await currentBranch(repoRoot) : recorded
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
        base,
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
          directory: true,
          description: {
            'zh-CN': 'git 仓库会为每个讨论串建一个独立的 worktree',
            en: 'A git repository gets a separate worktree per thread',
          },
        },
      },
      required: ['path'],
    },
    provides: ['workspace'],
    browse: (_settings, typed) => browseLocal(typed),
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
