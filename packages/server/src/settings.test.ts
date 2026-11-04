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

const provider = (settings: Record<string, unknown>, model: Record<string, unknown> = {}) => ({
  providers: [{ id: 'p1', kind: 'anthropic', name: '主力', settings }],
  models: [{ id: 'm1', provider: 'p1', model: 'some-model', ...model }],
  roles: { thread: { model: 'm1', effort: null }, coordinator: { model: null, effort: null } },
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

  test('fields this version does not know are not written back', () => {
    writeFileSync(path, JSON.stringify({ models: { ...provider({}), default: 'm1' } }))
    const store = new SettingsStore(path, host)
    store.updateConcurrency({ slots: 2, reserved: 1 })
    const saved = JSON.parse(readFileSync(path, 'utf8')).models
    expect(Object.keys(saved).toSorted()).toEqual(['models', 'providers', 'roles'])
  })

  test('work slots are checked and kept', () => {
    const store = new SettingsStore(path, host)
    expect(store.current.concurrency).toEqual({ slots: 3, reserved: 1 })
    expect(() => store.updateConcurrency({ slots: 0, reserved: 1 })).toThrow()
    expect(() => store.updateConcurrency({ slots: 2, reserved: 1.5 })).toThrow()
    store.updateConcurrency({ slots: 5, reserved: 0 })
    expect(new SettingsStore(path, host).current.concurrency).toEqual({ slots: 5, reserved: 0 })
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
    expect(store.resolveModel('coordinator')).toBeNull()
    store.updateModels(provider({ baseUrl: '', apiKey: 'k' }))
    const resolved = store.resolveModel('coordinator')
    expect(resolved?.settings).toEqual({
      baseUrl: 'https://api.anthropic.com',
      apiKey: 'k',
      maxTokens: 8192,
    })
    expect(resolved?.label).toBe('主力 / some-model')
  })

  test("the default model's context size reaches the turn, and must be sensible", () => {
    const store = new SettingsStore(path, host)
    store.updateModels(provider({ baseUrl: '', apiKey: 'k' }, { contextTokens: 32_000 }))
    expect(store.resolveModel('coordinator')?.contextTokens).toBe(32_000)
    expect(() =>
      store.updateModels(provider({ baseUrl: '', apiKey: 'k' }, { contextTokens: 10 })),
    ).toThrow()
  })

  test('each role has its model, and a thread may be given another', () => {
    const store = new SettingsStore(path, host)
    const settings = provider({ baseUrl: '', apiKey: 'k' })
    store.updateModels({
      ...settings,
      models: [
        ...settings.models,
        { id: 'm2', provider: 'p1', model: 'small', note: '便宜，适合调研' },
        { id: 'm3', provider: 'p1', model: 'large' },
      ],
      roles: {
        thread: { model: 'm3', effort: 'high' },
        coordinator: { model: 'm2', effort: 'low' },
      },
    })
    expect(store.resolveModel('coordinator')).toMatchObject({ model: 'small', effort: 'low' })
    expect(store.resolveModel('thread')).toMatchObject({ model: 'large', effort: 'high' })
    expect(store.resolveModel('thread', 'm1')).toMatchObject({
      model: 'some-model',
      effort: 'high',
    })
    // A model the thread was given that is no longer set up falls back to the default.
    expect(store.resolveModel('thread', 'gone')?.model).toBe('large')
    expect(store.modelChoices()).toEqual([
      { id: 'm1', label: '主力 / some-model', note: '', threadDefault: false },
      { id: 'm2', label: '主力 / small', note: '便宜，适合调研', threadDefault: false },
      { id: 'm3', label: '主力 / large', note: '', threadDefault: true },
    ])
    // Without a model of its own the coordinator uses the threads', thinking as it is told.
    store.updateModels({
      ...settings,
      roles: { thread: { model: 'm1', effort: null }, coordinator: { model: null, effort: 'low' } },
    })
    expect(store.resolveModel('coordinator')).toMatchObject({ model: 'some-model', effort: 'low' })
    expect(store.resolveModel('thread')?.effort).toBeUndefined()
    // A role must name a model that is set up, and a known effort.
    const role = (thread: unknown) => ({ ...settings, roles: { ...settings.roles, thread } })
    expect(() => store.updateModels(role({ model: 'nope', effort: null }))).toThrow()
    expect(() => store.updateModels(role({ model: 'm1', effort: 'max' }))).toThrow()
  })

  test('rejects an unknown provider kind', () => {
    const store = new SettingsStore(path, host)
    expect(() =>
      store.updateModels({
        providers: [{ id: 'x', kind: 'nope', settings: {} }],
        models: [],
        roles: { coordinator: null, thread: null },
      }),
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

test('plugins inside the plugin data folder, where worktrees live, are refused', async () => {
  const store = new SettingsStore(path, host)
  const inside = join(path, '..', 'plugins', 'local', 'worktrees', 'x')
  await expect(store.addPlugin(inside)).rejects.toMatchObject({ code: 'error.pluginInWorkspace' })
})
