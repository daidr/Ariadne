import { join } from 'node:path'
import { mkdir } from 'node:fs/promises'

const root = join(import.meta.dir, '../..')
const run = (args: string[], cwd = root, env = process.env) =>
  Bun.spawn(args, {
    cwd,
    env,
    stdin: 'inherit',
    stdout: 'inherit',
    stderr: 'inherit',
    windowsHide: true,
  })

const dev = process.argv[2] === 'dev'
if (!dev) {
  const frontend = run(['bun', 'run', 'build'])
  if ((await frontend.exited) !== 0) process.exit(1)
}

const out = join(root, 'dist', 'dev')
await mkdir(out, { recursive: true })
const suffix = process.platform === 'win32' ? '.exe' : ''
const binary = join(out, `ariadne-${process.pid}${suffix}`)
const build = run(['go', 'build', '-o', binary, './cmd/ariadne'])
if ((await build.exited) !== 0) process.exit(1)

const children = [
  run([binary, 'serve'], root, {
    ...process.env,
    ARIADNE_OPEN: process.env.ARIADNE_OPEN ?? (dev ? '0' : '1'),
  }),
]
if (dev) {
  children.push(run(['bun', 'run', '--filter', '@ariadne/web', 'dev']))
}
function stop() {
  for (const child of children) {
    if (child.exitCode === null) child.kill('SIGTERM')
  }
}
process.on('SIGINT', stop)
process.on('SIGTERM', stop)
const code = await Promise.race(children.map((child) => child.exited))
stop()
await Promise.all(children.map((child) => child.exited))
await Bun.file(binary).unlink()
process.exitCode = code
