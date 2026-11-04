import type { ExecResult } from '@ariadne/plugin-sdk'
import { threadEnv } from '@ariadne/plugin-local'

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
 * Runs a bash script with arguments on some machine: over SSH, through a daemon, or on
 * this one in tests.
 * The script gets its arguments as `$1`, `$2`, ... and nothing else from the caller.
 */
export interface RemoteShell {
  /** How the machine is reached, for messages: `ssh` or `daemon`. */
  readonly via: string
  /** Names the machine for labels, e.g. `dev@box`. */
  readonly host: string
  run(script: string, args: string[], options?: ShellRun): Promise<ExecResult>
  dispose(): Promise<void>
}

/** Commands may run this long unless the caller asks for less. */
export const DEFAULT_TIMEOUT_MS = 120_000
/** Output beyond this is cut, keeping the end where errors usually are. */
const MAX_OUTPUT = 64_000
/**
 * The exit code of a call that never reached the machine or lost it on the way: what
 * `ssh` gives, and what the daemon connection gives too.
 */
export const CONNECTION_FAILED = 255

export function clip(text: string): string {
  if (text.length <= MAX_OUTPUT) return text
  return `[… ${text.length - MAX_OUTPUT} characters cut …]\n${text.slice(-MAX_OUTPUT)}`
}

/** Run `argv` with the script's input, collecting its output; ends it on timeout or abort. */
export async function spawnScript(
  argv: string[],
  options: ShellRun & { payload: string; env?: Record<string, string> },
): Promise<ExecResult> {
  if (options.signal?.aborted) return { exitCode: 130, stdout: '', stderr: 'cancelled' }
  const proc = Bun.spawn(argv, {
    stdin: 'pipe',
    stdout: 'pipe',
    stderr: 'pipe',
    env: { ...threadEnv(), ...options.env },
  })
  proc.stdin.write(options.payload)
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
 * The remote command line, read by the user's login shell. Login shells quote differently
 * (fish keeps backslash escapes inside single quotes, csh refuses newlines in them), so
 * the line has nothing to quote: bash reads the script and its arguments from the input
 * instead, see `payload`. That bash reads no startup files: the login shell already read
 * its own, and the environment they set up (say, `PATH`) carries over.
 */
export const REMOTE_COMMAND = 'exec bash --norc --noprofile -s'

/**
 * What `bash -s` reads first: the script as a function, then one line that calls it with
 * the arguments (quoted for bash, which is the only shell that reads them) and exits. bash
 * reads its input from a pipe or socket one byte at a time and parses a whole line before
 * running it, so whatever follows that line stays unread for the script: a file's content
 * to write, or the input the command's watcher holds.
 */
export function payload(script: string, args: string[]): string {
  const call = ['ariadne_main', ...args.map(shellQuote)].join(' ')
  return `ariadne_main() {\n${script}\n}\n${call}; exit $?\n`
}

/**
 * This machine, through the same scripts and the same command line, handed to `shell` the
 * way sshd hands it to the login shell: for tests, and to compare login shells.
 */
export class LocalShell implements RemoteShell {
  readonly via = 'local'
  readonly host = 'localhost'

  constructor(private readonly shell = 'sh') {}

  run(script: string, args: string[], options: ShellRun = {}): Promise<ExecResult> {
    return spawnScript([this.shell, '-c', REMOTE_COMMAND], {
      ...options,
      payload: payload(script, args),
    })
  }

  async dispose(): Promise<void> {}
}
