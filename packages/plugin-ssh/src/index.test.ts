import { existsSync } from 'node:fs'
import { mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
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

  test('a long data directory gets no shared connection', () => {
    const shell = new SshShell({ host: 'box' }, join(base, 'x'.repeat(120)))
    expect(shell.host).toBe('box')
    expect(existsSync(join(base, 'x'.repeat(120)))).toBe(false)
  })
})
