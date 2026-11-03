import type {
  AnyStoredEvent,
  MessageView,
  NeedsMeItem,
  ProjectView,
  ThreadView,
  TodoView,
} from '@ariadne/core'
import type { LocalizedMessage } from '@ariadne/i18n'

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
  if (body !== undefined) headers['content-type'] = 'application/json'
  const response = await fetch(path, {
    method,
    headers,
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
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

export const api = {
  listProjects: () => request<{ projects: ProjectView[]; lastSeq: number }>('GET', '/api/projects'),
  createProject: (name: string) =>
    request<{ project: ProjectView }>('POST', '/api/projects', { name }),
  getProject: (id: string) => request<ProjectDetail>('GET', `/api/projects/${id}`),
  getConversation: (id: string) =>
    request<{ messages: MessageView[]; todo: TodoView }>('GET', `/api/conversations/${id}`),
  postMessage: (projectId: string, conversationId: string, content: string) =>
    request<{ message: MessageView }>('POST', `/api/projects/${projectId}/messages`, {
      conversationId,
      content,
    }),
  markRead: (conversationId: string, upTo: string) =>
    request<void>('POST', `/api/conversations/${conversationId}/read`, { upTo }),
  answerAttention: (id: string, choice: number) =>
    request<void>('POST', `/api/attentions/${id}/answer`, { choice }),
}

export type StreamStatus = 'connecting' | 'open' | 'lost' | 'unauthorized'

/**
 * Follow the event log from `afterSeq`. EventSource retries network drops on its own but
 * gives up for good on a non-200 answer (a 401, or a 502 while the dev server restarts),
 * so a closed source is reopened here with backoff, resuming after the last event seen.
 */
export function followEvents(
  afterSeq: number,
  onEvent: (event: AnyStoredEvent) => void,
  onStatus: (status: StreamStatus) => void,
): () => void {
  let lastSeq = afterSeq
  let source: EventSource | null = null
  let retry: ReturnType<typeof setTimeout> | undefined
  let delay = 500
  let stopped = false

  const open = () => {
    const query = `afterSeq=${lastSeq}&token=${encodeURIComponent(token() ?? '')}`
    const current = new EventSource(`/api/events?${query}`)
    source = current
    current.addEventListener('open', () => {
      delay = 500
      onStatus('open')
    })
    current.addEventListener('ariadne', (message) => {
      const event: AnyStoredEvent = JSON.parse((message as MessageEvent<string>).data)
      lastSeq = Math.max(lastSeq, event.seq)
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
