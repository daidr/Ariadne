import type { CommandSandbox } from '@ariadne/plugin-sdk'
import { threadEnv } from '@ariadne/plugin-local'
import { timingSafeEqual } from 'node:crypto'
import { chmodSync, existsSync, mkdirSync, readFileSync, renameSync, writeFileSync } from 'node:fs'
import { homedir } from 'node:os'
import { join } from 'node:path'
import {
  CERT_NAME,
  derOf,
  fingerprintOf,
  frame,
  PROTOCOL,
  readFrame,
  RUN_ARGV,
  STDERR,
  STDOUT,
  type ClientMessage,
  type DaemonMessage,
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

interface Run {
  proc: Bun.Subprocess<'pipe', 'pipe', 'pipe'>
  ended: boolean
}

interface Connection {
  runs: Map<number, Run>
}

/** Close a run's input: thread commands end their process group on that. */
function end(run: Run) {
  if (run.ended) return
  run.ended = true
  void Promise.resolve(run.proc.stdin.end()).catch(() => {})
}

export interface DaemonOptions {
  home: string
  hostname: string
  port: number
  certificate: Certificate
  sandbox: CommandSandbox
  /** Paths commands may read but not change, such as the daemon's own code. */
  readOnly?: string[]
}

/**
 * Serve runs to Ariadne. Each run is bash reading a script from its input, in the sandbox,
 * with the daemon's environment minus Ariadne's own variables. A run whose connection
 * closes gets its input closed (thread commands end their process group on that) and is
 * killed a moment later.
 */
export function serveDaemon(options: DaemonOptions) {
  const lockout = new Lockout()
  const authorized = (request: Request, address: string): Response | null => {
    if (lockout.blocked(address)) return new Response('too many failed attempts', { status: 429 })
    const header = request.headers.get('authorization') ?? ''
    const token = header.startsWith('Bearer ') ? header.slice(7).trim() : ''
    if (token && tokenMatches(options.home, token)) return null
    lockout.fail(address)
    return new Response('unauthorized', { status: 401 })
  }
  const send = (ws: Bun.ServerWebSocket<Connection>, message: DaemonMessage) => {
    ws.send(JSON.stringify(message))
  }

  async function pump(
    ws: Bun.ServerWebSocket<Connection>,
    id: number,
    stream: ReadableStream<Uint8Array>,
    kind: number,
  ) {
    for await (const chunk of stream) {
      if (ws.readyState !== 1) return
      ws.send(frame(id, chunk, kind))
    }
  }

  function start(ws: Bun.ServerWebSocket<Connection>, id: number) {
    if (ws.data.runs.has(id)) return
    let proc: Run['proc']
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
      send(ws, { op: 'fail', id, message: error instanceof Error ? error.message : String(error) })
      return
    }
    const run: Run = { proc, ended: false }
    ws.data.runs.set(id, run)
    void (async () => {
      const [code] = await Promise.all([
        proc.exited,
        pump(ws, id, proc.stdout, STDOUT),
        pump(ws, id, proc.stderr, STDERR),
      ])
      ws.data.runs.delete(id)
      if (ws.readyState === 1) send(ws, { op: 'exit', id, code })
    })()
  }

  return Bun.serve<Connection>({
    hostname: options.hostname,
    port: options.port,
    tls: { cert: options.certificate.cert, key: options.certificate.key },
    fetch(request, server) {
      const address = server.requestIP(request)?.address ?? 'unknown'
      const { pathname } = new URL(request.url)
      if (pathname !== '/v1/hello' && pathname !== '/v1/shell') {
        return new Response('not found', { status: 404 })
      }
      const refused = authorized(request, address)
      if (refused) return refused
      if (pathname === '/v1/hello') return Response.json({ protocol: PROTOCOL })
      if (server.upgrade(request, { data: { runs: new Map() } })) return undefined
      return new Response('expected a WebSocket', { status: 400 })
    },
    websocket: {
      // Whole files go through as one frame.
      maxPayloadLength: 256 * 1024 * 1024,
      message(ws, message) {
        if (typeof message !== 'string') {
          const parsed = readFrame(message, false)
          const run = parsed && ws.data.runs.get(parsed.id)
          if (run && !run.ended) {
            run.proc.stdin.write(parsed.data)
            void Promise.resolve(run.proc.stdin.flush()).catch(() => {})
          }
          return
        }
        let request: ClientMessage
        try {
          request = JSON.parse(message) as ClientMessage
        } catch {
          return
        }
        if (!Number.isInteger(request.id)) return
        if (request.op === 'run') start(ws, request.id)
        const run = ws.data.runs.get(request.id)
        if (!run) return
        if (request.op === 'end') end(run)
        if (request.op === 'kill') run.proc.kill('SIGKILL')
      },
      close(ws) {
        for (const run of ws.data.runs.values()) {
          end(run)
          setTimeout(() => run.proc.kill('SIGKILL'), 2000).unref?.()
        }
      },
    },
  })
}
