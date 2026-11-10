import { PluginHost } from '@ariadne/agent'
import { Ariadne } from '@ariadne/core'
import type { ModelStreamPart } from '@ariadne/plugin-sdk'
import { mkdirSync, mkdtempSync, readFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { createHandler } from './app'
import { checkModel, suggestModel, type ModelCheck } from './model-check'
import { openCommand } from './open-browser'
import { ModelCatalog } from './prices'
import { SettingsStore } from './settings'

const token = 'test-token'
const catalog = {
  demo: {
    models: {
      'big-2': {
        cost: { input: 2, output: 8, cache_read: 0.5 },
        limit: { context: 400_000, output: 32_000 },
        modalities: { input: ['text', 'image'] },
        tool_call: true,
        release_date: '2026-05-01',
      },
      'big-1': {
        cost: { input: 1, output: 4 },
        limit: { context: 200_000, output: 9_999_999 },
        modalities: { input: ['text'] },
        tool_call: true,
        release_date: '2025-01-01',
      },
      'big-2-mini': { tool_call: true, release_date: '2026-06-01' },
      'talk-only': { tool_call: false, release_date: '2026-07-01', reasoning: false },
    },
  },
  reseller: { models: { 'x/big-2': { cost: { input: 3, output: 9 } } } },
}
const prices = () =>
  new ModelCatalog((async () => Response.json(catalog)) as unknown as typeof fetch)

let app: Ariadne
let dir: string
let host: PluginHost
let settings: SettingsStore
let handle: ReturnType<typeof createHandler>
/** What the fake service is asked, and how it behaves. */
let service: {
  listed: string[] | Error
  asked: { settings: unknown; headers: unknown }[]
  env: Record<string, string>
}
let checked: string[]
const check: ModelCheck = async (model) => {
  checked.push(model.model)
  return model.model.startsWith('big')
    ? null
    : { key: 'error.modelCheck', params: { detail: 'no such model' } }
}

beforeEach(async () => {
  app = new Ariadne({ path: ':memory:' })
  dir = mkdtempSync(join(tmpdir(), 'ariadne-onboarding-'))
  host = new PluginHost(dir)
  service = { listed: ['talk-only', 'big-2-mini', 'big-2', 'big-1'], asked: [], env: {} }
  checked = []
  await host.load({
    id: 'demo',
    name: { 'zh-CN': '演示' },
    setup: (ctx) =>
      ctx.registerModelProvider({
        kind: 'demo',
        title: { 'zh-CN': '演示服务' },
        settings: {
          type: 'object',
          properties: {
            baseUrl: { type: 'string', title: { 'zh-CN': '地址' }, default: 'https://demo.test' },
            apiKey: { type: 'string', secret: true, title: { 'zh-CN': 'Key' } },
          },
        },
        async models(talksWith: unknown, options: { headers?: unknown } = {}) {
          service.asked.push({ settings: talksWith, headers: options.headers })
          if (service.listed instanceof Error) throw service.listed
          return service.listed
        },
        find: () =>
          Object.entries(service.env).map(([key, apiKey]) => ({
            id: key,
            name: 'Demo',
            from: { env: key },
            settings: { apiKey },
          })),
        async *stream(): AsyncGenerator<ModelStreamPart> {},
      } as never),
  })
  settings = new SettingsStore(join(dir, 'config.json'), host)
  handle = createHandler({ app, token, settings, host, prices: prices(), check })
})

afterEach(() => app.close())

function call(path: string, init: { method?: string; body?: unknown } = {}) {
  const headers = new Headers({ authorization: `Bearer ${token}` })
  const body = init.body === undefined ? undefined : JSON.stringify(init.body)
  if (body) headers.set('content-type', 'application/json')
  return handle(new Request(`http://127.0.0.1${path}`, { method: init.method, headers, body }))
}

const models = (names: string[], apiKey: string | null = 'k-1') => ({
  providers: [{ id: 'p1', kind: 'demo', name: '', settings: { apiKey } }],
  models: names.map((model, index) => ({ id: `m${index}`, provider: 'p1', model })),
  roles: { thread: { model: 'm0', effort: null }, coordinator: { model: null, effort: null } },
})

describe('the model catalog', () => {
  test('describes models by name, each fact from the best service that states it', async () => {
    const info = await prices().describe(['big-2', 'big-1', 'big-2-mini', 'unknown'])
    expect(info.get('big-2')).toEqual({
      contextTokens: 400_000,
      maxTokens: 32_000,
      images: true,
      price: { currency: 'USD', input: 2, output: 8, cacheRead: 0.5 },
      tools: true,
      released: '2026-05-01',
    })
    // An output limit no model has is left out.
    expect(info.get('big-1')).toMatchObject({ contextTokens: 200_000, images: false })
    expect(info.get('big-1')?.maxTokens).toBeUndefined()
    expect(info.get('big-2-mini')).toEqual({ tools: true, released: '2026-06-01' })
    expect(info.has('unknown')).toBe(false)
    // A model that does not reason has no effort to set.
    expect((await prices().describe(['talk-only'])).get('talk-only')?.efforts).toEqual([])
    // The service the address names goes first; what it leaves out comes from the others.
    const hinted = (await prices().describe(['big-2'], 'other https://reseller.test')).get('big-2')
    expect(hinted).toMatchObject({ price: { input: 3, output: 9 }, contextTokens: 400_000 })
  })

  test('starts with the newest general model that calls tools', async () => {
    const info = await prices().describe(['talk-only', 'big-2-mini', 'big-2', 'big-1'])
    expect(suggestModel(['talk-only', 'big-2-mini', 'big-2', 'big-1'], info)).toBe('big-2')
    // Unknown to the catalog: the first that is not small or special by its name.
    expect(suggestModel(['text-embedding-9', 'new-mini', 'new-large'], new Map())).toBe('new-large')
    expect(suggestModel(['only-mini'], new Map())).toBe('only-mini')
    expect(suggestModel([], new Map())).toBeNull()
  })
})

const save = async (body: unknown) =>
  (await (await call('/api/settings/models', { method: 'PUT', body })).json()) as {
    problems: unknown[]
    models: { model: string }[]
  }

describe('the model form', () => {
  test('lists a typed-in provider’s models with what the catalog knows', async () => {
    await call('/api/settings/models', { method: 'PUT', body: models(['big-1']) })
    const list = (provider: unknown) =>
      call('/api/settings/model-list', { method: 'POST', body: { provider } })

    // The key left empty in the form is the stored one; the default address is filled in.
    const listed = await list({ id: 'p1', kind: 'demo', settings: { apiKey: '' } })
    expect(service.asked.at(-1)?.settings).toEqual({ baseUrl: 'https://demo.test', apiKey: 'k-1' })
    const body = (await listed.json()) as { models: { id: string; info?: unknown }[] }
    // Those that call tools first, then the rest, each as the service ordered them.
    expect(body.models.map((model) => model.id)).toEqual([
      'big-2-mini',
      'big-2',
      'big-1',
      'talk-only',
    ])
    expect(body.models[1]?.info).toMatchObject({ contextTokens: 400_000, images: true })
    expect(JSON.stringify(body)).not.toContain('k-1')

    await list({ id: 'new', kind: 'demo', settings: { apiKey: 'k-2', baseUrl: 'https://b.test' } })
    expect(service.asked.at(-1)?.settings).toEqual({ baseUrl: 'https://b.test', apiKey: 'k-2' })

    service.listed = new Error('HTTP 401: bad key')
    const refused = await list({ kind: 'demo', settings: {} })
    expect(refused.status).toBe(502)
    expect(await refused.json()).toEqual({
      error: { key: 'error.modelCheck', params: { detail: 'HTTP 401: bad key' } },
    })
    expect((await list({ kind: 'none' })).status).toBe(400)
  })

  test('an agent program’s models keep its order, aliases described as what they stand for', async () => {
    await host.load({
      id: 'agentish',
      name: { 'zh-CN': '代理' },
      setup: (ctx) =>
        ctx.registerModelProvider({
          kind: 'agentish',
          title: { 'zh-CN': '代理程序' },
          program: true,
          settings: { type: 'object', properties: {} },
          models: async () => [
            { id: 'default', title: 'Big 2', resolves: 'big-2', efforts: ['low', 'max'] },
            { id: 'quiet', efforts: [] },
            'big-1',
            { id: 'big', title: 'Big 2', resolves: 'big-2' },
            'big',
          ],
          find: () => [{ id: 'agentish', from: { program: 'agentish' }, settings: {} }],
          async *stream(): AsyncGenerator<ModelStreamPart> {},
        } as never),
    })
    const listed = await call('/api/settings/model-list', {
      method: 'POST',
      body: { provider: { kind: 'agentish', settings: {} } },
    })
    const body = (await listed.json()) as {
      models: {
        id: string
        title?: string
        info?: { contextTokens?: number; efforts?: string[] }
      }[]
    }
    expect(body.models.map(({ id, title }) => ({ id, title }))).toEqual([
      { id: 'default', title: 'Big 2' },
      { id: 'quiet', title: undefined },
      { id: 'big-1', title: undefined },
      { id: 'big', title: 'Big 2' },
    ])
    // What the service says it accepts goes with what the catalog knows.
    expect(body.models[0]?.info).toMatchObject({ contextTokens: 400_000, efforts: ['low', 'max'] })
    expect(body.models[1]?.info).toEqual({ efforts: [] })
    expect(body.models[0]?.info?.contextTokens).toBe(400_000)
    expect(body.models[2]?.info?.contextTokens).toBe(200_000)
    expect(body.models[2]?.info?.efforts).toBeUndefined()

    // Taken into use, it starts on what the program itself is set to.
    const used = await call('/api/settings/found/use', {
      method: 'POST',
      body: { id: 'agentish:agentish' },
    })
    const saved = (await used.json()) as { models: { model: string; contextTokens?: number }[] }
    expect(saved.models[0]).toMatchObject({
      model: 'default',
      contextTokens: 400_000,
      efforts: ['low', 'max'],
    })
  })

  test('a catalog that cannot be read still lists the models', async () => {
    handle = createHandler({
      app,
      token,
      settings,
      host,
      check,
      prices: new ModelCatalog(
        (async () => new Response('no', { status: 503 })) as unknown as typeof fetch,
      ),
    })
    const listed = await call('/api/settings/model-list', {
      method: 'POST',
      body: { provider: { kind: 'demo', settings: {} } },
    })
    expect(((await listed.json()) as { models: unknown[] }).models).toHaveLength(4)
    const info = await call('/api/settings/model-info?model=big-2')
    expect(info.status).toBe(502)
  })

  test('tells what the catalog knows of one model', async () => {
    const known = await call('/api/settings/model-info?model=big-2&hint=demo')
    expect(await known.json()).toMatchObject({ info: { maxTokens: 32_000 } })
    const unknown = await call('/api/settings/model-info?model=nope')
    expect(unknown.status).toBe(404)
    expect(await unknown.json()).toEqual({
      error: { key: 'error.priceUnknown', params: { model: 'nope' } },
    })
  })

  test('saving asks each new model once and saves all the same', async () => {
    const first = await save(models(['big-1', 'typo']))
    expect(checked).toEqual(['big-1', 'typo'])
    expect(first.problems).toEqual([
      {
        model: 'm1',
        provider: 'p1',
        problem: { key: 'error.modelCheck', params: { detail: 'no such model' } },
      },
    ])
    expect(first.models.map((model) => model.model)).toEqual(['big-1', 'typo'])

    // Nothing new: nothing is asked.
    checked = []
    expect((await save(models(['big-1', 'typo'], ''))).problems).toEqual([])
    expect(checked).toEqual([])
    // One more model on the same provider: only it.
    await save(models(['big-1', 'typo', 'big-2'], ''))
    expect(checked).toEqual(['big-2'])
    // Another key: the provider is asked through its first model.
    checked = []
    await save(models(['big-1', 'typo', 'big-2'], 'k-2'))
    expect(checked).toEqual(['big-1'])
  })

  test('the check asks for one word and stops at the first answer', async () => {
    const asked: unknown[] = []
    const provider = (parts: ModelStreamPart[] | Error) =>
      ({
        provider: {
          async *stream(_settings: unknown, request: unknown) {
            asked.push(request)
            if (parts instanceof Error) throw parts
            yield* parts
          },
        },
        settings: {},
        model: 'm',
      }) as never
    expect(await checkModel(provider([{ type: 'text', text: 'Hello' }]), 'zh-CN')).toBeNull()
    expect(asked[0]).toMatchObject({ model: 'm', messages: [{ role: 'user', content: 'Hi' }] })
    // A sign of life is not an answer yet: the refusal after it counts.
    const refused = [
      { type: 'alive' },
      { type: 'finish', reason: 'error', error: 'insufficient quota' },
    ] as ModelStreamPart[]
    expect(await checkModel(provider(refused), 'zh-CN')).toEqual({
      key: 'error.modelCheck',
      params: { detail: 'insufficient quota' },
    })
    expect(
      await checkModel(provider([{ type: 'alive' }, { type: 'text', text: 'Hi' }]), 'zh-CN'),
    ).toBeNull()
    expect(await checkModel(provider(new Error('refused')), 'zh-CN')).toEqual({
      key: 'error.modelCheck',
      params: { detail: 'refused' },
    })
    // An agent program is asked whether it is there, not for an answer; it says why not
    // in the user's language.
    const program = {
      provider: { check: async () => ({ 'zh-CN': '找不到命令 x', en: 'command not found: x' }) },
      settings: {},
    }
    expect(await checkModel(program as never, 'en')).toEqual({
      key: 'error.modelCheck',
      params: { detail: 'command not found: x' },
    })
    expect(await checkModel(program as never, 'zh-CN')).toMatchObject({
      params: { detail: '找不到命令 x' },
    })
  })
})

describe('what is already on this machine', () => {
  test('offers a key in the environment and takes it into use', async () => {
    service.env = { DEMO_API_KEY: 'k-env' }
    const found = await (await call('/api/settings/found')).json()
    expect(found).toEqual({
      found: [
        {
          id: 'demo:DEMO_API_KEY',
          kind: 'demo',
          title: { 'zh-CN': 'Demo', en: 'Demo' },
          from: { env: 'DEMO_API_KEY' },
        },
      ],
    })

    const used = await call('/api/settings/found/use', {
      method: 'POST',
      body: { id: 'demo:DEMO_API_KEY' },
    })
    const body = (await used.json()) as {
      providers: { name: string; secretsSet: string[] }[]
      models: Record<string, unknown>[]
      roles: { thread: { model: string } }
      problems: unknown[]
    }
    expect(body.problems).toEqual([])
    expect(checked).toEqual(['big-2'])
    expect(body.providers[0]).toMatchObject({ name: 'Demo', secretsSet: ['apiKey'] })
    expect(body.models[0]).toMatchObject({
      model: 'big-2',
      contextTokens: 400_000,
      maxTokens: 32_000,
      price: { input: 2, output: 8 },
    })
    expect(body.roles.thread.model).toBe(body.models[0]?.id)
    expect(JSON.stringify(body)).not.toContain('k-env')
    // The key is kept, so the next start does not need the variable.
    expect(
      JSON.parse(readFileSync(join(dir, 'config.json'), 'utf8')).models.providers[0],
    ).toMatchObject({ settings: { apiKey: 'k-env' } })
    // In use: not offered again.
    expect(await (await call('/api/settings/found')).json()).toEqual({ found: [] })
  })

  test('a setup whose service refuses is not saved', async () => {
    service.env = { DEMO_API_KEY: 'k-bad' }
    service.listed = new Error('HTTP 401: bad key')
    const used = await call('/api/settings/found/use', {
      method: 'POST',
      body: { id: 'demo:DEMO_API_KEY' },
    })
    expect(used.status).toBe(400)
    expect(await used.json()).toEqual({
      error: { key: 'error.modelCheck', params: { detail: 'HTTP 401: bad key' } },
    })
    expect(settings.publicModels().providers).toEqual([])
    // Nothing to start with is said in the user's language, not passed on as a detail.
    service.listed = []
    const empty = await call('/api/settings/found/use', {
      method: 'POST',
      body: { id: 'demo:DEMO_API_KEY' },
    })
    expect(await empty.json()).toEqual({ error: { key: 'error.modelNone' } })
    expect(settings.publicModels().providers).toEqual([])
    const missing = await call('/api/settings/found/use', { method: 'POST', body: { id: 'x' } })
    expect(missing.status).toBeGreaterThanOrEqual(400)
  })
})

describe('the first screen', () => {
  test('one message starts a project, named after its directory', async () => {
    const { default: local } = await import('@ariadne/plugin-local')
    await host.load(local)
    const repo = join(dir, 'weekly-report')
    mkdirSync(repo)
    const created = await call('/api/projects', {
      method: 'POST',
      body: {
        name: 'weekly-report',
        message: '给报表页加上导出',
        source: { kind: 'local-directory', settings: { path: repo } },
      },
    })
    expect(created.status).toBe(201)
    const { project } = (await created.json()) as {
      project: { id: string; name: string; unnamed: boolean; coordinatorId: string }
    }
    expect(project).toMatchObject({ name: 'weekly-report', unnamed: false })
    expect(app.getProject(project.id).source).toMatchObject({ kind: 'local-directory' })
    expect(app.pageMessages(project.coordinatorId).messages.map((m) => m.content)).toEqual([
      '给报表页加上导出',
    ])

    // A directory that is not there leaves no project behind.
    const before = app.listProjects().length
    const refused = await call('/api/projects', {
      method: 'POST',
      body: {
        message: 'x',
        source: { kind: 'local-directory', settings: { path: join(dir, 'nope') } },
      },
    })
    expect(refused.status).toBeGreaterThanOrEqual(400)
    expect(app.listProjects()).toHaveLength(before)
  })

  test('without a directory the first words name it until it is renamed', async () => {
    const created = await call('/api/projects', {
      method: 'POST',
      body: { message: '做一个周报工具' },
    })
    const { project } = (await created.json()) as {
      project: { id: string; name: string; unnamed: boolean }
    }
    expect(project).toMatchObject({ name: '做一个周报工具', unnamed: true })
    const renamed = await call(`/api/projects/${project.id}`, {
      method: 'PUT',
      body: { name: ' 周报 ' },
    })
    expect(renamed.status).toBeLessThan(300)
    expect(app.getProject(project.id)).toMatchObject({ name: '周报', unnamed: false })
    expect(
      (await call(`/api/projects/${project.id}`, { method: 'PUT', body: { name: ' ' } })).status,
    ).toBe(400)
  })
})

describe('remembered for every project', () => {
  test('lines go into the instructions once, and come out', () => {
    expect(settings.addInstruction('  用中文\n回复 ')).toBe(true)
    expect(settings.addInstruction('用中文 回复')).toBe(false)
    settings.addInstruction('提交前跑测试')
    expect(settings.current.instructions).toBe('用中文 回复\n提交前跑测试')
    expect(settings.removeInstruction('用中文 回复')).toBe(true)
    expect(settings.removeInstruction('用中文 回复')).toBe(false)
    expect(settings.current.instructions).toBe('提交前跑测试')
    expect(() => settings.addInstruction('长'.repeat(100_000))).toThrow()
  })

  test('the user takes one back from the conversation', async () => {
    const project = app.createProject('P')
    settings.addInstruction('用中文回复')
    app.noteEverywhere(project.id, {
      conversationId: project.coordinatorId,
      action: 'saved',
      text: '用中文回复',
    })
    const notice = () => app.listNotices(project.coordinatorId).at(-1)!
    expect(notice()).toMatchObject({ kind: 'memory-everywhere', action: 'saved', undone: false })
    const undo = () =>
      call(`/api/projects/${project.id}/notices/${notice().id}/undo`, { method: 'POST' })
    expect((await undo()).status).toBe(204)
    expect(settings.current.instructions).toBe('')
    expect(notice()).toMatchObject({ undone: true })
    // Once: a second press does not bring it back.
    await undo()
    expect(settings.current.instructions).toBe('')
  })
})

describe('opening the page', () => {
  test('each platform has its command; no display, no browser', () => {
    const url = 'http://127.0.0.1:7421/#token=a&b'
    expect(openCommand(url, 'darwin', {})).toEqual(['open', url])
    expect(openCommand(url, 'win32', {})).toEqual(['rundll32', 'url.dll,FileProtocolHandler', url])
    expect(openCommand(url, 'linux', { DISPLAY: ':0' })).toEqual(['xdg-open', url])
    expect(openCommand(url, 'linux', { WAYLAND_DISPLAY: 'wayland-0' })).toEqual(['xdg-open', url])
    expect(openCommand(url, 'linux', {})).toBeNull()
  })
})
