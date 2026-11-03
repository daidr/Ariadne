import { DomainError, type ConversationView } from '@ariadne/core'
import type { ResolvedModel } from '@ariadne/agent'
import type { PluginHost } from '@ariadne/agent'
import type { Locale } from '@ariadne/i18n'
import type { SettingsSchema } from '@ariadne/plugin-sdk'
import { chmodSync, existsSync, readFileSync, renameSync, writeFileSync } from 'node:fs'
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
}

const defaults = (): UserConfig => ({
  locale: 'zh-CN',
  plugins: [],
  models: { providers: [], default: null },
  concurrency: { slots: 3, reserved: 1 },
})

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

  /** Replace the model settings. An empty secret keeps the value already stored. */
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
        if (!settings[key] && before?.kind === provider.kind && before.settings[key]) {
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
