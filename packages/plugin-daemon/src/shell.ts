import {
  CERT_NAME,
  fingerprintOf,
  frame,
  pemOf,
  PROTOCOL,
  readFrame,
  STDOUT,
  type ClientMessage,
  type DaemonMessage,
} from '@ariadne/daemon/protocol'
import type { ExecResult } from '@ariadne/plugin-sdk'
import {
  clip,
  CONNECTION_FAILED,
  DEFAULT_TIMEOUT_MS,
  payload,
  type RemoteShell,
  type ShellRun,
} from '@ariadne/remote'
import { connect as tlsConnect } from 'node:tls'

export interface DaemonTarget {
  host: string
  port: number
  token: string
  /** The certificate's fingerprint, from the pairing code. */
  fingerprint: string
}

/** Something wrong with reaching the daemon, worded for the user. */
export class DaemonError extends Error {
  override name = 'DaemonError'
}

/**
 * The daemon's certificate, as long as its fingerprint is the one the pairing code gave.
 * It is fetched on each connection and then trusted as the only CA, so the connection
 * that carries the token reaches that certificate's key and nothing else.
 */
export function pinnedCertificate(target: DaemonTarget, timeoutMs = 15_000): Promise<string> {
  return new Promise((resolve, reject) => {
    const socket = tlsConnect({
      host: target.host,
      port: target.port,
      servername: CERT_NAME,
      // Checked below, by fingerprint; nothing is sent on this connection.
      rejectUnauthorized: false,
    })
    const fail = (error: Error) => {
      socket.destroy()
      reject(error)
    }
    socket.setTimeout(timeoutMs, () => fail(new DaemonError('connecting to the daemon timed out')))
    socket.once('error', (error) =>
      fail(new DaemonError(`cannot reach the daemon: ${error.message}`)),
    )
    socket.once('secureConnect', () => {
      const raw = socket.getPeerCertificate().raw
      socket.end()
      if (!raw) return reject(new DaemonError('the daemon sent no certificate'))
      const der = new Uint8Array(raw)
      if (fingerprintOf(der) !== target.fingerprint) {
        return reject(
          new DaemonError(
            "the daemon's certificate does not match the pairing code: another machine answered, or the daemon made a new certificate (run ariadne-daemon pair there)",
          ),
        )
      }
      resolve(pemOf(der))
    })
  })
}

interface Pending {
  stdout: Uint8Array[]
  stderr: Uint8Array[]
  /** Hears each stream's output as it comes; its own decoders keep split characters whole. */
  onOutput?: (data: Uint8Array, stream: 'stdout' | 'stderr') => void
  done: (result: { exitCode: number; failure?: string }) => void
}

interface Link {
  ws: WebSocket
  runs: Map<number, Pending>
}

const encoder = new TextEncoder()
const decoder = () => new TextDecoder()

function joined(chunks: Uint8Array[]): string {
  const text = decoder()
  return chunks.map((chunk) => text.decode(chunk, { stream: true })).join('') + text.decode()
}

/**
 * A machine reached through its daemon. One WebSocket carries every call and is opened
 * again when it closed. A call that cannot reach the daemon, or loses it, ends with exit
 * code 255 and the reason, like ssh.
 */
export class DaemonShell implements RemoteShell {
  readonly via = 'daemon'
  readonly host: string
  private link: Promise<Link> | null = null
  /** The connection `link` resolved to, to tell whether a closed one is still the one in use. */
  private current: Link | null = null
  private nextId = 1
  private idle: ReturnType<typeof setTimeout> | null = null

  constructor(
    private readonly target: DaemonTarget,
    /**
     * The connection closes once no call has used it for this long, like a shared SSH
     * connection: a source replaced or only checked does not keep one open.
     */
    private readonly idleMs = 120_000,
  ) {
    this.host = target.host
  }

  /** Whether a connection is open now. */
  get connected(): boolean {
    return this.current !== null
  }

  private get base(): string {
    const host = this.target.host.includes(':') ? `[${this.target.host}]` : this.target.host
    return `${host}:${this.target.port}`
  }

  /** Check the token and the version, then open the shell connection. */
  private async open(): Promise<Link> {
    const ca = await pinnedCertificate(this.target)
    const tls = { ca, servername: CERT_NAME }
    const headers = { authorization: `Bearer ${this.target.token}` }
    let hello: Response
    try {
      hello = await fetch(`https://${this.base}/v1/hello`, {
        headers,
        tls,
        signal: AbortSignal.timeout(15_000),
      } as RequestInit)
    } catch (error) {
      throw new DaemonError(
        `cannot reach the daemon: ${error instanceof Error ? error.message : String(error)}`,
      )
    }
    if (hello.status === 401) throw new DaemonError('the daemon refused the pairing code')
    if (hello.status === 429) {
      throw new DaemonError('the daemon refuses this address for now: too many wrong pairing codes')
    }
    if (!hello.ok) throw new DaemonError(`the daemon answered ${hello.status}`)
    const { protocol } = (await hello.json()) as { protocol?: number }
    if (protocol !== PROTOCOL) {
      throw new DaemonError(
        `the daemon speaks protocol ${protocol}, Ariadne ${PROTOCOL}: run the same version on both sides`,
      )
    }
    const ws = new WebSocket(`wss://${this.base}/v1/shell`, { headers, tls } as never)
    ws.binaryType = 'arraybuffer'
    const link: Link = { ws, runs: new Map() }
    await new Promise<void>((resolve, reject) => {
      ws.addEventListener('open', () => resolve(), { once: true })
      ws.addEventListener(
        'error',
        () => reject(new DaemonError('the daemon closed the shell connection')),
        { once: true },
      )
    })
    ws.addEventListener('message', (event) => {
      if (typeof event.data === 'string') {
        const message = JSON.parse(event.data) as DaemonMessage
        const run = link.runs.get(message.id)
        if (!run) return
        link.runs.delete(message.id)
        run.done(
          message.op === 'exit'
            ? { exitCode: message.code }
            : { exitCode: CONNECTION_FAILED, failure: message.message },
        )
        return
      }
      const parsed = readFrame(new Uint8Array(event.data as ArrayBuffer), true)
      const run = parsed && link.runs.get(parsed.id)
      if (!run) return
      ;(parsed.stream === STDOUT ? run.stdout : run.stderr).push(parsed.data.slice())
      run.onOutput?.(parsed.data, parsed.stream === STDOUT ? 'stdout' : 'stderr')
    })
    ws.addEventListener('close', () => {
      if (this.current === link) {
        this.link = null
        this.current = null
      }
      for (const run of link.runs.values()) {
        run.done({ exitCode: CONNECTION_FAILED, failure: 'the connection to the daemon was lost' })
      }
      link.runs.clear()
    })
    return link
  }

  private connection(): Promise<Link> {
    this.link ??= this.open().then(
      (link) => (this.current = link),
      (error: unknown) => {
        this.link = null
        throw error
      },
    )
    return this.link
  }

  async run(script: string, args: string[], options: ShellRun = {}): Promise<ExecResult> {
    if (options.signal?.aborted) return { exitCode: 130, stdout: '', stderr: 'cancelled' }
    let link: Link
    try {
      link = await this.connection()
    } catch (error) {
      const reason = error instanceof Error ? error.message : String(error)
      return { exitCode: CONNECTION_FAILED, stdout: '', stderr: `daemon: ${reason}` }
    }
    if (this.idle) clearTimeout(this.idle)
    this.idle = null
    const id = this.nextId++
    const send = (message: ClientMessage) => {
      if (link.ws.readyState === WebSocket.OPEN) link.ws.send(JSON.stringify(message))
    }
    const finished = new Promise<{ exitCode: number; failure?: string; run: Pending }>(
      (resolve) => {
        const run: Pending = {
          stdout: [],
          stderr: [],
          done: (result) => resolve({ ...result, run }),
        }
        const listen = options.onOutput
        if (listen) {
          const decoders = { stdout: decoder(), stderr: decoder() }
          run.onOutput = (data, stream) => {
            const text = decoders[stream].decode(data, { stream: true })
            if (text) listen(text, stream)
          }
        }
        link.runs.set(id, run)
      },
    )
    send({ op: 'run', id })
    const input = payload(script, args) + (options.input ?? '')
    link.ws.send(frame(id, encoder.encode(input)))
    if (!options.hold) send({ op: 'end', id })
    let ended = false
    const end = () => {
      if (ended) return
      ended = true
      // Closing the input ends a thread command's process group; the kill follows in case
      // something holds on.
      send({ op: 'end', id })
      setTimeout(() => send({ op: 'kill', id }), 2000).unref?.()
    }
    const timer = setTimeout(end, options.timeoutMs ?? DEFAULT_TIMEOUT_MS)
    options.signal?.addEventListener('abort', end, { once: true })
    try {
      const { exitCode, failure, run } = await finished
      const stdout = joined(run.stdout)
      const stderr = joined(run.stderr) + (failure ? `daemon: ${failure}` : '')
      return options.clip
        ? { exitCode, stdout: clip(stdout), stderr: clip(stderr) }
        : { exitCode, stdout, stderr }
    } finally {
      clearTimeout(timer)
      options.signal?.removeEventListener('abort', end)
      if (options.hold) send({ op: 'end', id })
      if (!link.runs.size && link === this.current) {
        if (this.idle) clearTimeout(this.idle)
        this.idle = setTimeout(() => {
          if (link === this.current && !link.runs.size) void this.dispose()
        }, this.idleMs)
        this.idle.unref?.()
      }
    }
  }

  async dispose(): Promise<void> {
    if (this.idle) clearTimeout(this.idle)
    this.idle = null
    const link = this.link
    this.link = null
    this.current = null
    if (link) await link.then((open) => open.ws.close()).catch(() => {})
  }
}
