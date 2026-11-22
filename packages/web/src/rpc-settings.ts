import type {
  DirectoryListing,
  LinkPreview,
  LinkSummary,
  LocalizedText,
  SettingField,
  SettingsSchema,
} from '@ariadne/plugin-sdk'
import type {
  CheckSearchSettingsResponse as ProtoCheckSearchSettingsResponse,
  ConcurrencySettings as ProtoConcurrencySettings,
  DirectoryListing as ProtoDirectoryListing,
  FoundSetup as ProtoFoundSetup,
  InputSettings as ProtoInputSettings,
  LinkPreview as ProtoLinkPreview,
  LinkSummary as ProtoLinkSummary,
  ListedModel as ProtoListedModel,
  LocalizedText as ProtoLocalizedText,
  McpServerStatus as ProtoMcpServerStatus,
  McpServerView as ProtoMcpServerView,
  ModelEntry as ProtoModelEntry,
  ModelInfo as ProtoModelInfo,
  ModelKind as ProtoModelKind,
  ModelPrice as ProtoModelPrice,
  ModelProblem as ProtoModelProblem,
  ModelProviderView as ProtoModelProviderView,
  ModelRoles as ProtoModelRoles,
  ModelSettings as ProtoModelSettings,
  PluginConfig as ProtoPluginConfig,
  PluginRecord as ProtoPluginRecord,
  RoleConfig as ProtoRoleConfig,
  SearchCapabilities as ProtoSearchCapabilities,
  SearchSettings as ProtoSearchSettings,
  SettingField as ProtoSettingField,
  SettingsSchema as ProtoSettingsSchema,
  SourceKind as ProtoSourceKind,
  UpdateStatus as ProtoUpdateStatus,
  WorkLimits as ProtoWorkLimits,
  WorkSettings as ProtoWorkSettings,
} from '@ariadne/proto'
import type { JsonObject, JsonValue } from '@bufbuild/protobuf'
import type { Value } from '@bufbuild/protobuf/wkt'
import type {
  CommandAction,
  Concurrency,
  Currency,
  Effort,
  FoundSetup,
  InputSettings,
  Limits,
  ListedModel,
  McpServerInput,
  McpServerStatus,
  McpServerView,
  McpToolMark,
  ModelEntry,
  ModelInfo,
  ModelKind,
  ModelPrice,
  ModelProblem,
  ModelProviderInput,
  ModelSettings,
  ModelSettingsInput,
  PluginConfig,
  PluginRecord,
  RoleInput,
  SearchCapabilities,
  SearchCheckResult,
  SearchSettings,
  SearchSettingsInput,
  ShortcutAction,
  SourceKind,
  UpdateStatus,
  WorkSettings,
} from './api'
import { ApiError, decodeLocalizedError } from './rpc'

const SHORTCUT_KEYS: readonly ShortcutAction[] = [
  'search',
  'focus',
  'previousThread',
  'nextThread',
  'stop',
  'sheet',
]

function toJsonValue(value: unknown): JsonValue | undefined {
  if (value === undefined) return undefined
  if (value === null || typeof value === 'string' || typeof value === 'boolean') return value
  if (typeof value === 'number') return Number.isFinite(value) ? value : null
  if (Array.isArray(value)) {
    const out: JsonValue[] = []
    for (const item of value) {
      const converted = toJsonValue(item)
      if (converted !== undefined) out.push(converted)
    }
    return out
  }
  if (typeof value === 'object') {
    const out: JsonObject = {}
    for (const [key, entry] of Object.entries(value as Record<string, unknown>)) {
      const converted = toJsonValue(entry)
      if (converted !== undefined) out[key] = converted
    }
    return out
  }
  return undefined
}

export function toJsonObject(values?: Record<string, unknown>): JsonObject {
  const out: JsonObject = {}
  if (!values) return out
  for (const [key, value] of Object.entries(values)) {
    const converted = toJsonValue(value)
    if (converted !== undefined) out[key] = converted
  }
  return out
}

export function fromJsonObject(values?: JsonObject): Record<string, unknown> {
  if (!values) return {}
  return structuredClone(values)
}

function fromProtoLocalizedText(text?: ProtoLocalizedText): LocalizedText {
  const values = text?.values ?? {}
  return {
    'zh-CN': values['zh-CN'] ?? '',
    ...values,
  }
}

function fromProtoValue(value?: Value): string | number | boolean | undefined {
  if (!value) return undefined
  switch (value.kind.case) {
    case 'stringValue':
    case 'numberValue':
    case 'boolValue':
      return value.kind.value
    default:
      return undefined
  }
}

function fromProtoSettingField(field: ProtoSettingField): SettingField {
  const title = fromProtoLocalizedText(field.title)
  const description = field.description ? fromProtoLocalizedText(field.description) : undefined
  const defaultValue = fromProtoValue(field.defaultValue)
  const common = {
    title,
    ...(description ? { description } : {}),
  }
  switch (field.type) {
    case 'number':
      return {
        type: 'number',
        ...common,
        ...(typeof defaultValue === 'number' ? { default: defaultValue } : {}),
      }
    case 'boolean':
      return {
        type: 'boolean',
        ...common,
        ...(typeof defaultValue === 'boolean' ? { default: defaultValue } : {}),
      }
    case 'enum':
    case 'select':
      return {
        type: 'enum',
        ...common,
        options: [...field.options],
        ...(typeof defaultValue === 'string' ? { default: defaultValue } : {}),
      }
    default:
      return {
        type: 'string',
        ...common,
        ...(field.secret ? { secret: true } : {}),
        ...(field.directory ? { directory: true } : {}),
        ...(field.networkOrigin ? { networkOrigin: true } : {}),
        ...(typeof defaultValue === 'string' ? { default: defaultValue } : {}),
      }
  }
}

export function fromProtoSettingsSchema(schema?: ProtoSettingsSchema): SettingsSchema {
  const properties: Record<string, SettingField> = {}
  if (schema) {
    const seen = new Set<string>()
    for (const key of schema.fieldOrder) {
      const field = schema.properties[key]
      if (field && !seen.has(key)) {
        properties[key] = fromProtoSettingField(field)
        seen.add(key)
      }
    }
    for (const [key, field] of Object.entries(schema.properties)) {
      if (!seen.has(key)) {
        properties[key] = fromProtoSettingField(field)
      }
    }
  }
  return {
    type: 'object',
    properties,
    ...(schema && schema.required.length > 0 ? { required: [...schema.required] } : {}),
  }
}

export function fromProtoConcurrency(value?: ProtoConcurrencySettings): Concurrency {
  if (!value)
    throw new ApiError(500, { key: 'error.invalidBody', params: { reason: 'concurrency' } })
  return {
    slots: value.slots,
    reserved: value.reserved,
  }
}

export function fromProtoInputSettings(value?: ProtoInputSettings): InputSettings {
  if (!value) throw new ApiError(500, { key: 'error.invalidBody', params: { reason: 'input' } })
  const keys = {} as Record<ShortcutAction, string>
  for (const action of SHORTCUT_KEYS) {
    keys[action] = value.keys[action] ?? ''
  }
  return {
    submitOn: value.submitOn === 'mod-enter' ? 'mod-enter' : 'enter',
    snippets: value.snippets.map((snippet) => ({
      id: snippet.id,
      name: snippet.name,
      text: snippet.text,
    })),
    keys,
  }
}

function fromProtoWorkLimits(value?: ProtoWorkLimits): Limits {
  return {
    commandTimeout: value?.commandTimeout ?? 0,
    commandTimeoutMax: value?.commandTimeoutMax ?? 0,
    commandOutput: value?.commandOutput ?? 0,
    compactShare: value?.compactShare ?? 0,
    modelRetries: value?.modelRetries ?? 0,
    loopRepeats: value?.loopRepeats ?? 0,
  }
}

export function fromProtoWorkSettings(value?: ProtoWorkSettings): WorkSettings {
  if (!value) throw new ApiError(500, { key: 'error.invalidBody', params: { reason: 'work' } })
  return {
    instructions: value.instructions,
    rules: value.rules.map((rule) => ({
      pattern: rule.pattern,
      action: rule.action as CommandAction,
    })),
    limits: fromProtoWorkLimits(value.limits),
    defaults: fromProtoWorkLimits(value.defaults),
  }
}

function fromProtoSearchCapabilities(
  value?: ProtoSearchCapabilities,
): SearchCapabilities | undefined {
  if (!value) return undefined
  return {
    ...(value.maxQueryRunes ? { maxQueryRunes: value.maxQueryRunes } : {}),
    ...(value.maxResults ? { maxResults: value.maxResults } : {}),
    ...(value.returnsAnswer ? { returnsAnswer: true } : {}),
    ...(value.supportsCheck ? { supportsCheck: true } : {}),
  }
}

export function fromProtoSearchSettings(value?: ProtoSearchSettings): SearchSettings {
  if (!value) throw new ApiError(500, { key: 'error.invalidBody', params: { reason: 'search' } })
  return {
    provider: value.provider,
    ...(value.disabledProvider ? { disabledProvider: value.disabledProvider } : {}),
    ready: value.ready,
    keySet: value.keySet,
    needed: value.needed,
    models: [...value.models],
    plugins: value.plugins.map(fromProtoLocalizedText),
    services: value.services.map((service) => {
      const capabilities = fromProtoSearchCapabilities(service.capabilities)
      return {
        kind: service.kind,
        title: fromProtoLocalizedText(service.title),
        ...(service.description
          ? { description: fromProtoLocalizedText(service.description) }
          : {}),
        settings: fromProtoSettingsSchema(service.settings),
        ...(capabilities ? { capabilities } : {}),
        values: fromJsonObject(service.values),
        secretsSet: [...service.secretsSet],
        ready: service.ready,
      }
    }),
  }
}

export function fromProtoSearchCheckResult(
  response: ProtoCheckSearchSettingsResponse,
): SearchCheckResult {
  return {
    ok: response.ok,
    status: response.status ?? null,
    ...(response.message ? { message: fromProtoLocalizedText(response.message) } : {}),
  }
}

export function toProtoSearchInput(input: SearchSettingsInput) {
  const services: Record<string, JsonObject> = {}
  for (const [kind, entry] of Object.entries(input.services)) {
    if (!entry) continue
    services[kind] = toJsonObject(entry)
  }
  return {
    ...(input.provider !== undefined ? { provider: input.provider } : {}),
    services,
  }
}

function fromProtoModelPrice(price?: ProtoModelPrice): ModelPrice | undefined {
  if (!price) return undefined
  return {
    currency: (price.currency === 'CNY' ? 'CNY' : 'USD') as Currency,
    input: price.input,
    output: price.output,
    ...(price.cacheRead !== undefined ? { cacheRead: price.cacheRead } : {}),
    ...(price.cacheWrite !== undefined ? { cacheWrite: price.cacheWrite } : {}),
  }
}

function toProtoModelPrice(price?: ModelPrice) {
  if (!price) return undefined
  return {
    currency: price.currency,
    input: price.input,
    output: price.output,
    ...(price.cacheRead !== undefined ? { cacheRead: price.cacheRead } : {}),
    ...(price.cacheWrite !== undefined ? { cacheWrite: price.cacheWrite } : {}),
  }
}

function fromProtoModelKind(kind: ProtoModelKind): ModelKind {
  return {
    kind: kind.kind,
    title: fromProtoLocalizedText(kind.title),
    settings: fromProtoSettingsSchema(kind.settings),
    ...(kind.program ? { program: true } : {}),
  }
}

function fromProtoModelProviderView(
  provider: ProtoModelProviderView,
): ModelSettings['providers'][number] {
  return {
    id: provider.id,
    kind: provider.kind,
    name: provider.name,
    settings: fromJsonObject(provider.settings),
    secretsSet: [...provider.secretsSet],
    headers: [...provider.headers],
    extraBody: fromJsonObject(provider.extraBody),
    timeoutSeconds: provider.timeoutSeconds ?? null,
  }
}

function fromProtoModelEntry(entry: ProtoModelEntry): ModelEntry {
  const price = fromProtoModelPrice(entry.price)
  return {
    id: entry.id,
    provider: entry.provider,
    model: entry.model,
    ...(entry.contextTokens !== undefined ? { contextTokens: Number(entry.contextTokens) } : {}),
    ...(entry.note !== undefined ? { note: entry.note } : {}),
    ...(entry.images === false ? { images: false } : {}),
    ...(entry.maxTokens !== undefined ? { maxTokens: Number(entry.maxTokens) } : {}),
    ...(price ? { price } : {}),
    ...(entry.efforts !== undefined ? { efforts: [...entry.efforts.values] as Effort[] } : {}),
  }
}

function fromProtoRole(role?: ProtoRoleConfig): RoleInput {
  return {
    model: role?.model ?? null,
    effort: (role?.effort as Effort | undefined) ?? null,
    fallback: role?.fallback ?? null,
  }
}

function toProtoRole(role: RoleInput) {
  return {
    ...(role.model !== null ? { model: role.model } : {}),
    ...(role.effort !== null ? { effort: role.effort } : {}),
    ...(role.fallback !== null ? { fallback: role.fallback } : {}),
  }
}

function fromProtoRoles(roles?: ProtoModelRoles): ModelSettings['roles'] {
  return {
    thread: fromProtoRole(roles?.thread),
    coordinator: fromProtoRole(roles?.coordinator),
    triage: fromProtoRole(roles?.triage),
    research: fromProtoRole(roles?.research),
  }
}

export function fromProtoModelSettings(settings?: ProtoModelSettings): ModelSettings {
  if (!settings) throw new ApiError(500, { key: 'error.invalidBody', params: { reason: 'models' } })
  return {
    kinds: settings.kinds.map(fromProtoModelKind),
    providers: settings.providers.map(fromProtoModelProviderView),
    models: settings.models.map(fromProtoModelEntry),
    roles: fromProtoRoles(settings.roles),
    showCost: settings.showCost,
  }
}

export function toProtoSaveModelSettingsRequest(input: ModelSettingsInput) {
  return {
    providers: input.providers.map((provider: ModelProviderInput) => ({
      id: provider.id,
      kind: provider.kind,
      name: provider.name,
      settings: toJsonObject(provider.settings),
      headers: (provider.headers ?? []).map((header) => ({
        name: header.name,
        value: header.value,
      })),
      extraBody: toJsonObject(provider.extraBody),
      ...(provider.timeoutSeconds !== undefined && provider.timeoutSeconds !== null
        ? { timeoutSeconds: provider.timeoutSeconds }
        : {}),
    })),
    models: input.models.map((entry: ModelEntry) => ({
      id: entry.id,
      provider: entry.provider,
      model: entry.model,
      ...(entry.contextTokens !== undefined
        ? { contextTokens: BigInt(Math.trunc(entry.contextTokens)) }
        : {}),
      ...(entry.note !== undefined ? { note: entry.note } : {}),
      ...(entry.images !== undefined ? { images: entry.images } : {}),
      ...(entry.maxTokens !== undefined ? { maxTokens: BigInt(Math.trunc(entry.maxTokens)) } : {}),
      ...(entry.price ? { price: toProtoModelPrice(entry.price) } : {}),
      ...(entry.efforts !== undefined ? { efforts: { values: [...entry.efforts] } } : {}),
    })),
    roles: {
      thread: toProtoRole(input.roles.thread),
      coordinator: toProtoRole(input.roles.coordinator),
      triage: toProtoRole(input.roles.triage),
      research: toProtoRole(input.roles.research),
    },
    showCost: input.showCost,
  }
}

export function fromProtoModelProblems(problems: ProtoModelProblem[]): ModelProblem[] {
  return problems.map((item) => ({
    model: item.model,
    provider: item.provider,
    problem: decodeLocalizedError(item.problem) ?? {
      key: 'error.modelCheck',
      params: { detail: 'unknown' },
    },
  }))
}

export function fromProtoModelInfo(info?: ProtoModelInfo): ModelInfo {
  if (!info) return {}
  const price = fromProtoModelPrice(info.price)
  return {
    ...(info.contextTokens !== undefined ? { contextTokens: Number(info.contextTokens) } : {}),
    ...(info.maxTokens !== undefined ? { maxTokens: Number(info.maxTokens) } : {}),
    ...(info.images !== undefined ? { images: info.images } : {}),
    ...(price ? { price } : {}),
    ...(info.tools !== undefined ? { tools: info.tools } : {}),
    ...(info.released !== undefined ? { released: info.released } : {}),
    ...(info.efforts !== undefined ? { efforts: [...info.efforts.values] as Effort[] } : {}),
  }
}

export function fromProtoListedModel(model: ProtoListedModel): ListedModel {
  return {
    id: model.id,
    ...(model.title !== undefined ? { title: model.title } : {}),
    ...(model.info ? { info: fromProtoModelInfo(model.info) } : {}),
  }
}

export function fromProtoFoundSetup(setup: ProtoFoundSetup): FoundSetup {
  return {
    id: setup.id,
    kind: setup.kind,
    title: fromProtoLocalizedText(setup.title),
    from:
      setup.source.case === 'env'
        ? { env: setup.source.value }
        : { program: setup.source.case === 'program' ? setup.source.value : '' },
  }
}

export function fromProtoMcpServerView(server: ProtoMcpServerView): McpServerView {
  const tools: Record<string, McpToolMark> = {}
  for (const [name, mark] of Object.entries(server.tools)) {
    tools[name] = mark as McpToolMark
  }
  return {
    id: server.id,
    name: server.name,
    transport: server.transport === 'http' ? 'http' : 'stdio',
    ...(server.command !== undefined ? { command: server.command } : {}),
    ...(server.args.length > 0 ? { args: [...server.args] } : {}),
    ...(server.cwd !== undefined ? { cwd: server.cwd } : {}),
    ...(server.url !== undefined ? { url: server.url } : {}),
    env: [...server.env],
    headers: [...server.headers],
    tools,
  }
}

export function toProtoMcpServerInput(server: McpServerInput) {
  const tools: Record<string, string> = {}
  for (const [name, mark] of Object.entries(server.tools)) {
    tools[name] = mark
  }
  return {
    id: server.id,
    name: server.name,
    transport: server.transport,
    ...(server.command !== undefined ? { command: server.command } : {}),
    args: server.args ? [...server.args] : [],
    ...(server.cwd !== undefined ? { cwd: server.cwd } : {}),
    ...(server.url !== undefined ? { url: server.url } : {}),
    env: server.env.map((entry) => ({ name: entry.name, value: entry.value })),
    headers: server.headers.map((entry) => ({ name: entry.name, value: entry.value })),
    tools,
  }
}

export function fromProtoMcpServerStatus(status?: ProtoMcpServerStatus): McpServerStatus {
  if (!status) throw new ApiError(500, { key: 'error.invalidBody', params: { reason: 'mcp' } })
  return {
    id: status.id,
    state: status.state as McpServerStatus['state'],
    error: status.error ?? null,
    log: status.log ?? null,
    info: status.info
      ? {
          name: status.info.name,
          version: status.info.version,
          protocolVersion: status.info.protocolVersion,
        }
      : null,
    tools: status.tools
      ? status.tools.items.map((tool) => ({
          name: tool.name,
          ...(tool.title !== undefined ? { title: tool.title } : {}),
          ...(tool.description !== undefined ? { description: tool.description } : {}),
        }))
      : null,
  }
}

export function fromProtoPluginRecord(record: ProtoPluginRecord): PluginRecord {
  return {
    id: record.id ?? null,
    name: record.name ? fromProtoLocalizedText(record.name) : null,
    source: record.source === 'builtin' ? 'builtin' : 'path',
    path: record.path ?? null,
    status:
      record.status === 'loaded' ? 'loaded' : record.status === 'disabled' ? 'disabled' : 'failed',
    error: record.error ?? null,
    ...(record.configurable ? { configurable: true } : {}),
    provides: {
      models: [...(record.provides?.models ?? [])],
      contexts: [...(record.provides?.contexts ?? [])],
      dashboards: [...(record.provides?.dashboards ?? [])],
      searches: [...(record.provides?.searches ?? [])],
      links: [...(record.provides?.links ?? [])],
    },
    ...(record.usage
      ? {
          usage: {
            modelProviders: [...record.usage.modelProviders],
            modelRoles: [...record.usage.modelRoles],
            searches: [...record.usage.searches],
            projects: [...record.usage.projects],
          },
        }
      : {}),
  }
}

export function fromProtoPluginConfig(config?: ProtoPluginConfig): PluginConfig {
  if (!config) throw new ApiError(500, { key: 'error.invalidBody', params: { reason: 'plugin' } })
  return {
    name: fromProtoLocalizedText(config.name),
    schema: fromProtoSettingsSchema(config.schema),
    ...(config.fieldOrder.length > 0 ? { fieldOrder: [...config.fieldOrder] } : {}),
    values: fromJsonObject(config.values),
    secretsSet: [...config.secretsSet],
    ready: config.ready,
  }
}

export function fromProtoUpdateStatus(status?: ProtoUpdateStatus): UpdateStatus {
  if (!status) throw new ApiError(500, { key: 'error.invalidBody', params: { reason: 'updates' } })
  return {
    version: status.version,
    preferences: {
      channel: status.preferences?.channel === 'preview' ? 'preview' : 'stable',
      automatic: status.preferences?.automatic ?? false,
    },
    canInstall: status.canInstall,
    state: status.state as UpdateStatus['state'],
    downloadPercent: status.downloadPercent,
    ...(status.failure ? { failure: status.failure as UpdateStatus['failure'] } : {}),
    release: status.release
      ? {
          version: status.release.version,
          notes: status.release.notes,
          url: status.release.url,
          prerelease: status.release.prerelease,
          installable: status.release.installable,
        }
      : null,
  }
}

export function fromProtoSourceKind(kind: ProtoSourceKind): SourceKind {
  const defaults = fromJsonObject(kind.defaults)
  return {
    kind: kind.kind,
    title: fromProtoLocalizedText(kind.title),
    settings: fromProtoSettingsSchema(kind.settings),
    ...(kind.plugin ? { plugin: { id: kind.plugin.id, ready: kind.plugin.ready } } : {}),
    ...(Object.keys(defaults).length > 0 ? { defaults } : {}),
    provides: [...kind.provides],
    remote: kind.remote,
    hosting: kind.hosting,
    browse: kind.browse,
    summary: kind.summary ?? null,
  }
}

export function fromProtoDirectoryListing(
  listing?: ProtoDirectoryListing,
): DirectoryListing | null {
  if (!listing) return null
  return {
    path: listing.path,
    parent: listing.parent ?? null,
    directories: listing.directories.map((entry) => ({
      name: entry.name,
      path: entry.path,
    })),
  }
}

export function fromProtoLinkSummary(summary: ProtoLinkSummary): LinkSummary {
  return {
    kind: summary.kind === 'issue' ? 'issue' : 'pull-request',
    label: summary.label,
    ...(summary.reference !== undefined ? { reference: summary.reference } : {}),
    ...(summary.repositoryUrl !== undefined ? { repositoryUrl: summary.repositoryUrl } : {}),
  }
}

export function fromProtoLinkPreview(preview?: ProtoLinkPreview): LinkPreview | null {
  if (!preview) return null
  if (preview.kind === 'issue') {
    return {
      kind: 'issue',
      repository: preview.repository,
      number: preview.number,
      url: preview.url,
      title: preview.title,
      state: preview.state === 'closed' ? 'closed' : 'open',
      ...(preview.stateReason !== undefined ? { stateReason: preview.stateReason } : {}),
      ...(preview.labels.length > 0 ? { labels: [...preview.labels] } : {}),
      ...(preview.updatedAt !== undefined ? { updatedAt: preview.updatedAt } : {}),
    }
  }
  return {
    kind: 'pull-request',
    repository: preview.repository,
    number: preview.number,
    ...(preview.reference !== undefined ? { reference: preview.reference } : {}),
    url: preview.url,
    title: preview.title,
    state: (preview.state === 'merged' || preview.state === 'closed' ? preview.state : 'open') as
      'open' | 'closed' | 'merged',
    draft: preview.draft,
    ...(preview.conflicted ? { conflicted: true } : {}),
    author: preview.author ?? null,
    additions: preview.additions ?? null,
    deletions: preview.deletions ?? null,
    changedFiles: preview.changedFiles ?? null,
    at: preview.at,
  }
}
