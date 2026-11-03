import type { Capabilities, CapabilityName } from './capabilities'

export type * from './capabilities'

/** A JSON-schema-like description of a plugin's settings, rendered as a form by the web UI. */
export interface SettingsSchema {
  type: 'object'
  properties: Record<string, SettingField>
  required?: string[]
}

export type SettingField =
  | { type: 'string'; title: string; description?: string; secret?: boolean; default?: string }
  | { type: 'number'; title: string; description?: string; default?: number }
  | { type: 'boolean'; title: string; description?: string; default?: boolean }
  | { type: 'enum'; title: string; description?: string; options: string[]; default?: string }

/** A kind of context source a plugin contributes, e.g. "local directory". */
export interface ContextProvider<Settings = Record<string, unknown>> {
  kind: string
  title: string
  settings: SettingsSchema
  provides: CapabilityName[]
  /** Build the capabilities of one configured source. */
  connect(settings: Settings): Promise<Partial<Capabilities>>
}

export interface ChatMessage {
  role: 'system' | 'user' | 'assistant' | 'tool'
  content: string
  toolCallId?: string
}

export interface ToolSpec {
  name: string
  description: string
  /** JSON Schema of the arguments. */
  parameters: Record<string, unknown>
}

export type ModelStreamPart =
  | { type: 'text'; text: string }
  | { type: 'tool-call'; id: string; name: string; arguments: string }
  | { type: 'finish'; reason: 'stop' | 'length' | 'tool-calls' | 'error'; error?: string }

/** A wire protocol for talking to models, such as the OpenAI-compatible or Anthropic protocol. */
export interface ModelProvider<Settings = Record<string, unknown>> {
  kind: string
  title: string
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
