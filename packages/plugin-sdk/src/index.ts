import type { Capabilities, CapabilityName } from './capabilities'

export type * from './capabilities'
export { FileTooLargeError, MergeRefused, readOnlyView, WorkspacePathError } from './capabilities'
export {
  describeHttpError,
  httpErrorPart,
  incompletePart,
  MAX_RETRY_AFTER_MS,
  readServerSentEvents,
  retryAfterMs,
  transportErrorPart,
  type ServerSentEvent,
} from './sse'

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
  /**
   * The source's workspaces are on another machine: threads' commands run there, as the
   * user the source logs in as, and the host's sandbox does not apply to them.
   */
  remote?: boolean
  /**
   * How the source is named in one line, with `{setting}` replaced by its value, e.g.
   * `{host}:{path}`. Without it the first text setting is shown.
   */
  summary?: string
  /**
   * Its repository and issues can come with a remote source's workspaces: threads work on
   * the other machine, and their branches are pushed from here, with this source's
   * credentials, which never reach that machine.
   */
  hosting?: boolean
  /** Build the capabilities of one configured source. */
  connect(settings: Settings): Promise<Partial<Capabilities>>
}

export interface ToolCall {
  id: string
  name: string
  /** JSON text of the arguments, exactly as the model produced it. */
  arguments: string
}

/** A picture for the model: PNG, JPEG, GIF or WebP, base64 encoded. */
export interface ImagePart {
  mime: string
  data: string
}

/**
 * What a model thought before answering, as its provider returned it. `signature` (or the
 * opaque `text` of a `redacted` block) lets the provider check the block when it is sent
 * back; a provider that does not need it back may drop it.
 */
export interface ReasoningBlock {
  text: string
  signature?: string
  redacted?: boolean
}

/**
 * One message of a model request. Both protocols require a tool result to follow the
 * assistant message that made the call, so assistant turns carry their calls and the whole
 * request can be rebuilt from the event log. Images come after a message's text; a tool's
 * images belong to its result (a protocol that cannot put them there sends them right
 * after the results of the step).
 */
export type ChatMessage =
  | { role: 'system'; content: string }
  | { role: 'user'; content: string; images?: ImagePart[] }
  | { role: 'assistant'; content: string; toolCalls?: ToolCall[]; reasoning?: ReasoningBlock[] }
  | {
      role: 'tool'
      toolCallId: string
      content: string
      isError?: boolean
      images?: ImagePart[]
    }

export interface ToolSpec {
  name: string
  description: string
  /** JSON Schema of the arguments. */
  parameters: Record<string, unknown>
}

export type ModelStreamPart =
  | { type: 'text'; text: string }
  | ({ type: 'tool-call' } & ToolCall)
  /**
   * One whole block of reasoning; it comes before the answer it led to. `startedAt` is
   * when its first piece came, as it is only handed on once whole.
   */
  | ({ type: 'reasoning'; startedAt?: number } & ReasoningBlock)
  /**
   * Tokens of the whole response. `input` counts everything the model read, `cached` the
   * part of it read from the provider's cache and `cacheWrite` the part written to it;
   * `reasoning` is the part of `output` spent thinking. Each only when the provider says.
   */
  | {
      type: 'usage'
      input: number
      output: number
      cached?: number
      cacheWrite?: number
      reasoning?: number
    }
  | {
      type: 'finish'
      reason: 'stop' | 'length' | 'tool-calls' | 'error'
      error?: string
      /**
       * The error is worth trying again: rate limited, overloaded, the connection failed or
       * the stream ended early. `retryAfterMs` is how long the service asked to wait.
       */
      retryable?: boolean
      retryAfterMs?: number
    }

/** How hard a model thinks before it answers. Without it the request asks nothing of it. */
export type Effort = 'low' | 'medium' | 'high'

export const EFFORTS: readonly Effort[] = ['low', 'medium', 'high']

/** What the agent loop asks of a model provider for one response. */
export interface ModelRequest {
  model: string
  messages: ChatMessage[]
  tools: ToolSpec[]
  effort?: Effort
  signal?: AbortSignal
}

/** A wire protocol for talking to models, such as the OpenAI-compatible or Anthropic protocol. */
export interface ModelProvider<Settings = Record<string, unknown>> {
  kind: string
  title: LocalizedText
  settings: SettingsSchema
  /**
   * Stream one response. Text may arrive in pieces; each tool call arrives whole. Errors
   * of the remote service end the stream with a `finish` part of reason `error`, marked
   * `retryable` when trying again may help; the agent loop retries only those.
   */
  stream(settings: Settings, request: ModelRequest): AsyncIterable<ModelStreamPart>
  /**
   * The body `stream` sends for `request`, without credentials (they go in headers). The
   * trace shows it as the request the service got.
   */
  requestBody?(settings: Settings, request: ModelRequest): unknown
}

/** Undo a registration. */
export type Dispose = () => void | Promise<void>

/**
 * Where a thread's commands run. The host picks one when it starts: on Linux a mount and
 * process namespace (bubblewrap, or `unshare`), on macOS `sandbox-exec`. Inside it,
 * Ariadne's home directory (its token, settings and event log) and the browsers' profiles
 * are out of sight, except the paths a workspace keeps. On Linux the command also gets its
 * own process list, and whatever it leaves running ends with it; `sandbox-exec` only
 * restricts files, so on macOS the command sees the server's processes and can leave
 * processes behind.
 */
export interface CommandSandbox {
  /** What isolates commands, e.g. `bwrap`; `null` when they run as they are. */
  readonly kind: string | null
  /**
   * The command line that runs `argv` in the sandbox. `keep` are the paths the command
   * works in (its checkout, the git directory behind it); they stay visible even inside
   * a hidden directory. `readOnly` paths can be read but not changed: anything the
   * server later runs outside the sandbox reads, such as a git directory's `config` and
   * `hooks`.
   */
  wrap(argv: string[], options: { keep: string[]; readOnly?: string[] }): string[]
  /**
   * The real paths threads must not see (Ariadne's home, the browsers' profiles, what the
   * user added). Commands get them covered where the sandbox works; file tools refuse
   * them everywhere, also where no sandbox runs.
   */
  readonly hidden?: readonly string[]
}

/**
 * Live data for dashboards. A dashboard names a source and its settings; refreshing it
 * fetches the data again and stores it as a new version, so every version stays a
 * snapshot that replays the same. Dashboards without a source keep the data they were
 * given.
 */
export interface DashboardSource<Settings = Record<string, unknown>> {
  kind: string
  title: LocalizedText
  /** For the model: what data it gives, and which named data sets come back. */
  description: string
  /**
   * The model chooses these and they are stored in the event log with the dashboard, so
   * they must not hold secrets: fields marked `secret` are refused. Keep credentials in
   * the plugin's own configuration instead.
   */
  settings: SettingsSchema
  /**
   * Fetch now. Returns named data sets: arrays of rows, or one object for a stat. Ariadne
   * gives up after 30 seconds and aborts `signal`, so stop the work then.
   */
  fetch(
    settings: Settings,
    context: { projectId: string; signal?: AbortSignal },
  ): Promise<Record<string, unknown>>
}

/** One hit of a web search. */
export interface SearchResult {
  title: string
  url: string
  /** A few lines from the page about the query. */
  snippet: string
}

/**
 * Web search for threads and the coordinator. Ariadne ships none: a plugin brings the
 * service, and keeps its credentials (say, an API key from the environment or its own
 * file) to itself. Without one, agents have no search tool, only `fetch_url`.
 */
export interface SearchProvider {
  kind: string
  title: LocalizedText
  /**
   * Search now and return the best hits first, at most `limit`. Ariadne gives up after
   * 30 seconds and aborts `signal`. A thrown error's message is not shown to the model.
   */
  search(query: string, options: { limit: number; signal?: AbortSignal }): Promise<SearchResult[]>
}

/**
 * A pull request (or GitHub's kin: a GitLab merge request) as a link to it shows, without
 * asking its host: `label` is how a message names it, e.g. `daidr/Ariadne#34`.
 */
export interface LinkSummary {
  kind: 'pull-request'
  label: string
}

/** What the card over a pull request link shows. Titles are written by anyone who can open one. */
export interface PullRequestPreview {
  kind: 'pull-request'
  /** `owner/name`. */
  repository: string
  number: number
  /** How the host refers to it in text, `#<number>` when left out (see `PullRequestInfo`). */
  reference?: string
  url: string
  title: string
  state: 'open' | 'closed' | 'merged'
  draft: boolean
  author: string | null
  additions: number
  deletions: number
  changedFiles: number
  /** When it was merged, closed or opened, whichever is its state now; ISO time. */
  at: string
}

export type LinkPreview = PullRequestPreview

/**
 * Links in messages that point at something a host knows, such as a pull request. Ariadne
 * asks `match` for every full https address it shows, so it must answer from the address
 * alone; `preview` is asked only when the user points at the link. It keeps its
 * credentials to itself, like a search provider.
 */
export interface LinkProvider {
  kind: string
  title: LocalizedText
  match(url: URL): LinkSummary | null
  /**
   * What the link points at now; `null` when it is not there (or not visible). Ariadne
   * gives up after 10 seconds and aborts `signal`. A thrown error's message is not shown.
   */
  preview(url: URL, options: { signal?: AbortSignal }): Promise<LinkPreview | null>
}

/** What `setup` receives: every method registers something and returns its disposer. */
export interface PluginContext {
  /** A directory only this plugin writes to, e.g. for checkouts. Created on demand. */
  dataDir: string
  /** Run commands a thread asked for through this. */
  sandbox: CommandSandbox
  registerContextProvider(provider: ContextProvider<never>): Dispose
  registerModelProvider(provider: ModelProvider<never>): Dispose
  registerDashboardSource(source: DashboardSource<never>): Dispose
  registerSearchProvider(provider: SearchProvider): Dispose
  registerLinkProvider(provider: LinkProvider): Dispose
}

export interface Plugin {
  id: string
  name: LocalizedText
  /** Register contributions. A returned function runs when the plugin unloads. */
  setup(ctx: PluginContext): void | Dispose | Promise<void | Dispose>
}

export function definePlugin(plugin: Plugin): Plugin {
  return plugin
}
