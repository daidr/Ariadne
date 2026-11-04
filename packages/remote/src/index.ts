import {
  FileTooLargeError,
  WorkspacePathError,
  type BranchCapability,
  type ExecResult,
  type GitObjects,
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
  BUNDLE,
  EXEC,
  HAS,
  EXISTS,
  GIT,
  LIST,
  OPEN_WORKTREE,
  OUTSIDE,
  PROBE,
  READ,
  READ_BYTES,
  RECEIVE,
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
 *
 * `tracking` workspaces are for a checkout of a hosted repository: branches start from and
 * are compared against `origin/<base>`, and reach it through pull requests, not a merge.
 */
export function remoteWorkspaces(
  shell: RemoteShell,
  source: Probe,
  tracking = false,
): WorkspaceCapability {
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
  const baseRef = (base: string) =>
    tracking ? `refs/remotes/origin/${base}` : `refs/heads/${base}`
  return {
    isolated: true,
    ...(tracking
      ? {}
      : {
          checkout: {
            async originUrl() {
              const result = await git(['config', '--get', 'remote.origin.url'])
              return result.exitCode === 0 ? result.stdout.trim() || null : null
            },
            objects: remoteObjects(shell, repoRoot),
            tracking: () => remoteWorkspaces(shell, source, true),
          },
        }),
    branches: {
      async changes(threadId, base) {
        const dir = dirOf(threadId)
        return branchChanges(git, {
          branch: branchFor(threadId),
          base,
          baseRef: baseRef(base),
          worktree: (await exists(dir)) ? dir : null,
        })
      },
      file: (request) => fileVersions(git, request),
      merged: (threadId, base) => branchMerged(git, branchFor(threadId), baseRef(base)),
      ...(tracking
        ? {}
        : {
            merge: ({
              threadId,
              ...request
            }: Parameters<NonNullable<BranchCapability['merge']>>[0]) =>
              mergeBranch(git, identity, { branch: branchFor(threadId), ...request }),
          }),
      remove: (threadId, options) => removeWorktree(git, dirOf(threadId), exists, options),
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

/** Refs and objects of the repository `repo` on the machine `shell` reaches. */
export function remoteObjects(shell: RemoteShell, repo: string): GitObjects {
  const call = async (script: string, input: string, what: string) => {
    const result = await shell.run(script, [repo], { input, timeoutMs: 600_000 })
    if (result.exitCode !== 0) {
      throw new Error(`${what} failed: ${failure(shell, repo, result).message}`)
    }
    return result.stdout
  }
  return {
    async refs(prefixes) {
      const result = await shell.run(GIT, [
        repo,
        'for-each-ref',
        '--format=%(objectname) %(refname)',
        '--',
        ...prefixes,
      ])
      if (result.exitCode !== 0) throw failure(shell, repo, result)
      return parseRefs(result.stdout)
    },
    async has(shas) {
      if (!shas.length) return []
      return (await call(HAS, `${shas.join('\n')}\n`, 'git cat-file')).split('\n').filter(Boolean)
    },
    async bundle(tips, exclude) {
      const revs = [...tips, ...exclude.map((sha) => `^${sha}`)].join('\n')
      const out = await call(BUNDLE, `${revs}\n`, 'git bundle')
      return new Uint8Array(Buffer.from(out.replace(/\s+/g, ''), 'base64'))
    },
    async receive(bundle, refs) {
      const updates = Object.entries(refs).map(([ref, sha]) => `update ${ref} ${sha}\n`)
      const data = bundle ? Buffer.from(bundle).toString('base64') : ''
      await call(RECEIVE, `${updates.join('')}\n${data}`, 'receiving commits')
    },
  }
}

/** `<sha> <ref>` lines, as `for-each-ref` prints them. */
export function parseRefs(text: string): Record<string, string> {
  const refs: Record<string, string> = {}
  for (const line of text.split('\n')) {
    const [sha, ref] = line.trim().split(' ')
    if (sha && ref) refs[ref] = sha
  }
  return refs
}

/** The workspaces of the directory `path` on the machine `shell` reaches. */
export async function connectRemote(
  shell: RemoteShell,
  path: string,
): Promise<WorkspaceCapability> {
  return remoteWorkspaces(shell, await probe(shell, path))
}
