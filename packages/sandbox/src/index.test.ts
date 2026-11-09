import { mkdir, mkdtemp, readFile, rename, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterAll, beforeAll, describe, expect, test } from 'vitest'
import {
  bwrapArgs,
  createSandbox,
  mountPlan,
  sandboxWorks,
  seatbeltProfile,
  unshareArgs,
  type Mount,
  type SandboxKind,
} from './index'

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

describe('mountPlan', () => {
  test('covers a hidden directory whole when nothing in it is kept', () => {
    expect(mountPlan([home], [join(base, 'elsewhere')])).toEqual([{ kind: 'tmpfs', path: home }])
  })

  test('covers a hidden directory whole and puts kept paths inside it back', () => {
    expect(mountPlan([home], [work])).toEqual([
      { kind: 'hold', path: work },
      { kind: 'tmpfs', path: home },
      { kind: 'bind', path: work },
    ])
  })

  test('a kept path holding a hidden directory does not show it', () => {
    expect(mountPlan([home], [base])).toEqual([{ kind: 'tmpfs', path: home }])
  })

  test('a kept path inside a hidden directory that a kept path holds is put back', () => {
    const skills = join(home, 'plugins', 'github', 'repos')
    expect(mountPlan([home], [base, skills], [skills])).toEqual([
      { kind: 'hold', path: skills },
      { kind: 'tmpfs', path: home },
      { kind: 'bind', path: skills },
      { kind: 'ro', path: skills },
    ])
  })

  test('hidden files are covered with /dev/null; missing paths are skipped', () => {
    const token = join(home, 'token')
    expect(mountPlan([token, join(base, 'missing')], [])).toEqual([{ kind: 'null', path: token }])
  })

  test('read-only paths lock first, kept paths inside them open again, and inner ones lock last', () => {
    const plan = mountPlan([], [work], [home, join(work, 'README.md')])
    expect(plan).toEqual([
      { kind: 'ro', path: home },
      { kind: 'rw', path: work },
      { kind: 'ro', path: join(work, 'README.md') },
    ])
  })

  test('read-only paths inside a covered directory are locked before it is covered', () => {
    const readme = join(work, 'README.md')
    const settings = join(home, 'config.json')
    expect(mountPlan([home], [work], [readme, settings])).toEqual([
      { kind: 'hold', path: work },
      { kind: 'ro', path: settings },
      { kind: 'tmpfs', path: home },
      { kind: 'bind', path: work },
      { kind: 'ro', path: readme },
    ])
  })
})

describe('sandbox arguments', () => {
  const plan: Mount[] = [
    { kind: 'hold', path: '/h/k' },
    { kind: 'tmpfs', path: '/h' },
    { kind: 'bind', path: '/h/k' },
    { kind: 'null', path: '/x/token' },
    { kind: 'ro', path: '/h/k/config' },
  ]

  test('bwrap covers, puts back and locks in order', () => {
    const argv = bwrapArgs(plan, ['bash', '-c', 'ls'])
    expect(argv.slice(0, 2)).toEqual(['bwrap', '--dev-bind'])
    expect(argv).toContain('--unshare-pid')
    expect(argv.join(' ')).toContain(
      '--tmpfs /h --bind /h/k /h/k --ro-bind /dev/null /x/token --ro-bind /h/k/config /h/k/config -- bash -c ls',
    )
  })

  test('unshare mounts in its own namespace, then runs as the user again', () => {
    const argv = unshareArgs(plan, ['bash'], { uid: 1000, gid: 1000 })
    const tail = argv.slice(argv.indexOf('bash', argv.indexOf('-c') + 2) + 1)
    expect(tail).toEqual([
      ...plan.flatMap((mount) => [mount.kind, mount.path]),
      '--',
      'unshare',
      '--user',
      '--map-user=1000',
      '--map-group=1000',
      '--',
      'bash',
    ])
  })

  test('the macOS profile reopens only kept paths inside a hidden one, and locks the rest', () => {
    const profile = seatbeltProfile(['/h', '/b"x'], ['/h/w', '/', '/other'], ['/h/w/.git/config'])
    expect(profile).toContain('(deny file-read* file-write* (subpath "/h") (subpath "/b\\"x"))')
    expect(profile).toContain('(allow file-read* file-write* (subpath "/h/w"))')
    expect(profile).toContain('(deny file-write* (subpath "/h/w/.git/config"))')
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
      // Not by a relative path from the workspace either.
      const relative = await exec(wrap('cat ../../../../../../token'), work)
      expect(relative.stdout).not.toContain('secret-token')
      const other = await exec(
        wrap(`ls ${join(home, 'plugins', 'github', 'worktrees', 'o', 'r', 'thr_2')} | wc -l`),
        work,
      )
      expect(other.stdout.trim()).toBe('0')
      // A settings file replaced by a rename while the command runs stays hidden.
      const saved = exec(wrap(`sleep 0.3; cat ${join(home, 'config.json')}`), work)
      await writeFile(join(home, 'config.next'), 'secret-settings\n')
      await rename(join(home, 'config.next'), join(home, 'config.json'))
      expect((await saved).stdout).not.toContain('secret-settings')
      const readme = await exec(wrap('cat README.md && echo made > made.txt'), work)
      expect(readme).toEqual({ stdout: '# repo\n', exitCode: 0 })
      expect(await readFile(join(work, 'made.txt'), 'utf8')).toBe('made\n')
      // Its own process list: the server is not in it.
      const ps = await exec(wrap('ls /proc | grep -c "^[0-9]"'), work)
      expect(Number(ps.stdout.trim())).toBeLessThan(10)
      // Shown paths inside a hidden one can be read and run by every command, not changed;
      // also when the workspace holds the hidden directory.
      const skills = join(home, 'skills')
      await mkdir(join(skills, 'release'), { recursive: true })
      await writeFile(join(skills, 'release', 'tag.sh'), 'echo tagged\n')
      const showing = createSandbox({ kind, mode: 'auto', hide: [home], show: () => [skills] })
      const script = join(skills, 'release', 'tag.sh')
      for (const keep of [[work], [base]]) {
        const ran = await exec(
          showing.wrap(
            ['bash', '-c', `bash ${script}; echo x >> ${script}; cat ${join(home, 'token')}`],
            { keep },
          ),
          keep[0]!,
        )
        expect(ran.stdout).toBe('tagged\n')
      }
      expect(await readFile(script, 'utf8')).toBe('echo tagged\n')
      // Read-only paths can be read but not changed, nor replaced.
      const locked = sandbox.wrap(
        [
          'bash',
          '-c',
          'cat README.md; echo x > README.md; echo y > y && mv y README.md; cat README.md',
        ],
        { keep: [work], readOnly: [join(work, 'README.md')] },
      )
      expect((await exec(locked, work)).stdout).toBe('# repo\n# repo\n')
      // A kept path inside a read-only one (a checkout of the server's own repository)
      // stays writable; what lies beside it does not.
      const outside = join(base, 'install')
      await mkdir(join(outside, 'repo', '.git', 'hooks'), { recursive: true })
      await writeFile(join(outside, 'main.ts'), 'code\n')
      const nestedLock = sandbox.wrap(
        [
          'bash',
          '-c',
          'echo w > ok.txt; echo x > ../main.ts; echo h > .git/hooks/pre-commit; cat ../main.ts; ls .git/hooks',
        ],
        {
          keep: [work, join(outside, 'repo')],
          readOnly: [outside, join(outside, 'repo', '.git', 'hooks')],
        },
      )
      expect((await exec(nestedLock, join(outside, 'repo'))).stdout).toBe('code\n')
      expect(await readFile(join(outside, 'repo', 'ok.txt'), 'utf8')).toBe('w\n')
    })
  }
})
