import {
  FileTooLargeError,
  WorkspacePathError,
  type ExecResult,
  type Workspace,
  type WorkspaceCapability,
} from '@ariadne/plugin-sdk'
import {
  branchChanges,
  branchFor,
  branchMerged,
  fileVersions,
  gitIdentity,
  mergeBranch,
  removeWorktree,
  type GitRun,
} from '@ariadne/plugin-local'
import { createHash } from 'node:crypto'
import { posix } from 'node:path'
import {
  EXEC,
  EXISTS,
  GIT,
  LIST,
  OPEN_WORKTREE,
  OUTSIDE,
  PROBE,
  READ,
  READ_BYTES,
  STARTED,
  WORKTREES,
  WRITE,
} from './scripts'
import { CONNECTION_FAILED, type RemoteShell } from './shell'

export {
  clip,
  CONNECTION_FAILED,
  DEFAULT_TIMEOUT_MS,
  LocalShell,
  payload,
  REMOTE_COMMAND,
  shellQuote,
  spawnScript,
  type RemoteShell,
  type ShellRun,
} from './shell'

/** Where threads' worktrees go on the remote machine, under the user's home. */
export const WORKTREE_DIR = '.ariadne-worktrees'

/**
 * A workspace path as the scripts take it: relative, `/`-separated, without `.` or `..`
 * parts. One that leaves the root is refused here already.
 */
export function relativePath(path: string): string {
  const normal = posix.normalize(path || '.')
  if (posix.isAbsolute(normal) || normal === '..' || normal.startsWith('../')) {
    throw new WorkspacePathError(path)
  }
  return normal === '.' ? '' : normal.replace(/\/+$/, '')
}

/** What a failed script means: a path outside, or the reason it gave. */
function failure(shell: RemoteShell, path: string, result: ExecResult): Error {
  if (result.exitCode === 3) return new WorkspacePathError(path)
  const reason = result.stderr.trim() || `exited ${result.exitCode}`
  const prefix = `${shell.via}:`
  return new Error(
    result.exitCode === CONNECTION_FAILED && !reason.startsWith(prefix)
      ? `${prefix} ${reason}`
      : reason,
  )
}

/**
 * A directory on another machine. Files are read and written, and commands run, as the
 * user the connection logs in as; paths are kept inside the root, including through
 * symbolic links. Commands run with bash in a process group of their own, which ends
 * when they do, when they time out, or when the thread is stopped.
 */
export class RemoteWorkspace implements Workspace {
  constructor(
    readonly id: string,
    private readonly shell: RemoteShell,
    /** The real path of the root on the remote machine. */
    readonly root: string,
    readonly branch: string | null,
    readonly base: string | null = null,
  ) {}

  get label(): string {
    return `${this.shell.host}:${this.root}`
  }

  async readFile(path: string): Promise<string> {
    const result = await this.shell.run(READ, [this.root, relativePath(path)])
    if (result.exitCode !== 0) throw failure(this.shell, path, result)
    return result.stdout
  }

  async readBytes(path: string, maxBytes: number): Promise<Uint8Array> {
    const result = await this.shell.run(READ_BYTES, [
      this.root,
      relativePath(path),
      String(maxBytes),
    ])
    if (result.exitCode === 6) {
      throw new FileTooLargeError(path, Number(result.stderr.trim()) || maxBytes + 1, maxBytes)
    }
    if (result.exitCode !== 0) throw failure(this.shell, path, result)
    const bytes = new Uint8Array(Buffer.from(result.stdout.replace(/\s+/g, ''), 'base64'))
    if (bytes.length > maxBytes) throw new FileTooLargeError(path, bytes.length, maxBytes)
    return bytes
  }

  async writeFile(path: string, content: string): Promise<void> {
    const rel = relativePath(path)
    if (!rel) throw new WorkspacePathError(path)
    const result = await this.shell.run(WRITE, [this.root, rel], { input: content })
    if (result.exitCode !== 0) throw failure(this.shell, path, result)
  }

  async list(path: string): Promise<{ name: string; kind: 'file' | 'directory' }[]> {
    const result = await this.shell.run(LIST, [this.root, relativePath(path)])
    if (result.exitCode !== 0) throw failure(this.shell, path, result)
    return result.stdout
      .split('\0')
      .filter(Boolean)
      .map((entry) => ({
        name: entry.slice(2),
        kind: entry.startsWith('d ') ? ('directory' as const) : ('file' as const),
      }))
      .toSorted((a, b) => a.name.localeCompare(b.name))
  }

  async exec(
    command: string,
    options: { cwd?: string; timeoutMs?: number; signal?: AbortSignal } = {},
  ): Promise<ExecResult> {
    const cwd = options.cwd ?? '.'
    const result = await this.shell.run(EXEC, [this.root, relativePath(cwd), command], {
      hold: true,
      clip: true,
      ...(options.timeoutMs ? { timeoutMs: options.timeoutMs } : {}),
      ...(options.signal ? { signal: options.signal } : {}),
    })
    // The command's own failures are its exit code; a path outside is the caller's.
    // Whatever came before the marker is the login shell's own (a greeting in its startup
    // files); output cut for length lost the marker but certainly ran.
    const marker = result.stderr.indexOf(`${STARTED}\n`)
    const started = marker >= 0 || result.stderr.startsWith('[… ')
    const stderr = marker >= 0 ? result.stderr.slice(marker + STARTED.length + 1) : result.stderr
    if (result.exitCode === 3 && stderr.trim() === OUTSIDE) throw new WorkspacePathError(cwd)
    // The script never ran: ssh itself failed to connect, or lost the connection first.
    if (result.exitCode === CONNECTION_FAILED && !started) {
      return { ...result, stderr: `${stderr}\n[the ${this.shell.via} connection failed]` }
    }
    return { ...result, stderr }
  }

  async dispose(): Promise<void> {
    // Worktrees stay: the branch holds the thread's work.
  }
}

export interface Probe {
  /** The real path of the source. */
  path: string
  /** The checkout it is part of, or `null`. */
  repoRoot: string | null
  /** The path of the source inside the checkout; empty at its root. */
  sub: string
  home: string
}

export async function probe(shell: RemoteShell, path: string): Promise<Probe> {
  const result = await shell.run(PROBE, [path], { timeoutMs: 30_000 })
  if (result.exitCode !== 0) throw failure(shell, path, result)
  const [real = path, top = '', sub = '', home = ''] = result.stdout.split('\0')
  return { path: real, repoRoot: top || null, sub, home }
}

/**
 * Workspaces on a remote machine. A git checkout gives every thread its own worktree in
 * `~/.ariadne-worktrees/<key>/<thread>` on that machine, on branch `ariadne/<id tail>`
 * created from the checkout's current HEAD (or the ref asked for), so threads never see
 * each other's changes; any other directory is shared by all threads as it is.
 */
export function remoteWorkspaces(shell: RemoteShell, source: Probe): WorkspaceCapability {
  const { repoRoot } = source
  if (!repoRoot) {
    return {
      isolated: false,
      open: async ({ threadId }) => new RemoteWorkspace(threadId, shell, source.path, null),
    }
  }
  const key = createHash('sha256').update(repoRoot).digest('hex').slice(0, 12)
  const worktrees = posix.join(source.home, WORKTREE_DIR, key)
  const dirOf = (threadId: string) => posix.join(worktrees, threadId)
  const git: GitRun = (args, options = {}) =>
    shell.run(GIT, [options.cwd ?? repoRoot, ...args], {
      timeoutMs: 120_000,
      ...(options.whole ? {} : { clip: true }),
    })
  const exists = async (path: string) => (await shell.run(EXISTS, [path])).exitCode === 0
  // The remote user's own identity, as their git there would commit.
  const identity = gitIdentity((name) => git(['config', '--get', name]))
  return {
    isolated: true,
    branches: {
      async changes(threadId, base) {
        const dir = dirOf(threadId)
        return branchChanges(git, {
          branch: branchFor(threadId),
          base,
          baseRef: `refs/heads/${base}`,
          worktree: (await exists(dir)) ? dir : null,
        })
      },
      file: (request) => fileVersions(git, request),
      merged: (threadId, base) => branchMerged(git, branchFor(threadId), `refs/heads/${base}`),
      merge: ({ threadId, ...request }) =>
        mergeBranch(git, identity, { branch: branchFor(threadId), ...request }),
      remove: (threadId) => removeWorktree(git, dirOf(threadId), exists),
      async worktrees() {
        const result = await shell.run(WORKTREES, [worktrees], { timeoutMs: 120_000 })
        if (result.exitCode !== 0) throw failure(shell, worktrees, result)
        const fields = result.stdout.split('\0')
        const list = []
        for (let i = 0; i + 2 < fields.length; i += 3) {
          const kib = Number.parseInt(fields[i + 2]!, 10)
          list.push({
            threadId: fields[i]!,
            path: `${shell.host}:${fields[i + 1]!}`,
            bytes: Number.isFinite(kib) ? kib * 1024 : null,
          })
        }
        return list
      },
    },
    async open({ threadId, ref, base: recorded }) {
      const branch = branchFor(threadId)
      const dir = dirOf(threadId)
      let base = recorded ?? null
      if (recorded === undefined) {
        const head = await git(['symbolic-ref', '--quiet', '--short', 'HEAD'])
        base = head.exitCode === 0 ? head.stdout.trim() || null : null
      }
      const result = await shell.run(
        OPEN_WORKTREE,
        [repoRoot, dir, branch, ref || 'HEAD', source.sub],
        { timeoutMs: 600_000 },
      )
      if (result.exitCode !== 0) throw failure(shell, dir, result)
      const root = result.stdout.split('\0')[0] || dir
      return new RemoteWorkspace(threadId, shell, root, branch, base)
    },
  }
}

/** The workspaces of the directory `path` on the machine `shell` reaches. */
export async function connectRemote(
  shell: RemoteShell,
  path: string,
): Promise<WorkspaceCapability> {
  return remoteWorkspaces(shell, await probe(shell, path))
}
