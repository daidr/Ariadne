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

/** Run a program without a shell. */
export async function run(
  argv: string[],
  options: { cwd: string; timeoutMs?: number; signal?: AbortSignal },
): Promise<ExecResult> {
  const proc = Bun.spawn(argv, {
    cwd: options.cwd,
    stdout: 'pipe',
    stderr: 'pipe',
    stdin: 'ignore',
    env: { ...process.env, GIT_TERMINAL_PROMPT: '0' },
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
 * Any other directory is shared by all threads as it is.
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
      return new LocalWorkspace(threadId, dir, branch)
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
