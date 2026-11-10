import type { BytePipe, ExecResult } from '@ariadne/plugin-sdk'
import { threadEnv } from '@ariadne/plugin-local'
import { StreamLink, type Link } from '@ariadne/relay'
import {
  CONNECTION_FAILED,
  payload,
  RelayShell,
  REMOTE_COMMAND,
  spawnScript,
  type RemoteShell,
  type ShellRun,
} from '@ariadne/remote'
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'

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
    'GSSAPIDelegateCredentials=no',
    'Tunnel=no',
    'PermitLocalCommand=no',
    'ConnectTimeout=15',
    'ServerAliveInterval=15',
    'ServerAliveCountMax=4',
  ]
  // One connection carries every call; it stays open for two minutes after the last.
  // Without one of its own, it must not join a connection the user opened in a terminal
  // either: that one keeps whatever it forwards.
  if (controlPath) {
    options.push(`ControlMaster=auto`, `ControlPath=${controlPath}`, 'ControlPersist=120')
  } else {
    options.push('ControlMaster=no', 'ControlPath=none')
  }
  return [...options.flatMap((option) => ['-o', option]), '-T', '-e', 'none']
}

/** Unix sockets have short paths: longer ones get no shared connection. */
const MAX_SOCKET_PATH = 100

/** A daemon path the login shell takes as one word, whatever shell it is. */
const PLAIN_PATH = /^\/[\w./+-]+$/

/**
 * A machine reached with the system's `ssh`, using the user's own config and keys.
 *
 * When the machine has `ariadne-daemon` on its `PATH`, one long-lived `ssh` process runs
 * `ariadne-daemon --stdio` and every call is a channel of that one relay session: no new
 * process or round trip per call. Without it, each call is its own `ssh` process, sharing
 * one connection where the system's ssh can (not on Windows).
 */
export class SshShell implements RemoteShell {
  readonly via = 'ssh'
  readonly host: string
  private readonly argv: string[]
  private relay: Promise<RelayShell | null> | null = null

  constructor(
    private readonly target: SshTarget,
    /** Where shared connections keep their sockets; `null` for none. */
    socketDir: string | null,
    private readonly options: {
      /** The ssh command, for tests. */
      ssh?: string
      /** How long the daemon's connection stays after the last call. */
      idleMs?: number
    } = {},
  ) {
    this.host = target.host
    let controlPath: string | null = null
    // Windows' own OpenSSH has no shared connections.
    if (
      socketDir &&
      process.platform !== 'win32' &&
      join(socketDir, 'x'.repeat(40)).length <= MAX_SOCKET_PATH
    ) {
      mkdirSync(socketDir, { recursive: true, mode: 0o700 })
      controlPath = join(socketDir, '%C')
    }
    this.argv = [
      options.ssh ?? 'ssh',
      ...sshOptions(controlPath),
      ...(target.port ? ['-p', String(target.port)] : []),
      '--',
      target.host,
    ]
  }

  /** Whether calls go through the remote daemon; known after the first call. */
  async throughDaemon(): Promise<boolean> {
    return (await this.daemon()) !== null
  }

  async run(script: string, args: string[], options: ShellRun = {}): Promise<ExecResult> {
    const relay = await this.daemon()
    if (relay) return relay.run(script, args, options)
    return this.plain(script, args, options)
  }

  /**
   * A connection to `port` on the machine's loopback: a channel of the daemon's session, or
   * `ssh -W` over the shared connection (also when the daemon predates `tcp` channels).
   */
  async connect(port: number): Promise<BytePipe> {
    const relay = await this.daemon()
    if (relay && (await relay.tcp())) return relay.connect(port)
    const at = this.argv.indexOf('--')
    // `localhost`, not 127.0.0.1: sshd tries each address it resolves to, so a server
    // listening only on ::1 is reached too.
    const proc = Bun.spawn(
      [...this.argv.slice(0, at), '-W', `localhost:${port}`, ...this.argv.slice(at)],
      { stdin: 'pipe', stdout: 'pipe', stderr: 'pipe', windowsHide: true, env: threadEnv() },
    )
    const stderr = new Response(proc.stderr).text()
    let closed = false
    const pipe: BytePipe = {
      async write(bytes) {
        if (closed) return false
        try {
          proc.stdin.write(bytes)
          await proc.stdin.flush()
          return true
        } catch {
          return false
        }
      },
      onData: () => {},
      onClose: () => {},
      close() {
        if (closed) return
        closed = true
        void Promise.resolve(proc.stdin.end()).catch(() => {})
        setTimeout(() => proc.kill(), 2000).unref?.()
      },
    }
    void (async () => {
      for await (const chunk of proc.stdout) pipe.onData(chunk)
      const code = await proc.exited
      closed = true
      const last = (await stderr).trim().split('\n').at(-1)
      pipe.onClose(code === 0 ? undefined : last || `ssh exited with code ${code}`)
    })()
    return pipe
  }

  async dispose(): Promise<void> {
    // A shared connection ends by itself after it has been idle.
    const relay = await this.relay
    this.relay = null
    await relay?.dispose()
  }

  /** One call as its own ssh process. */
  private plain(script: string, args: string[], options: ShellRun = {}): Promise<ExecResult> {
    return spawnScript([...this.argv, REMOTE_COMMAND], {
      ...options,
      payload: payload(script, args),
    })
  }

  private daemon(): Promise<RelayShell | null> {
    this.relay ??= this.findDaemon()
    return this.relay
  }

  /** The remote daemon, if the machine has one that speaks this version. */
  private async findDaemon(): Promise<RelayShell | null> {
    const found = await this.plain('command -v ariadne-daemon', [], { timeoutMs: 30_000 })
    if (found.exitCode === CONNECTION_FAILED) {
      // Not reached at all: look again on the next call.
      this.relay = null
      return null
    }
    const path = found.stdout.trim()
    if (found.exitCode !== 0 || !PLAIN_PATH.test(path)) return null
    const relay = new RelayShell(
      'ssh',
      this.host,
      () => this.openDaemon(path),
      this.options.idleMs,
      {
        // A new ssh process starts a new daemon, which has no session to resume.
        resumeMs: 1000,
      },
    )
    // An older daemon has no --stdio: calls then go without it.
    const check = await relay.run('true', [], { timeoutMs: 30_000 })
    if (check.exitCode === 0) return relay
    await relay.dispose()
    return null
  }

  private async openDaemon(path: string): Promise<Link> {
    const proc = Bun.spawn([...this.argv, `exec ${path} --stdio`], {
      stdin: 'pipe',
      stdout: 'pipe',
      stderr: 'pipe',
      windowsHide: true,
      env: threadEnv(),
    })
    const stderr = new Response(proc.stderr).text()
    return new StreamLink(
      proc.stdout,
      {
        write: (bytes) => proc.stdin.write(bytes),
        flush: () => proc.stdin.flush(),
        end: () => {
          void Promise.resolve(proc.stdin.end()).catch(() => {})
          // ssh ends with the daemon; this is for one that does not.
          setTimeout(() => proc.kill(), 5000).unref?.()
        },
      },
      async () => {
        const code = await proc.exited
        const last = (await stderr).trim().split('\n').at(-1)
        return last || `ssh exited with code ${code}`
      },
    )
  }
}
