import {
  CheckSearchSettingsResponseSchema,
  DeletePluginResponseSchema,
  LinkPreviewSchema,
  LocalizedErrorSchema,
  ModelProblemSchema,
  ModelSettingsSchema,
  PluginConfigSchema,
  PluginRecordSchema,
  PluginService,
  SearchSettingsSchema,
  SettingsSchemaSchema,
  UpdateService,
  UpdateStatusSchema,
} from '@ariadne/proto'
import { create, toBinary } from '@bufbuild/protobuf'
import { ValueSchema } from '@bufbuild/protobuf/wkt'
import { describe, expect, it } from 'vitest'
import { ApiError, createAriadneTransport, createRpcClient, type RpcFetch } from './rpc'
import {
  fromProtoLinkPreview,
  fromProtoModelProblems,
  fromProtoModelSettings,
  fromProtoPluginConfig,
  fromProtoPluginRecord,
  fromProtoSearchCheckResult,
  fromProtoSearchSettings,
  fromProtoSettingsSchema,
  fromProtoUpdateStatus,
  toJsonObject,
  toProtoSaveModelSettingsRequest,
  toProtoSearchInput,
} from './rpc-settings'

describe('Domain 1 ConnectRPC mappers', () => {
  it('reconstructs SettingsSchema properties in fieldOrder and decodes defaults', () => {
    const proto = create(SettingsSchemaSchema, {
      type: 'object',
      required: ['token'],
      fieldOrder: ['token', 'port', 'enabled', 'mode', 'region'],
      properties: {
        region: {
          type: 'select',
          title: { values: { 'zh-CN': '区域', en: 'Region' } },
          options: ['cn', 'us'],
          defaultValue: create(ValueSchema, { kind: { case: 'stringValue', value: 'cn' } }),
        },
        mode: {
          type: 'enum',
          title: { values: { 'zh-CN': '模式', en: 'Mode' } },
          options: ['fast', 'safe'],
          defaultValue: create(ValueSchema, { kind: { case: 'stringValue', value: 'fast' } }),
        },
        enabled: {
          type: 'boolean',
          title: { values: { 'zh-CN': '启用' } },
          defaultValue: create(ValueSchema, { kind: { case: 'boolValue', value: true } }),
        },
        port: {
          type: 'number',
          title: { values: { 'zh-CN': '端口' } },
          defaultValue: create(ValueSchema, { kind: { case: 'numberValue', value: 8080 } }),
        },
        token: {
          type: 'string',
          title: { values: { 'zh-CN': '令牌' } },
          secret: true,
        },
      },
    })

    const schema = fromProtoSettingsSchema(proto)
    expect(Object.keys(schema.properties)).toEqual(['token', 'port', 'enabled', 'mode', 'region'])
    expect(schema.required).toEqual(['token'])
    expect(schema.properties.token).toEqual({
      type: 'string',
      title: { 'zh-CN': '令牌' },
      secret: true,
    })
    expect(schema.properties.port).toEqual({
      type: 'number',
      title: { 'zh-CN': '端口' },
      default: 8080,
    })
    expect(schema.properties.enabled).toEqual({
      type: 'boolean',
      title: { 'zh-CN': '启用' },
      default: true,
    })
    expect(schema.properties.mode).toEqual({
      type: 'enum',
      title: { 'zh-CN': '模式', en: 'Mode' },
      options: ['fast', 'safe'],
      default: 'fast',
    })
    expect(schema.properties.region).toEqual({
      type: 'enum',
      title: { 'zh-CN': '区域', en: 'Region' },
      options: ['cn', 'us'],
      default: 'cn',
    })
  })

  it('strips undefined while preserving null in toJsonObject', () => {
    expect(
      toJsonObject({
        keepNull: null,
        dropUndefined: undefined,
        nested: { a: 1, b: undefined, c: null },
        list: [1, undefined, null, 'ok'],
      }),
    ).toEqual({
      keepNull: null,
      nested: { a: 1, c: null },
      list: [1, null, 'ok'],
    })
  })

  it('maps unified SearchSettings, SearchCheckResult, and dynamic SearchSettingsInput', () => {
    const mapped = toProtoSearchInput({
      provider: 'tavily',
      services: {
        tavily: { baseUrl: 'https://api.tavily.com', apiKey: 'tvly-secret' },
        deepseek: { baseUrl: 'https://api.deepseek.com', apiKey: null },
        'custom-lua': { strict: false, ignored: undefined },
      },
    })
    expect(mapped).toEqual({
      provider: 'tavily',
      services: {
        tavily: { baseUrl: 'https://api.tavily.com', apiKey: 'tvly-secret' },
        deepseek: { baseUrl: 'https://api.deepseek.com', apiKey: null },
        'custom-lua': { strict: false },
      },
    })

    expect(toProtoSearchInput({ services: { deepseek: { baseUrl: '' } } })).toEqual({
      services: { deepseek: { baseUrl: '' } },
    })

    const disabledSearch = fromProtoSearchSettings(
      create(SearchSettingsSchema, {
        provider: '',
        disabledProvider: 'tavily',
        ready: false,
        keySet: false,
        needed: true,
      }),
    )
    expect(disabledSearch.disabledProvider).toBe('tavily')

    const decoded = fromProtoSearchSettings(
      create(SearchSettingsSchema, {
        provider: 'custom-lua',
        ready: true,
        keySet: false,
        needed: false,
        models: ['OpenAI / gpt-5'],
        plugins: [{ values: { 'zh-CN': '自定义搜索', en: 'Custom Search' } }],
        services: [
          {
            kind: 'custom-lua',
            title: { values: { 'zh-CN': '自定义搜索', en: 'Custom Search' } },
            description: { values: { 'zh-CN': '外部插件', en: 'External plugin' } },
            settings: {
              type: 'object',
              fieldOrder: ['strict'],
              properties: {
                strict: {
                  type: 'boolean',
                  title: { values: { 'zh-CN': '严格' } },
                  defaultValue: create(ValueSchema, { kind: { case: 'boolValue', value: true } }),
                },
              },
            },
            capabilities: { maxResults: 10, supportsCheck: true },
            values: { strict: true },
            secretsSet: [],
            ready: true,
          },
        ],
      }),
    )
    expect(decoded).toEqual({
      provider: 'custom-lua',
      ready: true,
      keySet: false,
      needed: false,
      models: ['OpenAI / gpt-5'],
      plugins: [{ 'zh-CN': '自定义搜索', en: 'Custom Search' }],
      services: [
        {
          kind: 'custom-lua',
          title: { 'zh-CN': '自定义搜索', en: 'Custom Search' },
          description: { 'zh-CN': '外部插件', en: 'External plugin' },
          settings: {
            type: 'object',
            properties: {
              strict: {
                type: 'boolean',
                title: { 'zh-CN': '严格' },
                default: true,
              },
            },
          },
          capabilities: { maxResults: 10, supportsCheck: true },
          values: { strict: true },
          secretsSet: [],
          ready: true,
        },
      ],
    })

    const checked = fromProtoSearchCheckResult(
      create(CheckSearchSettingsResponseSchema, {
        ok: false,
        status: 401,
        message: { values: { 'zh-CN': '密钥无效', en: 'Invalid key' } },
      }),
    )
    expect(checked).toEqual({
      ok: false,
      status: 401,
      message: { 'zh-CN': '密钥无效', en: 'Invalid key' },
    })
  })

  it('converts model settings, bigint token limits, empty efforts, and localized problems', () => {
    const req = toProtoSaveModelSettingsRequest({
      providers: [
        {
          id: 'p1',
          kind: 'openai-compatible',
          name: 'OpenAI',
          settings: { apiKey: null, baseUrl: 'https://example.test/v1' },
          headers: [{ name: 'X-Test', value: '' }],
          extraBody: { temperature: 0.2 },
          timeoutSeconds: 90,
        },
      ],
      models: [
        {
          id: 'm1',
          provider: 'p1',
          model: 'gpt-4o',
          contextTokens: 128000,
          maxTokens: 16384,
          images: false,
          efforts: [],
          price: { currency: 'USD', input: 2.5, output: 10 },
        },
      ],
      roles: {
        thread: { model: 'm1', effort: 'high' },
        coordinator: { model: null, effort: null },
        triage: { model: null, effort: null },
        research: { model: null, effort: 'low' },
      },
      showCost: true,
    })

    expect(req.models[0]?.contextTokens).toBe(128000n)
    expect(req.models[0]?.maxTokens).toBe(16384n)
    expect(req.models[0]?.efforts).toEqual({ values: [] })
    expect(req.roles.coordinator).toEqual({})

    const protoSettings = create(ModelSettingsSchema, {
      kinds: [],
      providers: [
        {
          id: 'p1',
          kind: 'openai-compatible',
          name: 'OpenAI',
          settings: { baseUrl: 'https://example.test/v1' },
          secretsSet: ['apiKey'],
          headers: ['X-Test'],
          extraBody: { temperature: 0.2 },
          timeoutSeconds: 90,
        },
      ],
      models: [
        {
          id: 'm1',
          provider: 'p1',
          model: 'gpt-4o',
          contextTokens: 128000n,
          maxTokens: 16384n,
          images: false,
          efforts: { values: [] },
        },
      ],
      roles: {
        thread: { model: 'm1', effort: 'high' },
      },
      showCost: true,
    })
    const decoded = fromProtoModelSettings(protoSettings)
    expect(decoded.models[0]).toEqual({
      id: 'm1',
      provider: 'p1',
      model: 'gpt-4o',
      contextTokens: 128000,
      maxTokens: 16384,
      images: false,
      efforts: [],
    })
    expect(decoded.roles.coordinator).toEqual({ model: null, effort: null })

    const defaultEffortReq = toProtoSaveModelSettingsRequest({
      providers: [],
      models: [],
      roles: {
        thread: { model: null, effort: null },
        coordinator: { model: null, effort: 'default' },
        triage: { model: null, effort: null },
        research: { model: null, effort: null },
      },
      showCost: false,
    })
    expect(defaultEffortReq.roles.coordinator).toEqual({ effort: 'default' })
    const decodedDefaultCoordinator = fromProtoModelSettings(
      create(ModelSettingsSchema, {
        roles: { coordinator: { effort: 'default' } },
      }),
    )
    expect(decodedDefaultCoordinator.roles.coordinator).toEqual({ model: null, effort: 'default' })

    const problems = fromProtoModelProblems([
      create(ModelProblemSchema, {
        model: 'm1',
        provider: 'p1',
        problem: create(LocalizedErrorSchema, {
          key: 'error.modelCheckTimeout',
          params: {
            seconds: { value: { case: 'numberValue', value: 20 } },
          },
        }),
      }),
    ])
    expect(problems).toEqual([
      {
        model: 'm1',
        provider: 'p1',
        problem: { key: 'error.modelCheckTimeout', params: { seconds: 20 } },
      },
    ])
  })

  it('maps plugin records, plugin config, update status, and link previews', () => {
    const record = fromProtoPluginRecord(
      create(PluginRecordSchema, {
        id: 'github',
        name: { values: { 'zh-CN': 'GitHub' } },
        source: 'builtin',
        status: 'loaded',
        configurable: true,
        provides: { contexts: ['github'], links: ['github'] },
      }),
    )
    expect(record).toEqual({
      id: 'github',
      name: { 'zh-CN': 'GitHub' },
      source: 'builtin',
      path: null,
      status: 'loaded',
      error: null,
      configurable: true,
      provides: {
        models: [],
        contexts: ['github'],
        dashboards: [],
        searches: [],
        links: ['github'],
      },
    })

    const disabledRecord = fromProtoPluginRecord(
      create(PluginRecordSchema, {
        id: 'model-anthropic',
        name: { values: { 'zh-CN': 'Anthropic 接口' } },
        source: 'builtin',
        status: 'disabled',
        provides: { models: ['anthropic'] },
        usage: {
          modelProviders: ['Claude'],
          modelRoles: ['thread'],
          searches: [],
          projects: [],
        },
      }),
    )
    expect(disabledRecord.status).toBe('disabled')
    expect(disabledRecord.provides.models).toEqual(['anthropic'])
    expect(disabledRecord.usage).toEqual({
      modelProviders: ['Claude'],
      modelRoles: ['thread'],
      searches: [],
      projects: [],
    })

    const cfg = fromProtoPluginConfig(
      create(PluginConfigSchema, {
        name: { values: { 'zh-CN': 'GitHub' } },
        schema: { type: 'object', properties: {} },
        fieldOrder: ['token'],
        values: { host: 'github.com' },
        secretsSet: ['token'],
        ready: true,
      }),
    )
    expect(cfg.ready).toBe(true)
    expect(cfg.fieldOrder).toEqual(['token'])
    expect(cfg.values).toEqual({ host: 'github.com' })

    const updateStatus = fromProtoUpdateStatus(
      create(UpdateStatusSchema, {
        version: '0.1.0',
        preferences: { channel: 'preview', automatic: true },
        canInstall: true,
        state: 'available',
        downloadPercent: 0,
        release: {
          version: '0.2.0',
          notes: 'notes',
          url: 'https://example.test/release',
          prerelease: true,
          installable: true,
        },
      }),
    )
    expect(updateStatus.release?.version).toBe('0.2.0')
    expect(updateStatus.preferences).toEqual({ channel: 'preview', automatic: true })

    const pr = fromProtoLinkPreview(
      create(LinkPreviewSchema, {
        kind: 'pull-request',
        repository: 'daidr/Ariadne',
        number: 42,
        url: 'https://github.com/daidr/Ariadne/pull/42',
        title: 'Migrate Domain 1',
        state: 'open',
        draft: false,
        conflicted: true,
        author: 'daidr',
        additions: 120,
        deletions: 30,
        changedFiles: 5,
        at: '2026-11-22T00:00:00Z',
      }),
    )
    expect(pr).toEqual({
      kind: 'pull-request',
      repository: 'daidr/Ariadne',
      number: 42,
      url: 'https://github.com/daidr/Ariadne/pull/42',
      title: 'Migrate Domain 1',
      state: 'open',
      draft: false,
      conflicted: true,
      author: 'daidr',
      additions: 120,
      deletions: 30,
      changedFiles: 5,
      at: '2026-11-22T00:00:00Z',
    })

    const issue = fromProtoLinkPreview(
      create(LinkPreviewSchema, {
        kind: 'issue',
        repository: 'daidr/Ariadne',
        number: 7,
        url: 'https://github.com/daidr/Ariadne/issues/7',
        title: 'Bug report',
        state: 'closed',
        stateReason: 'completed',
        labels: ['bug'],
      }),
    )
    expect(issue).toEqual({
      kind: 'issue',
      repository: 'daidr/Ariadne',
      number: 7,
      url: 'https://github.com/daidr/Ariadne/issues/7',
      title: 'Bug report',
      state: 'closed',
      stateReason: 'completed',
      labels: ['bug'],
    })
  })

  it('calls PluginService.DeletePlugin and maps UpdateService busy error to ApiError(409)', async () => {
    let deleteUrl = ''
    const deleteFetch: RpcFetch = async (input) => {
      deleteUrl = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url
      const payload = toBinary(
        DeletePluginResponseSchema,
        create(DeletePluginResponseSchema, { plugins: [] }),
      )
      return new Response(new Uint8Array(payload), {
        status: 200,
        headers: { 'content-type': 'application/proto' },
      })
    }
    const pluginClient = createRpcClient(
      PluginService,
      createAriadneTransport({ baseUrl: 'http://localhost/api/rpc', fetch: deleteFetch }),
    )
    const deleted = await pluginClient.deletePlugin({ path: '/tmp/plugin.lua' })
    expect(deleteUrl).toBe('http://localhost/api/rpc/ariadne.v1.PluginService/DeletePlugin')
    expect(deleted.plugins).toEqual([])

    const busyDetail = toBinary(
      LocalizedErrorSchema,
      create(LocalizedErrorSchema, { key: 'updates.error.busy' }),
    )
    const busyFetch: RpcFetch = async () =>
      new Response(
        JSON.stringify({
          code: 'aborted',
          message: 'updates.error.busy',
          details: [
            {
              type: LocalizedErrorSchema.typeName,
              value: Buffer.from(busyDetail).toString('base64'),
            },
          ],
        }),
        {
          status: 409,
          headers: { 'content-type': 'application/json' },
        },
      )
    const updateClient = createRpcClient(
      UpdateService,
      createAriadneTransport({ baseUrl: 'http://localhost/api/rpc', fetch: busyFetch }),
    )
    try {
      await updateClient.installUpdate({ version: '1.0.0' })
      expect.unreachable('expected busy error')
    } catch (error) {
      expect(error).toBeInstanceOf(ApiError)
      const apiErr = error as ApiError
      expect(apiErr.status).toBe(409)
      expect(apiErr.localized).toEqual({ key: 'updates.error.busy' })
    }
  })
})
