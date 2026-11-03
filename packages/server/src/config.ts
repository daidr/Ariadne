import { chmodSync, existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { homedir } from 'node:os'
import { join } from 'node:path'

export interface ServerConfig {
  home: string
  databasePath: string
  hostname: string
  port: number
  token: string
  /** Built web assets to serve, when present. */
  webDist: string | null
}

/** Read the token from `<home>/token`, creating it on first run (owner-only permissions). */
export function loadToken(home: string): string {
  const path = join(home, 'token')
  if (existsSync(path)) {
    const token = readFileSync(path, 'utf8').trim()
    if (token) return token
  }
  const bytes = crypto.getRandomValues(new Uint8Array(32))
  const token = Buffer.from(bytes).toString('base64url')
  writeFileSync(path, `${token}\n`, { mode: 0o600 })
  chmodSync(path, 0o600)
  return token
}

export function loadConfig(env: Record<string, string | undefined> = process.env): ServerConfig {
  const home = env.ARIADNE_HOME ?? join(homedir(), '.ariadne')
  mkdirSync(home, { recursive: true, mode: 0o700 })
  const webDist = new URL('../../web/dist', import.meta.url).pathname
  return {
    home,
    databasePath: join(home, 'ariadne.sqlite'),
    // Local only for now; remote access comes later and needs its own design.
    hostname: '127.0.0.1',
    port: Number(env.ARIADNE_PORT ?? 7428),
    token: env.ARIADNE_TOKEN ?? loadToken(home),
    webDist: existsSync(webDist) ? webDist : null,
  }
}
