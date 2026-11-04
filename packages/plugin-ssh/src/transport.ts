import type { ExecResult } from '@ariadne/plugin-sdk'
import {
  payload,
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

/** A machine reached with the system's `ssh`, using the user's own config and keys. */
export class SshShell implements RemoteShell {
  readonly via = 'ssh'
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
    return spawnScript([...this.argv, REMOTE_COMMAND], {
      ...options,
      payload: payload(script, args),
    })
  }

  async dispose(): Promise<void> {
    // The shared connection ends by itself after it has been idle.
  }
}
