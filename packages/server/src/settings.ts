import { DomainError } from '@ariadne/core'
import {
  COMMAND_ACTIONS,
  compactAt,
  DEFAULT_LIMITS,
  LIMIT_RANGES,
  MCP_TOOL_MARKS,
  type CommandAction,
  type CommandRule,
  type Limits,
  type McpServerConfig,
  type McpToolMark,
  type ModelChoice,
  type ModelRequestOptions,
  type PluginHost,
  type ResolvedModel,
  type TurnSettings,
} from '@ariadne/agent'
import type { Locale } from '@ariadne/i18n'
import { EFFORTS, type Effort, type SettingsSchema } from '@ariadne/plugin-sdk'
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
}

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
  },
  concurrency: { slots: 3, reserved: 1 },
  sourceSecrets: {},
  sandbox: { mode: 'auto', hide: [] },
  mcp: { servers: [] },
  input: { submitOn: 'enter', snippets: [], keys: { ...DEFAULT_KEYS } },
  instructions: '',
  commands: { rules: [] },
  limits: { ...DEFAULT_LIMITS },
  projectEnv: {},
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
    commands: { rules: parseRules(stored.commands?.rules, false) },
    limits: parseLimits(stored.limits, false),
    projectEnv: { ...stored.projectEnv },
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
    }
  }

  /**
   * Replace the model settings. An empty secret keeps the value already stored; `null`
   * clears it.
   */
  updateModels(input: unknown): void {
    const body = (input ?? {}) as Partial<ModelsConfig>
    if (!Array.isArray(body.providers)) throw new BadRequest('providers')
    const providers = body.providers.map((raw, index): ModelProviderConfig => {
      const provider = (raw ?? {}) as Partial<ModelProviderConfig>
      if (typeof provider.id !== 'string' || !provider.id)
        throw new BadRequest(`providers[${index}].id`)
      if (typeof provider.kind !== 'string' || !this.host.models.has(provider.kind)) {
        throw new BadRequest(`providers[${index}].kind`)
      }
      const settings = { ...provider.settings }
      const before = this.config.models.providers.find((p) => p.id === provider.id)
      for (const key of secretKeys(this.host.models.get(provider.kind)?.settings)) {
        if (settings[key] === null) delete settings[key]
        else if (!settings[key] && before?.kind === provider.kind && before.settings[key]) {
          settings[key] = before.settings[key]
        }
      }
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
      return {
        id: entry.id,
        provider: entry.provider as string,
        model: entry.model.trim(),
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
    this.config = {
      ...this.config,
      models: {
        providers,
        models,
        roles: Object.fromEntries(ROLES.map((key) => [key, role(key)])) as Record<Role, RoleConfig>,
      },
    }
    this.save()
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
    return Object.keys(this.config.sourceSecrets[projectId] ?? {})
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

  saveSourceSecrets(projectId: string, secrets: Record<string, string>): void {
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
    const settings: Record<string, unknown> = {}
    for (const [key, field] of Object.entries(provider.settings.properties)) {
      if (field.default !== undefined) settings[key] = field.default
    }
    for (const [key, value] of Object.entries(config.settings)) {
      if (value !== '' && value !== null && value !== undefined) settings[key] = value
    }
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
