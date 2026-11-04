import { certificate, Lockout, newToken, serveDaemon } from '@ariadne/daemon'
import { CERT_NAME, pairingCode, parseAddress, parsePairingCode } from '@ariadne/daemon/protocol'
import { branchFor, run } from '@ariadne/plugin-local'
import { createSandbox, detectSandbox, noSandbox } from '@ariadne/sandbox'
import { existsSync } from 'node:fs'
import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterAll, beforeAll, describe, expect, test } from 'vitest'
import { daemonProvider, DaemonShell, type DaemonTarget } from './index'

let base: string
let home: string
let server: ReturnType<typeof serveDaemon>
let target: DaemonTarget

beforeAll(async () => {
  base = await mkdtemp(join(tmpdir(), 'ariadne-daemon-'))
  home = join(base, 'daemon')
  await mkdir(home, { mode: 0o700 })
  const cert = await certificate(home)
  server = serveDaemon({
    home,
    hostname: '127.0.0.1',
    port: 0,
    certificate: cert,
    sandbox: noSandbox,
  })
  target = {
    host: '127.0.0.1',
    port: server.port!,
    token: newToken(home),
    fingerprint: cert.fingerprint,
  }
})

afterAll(async () => {
  await server.stop(true)
  await rm(base, { recursive: true, force: true })
})

describe('pairing', () => {
  test('codes and addresses are read strictly', () => {
    const token = 'a'.repeat(43)
    const fingerprint = 'b'.repeat(43)
    expect(parsePairingCode(` ${pairingCode(token, fingerprint)}\n`)).toEqual({
      token,
      fingerprint,
    })
    expect(parsePairingCode(token)).toBeNull()
    expect(parsePairingCode(`${token}.${fingerprint}x`)).toBeNull()
    expect(parseAddress('box')).toEqual({ host: 'box', port: 7429 })
    expect(parseAddress('10.0.0.2:9000')).toEqual({ host: '10.0.0.2', port: 9000 })
    expect(parseAddress('[::1]:9000')).toEqual({ host: '::1', port: 9000 })
    expect(parseAddress('-oProxy=x')).toBeNull()
    expect(parseAddress('box:70000')).toBeNull()
    expect(parseAddress('a b')).toBeNull()
  })

  test('the daemon keeps only a hash of the token', async () => {
    const stored = await readFile(join(home, 'token.sha256'), 'utf8')
    expect(stored).not.toContain(target.token)
    expect(stored.trim()).toMatch(/^[0-9a-f]{64}$/)
  })

  test('the certificate is pinned by its fingerprint', async () => {
    const result = await new DaemonShell({ ...target, fingerprint: 'x'.repeat(43) }).run(
      'echo hi',
      [],
    )
    expect(result.exitCode).toBe(255)
    expect(result.stderr).toContain('does not match the pairing code')
  })

  test('a wrong token is refused', async () => {
    const result = await new DaemonShell({ ...target, token: 'y'.repeat(43) }).run('echo hi', [])
    expect(result.exitCode).toBe(255)
    expect(result.stderr).toContain('refused the pairing code')
  })

  test('an address that keeps failing is locked out for a while', () => {
    let now = 0
    const lockout = new Lockout(3, 1000, () => now)
    for (let i = 0; i < 3; i++) lockout.fail('1.2.3.4')
    expect(lockout.blocked('1.2.3.4')).toBe(true)
    expect(lockout.blocked('5.6.7.8')).toBe(false)
    now = 1001
    expect(lockout.blocked('1.2.3.4')).toBe(false)
  })

  test('the WebSocket is refused without the token too', async () => {
    const ws = new WebSocket(`wss://127.0.0.1:${target.port}/v1/shell`, {
      tls: { ca: await Bun.file(join(home, 'cert.pem')).text(), servername: CERT_NAME },
    } as never)
    const opened = await new Promise<boolean>((resolve) => {
      ws.addEventListener('open', () => resolve(true))
      ws.addEventListener('error', () => resolve(false))
    })
    expect(opened).toBe(false)
  })
})

describe('daemon shell', () => {
  test('arguments, input and output arrive unchanged, many calls on one connection', async () => {
    const shell = new DaemonShell(target)
    const args = ["it's", '$HOME `id`', 'two\nlines', '', '中文']
    const script =
      'for a in "$@"; do printf "[%s]" "$a"; done; printf "|"; cat; echo oops >&2; exit 7'
    const results = await Promise.all(
      [1, 2, 3].map((n) => shell.run(script, args, { input: `input ${n}\n` })),
    )
    results.forEach((result, i) => {
      expect(result.exitCode).toBe(7)
      expect(result.stdout).toBe(`${args.map((a) => `[${a}]`).join('')}|input ${i + 1}\n`)
      expect(result.stderr).toBe('oops\n')
    })
    // Large output in many frames, split inside multibyte characters.
    const big = await shell.run('for i in $(seq 1 20000); do printf "中文%05d\\n" "$i"; done', [])
    expect(big.stdout.split('\n')).toHaveLength(20001)
    expect(big.stdout).toContain('中文19999')
    await shell.dispose()
  })

  test('a held call ends when it times out, with what it started', async () => {
    const shell = new DaemonShell(target)
    const marker = join(base, 'still-running')
    const started = Date.now()
    const result = await shell.run(
      // Like the workspace's EXEC: the command ends when its input closes.
      `set -m; ( sleep 5; touch "$1" ) > /dev/null 2>&1 & child=$!
      while read -r _; do :; done; kill -KILL -- "-$child"; exit 9`,
      [marker],
      { hold: true, timeoutMs: 300 },
    )
    expect(result.exitCode).toBe(9)
    expect(Date.now() - started).toBeLessThan(3000)
    await Bun.sleep(100)
    expect(existsSync(marker)).toBe(false)
    await shell.dispose()
  })

  test('a lost connection fails the calls under way, and the next call connects again', async () => {
    const shell = new DaemonShell(target)
    const running = shell.run('sleep 30', [], { timeoutMs: 60_000 })
    await Bun.sleep(200)
    await shell.dispose()
    const lost = await running
    expect(lost.exitCode).toBe(255)
    expect(lost.stderr).toContain('daemon:')
    const again = await shell.run('echo back', [])
    expect(again.stdout).toBe('back\n')
    await shell.dispose()
  })

  test('an idle connection closes, and the next call opens one again', async () => {
    const shell = new DaemonShell(target, 100)
    expect((await shell.run('echo one', [])).stdout).toBe('one\n')
    expect(shell.connected).toBe(true)
    await Bun.sleep(300)
    expect(shell.connected).toBe(false)
    expect((await shell.run('echo two', [])).stdout).toBe('two\n')
    await shell.dispose()
  })

  test('a new pairing code takes hold at once', async () => {
    const shell = new DaemonShell(target)
    expect((await shell.run('echo one', [])).stdout).toBe('one\n')
    const token = newToken(home)
    // The open connection was let in with the old token and stays.
    expect((await shell.run('echo two', [])).stdout).toBe('two\n')
    await shell.dispose()
    expect((await shell.run('echo three', [])).stderr).toContain('refused')
    target = { ...target, token }
    expect((await new DaemonShell(target).run('echo four', [])).stdout).toBe('four\n')
  })
})

describe('daemon source', () => {
  test('a git checkout gives each thread its own worktree on the remote machine', async () => {
    const repo = join(base, 'repo')
    await mkdir(repo)
    const git = (...args: string[]) => run(['git', ...args], { cwd: repo })
    await git('init', '-q', '-b', 'main')
    await git('config', 'user.email', 'test@example.com')
    await git('config', 'user.name', 'test')
    await writeFile(join(repo, 'README.md'), '# repo\n')
    await git('add', '.')
    await git('commit', '-q', '-m', 'init')

    const settings = {
      address: `127.0.0.1:${target.port}`,
      pairing: pairingCode(target.token, target.fingerprint),
      path: repo,
    }
    const { workspace } = await daemonProvider().connect(settings)
    expect(workspace?.isolated).toBe(true)
    const ws = await workspace!.open({ threadId: 'thr_daemon01' })
    expect(ws.branch).toMatch(/^ariadne\//)
    expect(ws.base).toBe('main')
    expect(ws.label).toMatch(/^127\.0\.0\.1:\//)
    await ws.writeFile('src/new.ts', 'export const x = 1\n')
    expect(await ws.readFile('src/new.ts')).toBe('export const x = 1\n')
    const result = await ws.exec(
      'git add -A && git -c user.name=t -c user.email=t@t commit -qm add && git log --oneline | wc -l',
    )
    expect(result.stdout.trim()).toBe('2')
    const changes = await workspace!.branches!.changes(
      { threadId: 'thr_daemon01', branch: branchFor('thr_daemon01') },
      'main',
    )
    expect(changes?.files.map((file) => file.path)).toEqual(['src/new.ts'])
  })

  test('settings are checked before connecting', async () => {
    const provider = daemonProvider()
    const pairing = pairingCode(target.token, target.fingerprint)
    await expect(provider.connect({ address: 'a b', pairing, path: '/x' })).rejects.toThrow(
      'address',
    )
    await expect(
      provider.connect({ address: 'box', pairing: 'short', path: '/x' }),
    ).rejects.toThrow('pairing code')
    await expect(provider.connect({ address: 'box', pairing, path: 'rel' })).rejects.toThrow(
      'absolute',
    )
  })
})

const sandboxKind = await detectSandbox()

describe.skipIf(!sandboxKind)('daemon sandbox', () => {
  test("thread commands cannot read the daemon's own directory", async () => {
    const own = join(base, 'sandboxed')
    await mkdir(own, { mode: 0o700 })
    const cert = await certificate(own)
    const sandboxed = serveDaemon({
      home: own,
      hostname: '127.0.0.1',
      port: 0,
      certificate: cert,
      sandbox: createSandbox({ kind: sandboxKind, mode: 'auto', hide: [own] }),
    })
    try {
      const shell = new DaemonShell({
        host: '127.0.0.1',
        port: sandboxed.port!,
        token: newToken(own),
        fingerprint: cert.fingerprint,
      })
      const result = await shell.run('cat "$1"/key.pem "$1"/token.sha256; ls -A "$1"', [own])
      expect(result.stdout).toBe('')
      expect(result.stderr).toContain('No such file')
      await shell.dispose()
    } finally {
      await sandboxed.stop(true)
    }
  })
})
