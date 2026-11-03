import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterAll, beforeAll, describe, expect, test } from 'vitest'
import {
  bwrapArgs,
  coverPaths,
  createSandbox,
  sandboxWorks,
  seatbeltProfile,
  unshareArgs,
  type SandboxKind,
} from './sandbox'

let base: string
let home: string
let work: string

beforeAll(async () => {
  base = await mkdtemp(join(tmpdir(), 'ariadne-sandbox-'))
  // Like the real layout: a GitHub checkout lives under Ariadne's own home.
  home = join(base, 'home')
  work = join(home, 'plugins', 'github', 'worktrees', 'o', 'r', 'thr_1')
  await mkdir(work, { recursive: true })
  await mkdir(join(home, 'plugins', 'github', 'worktrees', 'o', 'r', 'thr_2'), {
    recursive: true,
  })
  await mkdir(join(home, 'plugins', 'github', 'repos', 'o'), { recursive: true })
  await writeFile(join(home, 'token'), 'secret-token\n')
  await writeFile(join(home, 'config.json'), '{}\n')
  await writeFile(join(work, 'README.md'), '# repo\n')
})

afterAll(async () => {
  await rm(base, { recursive: true, force: true })
})

async function exec(argv: string[], cwd: string) {
  const proc = Bun.spawn(argv, { cwd, stdout: 'pipe', stderr: 'pipe' })
  const [stdout, exitCode] = await Promise.all([new Response(proc.stdout).text(), proc.exited])
  return { stdout, exitCode }
}

describe('coverPaths', () => {
  test('covers a hidden root whole when nothing in it is kept', () => {
    expect(coverPaths([home], [join(base, 'elsewhere')])).toEqual([{ kind: 'dir', path: home }])
  })

  test('covers only what lies beside a kept path inside a hidden root', () => {
    const covers = coverPaths([home], [work])
    const paths = covers.map((cover) => cover.path)
    expect(covers).toContainEqual({ kind: 'file', path: join(home, 'token') })
    expect(covers).toContainEqual({ kind: 'file', path: join(home, 'config.json') })
    // Another thread's checkout is hidden too.
    expect(paths).toContain(join(home, 'plugins', 'github', 'worktrees', 'o', 'r', 'thr_2'))
    expect(paths).toContain(join(home, 'plugins', 'github', 'repos'))
    expect(paths.some((path) => work.startsWith(path))).toBe(false)
  })

  test('a kept path holding a hidden root does not show it', () => {
    expect(coverPaths([home], [base])).toEqual([{ kind: 'dir', path: home }])
  })

  test('skips hidden paths that do not exist', () => {
    expect(coverPaths([join(base, 'missing')], [])).toEqual([])
  })
})

describe('sandbox arguments', () => {
  const covers = [
    { kind: 'dir' as const, path: '/h/plugins' },
    { kind: 'file' as const, path: '/h/token' },
  ]

  test('bwrap covers directories with tmpfs and files with /dev/null', () => {
    const argv = bwrapArgs(covers, ['bash', '-c', 'ls'])
    expect(argv.slice(0, 2)).toEqual(['bwrap', '--dev-bind'])
    expect(argv).toContain('--unshare-pid')
    expect(argv.join(' ')).toContain(
      '--tmpfs /h/plugins --ro-bind /dev/null /h/token -- bash -c ls',
    )
  })

  test('unshare mounts in its own namespace, then runs as the user again', () => {
    const argv = unshareArgs(covers, ['bash'], { uid: 1000, gid: 1000 })
    const tail = argv.slice(argv.indexOf('sh', argv.indexOf('-c')) + 1)
    expect(tail).toEqual([
      'dir',
      '/h/plugins',
      'file',
      '/h/token',
      '--',
      'unshare',
      '--user',
      '--map-user=1000',
      '--map-group=1000',
      '--',
      'bash',
    ])
  })

  test('the macOS profile reopens only kept paths inside a hidden one', () => {
    const profile = seatbeltProfile(['/h', '/b"x'], ['/h/w', '/', '/other'])
    expect(profile).toContain('(deny file-read* file-write* (subpath "/h") (subpath "/b\\"x"))')
    expect(profile).toContain('(allow file-read* file-write* (subpath "/h/w"))')
    expect(profile).not.toContain('(subpath "/other")')
    expect(profile).not.toContain('(subpath "/")')
  })

  test('turned off, commands run as they are', () => {
    const sandbox = createSandbox({ kind: 'bwrap', mode: 'off', hide: [home] })
    expect(sandbox.kind).toBeNull()
    expect(sandbox.status.mode).toBe('off')
    expect(sandbox.wrap(['true'], { keep: [] })).toEqual(['true'])
  })
})

const linuxKinds: SandboxKind[] = []
for (const kind of ['bwrap', 'unshare'] as const) {
  if (process.platform === 'linux' && (await sandboxWorks(kind))) linuxKinds.push(kind)
}

// Run for real with what this machine has; skipped where neither works.
describe.skipIf(!linuxKinds.length)('running in the sandbox', () => {
  for (const kind of linuxKinds) {
    test(`${kind} hides Ariadne's home but not the workspace`, async () => {
      const sandbox = createSandbox({ kind, mode: 'auto', hide: [home] })
      const wrap = (command: string) => sandbox.wrap(['bash', '-c', command], { keep: [work] })
      const token = await exec(wrap(`cat ${join(home, 'token')}`), work)
      expect(token.stdout).not.toContain('secret-token')
      const other = await exec(
        wrap(`ls ${join(home, 'plugins', 'github', 'worktrees', 'o', 'r', 'thr_2')} | wc -l`),
        work,
      )
      expect(other.stdout.trim()).toBe('0')
      const readme = await exec(wrap('cat README.md && echo made > made.txt'), work)
      expect(readme).toEqual({ stdout: '# repo\n', exitCode: 0 })
      expect(await readFile(join(work, 'made.txt'), 'utf8')).toBe('made\n')
      // Its own process list: the server is not in it.
      const ps = await exec(wrap('ls /proc | grep -c "^[0-9]"'), work)
      expect(Number(ps.stdout.trim())).toBeLessThan(10)
    })
  }
})
