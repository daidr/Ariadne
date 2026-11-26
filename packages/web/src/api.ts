import type {
  AnyStoredEvent,
  ArtifactCommentAction,
  ArtifactCommentThreadView,
  ArtifactKind,
  ArtifactVersionView,
  ArtifactView,
  BlockAnswer,
  ConversationView,
  LineComment,
  LiveUpdate,
  DraftUpdate,
  ReplyDraft,
  BackgroundUpdate,
  BackgroundView,
  MessageView,
  NeedsMeItem,
  ProjectView,
  ReviewRoundView,
  SentLineComment,
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
  RepoChanges,
  SettingsSchema,
  WorktreeInfo,
} from '@ariadne/plugin-sdk'
import {
  ConversationService,
  DataService,
  EventService,
  FileService,
  MetaService,
  PluginService,
  SettingsService,
  ThreadService,
  UpdateService,
  WorkspaceService,
  type BackupInfo as ProtoBackupInfo,
  type StorageSnapshot as ProtoStorageSnapshot,
  type StreamEventsResponse as ProtoStreamEventsResponse,
} from '@ariadne/proto'
import {
  fromProtoBackgroundCommand,
  fromProtoBranchChanges,
  fromProtoConversationView,
  fromProtoFileVersions,
  fromProtoMemoryView,
  fromProtoMergeOutcome,
  fromProtoMessageView,
  fromProtoNoticeView,
  fromProtoProjectWorktrees,
  fromProtoRepoChanges,
  fromProtoReviewRound,
  fromProtoSentLineComment,
  fromProtoSkillView,
  fromProtoThreadView,
  fromProtoTodoView,
  fromProtoTraceRequestDetail,
  fromProtoTraceTurn,
  toProtoBlockAnswer,
  toProtoLineComment,
  toProtoMentionInput,
} from './proto-mappers'
import {
  ApiError,
  authorizationHeader,
  createAriadneTransport,
  createRpcClient,
  decodeLocalizedError,
  getToken,
  initToken,
  isValidToken,
  parseTokenInput,
  saveToken,
  setToken,
} from './rpc'
import {
  domain2Api,
  type IssueCommentView,
  type GetPulseParams,
  type IssueDetailView,
  type IssueSummaryView,
  type UpdateProjectPatch,
} from './rpc-domain2'
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
  fromProtoNotificationSettings,
  fromProtoPluginConfig,
  fromProtoPluginRecord,
  fromProtoPushSubscriptionView,
  fromProtoSearchCheckResult,
  fromProtoSearchSettings,
  fromProtoSourceKind,
  fromProtoTestNotificationResult,
  fromProtoUpdateStatus,
  fromProtoWorkSettings,
  toJsonObject,
  toProtoMcpServerInput,
  toProtoNotifierServiceInput,
  toProtoSaveModelSettingsRequest,
  toProtoSaveNotificationSettingsRequest,
  toProtoSearchInput,
  toProtoWebhookInput,
} from './rpc-settings'
import { synchronizeServerClock } from './server-clock'

export { ApiError, getToken, initToken, isValidToken, parseTokenInput, saveToken, setToken }
export type { IssueCommentView, IssueDetailView, IssueSummaryView, UpdateProjectPatch }
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

export type RestartPhase = '' | 'waiting_idle' | 'draining' | 'restarting'
export type RestartMode = 'when-idle' | 'now'

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
  canRestart?: boolean
  restarting?: boolean
  restartPhase?: RestartPhase
  activeTurns?: number
  activeBackground?: number
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
const threadClient = createRpcClient(ThreadService)
const conversationClient = createRpcClient(ConversationService)
const keepaliveConversationClient = createRpcClient(
  ConversationService,
  createAriadneTransport({
    fetch: (input, init) => fetch(input, { ...init, keepalive: true }),
  }),
)
const workspaceClient = createRpcClient(WorkspaceService)
const eventClient = createRpcClient(EventService)
const fileClient = createRpcClient(FileService)
const utf8Decoder = new TextDecoder()

function requireField<T>(value: T | undefined, name: string): T {
  if (value === undefined) {
    throw new ApiError(500, { key: 'error.invalidBody', params: { reason: name } })
  }
  return value
}

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

export interface FileSheetPreview {
  sheets: string[]
  rows: string[][]
  rowNumbers: number[]
  truncated: boolean
}

export async function fetchSheet(
  id: string,
  index: number,
  signal?: AbortSignal,
): Promise<FileSheetPreview> {
  const response = await fileClient.getFileSheet(
    { fileId: id, index },
    signal ? { signal } : undefined,
  )
  return {
    sheets: response.sheets,
    rows: response.rows.map((row) => row.cells),
    rowNumbers: response.rowNumbers.map(Number),
    truncated: response.truncated,
  }
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

export interface PluginUsage {
  modelProviders: string[]
  modelRoles: string[]
  searches: string[]
  projects: string[]
  notifiers: string[]
}

/** A plugin as the server reports it; see `PluginRecord` in the agent package. */
export interface PluginRecord {
  id: string | null
  name: LocalizedText | null
  source: 'builtin' | 'path'
  path: string | null
  status: 'loaded' | 'failed' | 'disabled'
  error: string | null
  configurable?: boolean
  provides: {
    models: string[]
    contexts: string[]
    dashboards: string[]
    searches: string[]
    links: string[]
    notifiers: string[]
  }
  usage?: PluginUsage
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
  disabledProvider?: string
  ready: boolean
  keySet: boolean
  needed: boolean
  models: string[]
  plugins: LocalizedText[]
  services: SearchServiceView[]
}

export interface SearchSettingsInput {
  provider?: string
  services: Record<string, Record<string, unknown>>
}

export interface SearchCheckResult {
  ok: boolean
  status: number | null
  message?: LocalizedText
}

export interface PushSubscriptionView {
  id: string
  endpoint: string
  label: string
  userAgent: string
  createdAt: number
  lastUsedAt: number | null
  lastError: string | null
}

export interface WebhookView {
  id: string
  name: string
  url: string
  enabled: boolean
  secretSet: boolean
  headers: string[]
}

export interface WebhookInput {
  id: string
  name: string
  url: string
  enabled: boolean
  secret?: string
  clearSecret?: boolean
  headers?: { name: string; value: string }[]
}

export interface NotifierCapabilities {
  supportsCheck?: boolean
}

export interface NotifierServiceView {
  kind: string
  title: LocalizedText
  description?: LocalizedText
  settings: SettingsSchema
  capabilities?: NotifierCapabilities
  enabled: boolean
  values: Record<string, unknown>
  secretsSet: string[]
  ready: boolean
}

export interface NotifierServiceInput {
  enabled: boolean
  values: Record<string, unknown>
}

export interface NotificationSettings {
  enabled: boolean
  webPushEnabled: boolean
  vapidPublicKey: string
  subscriptions: PushSubscriptionView[]
  webhooks: WebhookView[]
  notifiers: NotifierServiceView[]
}

export interface NotificationSettingsInput {
  enabled: boolean
  webPushEnabled: boolean
  webhooks: WebhookInput[]
  notifiers: Record<string, NotifierServiceInput>
}

export interface SubscribePushInput {
  endpoint: string
  p256dh: string
  auth: string
  label: string
  userAgent: string
}

export interface TestNotificationInput {
  channel: 'webpush' | 'webhook' | 'notifier'
  targetId?: string
  webhookDraft?: WebhookInput
  notifierDraft?: NotifierServiceInput
}

export interface TestNotificationResult {
  ok: boolean
  status: number | null
  message?: LocalizedText
  delivered: number
  failed: number
}

export interface NotificationPresenceInput {
  clientId: string
  focused: boolean
  projectId?: string | null
  threadId?: string | null
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
   * `coordinator.model: null` uses the threads' model (and `'low'` effort when `effort` is
   * `null` and the model accepts `'low'`, or the model's own default when `effort` is
   * `'default'`), `triage.model: null` the coordinator's, `research.model: null` the asking
   * conversation's; `effort: null` asks nothing for other roles.
   */
  roles: {
    thread: RoleInput
    coordinator: RoleInput<CoordinatorEffort>
    triage: RoleInput
    research: RoleInput
  }
  /** Usage also shows the estimated cost. */
  showCost: boolean
}

export type Effort = 'low' | 'medium' | 'high' | 'xhigh' | 'max'

/**
 * Effort stored on the coordinator role: a thinking level, `'default'` when the user
 * explicitly chose the model's own default while inheriting the threads' model, or `null`
 * when untouched (which defaults to `'low'` if the inherited model accepts it).
 */
export type CoordinatorEffort = Effort | 'default'

export interface RoleInput<E extends string = Effort> {
  model: string | null
  effort: E | null
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
  nextVersionPageToken?: string
  hasMoreVersions?: boolean
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
  restartToApplyUpdate: async (
    mode: RestartMode = 'when-idle',
    signal?: AbortSignal,
  ): Promise<UpdateStatus> => {
    const response = await updateClient.restartToApplyUpdate(
      { mode },
      signal ? { signal } : undefined,
    )
    return fromProtoUpdateStatus(response.status)
  },
  cancelRestart: async (signal?: AbortSignal): Promise<UpdateStatus> => {
    const response = await updateClient.cancelRestart({}, signal ? { signal } : undefined)
    return fromProtoUpdateStatus(response.status)
  },
  listMemories: async (projectId: string): Promise<{ memories: MemoryView[] }> => {
    const response = await conversationClient.listMemories({ projectId })
    return { memories: response.memories.map(fromProtoMemoryView) }
  },
  addMemory: async (projectId: string, text: string): Promise<{ memory: MemoryView }> => {
    const response = await conversationClient.createMemory({ projectId, text })
    return { memory: fromProtoMemoryView(requireField(response.memory, 'memory')) }
  },
  saveMemory: async (
    projectId: string,
    memoryId: string,
    text: string,
  ): Promise<{ memory: MemoryView }> => {
    const response = await conversationClient.updateMemory({ projectId, memoryId, text })
    return { memory: fromProtoMemoryView(requireField(response.memory, 'memory')) }
  },
  resumeThread: async (threadId: string): Promise<{ thread: ThreadView }> => {
    const response = await threadClient.resumeThread({ threadId })
    return { thread: fromProtoThreadView(requireField(response.thread, 'thread')) }
  },
  reopenThread: async (threadId: string): Promise<{ thread: ThreadView }> => {
    const response = await threadClient.reopenThread({ threadId })
    return { thread: fromProtoThreadView(requireField(response.thread, 'thread')) }
  },
  removeThreadReference: async (
    threadId: string,
    referenceId: string,
  ): Promise<{ thread: ThreadView }> => {
    const response = await threadClient.removeThreadReference({ threadId, referenceId })
    return { thread: fromProtoThreadView(requireField(response.thread, 'thread')) }
  },
  keepThreadReference: async (
    threadId: string,
    referenceId: string,
  ): Promise<{ thread: ThreadView }> => {
    const response = await threadClient.keepThreadReference({ threadId, referenceId })
    return { thread: fromProtoThreadView(requireField(response.thread, 'thread')) }
  },
  completeThread: async (threadId: string): Promise<{ thread: ThreadView }> => {
    const response = await threadClient.completeThread({ threadId })
    return { thread: fromProtoThreadView(requireField(response.thread, 'thread')) }
  },
  endAside: async (threadId: string): Promise<{ thread: ThreadView }> => {
    const response = await threadClient.endAside({ threadId })
    return { thread: fromProtoThreadView(requireField(response.thread, 'thread')) }
  },
  undoNotice: async (projectId: string, noticeId: string): Promise<void> => {
    await conversationClient.undoNotice({ projectId, noticeId })
  },
  removeMemory: async (projectId: string, memoryId: string): Promise<void> => {
    await conversationClient.deleteMemory({ projectId, memoryId })
  },
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
  listProjects: (ids: readonly string[] = []) => domain2Api.listProjects(ids),
  listAllNeedsMe: () => domain2Api.listAllNeedsMe(),
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
  }) => domain2Api.createProject(input),
  /** Unified atomic project update (name, settings, env, slots, mcp, autopilotPaused). */
  updateProject: (id: string, patch: UpdateProjectPatch) => domain2Api.updateProject(id, patch),
  renameProject: async (id: string, name: string): Promise<{ project: ProjectView }> => {
    const { project } = await domain2Api.updateProject(id, { name })
    return { project }
  },
  deleteProject: (id: string) => domain2Api.deleteProject(id),
  /** With `since` (a `sync.cursor` given before), only the threads changed after it. */
  getProject: (id: string, since?: string): Promise<ProjectDetail & { sync: ThreadSync }> =>
    domain2Api.getProject(id, since),
  getProjectActivity: (id: string, signal: AbortSignal) =>
    domain2Api.getProjectActivity(id, signal),
  getPulse: (params?: GetPulseParams) => domain2Api.getPulse(params),
  /**
   * The newest messages, the page before `before`, or everything from `from` on. With
   * `since` (a `lastSeq` given before), only messages and notices changed after it.
   */
  getConversation: async (
    id: string,
    cursor: { before?: string; from?: string; since?: number; limit?: number } = {},
  ): Promise<{
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
    model: { id: string; contextTokens: number; compactAt: number; effort?: Effort | null } | null
  }> => {
    const response = await conversationClient.getConversation({
      conversationId: id,
      ...(cursor.before !== undefined ? { before: cursor.before } : {}),
      ...(cursor.from !== undefined ? { from: cursor.from } : {}),
      ...(cursor.limit !== undefined ? { limit: cursor.limit } : {}),
      ...(cursor.since !== undefined ? { since: BigInt(cursor.since) } : {}),
    })
    const notices: NoticeView[] = []
    for (const notice of response.notices) {
      const converted = fromProtoNoticeView(notice)
      if (converted) notices.push(converted)
    }
    return {
      conversation: fromProtoConversationView(requireField(response.conversation, 'conversation')),
      messages: response.messages.map(fromProtoMessageView),
      notices,
      ...(response.readThrough !== undefined ? { readThrough: response.readThrough } : {}),
      more: response.more,
      lastSeq: Number(response.lastSeq),
      todo: fromProtoTodoView(response.todo),
      model: response.model
        ? {
            id: response.model.id,
            contextTokens: Number(response.model.contextTokens),
            compactAt: Number(response.model.compactAt),
            ...(response.model.effort !== undefined
              ? { effort: response.model.effort as Effort }
              : {}),
          }
        : null,
    }
  },
  getTrace: async (
    id: string,
    options: { before?: number; from?: number; signal?: AbortSignal } = {},
  ): Promise<{ turns: TraceTurn[]; more: boolean }> => {
    const response = await conversationClient.getTrace(
      {
        conversationId: id,
        ...(options.before !== undefined ? { before: BigInt(options.before) } : {}),
        ...(options.from !== undefined ? { from: BigInt(options.from) } : {}),
      },
      options.signal ? { signal: options.signal } : undefined,
    )
    return {
      turns: response.turns.map(fromProtoTraceTurn),
      more: response.more,
    }
  },
  getTraceRequest: async (id: string, seq: number): Promise<{ request: TraceRequestDetail }> => {
    const response = await conversationClient.getTraceRequestDetail({
      conversationId: id,
      seq: BigInt(seq),
    })
    return {
      request: fromProtoTraceRequestDetail(requireField(response.request, 'request')),
    }
  },
  exportTrace: (id: string) =>
    download(
      `/api/conversations/${encodeURIComponent(id)}/trace/export`,
      `ariadne-trace-${id}.zip`,
    ),
  setThreadModel: async (
    threadId: string,
    model: string | null,
    effort: Effort | null = null,
  ): Promise<{ thread: ThreadView }> => {
    const response = await threadClient.setThreadModel({
      threadId,
      modelAction:
        model === null ? { case: 'useDefault', value: true } : { case: 'model', value: model },
      ...(model !== null && effort !== null ? { effort } : {}),
    })
    return { thread: fromProtoThreadView(requireField(response.thread, 'thread')) }
  },
  stopTurn: async (id: string): Promise<{ stopped: boolean }> => {
    const response = await conversationClient.stopTurn({ conversationId: id })
    return { stopped: response.stopped }
  },
  wakeNow: async (id: string): Promise<void> => {
    await conversationClient.wakeWait({ conversationId: id })
  },
  cancelWait: async (id: string): Promise<void> => {
    await conversationClient.cancelWait({ conversationId: id })
  },
  commandOutput: async (
    threadId: string,
    commandId: string,
    signal?: AbortSignal,
  ): Promise<{ command: BackgroundView; tail: string }> => {
    const response = await threadClient.getCommandOutput(
      { threadId, commandId },
      signal ? { signal } : undefined,
    )
    return {
      command: fromProtoBackgroundCommand(requireField(response.command, 'command')),
      tail: response.tail,
    }
  },
  streamCommandOutput: async function* (
    threadId: string,
    commandId: string,
    signal?: AbortSignal,
  ): AsyncGenerator<{ command: BackgroundView; tail: string }> {
    const stream = threadClient.streamCommandOutput(
      { threadId, commandId },
      signal ? { signal } : undefined,
    )
    for await (const response of stream) {
      yield {
        command: fromProtoBackgroundCommand(requireField(response.command, 'command')),
        tail: response.tail,
      }
    }
  },
  streamEvents: (
    options: { afterSeq?: number; projectId?: string },
    signal?: AbortSignal,
  ): AsyncIterable<ProtoStreamEventsResponse> =>
    eventClient.streamEvents(
      {
        ...(options.afterSeq === undefined ? {} : { afterSeq: BigInt(options.afterSeq) }),
        ...(options.projectId === undefined ? {} : { projectId: options.projectId }),
      },
      signal ? { signal } : undefined,
    ),
  pullRequestChecks: async (
    threadId: string,
    signal?: AbortSignal,
  ): Promise<{
    headSha: string
    checks: { id: string; name: string; status: string; conclusion: string | null }[]
  }> => {
    const response = await threadClient.getPullRequestChecks(
      { threadId },
      signal ? { signal } : undefined,
    )
    return {
      headSha: response.headSha,
      checks: response.checks.map((c) => ({
        id: c.id,
        name: c.name,
        status: c.status,
        conclusion: c.conclusion ?? null,
      })),
    }
  },
  stopCommand: async (
    threadId: string,
    commandId: string,
  ): Promise<{ command: BackgroundView }> => {
    const response = await threadClient.stopCommand({ threadId, commandId })
    return {
      command: fromProtoBackgroundCommand(requireField(response.command, 'command')),
    }
  },
  retryTurn: async (id: string): Promise<void> => {
    await conversationClient.retryTurn({ conversationId: id })
  },
  addReaction: async (messageId: string, emoji: string): Promise<{ message: MessageView }> => {
    const response = await conversationClient.addReaction({ messageId, emoji })
    return { message: fromProtoMessageView(requireField(response.message, 'message')) }
  },
  removeReaction: async (messageId: string, emoji: string): Promise<{ message: MessageView }> => {
    const response = await conversationClient.removeReaction({ messageId, emoji })
    return { message: fromProtoMessageView(requireField(response.message, 'message')) }
  },
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
  getNotificationSettings: async (): Promise<NotificationSettings> =>
    fromProtoNotificationSettings((await settingsClient.getNotificationSettings({})).notifications),
  saveNotificationSettings: async (
    input: NotificationSettingsInput,
  ): Promise<NotificationSettings> =>
    fromProtoNotificationSettings(
      (await settingsClient.saveNotificationSettings(toProtoSaveNotificationSettingsRequest(input)))
        .notifications,
    ),
  subscribePush: async (
    input: SubscribePushInput,
  ): Promise<{
    subscription: PushSubscriptionView | null
    subscriptions: PushSubscriptionView[]
  }> => {
    const response = await settingsClient.subscribePush(input)
    return {
      subscription: response.subscription
        ? fromProtoPushSubscriptionView(response.subscription)
        : null,
      subscriptions: response.subscriptions.map(fromProtoPushSubscriptionView),
    }
  },
  unsubscribePush: async (endpoint: string): Promise<{ subscriptions: PushSubscriptionView[] }> => {
    const response = await settingsClient.unsubscribePush({ endpoint })
    return { subscriptions: response.subscriptions.map(fromProtoPushSubscriptionView) }
  },
  deletePushSubscription: async (
    id: string,
  ): Promise<{ subscriptions: PushSubscriptionView[] }> => {
    const response = await settingsClient.deletePushSubscription({ id })
    return { subscriptions: response.subscriptions.map(fromProtoPushSubscriptionView) }
  },
  testNotification: async (input: TestNotificationInput): Promise<TestNotificationResult> =>
    fromProtoTestNotificationResult(
      await settingsClient.testNotification({
        channel: input.channel,
        ...(input.targetId !== undefined ? { targetId: input.targetId } : {}),
        ...(input.webhookDraft ? { webhookDraft: toProtoWebhookInput(input.webhookDraft) } : {}),
        ...(input.notifierDraft
          ? { notifierDraft: toProtoNotifierServiceInput(input.notifierDraft) }
          : {}),
      }),
    ),
  reportNotificationPresence: async (input: NotificationPresenceInput): Promise<void> => {
    await settingsClient.reportNotificationPresence({
      clientId: input.clientId,
      focused: input.focused,
      ...(input.projectId ? { projectId: input.projectId } : {}),
      ...(input.threadId ? { threadId: input.threadId } : {}),
    })
  },
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
  setPluginEnabled: async (id: string, enabled: boolean): Promise<{ plugins: PluginRecord[] }> => {
    const response = await pluginClient.setPluginEnabled({ pluginId: id, enabled })
    return { plugins: response.plugins.map(fromProtoPluginRecord) }
  },
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
  postMessage: async (
    projectId: string,
    conversationId: string,
    content: string,
    extras: MessageExtras = {},
  ): Promise<{ message: MessageView }> => {
    const response = await conversationClient.postMessage(
      {
        projectId,
        conversationId,
        content,
        attachments: extras.attachments ?? [],
        mentions: (extras.mentions ?? []).map(toProtoMentionInput),
        comments: (extras.comments ?? []).map(toProtoLineComment),
      },
      undefined,
    )
    return { message: fromProtoMessageView(requireField(response.message, 'message')) }
  },
  /** Line comments sent as the page closes, in a request that outlives it. */
  postCommentsLeaving: (projectId: string, conversationId: string, comments: LineComment[]) =>
    void keepaliveConversationClient
      .postMessage(
        {
          projectId,
          conversationId,
          content: '',
          comments: comments.map(toProtoLineComment),
        },
        undefined,
      )
      .catch(() => {}),
  mentionFiles: async (
    projectId: string,
    conversationId: string,
    query: string,
  ): Promise<{ files: string[] }> => {
    const response = await workspaceClient.listMentionFiles({
      projectId,
      ...(conversationId ? { conversationId } : {}),
      query,
    })
    return { files: [...response.files] }
  },
  markRead: async (conversationId: string, upTo: string): Promise<void> => {
    await conversationClient.markRead({ conversationId, upTo })
  },
  /** The user is back at the project after `away` ms without touching it. */
  noteReturn: async (projectId: string, away: number): Promise<void> => {
    await conversationClient.noteReturn({ projectId, away })
  },
  answerAttention: async (id: string, answer: BlockAnswer): Promise<void> => {
    await conversationClient.answerAttention({
      attentionId: id,
      answer: toProtoBlockAnswer(answer),
    })
  },
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
  getFileSources: (projectId: string) => domain2Api.getFileSources(projectId),
  setFileSources: (
    projectId: string,
    body: { sources: (SourceInput & { id?: string })[]; primaryIndex?: number },
  ) => domain2Api.setFileSources(projectId, body),
  addReference: (
    projectId: string,
    reference: { kind: string; settings: Record<string, unknown> },
  ) => domain2Api.addReference(projectId, reference),
  removeReference: (projectId: string, referenceId: string) =>
    domain2Api.removeReference(projectId, referenceId),
  /** The reference sources that cannot be read right now, with why. */
  referenceStatus: async (
    projectId: string,
  ): Promise<{ failed: { id: string; error: LocalizedMessage }[] }> => {
    const response = await workspaceClient.getReferenceStatus({ projectId })
    return {
      failed: response.failed.map((item) => ({
        id: item.id,
        error: decodeLocalizedError(item.error) ?? { key: 'error.invalidBody' },
      })),
    }
  },
  refreshDashboard: (artifactId: string) => domain2Api.refreshDashboard(artifactId),
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
  setProjectMcp: async (
    projectId: string,
    servers: string[],
  ): Promise<{ project: ProjectView }> => {
    const { project } = await domain2Api.updateProject(projectId, { mcp: servers })
    return { project }
  },
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
  getProjectSettings: (projectId: string): Promise<ProjectSettingsView> =>
    domain2Api.getProjectSettings(projectId),
  saveProjectSettings: (
    projectId: string,
    input: { settings: ProjectSettings; env?: { name: string; value: string }[] },
  ): Promise<ProjectSettingsView & { project: ProjectView }> =>
    domain2Api.updateProject(projectId, input),
  /** The project's log as a file; the token goes in a header, so not as a plain link. */
  exportProject: (projectId: string) =>
    download(`/api/projects/${projectId}/export`, `ariadne-${projectId}.json`),
  setAutopilotPaused: async (
    projectId: string,
    paused: boolean,
  ): Promise<{ project: ProjectView }> => {
    const { project } = await domain2Api.updateProject(projectId, { autopilotPaused: paused })
    return { project }
  },
  setProjectSlots: async (
    projectId: string,
    slots: number | null,
  ): Promise<{ project: ProjectView }> => {
    const { project } = await domain2Api.updateProject(projectId, { slots })
    return { project }
  },
  getReviews: async (
    threadId: string,
    options: { beforeId?: number; limit?: number } = {},
  ): Promise<{ rounds: ReviewRoundView[]; hasMore: boolean; nextBeforeId?: number }> => {
    const response = await threadClient.listReviews({
      threadId,
      ...(options.beforeId !== undefined ? { beforeId: BigInt(options.beforeId) } : {}),
      ...(options.limit !== undefined ? { limit: options.limit } : {}),
    })
    return {
      rounds: response.rounds.map(fromProtoReviewRound),
      hasMore: response.hasMore,
      ...(response.nextBeforeId !== undefined
        ? { nextBeforeId: Number(response.nextBeforeId) }
        : {}),
    }
  },
  getLineComments: async (
    threadId: string,
    options: { beforeMessageId?: string; limit?: number } = {},
  ): Promise<{ comments: SentLineComment[]; hasMore: boolean; nextBeforeMessageId?: string }> => {
    const response = await threadClient.listLineComments({
      threadId,
      ...(options.beforeMessageId !== undefined
        ? { beforeMessageId: options.beforeMessageId }
        : {}),
      ...(options.limit !== undefined ? { limit: options.limit } : {}),
    })
    return {
      comments: response.comments.map(fromProtoSentLineComment),
      hasMore: response.hasMore,
      ...(response.nextBeforeMessageId !== undefined
        ? { nextBeforeMessageId: response.nextBeforeMessageId }
        : {}),
    }
  },
  getSkills: async (conversationId: string): Promise<{ skills: SkillView[] }> => {
    const response = await conversationClient.listSkills({ conversationId })
    return { skills: response.skills.map(fromProtoSkillView) }
  },
  getChanges: async (
    threadId: string,
    known?: { head: string; baseHead: string },
    signal?: AbortSignal,
  ): Promise<{
    changes: BranchChanges | null
    repos?: RepoChanges[]
    mergeable: boolean
    unchanged?: boolean
  }> => {
    const response = await workspaceClient.getThreadChanges(
      {
        threadId,
        ...(known ? { head: known.head, baseHead: known.baseHead } : {}),
      },
      signal ? { signal } : undefined,
    )
    const repos = response.repos
      .map(fromProtoRepoChanges)
      .filter((item): item is RepoChanges => item !== null)
    return {
      changes: fromProtoBranchChanges(response.changes),
      ...(repos.length > 0 ? { repos } : {}),
      mergeable: response.mergeable,
      ...(response.unchanged ? { unchanged: true } : {}),
    }
  },
  getChangedFile: async (
    threadId: string,
    input: {
      from: string
      head: string
      path: string
      previousPath: string | null
      sourceId?: string
      repoPath?: string
    },
    signal?: AbortSignal,
  ): Promise<{ file: FileVersions }> => {
    const response = await workspaceClient.getChangedFile(
      {
        threadId,
        from: input.from,
        head: input.head,
        path: input.path,
        ...(input.previousPath ? { previousPath: input.previousPath } : {}),
        ...(input.sourceId ? { sourceId: input.sourceId } : {}),
        ...(input.repoPath ? { repoPath: input.repoPath } : {}),
      },
      signal ? { signal } : undefined,
    )
    return { file: fromProtoFileVersions(response.file) }
  },
  getChangedFiles: async (
    threadId: string,
    input: {
      from: string
      head: string
      files: { path: string; previousPath: string | null }[]
      sourceId?: string
      repoPath?: string
    },
    signal?: AbortSignal,
  ): Promise<{ files: FileVersions[] }> => {
    const response = await workspaceClient.getChangedFiles(
      {
        threadId,
        from: input.from,
        head: input.head,
        files: input.files.map((file) => ({
          path: file.path,
          ...(file.previousPath ? { previousPath: file.previousPath } : {}),
        })),
        ...(input.sourceId ? { sourceId: input.sourceId } : {}),
        ...(input.repoPath ? { repoPath: input.repoPath } : {}),
      },
      signal ? { signal } : undefined,
    )
    return { files: response.files.map((file) => fromProtoFileVersions(file)) }
  },
  mergeThread: async (
    threadId: string,
    input: { method: MergeMethod; message: string; expectedHead: string },
  ): Promise<{ outcome: MergeOutcome; thread: ThreadView }> => {
    const response = await workspaceClient.mergeThread({
      threadId,
      method: input.method,
      message: input.message,
      expectedHead: input.expectedHead,
    })
    return {
      outcome: fromProtoMergeOutcome(response.outcome),
      thread: fromProtoThreadView(requireField(response.thread, 'thread')),
    }
  },
  undoMerge: async (threadId: string): Promise<{ thread: ThreadView }> => {
    const response = await workspaceClient.undoMerge({ threadId })
    return { thread: fromProtoThreadView(requireField(response.thread, 'thread')) }
  },
  listWorktrees: async (
    sizes: boolean,
    signal?: AbortSignal,
  ): Promise<{ projects: ProjectWorktrees[]; windows: boolean }> => {
    const response = await workspaceClient.listWorktrees(
      { includeSizes: sizes },
      signal ? { signal } : undefined,
    )
    return {
      projects: response.projects.map(fromProtoProjectWorktrees),
      windows: response.windows,
    }
  },
  removeWorktree: async (
    projectId: string,
    threadId: string,
    sourceId?: string,
    orphaned?: boolean,
  ): Promise<void> => {
    await workspaceClient.removeWorktree({
      projectId,
      threadId,
      ...(sourceId ? { sourceId } : {}),
      orphaned: Boolean(orphaned),
    })
  },
  dismissProposal: (projectId: string, proposalId: string) =>
    domain2Api.dismissProposal(projectId, proposalId),
  listIssues: (
    projectId: string,
    query: { state?: string; labels?: string[]; search?: string; limit?: number } = {},
    signal?: AbortSignal,
  ) => domain2Api.listIssues(projectId, query, signal),
  getIssue: (projectId: string, number: number, signal?: AbortSignal) =>
    domain2Api.getIssue(projectId, number, signal),
  getPlan: (
    projectId: string,
    options: { revisionPageSize?: number; revisionPageToken?: string } = {},
  ) => domain2Api.getPlan(projectId, options),
  listPlanRevisions: (
    projectId: string,
    options: { pageSize?: number; pageToken?: string; signal?: AbortSignal } = {},
  ) => domain2Api.listPlanRevisions(projectId, options),
  search: (projectId: string, q: string, preferProjectId?: string) =>
    domain2Api.searchProject(projectId, q, preferProjectId),
  listArtifacts: (
    projectId: string,
    options: { pageSize?: number; pageToken?: string; signal?: AbortSignal } = {},
  ) => domain2Api.listArtifacts(projectId, options),
  getArtifact: (
    id: string,
    options: {
      version?: number
      signal?: AbortSignal
      versions?: boolean
      versionPageSize?: number
      versionPageToken?: string
    } = {},
  ) => domain2Api.getArtifact(id, options),
  listArtifactVersions: (
    id: string,
    options: { pageSize?: number; pageToken?: string; signal?: AbortSignal } = {},
  ) => domain2Api.listArtifactVersions(id, options),
  createArtifact: (projectId: string, input: { kind: ArtifactKind; title: string }) =>
    domain2Api.createArtifact(projectId, input),
  saveArtifact: (
    id: string,
    input: {
      /** `null` keeps what a document or whiteboard edited together holds now. */
      content: string | null
      baseVersion: number
      reason: string
      title?: string
      quotes?: Record<string, string>
      includeVersions?: boolean
    },
  ) => domain2Api.saveArtifact(id, input),
  /**
   * The user stopped for a moment in an artifact, or left it: what they did goes to the
   * agent, with pictures of the options they marked. `leaving` lets it go out as the page
   * closes, without the pictures.
   */
  sendFeedback: (id: string, images: string[], leaving = false) =>
    leaving
      ? void fetch('/api/rpc/ariadne.v1.ArtifactService/SendArtifactFeedback', {
          method: 'POST',
          keepalive: true,
          headers: { authorization: authorizationHeader(), 'content-type': 'application/json' },
          body: JSON.stringify({ artifactId: id, images: [] }),
        }).catch(() => {})
      : domain2Api.sendArtifactFeedback(id, images),
  withdrawFeedback: (id: string, round: number) => domain2Api.withdrawArtifactFeedback(id, round),
  /** Where a page joins an artifact's room, and the query that lets it in. */
  sharedRoom: (id: string) => ({
    server: `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/api/artifacts`,
    room: `${id}/live`,
    params: { token: token() ?? '' },
  }),
  createArtifactComment: (
    id: string,
    input: Parameters<typeof domain2Api.createArtifactComment>[1],
  ) => domain2Api.createArtifactComment(id, input),
  replyArtifactComment: (
    id: string,
    input: Parameters<typeof domain2Api.replyArtifactComment>[1],
  ) => domain2Api.replyArtifactComment(id, input),
  editArtifactComment: (id: string, input: Parameters<typeof domain2Api.editArtifactComment>[1]) =>
    domain2Api.editArtifactComment(id, input),
  deleteArtifactComment: (
    id: string,
    input: Parameters<typeof domain2Api.deleteArtifactComment>[1],
  ) => domain2Api.deleteArtifactComment(id, input),
  resolveArtifactCommentThread: (id: string, threadId: string) =>
    domain2Api.resolveArtifactCommentThread(id, threadId),
  reopenArtifactCommentThread: (id: string, threadId: string) =>
    domain2Api.reopenArtifactCommentThread(id, threadId),
  removeArtifactCommentThread: (id: string, threadId: string) =>
    domain2Api.removeArtifactCommentThread(id, threadId),
  commentArtifact: (id: string, action: ArtifactCommentAction) =>
    domain2Api.commentArtifact(id, action),
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
 * Follow the event log from `afterSeq` over ConnectRPC binary Server Streaming.
 * Reconnects with exponential backoff on network drops or server restarts,
 * resuming strictly after the last durable sequence number observed.
 */
export function followEvents(
  afterSeq: number | null,
  onEvent: (event: AnyStoredEvent) => void,
  onStatus: (status: StreamStatus) => void,
  onOpen?: () => void,
  onLive?: (update: LiveUpdate) => void,
  onBackground?: (update: BackgroundUpdate) => void,
  onDraft?: (update: DraftUpdate) => void,
  onUpdateStatus?: (status: UpdateStatus) => void,
): () => void {
  let lastSeq = afterSeq
  let controller: AbortController | null = null
  let retry: ReturnType<typeof setTimeout> | undefined
  let delay = 500
  let stopped = false

  const schedule = (status: StreamStatus) => {
    if (stopped) return
    onStatus(status)
    if (status === 'unauthorized') return
    retry = setTimeout(open, delay)
    delay = Math.min(delay * 2, 10_000)
  }

  const open = () => {
    if (stopped) return
    const current = new AbortController()
    controller = current
    const options = lastSeq === null ? {} : { afterSeq: lastSeq }
    const activeDrafts = new Map<string, ReplyDraft>()

    void (async () => {
      try {
        for await (const frame of api.streamEvents(options, current.signal)) {
          if (stopped || controller !== current) return
          switch (frame.payload.case) {
            case 'ready':
              activeDrafts.clear()
              delay = 500
              if (lastSeq === null) {
                lastSeq = Number(frame.payload.value.lastSeq)
              }
              onStatus('open')
              onOpen?.()
              break
            case 'heartbeat':
              synchronizeServerClock(String(frame.payload.value.serverTimeMs))
              break
            case 'event': {
              const ev = frame.payload.value
              const data = ev.rawJson.length ? JSON.parse(utf8Decoder.decode(ev.rawJson)) : {}
              const stored = {
                seq: Number(ev.seq),
                id: ev.id,
                projectId: ev.projectId,
                type: ev.type,
                at: Number(ev.at),
                data,
              } as AnyStoredEvent
              lastSeq = Math.max(lastSeq ?? 0, stored.seq)
              onEvent(stored)
              break
            }
            case 'live': {
              const u = frame.payload.value
              onLive?.({
                projectId: u.projectId,
                conversationId: u.conversationId,
                command: u.command
                  ? {
                      projectId: u.command.projectId,
                      conversationId: u.command.conversationId,
                      command: u.command.command,
                      cwd: u.command.cwd,
                      startedAt: Number(u.command.startedAt),
                      tail: u.command.tail,
                    }
                  : null,
              })
              break
            }
            case 'background': {
              const u = frame.payload.value
              onBackground?.({
                projectId: u.projectId,
                conversationId: u.conversationId,
                commands: u.commands.map(fromProtoBackgroundCommand),
              })
              break
            }
            case 'draft': {
              const u = frame.payload.value
              if (!u.draft) {
                activeDrafts.delete(u.conversationId)
                onDraft?.({
                  projectId: u.projectId,
                  conversationId: u.conversationId,
                  draft: null,
                })
                break
              }
              const prev = activeDrafts.get(u.conversationId)
              const canAppend = u.draft.append && prev?.messageId === u.draft.messageId
              const text = canAppend ? prev.text + u.draft.text : u.draft.text
              const blocks =
                canAppend && u.draft.keepBlocks
                  ? prev.blocks
                  : u.draft.blocksJson.length
                    ? JSON.parse(utf8Decoder.decode(u.draft.blocksJson))
                    : undefined
              const assembled: ReplyDraft = {
                messageId: u.draft.messageId,
                text,
                ...(blocks ? { blocks } : {}),
                startedAt: Number(u.draft.startedAt),
                ...(u.draft.posted ? { posted: true } : {}),
              }
              if (assembled.posted) activeDrafts.delete(u.conversationId)
              else activeDrafts.set(u.conversationId, assembled)
              onDraft?.({
                projectId: u.projectId,
                conversationId: u.conversationId,
                draft: assembled,
              })
              break
            }
            case 'updateStatus':
              onUpdateStatus?.(fromProtoUpdateStatus(frame.payload.value))
              break
          }
        }
        if (!stopped && controller === current && !current.signal.aborted) {
          schedule('lost')
        }
      } catch (error) {
        if (stopped || controller !== current || current.signal.aborted) return
        schedule(error instanceof ApiError && error.status === 401 ? 'unauthorized' : 'lost')
      }
    })()
  }

  open()
  return () => {
    stopped = true
    clearTimeout(retry)
    controller?.abort()
    controller = null
  }
}
