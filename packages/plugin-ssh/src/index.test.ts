import { existsSync } from 'node:fs'
import { mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { NothingToBrowse } from '@ariadne/plugin-sdk'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { sshOptions, sshProvider, SshShell } from './index'

let base: string

beforeEach(async () => {
  base = await mkdtemp(join(tmpdir(), 'ariadne-ssh-'))
})

afterEach(async () => {
  await rm(base, { recursive: true, force: true })
})

describe('ssh connection', () => {
  test('forwards nothing and never asks', () => {
    const options = sshOptions('/tmp/cm/%C').join(' ')
    expect(options).toContain('BatchMode=yes')
    expect(options).toContain('ForwardAgent=no')
    expect(options).toContain('ClearAllForwardings=yes')
    expect(options).toContain('PermitLocalCommand=no')
    expect(options).toContain('ControlPath=/tmp/cm/%C')
  })

  test('without a shared connection, joins none the user opened', () => {
    const options = sshOptions(null).join(' ')
    expect(options).toContain('ControlMaster=no')
    expect(options).toContain('ControlPath=none')
    expect(options).toContain('GSSAPIDelegateCredentials=no')
    expect(options).toContain('Tunnel=no')
  })

  test('refuses a host that ssh would read as an option', async () => {
    await expect(
      sshProvider(join(base, 'data')).connect({ host: '-oProxyCommand=x', path: '/srv' }),
    ).rejects.toThrow('host')
    await expect(
      sshProvider(join(base, 'data')).connect({ host: 'box', path: 'relative' }),
    ).rejects.toThrow('absolute')
  })

  test('browsing before a host is typed lists nothing, quietly', async () => {
    await expect(
      sshProvider(join(base, 'data')).browse!({ host: '' }, '/srv', {}),
    ).rejects.toBeInstanceOf(NothingToBrowse)
  })

  test('a long data directory gets no shared connection', () => {
    const shell = new SshShell({ host: 'box' }, join(base, 'x'.repeat(120)))
    expect(shell.host).toBe('box')
    expect(existsSync(join(base, 'x'.repeat(120)))).toBe(false)
  })
})

/**
 * A stand-in for ssh that runs the remote command line here with sh, the way sshd hands
 * it to the login shell, and a daemon on the PATH for it to find.
 */
async function machine(withDaemon: boolean) {
  const bin = join(base, 'bin')
  await mkdir(bin)
  const ssh = join(bin, 'fake-ssh')
  await writeFile(
    ssh,
    '#!/bin/sh\nwhile [ "$1" != "--" ]; do shift; done\nshift 2\nexec sh -c "$*"\n',
    { mode: 0o755 },
  )
  if (withDaemon) {
    const main = fileURLToPath(new URL('../../daemon/src/main.ts', import.meta.url))
    await writeFile(
      join(bin, 'ariadne-daemon'),
      `#!/bin/sh\nARIADNE_DAEMON_HOME='${join(base, 'daemon-home')}' exec '${process.execPath}' '${main}' "$@"\n`,
      { mode: 0o755 },
    )
  }
  return ssh
}

describe('ssh through the remote daemon', () => {
  const path = process.env.PATH
  afterEach(() => {
    process.env.PATH = path
  })

  test('one long-lived connection carries every call', async () => {
    const ssh = await machine(true)
    process.env.PATH = `${join(base, 'bin')}:${path}`
    const shell = new SshShell({ host: 'box' }, null, { ssh })
    expect(await shell.throughDaemon()).toBe(true)
    const args = ["it's", '$HOME `id`', 'two\nlines', '中文']
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
    // Every call went through the daemon's own process, not a new one each.
    const pids = await Promise.all([1, 2].map(() => shell.run('echo $PPID', [])))
    expect(pids[0]!.stdout).toBe(pids[1]!.stdout)
    await shell.dispose()
  })

  test('without a daemon, each call is its own ssh process', async () => {
    const ssh = await machine(false)
    process.env.PATH = `${join(base, 'bin')}:${path}`
    const shell = new SshShell({ host: 'box' }, null, { ssh })
    expect(await shell.throughDaemon()).toBe(false)
    const result = await shell.run('printf "%s" "$1"; cat', ['arg'], { input: 'in' })
    expect(result).toEqual({ exitCode: 0, stdout: 'argin', stderr: '' })
    await shell.dispose()
  })
})
