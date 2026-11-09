import { DEFAULT_COMMAND_RULES, DEFAULT_LIMITS, PluginHost } from '@ariadne/agent'
import anthropic from '@ariadne/plugin-anthropic'
import { mkdtempSync, readFileSync, statSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { beforeEach, describe, expect, test } from 'vitest'
import { DEFAULT_KEYS, SettingsStore } from './settings'

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
    expect(Object.keys(saved).toSorted()).toEqual(['models', 'providers', 'roles', 'showCost'])
  })

  test('work slots are checked and kept', () => {
    const store = new SettingsStore(path, host)
    expect(store.current.concurrency).toEqual({ slots: 3, reserved: 1 })
    expect(() => store.updateConcurrency({ slots: 0, reserved: 1 })).toThrow()
    expect(() => store.updateConcurrency({ slots: 2, reserved: 1.5 })).toThrow()
    store.updateConcurrency({ slots: 5, reserved: 0 })
    expect(new SettingsStore(path, host).current.concurrency).toEqual({ slots: 5, reserved: 0 })
  })

  test('sandbox settings are checked, trimmed and kept', () => {
    const store = new SettingsStore(path, host)
    expect(() => store.updateSandbox({ mode: 'strict', hide: [] })).toThrow()
    expect(() => store.updateSandbox({ mode: 'auto', hide: [1] })).toThrow()
    store.updateSandbox({ mode: 'off', hide: [' ~/.aws ', '', '~/.aws'] })
    expect(new SettingsStore(path, host).current.sandbox).toEqual({ mode: 'off', hide: ['~/.aws'] })
  })

  test('the send key and snippets are checked and kept', () => {
    const store = new SettingsStore(path, host)
    expect(store.current.input).toEqual({ submitOn: 'enter', snippets: [], keys: DEFAULT_KEYS })
    expect(() => store.updateInput({ submitOn: 'shift-enter', snippets: [] })).toThrow()
    expect(() =>
      store.updateInput({ submitOn: 'enter', snippets: [{ id: 'a', name: ' ', text: 'x' }] }),
    ).toThrow()
    expect(() =>
      store.updateInput({
        submitOn: 'enter',
        snippets: [
          { id: 'a', name: 'A', text: 'x' },
          { id: 'a', name: 'B', text: 'y' },
        ],
      }),
    ).toThrow()
    store.updateInput({
      submitOn: 'mod-enter',
      snippets: [{ id: 's1', name: ' 复查 ', text: '再看一遍改动\n' }],
    })
    expect(new SettingsStore(path, host).current.input).toEqual({
      submitOn: 'mod-enter',
      snippets: [{ id: 's1', name: '复查', text: '再看一遍改动\n' }],
      keys: DEFAULT_KEYS,
    })
  })

  test('shortcuts can be bound to other single key presses, each to its own', () => {
    const store = new SettingsStore(path, host)
    const input = { submitOn: 'enter' as const, snippets: [] }
    expect(() => store.updateInput({ ...input, keys: { search: 'g i' } })).toThrow()
    expect(() => store.updateInput({ ...input, keys: { search: 'mod+up' } })).toThrow()
    store.updateInput({ ...input, keys: { search: 'Mod+P', stop: 'shift+escape' } })
    expect(new SettingsStore(path, host).current.input.keys).toEqual({
      ...DEFAULT_KEYS,
      search: 'mod+p',
      stop: 'shift+escape',
    })
  })

  test('command rules start as the defaults until the user saves their own', () => {
    const store = new SettingsStore(path, host)
    expect(store.work().rules).toEqual(DEFAULT_COMMAND_RULES)
    store.updateWork({ rules: [] })
    expect(new SettingsStore(path, host).work().rules).toEqual([])
  })

  test('instructions, command rules and limits are checked and kept', () => {
    const store = new SettingsStore(path, host)
    expect(store.work().limits).toEqual(DEFAULT_LIMITS)
    expect(() => store.updateWork({ rules: [{ pattern: 'git push*', action: 'maybe' }] })).toThrow()
    expect(() => store.updateWork({ limits: { compactShare: 0.95 } })).toThrow()
    expect(() =>
      store.updateWork({ limits: { commandTimeout: 900, commandTimeoutMax: 600 } }),
    ).toThrow()
    store.updateWork({
      instructions: ' 回答简短 ',
      rules: [{ pattern: ' git push * ', action: 'ask' }],
      limits: { commandTimeout: 300, modelRetries: 2 },
    })
    const again = new SettingsStore(path, host)
    expect(again.work()).toMatchObject({
      instructions: '回答简短',
      rules: [{ pattern: 'git push *', action: 'ask' }],
      limits: { ...DEFAULT_LIMITS, commandTimeout: 300, modelRetries: 2 },
    })
    expect(again.turnSettings('prj_x')).toEqual({
      limits: again.work().limits,
      instructions: '回答简短',
      commandRules: [{ pattern: 'git push *', action: 'ask' }],
      env: {},
    })
  })

  test("a project's command environment keeps its values on the server", () => {
    const store = new SettingsStore(path, host)
    expect(() => store.updateProjectEnv('prj_a', [{ name: '1BAD', value: 'x' }])).toThrow()
    expect(() => store.updateProjectEnv('prj_a', [{ name: 'NEW', value: '' }])).toThrow()
    // Git configuration from the environment would lift the remote push block.
    for (const name of [
      'GIT_CONFIG_COUNT',
      'GIT_CONFIG_KEY_0',
      'git_config_global',
      'ARIADNE_HOME',
    ]) {
      expect(() => store.updateProjectEnv('prj_a', [{ name, value: 'x' }])).toThrow()
    }
    store.updateProjectEnv('prj_a', [{ name: 'TOKEN', value: 'secret-env' }])
    // An empty value keeps what is stored; the names are all the page sees.
    expect(store.updateProjectEnv('prj_a', [{ name: 'TOKEN', value: '' }])).toEqual(['TOKEN'])
    expect(store.turnSettings('prj_a').env).toEqual({ TOKEN: 'secret-env' })
    store.forgetProject('prj_a')
    expect(new SettingsStore(path, host).turnSettings('prj_a').env).toEqual({})
  })

  test('a hand-edited environment never hands a command git configuration', () => {
    writeFileSync(
      path,
      JSON.stringify({ projectEnv: { prj_a: { GIT_CONFIG_COUNT: '1', KEEP: 'yes' } } }),
    )
    expect(new SettingsStore(path, host).turnSettings('prj_a').env).toEqual({ KEEP: 'yes' })
  })

  test('request settings reach the resolved model; header values stay on the server', () => {
    const store = new SettingsStore(path, host)
    const body = provider({ baseUrl: '', apiKey: 'k' }) as {
      providers: Record<string, unknown>[]
      models: Record<string, unknown>[]
    }
    body.providers[0] = {
      ...body.providers[0],
      headers: [{ name: 'X-Org', value: 'org-secret' }],
      extraBody: { temperature: 0.3 },
      timeoutSeconds: 120,
    }
    body.models[0] = { ...body.models[0], maxTokens: 4096 }
    store.updateModels(body)
    expect(store.resolveModel('thread')?.request).toEqual({
      headers: { 'X-Org': 'org-secret' },
      extraBody: { temperature: 0.3 },
      timeoutSeconds: 120,
      maxTokens: 4096,
    })
    const shown = store.publicModels().providers[0]!
    expect(shown.headers).toEqual(['X-Org'])
    expect(JSON.stringify(store.publicModels())).not.toContain('org-secret')
    // Sent again without a value, the header keeps it.
    body.providers[0] = { ...body.providers[0], headers: [{ name: 'X-Org', value: '' }] }
    store.updateModels(body)
    expect(store.resolveModel('thread')?.request?.headers).toEqual({ 'X-Org': 'org-secret' })
    body.providers[0] = { ...body.providers[0], extraBody: ['no'] }
    expect(() => store.updateModels(body)).toThrow()
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

  test('a fallback goes with its role, and a role without a model takes the one it follows', () => {
    const store = new SettingsStore(path, host)
    const settings = provider({ baseUrl: '', apiKey: 'k' })
    const models = [...settings.models, { id: 'm2', provider: 'p1', model: 'backup' }]
    store.updateModels({
      ...settings,
      models,
      roles: { thread: { model: 'm1', effort: 'high', fallback: 'm2' } },
    })
    expect(store.resolveModel('thread')?.fallback).toMatchObject({
      model: 'backup',
      effort: 'high',
    })
    // The coordinator and triage follow the threads' model, and so their fallback.
    expect(store.resolveModel('coordinator')?.fallback?.model).toBe('backup')
    expect(store.resolveModel('triage')?.fallback?.model).toBe('backup')
    // A thread given the fallback itself has none.
    expect(store.resolveModel('thread', 'm2')?.fallback).toBeUndefined()
    // A role with a model of its own and no fallback has none.
    store.updateModels({
      ...settings,
      models,
      roles: { thread: { model: 'm1', fallback: 'm2' }, coordinator: { model: 'm2' } },
    })
    expect(store.resolveModel('coordinator')?.fallback).toBeUndefined()
    expect(() =>
      store.updateModels({
        ...settings,
        models,
        roles: { thread: { model: 'm1', fallback: 'x' } },
      }),
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
    // Triage left unset is the coordinator as it is; given a model, it uses that.
    expect(store.resolveModel('triage')).toMatchObject({ model: 'small', effort: 'low' })
    store.updateModels({
      ...settings,
      models: [
        ...settings.models,
        { id: 'm2', provider: 'p1', model: 'small' },
        { id: 'm3', provider: 'p1', model: 'large' },
      ],
      roles: {
        thread: { model: 'm3', effort: 'high' },
        coordinator: { model: 'm3', effort: null },
        triage: { model: 'm2', effort: 'low' },
      },
    })
    expect(store.resolveModel('triage')).toMatchObject({ model: 'small', effort: 'low' })
    expect(store.resolveModel('coordinator')).toMatchObject({ model: 'large' })
    // Only a quick look takes the triage model, and only while the coordinator's context
    // fits well within it (128k by default: summarised at 76.8k, spared to 61.4k).
    expect(store.resolveCoordinator({ triage: false, contextTokens: 0 })?.model).toBe('large')
    expect(store.resolveCoordinator({ triage: true, contextTokens: 1000 })?.model).toBe('small')
    expect(store.resolveCoordinator({ triage: true, contextTokens: 70_000 })?.model).toBe('large')
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

  test('a model’s price reaches the turn, is checked, and the cost switch is kept', () => {
    const store = new SettingsStore(path, host)
    const price = { currency: 'CNY', input: 2, output: 8, cacheRead: 0.5 }
    store.updateModels({ ...provider({ apiKey: 'k' }, { price }), showCost: true })
    expect(store.resolveModel('thread')?.price).toEqual(price)
    expect(store.publicModels().showCost).toBe(true)
    expect(new SettingsStore(path, host).publicModels().showCost).toBe(true)
    for (const bad of [
      { ...price, currency: 'EUR' },
      { ...price, output: undefined },
      { ...price, input: -1 },
      { ...price, cacheWrite: 'x' },
    ]) {
      expect(() => store.updateModels(provider({ apiKey: 'k' }, { price: bad }))).toThrow()
    }
    // Without the switch, usage shows tokens only.
    store.updateModels(provider({ apiKey: 'k' }))
    expect(store.publicModels().showCost).toBe(false)
    expect(store.resolveModel('thread')?.price).toBeUndefined()
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

  test("a reference's token is kept under its own id, and falls back to the project's", async () => {
    const { Ariadne } = await import('@ariadne/core')
    const { createHandler } = await import('./app')
    const connected: { repository: string; token?: string }[] = []
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
          summary: 'example.com/{repository}',
          provides: ['workspace', 'repo'],
          async connect(settings: { repository: string; token?: string }) {
            connected.push(settings)
            return {
              workspace: {
                view: async () => ({
                  ref: 'origin/main',
                  list: async () => {
                    if (settings.repository === 'gone/gone') throw new Error('404 not found')
                    return []
                  },
                  readBytes: async () => new Uint8Array(),
                }),
              },
            }
          },
        } as never),
    })
    const app = new Ariadne({ path: ':memory:' })
    const store = new SettingsStore(path, host)
    const handle = createHandler({ app, token: 't', settings: store, host })
    const projectId = app.createProject('P').id
    const send = (method: string, where: string, body?: unknown) =>
      handle(
        new Request(`http://127.0.0.1/api/projects/${projectId}/${where}`, {
          method,
          headers: { authorization: 'Bearer t', 'content-type': 'application/json' },
          body: body === undefined ? undefined : JSON.stringify(body),
        }),
      )
    const add = (settings: unknown) =>
      send('POST', 'references', { reference: { kind: 'fake-repo', settings } })
    await send('PUT', 'source', {
      source: { kind: 'fake-repo', settings: { repository: 'a/app', token: 'tok-main' } },
    })

    // No token typed: the project's own, of the same kind, is used.
    expect((await add({ repository: 'a/lib' })).status).toBe(200)
    expect(connected.at(-1)).toEqual({ repository: 'a/lib', token: 'tok-main' })
    expect((await add({ repository: 'b/design', token: 'tok-b' })).status).toBe(200)
    expect(connected.at(-1)).toEqual({ repository: 'b/design', token: 'tok-b' })
    const [lib, design] = app.getProject(projectId).references
    expect([lib?.name, design?.name]).toEqual(['lib', 'design'])
    expect(design?.settings).toEqual({ repository: 'b/design' })
    expect(JSON.stringify(app.events({ projectId }))).not.toContain('tok-b')
    expect(store.sourceSecrets(projectId)).toEqual({
      token: 'tok-main',
      [`reference.${design!.id}.token`]: 'tok-b',
    })
    // What the page is told has been set leaves the references' out.
    expect(store.sourceSecretsSet(projectId)).toEqual(['token'])

    // The same one again is refused, not added under a second name.
    expect((await add({ repository: 'a/lib' })).status).toBe(400)
    expect(app.getProject(projectId).references).toHaveLength(2)
    // One that cannot be read is not added.
    const refused = await add({ repository: 'gone/gone' })
    expect(refused.status).toBe(400)
    expect(app.getProject(projectId).references).toHaveLength(2)

    // Saving the project's own source keeps the references' tokens.
    await send('PUT', 'source', {
      source: { kind: 'fake-repo', settings: { repository: 'a/app', token: 'tok-new' } },
    })
    expect(store.sourceSecrets(projectId)[`reference.${design!.id}.token`]).toBe('tok-b')

    expect((await send('DELETE', `references/${design!.id}`)).status).toBe(200)
    expect(app.getProject(projectId).references.map((r) => r.name)).toEqual(['lib'])
    expect(readFileSync(path, 'utf8')).not.toContain('tok-b')
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

const stdio = (env: { name: string; value: string }[] = []) => ({
  id: 'files',
  name: '文件',
  transport: 'stdio',
  command: 'some-server',
  args: ['--root', '/tmp'],
  env,
  tools: { read: 'read', write: 'ask', drop: 'off' },
})

describe('MCP servers', () => {
  test('keep environment values and headers away from the browser, and keep one left empty', () => {
    const store = new SettingsStore(path, host)
    store.updateMcp({ servers: [stdio([{ name: 'TOKEN', value: 'secret-env' }])] })
    const shown = store.publicMcp()
    expect(JSON.stringify(shown)).not.toContain('secret-env')
    // `ask` is what an unmarked tool is anyway, so it is not stored.
    expect(shown[0]).toMatchObject({ env: ['TOKEN'], tools: { read: 'read', drop: 'off' } })
    store.updateMcp({ servers: [stdio([{ name: 'TOKEN', value: '' }])] })
    expect(store.current.mcp.servers[0]?.env).toEqual({ TOKEN: 'secret-env' })
    // A new name without a value has nothing to keep.
    expect(() => store.updateMcp({ servers: [stdio([{ name: 'OTHER', value: '' }])] })).toThrow()

    store.updateMcp({
      servers: [
        {
          id: 'web',
          name: '',
          transport: 'http',
          url: 'https://mcp.example.test/mcp',
          headers: [{ name: 'Authorization', value: 'Bearer secret-header' }],
          tools: {},
        },
      ],
    })
    expect(JSON.stringify(store.publicMcp())).not.toContain('secret-header')
    expect(store.publicMcp()[0]).toMatchObject({ name: 'web', headers: ['Authorization'] })
    expect(JSON.parse(readFileSync(path, 'utf8')).mcp.servers[0].headers).toEqual({
      Authorization: 'Bearer secret-header',
    })
  })

  test('refuse ids, addresses and directories that do not fit', () => {
    const store = new SettingsStore(path, host)
    const bad = [
      { ...stdio(), id: 'Has Space' },
      { ...stdio(), id: 'x'.repeat(30) },
      { ...stdio(), command: ' ' },
      { ...stdio(), args: [1] },
      { ...stdio(), tools: { read: 'maybe' } },
      // Ariadne's home holds its settings and log.
      { ...stdio(), cwd: join(path, '..') },
      { id: 'web', transport: 'http', url: 'file:///etc/passwd', tools: {} },
      { id: 'web', transport: 'http', url: 'https://user:pw@example.test', tools: {} },
      { id: 'web', transport: 'carrier-pigeon', tools: {} },
    ]
    for (const server of bad) expect(() => store.updateMcp({ servers: [server] })).toThrow()
    expect(() => store.updateMcp({ servers: [stdio(), stdio()] })).toThrow()
    expect(store.current.mcp.servers).toEqual([])
  })
})

describe('web search', () => {
  test('a key is asked for only when no model service searches the web itself', () => {
    const store = new SettingsStore(path, host)
    // Nothing set up yet: nothing to ask.
    expect(store.publicSearch()).toEqual({ keySet: false, needed: false })
    store.updateModels(provider({ baseUrl: 'https://relay.example', apiKey: 'k' }))
    expect(store.searchModels()).toEqual([])
    expect(store.publicSearch()).toEqual({ keySet: false, needed: true })
    store.updateModels(provider({ baseUrl: 'https://api.anthropic.com', apiKey: 'k' }))
    expect(store.searchModels().map((model) => model.label)).toEqual(['主力 / some-model'])
    expect(store.publicSearch()).toEqual({ keySet: false, needed: false })
  })

  test('the key is kept out of what the page gets; empty keeps it, null clears it', () => {
    const store = new SettingsStore(path, host)
    expect(store.searchService()).toBeNull()
    expect(store.updateSearch({ apiKey: ' tvly-secret ' })).toEqual({ keySet: true, needed: false })
    expect(JSON.parse(readFileSync(path, 'utf8')).search).toEqual({ apiKey: 'tvly-secret' })
    const service = store.searchService()
    expect(service?.kind).toBe('tavily')
    store.updateSearch({ apiKey: '' })
    store.updateSearch({})
    expect(new SettingsStore(path, host).searchService()).not.toBeNull()
    expect(store.searchService()).toBe(service)
    expect(() => store.updateSearch({ apiKey: 5 })).toThrow()
    expect(() => store.updateSearch({ apiKey: 'two words' })).toThrow()
    store.updateSearch({ apiKey: null })
    expect(store.searchService()).toBeNull()
    expect(store.publicSearch().keySet).toBe(false)
  })
})
