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
  PlanOp,
  PlanUndoPreview,
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
  ConversationService,
  DataService,
  ThreadService,
  WorkspaceService,
  type BackupInfo as ProtoBackupInfo,
  type StorageSnapshot as ProtoStorageSnapshot,
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
  createAriadneTransport,
  createRpcClient,
  decodeLocalizedError,
  getToken as token,
  initToken,
} from './rpc'
import { synchronizeServerClock } from './server-clock'

export { ApiError, initToken }

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
const threadClient = createRpcClient(ThreadService)
const conversationClient = createRpcClient(ConversationService)
const keepaliveConversationClient = createRpcClient(
  ConversationService,
  createAriadneTransport({
    fetch: (input, init) => fetch(input, { ...init, keepalive: true }),
  }),
)
const workspaceClient = createRpcClient(WorkspaceService)

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
  const headers: Record<string, string> = { authorization: `Bearer ${token() ?? ''}` }
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
    headers: { authorization: `Bearer ${token() ?? ''}` },
    signal,
  })
  if (!response.ok) throw new Error(`File request failed (${response.status})`)
  return response
}

async function download(path: string, fallbackName: string) {
  const response = await fetch(path, { headers: { authorization: `Bearer ${token() ?? ''}` } })
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

/** How the work is done everywhere: the user's instructions, command rules, limits. */
/** Web search in the settings: whether a key is stored, and whether search waits for one. */
export interface SearchSettings {
  provider: 'tavily' | 'deepseek'
  keySet: boolean
  needed: boolean
  models: string[]
  plugins: LocalizedText[]
  services: {
    kind: 'tavily' | 'deepseek'
    title: string
    defaultBaseUrl: string
    baseUrl: string
    keySet: boolean
  }[]
}

export interface SearchSettingsInput {
  provider: SearchSettings['provider']
  services: Partial<
    Record<SearchSettings['provider'], { baseUrl?: string; apiKey?: string | null }>
  >
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
  updateStatus: (signal?: AbortSignal) =>
    request<UpdateStatus>('GET', '/api/updates', undefined, signal),
  saveUpdatePreferences: (preferences: UpdatePreferences, signal?: AbortSignal) =>
    request<UpdateStatus>('PUT', '/api/updates', preferences, signal),
  checkForUpdate: (signal?: AbortSignal) =>
    request<UpdateStatus>('POST', '/api/updates/check', undefined, signal),
  installUpdate: (version: string, signal?: AbortSignal) =>
    request<UpdateStatus>('POST', '/api/updates/install', { version }, signal),
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
  ping: () => request<{ ok: boolean }>('GET', '/api/ping'),
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
    model: { id: string; contextTokens: number; compactAt: number } | null
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
  ): Promise<{ thread: ThreadView }> => {
    const response = await threadClient.setThreadModel({
      threadId,
      modelAction:
        model === null ? { case: 'useDefault', value: true } : { case: 'model', value: model },
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
  getConcurrency: () => request<Concurrency>('GET', '/api/settings/concurrency'),
  saveConcurrency: (input: Concurrency) =>
    request<Concurrency>('PUT', '/api/settings/concurrency', input),
  getInputSettings: () => request<InputSettings>('GET', '/api/settings/input'),
  saveInputSettings: (input: InputSettings) =>
    request<InputSettings>('PUT', '/api/settings/input', input),
  getModelSettings: () => request<ModelSettings>('GET', '/api/settings/models'),
  getSearchSettings: () => request<SearchSettings>('GET', '/api/settings/search'),
  /** `null` clears the stored key. */
  saveSearchSettings: (input: SearchSettingsInput) =>
    request<SearchSettings>('PUT', '/api/settings/search', input),
  checkSearchSettings: (input: SearchSettingsInput) =>
    request<{ ok: boolean; status: number | null }>('POST', '/api/settings/search/check', input),
  listPlugins: () => request<{ plugins: PluginRecord[] }>('GET', '/api/plugins'),
  getPluginConfig: (id: string) =>
    request<PluginConfig>('GET', `/api/plugins/${encodeURIComponent(id)}/settings`),
  savePluginConfig: (id: string, values: Record<string, unknown>) =>
    request<PluginConfig>('PUT', `/api/plugins/${encodeURIComponent(id)}/settings`, { values }),
  addPlugin: (path: string) =>
    request<{ plugins: PluginRecord[] }>('POST', '/api/plugins', { path }),
  removePlugin: (path: string) =>
    request<{ plugins: PluginRecord[] }>('POST', '/api/plugins/remove', { path }),
  /** What the public catalog knows of a model, by the name typed. */
  modelInfo: (model: string, hint: string) =>
    request<{ info: ModelInfo }>(
      'GET',
      `/api/settings/model-info?${new URLSearchParams({ model, hint })}`,
    ),
  /** The models a provider in the form can use; `null` when it cannot say. */
  listModels: (provider: Pick<ModelProviderInput, 'id' | 'kind' | 'settings'>) =>
    request<{ models: ListedModel[] | null }>('POST', '/api/settings/model-list', { provider }),
  /** Saved as sent; `problems` names the new models that did not answer. */
  saveModelSettings: (input: ModelSettingsInput) =>
    request<ModelSettings & { problems: ModelProblem[] }>('PUT', '/api/settings/models', input),
  /** Setups already on this machine that no provider uses yet. */
  foundSetups: () => request<{ found: FoundSetup[] }>('GET', '/api/settings/found'),
  useFoundSetup: (id: string) =>
    request<ModelSettings & { problems: ModelProblem[] }>('POST', '/api/settings/found/use', {
      id,
    }),
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
  sourceKinds: () => request<{ kinds: SourceKind[] }>('GET', '/api/source-kinds'),
  browseSource: (
    kind: string,
    body: {
      projectId: string | null
      sourceId?: string
      settings: Record<string, unknown>
      path: string
    },
  ) =>
    request<{ listing: DirectoryListing | null }>(
      'POST',
      `/api/source-kinds/${encodeURIComponent(kind)}/browse`,
      body,
    ),
  browseLocal: (path: string) =>
    request<{ listing: DirectoryListing | null }>(
      'GET',
      `/api/directories${queryString({ path })}`,
    ),
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
  refreshDashboard: (artifactId: string) =>
    request<{ artifact: unknown }>('POST', `/api/artifacts/${artifactId}/refresh`),
  getMcpSettings: () => request<McpSettings>('GET', '/api/settings/mcp'),
  saveMcpSettings: (servers: McpServerInput[]) =>
    request<McpSettings>('PUT', '/api/settings/mcp', { servers }),
  checkMcpServer: (id: string) =>
    request<{ status: McpServerStatus }>(
      'POST',
      `/api/settings/mcp/${encodeURIComponent(id)}/check`,
    ),
  setProjectMcp: (projectId: string, servers: string[]) =>
    request<{ project: ProjectView }>('PUT', `/api/projects/${projectId}/mcp`, { servers }),
  getWorkSettings: () => request<WorkSettings>('GET', '/api/settings/work'),
  saveWorkSettings: (input: Partial<Omit<WorkSettings, 'defaults'>>) =>
    request<WorkSettings>('PUT', '/api/settings/work', input),
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
  ): Promise<{ changes: BranchChanges | null; mergeable: boolean; unchanged?: boolean }> => {
    const response = await workspaceClient.getThreadChanges(
      {
        threadId,
        ...(known ? { head: known.head, baseHead: known.baseHead } : {}),
      },
      signal ? { signal } : undefined,
    )
    return {
      changes: fromProtoBranchChanges(response.changes),
      mergeable: response.mergeable,
      ...(response.unchanged ? { unchanged: true } : {}),
    }
  },
  getChangedFile: async (
    threadId: string,
    input: { from: string; head: string; path: string; previousPath: string | null },
    signal?: AbortSignal,
  ): Promise<{ file: FileVersions }> => {
    const response = await workspaceClient.getChangedFile(
      {
        threadId,
        from: input.from,
        head: input.head,
        path: input.path,
        ...(input.previousPath ? { previousPath: input.previousPath } : {}),
      },
      signal ? { signal } : undefined,
    )
    return { file: fromProtoFileVersions(response.file) }
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
    request<{ plan: PlanView }>(
      'POST',
      `/api/projects/${projectId}/plan/proposals/${proposalId}/dismiss`,
    ),
  linkIssue: (projectId: string, nodeId: string, number: number) =>
    request<{ plan: PlanView }>('POST', `/api/projects/${projectId}/plan/nodes/${nodeId}/issues`, {
      number,
    }),
  unlinkIssue: (projectId: string, nodeId: string, number: number) =>
    request<{ plan: PlanView }>(
      'DELETE',
      `/api/projects/${projectId}/plan/nodes/${nodeId}/issues/${number}`,
    ),
  getPlan: (projectId: string) =>
    request<{ plan: PlanView }>('GET', `/api/projects/${projectId}/plan`),
  revisePlan: (projectId: string, input: { reason: string; ops: PlanOp[] }) =>
    request<{ plan: PlanView }>('POST', `/api/projects/${projectId}/plan/revise`, input),
  previewPlanUndo: (projectId: string, revisionId: string) =>
    request<{ preview: PlanUndoPreview }>(
      'GET',
      `/api/projects/${projectId}/plan/revisions/${revisionId}/revert`,
    ),
  revertPlan: (projectId: string, revisionId: string, reason: string) =>
    request<{ plan: PlanView }>(
      'POST',
      `/api/projects/${projectId}/plan/revisions/${revisionId}/revert`,
      { reason },
    ),
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
          headers: { authorization: `Bearer ${token() ?? ''}`, 'content-type': 'application/json' },
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
  summarizeLinks: (urls: string[], signal?: AbortSignal) =>
    request<{ links: Record<string, LinkSummary | null> }>('POST', '/api/links', { urls }, signal),
  previewLink: (url: string, signal?: AbortSignal) =>
    request<{ preview: LinkPreview | null }>(
      'GET',
      `/api/links/preview?url=${encodeURIComponent(url)}`,
      undefined,
      signal,
    ),
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
    // Without a known position the server starts at its newest event.
    const from = lastSeq === null ? '' : `afterSeq=${lastSeq}&`
    const query = `${from}token=${encodeURIComponent(token() ?? '')}`
    const current = new EventSource(`/api/events?${query}`)
    source = current
    current.addEventListener('open', () => {
      delay = 500
      onStatus('open')
      onOpen?.()
    })
    current.addEventListener('ariadne', (message) => {
      const event: AnyStoredEvent = JSON.parse((message as MessageEvent<string>).data)
      lastSeq = Math.max(lastSeq ?? 0, event.seq)
      onEvent(event)
    })
    // A running command's output: not part of the log, so it moves nothing forward.
    current.addEventListener('live', (message) => {
      onLive?.(JSON.parse((message as MessageEvent<string>).data) as LiveUpdate)
    })
    // A reply as it is written: not part of the log either.
    current.addEventListener('draft', (message) => {
      onDraft?.(JSON.parse((message as MessageEvent<string>).data) as DraftUpdate)
    })
    current.addEventListener('background', (message) => {
      onBackground?.(JSON.parse((message as MessageEvent<string>).data) as BackgroundUpdate)
    })
    current.addEventListener('error', () => {
      if (stopped) return
      if (current.readyState !== EventSource.CLOSED) {
        onStatus('lost')
        return
      }
      // EventSource hides the status code; ask a lightweight authenticated endpoint whether the token is the reason.
      void api.ping().then(
        () => schedule('lost'),
        (error) =>
          schedule(error instanceof ApiError && error.status === 401 ? 'unauthorized' : 'lost'),
      )
    })
  }

  const schedule = (status: StreamStatus) => {
    onStatus(status)
    if (stopped || status === 'unauthorized') return
    retry = setTimeout(open, delay)
    delay = Math.min(delay * 2, 10_000)
  }

  open()
  return () => {
    stopped = true
    clearTimeout(retry)
    source?.close()
  }
}
