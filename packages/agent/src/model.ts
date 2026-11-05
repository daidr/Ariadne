import type { Effort, ModelProvider } from '@ariadne/plugin-sdk'

/** The model a conversation talks to, with its provider's settings. */
export interface ResolvedModel {
  provider: ModelProvider<never>
  settings: unknown
  model: string
  /** Shown to the user, e.g. `anthropic / some-model`. */
  label: string
  /** How many tokens the model reads at most, when its settings say. */
  contextTokens?: number
  /** `false` when the model cannot look at images: they are named in words instead. */
  images?: boolean
  /** How hard it thinks in this role; without it the request asks nothing. */
  effort?: Effort
  /** What the user added to its requests in the settings. */
  request?: ModelRequestOptions
  /**
   * What the turn goes on with when this model keeps failing on errors worth retrying
   * (rate limits, overload, server errors) after its retries.
   */
  fallback?: ResolvedModel
}

/** Request settings the user gave a provider (headers, body, timeout) or a model (output). */
export interface ModelRequestOptions {
  /** Added to every request's headers; may hold secrets. */
  headers?: Record<string, string>
  /** Fields added to every request's body, e.g. `temperature`. */
  extraBody?: Record<string, unknown>
  /** The most the model may write in one response. */
  maxTokens?: number
  /** A request with no output for this long is given up and tried again. */
  timeoutSeconds?: number
}

/**
 * What a request went with besides its messages, as recorded with it: the provider kind,
 * model, effort and settings, secrets left out. The trace rebuilds the body from it.
 */
export function callOf(model: ResolvedModel, effort: Effort | null): string {
  const settings: Record<string, unknown> = {}
  const fields = model.provider.settings.properties
  for (const [key, value] of Object.entries((model.settings ?? {}) as Record<string, unknown>)) {
    const field = fields[key]
    if (field && !(field.type === 'string' && field.secret)) settings[key] = value
  }
  const { maxTokens, extraBody } = model.request ?? {}
  return JSON.stringify({
    provider: model.provider.kind,
    model: model.model,
    effort,
    settings,
    ...(maxTokens ? { maxTokens } : {}),
    ...(extraBody && Object.keys(extraBody).length ? { extraBody } : {}),
  })
}
