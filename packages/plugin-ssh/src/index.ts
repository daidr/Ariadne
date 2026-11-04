import {
  definePlugin,
  WorkspacePathError,
  type ContextProvider,
  type ExecResult,
  type Workspace,
  type WorkspaceCapability,
} from '@ariadne/plugin-sdk'
import { branchFor } from '@ariadne/plugin-local'
import { createHash } from 'node:crypto'
import { posix } from 'node:path'
import { EXEC, LIST, OPEN_WORKTREE, OUTSIDE, PROBE, READ, STARTED, WRITE } from './scripts'
import { SSH_FAILED, SshShell, type RemoteShell } from './transport'

export { LocalShell, SshShell, type RemoteShell } from './transport'

export interface SshSettings {
  host: string
  port?: number
  path: string
}

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
function failure(path: string, result: ExecResult): Error {
  if (result.exitCode === 3) return new WorkspacePathError(path)
  const reason = result.stderr.trim() || `exited ${result.exitCode}`
  return new Error(
    result.exitCode === SSH_FAILED && !reason.startsWith('ssh:') ? `ssh: ${reason}` : reason,
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
  ) {}

  get label(): string {
    return `${this.shell.host}:${this.root}`
  }

  async readFile(path: string): Promise<string> {
    const result = await this.shell.run(READ, [this.root, relativePath(path)])
    if (result.exitCode !== 0) throw failure(path, result)
    return result.stdout
  }

  async writeFile(path: string, content: string): Promise<void> {
    const rel = relativePath(path)
    if (!rel) throw new WorkspacePathError(path)
    const result = await this.shell.run(WRITE, [this.root, rel], { input: content })
    if (result.exitCode !== 0) throw failure(path, result)
  }

  async list(path: string): Promise<{ name: string; kind: 'file' | 'directory' }[]> {
    const result = await this.shell.run(LIST, [this.root, relativePath(path)])
    if (result.exitCode !== 0) throw failure(path, result)
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
    if (result.exitCode === SSH_FAILED && !started) {
      return { ...result, stderr: `${stderr}\n[the SSH connection failed]` }
    }
    return { ...result, stderr }
  }

  async dispose(): Promise<void> {
    // Worktrees stay: the branch holds the thread's work.
  }
}

interface Probe {
  /** The real path of the source. */
  path: string
  /** The checkout it is part of, or `null`. */
  repoRoot: string | null
  /** The path of the source inside the checkout; empty at its root. */
  sub: string
  home: string
}

async function probe(shell: RemoteShell, path: string): Promise<Probe> {
  const result = await shell.run(PROBE, [path], { timeoutMs: 30_000 })
  if (result.exitCode !== 0) throw failure(path, result)
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
  return {
    isolated: source.repoRoot !== null,
    async open({ threadId, ref }) {
      const { repoRoot } = source
      if (!repoRoot) return new RemoteWorkspace(threadId, shell, source.path, null)
      const branch = branchFor(threadId)
      const key = createHash('sha256').update(repoRoot).digest('hex').slice(0, 12)
      const dir = posix.join(source.home, WORKTREE_DIR, key, threadId)
      const result = await shell.run(
        OPEN_WORKTREE,
        [repoRoot, dir, branch, ref || 'HEAD', source.sub],
        { timeoutMs: 600_000 },
      )
      if (result.exitCode !== 0) throw failure(dir, result)
      const root = result.stdout.split('\0')[0] || dir
      return new RemoteWorkspace(threadId, shell, root, branch)
    },
  }
}

/** The "SSH" context source; shared connections keep their sockets under `dataDir`. */
export function sshProvider(
  dataDir: string,
  makeShell: (settings: { host: string; port?: number }) => RemoteShell = (target) =>
    new SshShell(target, posix.join(dataDir, 'cm')),
): ContextProvider<SshSettings> {
  return {
    kind: 'ssh',
    title: { 'zh-CN': 'SSH 远程目录', en: 'SSH remote directory' },
    remote: true,
    summary: '{host}:{path}',
    settings: {
      type: 'object',
      properties: {
        host: {
          type: 'string',
          title: { 'zh-CN': '主机', en: 'Host' },
          description: {
            'zh-CN':
              'user@host 或 ~/.ssh/config 里的 Host。使用你自己的密钥和 ssh-agent，不支持密码登录；第一次连接前先在终端里 ssh 一次，确认主机指纹',
            en: 'user@host or a Host from ~/.ssh/config. Uses your own keys and ssh-agent; passwords are not supported. Connect once in a terminal first to accept the host key',
          },
        },
        port: { type: 'number', title: { 'zh-CN': '端口', en: 'Port' } },
        path: {
          type: 'string',
          title: { 'zh-CN': '远端目录', en: 'Remote directory' },
          description: {
            'zh-CN': '绝对路径。git 仓库会在远端为每个 thread 建一个独立的 worktree',
            en: 'An absolute path. A git repository gets a separate worktree per thread on the remote machine',
          },
        },
      },
      required: ['host', 'path'],
    },
    provides: ['workspace'],
    async connect(settings) {
      const host = settings.host?.trim()
      const path = settings.path?.trim()
      // Anything starting with `-` would be read as an option of ssh.
      if (!host || host.startsWith('-') || /\s/.test(host)) throw new Error('the host is not valid')
      if (!path || !posix.isAbsolute(path))
        throw new Error('the directory must be an absolute path')
      const port = settings.port ? Number(settings.port) : undefined
      if (port !== undefined && !(Number.isInteger(port) && port > 0 && port < 65536)) {
        throw new Error('the port is not valid')
      }
      const shell = makeShell({ host, ...(port ? { port } : {}) })
      const found = await probe(shell, path)
      return { workspace: remoteWorkspaces(shell, found) }
    },
  }
}

export default definePlugin({
  id: 'ssh',
  name: { 'zh-CN': 'SSH 远程目录', en: 'SSH remote directory' },
  setup(ctx) {
    return ctx.registerContextProvider(sshProvider(ctx.dataDir) as ContextProvider<never>)
  },
})
