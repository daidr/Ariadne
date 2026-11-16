import { createHash, createHmac } from 'node:crypto'
import { sameToken } from './http'

const cookieName = (origin: string) =>
  'ariadne_files_' + createHash('sha256').update(origin).digest('hex').slice(0, 12)
export const FILE_SESSION_MS = 24 * 60 * 60_000
const HOUR_MS = 60 * 60_000
function signature(token: string, origin: string, until: number): string {
  return createHmac('sha256', token)
    .update(`ariadne:file-read:${origin}:${until}`)
    .digest('base64url')
}

/** A file-only capability, issued after bearer authentication. The master token stays out of image URLs. */
export function fileSessionCookie(url: URL, token: string, now = Date.now()): string {
  // Stable within an hour; ordinary authenticated API activity renews the session.
  const until = Math.floor(now / HOUR_MS) * HOUR_MS + FILE_SESSION_MS
  const value = `${until}.${signature(token, url.origin, until)}`
  return `${cookieName(url.origin)}=${value}; Path=/api/files/; HttpOnly; SameSite=Strict; Max-Age=${Math.floor((until - now) / 1000)}${url.protocol === 'https:' ? '; Secure' : ''}`
}

export function fileSessionValid(
  request: Request,
  url: URL,
  token: string,
  now = Date.now(),
): boolean {
  const site = request.headers.get('sec-fetch-site')
  if (site && site !== 'same-origin' && site !== 'none') return false
  const cookies = (request.headers.get('cookie') ?? '').split(';')
  const name = cookieName(url.origin)
  const values = cookies
    .map((cookie) => cookie.trim())
    .filter((cookie) => cookie.startsWith(`${name}=`))
  if (values.length !== 1) return false
  const match = /^([0-9]{1,16})\.([A-Za-z0-9_-]{43})$/.exec(values[0]!.slice(name.length + 1))
  if (!match) return false
  const until = Number(match[1])
  return (
    until > now &&
    until <= now + FILE_SESSION_MS &&
    sameToken(match[2]!, signature(token, url.origin, until))
  )
}
