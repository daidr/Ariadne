import type {
  AnyStoredEvent,
  ArtifactCommentAction,
  ArtifactCommentThreadView,
  ArtifactKind,
  ArtifactSummary,
  ArtifactVersionView,
  ArtifactView,
  BlockAnswer,
  ConversationView,
  LineComment,
  LiveUpdate,
  DraftUpdate,
  FileSourceSet,
  BackgroundUpdate,
  BackgroundView,
  MessageView,
  NeedsMeItem,
  PlanView,
  ProjectActivity,
  ProjectView,
  ReviewRoundView,
  SentLineComment,
  SearchResult,
  ThreadView,
  TodoView,
  TraceRequestDetail,
  TraceTurn,
  MemoryView,
  NoticeView,
  ProjectDetected,
  ProjectSettings,
  StoredFile,
} from '@ariadne/core'
import type { LocalizedMessage } from '@ariadne/i18n'
import type {
  BranchChanges,
  DirectoryListing,
  FileVersions,
  LinkPreview,
  LinkSummary,
  LocalizedText,
  MergeMethod,
  MergeOutcome,
  SettingsSchema,
  WorktreeInfo,
} from '@ariadne/plugin-sdk'
import {
  DataService,
  MetaService,
  PluginService,
  SettingsService,
  UpdateService,
  type BackupInfo as ProtoBackupInfo,
  type StorageSnapshot as ProtoStorageSnapshot,
} from '@ariadne/proto'
import {
  ApiError,
  authorizationHeader,
  createRpcClient,
  getToken,
  initToken,
  isValidToken,
  parseTokenInput,
  saveToken,
  setToken,
} from './rpc'
import {
  fromProtoConcurrency,
  fromProtoDirectoryListing,
  fromProtoFoundSetup,
  fromProtoInputSettings,
  fromProtoLinkPreview,
  fromProtoLinkSummary,
  fromProtoListedModel,
  fromProtoMcpServerStatus,
  fromProtoMcpServerView,
  fromProtoModelInfo,
  fromProtoModelProblems,
  fromProtoModelSettings,
  fromProtoPluginConfig,
  fromProtoPluginRecord,
  fromProtoSearchCheckResult,
  fromProtoSearchSettings,
  fromProtoSourceKind,
  fromProtoUpdateStatus,
  fromProtoWorkSettings,
  toJsonObject,
  toProtoMcpServerInput,
  toProtoSaveModelSettingsRequest,
  toProtoSearchInput,
} from './rpc-settings'
import { synchronizeServerClock } from './server-clock'

export { ApiError, getToken, initToken, isValidToken, parseTokenInput, saveToken, setToken }
const token = getToken

/** How a tool of an external server may be used; unmarked tools are `ask`. */
export type McpToolMark = 'read' | 'ask' | 'off'

/** An external tool server as the settings hold it, without its secret values. */
export interface McpServerView {
  id: string
  name: string
  transport: 'stdio' | 'http'
  command?: string
  args?: string[]
  cwd?: string
  url?: string
  /** Names of its environment variables and headers; their values stay on the server. */
  env: string[]
  headers: string[]
  tools: Record<string, McpToolMark>
}

/** What the page sends: an empty value keeps the one stored under that name. */
export type McpServerInput = Omit<McpServerView, 'env' | 'headers'> & {
  env: { name: string; value: string }[]
  headers: { name: string; value: string }[]
}

export interface McpServerStatus {
  id: string
  state: 'idle' | 'connecting' | 'ready' | 'failed'
  error: string | null
  log: string | null
  info: { name: string; version: string; protocolVersion: string } | null
  tools: { name: string; title?: string; description?: string }[] | null
}

export interface McpSettings {
  servers: McpServerView[]
  status: McpServerStatus[]
}

export interface UpdatePreferences {
  channel: 'stable' | 'preview'
  automatic: boolean
}

export interface UpdateStatus {
  version: string
  preferences: UpdatePreferences
  canInstall: boolean
  state:
    | 'idle'
    | 'checking'
    | 'current'
    | 'no-release'
    | 'available'
    | 'downloading'
    | 'installing'
    | 'ready'
  downloadPercent: number
  failure?: 'check' | 'download' | 'downloadTimeout' | 'verification' | 'install' | 'permission'
  release: {
    version: string
    notes: string
    url: string
    prerelease: boolean
    installable: boolean
  } | null
}

const dataClient = createRpcClient(DataService)
const settingsClient = createRpcClient(SettingsService)
const pluginClient = createRpcClient(PluginService)
const updateClient = createRpcClient(UpdateService)
const metaClient = createRpcClient(MetaService)

function fromProtoBackupInfo(item: ProtoBackupInfo): BackupInfo {
  return {
    name: item.name,
    size: Number(item.size),
    createdAt: Number(item.createdAt),
    auto: item.auto,
  }
}

function fromProtoStorageSnapshot(snapshot?: ProtoStorageSnapshot): StorageSnapshot | null {
  if (!snapshot) return null
  const worktrees: Record<string, DirectorySize> = {}
  for (const [path, size] of Object.entries(snapshot.worktrees)) {
    worktrees[path] = {
      bytes: size.bytes === undefined ? null : Number(size.bytes),
      exclusive: size.exclusive === undefined ? null : Number(size.exclusive),
      ...(size.reclaimableEstimate === undefined
        ? {}
        : { reclaimableEstimate: Number(size.reclaimableEstimate) }),
    }
  }
  return {
    managed: snapshot.managed === undefined ? null : Number(snapshot.managed),
    total: snapshot.total === undefined ? null : Number(snapshot.total),
    worktrees,
    logical: snapshot.logical,
    measuredAt: Number(snapshot.measuredAt),
  }
}

/** A query string of the values given, or nothing when none is. */
function queryString(values: Record<string, string | undefined>): string {
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries(values)) if (value !== undefined) params.set(key, value)
  const text = params.toString()
  return text ? `?${text}` : ''
}

async function request<T>(
  method: string,
  path: string,
  body?: unknown,
  signal?: AbortSignal,
): Promise<T> {
  const headers: Record<string, string> = { authorization: authorizationHeader() }
  // A file goes as it is, with its own type; anything else as JSON.
  const raw = body instanceof Blob
  if (body !== undefined) {
    headers['content-type'] = raw ? body.type || 'application/octet-stream' : 'application/json'
  }
  const response = await fetch(path, {
    method,
    headers,
    ...(signal ? { signal } : {}),
    ...(body === undefined ? {} : { body: raw ? body : JSON.stringify(body) }),
  })
  if (response.ok) synchronizeServerClock(response.headers.get('X-Ariadne-Server-Time'))
  if (response.status === 204) return undefined as T
  const data = await response.json()
  if (!response.ok) throw new ApiError(response.status, data.error)
  return data as T
}

export async function fetchStoredFile(id: string, method: 'GET' | 'HEAD', signal: AbortSignal) {
  const response = await fetch(`/api/files/${encodeURIComponent(id)}`, {
    method,
    headers: { authorization: authorizationHeader() },
    signal,
  })
  if (!response.ok) throw new Error(`File request failed (${response.status})`)
  return response
}

async function download(path: string, fallbackName: string) {
  const response = await fetch(path, { headers: { authorization: authorizationHeader() } })
  if (!response.ok) throw new ApiError(response.status, (await response.json()).error)
  const name = /filename="([^"]+)"/.exec(response.headers.get('content-disposition') ?? '')?.[1]
  return { blob: await response.blob(), name: name ?? fallbackName }
}

export function fetchSheet(id: string, index: number, signal: AbortSignal) {
  return request<{ sheets: string[]; rows: string[][]; rowNumbers: number[]; truncated: boolean }>(
    'GET',
    `/api/files/${encodeURIComponent(id)}/sheet?index=${index}`,
    undefined,
    signal,
  )
}

/** One project's threads' worktrees, as the settings list them. */
export interface ProjectWorktrees {
  id: string
  name: string
  worktrees: (WorktreeInfo & { thread: ThreadView | null; sourceId?: string })[]
  /** Whether the source supports cloning on the worktree volume, when probed. */
  shared: boolean | null
  /** Why they could not be listed, e.g. the remote machine was unreachable. */
  error: LocalizedMessage | null
}

export interface DirectorySize {
  bytes: number | null
  exclusive: number | null
  reclaimableEstimate?: number
}

export interface StorageSnapshot {
  managed: number | null
  total: number | null
  worktrees: Record<string, DirectorySize>
  logical: boolean
  measuredAt: number
}

export interface ProjectDetail {
  project: ProjectView
  threads: ThreadView[]
  needsMe: NeedsMeItem[]
  /** Whether the project's source has issues that plan nodes can link to. */
  hasTracker: boolean
}

/** How the threads of a project detail were sent; see `patchThreads`. */
export interface ThreadSync {
  /** Given back as `since` next time. */
  cursor: string
  /** `threads` holds only the ones changed since the cursor asked with. */
  partial: boolean
  /** Every thread's id in order, when a partial answer's set of threads changed. */
  ids?: string[]
}

export interface ModelKind {
  kind: string
  title: LocalizedText
  settings: SettingsSchema
  /** An agent program: request headers, body fields and timeout do not apply. */
  program?: boolean
}

/** A source as the form sends it: secrets typed in, `null` to clear one. */
export interface SourceInput {
  kind: string
  settings: Record<string, unknown>
  hosting?: { kind: string; settings: Record<string, unknown> } | null
}

/** A kind of context source a plugin offers. */
export type { DirectoryListing }

export interface SourceKind extends ModelKind {
  plugin?: { id: string; ready: boolean }
  defaults?: Record<string, unknown>
  provides: string[]
  /** Threads' commands run on another machine. */
  remote: boolean
  /** Can bring its repository and issues to a remote source. */
  hosting: boolean
  /** Can list directories for its `directory` settings. */
  browse: boolean
  /** One-line name of a source, `{setting}` replaced by its value. */
  summary: string | null
}

export interface ModelProviderInput {
  id: string
  kind: string
  name: string
  settings: Record<string, unknown>
  /** Sent with every request; an empty value keeps the stored one. */
  headers?: { name: string; value: string }[]
  extraBody?: Record<string, unknown>
  timeoutSeconds?: number | null
}

/** A plugin as the server reports it; see `PluginRecord` in the agent package. */
export interface PluginRecord {
  id: string | null
  name: LocalizedText | null
  source: 'builtin' | 'path'
  path: string | null
  status: 'loaded' | 'failed'
  error: string | null
  configurable?: boolean
  provides: {
    models: string[]
    contexts: string[]
    dashboards: string[]
    searches: string[]
    links: string[]
  }
}

export interface PluginConfig {
  name: LocalizedText
  schema: SettingsSchema
  fieldOrder?: string[]
  values: Record<string, unknown>
  secretsSet: string[]
  ready: boolean
}

/** Worker turns that run at once, plus reserved worker replies; coordinators are independent. */
export interface Concurrency {
  slots: number
  reserved: number
}

/** A request the user sends often, offered after `/` in the message input. */
export interface Snippet {
  id: string
  name: string
  text: string
}

/** The app's keys the user can bind elsewhere. */
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

/** Which key sends a message, the snippets kept for reuse, and the app's keys. */
export interface InputSettings {
  submitOn: 'enter' | 'mod-enter'
  snippets: Snippet[]
  keys: Record<ShortcutAction, string>
}

export type CommandAction = 'allow' | 'ask' | 'deny'

export interface CommandRule {
  pattern: string
  action: CommandAction
}

/** Numbers that bound the work; seconds, characters, a share of the context, counts. */
export interface Limits {
  commandTimeout: number
  commandTimeoutMax: number
  commandOutput: number
  compactShare: number
  modelRetries: number
  loopRepeats: number
}

export interface SearchCapabilities {
  maxQueryRunes?: number
  maxResults?: number
  returnsAnswer?: boolean
  supportsCheck?: boolean
}

export interface SearchServiceView {
  kind: string
  title: LocalizedText
  description?: LocalizedText
  settings: SettingsSchema
  capabilities?: SearchCapabilities
  values: Record<string, unknown>
  secretsSet: string[]
  ready: boolean
}

/** Web search in the settings: registered search plugins, active selection, and fallback models. */
export interface SearchSettings {
  provider: string
  ready: boolean
  keySet: boolean
  needed: boolean
  models: string[]
  plugins: LocalizedText[]
  services: SearchServiceView[]
}

export interface SearchSettingsInput {
  provider: string
  services: Record<string, Record<string, unknown>>
}

export interface SearchCheckResult {
  ok: boolean
  status: number | null
  message?: LocalizedText
}

export interface WorkSettings {
  instructions: string
  rules: CommandRule[]
  limits: Limits
  defaults: Limits
}

export type ToolGroup = 'fetch' | 'search' | 'commands' | 'issueWrites'

export interface ProjectSettingsView {
  settings: ProjectSettings
  /** What Ariadne found out about the repository, shown where the user wrote nothing. */
  detected: ProjectDetected
  /** Names of the project's command environment variables; values stay on the server. */
  env: string[]
  /** The built-in tool groups a project can switch off. */
  tools: ToolGroup[]
  /**
   * The project's changes are merged on its code host, not here: nothing in the app
   * remembers how, so its settings ask.
   */
  hostedMerge: boolean
}

export interface BackupInfo {
  name: string
  size: number
  createdAt: number
  auto: boolean
}

export interface DataUsage {
  home: string
  database: number
  plugins: number | null
  backups: BackupInfo[]
  backupsDir: string
}

/** One model on one of the providers; `note` says what it is for, for the coordinator. */
export interface ModelEntry {
  id: string
  provider: string
  model: string
  contextTokens?: number
  note?: string
  /** `false` when the model cannot look at images. */
  images?: false
  /** The most it may write in one response. */
  maxTokens?: number
  /** What it costs per million tokens, for estimates. */
  price?: ModelPrice
  /** The efforts it accepts, least first; empty when none. The common ones without. */
  efforts?: Effort[]
}

/** What the public catalog knows of a model, each only where it says. */
export interface ModelInfo {
  contextTokens?: number
  maxTokens?: number
  images?: boolean
  price?: ModelPrice
  tools?: boolean
  released?: string
  efforts?: Effort[]
}

/** A model a provider lists, with what the catalog knows of it. */
export interface ListedModel {
  id: string
  /** What the service calls it, when that says more than the name. */
  title?: string
  info?: ModelInfo
}

/** A model just saved that did not answer when asked. */
export interface ModelProblem {
  model: string
  provider: string
  problem: LocalizedMessage
}

/** A way to talk to a model found on this machine: a key in the environment, a program signed in. */
export interface FoundSetup {
  id: string
  kind: string
  title: LocalizedText
  from: { env: string } | { program: string }
}

export type Currency = 'USD' | 'CNY'

/** Per million tokens; the cache ones cost as much as `input` when left out. */
export interface ModelPrice {
  currency: Currency
  input: number
  output: number
  cacheRead?: number
  cacheWrite?: number
}

export interface ModelSettingsInput {
  providers: ModelProviderInput[]
  models: ModelEntry[]
  /**
   * `coordinator.model: null` uses the threads' model, `triage.model: null` the
   * coordinator's, `research.model: null` the asking conversation's; `effort: null` asks
   * nothing.
   */
  roles: { thread: RoleInput; coordinator: RoleInput; triage: RoleInput; research: RoleInput }
  /** Usage also shows the estimated cost. */
  showCost: boolean
}

export type Effort = 'low' | 'medium' | 'high' | 'xhigh' | 'max'

export interface RoleInput {
  model: string | null
  effort: Effort | null
  /** Goes on with this when the model keeps being turned away; research has none. */
  fallback: string | null
}

export interface ModelSettings extends Omit<ModelSettingsInput, 'providers'> {
  kinds: ModelKind[]
  providers: (Omit<ModelProviderInput, 'headers'> & {
    secretsSet: string[]
    /** Names of the headers stored; their values stay on the server. */
    headers: string[]
    extraBody: Record<string, unknown>
    timeoutSeconds: number | null
  })[]
}

export interface ArtifactDetail {
  artifact: ArtifactView
  versions: ArtifactVersionView[]
  comments: ArtifactCommentThreadView[]
}

export const api = {
  updateStatus: async (signal?: AbortSignal): Promise<UpdateStatus> => {
    const response = await updateClient.getUpdateStatus({}, signal ? { signal } : undefined)
    return fromProtoUpdateStatus(response.status)
  },
  saveUpdatePreferences: async (
    preferences: UpdatePreferences,
    signal?: AbortSignal,
  ): Promise<UpdateStatus> => {
    const response = await updateClient.saveUpdatePreferences(
      { channel: preferences.channel, automatic: preferences.automatic },
      signal ? { signal } : undefined,
    )
    return fromProtoUpdateStatus(response.status)
  },
  checkForUpdate: async (signal?: AbortSignal): Promise<UpdateStatus> => {
    const response = await updateClient.checkForUpdate({}, signal ? { signal } : undefined)
    return fromProtoUpdateStatus(response.status)
  },
  installUpdate: async (version: string, signal?: AbortSignal): Promise<UpdateStatus> => {
    const response = await updateClient.installUpdate({ version }, signal ? { signal } : undefined)
    return fromProtoUpdateStatus(response.status)
  },
  listMemories: (projectId: string) =>
    request<{ memories: MemoryView[] }>('GET', `/api/projects/${projectId}/memory`),
  addMemory: (projectId: string, text: string) =>
    request<{ memory: MemoryView }>('POST', `/api/projects/${projectId}/memory`, { text }),
  saveMemory: (projectId: string, memoryId: string, text: string) =>
    request<{ memory: MemoryView }>('PUT', `/api/projects/${projectId}/memory/${memoryId}`, {
      text,
    }),
  resumeThread: (threadId: string) =>
    request<{ thread: ThreadView }>('POST', `/api/threads/${threadId}/resume`),
  removeThreadReference: (threadId: string, referenceId: string) =>
    request<{ thread: ThreadView }>('DELETE', `/api/threads/${threadId}/references/${referenceId}`),
  keepThreadReference: (threadId: string, referenceId: string) =>
    request<{ thread: ThreadView }>(
      'POST',
      `/api/threads/${threadId}/references/${referenceId}/keep`,
    ),
  completeThread: (threadId: string) =>
    request<{ thread: ThreadView }>('POST', `/api/threads/${threadId}/complete`),
  endAside: (threadId: string) =>
    request<{ thread: ThreadView }>('DELETE', `/api/threads/${threadId}/aside`),
  undoNotice: (projectId: string, noticeId: string) =>
    request<void>('POST', `/api/projects/${projectId}/notices/${noticeId}/undo`),
  removeMemory: (projectId: string, memoryId: string) =>
    request<void>('DELETE', `/api/projects/${projectId}/memory/${memoryId}`),
  uploadFile: (projectId: string, file: Blob, forMessage = false) =>
    request<{ file: StoredFile }>(
      'POST',
      `/api/projects/${projectId}/files${forMessage ? '?for=message' : ''}`,
      file,
    ),
  ping: async (): Promise<{ ok: boolean }> => {
    const response = await metaClient.ping({})
    return { ok: response.ok }
  },
  listProjects: (ids: readonly string[] = []) =>
    request<{ projects: ProjectView[]; lastSeq: number }>(
      'GET',
      '/api/projects' + (ids.length ? '?' + new URLSearchParams(ids.map((id) => ['id', id])) : ''),
    ),
  /**
   * A new project, with where its files are and the sources it only reads; each is
   * connected first, and the project is made only when all of them can be.
   */
  createProject: (input: {
    name: string
    source?: SourceInput
    references?: { kind: string; settings: Record<string, unknown> }[]
    sources?: (SourceInput & { id?: string })[]
    primaryIndex?: number
  }) => request<{ project: ProjectView }>('POST', '/api/projects', input),
  renameProject: (id: string, name: string) =>
    request<{ project: ProjectView }>('PUT', `/api/projects/${id}`, { name }),
  deleteProject: (id: string) => request<void>('DELETE', `/api/projects/${id}`),
  /** With `since` (a `sync.cursor` given before), only the threads changed after it. */
  getProject: (id: string, since?: string) =>
    request<ProjectDetail & { sync: ThreadSync }>(
      'GET',
      `/api/projects/${id}${since ? `?since=${encodeURIComponent(since)}` : ''}`,
    ),
  getProjectActivity: (id: string, signal: AbortSignal) =>
    request<ProjectActivity>('GET', `/api/projects/${id}/activity`, undefined, signal),
  /**
   * The newest messages, the page before `before`, or everything from `from` on. With
   * `since` (a `lastSeq` given before), only messages and notices changed after it.
   */
  getConversation: (id: string, cursor: { before?: string; from?: string; since?: number } = {}) =>
    request<{
      conversation: ConversationView
      messages: MessageView[]
      notices: NoticeView[]
      /** Last read coordinator reply, on the initial page. */
      readThrough?: string
      /** Earlier messages exist before the first one here; not said with `since`. */
      more: boolean
      /** The newest event this answer holds; changes after it come with `since`. */
      lastSeq: number
      todo: TodoView
      /** The model it uses now, with how much its context holds. */
      model: { id: string; contextTokens: number; compactAt: number } | null
    }>(
      'GET',
      `/api/conversations/${id}${queryString({
        before: cursor.before,
        from: cursor.from,
        since: cursor.since === undefined ? undefined : String(cursor.since),
      })}`,
    ),
  getTrace: (id: string, options: { before?: number; from?: number; signal?: AbortSignal } = {}) =>
    request<{ turns: TraceTurn[]; more: boolean }>(
      'GET',
      `/api/conversations/${id}/trace${queryString({
        before: options.before?.toString(),
        from: options.from?.toString(),
      })}`,
      undefined,
      options.signal,
    ),
  getTraceRequest: (id: string, seq: number) =>
    request<{ request: TraceRequestDetail }>(
      'GET',
      `/api/conversations/${id}/trace/requests/${seq}`,
    ),
  exportTrace: (id: string) =>
    download(
      `/api/conversations/${encodeURIComponent(id)}/trace/export`,
      `ariadne-trace-${id}.zip`,
    ),
  setThreadModel: (threadId: string, model: string | null) =>
    request<{ thread: ThreadView }>('PUT', `/api/threads/${threadId}/model`, { model }),
  stopTurn: (id: string) => request<{ stopped: boolean }>('POST', `/api/conversations/${id}/stop`),
  wakeNow: (id: string) => request<void>('POST', `/api/conversations/${id}/wait/now`),
  cancelWait: (id: string) => request<void>('DELETE', `/api/conversations/${id}/wait`),
  commandOutput: (threadId: string, commandId: string, signal?: AbortSignal) =>
    request<{ command: BackgroundView; tail: string }>(
      'GET',
      `/api/threads/${threadId}/commands/${commandId}`,
      undefined,
      signal,
    ),
  pullRequestChecks: (threadId: string, signal?: AbortSignal) =>
    request<{
      headSha: string
      checks: { id: string; name: string; status: string; conclusion: string | null }[]
    }>('GET', `/api/threads/${threadId}/pull-request/checks`, undefined, signal),
  stopCommand: (threadId: string, commandId: string) =>
    request<{ command: BackgroundView }>(
      'POST',
      `/api/threads/${threadId}/commands/${commandId}/stop`,
    ),
  retryTurn: (id: string) => request<void>('POST', `/api/conversations/${id}/retry`),
  react: (messageId: string, emoji: string, remove: boolean) =>
    request<{ message: MessageView }>('POST', `/api/messages/${messageId}/reactions`, {
      emoji,
      remove,
    }),
  getConcurrency: async (): Promise<Concurrency> =>
    fromProtoConcurrency((await settingsClient.getConcurrency({})).concurrency),
  saveConcurrency: async (input: Concurrency): Promise<Concurrency> =>
    fromProtoConcurrency(
      (await settingsClient.saveConcurrency({ slots: input.slots, reserved: input.reserved }))
        .concurrency,
    ),
  getInputSettings: async (): Promise<InputSettings> =>
    fromProtoInputSettings((await settingsClient.getInputSettings({})).input),
  saveInputSettings: async (input: InputSettings): Promise<InputSettings> =>
    fromProtoInputSettings(
      (
        await settingsClient.saveInputSettings({
          submitOn: input.submitOn,
          snippets: input.snippets.map((snippet) => ({
            id: snippet.id,
            name: snippet.name,
            text: snippet.text,
          })),
          keys: { ...input.keys },
        })
      ).input,
    ),
  getModelSettings: async (): Promise<ModelSettings> =>
    fromProtoModelSettings((await settingsClient.getModelSettings({})).settings),
  getSearchSettings: async (): Promise<SearchSettings> =>
    fromProtoSearchSettings((await settingsClient.getSearchSettings({})).search),
  /** `null` clears the stored key. */
  saveSearchSettings: async (input: SearchSettingsInput): Promise<SearchSettings> =>
    fromProtoSearchSettings(
      (await settingsClient.saveSearchSettings(toProtoSearchInput(input))).search,
    ),
  checkSearchSettings: async (input: SearchSettingsInput): Promise<SearchCheckResult> =>
    fromProtoSearchCheckResult(await settingsClient.checkSearchSettings(toProtoSearchInput(input))),
  listPlugins: async (): Promise<{ plugins: PluginRecord[] }> => {
    const response = await pluginClient.listPlugins({})
    return { plugins: response.plugins.map(fromProtoPluginRecord) }
  },
  getPluginConfig: async (id: string): Promise<PluginConfig> =>
    fromProtoPluginConfig((await pluginClient.getPluginConfig({ pluginId: id })).config),
  savePluginConfig: async (id: string, values: Record<string, unknown>): Promise<PluginConfig> =>
    fromProtoPluginConfig(
      (await pluginClient.savePluginConfig({ pluginId: id, values: toJsonObject(values) })).config,
    ),
  addPlugin: async (path: string): Promise<{ plugins: PluginRecord[] }> => {
    const response = await pluginClient.addPlugin({ path })
    return { plugins: response.plugins.map(fromProtoPluginRecord) }
  },
  removePlugin: async (path: string): Promise<{ plugins: PluginRecord[] }> => {
    const response = await pluginClient.deletePlugin({ path })
    return { plugins: response.plugins.map(fromProtoPluginRecord) }
  },
  /** What the public catalog knows of a model, by the name typed. */
  modelInfo: async (model: string, hint: string): Promise<{ info: ModelInfo }> => {
    const response = await settingsClient.getModelInfo({ model, hint })
    return { info: fromProtoModelInfo(response.info) }
  },
  /** The models a provider in the form can use; `null` when it cannot say. */
  listModels: async (
    provider: Pick<ModelProviderInput, 'id' | 'kind' | 'settings'>,
  ): Promise<{ models: ListedModel[] | null }> => {
    const response = await settingsClient.listProviderModels({
      provider: {
        id: provider.id,
        kind: provider.kind,
        settings: toJsonObject(provider.settings),
      },
    })
    return {
      models: response.models ? response.models.items.map(fromProtoListedModel) : null,
    }
  },
  /** Saved as sent; `problems` names the new models that did not answer. */
  saveModelSettings: async (
    input: ModelSettingsInput,
  ): Promise<ModelSettings & { problems: ModelProblem[] }> => {
    const response = await settingsClient.saveModelSettings(toProtoSaveModelSettingsRequest(input))
    return {
      ...fromProtoModelSettings(response.settings),
      problems: fromProtoModelProblems(response.problems),
    }
  },
  /** Setups already on this machine that no provider uses yet. */
  foundSetups: async (): Promise<{ found: FoundSetup[] }> => {
    const response = await settingsClient.listFoundSetups({})
    return { found: response.found.map(fromProtoFoundSetup) }
  },
  useFoundSetup: async (id: string): Promise<ModelSettings & { problems: ModelProblem[] }> => {
    const response = await settingsClient.useFoundSetup({ id })
    return {
      ...fromProtoModelSettings(response.settings),
      problems: fromProtoModelProblems(response.problems),
    }
  },
  postMessage: (
    projectId: string,
    conversationId: string,
    content: string,
    extras: MessageExtras = {},
  ) =>
    request<{ message: MessageView }>('POST', `/api/projects/${projectId}/messages`, {
      conversationId,
      content,
      ...(extras.attachments?.length ? { attachments: extras.attachments } : {}),
      ...(extras.mentions?.length ? { mentions: extras.mentions } : {}),
      ...(extras.comments?.length ? { comments: extras.comments } : {}),
    }),
  /** Line comments sent as the page closes, in a request that outlives it. */
  postCommentsLeaving: (projectId: string, conversationId: string, comments: LineComment[]) =>
    void fetch(`/api/projects/${projectId}/messages`, {
      method: 'POST',
      keepalive: true,
      headers: { authorization: authorizationHeader(), 'content-type': 'application/json' },
      body: JSON.stringify({ conversationId, content: '', comments }),
    }).catch(() => {}),
  mentionFiles: (projectId: string, conversationId: string, query: string) =>
    request<{ files: string[] }>(
      'GET',
      `/api/projects/${projectId}/mention-files?${new URLSearchParams({ conversationId, q: query })}`,
    ),
  markRead: (conversationId: string, upTo: string) =>
    request<void>('POST', `/api/conversations/${conversationId}/read`, { upTo }),
  /** The user is back at the project after `away` ms without touching it. */
  noteReturn: (projectId: string, away: number) =>
    request<void>('POST', `/api/projects/${projectId}/return`, { away }),
  answerAttention: (id: string, answer: BlockAnswer) =>
    request<void>('POST', `/api/attentions/${id}/answer`, { answer }),
  sourceKinds: async (): Promise<{ kinds: SourceKind[] }> => {
    const response = await metaClient.listSourceKinds({})
    return { kinds: response.kinds.map(fromProtoSourceKind) }
  },
  browseSource: async (
    kind: string,
    body: {
      projectId: string | null
      sourceId?: string
      settings: Record<string, unknown>
      path: string
    },
  ): Promise<{ listing: DirectoryListing | null }> => {
    const response = await metaClient.browseSource({
      kind,
      ...(body.projectId ? { projectId: body.projectId } : {}),
      ...(body.sourceId !== undefined ? { sourceId: body.sourceId } : {}),
      settings: toJsonObject(body.settings),
      path: body.path,
    })
    return { listing: fromProtoDirectoryListing(response.listing) }
  },
  browseLocal: async (path: string): Promise<{ listing: DirectoryListing | null }> => {
    const response = await metaClient.browseLocal({ path })
    return { listing: fromProtoDirectoryListing(response.listing) }
  },
  getFileSources: (projectId: string) =>
    request<FileSourceSet & { secretsSet: Record<string, string[]> }>(
      'GET',
      `/api/projects/${projectId}/sources`,
    ),
  setFileSources: (
    projectId: string,
    body: { sources: (SourceInput & { id?: string })[]; primaryIndex?: number },
  ) => request<{ project: ProjectView }>('PUT', `/api/projects/${projectId}/sources`, body),
  addReference: (
    projectId: string,
    reference: { kind: string; settings: Record<string, unknown> },
  ) =>
    request<{ project: ProjectView }>('POST', `/api/projects/${projectId}/references`, {
      reference,
    }),
  removeReference: (projectId: string, referenceId: string) =>
    request<{ project: ProjectView }>(
      'DELETE',
      `/api/projects/${projectId}/references/${referenceId}`,
    ),
  /** The reference sources that cannot be read right now, with why. */
  referenceStatus: (projectId: string) =>
    request<{ failed: { id: string; error: LocalizedMessage }[] }>(
      'GET',
      `/api/projects/${projectId}/references/status`,
    ),
  refreshDashboard: (artifactId: string) =>
    request<{ artifact: unknown }>('POST', `/api/artifacts/${artifactId}/refresh`),
  getMcpSettings: async (): Promise<McpSettings> => {
    const response = await settingsClient.getMcpSettings({})
    return {
      servers: response.servers.map(fromProtoMcpServerView),
      status: response.status.map(fromProtoMcpServerStatus),
    }
  },
  saveMcpSettings: async (servers: McpServerInput[]): Promise<McpSettings> => {
    const response = await settingsClient.saveMcpSettings({
      servers: servers.map(toProtoMcpServerInput),
    })
    return {
      servers: response.servers.map(fromProtoMcpServerView),
      status: response.status.map(fromProtoMcpServerStatus),
    }
  },
  checkMcpServer: async (id: string): Promise<{ status: McpServerStatus }> => {
    const response = await settingsClient.checkMcpServer({ serverId: id })
    return { status: fromProtoMcpServerStatus(response.status) }
  },
  setProjectMcp: (projectId: string, servers: string[]) =>
    request<{ project: ProjectView }>('PUT', `/api/projects/${projectId}/mcp`, { servers }),
  getWorkSettings: async (): Promise<WorkSettings> =>
    fromProtoWorkSettings((await settingsClient.getWorkSettings({})).work),
  saveWorkSettings: async (input: Partial<Omit<WorkSettings, 'defaults'>>): Promise<WorkSettings> =>
    fromProtoWorkSettings(
      (
        await settingsClient.saveWorkSettings({
          ...(input.instructions !== undefined ? { instructions: input.instructions } : {}),
          ...(input.rules !== undefined ? { rules: { items: input.rules } } : {}),
          ...(input.limits !== undefined
            ? {
                limits: {
                  commandTimeout: input.limits.commandTimeout,
                  commandTimeoutMax: input.limits.commandTimeoutMax,
                  commandOutput: input.limits.commandOutput,
                  compactShare: input.limits.compactShare,
                  modelRetries: input.limits.modelRetries,
                  loopRepeats: input.limits.loopRepeats,
                },
              }
            : {}),
        })
      ).work,
    ),
  getDataUsage: async (sizes: boolean, signal?: AbortSignal): Promise<DataUsage> => {
    const response = await dataClient.getDataUsage(
      { includeSizes: sizes },
      signal ? { signal } : undefined,
    )
    return {
      home: response.home,
      database: Number(response.database),
      plugins: response.plugins === undefined ? null : Number(response.plugins),
      backups: response.backups.map(fromProtoBackupInfo),
      backupsDir: response.backupsDir,
    }
  },
  getStorage: async (
    cached: boolean,
    signal: AbortSignal,
  ): Promise<{ storage: StorageSnapshot | null }> => {
    const response = await dataClient.getStorageSnapshot({ cachedOnly: cached }, { signal })
    return { storage: fromProtoStorageSnapshot(response.storage) }
  },
  measureReclaimable: async (
    path: string,
    signal: AbortSignal,
  ): Promise<{ exclusive: number | null }> => {
    const response = await dataClient.measureWorktreeReclaimable({ path }, { signal })
    return { exclusive: response.exclusive === undefined ? null : Number(response.exclusive) }
  },
  backupData: async (): Promise<{ backup: BackupInfo }> => {
    const response = await dataClient.createBackup({})
    if (!response.backup) {
      throw new ApiError(500, { key: 'error.invalidBody', params: { reason: 'backup' } })
    }
    return { backup: fromProtoBackupInfo(response.backup) }
  },
  removeBackup: async (name: string): Promise<void> => {
    await dataClient.deleteBackup({ name })
  },
  getProjectSettings: (projectId: string) =>
    request<ProjectSettingsView>('GET', `/api/projects/${projectId}/settings`),
  saveProjectSettings: (
    projectId: string,
    input: { settings: ProjectSettings; env?: { name: string; value: string }[] },
  ) =>
    request<ProjectSettingsView & { project: ProjectView }>(
      'PUT',
      `/api/projects/${projectId}/settings`,
      input,
    ),
  /** The project's log as a file; the token goes in a header, so not as a plain link. */
  exportProject: (projectId: string) =>
    download(`/api/projects/${projectId}/export`, `ariadne-${projectId}.json`),
  setAutopilotPaused: (projectId: string, paused: boolean) =>
    request<{ project: ProjectView }>('PUT', `/api/projects/${projectId}/autopilot`, { paused }),
  setProjectSlots: (projectId: string, slots: number | null) =>
    request<{ project: ProjectView }>('PUT', `/api/projects/${projectId}/slots`, { slots }),
  getReviews: (threadId: string) =>
    request<{ rounds: ReviewRoundView[] }>('GET', `/api/threads/${threadId}/reviews`),
  getLineComments: (threadId: string) =>
    request<{ comments: SentLineComment[] }>('GET', `/api/threads/${threadId}/line-comments`),
  getSkills: (conversationId: string) =>
    request<{ skills: SkillView[] }>('GET', `/api/conversations/${conversationId}/skills`),
  getChanges: (
    threadId: string,
    known?: { head: string; baseHead: string },
    signal?: AbortSignal,
  ) =>
    request<{ changes: BranchChanges | null; mergeable: boolean; unchanged?: boolean }>(
      'GET',
      `/api/threads/${threadId}/changes${known ? queryString(known) : ''}`,
      undefined,
      signal,
    ),
  getChangedFile: (
    threadId: string,
    input: { from: string; head: string; path: string; previousPath: string | null },
    signal?: AbortSignal,
  ) => {
    const query = new URLSearchParams({
      from: input.from,
      head: input.head,
      path: input.path,
      ...(input.previousPath ? { previousPath: input.previousPath } : {}),
    })
    return request<{ file: FileVersions }>(
      'GET',
      `/api/threads/${threadId}/changes/file?${query}`,
      undefined,
      signal,
    )
  },
  mergeThread: (
    threadId: string,
    input: { method: MergeMethod; message: string; expectedHead: string },
  ) =>
    request<{ outcome: MergeOutcome; thread: ThreadView }>(
      'POST',
      `/api/threads/${threadId}/merge`,
      input,
    ),
  undoMerge: (threadId: string) =>
    request<{ thread: ThreadView }>('POST', `/api/threads/${threadId}/merge/undo`),
  listWorktrees: (sizes: boolean, signal?: AbortSignal) =>
    request<{ projects: ProjectWorktrees[]; windows: boolean }>(
      'GET',
      sizes ? '/api/worktrees' : '/api/worktrees?sizes=0',
      undefined,
      signal,
    ),
  removeWorktree: (projectId: string, threadId: string, sourceId?: string, orphaned?: boolean) =>
    request<void>(
      'DELETE',
      `/api/projects/${projectId}/worktrees/${threadId}${queryString({ sourceId, orphaned: orphaned ? '1' : undefined })}`,
    ),
  dismissProposal: (projectId: string, proposalId: string) =>
    request<{ plan: PlanView }>(
      'POST',
      `/api/projects/${projectId}/plan/proposals/${proposalId}/dismiss`,
    ),
  getPlan: (projectId: string) =>
    request<{ plan: PlanView }>('GET', `/api/projects/${projectId}/plan`),
  search: (projectId: string, q: string) =>
    request<SearchResult>('GET', `/api/projects/${projectId}/search${queryString({ q })}`),
  listArtifacts: (projectId: string) =>
    request<{ artifacts: ArtifactSummary[] }>('GET', `/api/projects/${projectId}/artifacts`),
  getArtifact: (
    id: string,
    options: { version?: number; signal?: AbortSignal; versions?: boolean } = {},
  ) =>
    request<ArtifactDetail>(
      'GET',
      `/api/artifacts/${id}${queryString({
        version: options.version === undefined ? undefined : String(options.version),
        versions: options.versions === false ? '0' : undefined,
      })}`,
      undefined,
      options.signal,
    ),
  createArtifact: (projectId: string, input: { kind: ArtifactKind; title: string }) =>
    request<ArtifactDetail>('POST', `/api/projects/${projectId}/artifacts`, input),
  saveArtifact: (
    id: string,
    input: {
      /** `null` keeps what a document or whiteboard edited together holds now. */
      content: string | null
      baseVersion: number
      reason: string
      title?: string
      quotes?: Record<string, string>
    },
  ) => request<ArtifactDetail>('PUT', `/api/artifacts/${id}`, input),
  /**
   * The user stopped for a moment in an artifact, or left it: what they did goes to the
   * agent, with pictures of the options they marked. `leaving` lets it go out as the page
   * closes, without the pictures.
   */
  sendFeedback: (id: string, images: string[], leaving = false) =>
    leaving
      ? void fetch(`/api/artifacts/${id}/feedback`, {
          method: 'POST',
          keepalive: true,
          headers: { authorization: authorizationHeader(), 'content-type': 'application/json' },
          body: JSON.stringify({ images: [] }),
        }).catch(() => {})
      : request<{ feedback: unknown }>('POST', `/api/artifacts/${id}/feedback`, { images }),
  /** Where a page joins an artifact's room, and the query that lets it in. */
  sharedRoom: (id: string) => ({
    server: `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/api/artifacts`,
    room: `${id}/live`,
    params: { token: token() ?? '' },
  }),
  commentArtifact: (id: string, action: ArtifactCommentAction) =>
    request<{ comments: ArtifactCommentThreadView[] }>(
      'POST',
      `/api/artifacts/${id}/comments`,
      action,
    ),
  summarizeLinks: async (
    urls: string[],
    signal?: AbortSignal,
  ): Promise<{ links: Record<string, LinkSummary | null> }> => {
    const response = await metaClient.summarizeLinks({ urls }, signal ? { signal } : undefined)
    const links: Record<string, LinkSummary | null> = {}
    for (const [url, entry] of Object.entries(response.links)) {
      links[url] = entry.summary ? fromProtoLinkSummary(entry.summary) : null
    }
    return { links }
  },
  previewLink: async (
    url: string,
    signal?: AbortSignal,
  ): Promise<{ preview: LinkPreview | null }> => {
    const response = await metaClient.previewLink({ url }, signal ? { signal } : undefined)
    return { preview: fromProtoLinkPreview(response.preview) }
  },
}

/** What a message carries besides its text. */
export interface MessageExtras {
  attachments?: { fileId: string; name: string }[]
  mentions?: MentionInput[]
  comments?: LineComment[]
}

/** Something picked after `@`, as the server takes it; it fills in titles. */
export type MentionInput =
  | { kind: 'thread' | 'artifact'; id: string }
  | { kind: 'file'; path: string }
  | { kind: 'skill'; name: string }

/** A skill the conversation can be asked to work by, with when to use it. */
export interface SkillView {
  name: string
  when: string
  source: 'repo' | 'local'
}

export type StreamStatus = 'connecting' | 'open' | 'lost' | 'unauthorized'

/**
 * Follow the event log from `afterSeq`. EventSource retries network drops on its own but
 * gives up for good on a non-200 answer (a 401, or a 502 while the dev server restarts),
 * so a closed source is reopened here with backoff, resuming after the last event seen.
 */
export function followEvents(
  afterSeq: number | null,
  onEvent: (event: AnyStoredEvent) => void,
  onStatus: (status: StreamStatus) => void,
  onOpen?: () => void,
  onLive?: (update: LiveUpdate) => void,
  onBackground?: (update: BackgroundUpdate) => void,
  onDraft?: (update: DraftUpdate) => void,
): () => void {
  let lastSeq = afterSeq
  let source: EventSource | null = null
  let retry: ReturnType<typeof setTimeout> | undefined
  let delay = 500
  let stopped = false

  const open = () => {
    if (stopped) return
    // Without a known position the server starts at its newest event.
    const from = lastSeq === null ? '' : `afterSeq=${lastSeq}&`
    const query = `${from}token=${encodeURIComponent(token() ?? '')}`
    const current = new EventSource(`/api/events?${query}`)
    source = current
    current.addEventListener('open', () => {
      if (stopped || source !== current) return
      delay = 500
      onStatus('open')
      onOpen?.()
    })
    current.addEventListener('ariadne', (message) => {
      if (stopped || source !== current) return
      const event: AnyStoredEvent = JSON.parse((message as MessageEvent<string>).data)
      lastSeq = Math.max(lastSeq ?? 0, event.seq)
      onEvent(event)
    })
    // A running command's output: not part of the log, so it moves nothing forward.
    current.addEventListener('live', (message) => {
      if (stopped || source !== current) return
      onLive?.(JSON.parse((message as MessageEvent<string>).data) as LiveUpdate)
    })
    // A reply as it is written: not part of the log either.
    current.addEventListener('draft', (message) => {
      if (stopped || source !== current) return
      onDraft?.(JSON.parse((message as MessageEvent<string>).data) as DraftUpdate)
    })
    current.addEventListener('background', (message) => {
      if (stopped || source !== current) return
      onBackground?.(JSON.parse((message as MessageEvent<string>).data) as BackgroundUpdate)
    })
    current.addEventListener('error', () => {
      if (stopped || source !== current) return
      if (current.readyState !== EventSource.CLOSED) {
        onStatus('lost')
        return
      }
      // EventSource hides the status code; ask a lightweight authenticated endpoint whether the token is the reason.
      void api.ping().then(
        () => {
          if (source === current) schedule('lost')
        },
        (error) => {
          if (source === current) {
            schedule(error instanceof ApiError && error.status === 401 ? 'unauthorized' : 'lost')
          }
        },
      )
    })
  }

  const schedule = (status: StreamStatus) => {
    if (stopped) return
    onStatus(status)
    if (status === 'unauthorized') return
    retry = setTimeout(open, delay)
    delay = Math.min(delay * 2, 10_000)
  }

  open()
  return () => {
    stopped = true
    clearTimeout(retry)
    source?.close()
    source = null
  }
}
