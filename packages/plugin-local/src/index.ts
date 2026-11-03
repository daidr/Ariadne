import {
  definePlugin,
  WorkspacePathError,
  type ContextProvider,
  type ExecResult,
  type Workspace,
  type WorkspaceCapability,
} from '@ariadne/plugin-sdk'
import { createHash } from 'node:crypto'
import { existsSync } from 'node:fs'
import { mkdir, readdir, readFile, realpath, stat, writeFile } from 'node:fs/promises'
import { dirname, isAbsolute, join, relative, resolve } from 'node:path'

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
])

/**
 * The environment for commands a thread runs: the server's own, without its credentials
 * (Ariadne's local token, GitHub tokens, git config passed through the environment).
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
  const base = options.forThread ? threadEnv() : process.env
  const proc = Bun.spawn(argv, {
    cwd: options.cwd,
    stdout: 'pipe',
    stderr: 'pipe',
    stdin: 'ignore',
    env: { ...base, GIT_TERMINAL_PROMPT: '0', ...options.env },
  })
  const kill = () => proc.kill()
  const timer = setTimeout(kill, options.timeoutMs ?? DEFAULT_TIMEOUT_MS)
  options.signal?.addEventListener('abort', kill, { once: true })
  try {
    const [stdout, stderr, exitCode] = await Promise.all([
      new Response(proc.stdout).text(),
      new Response(proc.stderr).text(),
      proc.exited,
    ])
    return { exitCode, stdout: clip(stdout), stderr: clip(stderr) }
  } finally {
    clearTimeout(timer)
    options.signal?.removeEventListener('abort', kill)
  }
}

/** A directory on this machine, with every path kept inside it. */
export class LocalWorkspace implements Workspace {
  constructor(
    readonly id: string,
    readonly root: string,
    readonly branch: string | null,
  ) {}

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

  async readFile(path: string): Promise<string> {
    return readFile(await this.resolve(path), 'utf8')
  }

  async writeFile(path: string, content: string): Promise<void> {
    const target = await this.resolve(path)
    await mkdir(dirname(target), { recursive: true })
    await writeFile(target, content)
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
    return run(['bash', '-c', command], {
      cwd,
      forThread: true,
      ...(options.timeoutMs ? { timeoutMs: options.timeoutMs } : {}),
      ...(options.signal ? { signal: options.signal } : {}),
    })
  }

  async dispose(): Promise<void> {
    // Worktrees stay: the branch holds the thread's work.
  }
}

async function gitRoot(path: string): Promise<string | null> {
  const result = await run(['git', 'rev-parse', '--show-toplevel'], { cwd: path })
  return result.exitCode === 0 ? result.stdout.trim() : null
}

async function git(cwd: string, ...args: string[]): Promise<ExecResult> {
  return run(['git', ...args], { cwd })
}

/** Branch names stay short: the random tail of the thread id is unique enough. */
export const branchFor = (threadId: string) => `ariadne/${threadId.slice(-12)}`

/**
 * A git repository gives every thread its own worktree on branch `ariadne/<id tail>`,
 * created from the repository's current HEAD, so threads never see each other's changes.
 * When the directory is a subdirectory of the repository, the workspace is that
 * subdirectory of the worktree. Any other directory is shared by all threads as it is.
 *
 * Worktrees are not removed automatically yet; `git worktree remove <dir>` (and deleting
 * the `ariadne/*` branch) in the repository cleans one up.
 */
export function localWorkspaces(input: {
  path: string
  repoRoot: string | null
  dataDir: string
}): WorkspaceCapability {
  return {
    isolated: input.repoRoot !== null,
    async open({ threadId }) {
      const { repoRoot } = input
      if (!repoRoot) return new LocalWorkspace(threadId, input.path, null)
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
      return new LocalWorkspace(threadId, root, branch)
    },
  }
}

/** The "local directory" context source; checkouts go under `dataDir`. */
export function localDirectoryProvider(dataDir: string): ContextProvider<LocalSettings> {
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
      return { workspace: localWorkspaces({ path, repoRoot, dataDir }) }
    },
  }
}

export default definePlugin({
  id: 'local',
  name: { 'zh-CN': '本地目录', en: 'Local directory' },
  setup(ctx) {
    return ctx.registerContextProvider(
      localDirectoryProvider(ctx.dataDir) as ContextProvider<never>,
    )
  },
})
