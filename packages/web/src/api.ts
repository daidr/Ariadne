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
  BackgroundUpdate,
  BackgroundView,
  MessageView,
  NeedsMeItem,
  PlanOp,
  PlanUndoPreview,
  PlanView,
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

const TOKEN_KEY = 'ariadne.token'

/**
 * The server prints a link with `#token=…`. Take the token from there once, keep it,
 * and drop it from the address bar so it does not end up in history or screenshots.
 */
export function initToken(): string | null {
  const match = /(?:^|[#&])token=([^&]+)/.exec(location.hash)
  if (match?.[1]) {
    try {
      localStorage.setItem(TOKEN_KEY, decodeURIComponent(match[1]))
    } catch {
      // Storage can be unavailable; the token then lives only for this page.
      sessionToken = decodeURIComponent(match[1])
    }
    history.replaceState(null, '', location.pathname + location.search)
  }
  return token()
}

let sessionToken: string | null = null

function token(): string | null {
  try {
    return localStorage.getItem(TOKEN_KEY) ?? sessionToken
  } catch {
    return sessionToken
  }
}

export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly localized: LocalizedMessage,
  ) {
    super(localized.key)
  }
}

/** A query string of the values given, or nothing when none is. */
function queryString(values: Record<string, string | undefined>): string {
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries(values)) if (value !== undefined) params.set(key, value)
  const text = params.toString()
  return text ? `?${text}` : ''
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { authorization: `Bearer ${token() ?? ''}` }
  // A file goes as it is, with its own type; anything else as JSON.
  const raw = body instanceof Blob
  if (body !== undefined) headers['content-type'] = raw ? body.type : 'application/json'
  const response = await fetch(path, {
    method,
    headers,
    ...(body === undefined ? {} : { body: raw ? body : JSON.stringify(body) }),
  })
  if (response.status === 204) return undefined as T
  const data = await response.json()
  if (!response.ok) throw new ApiError(response.status, data.error)
  return data as T
}

/** One project's threads' worktrees, as the settings list them. */
export interface ProjectWorktrees {
  id: string
  name: string
  worktrees: (WorktreeInfo & { thread: ThreadView | null })[]
  /** Why they could not be listed, e.g. the remote machine was unreachable. */
  error: LocalizedMessage | null
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
  provides: string[]
  /** Threads' commands run on another machine, outside this one's sandbox. */
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
  provides: {
    models: string[]
    contexts: string[]
    dashboards: string[]
    searches: string[]
    links: string[]
  }
}

/** How many turns run at once, plus those kept for answering the user. */
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
  keySet: boolean
  needed: boolean
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

export type Effort = 'low' | 'medium' | 'high'

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

/** What the settings say about the sandbox; it takes effect when Ariadne starts. */
export interface SandboxConfig {
  mode: 'auto' | 'off'
  hide: string[]
}

export interface SandboxStatus {
  kind: 'bwrap' | 'unshare' | 'sandbox-exec' | null
  mode: 'auto' | 'off'
  hidden: string[]
  platform: string
}

export const api = {
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
  reviewThread: (threadId: string) =>
    request<{ thread: ThreadView }>('POST', `/api/threads/${threadId}/reviewed`),
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
  getSandbox: () =>
    request<{ sandbox: SandboxStatus; configured: SandboxConfig | null }>('GET', '/api/sandbox'),
  saveSandbox: (input: SandboxConfig) =>
    request<{ configured: SandboxConfig }>('PUT', '/api/sandbox', input),
  listProjects: () => request<{ projects: ProjectView[]; lastSeq: number }>('GET', '/api/projects'),
  createProject: (name: string) =>
    request<{ project: ProjectView }>('POST', '/api/projects', { name }),
  deleteProject: (id: string) => request<void>('DELETE', `/api/projects/${id}`),
  /** With `since` (a `sync.cursor` given before), only the threads changed after it. */
  getProject: (id: string, since?: string) =>
    request<ProjectDetail & { sync: ThreadSync }>(
      'GET',
      `/api/projects/${id}${since ? `?since=${encodeURIComponent(since)}` : ''}`,
    ),
  /**
   * The newest messages, the page before `before`, or everything from `from` on. With
   * `since` (a `lastSeq` given before), only messages and notices changed after it.
   */
  getConversation: (id: string, cursor: { before?: string; from?: string; since?: number } = {}) =>
    request<{
      conversation: ConversationView
      messages: MessageView[]
      notices: NoticeView[]
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
  getTrace: (id: string, before?: number) =>
    request<{ turns: TraceTurn[]; more: boolean }>(
      'GET',
      `/api/conversations/${id}/trace${before === undefined ? '' : `?before=${before}`}`,
    ),
  getTraceRequest: (id: string, seq: number) =>
    request<{ request: TraceRequestDetail }>(
      'GET',
      `/api/conversations/${id}/trace/requests/${seq}`,
    ),
  setThreadModel: (threadId: string, model: string | null) =>
    request<{ thread: ThreadView }>('PUT', `/api/threads/${threadId}/model`, { model }),
  stopTurn: (id: string) => request<{ stopped: boolean }>('POST', `/api/conversations/${id}/stop`),
  wakeNow: (id: string) => request<void>('POST', `/api/conversations/${id}/wait/now`),
  cancelWait: (id: string) => request<void>('DELETE', `/api/conversations/${id}/wait`),
  commandOutput: (threadId: string, commandId: string) =>
    request<{ command: BackgroundView; tail: string }>(
      'GET',
      `/api/threads/${threadId}/commands/${commandId}`,
    ),
  stopCommand: (threadId: string, commandId: string) =>
    request<{ command: BackgroundView }>(
      'POST',
      `/api/threads/${threadId}/commands/${commandId}/stop`,
    ),
  retryTurn: (id: string) => request<void>('POST', `/api/conversations/${id}/retry`),
  getConcurrency: () => request<Concurrency>('GET', '/api/settings/concurrency'),
  saveConcurrency: (input: Concurrency) =>
    request<Concurrency>('PUT', '/api/settings/concurrency', input),
  getInputSettings: () => request<InputSettings>('GET', '/api/settings/input'),
  saveInputSettings: (input: InputSettings) =>
    request<InputSettings>('PUT', '/api/settings/input', input),
  getModelSettings: () => request<ModelSettings>('GET', '/api/settings/models'),
  getSearchSettings: () => request<SearchSettings>('GET', '/api/settings/search'),
  /** `null` clears the stored key. */
  saveSearchSettings: (input: { apiKey: string | null }) =>
    request<SearchSettings>('PUT', '/api/settings/search', input),
  listPlugins: () => request<{ plugins: PluginRecord[] }>('GET', '/api/plugins'),
  addPlugin: (path: string) =>
    request<{ plugins: PluginRecord[] }>('POST', '/api/plugins', { path }),
  removePlugin: (path: string) =>
    request<{ plugins: PluginRecord[] }>('POST', '/api/plugins/remove', { path }),
  lookupModelPrice: (model: string, hint: string) =>
    request<{ price: ModelPrice; source: string }>(
      'GET',
      `/api/settings/model-price?${new URLSearchParams({ model, hint })}`,
    ),
  saveModelSettings: (input: ModelSettingsInput) =>
    request<ModelSettings>('PUT', '/api/settings/models', input),
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
      headers: { authorization: `Bearer ${token() ?? ''}`, 'content-type': 'application/json' },
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
  sourceKinds: () => request<{ kinds: SourceKind[] }>('GET', '/api/source-kinds'),
  browseSource: (
    kind: string,
    body: { projectId: string | null; settings: Record<string, unknown>; path: string },
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
  getSource: (projectId: string) =>
    request<{ source: ProjectView['source']; secretsSet: string[] }>(
      'GET',
      `/api/projects/${projectId}/source`,
    ),
  setSource: (projectId: string, source: SourceInput | null) =>
    request<{ project: ProjectView }>('PUT', `/api/projects/${projectId}/source`, { source }),
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
  getDataUsage: () => request<DataUsage>('GET', '/api/settings/data'),
  backupData: () => request<{ backup: BackupInfo }>('POST', '/api/settings/data/backups'),
  removeBackup: (name: string) =>
    request<void>('DELETE', `/api/settings/data/backups/${encodeURIComponent(name)}`),
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
  exportProject: async (projectId: string) => {
    const response = await fetch(`/api/projects/${projectId}/export`, {
      headers: { authorization: `Bearer ${token() ?? ''}` },
    })
    if (!response.ok) throw new ApiError(response.status, (await response.json()).error)
    const name = /filename="([^"]+)"/.exec(response.headers.get('content-disposition') ?? '')?.[1]
    return { blob: await response.blob(), name: name ?? `ariadne-${projectId}.json` }
  },
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
  getChanges: (threadId: string) =>
    request<{ changes: BranchChanges | null; mergeable: boolean }>(
      'GET',
      `/api/threads/${threadId}/changes`,
    ),
  getChangedFile: (
    threadId: string,
    input: { from: string; head: string; path: string; previousPath: string | null },
  ) => {
    const query = new URLSearchParams({
      from: input.from,
      head: input.head,
      path: input.path,
      ...(input.previousPath ? { previousPath: input.previousPath } : {}),
    })
    return request<{ file: FileVersions }>('GET', `/api/threads/${threadId}/changes/file?${query}`)
  },
  mergeThread: (threadId: string, input: { method: MergeMethod; message: string }) =>
    request<{ outcome: MergeOutcome; thread: ThreadView }>(
      'POST',
      `/api/threads/${threadId}/merge`,
      input,
    ),
  listWorktrees: () => request<{ projects: ProjectWorktrees[] }>('GET', '/api/worktrees'),
  removeWorktree: (projectId: string, threadId: string) =>
    request<void>('DELETE', `/api/projects/${projectId}/worktrees/${threadId}`),
  refreshPullRequest: (threadId: string) =>
    request<{ thread: ThreadView }>('POST', `/api/threads/${threadId}/pull-request/refresh`),
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
  getArtifact: (id: string, version?: number) =>
    request<ArtifactDetail>(
      'GET',
      `/api/artifacts/${id}${version === undefined ? '' : `?version=${version}`}`,
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
  summarizeLinks: (urls: string[]) =>
    request<{ links: Record<string, LinkSummary | null> }>('POST', '/api/links', { urls }),
  previewLink: (url: string) =>
    request<{ preview: LinkPreview | null }>(
      'GET',
      `/api/links/preview?url=${encodeURIComponent(url)}`,
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
      // EventSource hides the status code; ask a plain endpoint whether the token is the reason.
      void api.listProjects().then(
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
