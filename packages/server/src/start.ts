import { createTranslator } from '@ariadne/i18n'
import { existsSync, readdirSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { loadConfig } from './config'
import { loadUserConfig } from './settings'

/**
 * `bun run start`: the server with the built pages, building them first when they are
 * missing or older than what they are built from.
 */

const root = join(import.meta.dir, '../../..')
const web = join(root, 'packages/web')
const built = join(web, 'dist/index.html')

/** The newest modification time of the files under `path`, skipping `skip` directories. */
export function newest(path: string, skip: ReadonlySet<string> = new Set()): number {
  if (!existsSync(path)) return 0
  const stat = statSync(path)
  if (!stat.isDirectory()) return stat.mtimeMs
  let time = 0
  for (const entry of readdirSync(path, { withFileTypes: true })) {
    if (skip.has(entry.name)) continue
    time = Math.max(time, newest(join(path, entry.name), skip))
  }
  return time
}

/** Whether the pages need building: never built, or a source changed since. */
export function stale(): boolean {
  if (!existsSync(built)) return true
  const sources = Math.max(
    newest(web, new Set(['dist', 'node_modules'])),
    // What the pages import from the other packages, and the dependencies' versions.
    ...['core', 'i18n', 'plugin-sdk'].map((name) => newest(join(root, 'packages', name, 'src'))),
    newest(join(root, 'bun.lock')),
  )
  return sources > statSync(built).mtimeMs
}

if (import.meta.main) {
  const t = createTranslator(loadUserConfig(join(loadConfig().home, 'config.json')).locale)
  if (stale()) {
    console.info(t('cli.start.building'))
    const build = Bun.spawn(['bun', 'run', 'build'], {
      cwd: web,
      stdin: 'ignore',
      stdout: 'inherit',
      stderr: 'inherit',
      windowsHide: true,
    })
    if ((await build.exited) !== 0) {
      console.error(t('cli.start.buildFailed'))
      process.exit(1)
    }
  }
  await import('./main')
}
