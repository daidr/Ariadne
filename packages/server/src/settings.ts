import { DomainError, type ConversationView } from '@ariadne/core'
import type { ResolvedModel } from '@ariadne/agent'
import type { PluginHost } from '@ariadne/agent'
import type { Locale } from '@ariadne/i18n'
import type { SettingsSchema } from '@ariadne/plugin-sdk'
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
}

export interface ModelsConfig {
  providers: ModelProviderConfig[]
  /** The model every conversation uses for now. */
  default: { provider: string; model: string } | null
}

export interface UserConfig {
  locale: Locale
  /** Local plugin paths, loaded after the built-in ones. */
  plugins: string[]
  models: ModelsConfig
  concurrency: { slots: number; reserved: number }
  /** Secret settings of each project's context source (e.g. a token), by project id. */
  sourceSecrets: Record<string, Record<string, string>>
}

const defaults = (): UserConfig => ({
  locale: 'zh-CN',
  plugins: [],
  models: { providers: [], default: null },
  concurrency: { slots: 3, reserved: 1 },
  sourceSecrets: {},
})

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
    this.config = defaults()
    if (existsSync(path)) {
      const stored = JSON.parse(readFileSync(path, 'utf8')) as Partial<UserConfig>
      this.config = {
        ...this.config,
        ...stored,
        models: { ...this.config.models, ...stored.models },
        concurrency: { ...this.config.concurrency, ...stored.concurrency },
        sourceSecrets: { ...stored.sourceSecrets },
      }
    }
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
        return {
          ...provider,
          settings,
          secretsSet: secrets.filter((key) => !!provider.settings[key]),
        }
      }),
      default: this.config.models.default,
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
      return {
        id: provider.id,
        kind: provider.kind,
        name:
          typeof provider.name === 'string' && provider.name.trim()
            ? provider.name.trim()
            : provider.kind,
        settings,
      }
    })
    const chosen = body.default ?? null
    if (chosen !== null) {
      if (!providers.some((p) => p.id === chosen.provider)) throw new BadRequest('default.provider')
      if (typeof chosen.model !== 'string' || !chosen.model.trim())
        throw new BadRequest('default.model')
    }
    this.config = {
      ...this.config,
      models: {
        providers,
        default: chosen && { provider: chosen.provider, model: chosen.model.trim() },
      },
    }
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
   * the old secrets.
   */
  splitSourceSettings(
    projectId: string,
    kind: string | null,
    settings: Record<string, unknown>,
    previousKind: string | null,
  ): { settings: Record<string, unknown>; secrets: Record<string, string> } {
    const schema = kind ? this.host.contexts.get(kind)?.settings : undefined
    const before = previousKind === kind ? this.sourceSecrets(projectId) : {}
    const plain = { ...settings }
    const secrets: Record<string, string> = {}
    for (const key of secretKeys(schema)) {
      const value = plain[key]
      delete plain[key]
      if (value === null) continue
      if (typeof value === 'string' && value.trim()) secrets[key] = value.trim()
      else if (before[key]) secrets[key] = before[key]
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

  /** The model a conversation talks to, or `null` when none is set up. */
  resolveModel(_conversation: ConversationView): ResolvedModel | null {
    const chosen = this.config.models.default
    if (!chosen) return null
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
    return { provider, settings, model: chosen.model, label: `${config.name} / ${chosen.model}` }
  }

  private save(): void {
    const temp = `${this.path}.tmp`
    writeFileSync(temp, `${JSON.stringify(this.config, null, 2)}\n`, { mode: 0o600 })
    chmodSync(temp, 0o600)
    renameSync(temp, this.path)
  }
}
