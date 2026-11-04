/**
 * How Ariadne and the daemon talk. Ariadne connects; the daemon only answers. Every request
 * carries `Authorization: Bearer <token>`, over TLS with the daemon's own certificate, which
 * Ariadne pins by its fingerprint.
 *
 * - `GET /v1/hello` answers `{ protocol }`, so a wrong token, a blocked address or a daemon
 *   of another version is told apart before the shell connection opens.
 * - `GET /v1/shell` upgrades to a WebSocket that carries any number of runs at once. Each
 *   run is `bash --norc --noprofile -s` on the daemon's machine, fed what Ariadne sends.
 *
 * Text frames are JSON: `run`, `end` (its input is done) and `kill` from Ariadne; `exit`
 * and `fail` (it could not start) from the daemon. Binary frames are a run's bytes: four
 * bytes of run id, then for Ariadne's frames the input, for the daemon's one byte of
 * stream (1 output, 2 error output) and the output.
 */

/** Changes whenever the two sides would no longer understand each other. */
export const PROTOCOL = 1

export const DEFAULT_PORT = 7429

/** The name the daemon's certificate is issued to, and the name Ariadne checks it for. */
export const CERT_NAME = 'ariadne-daemon'

/** The command line every run starts, the same one SSH sources hand the login shell. */
export const RUN_ARGV = ['bash', '--norc', '--noprofile', '-s']

export type ClientMessage =
  { op: 'run'; id: number } | { op: 'end'; id: number } | { op: 'kill'; id: number }

export type DaemonMessage =
  { op: 'exit'; id: number; code: number } | { op: 'fail'; id: number; message: string }

export const STDOUT = 1
export const STDERR = 2

/** A binary frame: the run id, an optional stream byte, then the bytes. */
export function frame(id: number, data: Uint8Array, stream?: number): Uint8Array<ArrayBuffer> {
  const head = stream === undefined ? 4 : 5
  const out = new Uint8Array(new ArrayBuffer(head + data.length))
  new DataView(out.buffer).setUint32(0, id)
  if (stream !== undefined) out[4] = stream
  out.set(data, head)
  return out
}

export function readFrame(
  bytes: Uint8Array,
  withStream: boolean,
): { id: number; stream: number; data: Uint8Array } | null {
  const head = withStream ? 5 : 4
  if (bytes.length < head) return null
  const id = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength).getUint32(0)
  return { id, stream: withStream ? bytes[4]! : 0, data: bytes.subarray(head) }
}

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
