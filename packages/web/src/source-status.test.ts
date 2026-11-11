import { describe, expect, test } from 'vitest'
import type { SourceKind } from './api'
import { nameFrom } from './source-status'

const kind = (
  properties: Record<string, { type: string; directory?: boolean; secret?: boolean }>,
  required: string[] = [],
): SourceKind =>
  ({
    kind: 'x',
    title: 'x',
    settings: { type: 'object', properties, required },
  }) as unknown as SourceKind

describe('the name a new project takes from its source', () => {
  test('the last part of its directory, whichever way the separators lean', () => {
    const local = kind({ path: { type: 'string', directory: true } }, ['path'])
    expect(nameFrom(local, { path: '/home/me/weekly-report/' })).toBe('weekly-report')
    expect(nameFrom(local, { path: 'C:\\work\\shop' })).toBe('shop')
    expect(nameFrom(local, {})).toBe('')
  })

  test('a remote source is named after its directory, not its host', () => {
    const ssh = kind({ host: { type: 'string' }, path: { type: 'string', directory: true } }, [
      'host',
      'path',
    ])
    expect(nameFrom(ssh, { host: 'box', path: '~/src/api' })).toBe('api')
  })

  test("a repository by its own name, never by a secret's value", () => {
    const github = kind(
      { token: { type: 'string', secret: true }, repository: { type: 'string' } },
      ['token', 'repository'],
    )
    expect(nameFrom(github, { token: 'ghp_x', repository: 'daidr/dou-ui' })).toBe('dou-ui')
    expect(nameFrom(github, { repository: 'git@github.com:daidr/dou-ui.git' })).toBe('dou-ui')
  })
})
