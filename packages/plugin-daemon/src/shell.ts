import { CERT_NAME, fingerprintOf, pemOf, PROTOCOL } from '@ariadne/daemon/protocol'
import { RelayRefused, WebSocketLink, type Link } from '@ariadne/relay'
import { RelayShell } from '@ariadne/remote'
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
          new RelayRefused(
            "the daemon's certificate does not match the pairing code: another machine answered, or the daemon made a new certificate (run ariadne-daemon pair there)",
          ),
        )
      }
      resolve(pemOf(der))
    })
  })
}

/**
 * A machine reached through its daemon over the network. One relay session over a
 * WebSocket carries every call; a dropped connection is resumed, and a call that cannot
 * reach the daemon, or loses it for good, ends with exit code 255 and the reason, like ssh.
 */
export class DaemonShell extends RelayShell {
  constructor(
    private readonly target: DaemonTarget,
    /**
     * The connection closes once no call has used it for this long, like a shared SSH
     * connection: a source replaced or only checked does not keep one open.
     */
    idleMs = 120_000,
  ) {
    super('daemon', target.host, () => openLink(target), idleMs)
  }
}

function base(target: DaemonTarget): string {
  const host = target.host.includes(':') ? `[${target.host}]` : target.host
  return `${host}:${target.port}`
}

/** Check the certificate, the token and the version, then open the relay connection. */
export async function openLink(target: DaemonTarget): Promise<Link> {
  const ca = await pinnedCertificate(target)
  const tls = { ca, servername: CERT_NAME }
  const headers = { authorization: `Bearer ${target.token}` }
  let hello: Response
  try {
    hello = await fetch(`https://${base(target)}/v1/hello`, {
      headers,
      tls,
      signal: AbortSignal.timeout(15_000),
    } as RequestInit)
  } catch (error) {
    throw new DaemonError(
      `cannot reach the daemon: ${error instanceof Error ? error.message : String(error)}`,
    )
  }
  if (hello.status === 401) throw new RelayRefused('the daemon refused the pairing code')
  if (hello.status === 429) {
    throw new RelayRefused('the daemon refuses this address for now: too many wrong pairing codes')
  }
  if (!hello.ok) throw new DaemonError(`the daemon answered ${hello.status}`)
  const { protocol } = (await hello.json()) as { protocol?: number }
  if (protocol !== PROTOCOL) {
    throw new RelayRefused(
      `the daemon speaks protocol ${protocol}, Ariadne ${PROTOCOL}: run the same version on both sides`,
    )
  }
  const ws = new WebSocket(`wss://${base(target)}/v2/relay`, { headers, tls } as never)
  await new Promise<void>((resolve, reject) => {
    // A daemon that took the connection but never answers would hold every call waiting.
    const timer = setTimeout(() => {
      ws.close()
      reject(new DaemonError('connecting to the daemon timed out'))
    }, 15_000)
    ws.addEventListener(
      'open',
      () => {
        clearTimeout(timer)
        resolve()
      },
      { once: true },
    )
    ws.addEventListener(
      'error',
      () => {
        clearTimeout(timer)
        reject(new DaemonError('the daemon closed the connection'))
      },
      { once: true },
    )
  })
  return new WebSocketLink(ws)
}
