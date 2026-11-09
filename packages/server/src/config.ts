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
  /** Open the page in the user's browser once the server runs. */
  open: boolean
  /** Nothing was here before: no page can be open on it yet. */
  firstRun: boolean
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
  // By the database and not the token, which a given `ARIADNE_TOKEN` never writes.
  const firstRun = !existsSync(join(home, 'ariadne.sqlite'))
  mkdirSync(home, { recursive: true, mode: 0o700 })
  // A path, not a URL pathname: those are percent-encoded and break on non-ASCII directories.
  const webDist = join(import.meta.dir, '../../web/dist')
  return {
    home,
    databasePath: join(home, 'ariadne.sqlite'),
    // Local only for now; remote access comes later and needs its own design.
    hostname: '127.0.0.1',
    port: Number(env.ARIADNE_PORT ?? 7428),
    token: env.ARIADNE_TOKEN ?? loadToken(home),
    webDist: existsSync(webDist) ? webDist : null,
    // `bun run start` asks for it; the server on its own (development, tests) does not.
    open: env.ARIADNE_OPEN === '1',
    firstRun,
  }
}
