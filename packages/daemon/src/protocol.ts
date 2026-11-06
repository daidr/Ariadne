/**
 * How Ariadne and the daemon talk. Ariadne connects; the daemon only answers.
 *
 * Over the network every request carries `Authorization: Bearer <token>`, over TLS with the
 * daemon's own certificate, which Ariadne pins by its fingerprint:
 *
 * - `GET /v1/hello` answers `{ protocol }`, so a wrong token, a blocked address or a daemon
 *   of another version is told apart before the relay connection opens. Its path stays the
 *   same across versions so that any two versions can tell each other apart.
 * - `GET /v2/relay` upgrades to a WebSocket that carries a relay session.
 *
 * Through SSH, `ariadne-daemon --stdio` speaks the same relay session on its standard input
 * and output; ssh has already decided who may connect.
 *
 * Each command is an `exec` channel: `bash --norc --noprofile -s` on the daemon's machine.
 * Ariadne sends its input as data on stream 0 and `{ op: 'end' }` or `{ op: 'kill' }` as
 * control messages; the daemon sends output on streams 1 and 2 and closes the channel with
 * `{ code }` when bash exits, or `{ error }` when it could not start.
 */

/** Changes whenever the two sides would no longer understand each other. */
export const PROTOCOL = 2

export const DEFAULT_PORT = 7429

/** The name the daemon's certificate is issued to, and the name Ariadne checks it for. */
export const CERT_NAME = 'ariadne-daemon'

/** The command line every run starts, the same one SSH sources hand the login shell. */
export const RUN_ARGV = ['bash', '--norc', '--noprofile', '-s']

export const EXEC = 'exec'
export const STDIN = 0
export const STDOUT = 1
export const STDERR = 2

export type ExecControl = { op: 'end' } | { op: 'kill' }

/**
 * What the user pastes into Ariadne: the token, then the certificate's fingerprint, joined
 * by a dot. Both are base64url, which has no dot.
 */
export function pairingCode(token: string, fingerprint: string): string {
  return `${token}.${fingerprint}`
}

export function parsePairingCode(code: string): { token: string; fingerprint: string } | null {
  const match = /^([A-Za-z0-9_-]{43})\.([A-Za-z0-9_-]{43})$/.exec(code.trim())
  return match ? { token: match[1]!, fingerprint: match[2]! } : null
}

/** A certificate's fingerprint: SHA-256 of its DER bytes, base64url. */
export function fingerprintOf(der: Uint8Array): string {
  return new Bun.CryptoHasher('sha256').update(der).digest('base64url')
}

/** The DER bytes of the first certificate in a PEM file. */
export function derOf(pem: string): Uint8Array {
  const body = /-----BEGIN CERTIFICATE-----([\s\S]+?)-----END CERTIFICATE-----/.exec(pem)?.[1]
  if (!body) throw new Error('no certificate in the file')
  return new Uint8Array(Buffer.from(body.replace(/\s+/g, ''), 'base64'))
}

export function pemOf(der: Uint8Array): string {
  const lines =
    Buffer.from(der)
      .toString('base64')
      .match(/.{1,64}/g) ?? []
  return `-----BEGIN CERTIFICATE-----\n${lines.join('\n')}\n-----END CERTIFICATE-----\n`
}

/** `host:port` or `[v6]:port`, the port defaulting; `null` when it is not an address. */
export function parseAddress(address: string): { host: string; port: number } | null {
  const text = address.trim()
  const match = /^(?:\[([0-9a-fA-F:.]+)\]|([A-Za-z0-9.-]+))(?::(\d{1,5}))?$/.exec(text)
  if (!match) return null
  const host = match[1] ?? match[2]!
  const port = match[3] ? Number(match[3]) : DEFAULT_PORT
  if (!(port > 0 && port < 65536) || host.startsWith('-') || host.startsWith('.')) return null
  return { host, port }
}

export function isLoopback(host: string): boolean {
  return host === 'localhost' || host === '::1' || /^127\.\d{1,3}\.\d{1,3}\.\d{1,3}$/.test(host)
}
