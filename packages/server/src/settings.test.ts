import { PluginHost } from '@ariadne/agent'
import anthropic from '@ariadne/plugin-anthropic'
import { mkdtempSync, readFileSync, statSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { beforeEach, describe, expect, test } from 'vitest'
import { SettingsStore } from './settings'

let host: PluginHost
let path: string

beforeEach(async () => {
  host = new PluginHost('/tmp/ariadne-test-plugins')
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

  test('null clears a stored secret', () => {
    const store = new SettingsStore(path, host)
    store.updateModels(provider({ baseUrl: '', apiKey: 'secret-1' }))
    store.updateModels(provider({ baseUrl: '', apiKey: null }))
    expect(store.publicModels().providers[0]?.secretsSet).toEqual([])
    expect(readFileSync(path, 'utf8')).not.toContain('secret-1')
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

describe('project sources', () => {
  test('a source token goes to the config, never into the event log', async () => {
    const { Ariadne } = await import('@ariadne/core')
    const { createHandler } = await import('./app')
    const connected: unknown[] = []
    await host.load({
      id: 'fake-repo',
      name: { 'zh-CN': '假仓库' },
      setup: (ctx) =>
        ctx.registerContextProvider({
          kind: 'fake-repo',
          title: { 'zh-CN': '假仓库' },
          settings: {
            type: 'object',
            properties: {
              repository: { type: 'string', title: { 'zh-CN': '仓库' } },
              token: { type: 'string', secret: true, title: { 'zh-CN': '令牌' } },
            },
            required: ['repository'],
          },
          provides: ['workspace', 'repo'],
          async connect(settings: unknown) {
            connected.push(settings)
            return {}
          },
        } as never),
    })
    const app = new Ariadne({ path: ':memory:' })
    const store = new SettingsStore(path, host)
    const handle = createHandler({ app, token: 't', settings: store, host })
    const projectId = app.createProject('P').id
    const put = (source: unknown) =>
      handle(
        new Request(`http://127.0.0.1/api/projects/${projectId}/source`, {
          method: 'PUT',
          headers: { authorization: 'Bearer t', 'content-type': 'application/json' },
          body: JSON.stringify({ source }),
        }),
      )

    expect(
      (await put({ kind: 'fake-repo', settings: { repository: ' a/b ', token: 'tok-1' } })).status,
    ).toBe(200)
    expect(connected.at(-1)).toEqual({ repository: 'a/b', token: 'tok-1' })
    expect(app.getProject(projectId).source).toEqual({
      kind: 'fake-repo',
      settings: { repository: 'a/b' },
    })
    expect(JSON.stringify(app.events({ projectId }))).not.toContain('tok-1')
    expect(store.sourceSecrets(projectId)).toEqual({ token: 'tok-1' })

    // Saving again with the token left empty keeps it.
    await put({ kind: 'fake-repo', settings: { repository: 'a/c', token: '' } })
    expect(connected.at(-1)).toEqual({ repository: 'a/c', token: 'tok-1' })
    expect((await put({ kind: 'fake-repo', settings: {} })).status).toBe(400)

    await put(null)
    expect(store.sourceSecrets(projectId)).toEqual({})
    expect(readFileSync(path, 'utf8')).not.toContain('tok-1')
    app.close()
  })
})

function writePlugin(): string {
  const root = mkdtempSync(join(tmpdir(), 'ariadne-plugin-'))
  writeFileSync(
    join(root, 'index.ts'),
    `export default { id: 'echo', name: { 'zh-CN': 'Echo' }, setup(ctx) {
      ctx.registerModelProvider({ kind: 'echo', title: { 'zh-CN': 'Echo' }, settings: { type: 'object', properties: {} }, async *stream() {} })
    } }`,
  )
  return root
}

describe('plugins from a path', () => {
  test('load at once and on every start, and stop when removed', async () => {
    const store = new SettingsStore(path, host)
    const root = writePlugin()
    await store.addPlugin(root)
    expect(host.models.has('echo')).toBe(true)
    expect(store.publicModels().kinds.map((kind) => kind.kind)).toContain('echo')
    expect(JSON.parse(readFileSync(path, 'utf8')).plugins).toEqual([root])
    await expect(store.addPlugin(root)).rejects.toMatchObject({ code: 'error.pluginExists' })

    await store.removePlugin(root)
    expect(host.models.has('echo')).toBe(false)
    expect(JSON.parse(readFileSync(path, 'utf8')).plugins).toEqual([])
  })

  test('a path that fails to load is not remembered', async () => {
    const store = new SettingsStore(path, host)
    await expect(store.addPlugin('/nowhere/plugin')).rejects.toMatchObject({
      code: 'error.pluginLoad',
    })
    expect(store.current.plugins).toEqual([])
    expect(host.records().some((record) => record.status === 'failed')).toBe(false)
  })
})
