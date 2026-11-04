import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { beforeEach, describe, expect, test } from 'vitest'
import { PluginHost, resolvePluginPath } from './plugins'

let dir: string
let host: PluginHost

beforeEach(() => {
  dir = mkdtempSync(join(tmpdir(), 'ariadne-plugins-'))
  host = new PluginHost(join(dir, 'data'))
})

/** A plugin on disk whose setup registers a model protocol `kind`, then maybe throws. */
function writePlugin(name: string, kind: string, options: { fail?: boolean } = {}) {
  const root = join(dir, name)
  mkdirSync(join(root, 'src'), { recursive: true })
  writeFileSync(join(root, 'package.json'), JSON.stringify({ exports: { '.': './src/main.ts' } }))
  writeFileSync(
    join(root, 'src', 'main.ts'),
    `export default {
      id: ${JSON.stringify(name)},
      name: { 'zh-CN': ${JSON.stringify(name)} },
      setup(ctx) {
        ctx.registerModelProvider({ kind: ${JSON.stringify(kind)}, title: { 'zh-CN': 'x' }, settings: { type: 'object', properties: {} }, async *stream() {} })
        ${options.fail ? "throw new Error('setup broke')" : ''}
      },
    }`,
  )
  return root
}

describe('plugins from a path', () => {
  test('a folder loads through its package.json entry and can be unloaded', async () => {
    const root = writePlugin('demo', 'demo-protocol')
    expect(resolvePluginPath(root)).toBe(join(root, 'src', 'main.ts'))
    await host.loadFromPath(root)
    expect(host.models.has('demo-protocol')).toBe(true)
    expect(host.records()).toEqual([
      expect.objectContaining({
        id: 'demo',
        source: 'path',
        path: root,
        status: 'loaded',
        provides: { models: ['demo-protocol'], contexts: [], dashboards: [], searches: [] },
      }),
    ])
    await host.unloadPath(root)
    expect(host.models.has('demo-protocol')).toBe(false)
    expect(host.records()).toEqual([])
  })

  test('a setup that throws leaves nothing registered and is reported', async () => {
    const root = writePlugin('broken', 'broken-protocol', { fail: true })
    await expect(host.loadFromPath(root)).rejects.toThrow('setup broke')
    expect(host.models.has('broken-protocol')).toBe(false)
    expect(host.records()).toEqual([
      expect.objectContaining({ id: null, path: root, status: 'failed', error: 'setup broke' }),
    ])
  })

  test('a kind another plugin registered is refused', async () => {
    await host.loadFromPath(writePlugin('first', 'shared'))
    await expect(host.loadFromPath(writePlugin('second', 'shared'))).rejects.toThrow(
      'shared is already registered',
    )
    expect(host.plugins.map((plugin) => plugin.id)).toEqual(['first'])
  })

  test('relative and missing paths are refused before anything runs', async () => {
    expect(() => resolvePluginPath('plugins/demo')).toThrow('not an absolute path')
    await expect(host.loadFromPath(join(dir, 'nope'))).rejects.toThrow('does not exist')
    expect(host.records()[0]).toMatchObject({ status: 'failed' })
  })
})
