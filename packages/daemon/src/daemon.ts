import type { CommandSandbox } from '@ariadne/plugin-sdk'
import { threadEnv } from '@ariadne/plugin-local'
import { timingSafeEqual } from 'node:crypto'
import { chmodSync, existsSync, mkdirSync, readFileSync, renameSync, writeFileSync } from 'node:fs'
import { homedir } from 'node:os'
import { join } from 'node:path'
import { MAX_FRAME, RelayServer, ServerSocketLink, StreamLink, type Channel } from '@ariadne/relay'
import {
  CERT_NAME,
  derOf,
  EXEC,
  fingerprintOf,
  PROTOCOL,
  RUN_ARGV,
  STDERR,
  STDIN,
  STDOUT,
  type ExecControl,
} from './protocol'

/**
 * The daemon's own directory: the token's hash and the certificate with its key. Thread
 * commands never see it, see `hidden`.
 */
export function daemonHome(env: Record<string, string | undefined> = process.env): string {
  const home = env.ARIADNE_DAEMON_HOME ?? join(homedir(), '.ariadne-daemon')
  mkdirSync(home, { recursive: true, mode: 0o700 })
  chmodSync(home, 0o700)
  return home
}

const hashOf = (token: string) => new Bun.CryptoHasher('sha256').update(token).digest()

/**
 * Make a new token and keep only its hash, replacing the one before: the old token stops
 * working at once, also for a daemon that is running. The token itself is returned once,
 * to be shown to the user, and is never written anywhere.
 */
export function newToken(home: string): string {
  const token = Buffer.from(crypto.getRandomValues(new Uint8Array(32))).toString('base64url')
  const path = join(home, 'token.sha256')
  const next = `${path}.new`
  writeFileSync(next, `${hashOf(token).toString('hex')}\n`, { mode: 0o600 })
  renameSync(next, path)
  return token
}

export function hasToken(home: string): boolean {
  return existsSync(join(home, 'token.sha256'))
}

/** Whether `token` is the current one; read again each time, so a new pairing takes hold. */
export function tokenMatches(home: string, token: string): boolean {
  let stored: Buffer
  try {
    stored = Buffer.from(readFileSync(join(home, 'token.sha256'), 'utf8').trim(), 'hex')
  } catch {
    return false
  }
  const given = hashOf(token)
  return stored.length === given.length && timingSafeEqual(stored, given)
}

export interface Certificate {
  cert: string
  key: string
  fingerprint: string
}

/** There is no `openssl` to make the certificate with. */
export class MissingOpensslError extends Error {
  constructor() {
    super('openssl is needed to make the certificate')
    this.name = 'MissingOpensslError'
  }
}

/**
 * The daemon's certificate, made with `openssl` the first time: a self-signed P-256 one
 * for `ariadne-daemon`. Ariadne trusts it by its fingerprint, never through a CA, so the
 * name and the expiry do not matter.
 */
export async function certificate(home: string): Promise<Certificate> {
  const certPath = join(home, 'cert.pem')
  const keyPath = join(home, 'key.pem')
  if (!existsSync(certPath) || !existsSync(keyPath)) {
    if (!Bun.which('openssl')) throw new MissingOpensslError()
    const proc = Bun.spawn(
      [
        'openssl',
        'req',
        '-x509',
        '-newkey',
        'ec',
        '-pkeyopt',
        'ec_paramgen_curve:P-256',
        '-nodes',
        '-keyout',
        `${keyPath}.new`,
        '-out',
        `${certPath}.new`,
        '-days',
        '36500',
        '-subj',
        `/CN=${CERT_NAME}`,
        '-addext',
        `subjectAltName=DNS:${CERT_NAME}`,
      ],
      { stdout: 'ignore', stderr: 'pipe', stdin: 'ignore', windowsHide: true },
    )
    const [code, stderr] = await Promise.all([proc.exited, new Response(proc.stderr).text()])
    if (code !== 0) throw new Error(`openssl failed: ${stderr.trim() || `exit ${code}`}`)
    chmodSync(`${keyPath}.new`, 0o600)
    renameSync(`${keyPath}.new`, keyPath)
    renameSync(`${certPath}.new`, certPath)
  }
  const cert = readFileSync(certPath, 'utf8')
  return { cert, key: readFileSync(keyPath, 'utf8'), fingerprint: fingerprintOf(derOf(cert)) }
}

/**
 * Failed tokens per address: after `limit` within the window, the address is refused
 * until the window has passed since its first failure.
 */
export class Lockout {
  private readonly failures = new Map<string, { count: number; since: number }>()

  constructor(
    private readonly limit = 10,
    private readonly windowMs = 10 * 60_000,
    private readonly now = () => Date.now(),
  ) {}

  blocked(address: string): boolean {
    const entry = this.failures.get(address)
    if (!entry) return false
    if (this.now() - entry.since > this.windowMs) {
      this.failures.delete(address)
      return false
    }
    return entry.count >= this.limit
  }

  fail(address: string): void {
    const entry = this.failures.get(address)
    if (!entry || this.now() - entry.since > this.windowMs) {
      this.failures.set(address, { count: 1, since: this.now() })
    } else entry.count++
  }
}

export interface ExecOptions {
  sandbox: CommandSandbox
  /** Paths commands may read but not change, such as the daemon's own code. */
  readOnly?: string[]
}

/**
 * Runs each `exec` channel: bash reading a script from its input, in the sandbox, with the
 * daemon's environment minus Ariadne's own variables. When the channel ends without bash
 * having exited (Ariadne closed it, or its session was lost), bash's input is closed, which
 * ends thread commands' process groups, and bash is killed a moment later.
 */
export function execChannels(options: ExecOptions): (channel: Channel) => void {
  return (channel) => {
    if (channel.kind !== EXEC) {
      channel.close({ error: `the daemon has no ${channel.kind} channels` })
      return
    }
    let proc: Bun.Subprocess<'pipe', 'pipe', 'pipe'>
    try {
      proc = Bun.spawn(
        options.sandbox.wrap(RUN_ARGV, { keep: [], readOnly: options.readOnly ?? [] }),
        {
          cwd: homedir(),
          env: threadEnv(),
          stdin: 'pipe',
          stdout: 'pipe',
          stderr: 'pipe',
          windowsHide: true,
        },
      )
    } catch (error) {
      channel.close({ error: error instanceof Error ? error.message : String(error) })
      return
    }
    let inputEnded = false
    let exited = false
    const endInput = () => {
      if (inputEnded) return
      inputEnded = true
      void Promise.resolve(proc.stdin.end()).catch(() => {})
    }
    channel.onData = (stream, bytes) => {
      if (stream !== STDIN || inputEnded) return
      proc.stdin.write(bytes)
      void Promise.resolve(proc.stdin.flush()).catch(() => {})
    }
    channel.onControl = (message) => {
      const op = (message as ExecControl | null)?.op
      if (op === 'end') endInput()
      if (op === 'kill') proc.kill('SIGKILL')
    }
    channel.onClose = () => {
      endInput()
      setTimeout(() => {
        if (!exited) proc.kill('SIGKILL')
      }, 2000).unref?.()
    }
    const pump = async (stream: ReadableStream<Uint8Array>, kind: number) => {
      for await (const chunk of stream) {
        // Waits for Ariadne to take it, so a flood of output slows bash instead of piling up.
        if (!(await channel.write(chunk, kind))) return
      }
    }
    void (async () => {
      const [code] = await Promise.all([
        proc.exited,
        pump(proc.stdout, STDOUT),
        pump(proc.stderr, STDERR),
      ])
      exited = true
      channel.close({ code })
    })()
  }
}

export interface DaemonOptions extends ExecOptions {
  home: string
  hostname: string
  port: number
  certificate: Certificate
}

/** Serve relay sessions to Ariadne over TLS, each let in by the token. */
export function serveDaemon(options: DaemonOptions) {
  const lockout = new Lockout()
  const relay = new RelayServer({ onChannel: execChannels(options) })
  const authorized = (request: Request, address: string): Response | null => {
    if (lockout.blocked(address)) return new Response('too many failed attempts', { status: 429 })
    const header = request.headers.get('authorization') ?? ''
    const token = header.startsWith('Bearer ') ? header.slice(7).trim() : ''
    if (token && tokenMatches(options.home, token)) return null
    lockout.fail(address)
    return new Response('unauthorized', { status: 401 })
  }

  const server = Bun.serve<{ link: ServerSocketLink | null }>({
    hostname: options.hostname,
    port: options.port,
    tls: { cert: options.certificate.cert, key: options.certificate.key },
    fetch(request, bun) {
      const address = bun.requestIP(request)?.address ?? 'unknown'
      const { pathname } = new URL(request.url)
      if (pathname !== '/v1/hello' && pathname !== '/v2/relay') {
        return new Response('not found', { status: 404 })
      }
      const refused = authorized(request, address)
      if (refused) return refused
      if (pathname === '/v1/hello') return Response.json({ protocol: PROTOCOL })
      if (bun.upgrade(request, { data: { link: null } })) return undefined
      return new Response('expected a WebSocket', { status: 400 })
    },
    websocket: {
      // Frames stay below the relay's limit; this leaves room for its header.
      maxPayloadLength: MAX_FRAME + 1024,
      open(ws) {
        const link = new ServerSocketLink(ws as Bun.ServerWebSocket<unknown>)
        ws.data.link = link
        void relay.accept(link)
      },
      message(ws, message) {
        ws.data.link?.received(message)
      },
      close(ws, _code, reason) {
        ws.data.link?.ended(reason || undefined)
      },
    },
  })
  return Object.assign(server, {
    /** Stop listening and end every session's commands. */
    async shutdown() {
      relay.close()
      await server.stop(true)
    },
  })
}

/**
 * Serve one relay session on this process's standard input and output, for Ariadne
 * connecting through ssh. Resolves when the input ends; the session's commands are ended.
 */
export function serveStdio(options: ExecOptions): Promise<void> {
  return new Promise((resolve) => {
    // One client, and no coming back: a new ssh connection starts a new daemon.
    const relay = new RelayServer({
      onChannel: execChannels(options),
      keepMs: 0,
      onSessionEnd: () => resolve(),
    })
    const writer = Bun.stdout.writer()
    const link = new StreamLink(Bun.stdin.stream(), {
      write: (bytes) => writer.write(bytes),
      flush: () => writer.flush(),
      end: () => writer.end(),
    })
    void relay.accept(link)
  })
}
