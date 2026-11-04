import type {
  AnyStoredEvent,
  ArtifactKind,
  ArtifactSummary,
  ArtifactVersionView,
  ArtifactView,
  ConversationView,
  MessageView,
  NeedsMeItem,
  PlanOp,
  PlanView,
  ProjectView,
  ThreadView,
  TodoView,
  MemoryView,
  NoticeView,
  StoredFile,
} from '@ariadne/core'
import type { LocalizedMessage } from '@ariadne/i18n'
import type { LocalizedText, SettingsSchema } from '@ariadne/plugin-sdk'

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

export interface ProjectDetail {
  project: ProjectView
  threads: ThreadView[]
  needsMe: NeedsMeItem[]
}

export interface ModelKind {
  kind: string
  title: LocalizedText
  settings: SettingsSchema
}

/** A kind of context source a plugin offers. */
export interface SourceKind extends ModelKind {
  provides: string[]
}

export interface ModelProviderInput {
  id: string
  kind: string
  name: string
  settings: Record<string, unknown>
}

/** A plugin as the server reports it; see `PluginRecord` in the agent package. */
export interface PluginRecord {
  id: string | null
  name: LocalizedText | null
  source: 'builtin' | 'path'
  path: string | null
  status: 'loaded' | 'failed'
  error: string | null
  provides: { models: string[]; contexts: string[] }
}

/** How many turns run at once, plus those kept for answering the user. */
export interface Concurrency {
  slots: number
  reserved: number
}

export interface ModelSettingsInput {
  providers: ModelProviderInput[]
  default: { provider: string; model: string; contextTokens?: number } | null
}

export interface ModelSettings extends ModelSettingsInput {
  kinds: ModelKind[]
  providers: (ModelProviderInput & { secretsSet: string[] })[]
}

export interface ArtifactDetail {
  artifact: ArtifactView
  versions: ArtifactVersionView[]
}

export interface SandboxStatus {
  kind: 'bwrap' | 'unshare' | 'sandbox-exec' | null
  mode: 'auto' | 'off'
  hidden: string[]
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
  stopThread: (threadId: string, reason: string) =>
    request<{ thread: ThreadView }>('POST', `/api/threads/${threadId}/stop`, { reason }),
  resumeThread: (threadId: string) =>
    request<{ thread: ThreadView }>('POST', `/api/threads/${threadId}/resume`),
  undoNotice: (projectId: string, noticeId: string) =>
    request<void>('POST', `/api/projects/${projectId}/notices/${noticeId}/undo`),
  removeMemory: (projectId: string, memoryId: string) =>
    request<void>('DELETE', `/api/projects/${projectId}/memory/${memoryId}`),
  uploadFile: (projectId: string, file: Blob) =>
    request<{ file: StoredFile }>('POST', `/api/projects/${projectId}/files`, file),
  getSandbox: () => request<{ sandbox: SandboxStatus }>('GET', '/api/sandbox'),
  listProjects: () => request<{ projects: ProjectView[]; lastSeq: number }>('GET', '/api/projects'),
  createProject: (name: string) =>
    request<{ project: ProjectView }>('POST', '/api/projects', { name }),
  getProject: (id: string) => request<ProjectDetail>('GET', `/api/projects/${id}`),
  getConversation: (id: string) =>
    request<{
      conversation: ConversationView
      messages: MessageView[]
      notices: NoticeView[]
      todo: TodoView
    }>('GET', `/api/conversations/${id}`),
  stopTurn: (id: string) => request<{ stopped: boolean }>('POST', `/api/conversations/${id}/stop`),
  retryTurn: (id: string) => request<void>('POST', `/api/conversations/${id}/retry`),
  getConcurrency: () => request<Concurrency>('GET', '/api/settings/concurrency'),
  saveConcurrency: (input: Concurrency) =>
    request<Concurrency>('PUT', '/api/settings/concurrency', input),
  getModelSettings: () => request<ModelSettings>('GET', '/api/settings/models'),
  listPlugins: () => request<{ plugins: PluginRecord[] }>('GET', '/api/plugins'),
  addPlugin: (path: string) =>
    request<{ plugins: PluginRecord[] }>('POST', '/api/plugins', { path }),
  removePlugin: (path: string) =>
    request<{ plugins: PluginRecord[] }>('POST', '/api/plugins/remove', { path }),
  saveModelSettings: (input: ModelSettingsInput) =>
    request<ModelSettings>('PUT', '/api/settings/models', input),
  postMessage: (projectId: string, conversationId: string, content: string) =>
    request<{ message: MessageView }>('POST', `/api/projects/${projectId}/messages`, {
      conversationId,
      content,
    }),
  markRead: (conversationId: string, upTo: string) =>
    request<void>('POST', `/api/conversations/${conversationId}/read`, { upTo }),
  answerAttention: (id: string, choice: number) =>
    request<void>('POST', `/api/attentions/${id}/answer`, { choice }),
  sourceKinds: () => request<{ kinds: SourceKind[] }>('GET', '/api/source-kinds'),
  getSource: (projectId: string) =>
    request<{ source: ProjectView['source']; secretsSet: string[] }>(
      'GET',
      `/api/projects/${projectId}/source`,
    ),
  setSource: (
    projectId: string,
    source: { kind: string; settings: Record<string, unknown> } | null,
  ) => request<{ project: ProjectView }>('PUT', `/api/projects/${projectId}/source`, { source }),
  refreshPullRequest: (threadId: string) =>
    request<{ thread: ThreadView }>('POST', `/api/threads/${threadId}/pull-request/refresh`),
  dismissProposal: (projectId: string, proposalId: string) =>
    request<{ plan: PlanView }>(
      'POST',
      `/api/projects/${projectId}/plan/proposals/${proposalId}/dismiss`,
    ),
  getPlan: (projectId: string) =>
    request<{ plan: PlanView }>('GET', `/api/projects/${projectId}/plan`),
  revisePlan: (projectId: string, input: { reason: string; ops: PlanOp[] }) =>
    request<{ plan: PlanView }>('POST', `/api/projects/${projectId}/plan/revise`, input),
  revertPlan: (projectId: string, revisionId: string, reason: string) =>
    request<{ plan: PlanView }>(
      'POST',
      `/api/projects/${projectId}/plan/revisions/${revisionId}/revert`,
      { reason },
    ),
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
    input: { content: string; baseVersion: number; reason: string; title?: string },
  ) => request<ArtifactDetail>('PUT', `/api/artifacts/${id}`, input),
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
