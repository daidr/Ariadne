import { PluginHost } from '@ariadne/agent'
import anthropic from '@ariadne/plugin-anthropic'
import { mkdtempSync, readFileSync, statSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { beforeEach, describe, expect, test } from 'vitest'
import { SettingsStore } from './settings'

let host: PluginHost
let path: string

beforeEach(async () => {
  host = new PluginHost()
  await host.load(anthropic)
  path = join(mkdtempSync(join(tmpdir(), 'ariadne-settings-')), 'config.json')
})

const provider = (settings: Record<string, unknown>) => ({
  providers: [{ id: 'p1', kind: 'anthropic', name: '主力', settings }],
  default: { provider: 'p1', model: 'some-model' },
})

describe('model settings', () => {
  test('never hand secrets to the browser, and keep a secret left empty', () => {
    const store = new SettingsStore(path, host)
    store.updateModels(provider({ baseUrl: '', apiKey: 'secret-1' }))
    expect(statSync(path).mode & 0o777).toBe(0o600)

    const shown = store.publicModels()
    expect(JSON.stringify(shown)).not.toContain('secret-1')
    expect(shown.providers[0]).toMatchObject({ secretsSet: ['apiKey'], settings: { baseUrl: '' } })

    store.updateModels(provider({ baseUrl: 'https://example.test', apiKey: '' }))
    expect(JSON.parse(readFileSync(path, 'utf8')).models.providers[0].settings.apiKey).toBe(
      'secret-1',
    )
  })

  test('resolves the default model with schema defaults for empty fields', () => {
    const store = new SettingsStore(path, host)
    expect(store.resolveModel({} as never)).toBeNull()
    store.updateModels(provider({ baseUrl: '', apiKey: 'k' }))
    const resolved = store.resolveModel({} as never)
    expect(resolved?.settings).toEqual({
      baseUrl: 'https://api.anthropic.com',
      apiKey: 'k',
      maxTokens: 8192,
    })
    expect(resolved?.label).toBe('主力 / some-model')
  })

  test('rejects an unknown provider kind', () => {
    const store = new SettingsStore(path, host)
    expect(() =>
      store.updateModels({ providers: [{ id: 'x', kind: 'nope', settings: {} }], default: null }),
    ).toThrow()
  })
})
