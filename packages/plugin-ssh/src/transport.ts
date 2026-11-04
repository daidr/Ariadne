import type { ExecResult } from '@ariadne/plugin-sdk'
import { threadEnv } from '@ariadne/plugin-local'
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'

/** Quote one argument for a POSIX shell. */
export function shellQuote(value: string): string {
  return `'${value.replaceAll("'", `'\\''`)}'`
}

export interface ShellRun {
  /** Sent to the script's standard input, which then ends. */
  input?: string
  /**
   * Keep standard input open until the call ends; the script's watcher ends the command
   * when it closes. Used for thread commands, so a timeout or a stop ends them remotely.
   */
  hold?: boolean
  timeoutMs?: number
  signal?: AbortSignal
  /** Cut long output, keeping the end. File contents are never cut. */
  clip?: boolean
}

/**
 * Runs a bash script with arguments on some machine: over SSH, or on this one in tests.
 * The script gets its arguments as `$1`, `$2`, ... and nothing else from the caller.
 */
export interface RemoteShell {
  /** Names the machine for labels, e.g. `dev@box`. */
  readonly host: string
  run(script: string, args: string[], options?: ShellRun): Promise<ExecResult>
  dispose(): Promise<void>
}

/** Commands may run this long unless the caller asks for less. */
const DEFAULT_TIMEOUT_MS = 120_000
/** Output beyond this is cut, keeping the end where errors usually are. */
const MAX_OUTPUT = 64_000
/** The exit code `ssh` gives when it could not connect or lost the connection. */
export const SSH_FAILED = 255

function clip(text: string): string {
  if (text.length <= MAX_OUTPUT) return text
  return `[… ${text.length - MAX_OUTPUT} characters cut …]\n${text.slice(-MAX_OUTPUT)}`
}

/** Run `argv` with the script's input, collecting its output; ends it on timeout or abort. */
async function spawnScript(
  argv: string[],
  options: ShellRun & { env?: Record<string, string> },
): Promise<ExecResult> {
  if (options.signal?.aborted) return { exitCode: 130, stdout: '', stderr: 'cancelled' }
  const proc = Bun.spawn(argv, {
    stdin: 'pipe',
    stdout: 'pipe',
    stderr: 'pipe',
    env: { ...threadEnv(), ...options.env },
  })
  if (options.input !== undefined) proc.stdin.write(options.input)
  if (!options.hold) await proc.stdin.end()
  let ended = false
  const end = () => {
    if (ended) return
    ended = true
    // Closing the input tells the remote watcher to end the command's process group;
    // the local client follows a moment later in case the connection is gone.
    void Promise.resolve(proc.stdin.end()).catch(() => {})
    setTimeout(() => proc.kill('SIGKILL'), 2000).unref?.()
  }
  const timer = setTimeout(end, options.timeoutMs ?? DEFAULT_TIMEOUT_MS)
  options.signal?.addEventListener('abort', end, { once: true })
  try {
    const [stdout, stderr, exitCode] = await Promise.all([
      new Response(proc.stdout).text(),
      new Response(proc.stderr).text(),
      proc.exited,
    ])
    return options.clip
      ? { exitCode, stdout: clip(stdout), stderr: clip(stderr) }
      : { exitCode, stdout, stderr }
  } finally {
    clearTimeout(timer)
    options.signal?.removeEventListener('abort', end)
    if (options.hold) await Promise.resolve(proc.stdin.end()).catch(() => {})
  }
}

/**
 * The remote command line: login shells differ (bash, zsh, fish), so it only runs bash
 * with the script and its arguments, each quoted.
 */
export function remoteCommand(script: string, args: string[]): string {
  return ['exec', 'bash', '-c', shellQuote(script), 'ariadne', ...args.map(shellQuote)].join(' ')
}

export interface SshTarget {
  /** As `ssh` takes it: `user@host`, or a `Host` from `~/.ssh/config`. */
  host: string
  port?: number
}

/**
 * The options every connection uses, ahead of the user's `~/.ssh/config` (the first value
 * wins). Nothing is forwarded, so a thread's commands cannot reach back to this machine
 * through the connection: no agent, no X11, no ports. Nothing is ever asked: without a
 * key or agent that logs in, or with an unknown host key, connecting fails with a reason.
 */
export function sshOptions(controlPath: string | null): string[] {
  const options = [
    'BatchMode=yes',
    'ForwardAgent=no',
    'ForwardX11=no',
    'ClearAllForwardings=yes',
    'PermitLocalCommand=no',
    'ConnectTimeout=15',
    'ServerAliveInterval=15',
    'ServerAliveCountMax=4',
  ]
  // One connection carries every call; it stays open for two minutes after the last.
  if (controlPath) {
    options.push(`ControlMaster=auto`, `ControlPath=${controlPath}`, 'ControlPersist=120')
  }
  return [...options.flatMap((option) => ['-o', option]), '-T', '-e', 'none']
}

/** Unix sockets have short paths: longer ones get no shared connection. */
const MAX_SOCKET_PATH = 100

/** A machine reached with the system's `ssh`, using the user's own config and keys. */
export class SshShell implements RemoteShell {
  readonly host: string
  private readonly argv: string[]

  constructor(
    private readonly target: SshTarget,
    /** Where shared connections keep their sockets; `null` for none. */
    socketDir: string | null,
  ) {
    this.host = target.host
    let controlPath: string | null = null
    if (socketDir && join(socketDir, 'x'.repeat(40)).length <= MAX_SOCKET_PATH) {
      mkdirSync(socketDir, { recursive: true, mode: 0o700 })
      controlPath = join(socketDir, '%C')
    }
    this.argv = [
      'ssh',
      ...sshOptions(controlPath),
      ...(target.port ? ['-p', String(target.port)] : []),
      '--',
      target.host,
    ]
  }

  run(script: string, args: string[], options: ShellRun = {}): Promise<ExecResult> {
    return spawnScript([...this.argv, remoteCommand(script, args)], options)
  }

  async dispose(): Promise<void> {
    // The shared connection ends by itself after it has been idle.
  }
}

/** This machine, through the same scripts: for tests, and to compare behaviour. */
export class LocalShell implements RemoteShell {
  readonly host = 'localhost'

  run(script: string, args: string[], options: ShellRun = {}): Promise<ExecResult> {
    return spawnScript(['bash', '-c', script, 'ariadne', ...args], options)
  }

  async dispose(): Promise<void> {}
}
