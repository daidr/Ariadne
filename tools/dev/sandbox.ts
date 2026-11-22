import { mkdtemp } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { parseArgs } from 'node:util'
import { startMockApi } from './mock-api'
import { seedSandbox } from './seed'

const { values } = parseArgs({
  args: process.argv.slice(2),
  options: {
    port: { type: 'string', default: '7438' },
    'web-port': { type: 'string', default: '5183' },
    go: { type: 'boolean', default: false },
  },
  strict: true,
})
const port = (value: string) => {
  const number = Number(value)
  if (!Number.isInteger(number) || number < 1 || number > 65535)
    throw new Error(`Invalid port: ${value}`)
  return number
}
const apiPort = port(values.port!)
const webPort = port(values['web-port']!)
if (apiPort === webPort) throw new Error('API and web ports must differ')
// Fail before seeding when either port is occupied. In watch mode a failed backend
// stays alive waiting for edits, so waiting for its process to exit is not enough.
const reservations: ReturnType<typeof Bun.serve>[] = []
try {
  for (const reservedPort of [apiPort, webPort]) {
    reservations.push(
      Bun.serve({
        hostname: '127.0.0.1',
        port: reservedPort,
        fetch: () => new Response('Starting sandbox', { status: 503 }),
      }),
    )
  }
} catch (error) {
  for (const reservation of reservations) reservation.stop(true)
  throw error
}
const root = join(import.meta.dir, '../..')
const home = await mkdtemp(join(tmpdir(), 'ariadne-dev-'))
const mock = startMockApi()
const baseUrl = `http://127.0.0.1:${mock.port}/v1`
const children: ReturnType<typeof Bun.spawn>[] = []
let stopping = false
function stop(code: number) {
  if (stopping) return
  stopping = true
  for (const reservation of reservations) reservation.stop(true)
  for (const child of children) child.kill()
  mock.stop(true)
  // Kept for debugging; each invocation creates a fresh home.
  console.info(`Sandbox data kept at: ${home}`)
  process.exit(code)
}
process.on('SIGINT', () => stop(0))
process.on('SIGTERM', () => stop(0))
try {
  await seedSandbox(home, baseUrl)
  const token = crypto.randomUUID()
  const env = {
    ...process.env,
    ARIADNE_HOME: home,
    ARIADNE_PORT: String(apiPort),
    ARIADNE_TOKEN: token,
    ARIADNE_OPEN: '0',
  }
  const executable = join(home, process.platform === 'win32' ? 'ariadne.exe' : 'ariadne')
  const build = Bun.spawn(['go', 'build', '-o', executable, './cmd/ariadne'], {
    cwd: root,
    stdin: 'ignore',
    stdout: 'inherit',
    stderr: 'inherit',
    windowsHide: true,
  })
  children.push(build)
  if ((await build.exited) !== 0) throw new Error('Go backend build failed')
  children.pop()
  for (const reservation of reservations) reservation.stop(true)
  children.push(
    Bun.spawn([executable, 'serve'], {
      cwd: root,
      env,
      stdin: 'ignore',
      stdout: 'inherit',
      stderr: 'inherit',
      windowsHide: true,
    }),
  )
  children.push(
    Bun.spawn(
      [
        process.execPath,
        join(root, 'node_modules/vite/bin/vite.js'),
        '--host',
        '127.0.0.1',
        '--port',
        String(webPort),
        '--strictPort',
      ],
      {
        cwd: join(root, 'packages/web'),
        env,
        stdin: 'ignore',
        stdout: 'inherit',
        stderr: 'inherit',
        windowsHide: true,
      },
    ),
  )
  console.info(
    `\nSandbox settings: ${join(home, 'config.json')}\nMock AI endpoint: ${baseUrl}\nBrowser: http://127.0.0.1:${webPort}/#token=${token}\n`,
  )
  const code = await Promise.race(children.map((child) => child.exited))
  stop(code || 1)
} catch (error) {
  console.error(error)
  stop(1)
}
