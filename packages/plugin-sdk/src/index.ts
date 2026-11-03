import type { Capabilities, CapabilityName } from './capabilities'

export type * from './capabilities'

/**
 * User-facing text from a plugin, one entry per locale. Chinese is the source language and
 * must be present; the UI falls back to it when the user's locale is missing.
 */
export type LocalizedText = { 'zh-CN': string } & Partial<Record<string, string>>

/** A JSON-schema-like description of a plugin's settings, rendered as a form by the web UI. */
export interface SettingsSchema {
  type: 'object'
  properties: Record<string, SettingField>
  required?: string[]
}

export type SettingField =
  | {
      type: 'string'
      title: LocalizedText
      description?: LocalizedText
      secret?: boolean
      default?: string
    }
  | { type: 'number'; title: LocalizedText; description?: LocalizedText; default?: number }
  | { type: 'boolean'; title: LocalizedText; description?: LocalizedText; default?: boolean }
  | {
      type: 'enum'
      title: LocalizedText
      description?: LocalizedText
      options: string[]
      default?: string
    }

/** A kind of context source a plugin contributes, e.g. "local directory". */
export interface ContextProvider<Settings = Record<string, unknown>> {
  kind: string
  title: LocalizedText
  settings: SettingsSchema
  provides: CapabilityName[]
  /** Build the capabilities of one configured source. */
  connect(settings: Settings): Promise<Partial<Capabilities>>
}

export interface ToolCall {
  id: string
  name: string
  /** JSON text of the arguments, exactly as the model produced it. */
  arguments: string
}

/**
 * One message of a model request. Both protocols require a tool result to follow the
 * assistant message that made the call, so assistant turns carry their calls and the whole
 * request can be rebuilt from the event log.
 */
export type ChatMessage =
  | { role: 'system' | 'user'; content: string }
  | { role: 'assistant'; content: string; toolCalls?: ToolCall[] }
  | { role: 'tool'; toolCallId: string; content: string; isError?: boolean }

export interface ToolSpec {
  name: string
  description: string
  /** JSON Schema of the arguments. */
  parameters: Record<string, unknown>
}

export type ModelStreamPart =
  | { type: 'text'; text: string }
  | ({ type: 'tool-call' } & ToolCall)
  | { type: 'finish'; reason: 'stop' | 'length' | 'tool-calls' | 'error'; error?: string }

/** A wire protocol for talking to models, such as the OpenAI-compatible or Anthropic protocol. */
export interface ModelProvider<Settings = Record<string, unknown>> {
  kind: string
  title: LocalizedText
  settings: SettingsSchema
  stream(
    settings: Settings,
    request: { model: string; messages: ChatMessage[]; tools: ToolSpec[]; signal?: AbortSignal },
  ): AsyncIterable<ModelStreamPart>
}

/** Undo a registration. */
export type Dispose = () => void | Promise<void>

/** What `setup` receives: every method registers something and returns its disposer. */
export interface PluginContext {
  registerContextProvider(provider: ContextProvider<never>): Dispose
  registerModelProvider(provider: ModelProvider<never>): Dispose
}

export interface Plugin {
  id: string
  name: string
  /** Register contributions. A returned function runs when the plugin unloads. */
  setup(ctx: PluginContext): void | Dispose | Promise<void | Dispose>
}

export function definePlugin(plugin: Plugin): Plugin {
  return plugin
}
