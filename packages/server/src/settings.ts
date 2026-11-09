import { CURRENCIES, DomainError, type Currency } from '@ariadne/core'
import {
  COMMAND_ACTIONS,
  compactAt,
  DEFAULT_COMMAND_RULES,
  DEFAULT_LIMITS,
  forgetRefusedSearches,
  keyedSearch,
  LIMIT_RANGES,
  MCP_TOOL_MARKS,
  searchingModels,
  REFERENCE_SECRET,
  referenceSecretPrefix,
  type CommandAction,
  type CommandRule,
  type Limits,
  type McpServerConfig,
  type McpToolMark,
  type ModelPrice,
  type ModelChoice,
  type ModelRequestOptions,
  type PluginHost,
  type ResolvedModel,
  type TurnSettings,
} from '@ariadne/agent'
import type { Locale } from '@ariadne/i18n'
import {
  EFFORTS,
  type Effort,
  type FoundFrom,
  type LocalizedText,
  type ModelProvider,
  type SearchProvider,
  type SettingsSchema,
} from '@ariadne/plugin-sdk'
import {
  chmodSync,
  existsSync,
  readFileSync,
  realpathSync,
  renameSync,
  writeFileSync,
} from 'node:fs'
import { homedir } from 'node:os'
import { dirname, join, relative, resolve } from 'node:path'
import { BadRequest } from './http'
import type { ModelInfo } from './prices'

/** A setup found on this machine that is not among the providers yet. */
export interface FoundSetup {
  /** The kind and the kind's own id for it, e.g. `anthropic:ANTHROPIC_API_KEY`. */
  id: string
  kind: string
  /** The service's name when the setup has one, else its kind's title. */
  title: LocalizedText
  from: FoundFrom
  /** As found, secrets included: it never leaves the server. */
  settings: Record<string, unknown>
}

/** What a provider talks to its service with, to tell whether that changed. */
const talksWith = (p: ModelProviderConfig) => JSON.stringify([p.kind, p.settings, p.request])

/** What a model's settings take from the catalog. */
function infoFields(info: ModelInfo | undefined): Partial<ModelConfig> {
  if (!info) return {}
  return {
    ...(info.contextTokens ? { contextTokens: info.contextTokens } : {}),
    ...(info.images === false ? { images: false as const } : {}),
    ...(info.maxTokens ? { maxTokens: info.maxTokens } : {}),
    ...(info.price ? { price: info.price } : {}),
  }
}

export interface ModelProviderConfig {
  id: string
  kind: string
  name: string
  settings: Record<string, unknown>
  /** Added to every request: headers (which may be secret), body fields, a timeout. */
  request?: {
    headers?: Record<string, string>
    extraBody?: Record<string, unknown>
    timeoutSeconds?: number
  }
}

/**
 * One model the user set up, on one of their providers. `contextTokens` is how much it
 * reads at most; older history is summarised before a conversation gets near it. `note`
 * is what it is for, in the user's words: the coordinator reads it when it picks a model
 * for a thread. Models look at images unless `images` says they cannot.
 */
export interface ModelConfig {
  id: string
  provider: string
  model: string
  contextTokens?: number
  note?: string
  /** `false` when the model cannot look at images; it is told about them in words. */
  images?: false
  /** The most it may write in one response; the provider's default without. */
  maxTokens?: number
  /** What it costs per million tokens, for estimates; none without. */
  price?: ModelPrice
}

/** The most a price per million tokens may be: more is a typo. */
const MAX_PRICE = 10_000

/** A price as the page sent it, checked; `null` for none. */
function parsePrice(raw: unknown, at: string): ModelPrice | null {
  if (raw === undefined || raw === null) return null
  const price = raw as Record<string, unknown>
  if (!CURRENCIES.includes(price.currency as Currency)) throw new BadRequest(`${at}.currency`)
  const amount = (key: string, required: boolean): number | undefined => {
    const value = price[key]
    if ((value === undefined || value === null) && !required) return undefined
    if (typeof value !== 'number' || !Number.isFinite(value) || value < 0 || value > MAX_PRICE) {
      throw new BadRequest(`${at}.${key}`)
    }
    return value
  }
  const cacheRead = amount('cacheRead', false)
  const cacheWrite = amount('cacheWrite', false)
  return {
    currency: price.currency as Currency,
    input: amount('input', true)!,
    output: amount('output', true)!,
    ...(cacheRead !== undefined ? { cacheRead } : {}),
    ...(cacheWrite !== undefined ? { cacheWrite } : {}),
  }
}

/**
 * The model a role uses and how hard it thinks there. `effort: null` asks nothing of the
 * model: some reject the parameter. `model: null` on the coordinator uses the threads'
 * model, and on triage the coordinator's.
 */
export interface RoleConfig {
  model: string | null
  effort: Effort | null
  /**
   * What a turn goes on with when the model keeps being turned away (rate limit, overload,
   * server errors) after its retries. Research has none: a failed research is the asking
   * conversation's tool error, and it goes on itself.
   */
  fallback: string | null
}

/**
 * `triage` is the coordinator's quick look at what the user wrote inside a thread, when
 * nothing else waits for it: it only decides whether the request belongs elsewhere.
 * `research` answers a question a conversation hands off with its `research` tool; left
 * unset, the conversation's own model does.
 */
export type Role = 'coordinator' | 'thread' | 'triage' | 'research'
const ROLES: Role[] = ['thread', 'coordinator', 'triage', 'research']

/** A request the user sends often, offered after `/` in the message input. */
export interface Snippet {
  id: string
  name: string
  text: string
}

/** The app's own keys the user can bind elsewhere, by what they do. */
export const SHORTCUT_ACTIONS = [
  'search',
  'focus',
  'previousThread',
  'nextThread',
  'stop',
  'sheet',
] as const
export type ShortcutAction = (typeof SHORTCUT_ACTIONS)[number]

export const DEFAULT_KEYS: Readonly<Record<ShortcutAction, string>> = {
  search: 'mod+k',
  focus: '/',
  previousThread: 'mod+up',
  nextThread: 'mod+down',
  stop: 'escape',
  sheet: '?',
}

/** A single key press: modifiers and one key, as the hotkey syntax writes it. */
const HOTKEY =
  /^((mod|meta|ctrl|alt|shift)\+){0,4}([a-z0-9]|f([1-9]|1[0-2])|escape|enter|tab|space|backspace|delete|up|down|left|right|home|end|pageup|pagedown|[`~!@#$%^&*()\-_=+[\]{}\\|;:'",.<>/?])$/

/** How the message input behaves: which key sends, the requests kept for reuse, the app's keys. */
export interface InputConfig {
  /** `enter` sends on Enter (Shift+Enter breaks the line); `mod-enter` on ⌘/Ctrl+Enter. */
  submitOn: 'enter' | 'mod-enter'
  snippets: Snippet[]
  keys: Record<ShortcutAction, string>
}

function parseKeys(raw: unknown, strict: boolean): Record<ShortcutAction, string> {
  const given = (raw ?? {}) as Record<string, unknown>
  const keys = { ...DEFAULT_KEYS }
  for (const action of SHORTCUT_ACTIONS) {
    const key = given[action]
    if (key === undefined) continue
    if (typeof key === 'string' && HOTKEY.test(key.trim().toLowerCase())) {
      keys[action] = key.trim().toLowerCase()
    } else if (strict) throw new BadRequest(`keys.${action}`)
  }
  if (strict && new Set(Object.values(keys)).size < SHORTCUT_ACTIONS.length) {
    throw new BadRequest('keys')
  }
  return keys
}

const SNIPPET_ID = /^[A-Za-z0-9_-]{1,40}$/
const MAX_SNIPPETS = 200
const MAX_SNIPPET_NAME = 40
const MAX_SNIPPET_TEXT = 20_000

/** Stored snippets that still make sense; a hand-edited file may hold anything. */
function storedSnippets(raw: unknown): Snippet[] {
  if (!Array.isArray(raw)) return []
  const seen = new Set<string>()
  return raw.flatMap((item) => {
    const { id, name, text } = (item ?? {}) as Partial<Snippet>
    if (typeof id !== 'string' || !SNIPPET_ID.test(id) || seen.has(id)) return []
    if (typeof name !== 'string' || typeof text !== 'string' || !name.trim() || !text.trim()) {
      return []
    }
    seen.add(id)
    return [{ id, name, text }]
  })
}

export interface ModelsConfig {
  providers: ModelProviderConfig[]
  models: ModelConfig[]
  /** Threads do the work, so their model comes first; the coordinator may use another. */
  roles: Record<Role, RoleConfig>
  /** Usage also shows what it cost, estimated; tokens only without. */
  showCost: boolean
}

/** A server's id: short, so tool names stay within what model services take. */
const MCP_ID = /^[a-z0-9][a-z0-9-]{0,19}$/
const ENV_NAME = /^[A-Za-z_][A-Za-z0-9_]*$/
/**
 * A project's command variable: no `GIT_CONFIG*` (it would lift the remote push block, which
 * is git configuration given on the command line) and none of Ariadne's own.
 */
const PROJECT_ENV_NAME = /^(?!GIT_CONFIG|ARIADNE_)[A-Za-z_][A-Za-z0-9_]*$/i
const HEADER_NAME = /^[A-Za-z0-9!#$%&'*+.^_`|~-]+$/

const noRole = (): RoleConfig => ({ model: null, effort: null, fallback: null })

function storedRole(raw: unknown): RoleConfig {
  const role = (raw ?? {}) as Partial<RoleConfig>
  return {
    model: typeof role.model === 'string' ? role.model : null,
    effort: EFFORTS.includes(role.effort as Effort) ? (role.effort as Effort) : null,
    fallback: typeof role.fallback === 'string' ? role.fallback : null,
  }
}

export interface UserConfig {
  locale: Locale
  /** Local plugin paths, loaded after the built-in ones. */
  plugins: string[]
  models: ModelsConfig
  concurrency: { slots: number; reserved: number }
  /** Secret settings of each project's context source (e.g. a token), by project id. */
  sourceSecrets: Record<string, Record<string, string>>
  /**
   * How thread commands are isolated, read at start. `off` runs them as they are; `hide`
   * adds paths they must not see to Ariadne's home and the browsers' profiles.
   */
  sandbox: { mode: 'auto' | 'off'; hide: string[] }
  /** External tool servers; each project picks which of them it uses. */
  mcp: { servers: McpServerConfig[] }
  input: InputConfig
  /** The user's own instructions for every project. */
  instructions: string
  /** What thread commands may run without asking, must ask, or may never run. */
  commands: { rules: CommandRule[] }
  limits: Limits
  /** Environment variables of each project's thread commands, by project id; may be secret. */
  projectEnv: Record<string, Record<string, string>>
  /**
   * The key of the search service agents use when no model service searches the web
   * itself; `''` for none.
   */
  search: { apiKey: string }
}

/** The longest a search key may be: more is not a key. */
const MAX_SEARCH_KEY = 200

export const MAX_INSTRUCTIONS = 16_000
const MAX_COMMAND_RULES = 200

function parseRules(raw: unknown, strict: boolean): CommandRule[] {
  if (!Array.isArray(raw)) {
    if (strict) throw new BadRequest('rules')
    return []
  }
  if (strict && raw.length > MAX_COMMAND_RULES) throw new BadRequest('rules')
  return raw.flatMap((item, index) => {
    const { pattern, action } = (item ?? {}) as Partial<CommandRule>
    const text = typeof pattern === 'string' ? pattern.trim() : ''
    if (!text || text.length > 500 || !COMMAND_ACTIONS.includes(action as CommandAction)) {
      if (strict) throw new BadRequest(`rules[${index}]`)
      return []
    }
    return [{ pattern: text, action: action as CommandAction }]
  })
}

function parseLimits(raw: unknown, strict: boolean): Limits {
  const given = (raw ?? {}) as Partial<Record<keyof Limits, unknown>>
  const limits = { ...DEFAULT_LIMITS }
  for (const key of Object.keys(DEFAULT_LIMITS) as (keyof Limits)[]) {
    const value = given[key]
    if (value === undefined || value === null) continue
    const [min, max] = LIMIT_RANGES[key]
    const whole = key !== 'compactShare'
    if (
      typeof value === 'number' &&
      value >= min &&
      value <= max &&
      (!whole || Number.isInteger(value))
    ) {
      limits[key] = value
    } else if (strict) throw new BadRequest(`limits.${key}`)
  }
  if (limits.commandTimeout > limits.commandTimeoutMax) {
    if (strict) throw new BadRequest('limits.commandTimeout')
    limits.commandTimeout = limits.commandTimeoutMax
  }
  return limits
}

/**
 * `{ name, value }` pairs as the page sends them, checked against `pattern`: an empty value
 * keeps the one stored under that name, so the page never needs to know it.
 */
function secretPairs(
  list: unknown,
  pattern: RegExp,
  before: Record<string, string> | undefined,
  at: string,
): Record<string, string> {
  if (list === undefined || list === null) return {}
  if (!Array.isArray(list)) throw new BadRequest(at)
  const out: Record<string, string> = {}
  for (const pair of list as { name?: unknown; value?: unknown }[]) {
    const field = typeof pair?.name === 'string' ? pair.name.trim() : ''
    if (!pattern.test(field) || field in out) throw new BadRequest(at)
    const value = typeof pair.value === 'string' ? pair.value : ''
    const kept = before?.[field]
    if (value) out[field] = value
    else if (kept) out[field] = kept
    else throw new BadRequest(`${at}.${field}`)
  }
  return out
}

const defaults = (): UserConfig => ({
  locale: 'zh-CN',
  plugins: [],
  models: {
    providers: [],
    models: [],
    roles: { thread: noRole(), coordinator: noRole(), triage: noRole(), research: noRole() },
    showCost: false,
  },
  concurrency: { slots: 3, reserved: 1 },
  sourceSecrets: {},
  sandbox: { mode: 'auto', hide: [] },
  mcp: { servers: [] },
  input: { submitOn: 'enter', snippets: [], keys: { ...DEFAULT_KEYS } },
  instructions: '',
  commands: { rules: DEFAULT_COMMAND_RULES.map((rule) => ({ ...rule })) },
  limits: { ...DEFAULT_LIMITS },
  projectEnv: {},
  search: { apiKey: '' },
})

/** The settings stored at `path`, filled in with the defaults. */
export function loadUserConfig(path: string): UserConfig {
  const config = defaults()
  if (!existsSync(path)) return config
  const stored = JSON.parse(readFileSync(path, 'utf8')) as Partial<UserConfig>
  const sandbox = (stored.sandbox ?? {}) as Partial<UserConfig['sandbox']>
  return {
    ...config,
    ...stored,
    // Only the fields this version knows: anything else is not written back.
    models: {
      providers: stored.models?.providers ?? config.models.providers,
      models: stored.models?.models ?? config.models.models,
      roles: {
        thread: storedRole(stored.models?.roles?.thread),
        coordinator: storedRole(stored.models?.roles?.coordinator),
        triage: storedRole(stored.models?.roles?.triage),
        research: storedRole(stored.models?.roles?.research),
      },
      showCost: stored.models?.showCost === true,
    },
    concurrency: { ...config.concurrency, ...stored.concurrency },
    sourceSecrets: { ...stored.sourceSecrets },
    mcp: { servers: Array.isArray(stored.mcp?.servers) ? stored.mcp.servers : [] },
    input: {
      submitOn: stored.input?.submitOn === 'mod-enter' ? 'mod-enter' : 'enter',
      snippets: storedSnippets(stored.input?.snippets),
      keys: parseKeys(stored.input?.keys, false),
    },
    instructions: typeof stored.instructions === 'string' ? stored.instructions : '',
    // Rules never saved start as the defaults; a saved list, even an empty one, is the user's.
    commands: {
      rules:
        stored.commands?.rules === undefined
          ? config.commands.rules
          : parseRules(stored.commands.rules, false),
    },
    limits: parseLimits(stored.limits, false),
    projectEnv: { ...stored.projectEnv },
    search: { apiKey: typeof stored.search?.apiKey === 'string' ? stored.search.apiKey : '' },
    sandbox: {
      mode: sandbox.mode === 'off' ? 'off' : 'auto',
      hide: Array.isArray(sandbox.hide)
        ? sandbox.hide.filter((p): p is string => typeof p === 'string' && p.trim() !== '')
        : [],
    },
  }
}

/** A path made absolute, `~/` expanded and symbolic links followed where it exists. */
function realPath(path: string): string {
  const full = resolve(path.startsWith('~/') ? join(homedir(), path.slice(2)) : path)
  try {
    return realpathSync(full)
  } catch {
    return full
  }
}

/** Whether `path` is `dir` or inside it. */
function insideDir(path: string, dir: string): boolean {
  const rel = relative(realPath(dir), realPath(path))
  return rel === '' || (!rel.startsWith('..') && !rel.startsWith('/'))
}

function secretKeys(schema: SettingsSchema | undefined): string[] {
  if (!schema) return []
  return Object.entries(schema.properties)
    .filter(([, field]) => field.type === 'string' && field.secret)
    .map(([key]) => key)
}

/**
 * The user's settings in `<home>/config.json`. It holds API keys, so it is owner-only,
 * never part of the event log, and secrets never leave the server.
 */
export class SettingsStore {
  private config: UserConfig

  constructor(
    private readonly path: string,
    private readonly host: PluginHost,
  ) {
    this.config = loadUserConfig(path)
  }

  get current(): Readonly<UserConfig> {
    return this.config
  }

  /** The model settings as the browser may see them: secrets replaced by whether they are set. */
  publicModels() {
    return {
      kinds: [...this.host.models.values()].map((provider) => ({
        kind: provider.kind,
        title: provider.title,
        settings: provider.settings,
        ...(provider.program ? { program: true } : {}),
      })),
      providers: this.config.models.providers.map((provider) => {
        const secrets = secretKeys(this.host.models.get(provider.kind)?.settings)
        const settings = { ...provider.settings }
        for (const key of secrets) delete settings[key]
        const { request, ...rest } = provider
        return {
          ...rest,
          settings,
          secretsSet: secrets.filter((key) => !!provider.settings[key]),
          // Header values may be keys: only their names leave the server.
          headers: Object.keys(request?.headers ?? {}),
          extraBody: request?.extraBody ?? {},
          timeoutSeconds: request?.timeoutSeconds ?? null,
        }
      }),
      models: this.config.models.models,
      roles: this.config.models.roles,
      showCost: this.config.models.showCost,
    }
  }

  /**
   * Replace the model settings. An empty secret keeps the value already stored; `null`
   * clears it.
   */
  updateModels(input: unknown): ModelConfig[] {
    const body = (input ?? {}) as Partial<ModelsConfig>
    if (!Array.isArray(body.providers)) throw new BadRequest('providers')
    const providers = body.providers.map((raw, index): ModelProviderConfig => {
      const provider = (raw ?? {}) as Partial<ModelProviderConfig>
      if (typeof provider.id !== 'string' || !provider.id)
        throw new BadRequest(`providers[${index}].id`)
      if (typeof provider.kind !== 'string' || !this.host.models.has(provider.kind)) {
        throw new BadRequest(`providers[${index}].kind`)
      }
      const before = this.config.models.providers.find((p) => p.id === provider.id)
      const settings = this.withStoredSecrets(provider.kind, provider.settings, before)
      const extras = provider as Record<string, unknown>
      const headers = secretPairs(
        extras.headers,
        HEADER_NAME,
        before?.request?.headers,
        `providers[${index}].headers`,
      )
      const extraBody = extras.extraBody ?? {}
      if (!extraBody || typeof extraBody !== 'object' || Array.isArray(extraBody)) {
        throw new BadRequest(`providers[${index}].extraBody`)
      }
      if (JSON.stringify(extraBody).length > 10_000) {
        throw new BadRequest(`providers[${index}].extraBody`)
      }
      const timeout = extras.timeoutSeconds
      if (
        timeout !== undefined &&
        timeout !== null &&
        !(Number.isInteger(timeout) && (timeout as number) >= 10 && (timeout as number) <= 3600)
      ) {
        throw new BadRequest(`providers[${index}].timeoutSeconds`)
      }
      const request = {
        ...(Object.keys(headers).length ? { headers } : {}),
        ...(Object.keys(extraBody).length
          ? { extraBody: extraBody as Record<string, unknown> }
          : {}),
        ...(typeof timeout === 'number' ? { timeoutSeconds: timeout } : {}),
      }
      return {
        id: provider.id,
        kind: provider.kind,
        name:
          typeof provider.name === 'string' && provider.name.trim()
            ? provider.name.trim()
            : provider.kind,
        settings,
        ...(Object.keys(request).length ? { request } : {}),
      }
    })
    if (!Array.isArray(body.models)) throw new BadRequest('models')
    const ids = new Set<string>()
    const models = body.models.map((raw, index): ModelConfig => {
      const entry = (raw ?? {}) as Partial<ModelConfig>
      const at = `models[${index}]`
      if (typeof entry.id !== 'string' || !entry.id || ids.has(entry.id)) {
        throw new BadRequest(`${at}.id`)
      }
      ids.add(entry.id)
      if (!providers.some((p) => p.id === entry.provider)) throw new BadRequest(`${at}.provider`)
      if (typeof entry.model !== 'string' || !entry.model.trim()) {
        throw new BadRequest(`${at}.model`)
      }
      const size = entry.contextTokens
      if (size !== undefined && size !== null && !(Number.isInteger(size) && size >= 4096)) {
        throw new BadRequest(`${at}.contextTokens`)
      }
      const note = typeof entry.note === 'string' ? entry.note.trim().slice(0, 200) : ''
      const most = entry.maxTokens
      if (
        most !== undefined &&
        most !== null &&
        !(Number.isInteger(most) && most >= 256 && most <= 1_000_000)
      ) {
        throw new BadRequest(`${at}.maxTokens`)
      }
      const price = parsePrice(entry.price, `${at}.price`)
      return {
        id: entry.id,
        provider: entry.provider as string,
        model: entry.model.trim(),
        ...(price ? { price } : {}),
        ...(typeof size === 'number' ? { contextTokens: size } : {}),
        ...(note ? { note } : {}),
        ...(entry.images === false ? { images: false as const } : {}),
        ...(typeof most === 'number' ? { maxTokens: most } : {}),
      }
    })
    const roles = (body.roles ?? {}) as Partial<Record<Role, Partial<RoleConfig>>>
    const role = (key: Role): RoleConfig => {
      const model = roles[key]?.model ?? null
      if (model !== null && !ids.has(model)) throw new BadRequest(`roles.${key}.model`)
      const effort = roles[key]?.effort ?? null
      if (effort !== null && !EFFORTS.includes(effort)) throw new BadRequest(`roles.${key}.effort`)
      const fallback = key === 'research' ? null : (roles[key]?.fallback ?? null)
      if (fallback !== null && !ids.has(fallback)) throw new BadRequest(`roles.${key}.fallback`)
      return { model, effort, fallback }
    }
    const old = this.config.models
    this.config = {
      ...this.config,
      models: {
        providers,
        models,
        roles: Object.fromEntries(ROLES.map((key) => [key, role(key)])) as Record<Role, RoleConfig>,
        showCost: body.showCost === true,
      },
    }
    // A model whose service turned search down gets asked again: the key or model may differ.
    forgetRefusedSearches()
    this.save()
    // What is new is worth asking once: each model added, and a provider whose address
    // or key changed through the first of the models it already had.
    const sameProvider = (p: ModelProviderConfig) =>
      old.providers.some((before) => before.id === p.id && talksWith(before) === talksWith(p))
    const changed = new Set(providers.filter((p) => !sameProvider(p)).map((p) => p.id))
    return models.filter((model) => {
      if (!old.models.some((m) => m.provider === model.provider && m.model === model.model)) {
        changed.delete(model.provider)
        return true
      }
      return changed.delete(model.provider)
    })
  }

  /**
   * A provider's settings as typed in the form, with the secrets left empty taken from
   * what is stored for it (`null` clears one).
   */
  private withStoredSecrets(
    kind: string,
    typed: Record<string, unknown> | undefined,
    before: ModelProviderConfig | undefined,
  ): Record<string, unknown> {
    const settings = { ...typed }
    for (const key of secretKeys(this.host.models.get(kind)?.settings)) {
      if (settings[key] === null) delete settings[key]
      else if (!settings[key] && before?.kind === kind && before.settings[key]) {
        settings[key] = before.settings[key]
      }
    }
    return settings
  }

  /**
   * A provider as the form holds it, not saved yet, ready to be asked for its models: its
   * settings with the stored secrets and the defaults, and the headers stored for it.
   */
  draftProvider(input: unknown): {
    provider: ModelProvider<never>
    settings: Record<string, unknown>
    headers: Record<string, string>
    /** Its kind and address, to tell services apart in the catalog. */
    hint: string
  } {
    const draft = (input ?? {}) as Partial<ModelProviderConfig>
    const provider = typeof draft.kind === 'string' ? this.host.models.get(draft.kind) : undefined
    if (!provider) throw new BadRequest('provider.kind')
    const before = this.config.models.providers.find((p) => p.id === draft.id)
    const typed =
      draft.settings && typeof draft.settings === 'object' && !Array.isArray(draft.settings)
        ? draft.settings
        : {}
    const settings = this.effective(provider, this.withStoredSecrets(provider.kind, typed, before))
    for (const key of provider.settings.required ?? []) {
      if (settings[key] === undefined) throw new BadRequest(`provider.settings.${key}`)
    }
    return {
      provider,
      settings,
      headers: before?.kind === provider.kind ? { ...before.request?.headers } : {},
      hint: this.hintOf(provider.kind, settings),
    }
  }

  /** Stored settings over the provider's defaults, empty values left out. */
  private effective(
    provider: ModelProvider<never>,
    stored: Record<string, unknown>,
  ): Record<string, unknown> {
    const settings: Record<string, unknown> = {}
    for (const [key, field] of Object.entries(provider.settings.properties)) {
      if (field.default !== undefined) settings[key] = field.default
    }
    for (const [key, value] of Object.entries(stored)) {
      if (value !== '' && value !== null && value !== undefined) settings[key] = value
    }
    return settings
  }

  /** A provider's kind and what is not secret of its settings, in words the catalog may know. */
  private hintOf(kind: string, settings: Record<string, unknown>): string {
    const secrets = secretKeys(this.host.models.get(kind)?.settings)
    return [
      kind,
      ...Object.entries(settings).flatMap(([key, value]) =>
        typeof value === 'string' && !secrets.includes(key) ? [value] : [],
      ),
    ].join(' ')
  }

  /**
   * Setups found on this machine (a key in the environment, a program signed in) that no
   * provider uses yet, for the first run to offer.
   */
  foundSetups(
    context = {
      env: process.env,
      home: homedir(),
      which: (program: string) => Bun.which(program),
    },
  ): FoundSetup[] {
    const used = this.config.models.providers
    return [...this.host.models.values()].flatMap((provider) => {
      let found
      try {
        found = provider.find?.(context) ?? []
      } catch {
        // A plugin that cannot look finds nothing.
        return []
      }
      return found
        .map((setup) => ({
          id: `${provider.kind}:${setup.id}`,
          kind: provider.kind,
          title: setup.name ? { 'zh-CN': setup.name, en: setup.name } : provider.title,
          from: setup.from,
          settings: { ...(setup.settings as Record<string, unknown>) },
        }))
        .filter(
          (setup) =>
            !used.some(
              (p) =>
                p.kind === setup.kind &&
                Object.entries(setup.settings).every(([key, value]) => p.settings[key] === value),
            ),
        )
    })
  }

  /** A found setup as a provider would talk with it, to ask it for its models. */
  foundProvider(setup: FoundSetup) {
    const provider = this.host.models.get(setup.kind)
    if (!provider) throw new BadRequest('id')
    return {
      provider,
      settings: this.effective(provider, setup.settings),
      hint: this.hintOf(setup.kind, setup.settings),
    }
  }

  /**
   * Take a found setup into use with `model`: it becomes a provider with that one model,
   * which every role without a model of its own then uses. A key from the environment is
   * copied into the settings, so Ariadne works the same however it is started next.
   */
  useFound(setup: FoundSetup, model: string, info?: ModelInfo): ModelConfig {
    const stamp = Date.now().toString(36)
    const title = setup.title[this.config.locale] ?? setup.kind
    const taken = this.config.models.providers.some((p) => p.name === title)
    const provider: ModelProviderConfig = {
      id: `p_${stamp}`,
      kind: setup.kind,
      name: taken && 'env' in setup.from ? `${title} (${setup.from.env})` : title,
      settings: setup.settings,
    }
    const entry: ModelConfig = {
      id: `m_${stamp}`,
      provider: provider.id,
      model,
      ...infoFields(info),
    }
    const { models } = this.config
    this.config = {
      ...this.config,
      models: {
        ...models,
        providers: [...models.providers, provider],
        models: [...models.models, entry],
        roles: {
          ...models.roles,
          thread: { ...models.roles.thread, model: models.roles.thread.model ?? entry.id },
        },
      },
    }
    forgetRefusedSearches()
    this.save()
    return entry
  }

  /** A saved model as a turn would talk to it, to check that it answers. */
  resolveSaved(model: ModelConfig): ResolvedModel | null {
    return this.resolveConfig('thread', model)
  }

  /**
   * Add a line to the user's instructions for every project, as an agent remembers a
   * preference of theirs everywhere; `false` when it is there already.
   */
  addInstruction(line: string): boolean {
    const text = line.replace(/[\s\u0085]+/g, ' ').trim()
    if (!text) throw new BadRequest('text')
    const { instructions } = this.config
    if (instructions.split('\n').some((existing) => existing.trim() === text)) return false
    const next = instructions ? `${instructions}\n${text}` : text
    if (next.length > MAX_INSTRUCTIONS) throw new DomainError('error.instructionsFull')
    this.config = { ...this.config, instructions: next }
    this.save()
    return true
  }

  /** Take a line out of the user's instructions for every project; `false` when it is not there. */
  removeInstruction(line: string): boolean {
    const text = line.replace(/[\s\u0085]+/g, ' ').trim()
    const lines = this.config.instructions.split('\n')
    const at = lines.findIndex((existing) => existing.replace(/\s+/g, ' ').trim() === text)
    if (at === -1 || !text) return false
    lines.splice(at, 1)
    this.config = { ...this.config, instructions: lines.join('\n').trim() }
    this.save()
    return true
  }

  /**
   * The external tool servers as the browser may see them: the names of environment
   * variables and headers, never their values.
   */
  publicMcp() {
    return this.config.mcp.servers.map(({ env, headers, ...server }) => ({
      ...server,
      env: Object.keys(env ?? {}),
      headers: Object.keys(headers ?? {}),
    }))
  }

  /**
   * Replace the external tool servers. Environment variables and headers come as
   * `{ name, value }`: an empty value keeps the one stored under that name, so the page
   * never needs to know it.
   */
  updateMcp(input: unknown): void {
    const body = (input ?? {}) as { servers?: unknown }
    if (!Array.isArray(body.servers)) throw new BadRequest('servers')
    const ids = new Set<string>()
    const servers = body.servers.map((raw, index): McpServerConfig => {
      const entry = (raw ?? {}) as Record<string, unknown>
      const at = `servers[${index}]`
      const id = entry.id
      if (typeof id !== 'string' || !MCP_ID.test(id) || ids.has(id))
        throw new BadRequest(`${at}.id`)
      ids.add(id)
      const before = this.config.mcp.servers.find((s) => s.id === id)
      const name = typeof entry.name === 'string' && entry.name.trim() ? entry.name.trim() : id
      const tools: Record<string, McpToolMark> = {}
      for (const [tool, mark] of Object.entries((entry.tools ?? {}) as Record<string, unknown>)) {
        if (!MCP_TOOL_MARKS.includes(mark as McpToolMark)) throw new BadRequest(`${at}.tools`)
        // `ask` is what an unmarked tool is anyway.
        if (mark !== 'ask') tools[tool] = mark as McpToolMark
      }
      const pairs = (key: 'env' | 'headers') =>
        secretPairs(
          entry[key] ?? [],
          key === 'env' ? ENV_NAME : HEADER_NAME,
          before?.[key],
          `${at}.${key}`,
        )
      if (entry.transport === 'stdio') {
        const command = typeof entry.command === 'string' ? entry.command.trim() : ''
        if (!command) throw new BadRequest(`${at}.command`)
        const args = entry.args ?? []
        if (!Array.isArray(args) || args.some((a) => typeof a !== 'string')) {
          throw new BadRequest(`${at}.args`)
        }
        const cwd = typeof entry.cwd === 'string' ? entry.cwd.trim() : ''
        // A server never starts inside Ariadne's home: its settings and log live there.
        if (cwd && insideDir(cwd, dirname(this.path))) throw new BadRequest(`${at}.cwd`)
        const env = pairs('env')
        return {
          id,
          name,
          transport: 'stdio',
          command,
          args: args as string[],
          ...(cwd ? { cwd } : {}),
          ...(Object.keys(env).length ? { env } : {}),
          tools,
        }
      }
      if (entry.transport === 'http') {
        const url = typeof entry.url === 'string' ? entry.url.trim() : ''
        let parsed: URL | null = null
        try {
          parsed = new URL(url)
        } catch {
          parsed = null
        }
        if (!parsed || !['http:', 'https:'].includes(parsed.protocol) || parsed.username) {
          throw new BadRequest(`${at}.url`)
        }
        const headers = pairs('headers')
        return {
          id,
          name,
          transport: 'http',
          url,
          ...(Object.keys(headers).length ? { headers } : {}),
          tools,
        }
      }
      throw new BadRequest(`${at}.transport`)
    })
    this.config = { ...this.config, mcp: { servers } }
    this.save()
  }

  /**
   * Load a plugin from a path and remember it, so it loads on every start. Nothing is
   * remembered when it fails to load.
   */
  async addPlugin(input: unknown): Promise<void> {
    if (typeof input !== 'string' || !input.trim()) throw new BadRequest('path')
    const path = input.trim()
    if (this.config.plugins.includes(path)) throw new DomainError('error.pluginExists', { path })
    // Plugin data directories hold the threads' worktrees and clones of repositories:
    // code there is written by threads or comes from the repository, not from the user.
    if (insideDir(path, join(dirname(this.path), 'plugins'))) {
      throw new DomainError('error.pluginInWorkspace')
    }
    try {
      await this.host.loadFromPath(path)
    } catch (error) {
      // The settings page shows the failure from the request; it is not kept.
      await this.host.unloadPath(path)
      throw new DomainError('error.pluginLoad', {
        detail: error instanceof Error ? error.message : String(error),
      })
    }
    this.config = { ...this.config, plugins: [...this.config.plugins, path] }
    this.save()
  }

  /** Unload a plugin loaded from a path and stop loading it on start. */
  async removePlugin(input: unknown): Promise<void> {
    if (typeof input !== 'string' || !this.config.plugins.includes(input)) {
      throw new BadRequest('path')
    }
    await this.host.unloadPath(input)
    this.config = { ...this.config, plugins: this.config.plugins.filter((p) => p !== input) }
    this.save()
  }

  /**
   * Change how thread commands are isolated: on (`auto`) or `off`, and the paths hidden
   * from them besides Ariadne's own. Read on start: it takes effect after a restart.
   */
  updateSandbox(input: unknown): UserConfig['sandbox'] {
    const body = (input ?? {}) as Partial<UserConfig['sandbox']>
    if (body.mode !== 'auto' && body.mode !== 'off') throw new BadRequest('mode')
    if (!Array.isArray(body.hide) || body.hide.length > 100) throw new BadRequest('hide')
    const hide: string[] = []
    for (const path of body.hide) {
      if (typeof path !== 'string' || path.length > 1000) throw new BadRequest('hide')
      const trimmed = path.trim()
      if (trimmed && !hide.includes(trimmed)) hide.push(trimmed)
    }
    this.config = { ...this.config, sandbox: { mode: body.mode, hide } }
    this.save()
    return this.config.sandbox
  }

  /** Change how many turns run at once, plus those kept for answering the user. */
  updateConcurrency(input: unknown): UserConfig['concurrency'] {
    const body = (input ?? {}) as Partial<UserConfig['concurrency']>
    const slots = body.slots
    const reserved = body.reserved
    if (!(Number.isInteger(slots) && slots! >= 1 && slots! <= 16)) throw new BadRequest('slots')
    if (!(Number.isInteger(reserved) && reserved! >= 0 && reserved! <= 4)) {
      throw new BadRequest('reserved')
    }
    this.config = { ...this.config, concurrency: { slots: slots!, reserved: reserved! } }
    this.save()
    return this.config.concurrency
  }

  /** Change which key sends and the snippets offered after `/`; the whole list at once. */
  updateInput(input: unknown): InputConfig {
    const body = (input ?? {}) as Partial<InputConfig>
    if (body.submitOn !== 'enter' && body.submitOn !== 'mod-enter') {
      throw new BadRequest('submitOn')
    }
    if (!Array.isArray(body.snippets) || body.snippets.length > MAX_SNIPPETS) {
      throw new BadRequest('snippets')
    }
    const ids = new Set<string>()
    const snippets = body.snippets.map((item, index) => {
      const { id, name, text } = (item ?? {}) as Partial<Snippet>
      if (typeof id !== 'string' || !SNIPPET_ID.test(id) || ids.has(id)) {
        throw new BadRequest(`snippets[${index}].id`)
      }
      ids.add(id)
      const label = typeof name === 'string' ? name.trim() : ''
      if (!label || label.length > MAX_SNIPPET_NAME) throw new BadRequest(`snippets[${index}].name`)
      if (typeof text !== 'string' || !text.trim() || text.length > MAX_SNIPPET_TEXT) {
        throw new BadRequest(`snippets[${index}].text`)
      }
      return { id, name: label, text }
    })
    const keys = parseKeys(body.keys ?? DEFAULT_KEYS, true)
    this.config = { ...this.config, input: { submitOn: body.submitOn, snippets, keys } }
    this.save()
    return this.config.input
  }

  /** How the work is done everywhere: the user's instructions, command rules and limits. */
  work() {
    const { instructions, commands, limits } = this.config
    return { instructions, rules: commands.rules, limits, defaults: DEFAULT_LIMITS }
  }

  /** Replace what `work()` returns; each part sent is replaced whole, the others stay. */
  updateWork(input: unknown) {
    const body = (input ?? {}) as Record<string, unknown>
    let { instructions, commands, limits } = this.config
    if (body.instructions !== undefined) {
      if (typeof body.instructions !== 'string' || body.instructions.length > MAX_INSTRUCTIONS) {
        throw new BadRequest('instructions')
      }
      instructions = body.instructions.trim()
    }
    if (body.rules !== undefined) commands = { rules: parseRules(body.rules, true) }
    if (body.limits !== undefined) limits = parseLimits(body.limits, true)
    this.config = { ...this.config, instructions, commands, limits }
    this.save()
    return this.work()
  }

  /**
   * Web search as the settings page shows it. `needed` says a key is what search waits
   * for: models are set up, none of their services searches the web itself, and no plugin
   * brought a search service. The key itself stays here.
   */
  publicSearch() {
    const searches = this.host.searches.size > 0 || this.searchModels().length > 0
    return {
      keySet: !!this.config.search.apiKey,
      needed: this.config.models.models.length > 0 && !searches,
    }
  }

  /** Save the search key: `null` clears it, an empty one keeps what is stored. */
  updateSearch(input: unknown) {
    const { apiKey } = (input ?? {}) as { apiKey?: unknown }
    if (apiKey !== undefined && apiKey !== null && typeof apiKey !== 'string') {
      throw new BadRequest('apiKey')
    }
    const key = typeof apiKey === 'string' ? apiKey.trim() : apiKey
    if (typeof key === 'string' && (key.length > MAX_SEARCH_KEY || /\s/.test(key))) {
      throw new BadRequest('apiKey')
    }
    if (key === null || key) {
      this.config = { ...this.config, search: { apiKey: key ?? '' } }
      this.save()
    }
    return this.publicSearch()
  }

  private keyed: { apiKey: string; service: SearchProvider } | null = null

  /** The search service the saved key is for; `null` without a key. */
  searchService(): SearchProvider | null {
    const { apiKey } = this.config.search
    if (!apiKey) return null
    if (this.keyed?.apiKey !== apiKey) this.keyed = { apiKey, service: keyedSearch(apiKey) }
    return this.keyed.service
  }

  /** The models set up whose service searches the web itself, in the order listed. */
  searchModels(): ResolvedModel[] {
    return searchingModels(
      this.config.models.models.map((model) => {
        try {
          return this.resolveConfig('research', model)
        } catch {
          // Its plugin is gone: it cannot search either.
          return null
        }
      }),
    )
  }

  /** The names of the project's command environment variables; values stay here. */
  projectEnvNames(projectId: string): string[] {
    return Object.keys(this.config.projectEnv[projectId] ?? {})
  }

  /** Replace the project's command environment; an empty value keeps the stored one. */
  updateProjectEnv(projectId: string, input: unknown): string[] {
    const env = secretPairs(input, PROJECT_ENV_NAME, this.config.projectEnv[projectId], 'env')
    const all = { ...this.config.projectEnv }
    if (Object.keys(env).length) all[projectId] = env
    else delete all[projectId]
    this.config = { ...this.config, projectEnv: all }
    this.save()
    return this.projectEnvNames(projectId)
  }

  /** Forget what the settings hold for a project that is gone. */
  forgetProject(projectId: string): void {
    const { [projectId]: _env, ...projectEnv } = this.config.projectEnv
    const { [projectId]: _secrets, ...sourceSecrets } = this.config.sourceSecrets
    this.config = { ...this.config, projectEnv, sourceSecrets }
    this.save()
  }

  /** What a turn of the project's conversations goes by, besides the model. */
  turnSettings(projectId: string): TurnSettings {
    return {
      limits: this.config.limits,
      instructions: this.config.instructions,
      commandRules: this.config.commands.rules,
      // A hand-edited file may hold any name; the refused ones never reach a command.
      env: Object.fromEntries(
        Object.entries(this.config.projectEnv[projectId] ?? {}).filter(([name]) =>
          PROJECT_ENV_NAME.test(name),
        ),
      ),
    }
  }

  /** The secret settings of a project's source, merged in when it connects. */
  sourceSecrets(projectId: string): Record<string, string> {
    return { ...this.config.sourceSecrets[projectId] }
  }

  /** Which secrets of a project's source are set; their values never leave the server. */
  sourceSecretsSet(projectId: string): string[] {
    return Object.keys(this.config.sourceSecrets[projectId] ?? {}).filter(
      (key) => !key.startsWith(REFERENCE_SECRET),
    )
  }

  /**
   * Split a source's settings: secrets are kept here, the rest is returned for the event
   * log. An empty secret keeps the stored value, `null` clears it. `kind` changing drops
   * the old secrets. A hosting's secrets are kept under `prefix`.
   */
  splitSourceSettings(
    projectId: string,
    kind: string | null,
    settings: Record<string, unknown>,
    previousKind: string | null,
    prefix = '',
  ): { settings: Record<string, unknown>; secrets: Record<string, string> } {
    const schema = kind ? this.host.contexts.get(kind)?.settings : undefined
    const stored = this.sourceSecrets(projectId)
    const before = previousKind === kind ? stored : {}
    const plain = { ...settings }
    const secrets: Record<string, string> = {}
    for (const key of secretKeys(schema)) {
      const value = plain[key]
      delete plain[key]
      if (value === null) continue
      if (typeof value === 'string' && value.trim()) secrets[`${prefix}${key}`] = value.trim()
      else if (before[`${prefix}${key}`]) secrets[`${prefix}${key}`] = before[`${prefix}${key}`]!
    }
    return { settings: plain, secrets }
  }

  /** The secrets of the project's own source (and its hosting); its references keep theirs. */
  saveSourceSecrets(projectId: string, secrets: Record<string, string>): void {
    const kept = Object.entries(this.config.sourceSecrets[projectId] ?? {}).filter(([key]) =>
      key.startsWith(REFERENCE_SECRET),
    )
    this.writeSourceSecrets(projectId, { ...Object.fromEntries(kept), ...secrets })
  }

  /** The secrets of one reference source, by their plain names; none removes them. */
  saveReferenceSecrets(
    projectId: string,
    referenceId: string,
    secrets: Record<string, string>,
  ): void {
    const prefix = referenceSecretPrefix(referenceId)
    const others = Object.entries(this.config.sourceSecrets[projectId] ?? {}).filter(
      ([key]) => !key.startsWith(prefix),
    )
    this.writeSourceSecrets(projectId, {
      ...Object.fromEntries(others),
      ...Object.fromEntries(Object.entries(secrets).map(([key, value]) => [prefix + key, value])),
    })
  }

  private writeSourceSecrets(projectId: string, secrets: Record<string, string>): void {
    const all = { ...this.config.sourceSecrets }
    if (Object.keys(secrets).length) all[projectId] = secrets
    else delete all[projectId]
    this.config = { ...this.config, sourceSecrets: all }
    this.save()
  }

  /**
   * The model a conversation talks to, or `null` when none is set up. A thread uses the
   * model it was given while that is still set up, else the default for threads.
   */
  resolveModel(role: Role, given: string | null = null): ResolvedModel | null {
    const chosen = this.modelFor(role, given)
    if (!chosen) return null
    const resolved = this.resolveConfig(role, chosen)
    if (!resolved) return null
    const fallback = this.fallbackFor(role)
    // A fallback whose provider is gone, or that is the model itself, is no fallback.
    const backup = fallback && fallback.id !== chosen.id ? this.resolveConfig(role, fallback) : null
    return backup ? { ...resolved, fallback: backup } : resolved
  }

  /**
   * The fallback a role's turns go on with. A role that takes its model from another (the
   * coordinator from threads, triage from the coordinator) takes that one's fallback too.
   */
  fallbackFor(role: Role): ModelConfig | null {
    const { models, roles } = this.config.models
    const find = (id: string | null) => (id ? (models.find((m) => m.id === id) ?? null) : null)
    const own = find(roles[role].fallback)
    if (own || role === 'research' || role === 'thread' || roles[role].model) return own
    return this.fallbackFor(role === 'triage' ? 'coordinator' : 'thread')
  }

  /** A model setting as a conversation of `role` talks to it. */
  private resolveConfig(role: Role, chosen: ModelConfig): ResolvedModel | null {
    const effort = this.effortFor(role)
    const config = this.config.models.providers.find((p) => p.id === chosen.provider)
    if (!config) return null
    const provider = this.host.models.get(config.kind)
    if (!provider) throw new DomainError('error.modelUnknown', { provider: config.kind })
    const settings = this.effective(provider, config.settings)
    const request: ModelRequestOptions = {
      ...config.request,
      ...(chosen.maxTokens ? { maxTokens: chosen.maxTokens } : {}),
    }
    return {
      provider,
      settings,
      model: chosen.model,
      label: this.labelOf(chosen),
      ...(chosen.contextTokens ? { contextTokens: chosen.contextTokens } : {}),
      images: chosen.images !== false,
      ...(chosen.price ? { price: chosen.price } : {}),
      ...(effort ? { effort } : {}),
      ...(Object.keys(request).length ? { request } : {}),
    }
  }

  /**
   * The coordinator's model for a turn: the triage model when all it has waiting is what
   * the user wrote in threads (`triage`), unless the coordinator's context (`contextTokens`,
   * what it read last) is too large for it. A smaller model would make room in that history
   * by its own limit, and its summary would replace what the coordinator reads from then on.
   */
  resolveCoordinator(options: { triage: boolean; contextTokens: number }): ResolvedModel | null {
    const coordinator = this.resolveModel('coordinator')
    if (!options.triage) return coordinator
    const triage = this.resolveModel('triage')
    if (!triage) return coordinator
    // A fifth to spare: the turn adds to what was read last.
    const room = compactAt(triage.contextTokens, this.config.limits.compactShare) * 0.8
    return options.contextTokens < room ? triage : coordinator
  }

  /** How hard a role thinks; triage left wholly unset is the coordinator as it is. */
  private effortFor(role: Role): Effort | null {
    const { roles } = this.config.models
    if (role === 'triage' && !roles.triage.model && !roles.triage.effort) {
      return roles.coordinator.effort
    }
    return roles[role].effort
  }

  /** The model setting a role (or a thread given `given`) uses, or `null`. */
  modelFor(role: Role, given: string | null = null): ModelConfig | null {
    const { models, roles } = this.config.models
    const find = (id: string | null) => (id ? (models.find((m) => m.id === id) ?? null) : null)
    if (role === 'triage') {
      return find(roles.triage.model) ?? find(roles.coordinator.model) ?? find(roles.thread.model)
    }
    // Without one of its own, a research runs on the model of the conversation asking.
    if (role === 'research') return find(roles.research.model)
    if (role === 'coordinator') return find(roles.coordinator.model) ?? find(roles.thread.model)
    return find(given) ?? find(roles.thread.model)
  }

  /** The models the coordinator may give a thread, with what the user said each is for. */
  modelChoices(): ModelChoice[] {
    const threadDefault = this.modelFor('thread')
    return this.config.models.models
      .filter((m) => this.config.models.providers.some((p) => p.id === m.provider))
      .map((m) => ({
        id: m.id,
        label: this.labelOf(m),
        note: m.note ?? '',
        threadDefault: m.id === threadDefault?.id,
      }))
  }

  private labelOf(model: ModelConfig): string {
    const provider = this.config.models.providers.find((p) => p.id === model.provider)
    return `${provider?.name ?? model.provider} / ${model.model}`
  }

  private save(): void {
    const temp = `${this.path}.tmp`
    writeFileSync(temp, `${JSON.stringify(this.config, null, 2)}\n`, { mode: 0o600 })
    chmodSync(temp, 0o600)
    renameSync(temp, this.path)
  }
}
